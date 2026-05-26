package llm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func semanticTestProvider(name string, content string, err error) *TieredProvider {
	p := &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: content}}},
		},
		err: err,
	}
	return NewTieredProvider(name, TierLocal, p)
}

func TestSemanticRouter_CompleteWithTask_RoutesToCorrectTier(t *testing.T) {
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

	cfg := DefaultSemanticRouterConfig()
	router := NewSemanticRouter([]*SemanticProvider{fast, balanced, powerful}, cfg)

	req := CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	}
	ctx := context.Background()

	// TaskDiscovery prefers [Powerful, Balanced] -> should hit powerful first
	resp, err := router.CompleteWithTask(ctx, TaskDiscovery, req)
	require.NoError(t, err)
	assert.Equal(t, "from-powerful", resp.Choices[0].Message.Content)

	// TaskPatternReplay prefers [Fast, Balanced] -> should hit fast first
	resp, err = router.CompleteWithTask(ctx, TaskPatternReplay, req)
	require.NoError(t, err)
	assert.Equal(t, "from-fast", resp.Choices[0].Message.Content)

	// TaskEvaluation prefers [Balanced, Fast] -> should hit balanced first
	resp, err = router.CompleteWithTask(ctx, TaskEvaluation, req)
	require.NoError(t, err)
	assert.Equal(t, "from-balanced", resp.Choices[0].Message.Content)
}

func TestSemanticRouter_FallbackOnFailure(t *testing.T) {
	// Semantic providers all fail
	failing := &SemanticProvider{
		TieredProvider: semanticTestProvider("failing", "", errors.New("provider down")),
		ModelTier:      ModelTierPowerful,
		Capabilities:   []TaskType{TaskDiscovery},
	}
	// Fallback tier has a working provider (lower Tier = tried first in fallback)
	fallbackWorking := NewTieredProvider("fallback", TierFree, &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "from-fallback"}}},
		},
	})

	// Build router: semantic provider + a separate tiered provider for fallback
	// The fallback TieredRouter is built from all providers' TieredProviders.
	// So we need the failing one + a working one. Give the working one a different Tier
	// so fallback tries it. TierFree (1) < TierLocal (4), so fallback tries TierFree first.
	sp := &SemanticProvider{
		TieredProvider: failing.TieredProvider,
		ModelTier:      ModelTierPowerful,
		Capabilities:   []TaskType{TaskDiscovery},
	}
	// We need the fallback to include a working provider. The fallback is built from
	// SemanticProviders' TieredProviders only. So we need a SemanticProvider whose
	// TieredProvider works, but isn't selected for TaskDiscovery (wrong tier/capability).
	workingBalanced := &SemanticProvider{
		TieredProvider: fallbackWorking,
		ModelTier:      ModelTierBalanced,
		Capabilities:   []TaskType{TaskGeneral},
	}

	router := NewSemanticRouter([]*SemanticProvider{sp, workingBalanced}, DefaultSemanticRouterConfig())

	// TaskDiscovery wants [Powerful, Balanced]. sp (Powerful) fails. workingBalanced is
	// Balanced and has TaskGeneral but not TaskDiscovery in Capabilities. So it won't
	// be tried for TaskDiscovery. We fall through to fallback.
	// Fallback tries by Tier: TierFree (workingBalanced's TieredProvider) first.
	resp, err := router.CompleteWithTask(context.Background(), TaskDiscovery, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "from-fallback", resp.Choices[0].Message.Content)

	metrics := router.Metrics()
	assert.Equal(t, int64(1), metrics.Fallbacks)
}

func TestSemanticRouter_ClassifyTask(t *testing.T) {
	tests := []struct {
		name     string
		req      CompletionRequest
		expected TaskType
	}{
		{
			name: "discovery - discover",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Discover new UI elements on the page"},
					{Role: "user", Content: "go"},
				},
			},
			expected: TaskDiscovery,
		},
		{
			name: "discovery - find element",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Find element matching the selector"},
				},
			},
			expected: TaskDiscovery,
		},
		{
			name: "discovery - css selector",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Use CSS selector to locate the button"},
				},
			},
			expected: TaskDiscovery,
		},
		{
			name: "visual - screenshot",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Analyze the screenshot and describe the layout"},
				},
			},
			expected: TaskVisual,
		},
		{
			name: "visual - visual",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Perform visual UI analysis"},
				},
			},
			expected: TaskVisual,
		},
		{
			name: "visual - image",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Describe the image content"},
				},
			},
			expected: TaskVisual,
		},
		{
			name: "pattern_replay - pattern",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Execute the cached pattern"},
				},
			},
			expected: TaskPatternReplay,
		},
		{
			name: "pattern_replay - replay",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Replay the recorded steps"},
				},
			},
			expected: TaskPatternReplay,
		},
		{
			name: "pattern_replay - cached",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Use the cached result"},
				},
			},
			expected: TaskPatternReplay,
		},
		{
			name: "synthesis - mutate",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Mutate the DOM and evolve the component"},
				},
			},
			expected: TaskSynthesis,
		},
		{
			name: "synthesis - synthesize",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Synthesize a new variant"},
				},
			},
			expected: TaskSynthesis,
		},
		{
			name: "synthesis - evolve",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Evolve the component"},
				},
			},
			expected: TaskSynthesis,
		},
		{
			name: "evaluation - evaluate",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Evaluate the output quality"},
				},
			},
			expected: TaskEvaluation,
		},
		{
			name: "evaluation - rubric",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Score according to the rubric"},
				},
			},
			expected: TaskEvaluation,
		},
		{
			name: "evaluation - score",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "Score the output quality"},
				},
			},
			expected: TaskEvaluation,
		},
		{
			name: "general - no keywords",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "system", Content: "You are a helpful assistant"},
					{Role: "user", Content: "Hello"},
				},
			},
			expected: TaskGeneral,
		},
		{
			name: "general - no system message",
			req: CompletionRequest{
				Messages: []Message{
					{Role: "user", Content: "Hello"},
				},
			},
			expected: TaskGeneral,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyTask(tt.req)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestSemanticRouter_MetricsTracking(t *testing.T) {
	provider := &SemanticProvider{
		TieredProvider: semanticTestProvider("p", "ok", nil),
		ModelTier:      ModelTierBalanced,
		Capabilities:   []TaskType{TaskGeneral, TaskEvaluation},
	}
	router := NewSemanticRouter([]*SemanticProvider{provider}, DefaultSemanticRouterConfig())

	req := CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}}
	ctx := context.Background()

	_, err := router.CompleteWithTask(ctx, TaskGeneral, req)
	require.NoError(t, err)
	_, err = router.CompleteWithTask(ctx, TaskGeneral, req)
	require.NoError(t, err)
	_, err = router.CompleteWithTask(ctx, TaskEvaluation, req)
	require.NoError(t, err)

	metrics := router.Metrics()
	assert.Equal(t, int64(2), metrics.RequestsByTask[TaskGeneral])
	assert.Equal(t, int64(1), metrics.RequestsByTask[TaskEvaluation])
	assert.Equal(t, int64(3), metrics.RequestsByTier[ModelTierBalanced])
	assert.Equal(t, int64(0), metrics.Fallbacks)
}

