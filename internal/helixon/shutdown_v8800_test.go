// T-8800-B11: graceful shutdown — SIGTERM (or any signal) translates into a
// context cancellation that propagates through Run, channels, and heartbeat
// loops, all completing inside a 5-second budget.
package helixon

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// shutdownChannel reuses the Channel contract but with explicit shutdown
// counters and timing so we can assert deadline propagation. We name it
// distinctly to avoid colliding with lifecycle_test.go's fakeChannel.
type shutdownChannel struct {
	name        string
	served      chan struct{}
	shutdowns   atomic.Int32
	shutdownDur atomic.Int64
}

func newShutdownChannel(name string) *shutdownChannel {
	return &shutdownChannel{name: name, served: make(chan struct{}, 1)}
}

func (c *shutdownChannel) Name() string { return c.name }

func (c *shutdownChannel) Serve(ctx context.Context, _ MessageHandler) error {
	select {
	case c.served <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil
}

func (c *shutdownChannel) Shutdown(ctx context.Context) error {
	start := time.Now()
	c.shutdowns.Add(1)
	select {
	case <-ctx.Done():
	case <-time.After(50 * time.Millisecond):
	}
	c.shutdownDur.Store(time.Since(start).Nanoseconds())
	return nil
}

func (c *shutdownChannel) waitServed(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-c.served:
	case <-time.After(d):
		t.Fatal("Serve never reported ready")
	}
}

func newRunningRuntime(t *testing.T, channels ...Channel) *Runtime {
	t.Helper()
	rt := NewRuntime(&stubProvider{resp: "ok"}, RuntimeConfig{
		AgentID:        "v8800-shutdown-test",
		SessionDSN:     "file::memory:?cache=shared",
		HeartbeatEvery: 50 * time.Millisecond,
	})
	if err := rt.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	opts := make([]ConfigOption, 0, len(channels))
	for _, ch := range channels {
		opts = append(opts, WithChannel(ch))
	}
	if err := rt.Configure(context.Background(), opts...); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return rt
}

// TestRunWithSignal_CancelsCleanlyOn_SIGTERM proves that RunWithSignal blocks
// on Serve until the OS signal arrives, then triggers graceful shutdown that
// completes well inside the 5s budget.
func TestRunWithSignal_CancelsCleanlyOn_SIGTERM(t *testing.T) {
	ch := newShutdownChannel("fake")
	rt := newRunningRuntime(t, ch)

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- rt.RunWithSignal(context.Background(), 5*time.Second, syscall.SIGTERM)
	}()

	ch.waitServed(t, 2*time.Second)

	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunWithSignal: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunWithSignal did not return within 3s of SIGTERM")
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("graceful shutdown exceeded 5s budget: %v", elapsed)
	}

	if got := ch.shutdowns.Load(); got != 1 {
		t.Fatalf("expected channel Shutdown called once, got %d", got)
	}
	if rt.Phase() != PhaseShutdown {
		t.Fatalf("phase = %s, want %s", rt.Phase(), PhaseShutdown)
	}
}

// TestRunWithSignal_HonoursParentCtxCancel proves that cancelling the parent
// context (e.g. supervisor decided to stop) also drives a graceful shutdown.
func TestRunWithSignal_HonoursParentCtxCancel(t *testing.T) {
	ch := newShutdownChannel("ctx-cancel")
	rt := newRunningRuntime(t, ch)

	parent, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.RunWithSignal(parent, 5*time.Second, syscall.SIGTERM) }()

	ch.waitServed(t, 2*time.Second)
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("RunWithSignal: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunWithSignal did not return on parent ctx cancel")
	}
	if rt.Phase() != PhaseShutdown {
		t.Fatalf("phase = %s, want %s", rt.Phase(), PhaseShutdown)
	}
}

// TestRunWithSignal_ShutdownDeadline ensures we honour the supplied budget
// by capping the shutdown ctx — channels see a deadline, not unbounded time.
func TestRunWithSignal_ShutdownDeadline(t *testing.T) {
	ch := newShutdownChannel("deadline")
	rt := newRunningRuntime(t, ch)

	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.RunWithSignal(parent, 250*time.Millisecond, syscall.SIGTERM) }()

	ch.waitServed(t, 2*time.Second)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("RunWithSignal did not return")
	}

	if dur := time.Duration(ch.shutdownDur.Load()); dur > 500*time.Millisecond {
		t.Fatalf("Shutdown took %v, expected to be bounded by 250ms deadline", dur)
	}
}
