package llm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// latencyProvider wraps a Provider and adds configurable latency before delegating.
type latencyProvider struct {
	inner   Provider
	latency time.Duration
}

func (p *latencyProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(p.latency):
	}
	return p.inner.Complete(ctx, req)
}

// switchableProvider allows swapping the underlying provider at runtime (for recovery tests).
type switchableProvider struct {
	mu sync.Mutex
	p  Provider
}

func (s *switchableProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	s.mu.Lock()
	p := s.p
	s.mu.Unlock()
	return p.Complete(ctx, req)
}

func (s *switchableProvider) Set(p Provider) {
	s.mu.Lock()
	s.p = p
	s.mu.Unlock()
}

func TestStackIntegration_TieredRouterFallback(t *testing.T) {
	failErr := errors.New("provider down")

	tier1 := NewTieredProvider("free", TierFree, &testProvider{err: failErr})
	tier2 := NewTieredProvider("capped", TierCapped, &testProvider{err: failErr})
	tier3 := NewTieredProvider("paygo", TierPayAsYouGo, &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "from-paygo"}}},
		},
	})
	tier4 := NewTieredProvider("local", TierLocal, &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "from-local"}}},
		},
	})

	router := NewTieredRouter([]*TieredProvider{tier1, tier2, tier3, tier4})
	ctx := context.Background()
	req := CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}}

	// First 3 requests: tier1 fails 3x -> unhealthy, tier2 fails 3x -> unhealthy, tier3 succeeds
	for i := 0; i < 3; i++ {
		resp, err := router.Complete(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, "from-paygo", resp.Choices[0].Message.Content)
	}

	// Verify tier1 and tier2 are marked unhealthy
	healthy1, failCount1 := tier1.HealthStatus()
	healthy2, failCount2 := tier2.HealthStatus()
	assert.False(t, healthy1, "tier1 should be unhealthy after 3 failures")
	assert.GreaterOrEqual(t, failCount1, 3)
	assert.False(t, healthy2, "tier2 should be unhealthy after 3 failures")
	assert.GreaterOrEqual(t, failCount2, 3)

	// Fourth request: tier1 and tier2 skipped (unhealthy), tier3 succeeds
	resp, err := router.Complete(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "from-paygo", resp.Choices[0].Message.Content)
}

func TestStackIntegration_SemanticRouterTaskDispatch(t *testing.T) {
	fast := &SemanticProvider{
		TieredProvider: semanticTestProvider("fast", "from-fast", nil),
		ModelTier:      ModelTierFast,
		Capabilities:   []TaskType{TaskPatternReplay, TaskEvaluation, TaskGeneral},
	}
	balanced := &SemanticProvider{
		TieredProvider: semanticTestProvider("balanced", "from-balanced", nil),
		ModelTier:      ModelTierBalanced,
		Capabilities:   []TaskType{TaskDiscovery, TaskPatternReplay, TaskSynthesis, TaskEvaluation, TaskGeneral},
	}
	powerful := &SemanticProvider{
		TieredProvider: semanticTestProvider("powerful", "from-powerful", nil),
		ModelTier:      ModelTierPowerful,
		Capabilities:   []TaskType{TaskDiscovery, TaskVisual, TaskSynthesis, TaskGeneral},
	}
	vlm := &SemanticProvider{
		TieredProvider: semanticTestProvider("vlm", "from-vlm", nil),
		ModelTier:      ModelTierVLM,
		Capabilities:   []TaskType{TaskVisual},
	}

	cfg := DefaultSemanticRouterConfig()
	router := NewSemanticRouter([]*SemanticProvider{fast, balanced, powerful, vlm}, cfg)
	ctx := context.Background()
	req := CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}}

	// TaskDiscovery prefers [Powerful, Balanced] -> powerful first
	resp, err := router.CompleteWithTask(ctx, TaskDiscovery, req)
	require.NoError(t, err)
	assert.Equal(t, "from-powerful", resp.Choices[0].Message.Content)

	// TaskPatternReplay prefers [Fast, Balanced] -> fast first
	resp, err = router.CompleteWithTask(ctx, TaskPatternReplay, req)
	require.NoError(t, err)
	assert.Equal(t, "from-fast", resp.Choices[0].Message.Content)

	// TaskVisual prefers [VLM, Powerful] -> vlm first
	resp, err = router.CompleteWithTask(ctx, TaskVisual, req)
	require.NoError(t, err)
	assert.Equal(t, "from-vlm", resp.Choices[0].Message.Content)

	// TaskGeneral prefers [Balanced, Fast, Powerful] -> balanced first
	resp, err = router.CompleteWithTask(ctx, TaskGeneral, req)
	require.NoError(t, err)
	assert.Equal(t, "from-balanced", resp.Choices[0].Message.Content)
}

