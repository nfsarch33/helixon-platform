package helixon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/memory"
	"github.com/nfsarch33/helixon-platform/internal/llm"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// A fake Engram that serves the CANONICAL daemon dialect and REFUSES anything
// else.
//
// This client has been broken by dialect drift before: it spoke a mem0-shaped
// role/content body against a daemon that wanted plain strings, and a test that
// merely accepted whatever arrived would have stayed green through all of it.
// So every field below is asserted, and a body in the wrong shape is answered
// with 400 rather than politely absorbed.
//
//	POST /memories  {"messages":["<text>"],"app_id","user_id","workspace_id","infer":false} -> 201, JSON ARRAY
//	POST /search    {"query":"...","app_id","user_id","workspace_id","top_k":N}             -> 200, [{record,score}]
//
// The search field is `query`. It is NOT `text`.
// ---------------------------------------------------------------------------

type fakeEngram struct {
	mu sync.Mutex

	searchBodies []map[string]any
	addBodies    []map[string]any
	rejections   []string
	// recall is returned from /search as the single stored record.
	recall string
	// fail makes every route answer 500, standing in for a dead backend.
	fail bool
}

func (f *fakeEngram) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /memories", func(w http.ResponseWriter, r *http.Request) {
		body, ok := f.decode(t, w, r)
		if !ok {
			return
		}
		f.mu.Lock()
		f.addBodies = append(f.addBodies, body)
		bad := f.assertAddDialectLocked(body)
		f.mu.Unlock()
		if bad != "" {
			http.Error(w, bad, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":           "mem-1",
			"text":         firstMessage(body),
			"app_id":       body["app_id"],
			"user_id":      body["user_id"],
			"workspace_id": body["workspace_id"],
		}})
	})
	mux.HandleFunc("POST /search", func(w http.ResponseWriter, r *http.Request) {
		body, ok := f.decode(t, w, r)
		if !ok {
			return
		}
		f.mu.Lock()
		f.searchBodies = append(f.searchBodies, body)
		bad := f.assertSearchDialectLocked(body)
		recall := f.recall
		f.mu.Unlock()
		if bad != "" {
			http.Error(w, bad, http.StatusBadRequest)
			return
		}
		out := []map[string]any{}
		if recall != "" {
			out = append(out, map[string]any{
				"record": map[string]any{
					"id":           "mem-0",
					"text":         recall,
					"workspace_id": body["workspace_id"],
				},
				"score": 0.93,
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeEngram) decode(t *testing.T, w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	t.Helper()
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()
	if fail {
		w.WriteHeader(http.StatusInternalServerError)
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return nil, false
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, "not json", http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// assertAddDialectLocked returns a non-empty reason when the body is not the
// canonical add shape.
func (f *fakeEngram) assertAddDialectLocked(body map[string]any) string {
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return `"messages" must be a non-empty array`
	}
	// The regression that broke this client: messages as {role,content}
	// objects instead of plain strings.
	if _, isString := msgs[0].(string); !isString {
		f.rejections = append(f.rejections, "messages entries are not plain strings")
		return `"messages" entries must be plain strings, not role/content objects`
	}
	if _, has := body["workspace_id"]; !has {
		f.rejections = append(f.rejections, "no workspace_id")
		return `"workspace_id" is required`
	}
	if infer, has := body["infer"].(bool); !has || infer {
		f.rejections = append(f.rejections, "infer not false")
		return `"infer" must be present and false`
	}
	return ""
}

func (f *fakeEngram) assertSearchDialectLocked(body map[string]any) string {
	if _, wrong := body["text"]; wrong {
		f.rejections = append(f.rejections, "search sent text instead of query")
		return `search takes "query", not "text"`
	}
	q, ok := body["query"].(string)
	if !ok || q == "" {
		f.rejections = append(f.rejections, "search has no query")
		return `"query" is required`
	}
	if _, has := body["top_k"]; !has {
		f.rejections = append(f.rejections, "search has no top_k")
		return `"top_k" is required`
	}
	if _, has := body["workspace_id"]; !has {
		f.rejections = append(f.rejections, "search has no workspace_id")
		return `"workspace_id" is required`
	}
	return ""
}

func firstMessage(body map[string]any) string {
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return ""
	}
	s, _ := msgs[0].(string)
	return s
}

func (f *fakeEngram) snapshot() (searches, adds []map[string]any, rejections []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.searchBodies...),
		append([]map[string]any(nil), f.addBodies...),
		append([]string(nil), f.rejections...)
}

// ---------------------------------------------------------------------------
// A provider that answers once and records the prompt it was given.
// ---------------------------------------------------------------------------

type recordingProvider struct {
	mu       sync.Mutex
	messages []llm.Message
	reply    string
	err      error
}

func (p *recordingProvider) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	p.messages = append(p.messages, req.Messages...)
	p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	return &llm.CompletionResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: p.reply}}},
		Usage:   llm.Usage{PromptTokens: 11, CompletionTokens: 7},
	}, nil
}

