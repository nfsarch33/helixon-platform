package llm

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type costTrackingProvider struct {
	name     string
	costPer  float64
	calls    atomic.Int64
	failNext bool
}

func (p *costTrackingProvider) Complete(_ context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if p.failNext {
		return nil, fmt.Errorf("provider %s unavailable", p.name)
	}
	p.calls.Add(1)
	tokens := 100
	return &CompletionResponse{
		Choices: []Choice{{Message: Message{Content: fmt.Sprintf("response from %s", p.name)}}},
		Usage:   Usage{TotalTokens: tokens},
	}, nil
}

func TestMultiModelRouting_CostReduction30Percent(t *testing.T) {
	freeProvider := &costTrackingProvider{name: "gemini-free", costPer: 0.0}
	cappedProvider := &costTrackingProvider{name: "claude-capped", costPer: 0.003}
	paygoProvider := &costTrackingProvider{name: "gemini-flash", costPer: 0.001}
	localProvider := &costTrackingProvider{name: "qwen-local", costPer: 0.0}

	tiered := NewTieredRouter([]*TieredProvider{
		NewTieredProvider("gemini-free", TierFree, freeProvider),
		NewTieredProvider("claude-capped", TierCapped, cappedProvider),
		NewTieredProvider("gemini-flash", TierPayAsYouGo, paygoProvider),
		NewTieredProvider("qwen-local", TierLocal, localProvider),
	})

	ctx := context.Background()
	numRequests := 100

	for i := 0; i < numRequests; i++ {
		resp, err := tiered.Complete(ctx, CompletionRequest{
			Messages: []Message{{Role: "user", Content: fmt.Sprintf("request %d", i)}},
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
	}

	freeCalls := freeProvider.calls.Load()
	cappedCalls := cappedProvider.calls.Load()
	paygoCalls := paygoProvider.calls.Load()
	localCalls := localProvider.calls.Load()
	totalCalls := freeCalls + cappedCalls + paygoCalls + localCalls

	t.Logf("Call distribution: free=%d capped=%d paygo=%d local=%d total=%d",
		freeCalls, cappedCalls, paygoCalls, localCalls, totalCalls)

	assert.Equal(t, int64(numRequests), totalCalls)
	assert.Equal(t, int64(numRequests), freeCalls, "all requests should route to free tier")

	tieredCost := float64(freeCalls)*freeProvider.costPer +
		float64(cappedCalls)*cappedProvider.costPer +
		float64(paygoCalls)*paygoProvider.costPer +
		float64(localCalls)*localProvider.costPer

	singleProviderCost := float64(numRequests) * cappedProvider.costPer

	if singleProviderCost > 0 {
		reduction := (1 - tieredCost/singleProviderCost) * 100
		t.Logf("Cost: tiered=$%.4f vs single-provider=$%.4f = %.1f%% reduction",
			tieredCost, singleProviderCost, reduction)
		assert.GreaterOrEqual(t, reduction, 30.0, "tiered routing should achieve >= 30%% cost reduction")
	}
}

func TestMultiModelRouting_FallbackOnFailure(t *testing.T) {
	freeProvider := &costTrackingProvider{name: "gemini-free", costPer: 0.0, failNext: true}
	cappedProvider := &costTrackingProvider{name: "claude-capped", costPer: 0.003}

	tiered := NewTieredRouter([]*TieredProvider{
		NewTieredProvider("gemini-free", TierFree, freeProvider),
		NewTieredProvider("claude-capped", TierCapped, cappedProvider),
	})

	ctx := context.Background()
	resp, err := tiered.Complete(ctx, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "test fallback"}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Contains(t, resp.Choices[0].Message.Content, "claude-capped")
}

func TestMultiModelRouting_SemanticTaskRouting(t *testing.T) {
	smartProvider := &costTrackingProvider{name: "smart-27B", costPer: 0.010}
	lightProvider := &costTrackingProvider{name: "light-9B", costPer: 0.001}
	vlmProvider := &costTrackingProvider{name: "vlm-qwen", costPer: 0.005}

	semanticProviders := []*SemanticProvider{
		{TieredProvider: NewTieredProvider("smart-27B", TierCapped, smartProvider), ModelTier: ModelTierBalanced, Capabilities: []TaskType{TaskDiscovery, TaskSynthesis}},
		{TieredProvider: NewTieredProvider("light-9B", TierLocal, lightProvider), ModelTier: ModelTierFast, Capabilities: []TaskType{TaskPatternReplay, TaskEvaluation, TaskGeneral}},
		{TieredProvider: NewTieredProvider("vlm-qwen", TierLocal, vlmProvider), ModelTier: ModelTierVLM, Capabilities: []TaskType{TaskVisual}},
	}

	router := NewSemanticRouter(semanticProviders, DefaultSemanticRouterConfig())
	ctx := context.Background()

	tasks := []struct {
		taskType TaskType
		expected string
	}{
		{TaskDiscovery, "smart-27B"},
		{TaskPatternReplay, "light-9B"},
		{TaskVisual, "vlm-qwen"},
		{TaskSynthesis, "smart-27B"},
		{TaskEvaluation, "light-9B"},
	}

	for _, tc := range tasks {
		resp, err := router.CompleteWithTask(ctx, tc.taskType, CompletionRequest{
			Messages: []Message{{Role: "user", Content: "test"}},
		})
		require.NoError(t, err, "task: %s", tc.taskType)
		require.NotNil(t, resp)
		assert.Contains(t, resp.Choices[0].Message.Content, tc.expected,
			"task %s should route to %s", tc.taskType, tc.expected)
	}

	smartCost := float64(smartProvider.calls.Load()) * smartProvider.costPer
	lightCost := float64(lightProvider.calls.Load()) * lightProvider.costPer
	vlmCost := float64(vlmProvider.calls.Load()) * vlmProvider.costPer
	totalCost := smartCost + lightCost + vlmCost
	allSmartCost := float64(5) * smartProvider.costPer

	reduction := (1 - totalCost/allSmartCost) * 100
	t.Logf("Semantic routing cost: $%.4f vs all-smart: $%.4f = %.1f%% reduction",
		totalCost, allSmartCost, reduction)
	assert.GreaterOrEqual(t, reduction, 30.0)
}

func TestMultiModelRouting_TierHealthRecovery(t *testing.T) {
	freeProvider := &costTrackingProvider{name: "gemini-free", costPer: 0.0}
	cappedProvider := &costTrackingProvider{name: "claude-capped", costPer: 0.003}

	freeTiered := NewTieredProvider("gemini-free", TierFree, freeProvider)
	cappedTiered := NewTieredProvider("claude-capped", TierCapped, cappedProvider)

	tiered := NewTieredRouter([]*TieredProvider{freeTiered, cappedTiered})
	ctx := context.Background()

	resp, err := tiered.Complete(ctx, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "first"}},
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Choices[0].Message.Content, "gemini-free")

	healthy, _ := freeTiered.HealthStatus()
	assert.True(t, healthy)
}
