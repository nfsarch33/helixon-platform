// live_source_test.go -- exercises the LiveSource against a stub HTTP
// server so the runner's live-mode plumbing is verified without any
// real API calls. The stubHandler is a minimal OpenAI-compatible chat
// completion endpoint that returns a canned body containing all four
// rubric tokens.
package helixoneval

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stubDoer is a tiny http.RoundTripper that intercepts outbound
// requests and routes them through a Handler. It also enforces a fake
// auth header so we can prove the LiveSource sets it.
type stubDoer struct {
	handler http.Handler
	lastReq *http.Request
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	s.lastReq = req
	rec := newRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func newRecorder() *rec {
	return &rec{header: http.Header{}, body: bytes.NewBuffer(nil)}
}

type rec struct {
	header http.Header
	body   *bytes.Buffer
	code   int
}

func (r *rec) Header() http.Header         { return r.header }
func (r *rec) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *rec) WriteHeader(c int)           { r.code = c }
func (r *rec) Result() *http.Response {
	return &http.Response{
		StatusCode: r.code,
		Header:     r.header,
		Body:       io.NopCloser(r.body),
	}
}

// stubHandler returns a minimal OpenAI-compatible chat completion body.
func stubHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("missing bearer auth: %q", got)
		}
		body := map[string]any{
			"id":      "stub",
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "correctness completeness termination robustness"}}},
			"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 4, "total_tokens": 5},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(body)
	})
}

// TestLiveSource_AllTokensPresent verifies that a passing response
// scores 0.85 on every rubric.
func TestLiveSource_AllTokensPresent(t *testing.T) {
	doer := &stubDoer{handler: stubHandler(t)}
	src := LiveSource{
		Endpoints: map[Model]string{
			ModelQwen37Plus: "test-key",
		},
		Now:      time.Unix(1700000000, 0),
		HTTPDoer: doer,
	}
	trace, ok := src.Fetch("long-running context retention", ModelQwen37Plus)
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	for _, id := range RubricIDs {
		if got, want := trace.RubricScores[id], 0.85; got != want {
			t.Errorf("rubric %s score = %v, want %v", id, got, want)
		}
	}
}

// TestLiveSource_NoMatchScoresZeroFiftyFive verifies the negative path.
func TestLiveSource_NoMatchScoresZeroFiftyFive(t *testing.T) {
	doer := &stubDoer{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "nope"}}},
			"usage":   map[string]int{"total_tokens": 1},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(body)
	})}
	src := LiveSource{
		Endpoints: map[Model]string{ModelQwen37Plus: "k"},
		Now:       time.Unix(1700000000, 0),
		HTTPDoer:  doer,
	}
	trace, _ := src.Fetch("long-running context retention", ModelQwen37Plus)
	for _, id := range RubricIDs {
		if got := trace.RubricScores[id]; got != 0.55 {
			t.Errorf("rubric %s = %v, want 0.55", id, got)
		}
	}
}

// TestLiveSource_NoKeyRecordsLiveNoKey ensures missing API keys surface
// as a "live_no_key" trace rather than silently dropping the case.
func TestLiveSource_NoKeyRecordsLiveNoKey(t *testing.T) {
	src := LiveSource{
		Endpoints: map[Model]string{ModelQwen37Plus: "k"},
		Now:       time.Unix(1700000000, 0),
	}
	trace, ok := src.Fetch("PlanSync PR creation", ModelMiniMaxM3)
	if !ok {
		t.Fatalf("expected ok=true with live_no_key trace")
	}
	if trace.TerminationReason != "live_no_key" {
		t.Errorf("termination=%q want live_no_key", trace.TerminationReason)
	}
}

// TestLiveSource_HTTPErrorsRecordLiveError ensures HTTP failures fall
// through to a zero-score Trace rather than blowing up the runner.
func TestLiveSource_HTTPErrorsRecordLiveError(t *testing.T) {
	doer := &stubDoer{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})}
	src := LiveSource{
		Endpoints: map[Model]string{ModelQwen37Plus: "k"},
		Now:       time.Unix(1700000000, 0),
		HTTPDoer:  doer,
	}
	trace, ok := src.Fetch("eval rubric application", ModelQwen37Plus)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if trace.TerminationReason != "live_error" {
		t.Errorf("got %q want live_error", trace.TerminationReason)
	}
}

// TestLiveSource_RoutePerModel confirms each Model hits the right URL.
func TestLiveSource_RoutePerModel(t *testing.T) {
	for _, m := range []Model{ModelQwen37Plus, ModelQwen37Max, ModelMiniMaxM3} {
		got := baseURLFor(m)
		if got == "" {
			t.Errorf("baseURLFor(%s) returned empty", m)
		}
	}
}

// TestLiveSource_BuildProbeReturnsTokens guards the deterministic
// probe contract used by the runner.
func TestLiveSource_BuildProbeReturnsTokens(t *testing.T) {
	for _, task := range GoldenTasks() {
		prompt, tokens := buildProbe(task)
		if prompt == "" {
			t.Errorf("%s: empty prompt", task)
		}
		if len(tokens) == 0 {
			t.Errorf("%s: empty tokens", task)
		}
	}
}

// TestNewLiveSourceFromEnv_SkipsMissingKeys ensures graceful degradation.
func TestNewLiveSourceFromEnv_SkipsMissingKeys(t *testing.T) {
	saved := lookupEnv
	lookupEnv = func(key string) (string, bool) {
		if key == "ALIYUN_QWEN_TOKEN_PLAN_KEY" {
			return "k1", true
		}
		return "", false
	}
	t.Cleanup(func() { lookupEnv = saved })

	src := NewLiveSourceFromEnv(DefaultLiveEndpoints(), time.Unix(1700000000, 0))
	if got := src.Endpoints[ModelQwen37Plus]; got != "k1" {
		t.Errorf("qwen3.7-plus = %q, want k1", got)
	}
	if _, ok := src.Endpoints[ModelMiniMaxM3]; ok {
		t.Errorf("MiniMax-M3 should not be set when env missing")
	}
}

// TestModelString covers the fmt.Stringer contract used by the CLI.
func TestModelString(t *testing.T) {
	cases := map[Model]string{
		ModelQwen37Plus: "qwen3.7-plus",
		ModelQwen37Max:  "qwen3.7-max",
		ModelMiniMaxM3:  "MiniMax-M3",
		ModelOfflineFix: "offline-fixture",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("Model(%s).String() = %q, want %q", m, got, want)
		}
	}
}
