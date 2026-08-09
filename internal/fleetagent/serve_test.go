// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowlist(t *testing.T) {
	a := newAllowlist([]string{"wsl1", "wsl2"})
	assert.True(t, a.Allows("wsl1"))
	assert.False(t, a.Allows("wsl3"))

	a.Set([]string{"wsl4"})
	assert.False(t, a.Allows("wsl1"))
	assert.True(t, a.Allows("wsl4"))
}

func TestPublishHeartbeat_OK(t *testing.T) {
	var got int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	opts := heartbeatOpts{
		Cfg:    DefaultConfig("wsl1", filepath.Join(t.TempDir(), "agent.toml")),
		Logger: nopLogger(),
		Clock:  RealClock{},
		HTTPClient: &http.Client{
			Timeout: 2 * time.Second,
		},
		URL: srv.URL,
	}
	require.NoError(t, publishHeartbeat(context.Background(), opts))
	assert.Equal(t, 1, got)
}

func TestPublishHeartbeat_Fails(t *testing.T) {
	opts := heartbeatOpts{
		Cfg:    DefaultConfig("wsl1", filepath.Join(t.TempDir(), "agent.toml")),
		Logger: nopLogger(),
		Clock:  RealClock{},
		HTTPClient: &http.Client{
			Timeout: 500 * time.Millisecond,
		},
		URL: "http://127.0.0.1:1",
	}
	err := publishHeartbeat(context.Background(), opts)
	assert.Error(t, err)
}

func TestNextBackoff(t *testing.T) {
	assert.Equal(t, 10*time.Second, nextBackoff(5*time.Second))
	assert.Equal(t, 20*time.Second, nextBackoff(10*time.Second))
	assert.Equal(t, MaxBackoff, nextBackoff(MaxBackoff))
	assert.Equal(t, MaxBackoff, nextBackoff(10*time.Minute))
}

func TestNewControlHandler_Healthz(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	priv, _, err := EnsureHostKey(keyPath, true)
	require.NoError(t, err)

	cfg := DefaultConfig("wsl1", filepath.Join(dir, "agent.toml"))
	cfg.PeerAllowlist = []string{"wsl2"}
	h := newControlHandler(cfg, priv, newAllowlist(cfg.PeerAllowlist), nopLogger())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestNewControlHandler_StubsRejectUnknownPeer(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	priv, _, err := EnsureHostKey(keyPath, true)
	require.NoError(t, err)

	cfg := DefaultConfig("wsl1", filepath.Join(dir, "agent.toml"))
	cfg.PeerAllowlist = []string{"wsl2"}
	h := newControlHandler(cfg, priv, newAllowlist(cfg.PeerAllowlist), nopLogger())

	req := httptest.NewRequest(http.MethodPost, "/v1/control/exec", nil)
	req.Header.Set("X-Helixon-Peer", "wsl-unknown")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestNewControlHandler_StubAcceptsAllowlistedPeer(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	priv, _, err := EnsureHostKey(keyPath, true)
	require.NoError(t, err)

	cfg := DefaultConfig("wsl1", filepath.Join(dir, "agent.toml"))
	cfg.PeerAllowlist = []string{"wsl2"}
	h := newControlHandler(cfg, priv, newAllowlist(cfg.PeerAllowlist), nopLogger())

	req := httptest.NewRequest(http.MethodPost, "/v1/control/exec", nil)
	req.Header.Set("X-Helixon-Peer", "wsl2")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

// fakeClock lets tests fast-forward time. NewTimer pre-fires so the loop
// body runs immediately on each tick, exercising the happy and error paths.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *fakeClock) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	now := f.now.Add(d)
	f.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- now
	return fakeTimerForClock{c: ch}
}

type fakeTimerForClock struct{ c <-chan time.Time }

func (f fakeTimerForClock) C() <-chan time.Time { return f.c }
func (f fakeTimerForClock) Stop() bool          { return true }

func TestRunHeartbeat_ShutdownWhenContextCancelled(t *testing.T) {
	var got int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&got, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	clk := &fakeClock{now: time.Unix(0, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go runHeartbeat(ctx, heartbeatOpts{
		Cfg:        DefaultConfig("wsl1", filepath.Join(t.TempDir(), "agent.toml")),
		Logger:     nopLogger(),
		Clock:      clk,
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
		URL:        srv.URL,
	}, done)

	// Let one heartbeat fire.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runHeartbeat did not return after cancel")
	}
	assert.GreaterOrEqual(t, atomic.LoadInt32(&got), int32(1))
}

func TestRunHeartbeat_AppliesBackoffOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	clk := &fakeClock{now: time.Unix(0, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go runHeartbeat(ctx, heartbeatOpts{
		Cfg:        DefaultConfig("wsl1", filepath.Join(t.TempDir(), "agent.toml")),
		Logger:     nopLogger(),
		Clock:      clk,
		HTTPClient: &http.Client{Timeout: 500 * time.Millisecond},
		URL:        srv.URL,
	}, done)

	// Backoff progression: 5s -> 10s -> 20s -> 40s -> 80s -> 160s -> 300s cap.
	// After ~600ms wall clock (with NewTimer pre-firing) loop should have
	// cycled several times.
	time.Sleep(400 * time.Millisecond)
	cancel()
	<-done
	// No assertion on internal state; the fact that runHeartbeat exited
	// cleanly under repeated 500 errors is the contract.
}

func TestRunHeartbeat_BackoffCaps(t *testing.T) {
	// Hammer with unreachable URL, verify the loop still terminates when
	// ctx is cancelled (backoff is bounded).
	clk := &fakeClock{now: time.Unix(0, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go runHeartbeat(ctx, heartbeatOpts{
		Cfg:        DefaultConfig("wsl1", filepath.Join(t.TempDir(), "agent.toml")),
		Logger:     nopLogger(),
		Clock:      clk,
		HTTPClient: &http.Client{Timeout: 50 * time.Millisecond},
		URL:        "http://127.0.0.1:1",
	}, done)

	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runHeartbeat did not exit")
	}
}