func TestStackIntegration_FullPipelineSimulation(t *testing.T) {
	fast := &SemanticProvider{
		TieredProvider: semanticTestProvider("fast", "pattern-replay-ok", nil),
		ModelTier:      ModelTierFast,
		Capabilities:   []TaskType{TaskPatternReplay, TaskEvaluation, TaskGeneral},
	}
	balanced := &SemanticProvider{
		TieredProvider: semanticTestProvider("balanced", "evaluation-ok", nil),
		ModelTier:      ModelTierBalanced,
		Capabilities:   []TaskType{TaskDiscovery, TaskPatternReplay, TaskSynthesis, TaskEvaluation, TaskGeneral},
	}
	powerful := &SemanticProvider{
		TieredProvider: semanticTestProvider("powerful", "discovery-ok", nil),
		ModelTier:      ModelTierPowerful,
		Capabilities:   []TaskType{TaskDiscovery, TaskVisual, TaskSynthesis, TaskGeneral},
	}
	vlm := &SemanticProvider{
		TieredProvider: semanticTestProvider("vlm", "visual-ok", nil),
		ModelTier:      ModelTierVLM,
		Capabilities:   []TaskType{TaskVisual},
	}

	cfg := DefaultSemanticRouterConfig()
	router := NewSemanticRouterWithLogger(
		[]*SemanticProvider{fast, balanced, powerful, vlm},
		cfg,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	ctx := context.Background()

	// a. Discovery request
	discoveryReq := CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "Discover new UI patterns on the page"},
			{Role: "user", Content: "find elements"},
		},
	}
	resp, err := router.Complete(ctx, discoveryReq)
	require.NoError(t, err)
	assert.Equal(t, "discovery-ok", resp.Choices[0].Message.Content)

	// b. Pattern replay request
	patternReq := CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "Execute the cached pattern"},
			{Role: "user", Content: "replay"},
		},
	}
	resp, err = router.Complete(ctx, patternReq)
	require.NoError(t, err)
	assert.Equal(t, "pattern-replay-ok", resp.Choices[0].Message.Content)

	// c. Visual request
	visualReq := CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "Analyze the screenshot and describe the layout"},
			{Role: "user", Content: "describe image"},
		},
	}
	resp, err = router.Complete(ctx, visualReq)
	require.NoError(t, err)
	assert.Equal(t, "visual-ok", resp.Choices[0].Message.Content)

	// d. Evaluation request
	evalReq := CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "Evaluate the output quality according to the rubric"},
			{Role: "user", Content: "score"},
		},
	}
	resp, err = router.Complete(ctx, evalReq)
	require.NoError(t, err)
	assert.Equal(t, "evaluation-ok", resp.Choices[0].Message.Content)

	metrics := router.Metrics()
	assert.Equal(t, int64(1), metrics.RequestsByTask[TaskDiscovery])
	assert.Equal(t, int64(1), metrics.RequestsByTask[TaskPatternReplay])
	assert.Equal(t, int64(1), metrics.RequestsByTask[TaskVisual])
	assert.Equal(t, int64(1), metrics.RequestsByTask[TaskEvaluation])
	assert.Equal(t, int64(0), metrics.Fallbacks)
}

