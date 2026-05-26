package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockAgent struct {
	sessionID string
	response  string
	err       error
}

func (m *mockAgent) CreateSession(_ context.Context, _ string) (string, error) {
	return m.sessionID, nil
}

func (m *mockAgent) Run(_ context.Context, _ string, _ string) (string, error) {
	return m.response, m.err
}

func TestREPLExitCommand(t *testing.T) {
	agent := &mockAgent{sessionID: "sess-1", response: "world"}
	repl := NewREPL(agent, REPLConfig{})

	in := strings.NewReader("hello\n/exit\n")
	var out bytes.Buffer

	err := repl.Run(context.Background(), in, &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "world")
	assert.Contains(t, out.String(), "Goodbye!")
}

func TestREPLSkipsEmpty(t *testing.T) {
	agent := &mockAgent{sessionID: "sess-2", response: "reply"}
	repl := NewREPL(agent, REPLConfig{})

	in := strings.NewReader("\n\nmessage\n/quit\n")
	var out bytes.Buffer

	err := repl.Run(context.Background(), in, &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "reply")
}

func TestWebhookSuccess(t *testing.T) {
	agent := &mockAgent{sessionID: "ws-1", response: "webhook reply"}
	handler := NewWebhookHandler(agent, WebhookConfig{BearerToken: "secret"})

	body, _ := json.Marshal(WebhookRequest{Message: "hello"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var resp WebhookResponse
	err := json.NewDecoder(rr.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "webhook reply", resp.Response)
	assert.Equal(t, "ws-1", resp.SessionID)
	assert.Empty(t, resp.Error)
}

func TestWebhookUnauthorized(t *testing.T) {
	handler := NewWebhookHandler(&mockAgent{}, WebhookConfig{BearerToken: "secret"})

	body, _ := json.Marshal(WebhookRequest{Message: "hello"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestWebhookNoAuth(t *testing.T) {
	agent := &mockAgent{sessionID: "s1", response: "ok"}
	handler := NewWebhookHandler(agent, WebhookConfig{})

	body, _ := json.Marshal(WebhookRequest{Message: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewReader(body))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestWebhookMethodNotAllowed(t *testing.T) {
	handler := NewWebhookHandler(&mockAgent{}, WebhookConfig{})
	req := httptest.NewRequest(http.MethodGet, "/v1/chat", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestWebhookEmptyMessage(t *testing.T) {
	handler := NewWebhookHandler(&mockAgent{}, WebhookConfig{})
	body, _ := json.Marshal(WebhookRequest{Message: ""})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestWebhookExistingSession(t *testing.T) {
	agent := &mockAgent{sessionID: "new", response: "ok"}
	handler := NewWebhookHandler(agent, WebhookConfig{})

	body, _ := json.Marshal(WebhookRequest{SessionID: "existing-sess", Message: "hi"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	var resp WebhookResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	assert.Equal(t, "existing-sess", resp.SessionID)
}

func TestMCPChannelListTools(t *testing.T) {
	mcp := NewMCPChannel(nil)
	err := mcp.RegisterTool(MCPToolDef{
		Name:        "search",
		Description: "Search memories",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
		Handler:     func(_ context.Context, _ json.RawMessage) (any, error) { return "result", nil },
	})
	require.NoError(t, err)

	resp := mcp.HandleRequest(context.Background(), MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/list",
		ID:      1,
	})

	assert.Nil(t, resp.Error)
	result := resp.Result.(map[string]any)
	tools := result["tools"].([]map[string]any)
	assert.Len(t, tools, 1)
	assert.Equal(t, "search", tools[0]["name"])
}

func TestMCPChannelCallTool(t *testing.T) {
	mcp := NewMCPChannel(nil)
	_ = mcp.RegisterTool(MCPToolDef{
		Name: "echo",
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(params, &args)
			return args.Text, nil
		},
	})

	params, _ := json.Marshal(map[string]any{
		"name":      "echo",
		"arguments": map[string]string{"text": "hello world"},
	})

	resp := mcp.HandleRequest(context.Background(), MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      2,
		Params:  params,
	})

	assert.Nil(t, resp.Error)
	result := resp.Result.(map[string]any)
	content := result["content"].([]map[string]string)
	assert.Equal(t, "hello world", content[0]["text"])
}

func TestMCPChannelInitialize(t *testing.T) {
	mcp := NewMCPChannel(nil)
	resp := mcp.HandleRequest(context.Background(), MCPRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		ID:      0,
	})
	assert.Nil(t, resp.Error)
	result := resp.Result.(map[string]any)
	assert.Equal(t, "2025-03-26", result["protocolVersion"])
}

func TestMCPChannelUnknownMethod(t *testing.T) {
	mcp := NewMCPChannel(nil)
	resp := mcp.HandleRequest(context.Background(), MCPRequest{
		JSONRPC: "2.0",
		Method:  "unknown/method",
		ID:      3,
	})
	assert.NotNil(t, resp.Error)
	assert.Equal(t, -32601, resp.Error.Code)
}

func TestMCPDuplicateRegister(t *testing.T) {
	mcp := NewMCPChannel(nil)
	def := MCPToolDef{
		Name:    "dup",
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) { return nil, nil },
	}
	require.NoError(t, mcp.RegisterTool(def))
	assert.Error(t, mcp.RegisterTool(def))
}

func TestRouterHealth(t *testing.T) {
	handler := NewWebhookHandler(&mockAgent{}, WebhookConfig{})
	mux := Router(handler)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "ok")
}