func (p *recordingProvider) sawContaining(needle string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.messages {
		if strings.Contains(m.Content, needle) {
			return true
		}
	}
	return false
}

// memoryRuntime builds a runtime whose ticket path is wired to engramURL, or to
// nothing when engramURL is "".
func memoryRuntime(t *testing.T, provider llm.Provider, engramURL string) *Runtime {
	t.Helper()
	cfg := RuntimeConfig{
		AgentID:    "mem-agent",
		TenantID:   "ws-42",
		SessionDSN: "file:" + filepath.Join(t.TempDir(), "mem.db") + "?cache=shared&mode=rwc",
		Timeout:    20 * time.Second,
		Logger:     quietLogger(),
		Memory: LoopMemoryConfig{
			Enabled:    true,
			EngramURL:  engramURL,
			AppID:      "helixon",
			MaxContext: 3,
			Timeout:    3 * time.Second,
		},
	}
	rt := NewRuntime(provider, cfg)
	ctx := context.Background()
	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	opts := []ConfigOption{}
	if engramURL != "" {
		client := memory.NewEngramClient(memory.EngramConfig{BaseURL: engramURL, Timeout: 3 * time.Second}, quietLogger())
		searcher := memory.NewHybridSearcher(nil, client, memory.HybridSearchConfig{MaxResults: 3}, quietLogger())
		opts = append(opts, WithAgentMemory(memory.NewAgentMemory(searcher, memory.AgentMemoryConfig{
			AppID: "helixon", UserID: "mem-agent", TenantID: "ws-42", MaxContext: 3, Logger: quietLogger(),
		})))
	}
	if err := rt.Configure(ctx, opts...); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return rt
}

var memTicket = controlplane.Ticket{
	ID: "T-77", Title: "wire loop memory", Status: "ready",
	AcceptanceCriteria: "recall before, summary after",
}

// TestLoopMemoryRecallsBeforeAndStoresAfter is the S-10 acceptance test: one
// ticket run, against a server that enforces the canonical Engram dialect.
func TestLoopMemoryRecallsBeforeAndStoresAfter(t *testing.T) {
	eng := &fakeEngram{recall: "last time this ticket needed GOFLAGS=-mod=mod"}
	srv := eng.server(t)
	provider := &recordingProvider{reply: "did the thing; verifier green"}
	rt := memoryRuntime(t, provider, srv.URL)

	out, err := rt.runTicketWork(context.Background(), memTicket)
	if err != nil {
		t.Fatalf("runTicketWork: %v", err)
	}
	if out != "did the thing; verifier green" {
		t.Fatalf("final content = %q", out)
	}

	searches, adds, rejections := eng.snapshot()
	if len(rejections) != 0 {
		t.Fatalf("the client spoke a non-canonical dialect: %v", rejections)
	}
	if len(searches) == 0 {
		t.Fatal("no /search was issued; nothing recalled memory before the run")
	}
	if len(adds) == 0 {
		t.Fatal("no /memories was issued; nothing stored a summary after the run")
	}

	// Recall must reach the MODEL, not merely be fetched and dropped.
	if !provider.sawContaining("last time this ticket needed GOFLAGS=-mod=mod") {
		t.Error("recalled memory never reached the prompt the model was given")
	}
	if !provider.sawContaining("wire loop memory") {
		t.Error("the ticket itself did not reach the prompt")
	}

	// The stored summary must identify the ticket and its outcome.
	stored := firstMessage(adds[0])
	for _, want := range []string{"mem-agent", "T-77", "completed", "did the thing"} {
		if !strings.Contains(stored, want) {
			t.Errorf("stored summary %q does not mention %q", stored, want)
		}
	}
	if got := adds[0]["workspace_id"]; got != "ws-42" {
		t.Errorf("summary stored under workspace_id %v, want ws-42", got)
	}
	if got := searches[0]["workspace_id"]; got != "ws-42" {
		t.Errorf("search scoped to workspace_id %v, want ws-42", got)
	}
}

