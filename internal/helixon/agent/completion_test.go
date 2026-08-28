package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/llm"
	"go.uber.org/goleak"

	_ "modernc.org/sqlite"
)

// scriptedProvider replays canned completions in order.
type scriptedProvider struct {
	responses []*llm.CompletionResponse
	idx       int
}

//nolint:gocritic // hugeParam: the signature is llm.Provider's, not ours
func (p *scriptedProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if p.idx >= len(p.responses) {
		return nil, fmt.Errorf("scripted provider exhausted after %d calls", p.idx)
	}
	r := p.responses[p.idx]
	p.idx++
	return r, nil
}

// scriptedExecutor returns a canned payload per tool name and advertises a
// fixed tool list (which is what activates the completion gate).
type scriptedExecutor struct {
	results map[string][]string
	counts  map[string]int
	tools   []string
}

func (e *scriptedExecutor) Execute(_ context.Context, name, _ string) (string, error) {
	if e.counts == nil {
		e.counts = map[string]int{}
	}
	seq := e.results[name]
	i := e.counts[name]
	e.counts[name]++
	if len(seq) == 0 {
		return "ok", nil
	}
	if i >= len(seq) {
		i = len(seq) - 1
	}
	return seq[i], nil
}

func (e *scriptedExecutor) Available() []llm.Tool {
	out := make([]llm.Tool, 0, len(e.tools))
	for _, name := range e.tools {
		out = append(out, llm.Tool{Type: "function", Function: llm.FunctionDef{Name: name}})
	}
	return out
}

func toolCallResponse(id, name, args string) *llm.CompletionResponse {
	return &llm.CompletionResponse{
		Choices: []llm.Choice{{Message: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: id, Type: "function",
				Function: llm.FunctionCall{Name: name, Arguments: args},
			}},
		}}},
		Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5},
	}
}

func textResponse(content string) *llm.CompletionResponse {
	return &llm.CompletionResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: content}}},
		Usage:   llm.Usage{PromptTokens: 7, CompletionTokens: 3},
	}
}

