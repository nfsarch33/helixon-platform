package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/memory"
)

type fakeRuns struct {
	runs  []agent.RunRecord
	steps map[string][]agent.RunStep
	turns map[string][]agent.Turn
	usage map[bool]agent.RunUsage // keyed by since.IsZero()
	// ignoreTurnLimit models a RunView that does not apply the limit it is
	// given, so the endpoint's own bound can be tested rather than the
	// store's. The real SessionStore applies it in SQL.
	ignoreTurnLimit bool
}

func (f *fakeRuns) ListRuns(_ context.Context, fl agent.RunFilter) ([]agent.RunRecord, error) {
	var out []agent.RunRecord
	for i := range f.runs {
		if fl.Status == "" || f.runs[i].Status == fl.Status {
			out = append(out, f.runs[i])
		}
	}
	if fl.Limit > 0 && len(out) > fl.Limit {
		out = out[:fl.Limit]
	}
	return out, nil
}

func (f *fakeRuns) GetRun(_ context.Context, id string) (*agent.RunRecord, error) {
	for i := range f.runs {
		if f.runs[i].ID == id {
			return &f.runs[i], nil
		}
	}
	return nil, agent.ErrRunNotFound
}

func (f *fakeRuns) ListSteps(_ context.Context, runID string) ([]agent.RunStep, error) {
	return f.steps[runID], nil
}

func (f *fakeRuns) ListTurns(_ context.Context, sessionID string, limit int) ([]agent.Turn, error) {
	t := f.turns[sessionID]
	if limit > 0 && !f.ignoreTurnLimit && len(t) > limit {
		t = t[:limit]
	}
	return t, nil
}

func (f *fakeRuns) RunUsage(_ context.Context, since time.Time) (agent.RunUsage, error) {
	return f.usage[since.IsZero()], nil
}

func getJSON(t *testing.T, srv *httptest.Server, path string, want int) map[string]any {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		t.Fatalf("GET %s: status %d, want %d", path, resp.StatusCode, want)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return out
}

func consoleServer(t *testing.T, rv RunView, mem MemorySearcher, cfg *ConsoleConfig) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	MountConsole(mux, rv, mem, cfg)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestConsole_RunsListDetailAndCosts(t *testing.T) {
	t.Parallel()
	fr := &fakeRuns{
		runs: []agent.RunRecord{
			{ID: "r2", SessionID: "s2", Status: agent.RunCompleted, TokensIn: 10, TokensOut: 4},
			{ID: "r1", SessionID: "s1", Status: agent.RunNeedsHuman},
		},
		steps: map[string][]agent.RunStep{"r1": {{RunID: "r1", Seq: 1, ToolCallID: "c1", Tool: "shell", Status: agent.StepFailed}}},
		turns: map[string][]agent.Turn{"s1": {{ID: "t1", SessionID: "s1", Role: agent.RoleUser, Content: "do it", Seq: 1}}},
		usage: map[bool]agent.RunUsage{true: {Runs: 2, TokensIn: 10}, false: {Runs: 1, TokensIn: 10}},
	}
	srv := consoleServer(t, fr, nil, &ConsoleConfig{})

	list := getJSON(t, srv, "/api/v1/runs", http.StatusOK)
	if runs := list["runs"].([]any); len(runs) != 2 {
		t.Fatalf("runs = %v", list["runs"])
	}
	filtered := getJSON(t, srv, "/api/v1/runs?status=needs_human&limit=5", http.StatusOK)
	if runs := filtered["runs"].([]any); len(runs) != 1 || runs[0].(map[string]any)["id"] != "r1" {
		t.Fatalf("filtered = %v", filtered["runs"])
	}
	detail := getJSON(t, srv, "/api/v1/runs/r1", http.StatusOK)
	if detail["run"].(map[string]any)["status"] != "needs_human" {
		t.Fatalf("detail run = %v", detail["run"])
	}
	if steps := detail["steps"].([]any); len(steps) != 1 || steps[0].(map[string]any)["tool"] != "shell" {
		t.Fatalf("detail steps = %v", detail["steps"])
	}
	if turns := detail["turns"].([]any); len(turns) != 1 || turns[0].(map[string]any)["content"] != "do it" {
		t.Fatalf("detail turns = %v", detail["turns"])
	}
	getJSON(t, srv, "/api/v1/runs/nope", http.StatusNotFound)

	costs := getJSON(t, srv, "/api/v1/costs", http.StatusOK)
	if costs["all_time"].(map[string]any)["runs"].(float64) != 2 || costs["last_24h"].(map[string]any)["runs"].(float64) != 1 {
		t.Fatalf("costs = %v", costs)
	}
}

