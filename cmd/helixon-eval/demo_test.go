package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewDemoCmd_Registers verifies the demo subcommand is wired into
// the root cobra command at v18684-5.
func TestNewDemoCmd_Registers(t *testing.T) {
	root := newRootCmd()
	var found bool
	for _, c := range root.Commands() {
		if c.Name() == "demo" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("demo subcommand not registered on helixon-eval root command")
	}
}

// TestSupportedBackends_MinimaxAndQwen pins the v18684-5 contract: both
// `minimax` and `qwen` must always be supported. Adding a third backend
// must NOT drop either — this test catches accidental regressions.
func TestSupportedBackends_MinimaxAndQwen(t *testing.T) {
	for _, want := range []string{"minimax", "qwen"} {
		spec, ok := backendSpecs[want]
		if !ok {
			t.Errorf("backendSpecs missing %q", want)
			continue
		}
		if spec.ItemUUID == "" || len(spec.ItemUUID) != 26 {
			t.Errorf("backendSpecs[%q].ItemUUID = %q (must be 26-char UUID per 1password-uuid-required.mdc)",
				want, spec.ItemUUID)
		}
		if spec.BaseURL == "" {
			t.Errorf("backendSpecs[%q].BaseURL is empty", want)
		}
		if spec.ModelName == "" {
			t.Errorf("backendSpecs[%q].ModelName is empty", want)
		}
	}
}

// TestEstimateCost_MiniMax sanity-checks the v18684-5 cost formula.
// Inputs: 1k prompt tokens, 0 completion tokens → expected ~0.00014 USD.
func TestEstimateCost_MiniMax(t *testing.T) {
	got := estimateCost("MiniMax-M3", 1000, 0)
	if got < 0.0001 || got > 0.0002 {
		t.Errorf("MiniMax cost for 1k in: got %.5f USD, want ~0.00014 USD", got)
	}
}

// TestEstimateCost_Qwen confirms the bundled Aliyun plan reports zero
// incremental cost (we don't have a public rate sheet for it yet).
func TestEstimateCost_Qwen(t *testing.T) {
	if got := estimateCost("qwen3.7-max", 1000, 1000); got != 0.0 {
		t.Errorf("Qwen cost = %f, want 0.0 (bundled plan)", got)
	}
}

// TestEstimateCost_UnknownModel confirms unknown models report 0.0
// rather than panicking — protects log readers from `inf`/`NaN` lines.
func TestEstimateCost_UnknownModel(t *testing.T) {
	if got := estimateCost("gpt-99-unknown", 123, 456); got != 0.0 {
		t.Errorf("unknown model cost = %f, want 0.0", got)
	}
}

