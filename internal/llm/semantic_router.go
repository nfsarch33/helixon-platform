package llm

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// TaskType classifies the nature of an LLM request.
type TaskType string

const (
	// TaskDiscovery is for new UI element/pattern discovery (needs smart model).
	TaskDiscovery TaskType = "discovery"
	// TaskPatternReplay is for executing known patterns (light model ok).
	TaskPatternReplay TaskType = "pattern_replay"
	// TaskVisual is for visual UI analysis (needs VLM).
	TaskVisual TaskType = "visual"
	// TaskSynthesis is for mutation synthesis and evolution (needs smart model).
	TaskSynthesis TaskType = "synthesis"
	// TaskEvaluation is for quality scoring and rubric evaluation.
	TaskEvaluation TaskType = "evaluation"
	// TaskGeneral is the default task type.
	TaskGeneral TaskType = "general"
)

// ModelTierTag labels a provider's capability level.
type ModelTierTag string

const (
	// ModelTierFast is for lightweight models (e.g. qwen3.5-9b on RTX 4070 Ti).
	ModelTierFast ModelTierTag = "fast"
	// ModelTierBalanced is for mid-range models (e.g. qwen3.5-27b on RTX 4090).
	ModelTierBalanced ModelTierTag = "balanced"
	// ModelTierPowerful is for large models (e.g. qwen3.5-72b on RTX 5090).
	ModelTierPowerful ModelTierTag = "powerful"
	// ModelTierVLM is for vision-language models (e.g. qwen3-vl).
	ModelTierVLM ModelTierTag = "vlm"
)

// SemanticProvider wraps a Provider with task-type capability tags.
type SemanticProvider struct {
	*TieredProvider
	ModelTier    ModelTierTag
	Capabilities []TaskType // which task types this provider handles well
}

// SemanticRouterConfig holds routing configuration.
type SemanticRouterConfig struct {
	// TaskTierMap maps task types to preferred model tiers (ordered by preference).
	TaskTierMap map[TaskType][]ModelTierTag
}

// DefaultSemanticRouterConfig returns the standard task-to-tier mapping.
func DefaultSemanticRouterConfig() SemanticRouterConfig {
	return SemanticRouterConfig{
		TaskTierMap: map[TaskType][]ModelTierTag{
			TaskDiscovery:     {ModelTierPowerful, ModelTierBalanced},
			TaskPatternReplay: {ModelTierFast, ModelTierBalanced},
			TaskVisual:        {ModelTierVLM, ModelTierPowerful},
			TaskSynthesis:     {ModelTierPowerful, ModelTierBalanced},
			TaskEvaluation:    {ModelTierBalanced, ModelTierFast},
			TaskGeneral:       {ModelTierBalanced, ModelTierFast, ModelTierPowerful},
		},
	}
}

// SemanticRouter extends TieredRouter with task-type-aware routing.
// It selects providers based on both task classification and tier health.
type SemanticRouter struct {
	providers []*SemanticProvider
	config    SemanticRouterConfig
	fallback  *TieredRouter
	logger    *slog.Logger
	mu        sync.RWMutex
	metrics   SemanticRouterMetrics
}

// SemanticRouterMetrics tracks routing decisions.
type SemanticRouterMetrics struct {
	RequestsByTask map[TaskType]int64
	RequestsByTier map[ModelTierTag]int64
	Fallbacks      int64
}

// NewSemanticRouter creates a new semantic router.
func NewSemanticRouter(providers []*SemanticProvider, cfg SemanticRouterConfig) *SemanticRouter {
	// Build fallback TieredRouter from underlying TieredProviders
	tiered := make([]*TieredProvider, 0, len(providers))
	for _, sp := range providers {
		tiered = append(tiered, sp.TieredProvider)
	}
	fallback := NewTieredRouter(tiered)

	logger := slog.Default()

	return &SemanticRouter{
		providers: providers,
		config:    cfg,
		fallback:  fallback,
		logger:    logger,
		metrics: SemanticRouterMetrics{
			RequestsByTask: make(map[TaskType]int64),
			RequestsByTier: make(map[ModelTierTag]int64),
		},
	}
}

