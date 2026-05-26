package tooldispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterAndExecute(t *testing.T) {
	reg := NewRegistry(nil)

	err := reg.Register(ToolDef{
		Name:        "add",
		Description: "Add two numbers",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"a": {"type": "number"},
				"b": {"type": "number"}
			},
			"required": ["a", "b"]
		}`),
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			a, _ := args["a"].(float64)
			b, _ := args["b"].(float64)
			out, err := json.Marshal(a + b)
			return string(out), err
		},
	})
	require.NoError(t, err)

	result, err := reg.Execute(context.Background(), "add", `{"a": 3, "b": 4}`)
	require.NoError(t, err)
	assert.Equal(t, "7", result)
}

func TestExecuteToolNotFound(t *testing.T) {
	reg := NewRegistry(nil)
	_, err := reg.Execute(context.Background(), "nonexistent", "{}")
	assert.ErrorIs(t, err, ErrToolNotFound)
}

func TestRegisterDuplicate(t *testing.T) {
	reg := NewRegistry(nil)

	err := reg.Register(ToolDef{
		Name:    "tool1",
		Handler: func(_ context.Context, _ map[string]any) (string, error) { return "", nil },
	})
	require.NoError(t, err)

	err = reg.Register(ToolDef{
		Name:    "tool1",
		Handler: func(_ context.Context, _ map[string]any) (string, error) { return "", nil },
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestValidationMissingRequired(t *testing.T) {
	reg := NewRegistry(nil)

	err := reg.Register(ToolDef{
		Name: "greet",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {"name": {"type": "string"}},
			"required": ["name"]
		}`),
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			return "hello " + args["name"].(string), nil
		},
	})
	require.NoError(t, err)

	_, err = reg.Execute(context.Background(), "greet", `{}`)
	assert.ErrorIs(t, err, ErrInvalidArguments)
	assert.Contains(t, err.Error(), "missing required field: name")
}

