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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
	if reason := liveProbe(baseURL); reason != "" {
		if liveRequired() {
			t.Fatalf("%s is set: %s", liveRequiredEnv, reason)
		}
		t.Skip(reason)
	}
	return baseURL
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
