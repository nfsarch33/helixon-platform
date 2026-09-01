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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func liveEngramURL(t *testing.T) string {
	t.Helper()
	baseURL := os.Getenv("ENGRAM_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8280"
	}
	probe := &http.Client{Timeout: 2 * time.Second}
	resp, err := probe.Get(baseURL + "/healthz") //nolint:gosec // G704 ENGRAM_URL is operator test config; defaults to loopback
	if err != nil {
		t.Skipf("engram daemon not reachable at %s: %v", baseURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Skipf("engram daemon at %s unhealthy: status %d", baseURL, resp.StatusCode)
	}
	return baseURL
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
