package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// itemEnvPrefix is the naming contract for the per-backend item env vars
// (HLXN per the fleet-wide header/env-var convention).
const itemEnvPrefix = "HLXN_OP_ITEM_"

// testItemUUID is a syntactically valid but entirely fictional 1Password
// item UUID: 26 lowercase alphanumerics, which is the shape resolveOPRef
// enforces. It names no real item.
const testItemUUID = "abcdefghijklmnopqrstuvwxyz"

// testItemEnv is the env var the runDemoOnce tests resolve through. It is
// deliberately not one of the real backend env vars so a developer's own
// exported HLXN_OP_ITEM_MINIMAX cannot influence the result.
const testItemEnv = "HLXN_OP_ITEM_TESTBACKEND"

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
		if spec.ItemEnv == "" {
			t.Errorf("backendSpecs[%q].ItemEnv is empty; it must name the env var carrying the item UUID", want)
		}
		if spec.FieldID == "" {
			t.Errorf("backendSpecs[%q].FieldID is empty", want)
		}
		if spec.BaseURL == "" {
			t.Errorf("backendSpecs[%q].BaseURL is empty", want)
		}
		if spec.ModelName == "" {
			t.Errorf("backendSpecs[%q].ModelName is empty", want)
		}
	}
}

// TestBackendSpecs_CarryNoSecretStoreMap is the regression guard for the
// change that moved the vault name and the item UUIDs out of this public
// repository. A vault name plus a set of item UUIDs is an internal map of
// the secret store; neither is a secret on its own, which is exactly why
// it is easy to paste one back in.
//
// The check is structural rather than a denylist of the specific strings
// that used to live here: a denylist would have to spell out the very
// identifiers this change exists to remove, re-committing them to a public
// repository in the test that guards against them.
func TestBackendSpecs_CarryNoSecretStoreMap(t *testing.T) {
	uuidLike := regexp.MustCompile(`^[a-z0-9]{26}$`)
	envNameLike := regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

	for name, spec := range backendSpecs {
		if uuidLike.MatchString(spec.ItemEnv) {
			t.Errorf("backendSpecs[%q].ItemEnv is a literal 1Password item UUID; it must name an env var (%s...) instead",
				name, itemEnvPrefix)
		}
		if !envNameLike.MatchString(spec.ItemEnv) {
			t.Errorf("backendSpecs[%q].ItemEnv = %q is not a valid environment variable name", name, spec.ItemEnv)
		}
		if !strings.HasPrefix(spec.ItemEnv, itemEnvPrefix) {
			t.Errorf("backendSpecs[%q].ItemEnv = %q; want the %q prefix", name, spec.ItemEnv, itemEnvPrefix)
		}
		if uuidLike.MatchString(spec.FieldID) {
			t.Errorf("backendSpecs[%q].FieldID is a literal 1Password item UUID", name)
		}
	}
}

// TestResolveOPRef_Success is the happy path: both env vars exported and
// the item UUID the right shape.
func TestResolveOPRef_Success(t *testing.T) {
	t.Setenv(vaultEnv, "test-vault")
	t.Setenv(testItemEnv, testItemUUID)

	vault, item, err := resolveOPRef(demoSpec{ItemEnv: testItemEnv})
	if err != nil {
		t.Fatalf("resolveOPRef: %v", err)
	}
	if vault != "test-vault" {
		t.Errorf("vault = %q, want test-vault", vault)
	}
	if item != testItemUUID {
		t.Errorf("item = %q, want %q", item, testItemUUID)
	}
}

// TestResolveOPRef_TrimsWhitespace covers the common `export VAR="uuid "`
// paste, which would otherwise produce a 27-char value and a confusing
// length error.
func TestResolveOPRef_TrimsWhitespace(t *testing.T) {
	t.Setenv(vaultEnv, "  test-vault\n")
	t.Setenv(testItemEnv, "  "+testItemUUID+"\n")

	vault, item, err := resolveOPRef(demoSpec{ItemEnv: testItemEnv})
	if err != nil {
		t.Fatalf("resolveOPRef: %v", err)
	}
	if vault != "test-vault" || item != testItemUUID {
		t.Errorf("resolveOPRef did not trim: vault=%q item=%q", vault, item)
	}
}

// TestResolveOPRef_VaultUnset confirms there is no baked-in vault default.
// A default is the internal map this indirection exists to remove.
func TestResolveOPRef_VaultUnset(t *testing.T) {
	t.Setenv(vaultEnv, "")
	t.Setenv(testItemEnv, testItemUUID)

	if _, _, err := resolveOPRef(demoSpec{ItemEnv: testItemEnv}); err == nil {
		t.Fatal("expected an error when the vault env var is unset, got nil")
	} else if !strings.Contains(err.Error(), vaultEnv) {
		t.Errorf("error should name %s so the operator knows what to export, got: %v", vaultEnv, err)
	}
}

// TestResolveOPRef_ItemUnset confirms a missing per-backend item UUID is a
// loud error naming the variable to export.
func TestResolveOPRef_ItemUnset(t *testing.T) {
	t.Setenv(vaultEnv, "test-vault")
	t.Setenv(testItemEnv, "")

	if _, _, err := resolveOPRef(demoSpec{ItemEnv: testItemEnv}); err == nil {
		t.Fatal("expected an error when the item env var is unset, got nil")
	} else if !strings.Contains(err.Error(), testItemEnv) {
		t.Errorf("error should name %s, got: %v", testItemEnv, err)
	}
}

