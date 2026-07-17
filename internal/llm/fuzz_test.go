package llm

import (
	"encoding/json"
	"testing"
	"time"
)

// FuzzClassifyTask fuzzes ClassifyTask with random system prompts.
func FuzzClassifyTask(f *testing.F) {
	seeds := []string{
		"",
		"discover elements",
		"screenshot visual image",
		"pattern replay cached",
		"mutate evolve synthesize",
		"evaluate score rubric",
		"general task",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, systemPrompt string) { //nolint:revive // unused-parameter required by interface
		req := CompletionRequest{
			Messages: []Message{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: "test"},
			},
		}
		_ = ClassifyTask(req)
	})
}

func FuzzCompletionResponseUnmarshal(f *testing.F) {
	f.Add([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"choices":[]}`))
	f.Fuzz(func(t *testing.T, data []byte) { //nolint:revive // unused-parameter required by interface
		var r CompletionResponse
		_ = json.Unmarshal(data, &r)
	})
}

func FuzzMessageMarshal(f *testing.F) {
	f.Add("user", "hello")
	f.Add("system", "You are a helpful assistant.")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, role, content string) { //nolint:revive // unused-parameter required by interface
		m := Message{Role: role, Content: content}
		_, _ = json.Marshal(m)
	})
}

func FuzzTierRoutingKeyLookup(f *testing.F) {
	f.Add("discovery")
	f.Add("pattern_replay")
	f.Add("visual")
	f.Add("unknown")
	f.Add("")
	f.Fuzz(func(t *testing.T, taskTypeStr string) { //nolint:revive // unused-parameter required by interface
		cfg := DefaultSemanticRouterConfig()
		tt := TaskType(taskTypeStr)
		_ = cfg.TaskTierMap[tt]
	})
}

func FuzzExtractSystemContent(f *testing.F) {
	f.Add(`[{"role":"system","content":"sys"}]`)
	f.Add(`[{"role":"user","content":"hi"}]`)
	f.Add(`[]`)
	f.Fuzz(func(t *testing.T, messagesJSON string) { //nolint:revive // unused-parameter required by interface
		var msgs []Message
		if json.Unmarshal([]byte(messagesJSON), &msgs) != nil {
			return
		}
		req := CompletionRequest{Messages: msgs}
		_ = ClassifyTask(req)
	})
}

func FuzzPromptBuilding(f *testing.F) {
	f.Add("short")
	f.Add(string(make([]byte, 10000)))
	f.Add("")
	f.Fuzz(func(t *testing.T, content string) { //nolint:revive // unused-parameter required by interface
		req := CompletionRequest{
			Messages: []Message{
				{Role: "system", Content: content},
				{Role: "user", Content: "test"},
			},
		}
		_ = ClassifyTask(req)
	})
}

func FuzzOllamaResponseUnmarshal(f *testing.F) {
	f.Add([]byte(`{"model":"llama","message":{"role":"assistant","content":"hi"},"done":true}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		var r struct {
			Message Message `json:"message"`
		}
		_ = json.Unmarshal(data, &r)
	})
}

// completionRequestWire is a JSON-marshallable view of CompletionRequest for fuzzing.
type completionRequestWire struct {
	Model           string      `json:"model"`
	Messages        []Message   `json:"messages"`
	Temperature     *float64    `json:"temperature,omitempty"`
	MaxTokens       *int        `json:"max_tokens,omitempty"`
	DisableThinking bool        `json:"disable_thinking,omitempty"`
	Tools           []Tool      `json:"tools,omitempty"`
	ToolChoice      *ToolChoice `json:"tool_choice,omitempty"`
}

// FuzzCompletionRequestMarshal fuzzes CompletionRequest marshal/unmarshal roundtrip.
func FuzzCompletionRequestMarshal(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{}`),
		[]byte(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"messages":[{"role":"system","content":"sys"}]}`),
	}
	for _, b := range seeds {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var w completionRequestWire
		if err := json.Unmarshal(data, &w); err != nil {
			return
		}
		req := CompletionRequest(w)
		out, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded CompletionRequest
		if err := json.Unmarshal(out, &decoded); err != nil {
			t.Fatalf("roundtrip unmarshal: %v", err)
		}
	})
}

// FuzzSemanticRouterConfig fuzzes DefaultSemanticRouterConfig task type lookups.
func FuzzSemanticRouterConfig(f *testing.F) {
	cfg := DefaultSemanticRouterConfig()
	seeds := []string{
		"", "discovery", "pattern_replay", "visual", "synthesis", "evaluation", "general",
		"unknown", "DISCOVERY", "x",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, taskTypeStr string) { //nolint:revive // unused-parameter required by interface
		tt := TaskType(taskTypeStr)
		_ = cfg.TaskTierMap[tt]
	})
}

