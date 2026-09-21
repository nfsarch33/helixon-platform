package helixon

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestBuildProvider_WrapsIdentityDoer is the structural control of
// v18846-3: the one provider construction site must route its HTTP through
// the identity doer, so a new provider kind cannot silently bypass
// attribution. Grep-style by design - the client's doer is not exported.
func TestBuildProvider_WrapsIdentityDoer(t *testing.T) {
	src, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatalf("read provider.go: %v", err)
	}
	if !strings.Contains(string(src), "NewIdentityDoer") {
		t.Fatal("BuildProvider does not wrap its HTTP client with llm.NewIdentityDoer; model calls would carry no run identity")
	}
}

// TestBuildProvider_OpenAICompatStillBuilds guards the wrap refactor.
func TestBuildProvider_OpenAICompatStillBuilds(t *testing.T) {
	t.Setenv("HLXN_TEST_KEY", "k")
	p, err := BuildProvider(ProviderConfig{
		Kind: "openai-compat", BaseURL: "http://127.0.0.1:1/v1",
		APIKey: "${HLXN_TEST_KEY}", Model: "m", Timeout: time.Second,
	})
	if err != nil || p == nil {
		t.Fatalf("BuildProvider: %v %v", p, err)
	}
}
