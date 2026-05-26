// Package builtins exposes the v8900-B8..B11 set of built-in tools that
// any helixon Runtime can register into its tooldispatch.Registry. The
// tools share a small surface so the platform server, the REPL, and the
// fleet-agent (block 2) can all draw on the same vetted handlers.
//
// Tools:
//
//	shell        — bounded shell exec; allow-listed commands only.
//	web_fetch    — HTTP(S) GET with size/time bounds.
//	memory       — read/write/search the runtime's HybridSearcher.
//	sprintboard  — register/heartbeat/claim/complete/sprint-status against
//	               the controlplane SprintboardClient.
//
// Each tool returns a string payload (JSON-encoded for structured tools)
// to fit the existing tooldispatch.ToolFunc contract. Tool schemas are
// the OpenAI/Anthropic JSON-schema subset already enforced by
// tooldispatch.validateArgs.
package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
	"github.com/nfsarch33/helixon-platform/internal/helixon/memory"
	"github.com/nfsarch33/helixon-platform/internal/helixon/tooldispatch"
)

// ShellAllowedCommands is the default allow-list. The shell tool is
// deliberately restrictive: anything that mutates the working tree, the
// filesystem, or the user environment lives outside this surface.
var ShellAllowedCommands = []string{
	"echo", "ls", "pwd", "uname", "whoami", "date", "hostname",
	"cat", "head", "tail", "wc", "grep", "find",
	"git", "go", "make",
}

// ShellConfig configures the shell tool.
type ShellConfig struct {
	AllowedCommands []string
	Timeout         time.Duration
	MaxOutputBytes  int
}

func (c ShellConfig) withDefaults() ShellConfig {
	if len(c.AllowedCommands) == 0 {
		c.AllowedCommands = ShellAllowedCommands
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = 64 * 1024
	}
	return c
}

// ShellTool returns a tooldispatch.ToolDef for bounded shell execution.
func ShellTool(cfg ShellConfig) tooldispatch.ToolDef {
	cfg = cfg.withDefaults()
	allow := make(map[string]struct{}, len(cfg.AllowedCommands))
	for _, c := range cfg.AllowedCommands {
		allow[c] = struct{}{}
	}
	return tooldispatch.ToolDef{
		Name:        "shell",
		Description: "Execute an allow-listed shell command and return stdout/stderr.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["command"],
			"properties": {
				"command": {"type": "string", "description": "Allow-listed binary name (e.g. 'git', 'ls')."},
				"args":    {"type": "array", "description": "Arguments to pass to the command."}
			}
		}`),
		Timeout: cfg.Timeout,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			cmdName, _ := args["command"].(string)
			if cmdName == "" {
				return "", errors.New("command is required")
			}
			if _, ok := allow[cmdName]; !ok {
				return "", fmt.Errorf("command %q is not allow-listed", cmdName)
			}
			var argv []string
			if raw, ok := args["args"].([]any); ok {
				for _, a := range raw {
					s, ok := a.(string)
					if !ok {
						return "", fmt.Errorf("args entries must be strings, got %T", a)
					}
					argv = append(argv, s)
				}
			}
			cmd := exec.CommandContext(ctx, cmdName, argv...)
			out, err := cmd.CombinedOutput()
			result := truncateBytes(string(out), cfg.MaxOutputBytes)
			if err != nil {
				return result, fmt.Errorf("shell %s: %w", cmdName, err)
			}
			return result, nil
		},
	}
}

// WebFetchConfig configures the web_fetch tool.
type WebFetchConfig struct {
	HTTPClient   *http.Client
	MaxBodyBytes int64
	UserAgent    string
	AllowSchemes []string
}

func (c WebFetchConfig) withDefaults() WebFetchConfig {
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = 256 * 1024
	}
	if c.UserAgent == "" {
		c.UserAgent = "helixon-platform/v8900"
	}
	if len(c.AllowSchemes) == 0 {
		c.AllowSchemes = []string{"http", "https"}
	}
	return c
}

// WebFetchTool returns a tool that issues HTTP(S) GETs with bounded
// body size. It is intentionally read-only; POST/PUT/DELETE are rejected.
func WebFetchTool(cfg WebFetchConfig) tooldispatch.ToolDef {
	cfg = cfg.withDefaults()
	allow := make(map[string]struct{}, len(cfg.AllowSchemes))
	for _, s := range cfg.AllowSchemes {
		allow[s] = struct{}{}
	}
	return tooldispatch.ToolDef{
		Name:        "web_fetch",
		Description: "Fetch a URL via HTTP(S) GET and return the response body (truncated).",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["url"],
			"properties": {
				"url": {"type": "string", "description": "Absolute http(s) URL to fetch."}
			}
		}`),
		Timeout: 30 * time.Second,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			rawURL, _ := args["url"].(string)
			if rawURL == "" {
				return "", errors.New("url is required")
			}
			scheme := schemeOf(rawURL)
			if _, ok := allow[scheme]; !ok {
				return "", fmt.Errorf("scheme %q not allowed", scheme)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
			if err != nil {
				return "", fmt.Errorf("web_fetch: %w", err)
			}
			req.Header.Set("User-Agent", cfg.UserAgent)
			resp, err := cfg.HTTPClient.Do(req)
			if err != nil {
				return "", fmt.Errorf("web_fetch: %w", err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, cfg.MaxBodyBytes))
			if err != nil {
				return "", fmt.Errorf("web_fetch: read body: %w", err)
			}
			out := map[string]any{
				"status":  resp.StatusCode,
				"headers": flattenHeaders(resp.Header),
				"body":    string(body),
			}
			data, _ := json.Marshal(out)
			return string(data), nil
		},
	}
}