func TestValidationWrongType(t *testing.T) {
	reg := NewRegistry(nil)

	err := reg.Register(ToolDef{
		Name: "count",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {"n": {"type": "integer"}}
		}`),
		Handler: func(_ context.Context, _ map[string]any) (string, error) { return "ok", nil },
	})
	require.NoError(t, err)

	_, err = reg.Execute(context.Background(), "count", `{"n": "not-a-number"}`)
	assert.ErrorIs(t, err, ErrInvalidArguments)
	assert.Contains(t, err.Error(), "expected number")
}

func TestAvailableTools(t *testing.T) {
	reg := NewRegistry(nil)

	_ = reg.Register(ToolDef{
		Name:        "tool_a",
		Description: "Tool A",
		Handler:     func(_ context.Context, _ map[string]any) (string, error) { return "", nil },
	})
	_ = reg.Register(ToolDef{
		Name:        "tool_b",
		Description: "Tool B",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Handler:     func(_ context.Context, _ map[string]any) (string, error) { return "", nil },
	})

	tools := reg.Available()
	assert.Len(t, tools, 2)

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Function.Name] = true
		assert.Equal(t, "function", tool.Type)
	}
	assert.True(t, names["tool_a"])
	assert.True(t, names["tool_b"])
}

func TestUnregister(t *testing.T) {
	reg := NewRegistry(nil)
	_ = reg.Register(ToolDef{
		Name:    "temp",
		Handler: func(_ context.Context, _ map[string]any) (string, error) { return "", nil },
	})

	assert.True(t, reg.Unregister("temp"))
	assert.False(t, reg.Unregister("temp"))

	_, err := reg.Execute(context.Background(), "temp", "{}")
	assert.ErrorIs(t, err, ErrToolNotFound)
}

func TestToolTimeout(t *testing.T) {
	reg := NewRegistry(nil)
	_ = reg.Register(ToolDef{
		Name:    "slow",
		Timeout: 50 * time.Millisecond,
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			select {
			case <-time.After(5 * time.Second):
				return "done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})

	_, err := reg.Execute(context.Background(), "slow", "{}")
	assert.ErrorIs(t, err, ErrToolTimeout)
}

func TestToolReturnsError(t *testing.T) {
	reg := NewRegistry(nil)
	toolErr := errors.New("computation failed")
	_ = reg.Register(ToolDef{
		Name: "fail",
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "", toolErr
		},
	})

	_, err := reg.Execute(context.Background(), "fail", "{}")
	assert.ErrorIs(t, err, toolErr)
}

func TestRegisterValidation(t *testing.T) {
	reg := NewRegistry(nil)

	err := reg.Register(ToolDef{Name: "", Handler: func(_ context.Context, _ map[string]any) (string, error) { return "", nil }})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")

	err = reg.Register(ToolDef{Name: "x", Handler: nil})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "handler is required")

	err = reg.Register(ToolDef{
		Name:       "bad_schema",
		Parameters: json.RawMessage(`{invalid json`),
		Handler:    func(_ context.Context, _ map[string]any) (string, error) { return "", nil },
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not valid JSON")
}

func TestNames(t *testing.T) {
	reg := NewRegistry(nil)
	_ = reg.Register(ToolDef{Name: "a", Handler: func(_ context.Context, _ map[string]any) (string, error) { return "", nil }})
	_ = reg.Register(ToolDef{Name: "b", Handler: func(_ context.Context, _ map[string]any) (string, error) { return "", nil }})

	names := reg.Names()
	assert.Len(t, names, 2)
	assert.Contains(t, names, "a")
	assert.Contains(t, names, "b")
}

// --- v12022 tool dispatch hardening tests ---

func TestConcurrentExecute_v12022(t *testing.T) {
	reg := NewRegistry(nil)
	_ = reg.Register(ToolDef{
		Name: "counter",
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			time.Sleep(time.Millisecond)
			return "ok", nil
		},
	})

	const N = 64
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = reg.Execute(context.Background(), "counter", "{}")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d failed", i)
	}
}

func TestContextCancellation_v12022(t *testing.T) {
	reg := NewRegistry(nil)
	_ = reg.Register(ToolDef{
		Name:    "blocker",
		Timeout: 5 * time.Second,
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := reg.Execute(ctx, "blocker", "{}")
		done <- err
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	err := <-done
	assert.Error(t, err)
}

func TestConcurrentRegisterAndExecute_v12022(t *testing.T) {
	reg := NewRegistry(nil)
	_ = reg.Register(ToolDef{
		Name:    "base",
		Handler: func(_ context.Context, _ map[string]any) (string, error) { return "base", nil },
	})

	var wg sync.WaitGroup
	wg.Add(20)
	for i := 0; i < 10; i++ {
		go func(idx int) {
			defer wg.Done()
			_ = reg.Register(ToolDef{
				Name:    fmt.Sprintf("dynamic_%d", idx),
				Handler: func(_ context.Context, _ map[string]any) (string, error) { return "d", nil },
			})
		}(i)
	}
	for i := 0; i < 10; i++ {
		go func() {
			defer wg.Done()
			reg.Execute(context.Background(), "base", "{}")
		}()
	}
	wg.Wait()

	names := reg.Names()
	assert.Contains(t, names, "base")
	assert.GreaterOrEqual(t, len(names), 2)
}

func TestExecuteEmptyArgs_v12022(t *testing.T) {
	reg := NewRegistry(nil)
	_ = reg.Register(ToolDef{
		Name: "noargs",
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			if args == nil {
				return "", errors.New("args should not be nil")
			}
			return "ok", nil
		},
	})

	result, err := reg.Execute(context.Background(), "noargs", "")
	assert.NoError(t, err)
	assert.Equal(t, "ok", result)

	result, err = reg.Execute(context.Background(), "noargs", "{}")
	assert.NoError(t, err)
	assert.Equal(t, "ok", result)
}

func TestDefaultTimeout_v12022(t *testing.T) {
	reg := NewRegistry(nil)
	err := reg.Register(ToolDef{
		Name:    "notimeout",
		Timeout: 0,
		Handler: func(_ context.Context, _ map[string]any) (string, error) { return "ok", nil },
	})
	require.NoError(t, err)

	result, err := reg.Execute(context.Background(), "notimeout", "{}")
	assert.NoError(t, err)
	assert.Equal(t, "ok", result)
}

func TestValidateArrayAndObjectTypes_v12022(t *testing.T) {
	reg := NewRegistry(nil)
	_ = reg.Register(ToolDef{
		Name: "complex",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"tags": {"type": "array"},
				"meta": {"type": "object"},
				"flag": {"type": "boolean"}
			}
		}`),
		Handler: func(_ context.Context, _ map[string]any) (string, error) { return "ok", nil },
	})

	_, err := reg.Execute(context.Background(), "complex", `{"tags": [1,2,3], "meta": {"k":"v"}, "flag": true}`)
	assert.NoError(t, err)

	_, err = reg.Execute(context.Background(), "complex", `{"tags": "not-array"}`)
	assert.ErrorIs(t, err, ErrInvalidArguments)

	_, err = reg.Execute(context.Background(), "complex", `{"meta": "not-object"}`)
	assert.ErrorIs(t, err, ErrInvalidArguments)

	_, err = reg.Execute(context.Background(), "complex", `{"flag": "not-bool"}`)
	assert.ErrorIs(t, err, ErrInvalidArguments)
}

func TestUnregisterMidConcurrentExecute_v12022(t *testing.T) {
	reg := NewRegistry(nil)
	_ = reg.Register(ToolDef{
		Name: "ephemeral",
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			time.Sleep(5 * time.Millisecond)
			return "ok", nil
		},
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		reg.Execute(context.Background(), "ephemeral", "{}")
	}()
	go func() {
		defer wg.Done()
		time.Sleep(2 * time.Millisecond)
		reg.Unregister("ephemeral")
	}()
	wg.Wait()
}
