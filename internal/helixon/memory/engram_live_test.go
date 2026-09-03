package memory

// Live integration test against a running Engram daemon (canonical v2
// httpapi, wsl1 native :8280). Gated on reachability: skipped unless the
// daemon answers GET /healthz at ENGRAM_URL (default http://127.0.0.1:8280).
//
// This exists because the httptest stubs can drift from the real wire
// schema: the client used to send mem0-style {role,content} message
// objects, which the live daemon rejects with 400 "invalid JSON" (it
// expects messages as plain strings and answers with a record array).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveRequiredEnv turns "daemon unreachable" from a skip into a failure when
// set to any non-empty value. CI on the runner that shares a host with the
// daemon sets it: a silently skipped roundtrip would otherwise read as green,
// which is exactly how a wire-schema drift stays invisible until an agent
// hits it in production.
const liveRequiredEnv = "HLXN_ENGRAM_LIVE_REQUIRED"

func liveEngramURL(t *testing.T) string {
	t.Helper()
	baseURL := os.Getenv("ENGRAM_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8280"
	}
	skip, fatal := liveGate(baseURL, liveRequired())
	if fatal != "" {
		t.Fatalf("%s is set: %s", liveRequiredEnv, fatal)
	}
	if skip != "" {
		t.Skip(skip)
	}
	return baseURL
}

// liveGate decides what the roundtrip does before it starts:
//   - daemon unreachable or /healthz not 200: fail when required, else skip;
//   - daemon reachable but reporting ITSELF degraded (its /metrics.json health
//     verdict, e.g. an embedder that is not answering): skip even when
//     required, quoting the daemon's reason. Add blocks on the embedder, so
//     the roundtrip cannot tell a wire-schema drift from a starved embedder,
//     and a starved embedder is the daemon's own health signal to raise, not
//     this gate's. Measured on a loaded host: primary embedder timing out at
//     its 30s budget and falling through, then the fallback timing out too.
//   - healthy: run.
func liveGate(baseURL string, required bool) (skip, fatal string) {
	if reason := liveProbe(baseURL); reason != "" {
		if required {
			return "", reason
		}
		return reason, ""
	}
	if reason := daemonDegraded(baseURL); reason != "" {
		return "engram daemon reachable but degraded; roundtrip skipped: " + reason, ""
	}
	return "", ""
}