// TestLoopMemoryUsesTheCanonicalSearchField is the dialect control.
//
// A test that only asserted "a search happened" would pass against the wrong
// dialect, which is exactly how this client was broken before. This one names
// the field.
func TestLoopMemoryUsesTheCanonicalSearchField(t *testing.T) {
	eng := &fakeEngram{recall: "prior context"}
	srv := eng.server(t)
	rt := memoryRuntime(t, &recordingProvider{reply: "ok"}, srv.URL)

	if _, err := rt.runTicketWork(context.Background(), memTicket); err != nil {
		t.Fatalf("runTicketWork: %v", err)
	}
	searches, adds, rejections := eng.snapshot()
	// The server answers a non-canonical body with 400 rather than absorbing
	// it. Asserting the rejection log is what makes that refusal count: without
	// it, a dropped field would fail the CALL while every field assertion below
	// still passed against the body that was recorded before the rejection.
	if len(rejections) != 0 {
		t.Fatalf("the client spoke a non-canonical dialect: %v", rejections)
	}
	if len(searches) == 0 {
		t.Fatal("no search issued")
	}
	if _, wrong := searches[0]["text"]; wrong {
		t.Error(`search body carries "text"; the canonical daemon field is "query"`)
	}
	if q, ok := searches[0]["query"].(string); !ok || q == "" {
		t.Errorf(`search body has no "query" string: %v`, searches[0])
	}
	if _, ok := searches[0]["top_k"]; !ok {
		t.Errorf(`search body has no "top_k": %v`, searches[0])
	}
	// workspace_id is the tenant boundary on the wire. Dropping it does not
	// break a call — it silently widens the query across every workspace.
	if ws, ok := searches[0]["workspace_id"].(string); !ok || ws != "ws-42" {
		t.Errorf(`search body workspace_id = %v, want "ws-42"; without it the query is not scoped to a tenant`, searches[0]["workspace_id"])
	}
	if _, ok := searches[0]["app_id"]; !ok {
		t.Errorf(`search body has no "app_id": %v`, searches[0])
	}
	if _, ok := searches[0]["user_id"]; !ok {
		t.Errorf(`search body has no "user_id": %v`, searches[0])
	}
	if len(adds) == 0 {
		t.Fatal("no add issued")
	}
	msgs, ok := adds[0]["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf(`add body "messages" is not a non-empty array: %v`, adds[0])
	}
	if _, isString := msgs[0].(string); !isString {
		t.Errorf(`add body "messages" entries are %T, want plain strings`, msgs[0])
	}
	if infer, _ := adds[0]["infer"].(bool); infer {
		t.Error(`add body sent "infer": true; the canonical call sets it false`)
	}
	if ws, ok := adds[0]["workspace_id"].(string); !ok || ws != "ws-42" {
		t.Errorf(`add body workspace_id = %v, want "ws-42"; an unscoped write is visible to every tenant`, adds[0]["workspace_id"])
	}
}

// TestDeadEngramDoesNotFailTheTicket is the load-bearing S-10 safety property.
// A memory backend being down degrades the agent; it must never decide the
// verdict of a ticket.
func TestDeadEngramDoesNotFailTheTicket(t *testing.T) {
	eng := &fakeEngram{fail: true}
	srv := eng.server(t)
	provider := &recordingProvider{reply: "work finished despite no memory"}
	rt := memoryRuntime(t, provider, srv.URL)

	out, err := rt.runTicketWork(context.Background(), memTicket)
	if err != nil {
		t.Fatalf("a dead memory backend failed the ticket: %v", err)
	}
	if out != "work finished despite no memory" {
		t.Fatalf("final content = %q, want the agent's own answer", out)
	}
	if !provider.sawContaining("wire loop memory") {
		t.Error("the ticket prompt did not reach the model when memory was down")
	}
}

// TestUnreachableEngramDoesNotFailTheTicket covers the harsher case: not a 500,
// but nothing listening at all.
func TestUnreachableEngramDoesNotFailTheTicket(t *testing.T) {
	// A port that is closed: httptest hands out a real address, then closes it.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()

	provider := &recordingProvider{reply: "still finished"}
	rt := memoryRuntime(t, provider, url)

	out, err := rt.runTicketWork(context.Background(), memTicket)
	if err != nil {
		t.Fatalf("an unreachable memory backend failed the ticket: %v", err)
	}
	if out != "still finished" {
		t.Fatalf("final content = %q", out)
	}
}

