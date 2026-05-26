package helixon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProvider_MockEcho(t *testing.T) {
	t.Parallel()
	cfg := ProviderConfig{Kind: "mock"}
	p, err := BuildProvider(cfg)
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	if p == nil {
		t.Fatal("BuildProvider returned nil provider for mock kind")
	}
}

func TestBuildProvider_OpenAIRequiresBaseURL(t *testing.T) {
	t.Parallel()
	cfg := ProviderConfig{Kind: "openai-compat", APIKey: "x", Model: "gpt-4o"}
	if _, err := BuildProvider(cfg); err == nil {
		t.Fatal("expected error when base_url missing")
	}
}

func TestBuildProvider_NoneReturnsNil(t *testing.T) {
	t.Parallel()
	p, err := BuildProvider(ProviderConfig{Kind: "none"})
	if err != nil {
		t.Fatalf("BuildProvider(none): %v", err)
	}
	if p != nil {
		t.Fatalf("expected nil provider for kind=none, got %T", p)
	}
}

func TestBuildProvider_RejectsUnknownKind(t *testing.T) {
	t.Parallel()
	if _, err := BuildProvider(ProviderConfig{Kind: "anthropic-bedrock"}); err == nil {
		t.Fatal("expected unknown kind to error")
	}
}

func TestBuildProvider_ExpandsEnvAPIKey(t *testing.T) {
	t.Setenv("V8800_FAKE_KEY", "sk-test-123")
	cfg := ProviderConfig{
		Kind:    "openai-compat",
		BaseURL: "http://127.0.0.1:8787/v1",
		APIKey:  "${V8800_FAKE_KEY}",
		Model:   "qwen2.5-7b",
	}
	p, err := BuildProvider(cfg)
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	if p == nil {
		t.Fatal("expected provider")
	}
}

func TestLoadConfig_ParsesProviderBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "helixon.yaml")
	body := strings.Join([]string{
		`agent_id: helixon-llm`,
		`session_dsn: file::memory:?cache=shared`,
		`provider:`,
		`  kind: openai-compat`,
		`  base_url: http://127.0.0.1:8787/v1`,
		`  api_key: sk-x`,
		`  model: qwen2.5-7b`,
		`  timeout: 30s`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Provider.Kind != "openai-compat" {
		t.Fatalf("Kind = %q", cfg.Provider.Kind)
	}
	if cfg.Provider.BaseURL != "http://127.0.0.1:8787/v1" {
		t.Fatalf("BaseURL = %q", cfg.Provider.BaseURL)
	}
	if cfg.Provider.Model != "qwen2.5-7b" {
		t.Fatalf("Model = %q", cfg.Provider.Model)
	}
	if cfg.Provider.Timeout.String() != "30s" {
		t.Fatalf("Timeout = %s", cfg.Provider.Timeout)
	}
}