func TestStackIntegration_HealthRecovery(t *testing.T) {
	failErr := errors.New("provider down")
	sw := &switchableProvider{p: &testProvider{err: failErr}}

	tier1 := NewTieredProvider("recoverable", TierFree, sw)
	tier1.cooldown = 30 * time.Second // will be overwritten by recordFailure; we shorten after
	tier2 := NewTieredProvider("fallback", TierCapped, &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "from-fallback"}}},
		},
	})
	router := NewTieredRouter([]*TieredProvider{tier1, tier2})
	ctx := context.Background()
	req := CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}}

	// Trigger 3 failures on tier1 to mark it unhealthy
	for i := 0; i < 3; i++ {
		_, err := router.Complete(ctx, req)
		require.NoError(t, err) // tier2 succeeds
	}

	healthy, _ := tier1.HealthStatus()
	require.False(t, healthy, "tier1 should be unhealthy")

	// Shorten cooldown for test (must lock to avoid race with isAvailable)
	tier1.mu.Lock()
	tier1.cooldown = 10 * time.Millisecond
	tier1.mu.Unlock()

	time.Sleep(20 * time.Millisecond)

	// Replace tier1's provider with a working one
	sw.Set(&testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "recovered"}}},
		},
	})

	// Next request: tier1 should be available (cooldown passed) and succeed
	resp, err := router.Complete(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "recovered", resp.Choices[0].Message.Content)

	healthy, _ = tier1.HealthStatus()
	assert.True(t, healthy, "tier1 should be healthy after success")
}

func TestStackIntegration_LatencyBenchmark(t *testing.T) {
	// Create mock providers with configurable latency
	mkProvider := func(latency time.Duration, content string) Provider {
		mock := NewMockProvider()
		mock.DefaultResp = &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: content}}},
		}
		return &latencyProvider{inner: mock, latency: latency}
	}

	tier1 := NewTieredProvider("fast", TierFree, mkProvider(1*time.Millisecond, "fast"))
	tier2 := NewTieredProvider("medium", TierCapped, mkProvider(5*time.Millisecond, "medium"))
	tier3 := NewTieredProvider("slow", TierPayAsYouGo, mkProvider(10*time.Millisecond, "slow"))
	router := NewTieredRouter([]*TieredProvider{tier1, tier2, tier3})

	ctx := context.Background()
	req := CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}}

	var latencies []time.Duration
	for i := 0; i < 100; i++ {
		start := time.Now()
		resp, err := router.Complete(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		latencies = append(latencies, time.Since(start))
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[49]
	p95 := latencies[94]

	// With 1ms tier succeeding, p50 and p95 should be close to 1ms (allow 2x for variance)
	assert.Less(t, p50.Milliseconds(), int64(5), "p50 latency should be under 5ms")
	assert.Less(t, p95.Milliseconds(), int64(15), "p95 latency should be under 15ms")
}

func TestStackIntegration_ConcurrentRequests(t *testing.T) {
	fast := &SemanticProvider{
		TieredProvider: semanticTestProvider("fast", "ok", nil),
		ModelTier:      ModelTierFast,
		Capabilities:   []TaskType{TaskGeneral, TaskPatternReplay},
	}
	balanced := &SemanticProvider{
		TieredProvider: semanticTestProvider("balanced", "ok", nil),
		ModelTier:      ModelTierBalanced,
		Capabilities:   []TaskType{TaskGeneral, TaskDiscovery},
	}
	cfg := DefaultSemanticRouterConfig()
	router := NewSemanticRouter([]*SemanticProvider{fast, balanced}, cfg)

	ctx := context.Background()
	req := CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}}

	var wg sync.WaitGroup
	results := make(chan *CompletionResponse, 50)
	errs := make(chan error, 50)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := router.CompleteWithTask(ctx, TaskGeneral, req)
			if err != nil {
				errs <- err
				return
			}
			results <- resp
		}()
	}

	wg.Wait()
	close(results)
	close(errs)

	var count int
	for r := range results {
		require.NotNil(t, r)
		assert.NotEmpty(t, r.Choices)
		assert.Equal(t, "ok", r.Choices[0].Message.Content)
		count++
	}
	for err := range errs {
		t.Errorf("unexpected error: %v", err)
	}
	assert.Equal(t, 50, count, "all 50 requests should succeed")
}

