package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// AgentMemoryConfig configures how memory is used during agent conversations.
type AgentMemoryConfig struct {
	AppID      string
	UserID     string
	TenantID   string
	MaxContext int
	Logger     *slog.Logger
}

func (c AgentMemoryConfig) withDefaults() AgentMemoryConfig {
	if c.MaxContext <= 0 {
		c.MaxContext = 5
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// AgentMemory wraps a HybridSearcher to provide conversation-boundary
// memory operations: search for context at conversation start, and
// store key learnings at conversation end.
type AgentMemory struct {
	searcher *HybridSearcher
	cfg      AgentMemoryConfig
	logger   *slog.Logger
}

// NewAgentMemory creates an AgentMemory wired to the given HybridSearcher.
func NewAgentMemory(searcher *HybridSearcher, cfg AgentMemoryConfig) *AgentMemory {
	cfg = cfg.withDefaults()
	return &AgentMemory{
		searcher: searcher,
		cfg:      cfg,
		logger:   cfg.Logger.With(slog.String("component", "helixon.memory.agent")),
	}
}

// RetrieveContext searches memory for content relevant to the user's
// prompt and returns a formatted context string to prepend to the
// system prompt. Returns empty string if no relevant memories found.
func (am *AgentMemory) RetrieveContext(ctx context.Context, userPrompt string) string {
	if am.searcher == nil {
		return ""
	}

	results, err := am.searcher.Search(ctx, userPrompt, am.cfg.AppID, am.cfg.UserID, am.cfg.TenantID)
	if err != nil {
		am.logger.Warn("memory search failed (non-fatal)", slog.String("error", err.Error()))
		return ""
	}

	if len(results) == 0 {
		return ""
	}

	limit := am.cfg.MaxContext
	if limit > len(results) {
		limit = len(results)
	}
	results = results[:limit]

	var sb strings.Builder
	sb.WriteString("\n\n<relevant_memories>\n")
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. [%s] %s\n", i+1, r.Source, r.Content)
	}
	sb.WriteString("</relevant_memories>")

	am.logger.Debug("retrieved memory context",
		slog.Int("results", len(results)),
		slog.Float64("top_score", results[0].Score),
	)
	return sb.String()
}

// StoreConversationSummary persists a summary of the conversation to
// memory for future retrieval. The summary should capture key decisions,
// learnings, or facts from the conversation.
func (am *AgentMemory) StoreConversationSummary(ctx context.Context, summary string) error {
	if am.searcher == nil {
		return nil
	}
	if strings.TrimSpace(summary) == "" {
		return nil
	}

	mem, err := am.searcher.Write(ctx, summary, am.cfg.AppID, am.cfg.UserID, am.cfg.TenantID)
	if err != nil {
		return fmt.Errorf("store summary: %w", err)
	}

	am.logger.Info("stored conversation summary",
		slog.String("id", mem.ID),
		slog.Int("content_len", len(summary)),
	)
	return nil
}

// ExtractSummary attempts to extract a concise summary from
// a conversation's turn history for memory storage. The turns
// parameter should be JSON-encoded turn data.
func ExtractSummary(turnsJSON []byte) string {
	var turns []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(turnsJSON, &turns); err != nil {
		return ""
	}

	var userQueries, assistantAnswers []string
	for _, t := range turns {
		switch t.Role {
		case "user":
			if len(t.Content) > 200 {
				userQueries = append(userQueries, t.Content[:200]+"...")
			} else {
				userQueries = append(userQueries, t.Content)
			}
		case "assistant":
			if t.Content != "" && len(assistantAnswers) < 2 {
				if len(t.Content) > 300 {
					assistantAnswers = append(assistantAnswers, t.Content[:300]+"...")
				} else {
					assistantAnswers = append(assistantAnswers, t.Content)
				}
			}
		}
	}

	if len(userQueries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Conversation summary: User asked about: ")
	sb.WriteString(strings.Join(userQueries, "; "))
	if len(assistantAnswers) > 0 {
		sb.WriteString(". Key responses: ")
		sb.WriteString(strings.Join(assistantAnswers, "; "))
	}
	return sb.String()
}