// MemoryTool wires the runtime's HybridSearcher into a tool. Pass a nil
// searcher to disable: the tool will register but every call returns an
// "unconfigured" error, keeping the schema visible to the LLM.
func MemoryTool(searcher *memory.HybridSearcher, defaultAppID, defaultUserID string) tooldispatch.ToolDef {
	return tooldispatch.ToolDef{
		Name:        "memory",
		Description: "Read, write, or search the agent's hybrid memory store.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["op"],
			"properties": {
				"op":      {"type": "string", "description": "One of: read, write, search."},
				"id":      {"type": "string", "description": "Memory id for read."},
				"query":   {"type": "string", "description": "Query string for search."},
				"content": {"type": "string", "description": "Content to write."},
				"app_id":  {"type": "string", "description": "Override default app_id."},
				"user_id": {"type": "string", "description": "Override default user_id."}
			}
		}`),
		Timeout: 15 * time.Second,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if searcher == nil {
				return "", errors.New("memory tool: HybridSearcher not configured")
			}
			op, _ := args["op"].(string)
			appID, _ := args["app_id"].(string)
			if appID == "" {
				appID = defaultAppID
			}
			userID, _ := args["user_id"].(string)
			if userID == "" {
				userID = defaultUserID
			}
			switch op {
			case "read":
				id, _ := args["id"].(string)
				if id == "" {
					return "", errors.New("memory.read: id is required")
				}
				m, err := searcher.Read(ctx, id)
				if err != nil {
					return "", err
				}
				data, _ := json.Marshal(m)
				return string(data), nil
			case "write":
				content, _ := args["content"].(string)
				if content == "" {
					return "", errors.New("memory.write: content is required")
				}
				m, err := searcher.Write(ctx, content, appID, userID)
				if err != nil {
					return "", err
				}
				data, _ := json.Marshal(m)
				return string(data), nil
			case "search":
				q, _ := args["query"].(string)
				if q == "" {
					return "", errors.New("memory.search: query is required")
				}
				results, err := searcher.Search(ctx, q, appID, userID)
				if err != nil {
					return "", err
				}
				data, _ := json.Marshal(results)
				return string(data), nil
			default:
				return "", fmt.Errorf("memory: unknown op %q", op)
			}
		},
	}
}

// SprintboardTool wraps the controlplane.SprintboardClient.
func SprintboardTool(client *controlplane.SprintboardClient) tooldispatch.ToolDef {
	return tooldispatch.ToolDef{
		Name:        "sprintboard",
		Description: "Interact with the sprintboard control plane (register, claim, complete, status).",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["op"],
			"properties": {
				"op":        {"type": "string", "description": "One of: register, claim, complete, sprint_status."},
				"ticket_id": {"type": "string"},
				"sprint_id": {"type": "string"},
				"evidence":  {"type": "string"}
			}
		}`),
		Timeout: 20 * time.Second,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if client == nil {
				return "", errors.New("sprintboard tool: client not configured")
			}
			op, _ := args["op"].(string)
			switch op {
			case "register":
				if err := client.Register(ctx); err != nil {
					return "", err
				}
				return `{"ok":true,"op":"register"}`, nil
			case "claim":
				ticket, _ := args["ticket_id"].(string)
				if ticket == "" {
					return "", errors.New("sprintboard.claim: ticket_id is required")
				}
				if err := client.ClaimTicket(ctx, ticket); err != nil {
					return "", err
				}
				return fmt.Sprintf(`{"ok":true,"op":"claim","ticket_id":%q}`, ticket), nil
			case "complete":
				ticket, _ := args["ticket_id"].(string)
				evidence, _ := args["evidence"].(string)
				if ticket == "" {
					return "", errors.New("sprintboard.complete: ticket_id is required")
				}
				if err := client.CompleteTicket(ctx, ticket, evidence); err != nil {
					return "", err
				}
				return fmt.Sprintf(`{"ok":true,"op":"complete","ticket_id":%q}`, ticket), nil
			case "sprint_status":
				sprint, _ := args["sprint_id"].(string)
				if sprint == "" {
					return "", errors.New("sprintboard.sprint_status: sprint_id is required")
				}
				st, err := client.SprintStatus(ctx, sprint)
				if err != nil {
					return "", err
				}
				data, _ := json.Marshal(st)
				return string(data), nil
			default:
				return "", fmt.Errorf("sprintboard: unknown op %q", op)
			}
		},
	}
}