// TestNoMemoryWiredLeavesThePromptAlone is the positive control for the recall
// assertions: with WithAgentMemory removed, nothing may be prepended. Without
// it, "recall reached the prompt" could be satisfied by any text that happened
// to be in the ticket.
func TestNoMemoryWiredLeavesThePromptAlone(t *testing.T) {
	provider := &recordingProvider{reply: "ok"}
	rt := memoryRuntime(t, provider, "")
	if rt.AgentMemory() != nil {
		t.Fatal("memory was wired despite no Engram URL")
	}

	if _, err := rt.runTicketWork(context.Background(), memTicket); err != nil {
		t.Fatalf("runTicketWork: %v", err)
	}
	if provider.sawContaining("<relevant_memories>") {
		t.Error("a runtime with no memory wired still prepended a memories block")
	}
	if !provider.sawContaining("wire loop memory") {
		t.Error("the ticket prompt did not reach the model")
	}
}

// TestFailedRunIsStillRemembered: the runs worth remembering are the ones that
// went wrong, so a failure must be written down — with the failure named.
func TestFailedRunIsStillRemembered(t *testing.T) {
	eng := &fakeEngram{}
	srv := eng.server(t)
	provider := &recordingProvider{err: errors.New("provider hung up")}
	rt := memoryRuntime(t, provider, srv.URL)

	if _, err := rt.runTicketWork(context.Background(), memTicket); err == nil {
		t.Fatal("runTicketWork returned nil for a failing provider")
	}
	_, adds, rejections := eng.snapshot()
	if len(rejections) != 0 {
		t.Fatalf("non-canonical dialect: %v", rejections)
	}
	if len(adds) == 0 {
		t.Fatal("a failed run stored nothing; memory would learn that everything works")
	}
	stored := firstMessage(adds[0])
	if !strings.Contains(stored, "FAILED") || !strings.Contains(stored, "provider hung up") {
		t.Errorf("stored summary %q does not name the failure", stored)
	}
}

// TestTicketMemorySummaryIsBounded: agent output is model-controlled and
// unbounded; what reaches a vector store must not be.
func TestTicketMemorySummaryIsBounded(t *testing.T) {
	huge := strings.Repeat("x", 50_000)
	big := controlplane.Ticket{ID: "T-1", Title: strings.Repeat("t", 5_000)}
	got := TicketMemorySummary("agent", big, huge, nil)
	if len(got) > memorySummaryTitleBytes+memorySummaryResultBytes+512 {
		t.Errorf("summary is %d bytes; model-controlled text reached the store unbounded", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("an over-long summary was not marked as truncated")
	}
	if TicketMemorySummary("agent", big, "   ", nil) != "" {
		t.Error("an empty successful result produced a summary worth storing")
	}
}

// TestMemoryConfigNeedsAServerBeforeItActivates: `enabled` defaults true, so
// URL-presence is what stops an upgraded binary acquiring a network
// destination nobody configured.
func TestMemoryConfigNeedsAServerBeforeItActivates(t *testing.T) {
	if (LoopMemoryConfig{Enabled: true}).Active() {
		t.Error("memory activated with no engram_url configured")
	}
	if (LoopMemoryConfig{Enabled: false, EngramURL: "http://x"}).Active() {
		t.Error("enabled: false did not switch memory off")
	}
	if !(LoopMemoryConfig{Enabled: true, EngramURL: "http://x"}).Active() {
		t.Error("memory did not activate with both a flag and a URL")
	}
	if (LoopMemoryConfig{Enabled: true, EngramURL: "   "}).Active() {
		t.Error("whitespace passed for a configured server")
	}
}

// TestMemoryTimeoutIsNeverZero: context.WithTimeout(ctx, 0) is already expired,
// so a zero default would make memory fail silently and always.
func TestMemoryTimeoutIsNeverZero(t *testing.T) {
	cfg := RuntimeConfig{}.withDefaults()
	if cfg.Memory.Timeout <= 0 {
		t.Fatalf("default memory timeout = %v; every memory call would expire before it was made", cfg.Memory.Timeout)
	}
	if cfg.Memory.MaxContext <= 0 || cfg.Memory.AppID == "" {
		t.Errorf("memory defaults incomplete: %+v", cfg.Memory)
	}
}
