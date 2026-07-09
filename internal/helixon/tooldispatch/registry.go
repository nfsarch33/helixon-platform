package tooldispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/nfsarch33/helixon-platform/internal/toolresult"
)

var (
	ErrToolNotFound     = errors.New("tool not found")
	ErrInvalidArguments = errors.New("invalid tool arguments")
	ErrToolTimeout      = errors.New("tool execution timeout")
)

// ToolFunc is the function signature for a registered tool. It receives
// the execution context and a parsed JSON arguments map.
type ToolFunc func(ctx context.Context, args map[string]any) (string, error)

// ToolDef describes a registered tool with its schema and handler.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Handler     ToolFunc        `json:"-"`
	Timeout     time.Duration   `json:"-"`
}

// Registry manages tool registration, schema validation, and dispatch.
type Registry struct {
	mu     sync.RWMutex
	tools  map[string]*ToolDef
	logger *slog.Logger
}

// NewRegistry creates an empty tool registry.
func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{
		tools:  make(map[string]*ToolDef),
		logger: logger.With(slog.String("component", "helixon.tooldispatch")),
	}
}

// Register adds a tool to the registry. Returns an error if a tool with
// the same name already exists.
func (r *Registry) Register(def ToolDef) error {
	if def.Name == "" {
		return errors.New("tool name is required")
	}
	if def.Handler == nil {
		return errors.New("tool handler is required")
	}
	if def.Timeout <= 0 {
		def.Timeout = 30 * time.Second
	}

	if len(def.Parameters) > 0 {
		if !json.Valid(def.Parameters) {
			return fmt.Errorf("parameters for tool %q is not valid JSON", def.Name)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[def.Name]; exists {
		return fmt.Errorf("tool %q already registered", def.Name)
	}
	r.tools[def.Name] = &def
	r.logger.Info("tool registered", slog.String("tool", def.Name))
	return nil
}

// Unregister removes a tool from the registry.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; ok {
		delete(r.tools, name)
		return true
	}
	return false
}

// Execute dispatches a tool call by name, validating and parsing arguments.
// Implements the agent.ToolExecutor interface.
func (r *Registry) Execute(ctx context.Context, name string, argsJSON string) (string, error) {
	res, err := r.ExecuteToolResult(ctx, name, argsJSON)
	if err != nil {
		// Return the original error so errors.Is(err, sentinel) still works in callers
		// that pre-date ToolResult. The full ToolResult is also retained for new
		// callers via ExecuteToolResult.
		return res.Output, err
	}
	return res.Output, nil
}

// ExecuteToolResult is the canonical, typed form of Execute. It returns a
// toolresult.ToolResult capturing status, output, error, latency, cost,
// idempotency key, and content hash. The legacy Execute wraps this and
// returns only the string output + error for backward compatibility.
func (r *Registry) ExecuteToolResult(ctx context.Context, name string, argsJSON string) (toolresult.ToolResult, error) {
	r.mu.RLock()
	def, ok := r.tools[name]
	r.mu.RUnlock()

	start := time.Now()
	idemKey := toolresult.NewToolResult(name, argsJSON, "", "", "", 0, 0).IdempotencyKey

	if !ok {
		res := toolresult.NewToolResult(name, argsJSON, toolresult.StatusError, "", ErrToolNotFound.Error()+": "+name, time.Since(start).Milliseconds(), 0)
		res.IdempotencyKey = idemKey
		return res, fmt.Errorf("%w: %s", ErrToolNotFound, name)
	}

	var args map[string]any
	if argsJSON != "" && argsJSON != "{}" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			msg := ErrInvalidArguments.Error() + ": " + name + ": " + err.Error()
			res := toolresult.NewToolResult(name, argsJSON, toolresult.StatusError, "", msg, time.Since(start).Milliseconds(), 0)
			res.IdempotencyKey = idemKey
			return res, fmt.Errorf("%w: %s: %s", ErrInvalidArguments, name, err)
		}
	}
	if args == nil {
		args = make(map[string]any)
	}

	if len(def.Parameters) > 0 {
		if err := validateArgs(def.Parameters, args); err != nil {
			msg := ErrInvalidArguments.Error() + ": " + name + ": " + err.Error()
			res := toolresult.NewToolResult(name, argsJSON, toolresult.StatusError, "", msg, time.Since(start).Milliseconds(), 0)
			res.IdempotencyKey = idemKey
			return res, fmt.Errorf("%w: %s: %s", ErrInvalidArguments, name, err)
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, def.Timeout)
	defer cancel()

	result, err := def.Handler(execCtx, args)
	elapsed := time.Since(start)

	r.logger.Debug("tool executed",
		slog.String("tool", name),
		slog.Duration("elapsed", elapsed),
		slog.Bool("error", err != nil),
	)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			msg := ErrToolTimeout.Error() + " after " + def.Timeout.String()
			res := toolresult.NewToolResult(name, argsJSON, toolresult.StatusError, "", msg, elapsed.Milliseconds(), 0)
			res.IdempotencyKey = idemKey
			return res, fmt.Errorf("%w: %s after %s", ErrToolTimeout, name, def.Timeout)
		}
		res := toolresult.NewToolResult(name, argsJSON, toolresult.StatusError, "", err.Error(), elapsed.Milliseconds(), 0)
		res.IdempotencyKey = idemKey
		return res, err
	}

	res := toolresult.NewToolResult(name, argsJSON, toolresult.StatusOK, result, "", elapsed.Milliseconds(), 0)
	res.IdempotencyKey = idemKey
	return res, nil
}

// Available returns the tool definitions in OpenAI tool format.
func (r *Registry) Available() []llm.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]llm.Tool, 0, len(r.tools))
	for _, def := range r.tools {
		tools = append(tools, llm.Tool{
			Type: "function",
			Function: llm.FunctionDef{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.Parameters,
			},
		})
	}
	return tools
}

// Names returns the registered tool names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// validateArgs performs basic JSON schema validation for required fields and types.
func validateArgs(schemaJSON json.RawMessage, args map[string]any) error {
	var schema struct {
		Type       string              `json:"type"`
		Required   []string            `json:"required"`
		Properties map[string]property `json:"properties"`
	}
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return nil
	}

	for _, req := range schema.Required {
		if _, ok := args[req]; !ok {
			return fmt.Errorf("missing required field: %s", req)
		}
	}

	for name, prop := range schema.Properties {
		val, ok := args[name]
		if !ok {
			continue
		}
		if err := checkType(name, prop.Type, val); err != nil {
			return err
		}
	}

	return nil
}

type property struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

func checkType(name, expectedType string, val any) error {
	switch expectedType {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("field %q: expected string, got %T", name, val)
		}
	case "number", "integer":
		switch val.(type) {
		case float64, int, int64, json.Number:
		default:
			return fmt.Errorf("field %q: expected number, got %T", name, val)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("field %q: expected boolean, got %T", name, val)
		}
	case "array":
		if _, ok := val.([]any); !ok {
			return fmt.Errorf("field %q: expected array, got %T", name, val)
		}
	case "object":
		if _, ok := val.(map[string]any); !ok {
			return fmt.Errorf("field %q: expected object, got %T", name, val)
		}
	}
	return nil
}