func newGateAgent(t *testing.T, provider llm.Provider, exec ToolExecutor, policy CompletionPolicy) (*Agent, *SessionStore, string) {
	t.Helper()
	store, err := NewSessionStore(context.Background(), filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ag := New(provider, exec, store, Config{
		MaxIterations: 10, MaxTokens: 100000, Timeout: 30 * time.Second,
		Completion: policy,
	})
	sess, err := store.CreateSession(context.Background(), "gate-test", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return ag, store, sess.ID
}

const verifierPass = `{"check":"go_test","pass":true,"outcome":"passed","exit_code":0}`
const verifierFail = `{"check":"go_test","pass":false,"outcome":"failed","exit_code":1}`

// TestCompletionGate_TableDriven is the core behavior: a run that changed
// state may not report success on the model's say-so alone.
func TestCompletionGate_TableDriven(t *testing.T) {
	// No t.Parallel: every case here opens its own SQLite session store,
	// and a dozen of those running concurrently starves the package's
	// pre-existing time-boxed tests (TestAgentBudgetExhaust has a 10s
	// agent ceiling) into failing for the wrong reason.
	tests := []struct {
		name      string
		policy    CompletionPolicy
		tools     []string
		responses []*llm.CompletionResponse
		results   map[string][]string
		wantErr   error
		wantAppr  bool
	}{
		{
			name:   "state-changing run with no verifier evidence is refused",
			policy: DefaultCompletionPolicy(),
			tools:  []string{"file_write", "verifier_run"},
			responses: []*llm.CompletionResponse{
				toolCallResponse("1", "file_write", `{"path":"/x"}`),
				textResponse("all done, trust me"),
			},
			wantErr:  ErrNoVerifierEvidence,
			wantAppr: true,
		},
		{
			name:   "state-changing run WITH passing evidence completes",
			policy: DefaultCompletionPolicy(),
			tools:  []string{"file_write", "verifier_run"},
			responses: []*llm.CompletionResponse{
				toolCallResponse("1", "file_write", `{"path":"/x"}`),
				toolCallResponse("2", "verifier_run", `{"check":"go_test"}`),
				textResponse("done, and proven"),
			},
			results: map[string][]string{"verifier_run": {verifierPass}},
		},
		{
			name:   "read-only run is not gated",
			policy: DefaultCompletionPolicy(),
			tools:  []string{"file_read", "verifier_run"},
			responses: []*llm.CompletionResponse{
				toolCallResponse("1", "file_read", `{"path":"/x"}`),
				textResponse("here is what I read"),
			},
		},
		{
			// Demanding evidence the agent has no tool to produce would
			// turn every working run into a failure.
			name:   "gate is inert when no verifier tool is available",
			policy: DefaultCompletionPolicy(),
			tools:  []string{"file_write"},
			responses: []*llm.CompletionResponse{
				toolCallResponse("1", "file_write", `{"path":"/x"}`),
				textResponse("all done"),
			},
		},
		{
			name:   "gate can be switched off",
			policy: CompletionPolicy{Enabled: false},
			tools:  []string{"file_write", "verifier_run"},
			responses: []*llm.CompletionResponse{
				toolCallResponse("1", "file_write", `{"path":"/x"}`),
				textResponse("all done"),
			},
		},
		{
			// An unreadable verifier payload is a FAILURE, not a pass.
			name:   "unparseable verifier output does not satisfy the gate",
			policy: CompletionPolicy{Enabled: true, MaxConsecutiveFailures: 5},
			tools:  []string{"file_write", "verifier_run"},
			responses: []*llm.CompletionResponse{
				toolCallResponse("1", "file_write", `{"path":"/x"}`),
				toolCallResponse("2", "verifier_run", `{"check":"go_test"}`),
				textResponse("done"),
			},
			results:  map[string][]string{"verifier_run": {"not json at all"}},
			wantErr:  ErrNoVerifierEvidence,
			wantAppr: true,
		},
		{
			// pass:true with a non-"passed" outcome (a timeout, say) must
			// not count as evidence.
			name:   "pass flag without a passed outcome is not evidence",
			policy: CompletionPolicy{Enabled: true, MaxConsecutiveFailures: 5},
			tools:  []string{"file_write", "verifier_run"},
			responses: []*llm.CompletionResponse{
				toolCallResponse("1", "file_write", `{"path":"/x"}`),
				toolCallResponse("2", "verifier_run", `{"check":"go_test"}`),
				textResponse("done"),
			},
			results:  map[string][]string{"verifier_run": {`{"pass":true,"outcome":"timeout"}`}},
			wantErr:  ErrNoVerifierEvidence,
			wantAppr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &scriptedExecutor{results: tt.results, tools: tt.tools}
			ag, _, sessID := newGateAgent(t, &scriptedProvider{responses: tt.responses}, exec, tt.policy)
			res, err := ag.Run(context.Background(), sessID, "do the work")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Run() error = %v, want %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Run() error = %v, want nil", err)
			}
			if res.NeedsHumanApproval != tt.wantAppr {
				t.Fatalf("NeedsHumanApproval = %v, want %v", res.NeedsHumanApproval, tt.wantAppr)
			}
		})
	}
}

