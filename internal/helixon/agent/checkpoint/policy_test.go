package checkpoint

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeFlusher struct {
	count  atomic.Int64
	failOn atomic.Int64
}

func (f *fakeFlusher) Flush(ctx context.Context) error { //nolint:revive // unused-parameter required by interface
	n := f.count.Add(1)
	if f.failOn.Load() != 0 && n == f.failOn.Load() {
		return errors.New("simulated flush failure")
	}
	return nil
}

func TestFlushPolicy_DefaultEveryN(t *testing.T) {
	p := New(&fakeFlusher{}, 0, nil)
	if p.everyN != DefaultEveryN {
		t.Fatalf("everyN: got %d want %d", p.everyN, DefaultEveryN)
	}
}

func TestFlushPolicy_FiresEveryN(t *testing.T) {
	f := &fakeFlusher{}
	p := New(f, 3, nil)
	ctx := context.Background()
	for i := 1; i <= 9; i++ {
		_ = p.OnStep(ctx)
	}
	if got := f.count.Load(); got != 3 {
		t.Fatalf("flush count after 9 steps: got %d want 3", got)
	}
}

func TestFlushPolicy_ForceNow(t *testing.T) {
	f := &fakeFlusher{}
	p := New(f, 100, nil)
	if err := p.ForceNow(context.Background()); err != nil {
		t.Fatalf("ForceNow: %v", err)
	}
	if f.count.Load() != 1 {
		t.Fatalf("ForceNow flush count: got %d want 1", f.count.Load())
	}
}

func TestFlushPolicy_PropagatesError(t *testing.T) {
	f := &fakeFlusher{}
	f.failOn.Store(1)
	p := New(f, 1, nil)
	err := p.OnStep(context.Background())
	if err == nil {
		t.Fatal("OnStep should propagate flush error")
	}
}

func TestFlushPolicy_Stats(t *testing.T) {
	p := New(&fakeFlusher{}, 2, nil)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		_ = p.OnStep(ctx)
	}
	steps, flushes := p.Stats()
	if steps != 4 {
		t.Fatalf("steps: got %d want 4", steps)
	}
	if flushes != 2 {
		t.Fatalf("flushes: got %d want 2", flushes)
	}
}

func TestFlushPolicy_ComposeWith(t *testing.T) {
	f := &fakeFlusher{}
	p := New(f, 1000, nil)
	fl := p.ComposeWith()
	if err := fl.Flush(context.Background()); err != nil {
		t.Fatalf("composed Flush: %v", err)
	}
	if f.count.Load() != 1 {
		t.Fatalf("composed flush count: got %d want 1", f.count.Load())
	}
}

// TestFlushPolicy_ConcurrentOnStep: many concurrent OnStep calls
// must not double-flush or skip flushes beyond the cadence.
func TestFlushPolicy_ConcurrentOnStep(t *testing.T) {
	f := &fakeFlusher{}
	p := New(f, 10, nil)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.OnStep(context.Background())
		}()
	}
	wg.Wait()
	steps, flushes := p.Stats()
	if steps != 100 {
		t.Fatalf("steps: got %d want 100", steps)
	}
	// 100 steps / everyN=10 = 10 flushes (exact).
	if flushes != 10 {
		t.Fatalf("flushes: got %d want 10 (one per cadence boundary)", flushes)
	}
}

// ensure time import is used (to keep the import set stable across refactors).
var _ = time.Second
