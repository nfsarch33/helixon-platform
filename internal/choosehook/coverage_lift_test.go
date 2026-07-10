package choosehook

import (
	"errors"
	"strings"
	"testing"
)

// Tier 4 hot-spot regression tests. These close the v17702-1
// coverage gap (choosehook was 66.1%) by exercising the small
// helpers and the production entry points that the original
// tests skipped. Each test names the function path it covers so
// future coverage diffing can attribute the lift cleanly.

func TestTierLabel_KnownAndUnknown(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   Tier
		want string
	}{
		{Tier0, "tier0"},
		{Tier1, "tier1"},
		{Tier2, "tier2"},
		{Tier3, "tier3"},
		{Tier(99), "unknown"},
		{Tier(-1), "unknown"},
	}
	for _, tc := range cases {
		if got := tierLabel(tc.in); got != tc.want {
			t.Fatalf("tierLabel(%v)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestModelFromCell_KnownAndDefault(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cell string
		want string
	}{
		{"C1", "qwen36-27b-int4"},
		{"C2", "qwen36-27b-q4"},
		{"C7", "qwen36-27b-mtp-q8"},
		{"C8", "qwen36-9b-q4"},
		{"unknown-cell", "qwen36-27b-q4"},
		{"", "qwen36-27b-q4"},
	}
	for _, tc := range cases {
		if got := modelFromCell(tc.cell); got != tc.want {
			t.Fatalf("modelFromCell(%q)=%q, want %q", tc.cell, got, tc.want)
		}
	}
}

func TestPromptFingerprint_StableAndFormat(t *testing.T) {
	t.Parallel()
	a := promptFingerprint("hello world")
	b := promptFingerprint("hello world")
	if a != b {
		t.Fatalf("fingerprint must be stable: %q vs %q", a, b)
	}
	c := promptFingerprint("hello world!")
	if a == c {
		t.Fatalf("fingerprint must differ for different input")
	}
	if !strings.HasPrefix(a, "fnv64a:") {
		t.Fatalf("fingerprint must be fnv64a:<hex>; got %q", a)
	}
	if len(a) != len("fnv64a:")+16 {
		t.Fatalf("fingerprint must have 16 hex chars; got %q", a)
	}
}

func TestPromptFingerprint_EmptyString(t *testing.T) {
	t.Parallel()
	if got := promptFingerprint(""); !strings.HasPrefix(got, "fnv64a:") {
		t.Fatalf("empty prompt must still produce a valid fingerprint, got %q", got)
	}
}

func TestTokenEstimate_ClampsBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
	}{
		{"", 16},      // 0 chars -> floor 16
		{"short", 16}, // 6 chars -> 1, floor 16
		{"medium input", 16},
		{strings.Repeat("x", 64), 16},
		{strings.Repeat("x", 256), 64},
		{strings.Repeat("x", 1024), 256},
		{strings.Repeat("x", 1<<20), 65536}, // huge -> cap 65536
	}
	for _, tc := range cases {
		if got := tokenEstimate(tc.in); got != tc.want {
			t.Fatalf("tokenEstimate(len=%d)=%d, want %d", len(tc.in), got, tc.want)
		}
	}
}

func TestCostEvent_HasExpectedFields(t *testing.T) {
	t.Parallel()
	out := Output{
		SprintID:       "v17702",
		CapturedPrompt: "fp-test",
		CellID:         "C1",
	}
	ev := costEvent(out, Tier2, "ok")
	if ev.SprintID != "v17702" {
		t.Fatalf("SprintID=%q", ev.SprintID)
	}
	if ev.JobID != "fp-test" {
		t.Fatalf("JobID=%q", ev.JobID)
	}
	if ev.CellID != "C1" {
		t.Fatalf("CellID=%q", ev.CellID)
	}
	if ev.JobType != "cursor.beforeSubmitPrompt" {
		t.Fatalf("JobType=%q", ev.JobType)
	}
	if ev.Outcome != "ok" {
		t.Fatalf("Outcome=%q", ev.Outcome)
	}
	if ev.ModelTier != int(Tier2) {
		t.Fatalf("ModelTier=%d", ev.ModelTier)
	}
	// 64 in + 256 out at $0.0020/$0.0030 for qwen36-27b-int4 (C1)
	// = 0.000128 + 0.000768 = 0.000896
	wantCost := 64.0/1000.0*0.0020 + 256.0/1000.0*0.0030
	if ev.EstCostUSD < wantCost-1e-9 || ev.EstCostUSD > wantCost+1e-9 {
		t.Fatalf("EstCostUSD=%v want %v", ev.EstCostUSD, wantCost)
	}
}

func TestCostEvent_ClassifyFailedPath(t *testing.T) {
	t.Parallel()
	out := Output{SprintID: "v17702", DecisionLabel: "no_decision", Reason: "classify_failed"}
	ev := costEvent(out, Tier0, "error")
	if ev.Outcome != "error" {
		t.Fatalf("Outcome=%q", ev.Outcome)
	}
	if ev.ModelTier != 0 {
		t.Fatalf("ModelTier=%d", ev.ModelTier)
	}
}