// TestEscalation_TwoConsecutiveFailuresStopTheLoop is the escalation policy.
// Without it the model is free to re-run a red verifier until the iteration
// or token budget runs out.
func TestEscalation_TwoConsecutiveFailuresStopTheLoop(t *testing.T) {
	// No t.Parallel: every case here opens its own SQLite session store,
	// and a dozen of those running concurrently starves the package's
	// pre-existing time-boxed tests (TestAgentBudgetExhaust has a 10s
	// agent ceiling) into failing for the wrong reason.
	exec := &scriptedExecutor{
		results: map[string][]string{"verifier_run": {verifierFail, verifierFail, verifierFail}},
		tools:   []string{"file_write", "verifier_run"},
	}
	responses := []*llm.CompletionResponse{
		toolCallResponse("1", "file_write", `{"path":"/x"}`),
		toolCallResponse("2", "verifier_run", `{"check":"go_test"}`),
		toolCallResponse("3", "verifier_run", `{"check":"go_test"}`),
		toolCallResponse("4", "verifier_run", `{"check":"go_test"}`),
		textResponse("never reached"),
	}
	ag, _, sessID := newGateAgent(t, &scriptedProvider{responses: responses}, exec, DefaultCompletionPolicy())

	res, err := ag.Run(context.Background(), sessID, "fix the build")
	if !errors.Is(err, ErrNeedsHumanApproval) {
		t.Fatalf("Run() error = %v, want ErrNeedsHumanApproval", err)
	}
	if !res.NeedsHumanApproval {
		t.Fatal("NeedsHumanApproval must be set on the result")
	}
	if res.VerifierFailures != 2 {
		t.Fatalf("VerifierFailures = %d, want 2", res.VerifierFailures)
	}
	if got := exec.counts["verifier_run"]; got != 2 {
		t.Fatalf("the verifier ran %d times; the loop must stop at the 2nd consecutive failure", got)
	}
}

// TestEscalation_FailureThenPassResetsTheCounter: the policy is about a stuck
// loop, not about a run that had one red check and then fixed it.
func TestEscalation_FailureThenPassResetsTheCounter(t *testing.T) {
	// No t.Parallel: every case here opens its own SQLite session store,
	// and a dozen of those running concurrently starves the package's
	// pre-existing time-boxed tests (TestAgentBudgetExhaust has a 10s
	// agent ceiling) into failing for the wrong reason.
	exec := &scriptedExecutor{
		results: map[string][]string{"verifier_run": {verifierFail, verifierPass, verifierFail}},
		tools:   []string{"file_write", "verifier_run"},
	}
	responses := []*llm.CompletionResponse{
		toolCallResponse("1", "file_write", `{"path":"/x"}`),
		toolCallResponse("2", "verifier_run", `{"check":"go_test"}`), // fail  -> 1
		toolCallResponse("3", "verifier_run", `{"check":"go_test"}`), // pass  -> 0
		toolCallResponse("4", "verifier_run", `{"check":"go_test"}`), // fail  -> 1
		toolCallResponse("5", "verifier_run", `{"check":"go_test"}`), // fail  -> 2, escalate
		textResponse("unreachable"),
	}
	ag, _, sessID := newGateAgent(t, &scriptedProvider{responses: responses}, exec, DefaultCompletionPolicy())

	res, err := ag.Run(context.Background(), sessID, "fix it")
	if !errors.Is(err, ErrNeedsHumanApproval) {
		t.Fatalf("Run() error = %v, want ErrNeedsHumanApproval after fail,pass,fail,fail", err)
	}
	if res.VerifierFailures != 2 {
		t.Fatalf("VerifierFailures = %d, want 2 (the counter must reset on the pass)", res.VerifierFailures)
	}
	// Four verifier calls happened; without the reset the run would have
	// escalated on the third.
	if got := exec.counts["verifier_run"]; got != 4 {
		t.Fatalf("verifier ran %d times, want 4 (fail, pass, fail, fail)", got)
	}
}