// TestAppendDemoNDJSON_CreatesHome verifies the audit stream resolves
// `~/logs/...` against $HOME, creates the directory if missing, and
// writes exactly one NDJSON line.
func TestAppendDemoNDJSON_CreatesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	res := demoResult{
		RunID:     "test-run",
		StartedAt: "2026-07-18T07:00:00Z",
		Backend:   "minimax",
		Model:     "MiniMax-M3",
		Status:    "ok",
		TotalTok:  8,
	}
	if err := appendDemoNDJSON(res); err != nil {
		t.Fatalf("appendDemoNDJSON: %v", err)
	}

	wantPath := filepath.Join(home, "logs", "helixon-eval-demo.ndjson")
	data, err := os.ReadFile(wantPath) //nolint:gosec // G304: test wants to assert on this file
	if err != nil {
		t.Fatalf("read NDJSON stream: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("NDJSON line missing trailing newline: %q", string(data))
	}

	var got demoResult
	if err := json.Unmarshal([]byte(strings.TrimRight(string(data), "\n")), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RunID != res.RunID || got.Status != "ok" || got.TotalTok != 8 {
		t.Errorf("NDJSON roundtrip mismatch: got %+v", got)
	}
}

// TestWriteDemoResult_JSON pins the stdout JSON output shape so future
// refactors of demoResult keep at least {run_id,status,tokens} intact.
func TestWriteDemoResult_JSON(t *testing.T) {
	res := demoResult{
		RunID:     "x",
		StartedAt: "2026-07-18T07:00:00Z",
		Backend:   "minimax",
		Model:     "MiniMax-M3",
		Status:    "ok",
		TotalTok:  9,
	}
	var out strings.Builder
	if err := writeDemoResult(&out, res, true); err != nil {
		t.Fatalf("writeDemoResult: %v", err)
	}
	if !strings.Contains(out.String(), `"run_id": "x"`) {
		t.Errorf("expected run_id in JSON output, got: %s", out.String())
	}
	if !strings.Contains(out.String(), `"status": "ok"`) {
		t.Errorf("expected status in JSON output, got: %s", out.String())
	}
	if !strings.Contains(out.String(), `"backend": "minimax"`) {
		t.Errorf("expected backend in JSON output, got: %s", out.String())
	}
}

// TestWriteDemoResult_Text pins the human-readable path. The
// non-JSON output is one line so it streams cleanly into `tee` logs.
func TestWriteDemoResult_Text(t *testing.T) {
	res := demoResult{
		RunID:    "x",
		Status:   "ok",
		TotalTok: 9,
	}
	var out strings.Builder
	if err := writeDemoResult(&out, res, false); err != nil {
		t.Fatalf("writeDemoResult: %v", err)
	}
	got := strings.TrimSpace(out.String())
	if !strings.HasPrefix(got, "demo x status=ok tokens=9") {
		t.Errorf("text output does not match expected prefix: %q", got)
	}
}

// TestRunDemoOnce_LLMSuccess exercises the LLM plumbing against a stub
// upstream. It bypasses `op read` by swapping opReadSecret with a stub
// that returns a known token; the httptest.Server serves an
// OpenAI-compatible `/v1/chat/completions` response and we verify the
// end-to-end pipe (runDemoOnce → writeDemoResult → NDJSON).
func TestRunDemoOnce_LLMSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "choices": [
    {"index": 0, "message": {"role": "assistant", "content": "OK"}}
  ],
  "usage": {"prompt_tokens": 4, "completion_tokens": 1, "total_tokens": 5}
}`))
	}))
	defer srv.Close()

	// Override the spec to point at the stub upstream.
	spec := demoSpec{
		ItemUUID:  "test-item-uuid-zzzzzzzzzz",
		FieldID:   "password",
		BaseURL:   srv.URL + "/v1",
		ModelName: "MiniMax-M3",
	}

	// Swap opReadSecret to a stub that returns a known bearer.
	origRead := opReadSecretFn
	opReadSecretFn = func(_, _ string) (string, error) { return "stub-bearer-token", nil }
	defer func() { opReadSecretFn = origRead }()

	home := t.TempDir()
	t.Setenv("HOME", home)

	res := runDemoOnce(spec, "minimax")
	if res.Status != "ok" {
		t.Fatalf("expected status=ok, got %q (err=%q)", res.Status, res.ErrSnippet)
	}
	if res.TotalTok != 5 {
		t.Errorf("TotalTok = %d, want 5", res.TotalTok)
	}
	if res.FirstChars != "OK" {
		t.Errorf("FirstChars = %q, want OK", res.FirstChars)
	}
	if res.EstCostUSD <= 0 {
		t.Errorf("EstCostUSD = %f, want > 0", res.EstCostUSD)
	}

	// Drive writeDemoResult so the audit stream gets appended (matches
	// the production cmd.RunE path).
	if err := writeDemoResult(io.Discard, res, false); err != nil {
		t.Fatalf("writeDemoResult: %v", err)
	}

	// Confirm the NDJSON line landed in the audit stream.
	wantPath := filepath.Join(home, "logs", "helixon-eval-demo.ndjson")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected NDJSON file at %s, got %v", wantPath, err)
	}
}

// TestRunDemoOnce_OpErrorWhenNoOpRead verifies the op-error path: when
// the 1Password resolver returns an error, the demo surfaces it in
// Status=op-error and does NOT crash. This is the v18684-5 contract for
// CI environments where 1Password is unreachable.
func TestRunDemoOnce_OpErrorWhenNoOpRead(t *testing.T) {
	origRead := opReadSecretFn
	opReadSecretFn = func(_, _ string) (string, error) {
		return "", &demoOpError{msg: "simulated op failure"}
	}
	defer func() { opReadSecretFn = origRead }()

	res := runDemoOnce(demoSpec{
		ItemUUID:  "test-item",
		FieldID:   "password",
		BaseURL:   "http://localhost:0",
		ModelName: "MiniMax-M3",
	}, "minimax")
	if res.Status != "op-error" {
		t.Errorf("expected status=op-error, got %q", res.Status)
	}
	if res.ErrSnippet == "" {
		t.Errorf("expected ErrSnippet to capture the op failure")
	}
}

// demoOpError is a typed error for the test stub. Implementing Error()
// on a struct is the idiomatic Go shape and matches what opReadSecret
// returns from `exec.Command`'s error path.
type demoOpError struct{ msg string }

func (e *demoOpError) Error() string { return e.msg }