// daemonDegraded returns "" when the daemon's own health verdict is ok, else
// the verdict with every non-ok subsystem. The verdict endpoint runs the
// daemon's subsystem probes (including one embedder call), so the budget
// covers a slow embedder; a verdict that cannot be obtained is itself a
// degraded state.
func daemonDegraded(baseURL string) string {
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Get(baseURL + "/metrics.json") //nolint:gosec // G704 ENGRAM_URL is operator test config; defaults to loopback
	if err != nil {
		return "health verdict unavailable: " + err.Error()
	}
	defer resp.Body.Close() //nolint:errcheck
	var verdict struct {
		Status     string            `json:"status"`
		Subsystems map[string]string `json:"subsystems"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&verdict); err != nil {
		return "health verdict undecodable: " + err.Error()
	}
	if verdict.Status == "ok" {
		return ""
	}
	bad := make([]string, 0, len(verdict.Subsystems))
	for name, state := range verdict.Subsystems {
		if state != "ok" {
			bad = append(bad, name+"="+state)
		}
	}
	sort.Strings(bad)
	return fmt.Sprintf("status=%s %s", verdict.Status, strings.Join(bad, " "))
}

// liveRequired reports whether an unreachable daemon must fail the test
// instead of skipping it.
func liveRequired() bool { return os.Getenv(liveRequiredEnv) != "" }

// liveProbe returns "" when the daemon answers GET /healthz with 200, else
// the reason to put in the skip or failure message.
func liveProbe(baseURL string) string {
	probe := &http.Client{Timeout: 2 * time.Second}
	resp, err := probe.Get(baseURL + "/healthz") //nolint:gosec // G704 ENGRAM_URL is operator test config; defaults to loopback
	if err != nil {
		return fmt.Sprintf("engram daemon not reachable at %s: %v", baseURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("engram daemon at %s unhealthy: status %d", baseURL, resp.StatusCode)
	}
	return ""
}

// TestLiveProbe_ReportsWhyItCannotRun pins the gate's halves: a healthy
// daemon yields no reason; a non-200 /healthz and a closed port each yield
// one, so the skip/failure message always says what was wrong.
func TestLiveProbe_ReportsWhyItCannotRun(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()
	assert.Empty(t, liveProbe(healthy.URL))

	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer sick.Close()
	assert.Contains(t, liveProbe(sick.URL), "unhealthy")

	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	assert.Contains(t, liveProbe(closedURL), "not reachable")
}

// TestLiveRequired_ReadsTheEnv: the env var, and only the env var, decides
// whether an unreachable daemon is a skip or a failure.
func TestLiveRequired_ReadsTheEnv(t *testing.T) {
	t.Setenv(liveRequiredEnv, "")
	assert.False(t, liveRequired())
	t.Setenv(liveRequiredEnv, "1")
	assert.True(t, liveRequired())
}

// TestEngramClient_LiveAddGetDelete round-trips a memory through the real
// daemon: Health, Add, Get by returned ID, Delete, and not-found after
// delete. Content is unique per run and the record is removed via the
// daemon (never by touching engram.db directly).
func TestEngramClient_LiveAddGetDelete(t *testing.T) {
	baseURL := liveEngramURL(t)
	// The budget covers the daemon's own per-request timeout (30s) for each
	// of Add and Delete, not a latency expectation: Add blocks on the
	// embedder, and on a loaded host the primary embedder has been measured
	// timing out and falling through, so one Add took 27s. This test checks
	// the wire schema; with the gate required in CI, a tight budget would
	// turn host load into a false red.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := NewEngramClient(EngramConfig{BaseURL: baseURL}, nil)
	require.NoError(t, client.Health(ctx), "Health must pass once /healthz probe succeeded")

	content := fmt.Sprintf("helixon-platform live integration probe %d", time.Now().UnixNano())
	mem, err := client.Add(ctx, content, "helixon-it", "it-user", "")
	require.NoError(t, err, "Add must match the live daemon schema")
	require.NotNil(t, mem)
	require.NotEmpty(t, mem.ID, "daemon must return the created record id")
	assert.Equal(t, content, mem.Content)

	// Best-effort cleanup even if an assertion below fails; the explicit
	// Delete later makes this a no-op (ErrMemoryNotFound).
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := client.Delete(cleanupCtx, mem.ID); err != nil && !errors.Is(err, ErrMemoryNotFound) {
			t.Logf("cleanup: delete %s failed: %v", mem.ID, err)
		}
	})

	got, err := client.Get(ctx, mem.ID)
	require.NoError(t, err)
	assert.Equal(t, mem.ID, got.ID)
	assert.Equal(t, content, got.Content)

	require.NoError(t, client.Delete(ctx, mem.ID))
	_, err = client.Get(ctx, mem.ID)
	require.ErrorIs(t, err, ErrMemoryNotFound, "record must be gone after delete")
}

// fakeDaemon serves /healthz and a /metrics.json verdict for the gate tests.
func fakeDaemon(t *testing.T, healthz int, verdict string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(healthz)
		case "/metrics.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(verdict))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestLiveGate_UnreachableFailsOnlyWhenRequired: the required flag decides
// between skip and failure for a daemon that does not answer at all.
func TestLiveGate_UnreachableFailsOnlyWhenRequired(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()

	skip, fatal := liveGate(url, true)
	assert.Empty(t, skip)
	assert.Contains(t, fatal, "not reachable")

	skip, fatal = liveGate(url, false)
	assert.Contains(t, skip, "not reachable")
	assert.Empty(t, fatal)
}

// TestLiveGate_DegradedDaemonSkipsEvenWhenRequired: a daemon that reports
// its embedder down is a skip with the daemon's reason, never a failure -
// the roundtrip would only be measuring the embedder.
func TestLiveGate_DegradedDaemonSkipsEvenWhenRequired(t *testing.T) {
	srv := fakeDaemon(t, http.StatusOK,
		`{"memory_count":6,"status":"degraded","subsystems":{"embedder":"error: context deadline exceeded","history_store":"ok","vector_store":"ok"}}`)
	skip, fatal := liveGate(srv.URL, true)
	assert.Empty(t, fatal)
	assert.Contains(t, skip, "degraded")
	assert.Contains(t, skip, "embedder=error: context deadline exceeded")
	assert.NotContains(t, skip, "history_store", "only non-ok subsystems are quoted")
}

// TestLiveGate_HealthyRuns: a healthy verdict lets the roundtrip run.
func TestLiveGate_HealthyRuns(t *testing.T) {
	srv := fakeDaemon(t, http.StatusOK, `{"memory_count":6,"status":"ok","subsystems":{"embedder":"ok","history_store":"ok","vector_store":"ok"}}`)
	skip, fatal := liveGate(srv.URL, true)
	assert.Empty(t, skip)
	assert.Empty(t, fatal)
}

// TestLiveGate_UndecodableVerdictIsDegraded: a daemon that answers /healthz
// but cannot produce a verdict is not trusted to run the roundtrip.
func TestLiveGate_UndecodableVerdictIsDegraded(t *testing.T) {
	srv := fakeDaemon(t, http.StatusOK, `not json`)
	skip, fatal := liveGate(srv.URL, true)
	assert.Empty(t, fatal)
	assert.Contains(t, skip, "undecodable")
}