// --- Sprint 5: Full LLM stack integration (Claude CLI + local Qwen via router) ---

func TestStackIntegration_ClaudeCLI_InTieredRouter(t *testing.T) {
	claudeClient := NewClaudeCLIClient(ClaudeCLIConfig{Model: "claude-sonnet-4-20250514"})
	claudeClient.execFunc = mockExec([]byte(`{"result":"claude-says-hello","model":"claude-sonnet-4-20250514","usage":{"input_tokens":5,"output_tokens":10},"cost_usd":0.0005}`), nil)

	localQwen := NewMockProvider()
	localQwen.DefaultResp = &CompletionResponse{
		Choices: []Choice{{Message: Message{Content: "qwen-local-response"}}},
		Usage:   Usage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15},
	}

	tier1 := NewTieredProvider("gemini-free", TierFree, &testProvider{err: errors.New("gemini down")})
	tier2 := NewTieredProvider("claude-cli", TierCapped, claudeClient)
	tier3 := NewTieredProvider("gemini-api", TierPayAsYouGo, &testProvider{err: errors.New("api exhausted")})
	tier4 := NewTieredProvider("qwen-local", TierLocal, localQwen)

	router := NewTieredRouter([]*TieredProvider{tier1, tier2, tier3, tier4})

	ctx := context.Background()
	req := CompletionRequest{Messages: []Message{{Role: "user", Content: "summarize this paper"}}}

	resp, err := router.Complete(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "claude-says-hello", resp.Choices[0].Message.Content)
	assert.Equal(t, 5, resp.Usage.PromptTokens)
}

func TestStackIntegration_ClaudeCLI_FallbackToQwen(t *testing.T) {
	claudeClient := NewClaudeCLIClient(ClaudeCLIConfig{})
	claudeClient.execFunc = mockExec(nil, errors.New("claude binary not found"))

	localQwen := NewMockProvider()
	localQwen.DefaultResp = &CompletionResponse{
		Choices: []Choice{{Message: Message{Content: "qwen-fallback-ok"}}},
	}

	tier1 := NewTieredProvider("claude-cli", TierCapped, claudeClient)
	tier2 := NewTieredProvider("qwen-local", TierLocal, localQwen)
	router := NewTieredRouter([]*TieredProvider{tier1, tier2})

	ctx := context.Background()
	req := CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}}

	resp, err := router.Complete(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "qwen-fallback-ok", resp.Choices[0].Message.Content)
}