func TestEscalation_ThresholdIsConfigurable(t *testing.T) {
	// No t.Parallel: every case here opens its own SQLite session store,
	// and a dozen of those running concurrently starves the package's
	// pre-existing time-boxed tests (TestAgentBudgetExhaust has a 10s
	// agent ceiling) into failing for the wrong reason.
	exec := &scriptedExecutor{
		results: map[string][]string{"verifier_run": {verifierFail}},
		tools:   []string{"file_write", "verifier_run"},
	}
	responses := []*llm.CompletionResponse{
		toolCallResponse("1", "verifier_run", `{"check":"go_test"}`),
		textResponse("unreachable"),
	}
	policy := DefaultCompletionPolicy()
	policy.MaxConsecutiveFailures = 1
	ag, _, sessID := newGateAgent(t, &scriptedProvider{responses: responses}, exec, policy)

	if _, err := ag.Run(context.Background(), sessID, "x"); !errors.Is(err, ErrNeedsHumanApproval) {
		t.Fatalf("with a threshold of 1 the first failure must escalate; got %v", err)
	}
	if got := exec.counts["verifier_run"]; got != 1 {
		t.Fatalf("verifier ran %d times, want 1", got)
	}
}

func TestParseVerifierVerdict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload string
		err     error
		want    bool
	}{
		{name: "passing", payload: verifierPass, want: true},
		{name: "failing", payload: verifierFail},
		{name: "tool error beats the payload", payload: verifierPass, err: errors.New("boom")},
		{name: "malformed json", payload: "{"},
		{name: "empty", payload: ""},
		{name: "pass true, outcome timeout", payload: `{"pass":true,"outcome":"timeout"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parseVerifierVerdict(tt.payload, tt.err); got != tt.want {
				t.Fatalf("parseVerifierVerdict(%q, %v) = %v, want %v", tt.payload, tt.err, got, tt.want)
			}
		})
	}
}

func TestCompletionPolicy_Defaults(t *testing.T) {
	t.Parallel()
	got := CompletionPolicy{Enabled: true}.withDefaults()
	if got.VerifierTool != "verifier_run" {
		t.Errorf("VerifierTool = %q", got.VerifierTool)
	}
	if got.MaxConsecutiveFailures != 2 {
		t.Errorf("MaxConsecutiveFailures = %d, want the conservative default of 2", got.MaxConsecutiveFailures)
	}
	if !got.isMutating("shell") || !got.isMutating("file_write") {
		t.Errorf("shell and file_write must count as state-changing: %v", got.MutatingTools)
	}
	if got.isMutating("file_read") {
		t.Error("file_read must not count as state-changing")
	}
}

// TestTokensArePersistedPerTurn: tokens_in/tokens_out were hard-coded to 0 at
// every AppendTurn call site, so SessionTokenUsage summed to zero for every
// session ever recorded.
func TestTokensArePersistedPerTurn(t *testing.T) {
	// No t.Parallel: every case here opens its own SQLite session store,
	// and a dozen of those running concurrently starves the package's
	// pre-existing time-boxed tests (TestAgentBudgetExhaust has a 10s
	// agent ceiling) into failing for the wrong reason.
	exec := &scriptedExecutor{tools: []string{"file_read"}}
	responses := []*llm.CompletionResponse{
		toolCallResponse("1", "file_read", `{"path":"/x"}`), // 10 in / 5 out
		textResponse("done"), // 7 in / 3 out
	}
	ag, store, sessID := newGateAgent(t, &scriptedProvider{responses: responses}, exec, CompletionPolicy{})

	res, err := ag.Run(context.Background(), sessID, "read it")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.TokensIn != 17 || res.TokensOut != 8 {
		t.Fatalf("run totals = %d/%d, want 17/8", res.TokensIn, res.TokensOut)
	}
	in, out, err := store.SessionTokenUsage(context.Background(), sessID)
	if err != nil {
		t.Fatalf("SessionTokenUsage: %v", err)
	}
	if in != 17 || out != 8 {
		t.Fatalf("persisted usage = %d/%d, want 17/8 — the columns are being written as 0 again", in, out)
	}
}

// TestMain adds a goleak check over the whole agent package: the loop now
// owns timeouts, tool execution and a gate, and a leaked goroutine there
// would accumulate for the length of a soak.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("modernc.org/sqlite.(*conn).interruptOnDoneLocked.func1"),
		goleak.IgnoreAnyFunction("modernc.org/sqlite.applyQueryParams"),
	)
}