func TestSemanticRouter_Complete_DefaultsToGeneral(t *testing.T) {
	generalOnly := &SemanticProvider{
		TieredProvider: semanticTestProvider("general", "from-general", nil),
		ModelTier:      ModelTierBalanced,
		Capabilities:   []TaskType{TaskGeneral},
	}
	router := NewSemanticRouter([]*SemanticProvider{generalOnly}, DefaultSemanticRouterConfig())

	// Complete() should classify and route; no system prompt keywords -> TaskGeneral
	resp, err := router.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "from-general", resp.Choices[0].Message.Content)
}

func TestSemanticRouter_NoMatchingProvider(t *testing.T) {
	// Only handles TaskVisual; we request TaskDiscovery
	visualOnly := &SemanticProvider{
		TieredProvider: semanticTestProvider("visual", "from-visual", nil),
		ModelTier:      ModelTierVLM,
		Capabilities:   []TaskType{TaskVisual},
	}
	// Fallback provider - different Tier so it gets tried
	fallback := &SemanticProvider{
		TieredProvider: NewTieredProvider("fallback", TierFree, &testProvider{
			resp: &CompletionResponse{
				Choices: []Choice{{Message: Message{Content: "from-fallback"}}},
			},
		}),
		ModelTier:    ModelTierBalanced,
		Capabilities: []TaskType{TaskGeneral},
	}

	router := NewSemanticRouter([]*SemanticProvider{visualOnly, fallback}, DefaultSemanticRouterConfig())

	// TaskDiscovery wants [Powerful, Balanced]. visualOnly is VLM (not preferred).
	// fallback is Balanced and has TaskGeneral but not TaskDiscovery. So no semantic match.
	// Fallback TieredRouter: TierFree (fallback) first, then TierLocal (visualOnly).
	resp, err := router.CompleteWithTask(context.Background(), TaskDiscovery, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "discover"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "from-fallback", resp.Choices[0].Message.Content)

	metrics := router.Metrics()
	assert.Equal(t, int64(1), metrics.Fallbacks)
}

func TestSemanticRouter_ProviderWithEmptyCapabilitiesHandlesAll(t *testing.T) {
	// Capabilities nil/empty = handles all task types
	universal := &SemanticProvider{
		TieredProvider: semanticTestProvider("universal", "from-universal", nil),
		ModelTier:      ModelTierPowerful,
		Capabilities:   nil, // empty = all
	}
	router := NewSemanticRouter([]*SemanticProvider{universal}, DefaultSemanticRouterConfig())

	// TaskDiscovery prefers Powerful -> should hit universal
	resp, err := router.CompleteWithTask(context.Background(), TaskDiscovery, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "from-universal", resp.Choices[0].Message.Content)
}

func TestDefaultSemanticRouterConfig(t *testing.T) {
	cfg := DefaultSemanticRouterConfig()
	require.NotNil(t, cfg.TaskTierMap)

	assert.Equal(t, []ModelTierTag{ModelTierPowerful, ModelTierBalanced}, cfg.TaskTierMap[TaskDiscovery])
	assert.Equal(t, []ModelTierTag{ModelTierFast, ModelTierBalanced}, cfg.TaskTierMap[TaskPatternReplay])
	assert.Equal(t, []ModelTierTag{ModelTierVLM, ModelTierPowerful}, cfg.TaskTierMap[TaskVisual])
	assert.Equal(t, []ModelTierTag{ModelTierPowerful, ModelTierBalanced}, cfg.TaskTierMap[TaskSynthesis])
	assert.Equal(t, []ModelTierTag{ModelTierBalanced, ModelTierFast}, cfg.TaskTierMap[TaskEvaluation])
	assert.Equal(t, []ModelTierTag{ModelTierBalanced, ModelTierFast, ModelTierPowerful}, cfg.TaskTierMap[TaskGeneral])
}

func TestSemanticRouter_WithLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	provider := &SemanticProvider{
		TieredProvider: semanticTestProvider("p", "ok", nil),
		ModelTier:      ModelTierBalanced,
		Capabilities:   []TaskType{TaskGeneral},
	}
	router := NewSemanticRouterWithLogger([]*SemanticProvider{provider}, DefaultSemanticRouterConfig(), logger)

	resp, err := router.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Choices[0].Message.Content)
}