func TestStackIntegration_FullStack_SemanticWithClaudeAndQwen(t *testing.T) {
	claudeClient := NewClaudeCLIClient(ClaudeCLIConfig{Model: "claude-sonnet-4-20250514"})
	claudeClient.execFunc = mockExec(
		[]byte(`{"result":"claude-discovery-result","model":"claude-sonnet-4-20250514","usage":{"input_tokens":20,"output_tokens":50}}`),
		nil,
	)

	qwen9b := NewMockProvider()
	qwen9b.DefaultResp = &CompletionResponse{
		Choices: []Choice{{Message: Message{Content: "qwen9b-pattern-replay"}}},
	}

	qwen27b := NewMockProvider()
	qwen27b.DefaultResp = &CompletionResponse{
		Choices: []Choice{{Message: Message{Content: "qwen27b-evaluation"}}},
	}

	qwenVL := NewMockProvider()
	qwenVL.DefaultResp = &CompletionResponse{
		Choices: []Choice{{Message: Message{Content: "qwen-vl-visual"}}},
	}

	fast := &SemanticProvider{
		TieredProvider: NewTieredProvider("qwen3.5-9b", TierLocal, qwen9b),
		ModelTier:      ModelTierFast,
		Capabilities:   []TaskType{TaskPatternReplay, TaskGeneral},
	}
	balanced := &SemanticProvider{
		TieredProvider: NewTieredProvider("qwen3.5-27b", TierLocal, qwen27b),
		ModelTier:      ModelTierBalanced,
		Capabilities:   []TaskType{TaskEvaluation, TaskDiscovery, TaskGeneral},
	}
	powerful := &SemanticProvider{
		TieredProvider: NewTieredProvider("claude-cli", TierCapped, claudeClient),
		ModelTier:      ModelTierPowerful,
		Capabilities:   []TaskType{TaskDiscovery, TaskSynthesis, TaskGeneral},
	}
	vlm := &SemanticProvider{
		TieredProvider: NewTieredProvider("qwen3-vl", TierLocal, qwenVL),
		ModelTier:      ModelTierVLM,
		Capabilities:   []TaskType{TaskVisual},
	}

	cfg := DefaultSemanticRouterConfig()
	router := NewSemanticRouterWithLogger(
		[]*SemanticProvider{fast, balanced, powerful, vlm},
		cfg,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	ctx := context.Background()

	// Discovery -> powerful (Claude CLI) first
	discoveryReq := CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "Discover new UI patterns"},
			{Role: "user", Content: "find elements"},
		},
	}
	resp, err := router.CompleteWithTask(ctx, TaskDiscovery, discoveryReq)
	require.NoError(t, err)
	assert.Equal(t, "claude-discovery-result", resp.Choices[0].Message.Content)

	// Pattern replay -> fast (qwen9b)
	replayReq := CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "Execute cached pattern"},
			{Role: "user", Content: "replay"},
		},
	}
	resp, err = router.CompleteWithTask(ctx, TaskPatternReplay, replayReq)
	require.NoError(t, err)
	assert.Equal(t, "qwen9b-pattern-replay", resp.Choices[0].Message.Content)

	// Visual -> VLM (qwen-vl)
	visualReq := CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "Analyze screenshot"},
			{Role: "user", Content: "verify layout"},
		},
	}
	resp, err = router.CompleteWithTask(ctx, TaskVisual, visualReq)
	require.NoError(t, err)
	assert.Equal(t, "qwen-vl-visual", resp.Choices[0].Message.Content)

	// Evaluation -> balanced (qwen27b)
	evalReq := CompletionRequest{
		Messages: []Message{
			{Role: "system", Content: "Evaluate quality against rubric"},
			{Role: "user", Content: "score"},
		},
	}
	resp, err = router.CompleteWithTask(ctx, TaskEvaluation, evalReq)
	require.NoError(t, err)
	assert.Equal(t, "qwen27b-evaluation", resp.Choices[0].Message.Content)

	metrics := router.Metrics()
	assert.Equal(t, int64(1), metrics.RequestsByTask[TaskDiscovery])
	assert.Equal(t, int64(1), metrics.RequestsByTask[TaskPatternReplay])
	assert.Equal(t, int64(1), metrics.RequestsByTask[TaskVisual])
	assert.Equal(t, int64(1), metrics.RequestsByTask[TaskEvaluation])
	assert.Equal(t, int64(0), metrics.Fallbacks)

	assert.Equal(t, int64(1), metrics.RequestsByTier[ModelTierPowerful])
	assert.Equal(t, int64(1), metrics.RequestsByTier[ModelTierFast])
	assert.Equal(t, int64(1), metrics.RequestsByTier[ModelTierVLM])
	assert.Equal(t, int64(1), metrics.RequestsByTier[ModelTierBalanced])
}

