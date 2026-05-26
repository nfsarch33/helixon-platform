package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
)

// MCPRequest represents an incoming MCP JSON-RPC request.
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	ID      any             `json:"id"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// MCPResponse represents an outgoing MCP JSON-RPC response.
type MCPResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *MCPError `json:"error,omitempty"`
}

// MCPError is the JSON-RPC error structure.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// MCPToolHandler handles a single MCP tool invocation.
type MCPToolHandler func(ctx context.Context, params json.RawMessage) (any, error)

// MCPToolDef describes an MCP tool exposed to external agents.
type MCPToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Handler     MCPToolHandler  `json:"-"`
}

// MCPChannel exposes agent capabilities as MCP tools over JSON-RPC.
type MCPChannel struct {
	mu     sync.RWMutex
	tools  map[string]*MCPToolDef
	logger *slog.Logger
}

// NewMCPChannel creates an MCP channel for tool exposure.
func NewMCPChannel(logger *slog.Logger) *MCPChannel {
	if logger == nil {
		logger = slog.Default()
	}
	return &MCPChannel{
		tools:  make(map[string]*MCPToolDef),
		logger: logger.With(slog.String("component", "helixon.channel.mcp")),
	}
}

// RegisterTool adds a tool to the MCP channel.
func (m *MCPChannel) RegisterTool(def MCPToolDef) error {
	if def.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if def.Handler == nil {
		return fmt.Errorf("tool handler is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.tools[def.Name]; exists {
		return fmt.Errorf("tool %q already registered", def.Name)
	}
	m.tools[def.Name] = &def
	return nil
}

// HandleRequest processes a single MCP JSON-RPC request.
func (m *MCPChannel) HandleRequest(ctx context.Context, req MCPRequest) MCPResponse {
	switch req.Method {
	case "tools/list":
		return m.handleListTools(req)
	case "tools/call":
		return m.handleCallTool(ctx, req)
	case "initialize":
		return MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2025-03-26",
				"serverInfo": map[string]string{
					"name":    "helixon-agent",
					"version": "1.0.0",
				},
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
			},
		}
	default:
		return MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &MCPError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
}

func (m *MCPChannel) handleListTools(req MCPRequest) MCPResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tools := make([]map[string]any, 0, len(m.tools))
	for _, def := range m.tools {
		tool := map[string]any{
			"name":        def.Name,
			"description": def.Description,
		}
		if len(def.InputSchema) > 0 {
			var schema any
			if err := json.Unmarshal(def.InputSchema, &schema); err == nil {
				tool["inputSchema"] = schema
			}
		}
		tools = append(tools, tool)
	}

	return MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"tools": tools},
	}
}

func (m *MCPChannel) handleCallTool(ctx context.Context, req MCPRequest) MCPResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &MCPError{Code: -32602, Message: "invalid params"},
		}
	}

	m.mu.RLock()
	def, ok := m.tools[params.Name]
	m.mu.RUnlock()

	if !ok {
		return MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &MCPError{Code: -32602, Message: fmt.Sprintf("tool not found: %s", params.Name)},
		}
	}

	result, err := def.Handler(ctx, params.Arguments)
	if err != nil {
		m.logger.Warn("tool call failed",
			slog.String("tool", params.Name),
			slog.String("error", err.Error()),
		)
		return MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]string{
					{"type": "text", "text": fmt.Sprintf("Error: %s", err.Error())},
				},
				"isError": true,
			},
		}
	}

	var content any
	switch v := result.(type) {
	case string:
		content = []map[string]string{{"type": "text", "text": v}}
	default:
		text, _ := json.Marshal(v)
		content = []map[string]string{{"type": "text", "text": string(text)}}
	}

	return MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"content": content},
	}
}

// ToolCount returns the number of registered tools.
func (m *MCPChannel) ToolCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tools)
}
