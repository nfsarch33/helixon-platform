package evolver_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/evolver"
)

// fakeSource implements evolver.Source for tests.
type fakeSource struct {
	name      string
	obs       []evolver.Observation
	err       error
	calls     int32
	failFirst int // number of attempts to fail before succeeding
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Observe(ctx context.Context) ([]evolver.Observation, error) {
	n := atomic.AddInt32(&f.calls, 1)
	if int(n) <= f.failFirst {
		return nil, errors.New("simulated transient failure")
	}
	return f.obs, f.err
}

// fakeDistill implements evolver.Distill.
type fakeDistill struct {
	cands []evolver.Candidate
	err   error
}

func (d *fakeDistill) Reflect(ctx context.Context, in []evolver.Observation) ([]evolver.Candidate, error) {
	return d.cands, d.err
}

// fakePromote implements evolver.Promote.
type fakePromote struct {
	mu        sync.Mutex
	persisted []evolver.Candidate
	failOn    map[string]bool // Candidate.ID → fail Persist?
	calls     int32
}

func (p *fakePromote) Persist(ctx context.Context, c evolver.Candidate) error {
	atomic.AddInt32(&p.calls, 1)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failOn[c.ID] {
		return errors.New("simulated persist failure")
	}
	p.persisted = append(p.persisted, c)
	return nil
}

// Test1_RequiresSourceDistillPromote verifies the constructor fails
// fast when any of the three required interfaces is nil.
func Test1_RequiresSourceDistillPromote(t *testing.T) {
	if _, err := evolver.NewEvolver(nil, &fakeDistill{}, &fakePromote{}); !errors.Is(err, evolver.ErrNoSubsystems) {
		t.Errorf("nil source: expected ErrNoSubsystems, got %v", err)
	}
	if _, err := evolver.NewEvolver(&fakeSource{}, nil, &fakePromote{}); !errors.Is(err, evolver.ErrNoSubsystems) {
		t.Errorf("nil distill: expected ErrNoSubsystems, got %v", err)
	}
	if _, err := evolver.NewEvolver(&fakeSource{}, &fakeDistill{}, nil); !errors.Is(err, evolver.ErrNoSubsystems) {
		t.Errorf("nil promote: expected ErrNoSubsystems, got %v", err)
	}
}

// Test2_HappyPath_PromotesAllCandidates exercises the canonical
// Source → Distill → Promote flow with no retries and no dedupe.
func Test2_HappyPath_PromotesAllCandidates(t *testing.T) {
	src := &fakeSource{name: "agentrace", obs: []evolver.Observation{{Source: "agentrace", Key: "k1", Payload: "p"}}}
	dist := &fakeDistill{cands: []evolver.Candidate{{ID: "c1", Source: "agentrace", Title: "t1"}, {ID: "c2", Source: "agentrace", Title: "t2"}}}
	pro := &fakePromote{}
	e, err := evolver.NewEvolver(src, dist, pro)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Cycle(context.Background())
	if err != nil {
		t.Fatalf("Cycle: %v", err)
	}
	if res.Observed != 1 || res.Reflected != 2 || res.Promoted != 2 || res.Skipped != 0 {
		t.Errorf("bad counts: %+v", res)
	}
	if len(pro.persisted) != 2 {
		t.Errorf("expected 2 persisted, got %d", len(pro.persisted))
	}
}

// Test3_DistillError_AbortsCycle proves a Distill failure surfaces
// to the caller and zero Candidates are promoted.
func Test3_DistillError_AbortsCycle(t *testing.T) {
	src := &fakeSource{name: "eval", obs: []evolver.Observation{{Source: "eval", Key: "k"}}}
	dist := &fakeDistill{err: errors.New("reflect boom")}
	pro := &fakePromote{}
	e, _ := evolver.NewEvolver(src, dist, pro)
	if _, err := e.Cycle(context.Background()); err == nil {
		t.Fatal("expected error from Distill failure")
	}
	if len(pro.persisted) != 0 {
		t.Errorf("expected 0 persisted on Distill error, got %d", len(pro.persisted))
	}
}

// Test4_SourceRetry_RecoversAfterTransientFailure installs a Source
// that fails twice then succeeds; the driver must retry and return
// the successful Observations, not the last error.
func Test4_SourceRetry_RecoversAfterTransientFailure(t *testing.T) {
	src := &fakeSource{name: "llm-router", obs: []evolver.Observation{{Source: "llm-router", Key: "k"}}, failFirst: 2}
	dist := &fakeDistill{cands: []evolver.Candidate{{ID: "c", Source: "llm-router"}}}
	pro := &fakePromote{}
	e, _ := evolver.NewEvolver(src, dist, pro)
	e.SetMaxRetries(3)
	res, err := e.Cycle(context.Background())
	if err != nil {
		t.Fatalf("Cycle: %v", err)
	}
	if res.Observed != 1 {
		t.Errorf("expected 1 observation after retry, got %d", res.Observed)
	}
	if src.calls != 3 {
		t.Errorf("expected 3 Source calls (2 fail + 1 succeed), got %d", src.calls)
	}
}

// Test5_PromoteFailure_ContinuesAndDedupes checks (a) Promote
// failures do NOT abort the cycle, (b) the same Candidate ID
// appearing twice in one cycle is deduplicated.
func Test5_PromoteFailure_ContinuesAndDedupes(t *testing.T) {
	src := &fakeSource{name: "notify", obs: []evolver.Observation{{Source: "notify", Key: "k"}}}
	dist := &fakeDistill{cands: []evolver.Candidate{
		{ID: "c1", Source: "notify"},
		{ID: "c2", Source: "notify"},
		{ID: "c1", Source: "notify"}, // duplicate within same cycle
	}}
	pro := &fakePromote{failOn: map[string]bool{"c2": true}}
	e, _ := evolver.NewEvolver(src, dist, pro)
	res, err := e.Cycle(context.Background())
	if err != nil {
		t.Fatalf("Cycle: %v (Promote failure must not abort cycle)", err)
	}
	if res.Promoted != 1 {
		t.Errorf("expected 1 promoted (c1), got %d", res.Promoted)
	}
	if res.Skipped != 2 {
		t.Errorf("expected 2 skipped (c2 fail + c1 dup), got %d", res.Skipped)
	}
}

// Test5_NoObservations_NoDistill proves an empty Source.Observe
// short-circuits the cycle without calling Distill or Promote.
type countingDistill struct{ calls int32 }

func (c *countingDistill) Reflect(ctx context.Context, in []evolver.Observation) ([]evolver.Candidate, error) {
	atomic.AddInt32(&c.calls, 1)
	return nil, nil
}

func Test5_NoObservations_NoDistill(t *testing.T) {
	src := &fakeSource{name: "silent", obs: nil}
	dist := &countingDistill{}
	pro := &fakePromote{}
	e, _ := evolver.NewEvolver(src, dist, pro)
	res, err := e.Cycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Observed != 0 || res.Reflected != 0 {
		t.Errorf("expected 0/0, got %+v", res)
	}
	if dist.calls != 0 {
		t.Errorf("Distill must not be called when Source returned no observations, got %d calls", dist.calls)
	}
}

// Test5_ContextCancel_StopsRetry confirms context cancellation breaks
// out of the retry loop rather than waiting for the full retry budget.
func Test5_ContextCancel_StopsRetry(t *testing.T) {
	src := &fakeSource{name: "stuck", obs: nil, failFirst: 100}
	dist := &fakeDistill{}
	pro := &fakePromote{}
	e, _ := evolver.NewEvolver(src, dist, pro)
	e.SetMaxRetries(10)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := e.Cycle(ctx); err == nil {
		t.Fatal("expected context cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cycle did not respect context cancellation: took %s", elapsed)
	}
}