// TestResolveOPRef_ItemWrongLength enforces `1password-uuid-required.mdc`
// on the resolved value: `op read` also accepts a display name, which
// leaks more than a UUID and is easy to export by mistake.
func TestResolveOPRef_ItemWrongLength(t *testing.T) {
	t.Setenv(vaultEnv, "test-vault")
	t.Setenv(testItemEnv, "Some Item Display Name")

	if _, _, err := resolveOPRef(demoSpec{ItemEnv: testItemEnv}); err == nil {
		t.Fatal("expected an error for a non-UUID item reference, got nil")
	}
}

// TestResolveOPRef_ErrorNeverEchoesValue is the leak guard on the error
// path. resolveOPRef's error text flows into demoResult.ErrSnippet, which
// is printed to stdout AND appended to the NDJSON audit stream on disk. An
// operator who pastes the API key into HLXN_OP_ITEM_* instead of the item
// UUID must not have that key written to a log file — so the error may
// report the length but never the value.
func TestResolveOPRef_ErrorNeverEchoesValue(t *testing.T) {
	const pastedSecret = "sk-live-not-a-uuid-000000000000000000"
	t.Setenv(vaultEnv, "test-vault")
	t.Setenv(testItemEnv, pastedSecret)

	_, _, err := resolveOPRef(demoSpec{ItemEnv: testItemEnv})
	if err == nil {
		t.Fatal("expected an error for a non-UUID item reference, got nil")
	}
	if strings.Contains(err.Error(), pastedSecret) {
		t.Errorf("error text echoes the env var value into the audit stream: %v", err)
	}
	wantLen := strconv.Itoa(len(pastedSecret))
	if !strings.Contains(err.Error(), wantLen) {
		t.Errorf("error should report the observed length (%s) so the operator can debug, got: %v", wantLen, err)
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
		ItemEnv:   testItemEnv,
		FieldID:   "password",
		BaseURL:   srv.URL + "/v1",
		ModelName: "MiniMax-M3",
	}

	// The vault/item reference now comes from the environment, so the test
	// must export it or runDemoOnce short-circuits at resolveOPRef.
	t.Setenv(vaultEnv, "test-vault")
	t.Setenv(testItemEnv, testItemUUID)

	// Swap opReadSecret to a stub that returns a known bearer, and assert it
	// receives exactly what resolveOPRef produced.
	origRead := opReadSecretFn
	var gotVault, gotItem, gotField string
	opReadSecretFn = func(vault, item, field string) (string, error) {
		gotVault, gotItem, gotField = vault, item, field
		return "stub-bearer-token", nil
	}
	defer func() { opReadSecretFn = origRead }()

	home := t.TempDir()
	t.Setenv("HOME", home)

	res := runDemoOnce(spec, "minimax")
	if res.Status != "ok" {
		t.Fatalf("expected status=ok, got %q (err=%q)", res.Status, res.ErrSnippet)
	}
	if gotVault != "test-vault" || gotItem != testItemUUID || gotField != "password" {
		t.Errorf("opReadSecret got (%q, %q, %q), want (test-vault, %q, password)",
			gotVault, gotItem, gotField, testItemUUID)
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
//
// The env vars are exported so the run reaches opReadSecretFn. Without
// them resolveOPRef fails first and the test would still see op-error —
// passing while exercising nothing, so ErrSnippet is asserted too.
func TestRunDemoOnce_OpErrorWhenNoOpRead(t *testing.T) {
	t.Setenv(vaultEnv, "test-vault")
	t.Setenv(testItemEnv, testItemUUID)

	origRead := opReadSecretFn
	opReadSecretFn = func(_, _, _ string) (string, error) {
		return "", &demoOpError{msg: "simulated op failure"}
	}
	defer func() { opReadSecretFn = origRead }()

	res := runDemoOnce(demoSpec{
		ItemEnv:   testItemEnv,
		FieldID:   "password",
		BaseURL:   "http://localhost:0",
		ModelName: "MiniMax-M3",
	}, "minimax")
	if res.Status != "op-error" {
		t.Errorf("expected status=op-error, got %q", res.Status)
	}
	if !strings.Contains(res.ErrSnippet, "simulated op failure") {
		t.Errorf("ErrSnippet = %q; want the stubbed op failure, not a resolveOPRef error", res.ErrSnippet)
	}
}

// TestRunDemoOnce_OpErrorWhenVaultUnset covers the new failure mode this
// change introduces: an operator who has not exported the vault gets a
// clean op-error naming the variable, not a panic or a silent `op read`
// against the wrong vault.
func TestRunDemoOnce_OpErrorWhenVaultUnset(t *testing.T) {
	t.Setenv(vaultEnv, "")
	t.Setenv(testItemEnv, testItemUUID)

	origRead := opReadSecretFn
	called := false
	opReadSecretFn = func(_, _, _ string) (string, error) {
		called = true
		return "should-not-be-reached", nil
	}
	defer func() { opReadSecretFn = origRead }()

	res := runDemoOnce(demoSpec{
		ItemEnv:   testItemEnv,
		FieldID:   "password",
		BaseURL:   "http://localhost:0",
		ModelName: "MiniMax-M3",
	}, "minimax")
	if res.Status != "op-error" {
		t.Errorf("expected status=op-error, got %q", res.Status)
	}
	if called {
		t.Error("opReadSecret was called despite an unresolved vault reference")
	}
	if !strings.Contains(res.ErrSnippet, vaultEnv) {
		t.Errorf("ErrSnippet = %q; want it to name %s", res.ErrSnippet, vaultEnv)
	}
}

// demoOpError is a typed error for the test stub. Implementing Error()
// on a struct is the idiomatic Go shape and matches what opReadSecret
// returns from `exec.Command`'s error path.
type demoOpError struct{ msg string }

func (e *demoOpError) Error() string { return e.msg }
