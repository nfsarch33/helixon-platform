package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WorkspaceConfig describes the agent's workspace identity files.
type WorkspaceConfig struct {
	Dir          string
	AgentName    string
	Capabilities []string
	SoulTraits   map[string]string
	CustomFiles  map[string]string
}

// WorkspaceInjector manages the lifecycle of workspace identity files
// (AGENTS.md, SOUL.md, USER.md) that prime an agent's system prompt.
type WorkspaceInjector struct {
	dir string
}

// NewWorkspaceInjector ensures the workspace directory exists and returns
// the injector.
func NewWorkspaceInjector(dir string) (*WorkspaceInjector, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace dir: %w", err)
	}
	return &WorkspaceInjector{dir: dir}, nil
}

// WriteAGENTSMD generates and writes the AGENTS.md identity file.
func (w *WorkspaceInjector) WriteAGENTSMD(cfg WorkspaceConfig) error {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s Agent\n\n", cfg.AgentName))
	sb.WriteString(fmt.Sprintf("Generated: %s\n\n", time.Now().UTC().Format(time.RFC3339)))

	if len(cfg.Capabilities) > 0 {
		sb.WriteString("## Capabilities\n\n")
		for _, cap := range cfg.Capabilities {
			sb.WriteString(fmt.Sprintf("- %s\n", cap))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Operating Constraints\n\n")
	sb.WriteString("- Do not execute arbitrary shell commands without safety review\n")
	sb.WriteString("- Respect token budget limits per session\n")
	sb.WriteString("- Report errors through the control plane, not stdout\n")
	sb.WriteString("- Always use structured logging (slog)\n")

	return os.WriteFile(filepath.Join(w.dir, "AGENTS.md"), []byte(sb.String()), 0o644)
}

// WriteSOULMD generates and writes the SOUL.md personality/traits file.
func (w *WorkspaceInjector) WriteSOULMD(agentName string, traits map[string]string) error {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s Soul\n\n", agentName))

	if len(traits) == 0 {
		traits = map[string]string{
			"Approach":    "Methodical, evidence-based",
			"Tone":        "Direct, concise",
			"Error Style": "Fail-fast with clear diagnostics",
			"Learning":    "Captures patterns for continuous improvement",
		}
	}

	sb.WriteString("## Personality Traits\n\n")
	for k, v := range traits {
		sb.WriteString(fmt.Sprintf("- **%s**: %s\n", k, v))
	}

	return os.WriteFile(filepath.Join(w.dir, "SOUL.md"), []byte(sb.String()), 0o644)
}

// WriteUSERMD generates and writes the USER.md context file.
func (w *WorkspaceInjector) WriteUSERMD(userID string, preferences map[string]string) error {
	var sb strings.Builder
	sb.WriteString("# User Context\n\n")
	sb.WriteString(fmt.Sprintf("User ID: %s\n\n", userID))

	if len(preferences) > 0 {
		sb.WriteString("## Preferences\n\n")
		for k, v := range preferences {
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", k, v))
		}
	}

	return os.WriteFile(filepath.Join(w.dir, "USER.md"), []byte(sb.String()), 0o644)
}

// WriteCustomFile writes an arbitrary identity file to the workspace.
func (w *WorkspaceInjector) WriteCustomFile(name, content string) error {
	return os.WriteFile(filepath.Join(w.dir, name), []byte(content), 0o644)
}

// ReadAll reads all identity files from the workspace and returns them
// as a concatenated system prompt section.
func (w *WorkspaceInjector) ReadAll() (string, error) {
	files := []string{"AGENTS.md", "SOUL.md", "USER.md"}
	var sb strings.Builder

	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(w.dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		sb.WriteString(fmt.Sprintf("--- %s ---\n", name))
		sb.Write(data)
		sb.WriteString("\n\n")
	}

	return sb.String(), nil
}

// Dir returns the workspace directory path.
func (w *WorkspaceInjector) Dir() string {
	return w.dir
}
