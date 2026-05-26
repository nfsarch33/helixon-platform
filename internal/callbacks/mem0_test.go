package callbacks_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nfsarch33/helixon-platform/internal/callbacks"
)

func TestMem0Handler_OnEnd_WritesCapsule(t *testing.T) {
	var mu sync.Mutex
	var received []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		w.WriteHeader(200)
		w.Write([]byte(`{"results": [{"id": "test"}]}`))
	}))
	defer srv.Close()

	h := callbacks.NewMem0Handler(callbacks.Mem0Config{
		BaseURL:   srv.URL,
		APIKey:    "test-key",
		AppID:     "cursor-global-kb",
		UserID:    "nfsarch33",
		Namespace: "callbacks",
	})

	info := &callbacks.RunInfo{
		ComponentName: "research-agent",
		RunID:         "run-42",
		AgentType:     "research",
	}

	h.OnEnd(context.Background(), info, "done")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 1
	}, 5*time.Second, 50*time.Millisecond)

	mu.Lock()
	body := received[0]
	mu.Unlock()

	assert.Equal(t, "cursor-global-kb", body["app_id"])
	assert.Equal(t, "nfsarch33", body["user_id"])

	meta, ok := body["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "completed", meta["event"])
	assert.Equal(t, "research-agent", meta["component"])
	assert.Contains(t, meta["namespace_key"], "cursor-global-kb/nfsarch33/callbacks/run-42")
}

func TestMem0Handler_OnError_WritesCapsule(t *testing.T) {
	var mu sync.Mutex
	var received []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	h := callbacks.NewMem0Handler(callbacks.Mem0Config{
		BaseURL:   srv.URL,
		Namespace: "callbacks",
	})

	info := &callbacks.RunInfo{ComponentName: "parser", RunID: "run-err"}
	h.OnError(context.Background(), info, errors.New("parse failed"))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 1
	}, 5*time.Second, 50*time.Millisecond)

	mu.Lock()
	body := received[0]
	mu.Unlock()

	meta, ok := body["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "error", meta["event"])
}

func TestMem0Handler_OnStart_NoOp(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(200)
	}))
	defer srv.Close()

	h := callbacks.NewMem0Handler(callbacks.Mem0Config{BaseURL: srv.URL})
	info := &callbacks.RunInfo{ComponentName: "test", RunID: "noop"}

	h.OnStart(context.Background(), info, nil)
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, 0, requestCount, "OnStart should not make HTTP calls")
}

func TestMem0Handler_NamespaceKey_Format(t *testing.T) {
	var mu sync.Mutex
	var received []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	h := callbacks.NewMem0Handler(callbacks.Mem0Config{
		BaseURL:   srv.URL,
		AppID:     "app1",
		UserID:    "user1",
		Namespace: "evoloop",
	})

	info := &callbacks.RunInfo{ComponentName: "promote", RunID: "cycle-7"}
	h.OnEnd(context.Background(), info, nil)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 1
	}, 5*time.Second, 50*time.Millisecond)

	mu.Lock()
	body := received[0]
	mu.Unlock()

	meta := body["metadata"].(map[string]any)
	assert.Equal(t, "app1/user1/evoloop/cycle-7", meta["namespace_key"],
		"namespace key must follow 4-part format: {app_id}/{user_id}/{namespace}/{run_id}")
}

func TestMem0Handler_DefaultNamespace(t *testing.T) {
	var mu sync.Mutex
	var received []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	h := callbacks.NewMem0Handler(callbacks.Mem0Config{
		BaseURL: srv.URL,
	})

	info := &callbacks.RunInfo{ComponentName: "test", RunID: "def-ns"}
	h.OnEnd(context.Background(), info, nil)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 1
	}, 5*time.Second, 50*time.Millisecond)

	mu.Lock()
	body := received[0]
	mu.Unlock()

	meta := body["metadata"].(map[string]any)
	assert.Contains(t, meta["namespace_key"], "/callbacks/",
		"default namespace should be 'callbacks'")
}