func TestStackIntegration_QueuedSemanticRouter(t *testing.T) {
	qwen9b := NewMockProvider()
	qwen9b.DefaultResp = &CompletionResponse{
		Choices: []Choice{{Message: Message{Content: "queued-ok"}}},
	}

	fast := &SemanticProvider{
		TieredProvider: NewTieredProvider("qwen3.5-9b", TierLocal, qwen9b),
		ModelTier:      ModelTierFast,
		Capabilities:   []TaskType{TaskGeneral, TaskPatternReplay, TaskEvaluation},
	}

	cfg := DefaultSemanticRouterConfig()
	semanticRouter := NewSemanticRouter([]*SemanticProvider{fast}, cfg)

	queued := NewQueuedProvider(semanticRouter, QueueConfig{MaxConcurrent: 4, MaxPending: 20})

	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan string, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := queued.Complete(ctx, CompletionRequest{
				Messages: []Message{{Role: "user", Content: "test"}},
			})
			if err != nil {
				results <- "error:" + err.Error()
				return
			}
			results <- resp.Choices[0].Message.Content
		}()
	}

	wg.Wait()
	close(results)

	var count int
	for r := range results {
		assert.Equal(t, "queued-ok", r)
		count++
	}
	assert.Equal(t, 20, count)
}

func TestStackIntegration_ClaudeCLI_WithSemanticFallback(t *testing.T) {
	claudeClient := NewClaudeCLIClient(ClaudeCLIConfig{})
	claudeClient.execFunc = mockExec(nil, errors.New("claude rate limited"))

	qwen27b := NewMockProvider()
	qwen27b.DefaultResp = &CompletionResponse{
		Choices: []Choice{{Message: Message{Content: "qwen27b-fallback"}}},
	}

	powerful := &SemanticProvider{
		TieredProvider: NewTieredProvider("claude-cli", TierCapped, claudeClient),
		ModelTier:      ModelTierPowerful,
		Capabilities:   []TaskType{TaskDiscovery, TaskSynthesis},
	}
	balanced := &SemanticProvider{
		TieredProvider: NewTieredProvider("qwen3.5-27b", TierLocal, qwen27b),
		ModelTier:      ModelTierBalanced,
		Capabilities:   []TaskType{TaskDiscovery, TaskEvaluation, TaskGeneral},
	}

	cfg := DefaultSemanticRouterConfig()
	router := NewSemanticRouterWithLogger(
		[]*SemanticProvider{powerful, balanced},
		cfg,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	ctx := context.Background()

	// Discovery: powerful (claude) fails -> balanced (qwen27b) takes over
	resp, err := router.CompleteWithTask(ctx, TaskDiscovery, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "discover patterns"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "qwen27b-fallback", resp.Choices[0].Message.Content)

	metrics := router.Metrics()
	assert.Equal(t, int64(1), metrics.RequestsByTask[TaskDiscovery])
	assert.Equal(t, int64(1), metrics.RequestsByTier[ModelTierBalanced])
}

func TestStackIntegration_RateLimitedClaudeCLI(t *testing.T) {
	callCount := 0
	claudeClient := NewClaudeCLIClient(ClaudeCLIConfig{})
	claudeClient.execFunc = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		callCount++
		return []byte(`{"result":"ok","usage":{"input_tokens":5,"output_tokens":10}}`), nil
	}

	rl := NewRateLimitedProvider("claude-limited", claudeClient, RateLimitConfig{
		RequestsPerMinute: 60,
		Window:            time.Second,
	})
	tier := NewTieredProvider("claude-limited", TierCapped, rl)
	router := NewTieredRouter([]*TieredProvider{tier})

	ctx := context.Background()
	req := CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}}

	for i := 0; i < 5; i++ {
		resp, err := router.Complete(ctx, req)
		require.NoError(t, err, "request %d should succeed", i)
		assert.Equal(t, "ok", resp.Choices[0].Message.Content)
	}

	assert.Equal(t, 5, callCount)
}