func TestConsole_EmptyStoreIsEmptyNotNull(t *testing.T) {
	t.Parallel()
	srv := consoleServer(t, &fakeRuns{}, nil, &ConsoleConfig{})
	resp, err := http.Get(srv.URL + "/api/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), `"runs":[]`) {
		t.Fatalf("an empty store must render an empty list, got %s", body[:n])
	}
}

func TestConsole_NilStoreIs503WithAReason(t *testing.T) {
	t.Parallel()
	srv := consoleServer(t, nil, nil, &ConsoleConfig{})
	for _, p := range []string{"/api/v1/runs", "/api/v1/runs/x", "/api/v1/costs", "/api/v1/memory/search?q=x"} {
		out := getJSON(t, srv, p, http.StatusServiceUnavailable)
		if out["error"] == "" {
			t.Fatalf("%s: a 503 must say why", p)
		}
	}
}

func TestConsole_EvalsFromLedgerAndTextfiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ledger := filepath.Join(dir, "cycles.ndjson")
	if err := os.WriteFile(ledger, []byte(`{"seq":1,"status":"complete","eval_ab":"EVAL_AB verdict=PASS"}
{"seq":2,"status":"failed","eval_ab":"EVAL_AB verdict=FAIL"}
not json
{"seq":3,"status":"complete","eval_ab":"EVAL_AB verdict=PASS delta_pp=+3.3"}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	tf := filepath.Join(dir, "tf")
	if err := os.Mkdir(tf, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tf, "hlxn-student-parity.prom"), []byte("# HELP hlxn_student_score_ratio x\n# TYPE hlxn_student_score_ratio gauge\nhlxn_student_score_ratio{student=\"m3\",reference=\"local\"} 0.9643\nnode_other 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := consoleServer(t, nil, nil, &ConsoleConfig{EvalLedgerPath: ledger, TextfileDir: tf})
	out := getJSON(t, srv, "/api/v1/evals?limit=2", http.StatusOK)
	led := out["ledger"].([]any)
	if len(led) != 2 || led[0].(map[string]any)["seq"].(float64) != 3 || led[1].(map[string]any)["seq"].(float64) != 2 {
		t.Fatalf("ledger = %v, want the newest two, newest first, torn line skipped", led)
	}
	mets := out["metrics"].([]any)
	if len(mets) != 1 {
		t.Fatalf("metrics = %v, want only the hlxn_ sample", mets)
	}
	m := mets[0].(map[string]any)
	if m["name"] != "hlxn_student_score_ratio" || m["value"].(float64) != 0.9643 || m["labels"].(map[string]any)["student"] != "m3" {
		t.Fatalf("metric = %v", m)
	}

	// Absence is absence: a missing ledger and directory render empty, 200.
	srv2 := consoleServer(t, nil, nil, &ConsoleConfig{EvalLedgerPath: filepath.Join(dir, "missing.ndjson"), TextfileDir: filepath.Join(dir, "missing")})
	out2 := getJSON(t, srv2, "/api/v1/evals", http.StatusOK)
	if len(out2["ledger"].([]any)) != 0 || len(out2["metrics"].([]any)) != 0 || out2["ledger_error"] != nil {
		t.Fatalf("missing artifacts must be empty without an error: %v", out2)
	}
}

type fakeMem struct {
	results []memory.HybridResult
	err     error
}

func (f *fakeMem) Search(_ context.Context, _, _, _, _ string) ([]memory.HybridResult, error) {
	return f.results, f.err
}

func TestConsole_MemorySearch(t *testing.T) {
	t.Parallel()
	srv := consoleServer(t, nil, &fakeMem{results: []memory.HybridResult{{}, {}, {}}}, &ConsoleConfig{})
	out := getJSON(t, srv, "/api/v1/memory/search?q=lease&limit=2", http.StatusOK)
	if out["query"] != "lease" || len(out["results"].([]any)) != 2 {
		t.Fatalf("search = %v", out)
	}
	getJSON(t, srv, "/api/v1/memory/search", http.StatusBadRequest)
	srvErr := consoleServer(t, nil, &fakeMem{err: errors.New("engram down")}, &ConsoleConfig{})
	out = getJSON(t, srvErr, "/api/v1/memory/search?q=x", http.StatusBadGateway)
	if !strings.Contains(out["error"].(string), "engram down") {
		t.Fatalf("upstream error must be surfaced: %v", out)
	}
}

func TestParseSample(t *testing.T) {
	t.Parallel()
	m, ok := parseSample(`hlxn_student_truncated_total{student="MiniMax-M3",reference="qwen3.8-27b-local"} 1`, "hlxn_")
	if !ok || m.Value != 1 || m.Labels["reference"] != "qwen3.8-27b-local" {
		t.Fatalf("parse = %+v ok=%v", m, ok)
	}
	if _, ok := parseSample("# TYPE hlxn_x gauge", "hlxn_"); ok {
		t.Fatal("comments are not samples")
	}
	if _, ok := parseSample("node_load1 0.5", "hlxn_"); ok {
		t.Fatal("prefix must filter")
	}
}

// An unknown ?status= used to answer 200 with an empty list, which reads
// exactly like "nothing needs a human" -- the one answer an operator acts on
// by walking away. A typo must be a rejection, and must say what is allowed.
func TestConsole_AnUnknownStatusIsARejection(t *testing.T) {
	f := &fakeRuns{runs: []agent.RunRecord{{ID: "r1", Status: agent.RunNeedsHuman}}}
	mux := http.NewServeMux()
	MountConsole(mux, f, nil, &ConsoleConfig{})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/runs?status=needs-human")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 -- a typo'd filter must not read as an empty fleet", resp.StatusCode)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "needs_human") {
		t.Fatalf("the rejection does not name the allowed values: %q", msg)
	}

	// The control: a status the store can hold is still served, so the check
	// above is a rejection of the unknown, not of filtering.
	resp, err = http.Get(srv.URL + "/api/v1/runs?status=needs_human")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a known status returned %d, want 200", resp.StatusCode)
	}
}

// The run detail endpoint is polled, and the turns it returns belong to the
// SESSION, which outlives the run. Both facts have to reach the client:
// a bound, and the scope of what came back.
func TestConsole_RunDetailIsBoundedAndSaysItsScope(t *testing.T) {
	turns := make([]agent.Turn, 0, 40)
	steps := make([]agent.RunStep, 0, 40)
	for i := 0; i < 40; i++ {
		turns = append(turns, agent.Turn{ID: fmt.Sprintf("t%d", i), SessionID: "s1", Role: "user", Seq: int64(i)})
		steps = append(steps, agent.RunStep{RunID: "r1", Seq: int64(i), Iteration: i, ToolCallID: fmt.Sprintf("c%d", i), Tool: "shell"})
	}
	f := &fakeRuns{
		runs:  []agent.RunRecord{{ID: "r1", SessionID: "s1", Status: agent.RunCompleted}},
		steps: map[string][]agent.RunStep{"r1": steps},
		turns: map[string][]agent.Turn{"s1": turns},
	}
	mux := http.NewServeMux()
	MountConsole(mux, f, nil, &ConsoleConfig{})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/runs/r1?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	var d RunDetail
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if d.TurnsScope != "session" {
		t.Fatalf("turns_scope = %q, want \"session\" -- the console labels the panel from this", d.TurnsScope)
	}
	if d.Limit != 10 {
		t.Fatalf("limit = %d, want 10", d.Limit)
	}
	if len(d.Steps) != 10 || !d.StepsTruncated {
		t.Fatalf("steps: %d returned, truncated=%v; want 10 and true", len(d.Steps), d.StepsTruncated)
	}
	if len(d.Turns) != 10 || !d.TurnsTruncated {
		t.Fatalf("turns: %d returned, truncated=%v; want 10 and true", len(d.Turns), d.TurnsTruncated)
	}

	// The bound is the endpoint's, not the store's: a view that ignores the
	// limit it is handed must still not produce an unbounded response.
	f.ignoreTurnLimit = true
	resp, err = http.Get(srv.URL + "/api/v1/runs/r1?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	d = RunDetail{}
	_ = json.NewDecoder(resp.Body).Decode(&d)
	_ = resp.Body.Close()
	if len(d.Turns) != 10 || !d.TurnsTruncated {
		t.Fatalf("a view ignoring the limit produced %d turns; the endpoint must cap at 10", len(d.Turns))
	}
	f.ignoreTurnLimit = false

	// The control: a run whose lists fit under the bound is NOT reported as
	// truncated, so the flag means something.
	f.steps["r1"] = steps[:2]
	f.turns["s1"] = turns[:2]
	resp, err = http.Get(srv.URL + "/api/v1/runs/r1?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	d = RunDetail{}
	_ = json.NewDecoder(resp.Body).Decode(&d)
	_ = resp.Body.Close()
	if d.StepsTruncated || d.TurnsTruncated {
		t.Fatalf("a short run is reported truncated: steps=%v turns=%v", d.StepsTruncated, d.TurnsTruncated)
	}
}

// A gate that has nothing to report writes NaN, which is legal in a textfile
// and impossible in JSON. Encoding it straight to the ResponseWriter failed
// after 200 had gone out, and the console was handed a successful, empty
// answer -- which it renders as a spinner that never resolves.
func TestConsole_ANonFiniteSampleDoesNotEmptyTheResponse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hlxn_eval.prom"), []byte(
		"# HELP hlxn_student_score_ratio ratio\n"+
			"hlxn_student_score_ratio{tier=\"minimax\"} NaN\n"+
			"hlxn_student_score_ratio{tier=\"local\"} +Inf\n"+
			"hlxn_eval_tasks_total 52\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	MountConsole(mux, nil, nil, &ConsoleConfig{TextfileDir: dir, EvalLedgerPath: filepath.Join(dir, "none.ndjson")})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/evals")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, raw)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		t.Fatal("200 with an empty body: the encoder failed after the status was sent")
	}
	var body struct {
		Metrics []TextfileMetric `json:"metrics"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the body is not JSON (%v): %s", err, raw)
	}
	// The finite sample survives; the two that cannot be carried are omitted
	// rather than carried wrongly.
	if len(body.Metrics) != 1 || body.Metrics[0].Name != "hlxn_eval_tasks_total" {
		t.Fatalf("metrics = %+v, want only the finite sample", body.Metrics)
	}
}

// The belt to that pair of braces: whatever the payload, a response that
// cannot be encoded must not be a 200. Ranging over a channel-valued map is
// the smallest thing json refuses.
func TestConsole_AnUnencodableBodyIsNotA200(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]any{"bad": make(chan int)})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "could not be encoded") {
		t.Fatalf("the failure does not say what happened: %s", rec.Body.String())
	}
}
