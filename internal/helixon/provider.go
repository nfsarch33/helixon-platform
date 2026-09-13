package helixon

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// ProviderConfig is the YAML-friendly description of which LLM provider
// the runtime should be wired to. It is intentionally permissive: an
// empty / "none" Kind returns a nil provider (the runtime then runs in
// echo-only smoke mode).
type ProviderConfig struct {
	// Kind selects the backend. Supported: "none", "mock", "openai-compat".
	Kind string `yaml:"kind"`
	// BaseURL is the API root (e.g. http://127.0.0.1:8787/v1 for vLLM).
	BaseURL string `yaml:"base_url"`
	// APIKey supports ${ENV_VAR} expansion so secrets stay out of YAML.
	APIKey string `yaml:"api_key"`
	// Model is the provider-side model identifier.
	Model string `yaml:"model"`
	// Timeout is the per-request HTTP timeout. Zero means "use provider default".
	Timeout time.Duration `yaml:"-"`
	// TimeoutString is the YAML mirror parsed into Timeout by FileConfig.ToRuntimeConfig.
	TimeoutString string `yaml:"timeout"`
}

// BuildProvider returns an llm.Provider for the given config. A "none"
// kind (or empty Kind) returns (nil, nil) so the caller can decide
// whether the runtime should run without an LLM.
func BuildProvider(cfg ProviderConfig) (llm.Provider, error) {
	kind := strings.ToLower(strings.TrimSpace(cfg.Kind))
	switch kind {
	case "", "none":
		return nil, nil
	case "mock":
		return llm.NewMockProvider(), nil
	case "openai-compat", "openai":
		if cfg.BaseURL == "" {
			return nil, errors.New("helixon: provider kind=openai-compat requires base_url")
		}
		if cfg.Model == "" {
			return nil, errors.New("helixon: provider kind=openai-compat requires model")
		}
		key, err := expandEnv(cfg.APIKey)
		if err != nil {
			return nil, err
		}
		return llm.NewClient(llm.Config{
			BaseURL: cfg.BaseURL,
			APIKey:  key,
			Model:   cfg.Model,
			Timeout: cfg.Timeout,
		}), nil
	default:
		return nil, fmt.Errorf("helixon: unknown provider kind %q", cfg.Kind)
	}
}

// expandEnv resolves a single ${VAR} placeholder. A literal value is returned as-is.
func expandEnv(v string) (string, error) {
	return expandEnvNamed(v, "api_key")
}

// expandEnvNamed is expandEnv with the config field named in every error, so
// a sprintboard.token failure cannot read as an api_key one. A variable that
// is set but EMPTY passes through as ""; callers that need a non-empty value
// check for it themselves.
func expandEnvNamed(v, field string) (string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "${") || !strings.HasSuffix(v, "}") {
		return v, nil
	}
	name := strings.TrimSuffix(strings.TrimPrefix(v, "${"), "}")
	if name == "" {
		return "", fmt.Errorf("helixon: empty env var reference in %s", field)
	}
	val, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("helixon: %s env var %q is not set", field, name)
	}
	return val, nil
}
