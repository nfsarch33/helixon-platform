package dashboard

import (
	"context"
	"encoding/json"
	"errors"
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

func (f *fakeRuns) ListTurns(_ context.Context, sessionID string, _ int) ([]agent.Turn, error) {
	return f.turns[sessionID], nil
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