// NewSemanticRouterWithLogger creates a semantic router with an explicit logger.
func NewSemanticRouterWithLogger(providers []*SemanticProvider, cfg SemanticRouterConfig, logger *slog.Logger) *SemanticRouter {
	sr := NewSemanticRouter(providers, cfg)
	if logger != nil {
		sr.logger = logger
	}
	return sr
}

// CompleteWithTask routes a request based on its task type.
func (r *SemanticRouter) CompleteWithTask(ctx context.Context, taskType TaskType, req CompletionRequest) (*CompletionResponse, error) {
	r.mu.Lock()
	r.metrics.RequestsByTask[taskType]++
	r.mu.Unlock()

	preferredTiers := r.config.TaskTierMap[taskType]
	if len(preferredTiers) == 0 {
		preferredTiers = r.config.TaskTierMap[TaskGeneral]
	}

	// Try semantic providers in preferred tier order
	for _, tier := range preferredTiers {
		for _, sp := range r.providers {
			if sp.ModelTier != tier {
				continue
			}
			if !sp.capableOf(taskType) {
				continue
			}
			if !sp.isAvailable() {
				continue
			}

			resp, err := sp.Provider.Complete(ctx, req)
			if err != nil {
				sp.recordFailure()
				r.logger.Debug("semantic provider failed, trying next",
					"provider", sp.Name,
					"tier", string(sp.ModelTier),
					"error", err)
				continue
			}

			sp.recordSuccess()
			r.mu.Lock()
			r.metrics.RequestsByTier[sp.ModelTier]++
			r.mu.Unlock()
			return resp, nil
		}
	}

	// Fallback to tiered router
	r.mu.Lock()
	r.metrics.Fallbacks++
	r.mu.Unlock()
	r.logger.Debug("no matching semantic provider, falling back to tiered router",
		"task_type", string(taskType))

	resp, err := r.fallback.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("semantic router fallback: %w", err)
	}
	return resp, nil
}

// Complete implements Provider interface, defaults to TaskGeneral.
func (r *SemanticRouter) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	taskType := ClassifyTask(req)
	return r.CompleteWithTask(ctx, taskType, req)
}

// Metrics returns current routing metrics snapshot.
func (r *SemanticRouter) Metrics() SemanticRouterMetrics {
	r.mu.RLock()
	defer r.mu.RUnlock()

	snap := SemanticRouterMetrics{
		RequestsByTask: make(map[TaskType]int64),
		RequestsByTier: make(map[ModelTierTag]int64),
		Fallbacks:      r.metrics.Fallbacks,
	}
	for k, v := range r.metrics.RequestsByTask {
		snap.RequestsByTask[k] = v
	}
	for k, v := range r.metrics.RequestsByTier {
		snap.RequestsByTier[k] = v
	}
	return snap
}

// capableOf returns true if the provider handles the given task type.
func (sp *SemanticProvider) capableOf(t TaskType) bool {
	if len(sp.Capabilities) == 0 {
		return true
	}
	for _, c := range sp.Capabilities {
		if c == t {
			return true
		}
	}
	return false
}

// ClassifyTask provides a simple heuristic to classify a request by system prompt content.
func ClassifyTask(req CompletionRequest) TaskType {
	systemContent := extractSystemContent(req)
	lower := strings.ToLower(systemContent)

	// Order matters: more specific patterns first
	if containsAny(lower, "screenshot", "visual", "image") {
		return TaskVisual
	}
	if containsAny(lower, "discover", "find element", "css selector") {
		return TaskDiscovery
	}
	if containsAny(lower, "pattern", "cached", "replay") {
		return TaskPatternReplay
	}
	if containsAny(lower, "mutate", "evolve", "synthesize") {
		return TaskSynthesis
	}
	if containsAny(lower, "evaluate", "score", "rubric") {
		return TaskEvaluation
	}

	return TaskGeneral
}

func extractSystemContent(req CompletionRequest) string {
	for _, m := range req.Messages {
		if strings.EqualFold(m.Role, "system") {
			return m.Content
		}
	}
	return ""
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