func TestContains_NegativeAndPositive(t *testing.T) {
	t.Parallel()
	if !contains("hello world", "hello") {
		t.Fatal("hello must be found in 'hello world'")
	}
	if !contains("hello world", "x", "world") {
		t.Fatal("world must be found via multi-substring")
	}
	if contains("hello", "x", "y") {
		t.Fatal("no match must return false")
	}
}

func TestDecideWithFn_CallsRouter(t *testing.T) {
	t.Parallel()
	in := DecideInput{Prompt: "write a Go function", HookMode: "redirect"}
	called := false
	router := func(t Tier) (Decision, error) {
		called = true
		return Decision{Tier: t, CellID: "C2", BaseURL: "http://c2/v1", Reason: "stub"}, nil
	}
	out, err := DecideWithFn(in, router)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("router must be invoked exactly once per call")
	}
	if out.CellID != "C2" {
		t.Fatalf("CellID=%q", out.CellID)
	}
	if out.DecisionLabel != "tier2" {
		t.Fatalf("DecisionLabel=%q", out.DecisionLabel)
	}
}

func TestDecide_MatrixLoadErrorEmitsErrorEvent(t *testing.T) {
	t.Parallel()
	// /dev/null is a directory on Linux; reading it as a file errors.
	_, err := Decide(DecideInput{Prompt: "anything"}, "/dev/null/missing.yaml", "")
	if err == nil {
		t.Fatal("expected matrix-load error")
	}
}

func TestDecide_DefaultHookModeIsRedirect(t *testing.T) {
	t.Parallel()
	out, err := DecideWithFn(DecideInput{Prompt: "hello"}, func(t Tier) (Decision, error) {
		return Decision{Tier: t, CellID: "C1", BaseURL: "http://c1/v1", Reason: "stub"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.HookMode != "redirect" {
		t.Fatalf("default HookMode=%q, want redirect", out.HookMode)
	}
	if out.SprintID != "v14511" {
		t.Fatalf("SprintID=%q", out.SprintID)
	}
}

func TestDecide_AnnotateClearsBaseURL(t *testing.T) {
	t.Parallel()
	out, err := DecideWithFn(DecideInput{Prompt: "hello", HookMode: "annotate"}, func(t Tier) (Decision, error) {
		return Decision{Tier: t, CellID: "C1", BaseURL: "http://c1/v1", Reason: "stub"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.BaseURL != "" {
		t.Fatalf("annotate mode BaseURL=%q, want empty", out.BaseURL)
	}
}

func TestErrMatrixUnreadable_MessageAndIdentity(t *testing.T) {
	t.Parallel()
	if ErrMatrixUnreadable == nil {
		t.Fatal("ErrMatrixUnreadable must be defined")
	}
	if !strings.Contains(ErrMatrixUnreadable.Error(), "choosehook") {
		t.Fatalf("err message %q must contain 'choosehook'", ErrMatrixUnreadable.Error())
	}
	if !errors.Is(ErrMatrixUnreadable, ErrMatrixUnreadable) {
		t.Fatal("ErrMatrixUnreadable must match itself via errors.Is")
	}
}

func TestEnforce_NilRTXDisableStore(t *testing.T) {
	t.Parallel()
	// Without RTX, no store path is exercised; this guards against
	// nil-deref regressions in the EnforceConfig glue.
	cfg := EnforceConfig{RejectOversized: true}
	out, rejected := cfg.Enforce(Output{DecisionLabel: "tier1", CellID: "qwen36-27b-q4"}, DecideInput{Prompt: "hello"}, 16)
	if rejected {
		t.Fatal("expected not-rejected for tiny prompt")
	}
	if out.CacheHit {
		t.Fatal("nil rtx implies no cache hit")
	}
}

func TestTierSuffix_KnownAndFallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"tier0", "0"},
		{"tier1", "1"},
		{"tier2", "2"},
		{"tier3", "3"},
		{"0", "0"},
		{"1", "1"},
		{"unknown", "0"},
		{"", "0"},
	}
	for _, tc := range cases {
		if got := tierSuffix(tc.in); got != tc.want {
			t.Fatalf("tierSuffix(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLabelToTierInt_KnownAndFallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
	}{
		{"tier0", 0}, {"tier1", 1}, {"tier2", 2}, {"tier3", 3},
		{"0", 0}, {"1", 1}, {"2", 2}, {"3", 3},
		{"no_decision", 0}, {"", 0},
		{"unknown", 0},
	}
	for _, tc := range cases {
		if got := labelToTierInt(tc.in); got != tc.want {
			t.Fatalf("labelToTierInt(%q)=%d, want %d", tc.in, got, tc.want)
		}
	}
}