func FuzzBuildPrompt(f *testing.F) {
	f.Add("user", "hello", "")
	f.Add("system", "be helpful", "user")
	f.Add("", "", "")
	f.Fuzz(func(t *testing.T, role1, content1, role2 string) { //nolint:revive // unused-parameter required by interface
		msgs := []Message{{Role: role1, Content: content1}}
		if role2 != "" {
			msgs = append(msgs, Message{Role: role2, Content: "test"})
		}
		_ = buildPrompt(msgs)
	})
}

func FuzzParseClaudeCLIOutput(f *testing.F) {
	f.Add([]byte(`{"result":"ok","is_error":false,"model":"claude","usage":{"input_tokens":5,"output_tokens":10},"cost_usd":0.001}`))
	f.Add([]byte(`{"result":"err","is_error":true}`))
	f.Add([]byte(`just plain text`))
	f.Add([]byte(``))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) { //nolint:revive // unused-parameter required by interface
		_, _ = parseClaudeCLIOutput(data)
	})
}

func FuzzContainsAny(f *testing.F) {
	f.Add("discover new elements on page", "discover")
	f.Add("screenshot of the UI", "visual")
	f.Add("", "")
	f.Add("nothing relevant here", "xyz")
	f.Fuzz(func(t *testing.T, s, substr string) { //nolint:revive // unused-parameter required by interface
		_ = containsAny(s, substr)
	})
}

func FuzzQueueConfigDefaults(f *testing.F) {
	f.Add(0, 0, int64(0), int64(0), int64(0))
	f.Add(100, 4, int64(512), int64(300000000000), int64(2000000000))
	f.Add(-1, -1, int64(-1), int64(-1), int64(-1))
	f.Fuzz(func(t *testing.T, maxPending, maxConcurrent int, maxMemMB, timeoutNs, backpressureNs int64) { //nolint:revive // unused-parameter required by interface
		cfg := QueueConfig{
			MaxPending:        maxPending,
			MaxConcurrent:     maxConcurrent,
			MaxMemoryMB:       maxMemMB,
			RequestTimeout:    time.Duration(timeoutNs),
			BackpressureDelay: time.Duration(backpressureNs),
		}
		_ = cfg
	})
}

func FuzzRateLimitConfigDefaults(f *testing.F) {
	f.Add(10, int64(60000000000), 2)
	f.Add(0, int64(0), 0)
	f.Add(-1, int64(-1), -1)
	f.Fuzz(func(t *testing.T, rpm int, windowNs int64, burst int) { //nolint:revive // unused-parameter required by interface
		cfg := RateLimitConfig{
			RequestsPerMinute: rpm,
			Window:            time.Duration(windowNs),
			BurstSize:         burst,
		}
		_ = cfg
	})
}

func FuzzUsageJSON(f *testing.F) {
	f.Add([]byte(`{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"prompt_tokens":-1}`))
	f.Fuzz(func(t *testing.T, data []byte) { //nolint:revive // unused-parameter required by interface
		var u Usage
		_ = json.Unmarshal(data, &u)
	})
}

func FuzzAPIErrorFormat(f *testing.F) {
	f.Add(400, "bad request")
	f.Add(429, "rate limited")
	f.Add(500, "")
	f.Add(0, "")
	f.Fuzz(func(t *testing.T, status int, body string) { //nolint:revive // unused-parameter required by interface
		e := &APIError{StatusCode: status, Body: body}
		_ = e.Error()
	})
}

func FuzzClaudeCLIConfig(f *testing.F) {
	f.Add("claude", "claude-sonnet", 0, int64(0))
	f.Add("", "", 1000, int64(300000000000))
	f.Add("/usr/local/bin/claude", "claude-opus", 4096, int64(600000000000))
	f.Fuzz(func(t *testing.T, binary, model string, maxTokens int, timeoutNs int64) {
		cfg := ClaudeCLIConfig{
			BinaryPath: binary,
			Model:      model,
			MaxTokens:  maxTokens,
			Timeout:    time.Duration(timeoutNs),
		}
		c := NewClaudeCLIClient(cfg)
		if c == nil {
			t.Fatal("expected non-nil client")
		}
	})
}

func FuzzTierOrdering(f *testing.F) {
	f.Add(1, 2, 3, 4)
	f.Add(4, 3, 2, 1)
	f.Add(1, 1, 1, 1)
	f.Fuzz(func(t *testing.T, t1, t2, t3, t4 int) {
		if t1 < 0 || t2 < 0 || t3 < 0 || t4 < 0 {
			return
		}
		providers := []*TieredProvider{
			NewTieredProvider("a", Tier(t1), NewMockProvider()),
			NewTieredProvider("b", Tier(t2), NewMockProvider()),
			NewTieredProvider("c", Tier(t3), NewMockProvider()),
			NewTieredProvider("d", Tier(t4), NewMockProvider()),
		}
		router := NewTieredRouter(providers)
		ps := router.Providers()
		for i := 1; i < len(ps); i++ {
			if ps[i].Tier < ps[i-1].Tier {
				t.Fatalf("providers not sorted: tier[%d]=%d < tier[%d]=%d", i, ps[i].Tier, i-1, ps[i-1].Tier)
			}
		}
	})
}
