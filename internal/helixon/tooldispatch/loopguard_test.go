// Tests for the LoopGuardExecutor wrapper.
package tooldispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/nfsarch33/helixon-platform/internal/loopguard"
)

// fakeInner is a minimal InnerExecutor used to assert LoopGuardExecutor
// forwards/short-circuits correctly.
type fakeInner struct {
	calls int
	reply string
	err   error
}

func (f *fakeInner) Execute(ctx context.Context, name string, argsJSON string) (string, error) { //nolint:revive // unused-parameter required by interface
	f.calls++
	return f.reply, f.err
}
func (f *fakeInner) Available() []llm.Tool { return nil }

// TestLoopGuardExecutor_AllowsDistinctCalls asserts that distinct (name, args)
// are forwarded to the inner executor with no detection.
func TestLoopGuardExecutor_AllowsDistinctCalls(t *testing.T) {
	inner := &fakeInner{reply: "ok"}
	g := NewLoopGuardExecutor(inner, loopguard.New(3, 60_000_000_000)) // 60s
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		out, err := g.Execute(ctx, "tool", "args-"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if out != "ok" {
			t.Fatalf("call %d: want ok, got %q", i, out)
		}
	}
	if inner.calls != 5 {
		t.Fatalf("inner.calls: want 5, got %d", inner.calls)
	}
}

// TestLoopGuardExecutor_TripsAfterThreshold asserts that the 3rd identical
// call returns ErrLoopDetected and DOES NOT call the inner executor.
func TestLoopGuardExecutor_TripsAfterThreshold(t *testing.T) {
	inner := &fakeInner{reply: "ok"}
	g := NewLoopGuardExecutor(inner, loopguard.New(3, 60_000_000_000))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := g.Execute(ctx, "tool", "args-same"); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
	}
	// 3rd identical should trip.
	_, err := g.Execute(ctx, "tool", "args-same")
	if !errors.Is(err, loopguard.ErrLoopDetected) {
		t.Fatalf("3rd call: want ErrLoopDetected, got %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("inner.calls: want 2 (no inner call on 3rd), got %d", inner.calls)
	}
}

// TestLoopGuardExecutor_OnDetectCallback asserts the callback fires on trip.
func TestLoopGuardExecutor_OnDetectCallback(t *testing.T) {
	inner := &fakeInner{reply: "ok"}
	g := NewLoopGuardExecutor(inner, loopguard.New(3, 60_000_000_000))

	called := 0
	var lastName, lastHash string
	g.WithOnDetect(func(name, hash string) {
		called++
		lastName = name
		lastHash = hash
	})

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, _ = g.Execute(ctx, "toolX", "{\"x\":1}")
	}
	if called != 1 {
		t.Fatalf("onDetect call count: want 1, got %d", called)
	}
	if lastName != "toolX" {
		t.Fatalf("onDetect lastName: want toolX, got %q", lastName)
	}
	if lastHash == "" {
		t.Fatal("onDetect lastHash empty")
	}
}

// TestHashToolCall_StableAndDifferent asserts hashing semantics.
func TestHashToolCall_StableAndDifferent(t *testing.T) {
	a1 := hashToolCall("tool", `{"a":1}`)
	a2 := hashToolCall("tool", `{"a":1}`)
	b := hashToolCall("tool", `{"a":2}`)
	c := hashToolCall("tool2", `{"a":1}`)

	if a1 != a2 {
		t.Fatalf("same input should hash equal: %q vs %q", a1, a2)
	}
	if a1 == b {
		t.Fatalf("different args should hash different: %q vs %q", a1, b)
	}
	if a1 == c {
		t.Fatalf("different name should hash different: %q vs %q", a1, c)
	}
}
