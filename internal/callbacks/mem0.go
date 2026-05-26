package callbacks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Mem0Config configures the Mem0Handler for scoped capsule writes.
type Mem0Config struct {
	BaseURL   string // e.g. "http://127.0.0.1:18888" or cloud endpoint
	APIKey    string
	AppID     string // 4-part namespace: app_id
	UserID    string // 4-part namespace: user_id
	Namespace string // 4-part namespace: namespace (e.g. "callbacks")
}

// Mem0Handler writes scoped capsule entries to Mem0 on OnEnd and OnError.
// OnStart is a no-op (capsules are written at completion boundaries only).
// Each memory entry includes the 4-part namespace key for Race Layer 3 isolation.
type Mem0Handler struct {
	cfg    Mem0Config
	client *http.Client
}

// NewMem0Handler creates a handler that writes capsule entries to Mem0.
func NewMem0Handler(cfg Mem0Config) *Mem0Handler {
	if cfg.Namespace == "" {
		cfg.Namespace = "callbacks"
	}
	return &Mem0Handler{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (h *Mem0Handler) OnStart(ctx context.Context, _ *RunInfo, _ any) context.Context {
	return ctx
}

func (h *Mem0Handler) OnEnd(ctx context.Context, info *RunInfo, output any) context.Context {
	capsule := h.buildCapsule(info, "completed", output, nil)
	h.writeAsync(ctx, capsule)
	return ctx
}

func (h *Mem0Handler) OnError(ctx context.Context, info *RunInfo, err error) context.Context {
	capsule := h.buildCapsule(info, "error", nil, err)
	h.writeAsync(ctx, capsule)
	return ctx
}

func (h *Mem0Handler) buildCapsule(info *RunInfo, event string, output any, err error) map[string]any {
	capsule := map[string]any{
		"event":          event,
		"component_name": info.ComponentName,
		"run_id":         info.RunID,
		"agent_type":     info.AgentType,
		"parent_chain":   info.ParentChain(),
		"namespace_key":  h.namespaceKey(info.RunID),
		"timestamp":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if output != nil {
		capsule["output_summary"] = fmt.Sprintf("%v", output)
	}
	if err != nil {
		capsule["error"] = err.Error()
	}
	for k, v := range info.Tags {
		capsule["tag_"+k] = v
	}
	return capsule
}

// namespaceKey produces the 4-part key: {app_id}/{user_id}/{namespace}/{run_id}
func (h *Mem0Handler) namespaceKey(runID string) string {
	return fmt.Sprintf("%s/%s/%s/%s", h.cfg.AppID, h.cfg.UserID, h.cfg.Namespace, runID)
}

func (h *Mem0Handler) writeAsync(ctx context.Context, capsule map[string]any) {
	go func() {
		if err := h.addMemory(ctx, capsule); err != nil {
			slog.Warn("mem0 callback write failed",
				"error", err,
				"component", capsule["component_name"],
				"run_id", capsule["run_id"],
			)
		}
	}()
}

func (h *Mem0Handler) addMemory(ctx context.Context, capsule map[string]any) error {
	content, _ := json.Marshal(capsule)
	body := map[string]any{
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": string(content),
			},
		},
		"metadata": map[string]string{
			"namespace_key": capsule["namespace_key"].(string),
			"event":         capsule["event"].(string),
			"component":     capsule["component_name"].(string),
		},
	}
	if h.cfg.AppID != "" {
		body["app_id"] = h.cfg.AppID
	}
	if h.cfg.UserID != "" {
		body["user_id"] = h.cfg.UserID
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal mem0 body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(h.cfg.BaseURL, "/")+"/v1/memories/",
		strings.NewReader(string(bodyBytes)))
	if err != nil {
		return fmt.Errorf("create mem0 request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if h.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Token "+h.cfg.APIKey)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("mem0 HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("mem0 returned %d", resp.StatusCode)
	}
	return nil
}
