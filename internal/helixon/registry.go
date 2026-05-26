package helixon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
)

// NamespacedRegistry wraps a tooldispatch.Registry with namespace prefixing
// and convenience registration for the Helixon runtime's built-in tools.
type NamespacedRegistry struct {
	inner  *tooldispatch.Registry
	logger *slog.Logger
}

// NewNamespacedRegistry wraps an existing registry with namespace support.
func NewNamespacedRegistry(inner *tooldispatch.Registry, logger *slog.Logger) *NamespacedRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	return &NamespacedRegistry{
		inner:  inner,
		logger: logger.With(slog.String("component", "helixon.registry")),
	}
}

// RegisterNamespaced registers a tool with a namespace prefix (e.g., "sprintboard.claim_ticket").
func (nr *NamespacedRegistry) RegisterNamespaced(namespace string, def tooldispatch.ToolDef) error {
	if namespace != "" {
		def.Name = namespace + "." + def.Name
	}
	return nr.inner.Register(def)
}

// UnregisterNamespace removes all tools with the given namespace prefix.
func (nr *NamespacedRegistry) UnregisterNamespace(namespace string) int {
	prefix := namespace + "."
	removed := 0
	for _, name := range nr.inner.Names() {
		if strings.HasPrefix(name, prefix) {
			if nr.inner.Unregister(name) {
				removed++
			}
		}
	}
	if removed > 0 {
		nr.logger.Info("namespace unregistered",
			slog.String("namespace", namespace),
			slog.Int("tools_removed", removed),
		)
	}
	return removed
}

// ListNamespaces returns distinct namespace prefixes from registered tools.
func (nr *NamespacedRegistry) ListNamespaces() []string {
	seen := make(map[string]bool)
	for _, name := range nr.inner.Names() {
		parts := strings.SplitN(name, ".", 2)
		if len(parts) == 2 {
			seen[parts[0]] = true
		}
	}

	ns := make([]string, 0, len(seen))
	for k := range seen {
		ns = append(ns, k)
	}
	return ns
}

// RegisterBuiltinTools registers the runtime's built-in tools for memory search
// and sprintboard operations. These are the standard tools available to every
// Helixon agent instance.
func RegisterBuiltinTools(r *Runtime) error {
	if r.registry == nil {
		return fmt.Errorf("registry not initialised; call Init() first")
	}

	nr := NewNamespacedRegistry(r.registry, r.logger)

	if r.memory != nil {
		if err := nr.RegisterNamespaced("memory", tooldispatch.ToolDef{
			Name:        "search",
			Description: "Search agent memory using hybrid FTS5 + vector retrieval",
			Parameters: mustJSON(map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query":       map[string]string{"type": "string", "description": "Search query"},
					"max_results": map[string]string{"type": "integer", "description": "Maximum results to return"},
				},
			}),
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				query, _ := args["query"].(string)
				if query == "" {
					return "", fmt.Errorf("query is required")
				}
				results, err := r.memory.Search(ctx, query, r.cfg.AgentID, "")
				if err != nil {
					return "", err
				}
				data, _ := json.Marshal(results)
				return string(data), nil
			},
		}); err != nil {
			return fmt.Errorf("register memory.search: %w", err)
		}

		if err := nr.RegisterNamespaced("memory", tooldispatch.ToolDef{
			Name:        "write",
			Description: "Persist a memory to canonical Engram + local FTS mirror",
			Parameters: mustJSON(map[string]any{
				"type":     "object",
				"required": []string{"content"},
				"properties": map[string]any{
					"content": map[string]string{"type": "string", "description": "Memory content to persist"},
					"app_id":  map[string]string{"type": "string", "description": "Optional app namespace (defaults to runtime AgentID)"},
					"user_id": map[string]string{"type": "string", "description": "Optional user identifier"},
				},
			}),
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				content, _ := args["content"].(string)
				if content == "" {
					return "", fmt.Errorf("content is required")
				}
				appID, _ := args["app_id"].(string)
				if appID == "" {
					appID = r.cfg.AgentID
				}
				userID, _ := args["user_id"].(string)
				mem, err := r.memory.Write(ctx, content, appID, userID)
				if err != nil {
					return "", err
				}
				data, _ := json.Marshal(mem)
				return string(data), nil
			},
		}); err != nil {
			return fmt.Errorf("register memory.write: %w", err)
		}

		if err := nr.RegisterNamespaced("memory", tooldispatch.ToolDef{
			Name:        "read",
			Description: "Fetch a single memory by id from the canonical Engram store",
			Parameters: mustJSON(map[string]any{
				"type":     "object",
				"required": []string{"id"},
				"properties": map[string]any{
					"id": map[string]string{"type": "string", "description": "Memory id to fetch"},
				},
			}),
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				id, _ := args["id"].(string)
				if id == "" {
					return "", fmt.Errorf("id is required")
				}
				mem, err := r.memory.Read(ctx, id)
				if err != nil {
					return "", err
				}
				data, _ := json.Marshal(mem)
				return string(data), nil
			},
		}); err != nil {
			return fmt.Errorf("register memory.read: %w", err)
		}
	}

	if r.sprintCtl != nil {
		if err := nr.RegisterNamespaced("sprintboard", tooldispatch.ToolDef{
			Name:        "claim_ticket",
			Description: "Claim a sprintboard ticket for this agent",
			Parameters: mustJSON(map[string]any{
				"type":     "object",
				"required": []string{"ticket_id"},
				"properties": map[string]any{
					"ticket_id": map[string]string{"type": "string", "description": "Ticket ID to claim"},
				},
			}),
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				ticketID, _ := args["ticket_id"].(string)
				if ticketID == "" {
					return "", fmt.Errorf("ticket_id is required")
				}
				if err := r.sprintCtl.ClaimTicket(ctx, ticketID); err != nil {
					return "", err
				}
				return fmt.Sprintf("ticket %s claimed by %s", ticketID, r.cfg.AgentID), nil
			},
		}); err != nil {
			return fmt.Errorf("register sprintboard.claim_ticket: %w", err)
		}

		if err := nr.RegisterNamespaced("sprintboard", tooldispatch.ToolDef{
			Name:        "complete_ticket",
			Description: "Mark a sprintboard ticket as completed with evidence",
			Parameters: mustJSON(map[string]any{
				"type":     "object",
				"required": []string{"ticket_id", "evidence"},
				"properties": map[string]any{
					"ticket_id": map[string]string{"type": "string", "description": "Ticket ID to complete"},
					"evidence":  map[string]string{"type": "string", "description": "Completion evidence"},
				},
			}),
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				ticketID, _ := args["ticket_id"].(string)
				evidence, _ := args["evidence"].(string)
				if ticketID == "" {
					return "", fmt.Errorf("ticket_id is required")
				}
				if err := r.sprintCtl.CompleteTicket(ctx, ticketID, evidence); err != nil {
					return "", err
				}
				return fmt.Sprintf("ticket %s completed", ticketID), nil
			},
		}); err != nil {
			return fmt.Errorf("register sprintboard.complete_ticket: %w", err)
		}
	}

	r.logger.Info("builtin tools registered",
		slog.Int("total", len(r.registry.Names())),
	)
	return nil
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustJSON: %v", err))
	}
	return data
}