// FileReadConfig configures the file_read tool.
type FileReadConfig struct {
	MaxBytes     int64
	AllowedPaths []string // if empty, all paths allowed
	Timeout      time.Duration
}

func (c FileReadConfig) withDefaults() FileReadConfig {
	if c.MaxBytes <= 0 {
		c.MaxBytes = 256 * 1024
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	return c
}

// FileReadTool returns a tool that reads file contents with size bounds.
func FileReadTool(cfg FileReadConfig) tooldispatch.ToolDef {
	cfg = cfg.withDefaults()
	return tooldispatch.ToolDef{
		Name:        "file_read",
		Description: "Read the contents of a file at a given path. Returns the file content as a string.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["path"],
			"properties": {
				"path": {"type": "string", "description": "Absolute or relative file path to read."}
			}
		}`),
		Timeout: cfg.Timeout,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", errors.New("path is required")
			}
			if len(cfg.AllowedPaths) > 0 {
				allowed := false
				for _, prefix := range cfg.AllowedPaths {
					if strings.HasPrefix(path, prefix) {
						allowed = true
						break
					}
				}
				if !allowed {
					return "", fmt.Errorf("path %q is not within allowed directories", path)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("file_read: %w", err)
			}
			if int64(len(data)) > cfg.MaxBytes {
				return string(data[:cfg.MaxBytes]) + "\n... [truncated]", nil
			}
			return string(data), nil
		},
	}
}

// FileWriteConfig configures the file_write tool.
type FileWriteConfig struct {
	MaxBytes     int64
	AllowedPaths []string
	Timeout      time.Duration
}

func (c FileWriteConfig) withDefaults() FileWriteConfig {
	if c.MaxBytes <= 0 {
		c.MaxBytes = 256 * 1024
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	return c
}

// FileWriteTool returns a tool that writes content to a file.
func FileWriteTool(cfg FileWriteConfig) tooldispatch.ToolDef {
	cfg = cfg.withDefaults()
	return tooldispatch.ToolDef{
		Name:        "file_write",
		Description: "Write content to a file at a given path. Creates the file if it doesn't exist, overwrites if it does.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["path", "content"],
			"properties": {
				"path":    {"type": "string", "description": "Absolute or relative file path to write."},
				"content": {"type": "string", "description": "Content to write to the file."}
			}
		}`),
		Timeout: cfg.Timeout,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)
			if path == "" {
				return "", errors.New("path is required")
			}
			if len(cfg.AllowedPaths) > 0 {
				allowed := false
				for _, prefix := range cfg.AllowedPaths {
					if strings.HasPrefix(path, prefix) {
						allowed = true
						break
					}
				}
				if !allowed {
					return "", fmt.Errorf("path %q is not within allowed directories", path)
				}
			}
			if int64(len(content)) > cfg.MaxBytes {
				return "", fmt.Errorf("content exceeds max size of %d bytes", cfg.MaxBytes)
			}
			dir := filepath.Dir(path)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("file_write: create directory: %w", err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return "", fmt.Errorf("file_write: %w", err)
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
		},
	}
}

// RegisterAll registers every configured tool into the registry. Pass nil
// for a tool's config struct to skip it. The function is idempotent per
// registry but a subsequent identical Register call returns an error
// (matching tooldispatch.Registry semantics).
func RegisterAll(reg *tooldispatch.Registry, opts Options) error {
	defs := opts.Defs()
	var firstErr error
	for _, d := range defs {
		if err := reg.Register(d); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Options bundles config for RegisterAll.
type Options struct {
	Shell        *ShellConfig
	WebFetch     *WebFetchConfig
	FileRead     *FileReadConfig
	FileWrite    *FileWriteConfig
	Memory       *memory.HybridSearcher
	MemoryAppID  string
	MemoryUserID string
	Sprintboard  *controlplane.SprintboardClient
}

// Defs returns the slice of ToolDefs that Options describes, in stable
// order (so test assertions can compare slices directly).
func (o Options) Defs() []tooldispatch.ToolDef {
	var defs []tooldispatch.ToolDef
	if o.Shell != nil {
		defs = append(defs, ShellTool(*o.Shell))
	}
	if o.WebFetch != nil {
		defs = append(defs, WebFetchTool(*o.WebFetch))
	}
	if o.FileRead != nil {
		defs = append(defs, FileReadTool(*o.FileRead))
	}
	if o.FileWrite != nil {
		defs = append(defs, FileWriteTool(*o.FileWrite))
	}
	if o.Memory != nil {
		defs = append(defs, MemoryTool(o.Memory, o.MemoryAppID, o.MemoryUserID))
	}
	if o.Sprintboard != nil {
		defs = append(defs, SprintboardTool(o.Sprintboard))
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

func truncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]"
}

func schemeOf(rawURL string) string {
	if i := strings.Index(rawURL, "://"); i > 0 {
		return strings.ToLower(rawURL[:i])
	}
	return ""
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}
