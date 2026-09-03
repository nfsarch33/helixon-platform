// Demo subcommand for v18684-5.
//
// `helixon-eval demo --backend minimax` issues one chat-completion call
// against the configured backend and emits:
//
//   1. A one-line JSON summary on stdout (status, latency, tokens, est. cost).
//   2. An NDJSON line on the audit stream (`~/logs/helixon-eval-demo.ndjson`)
//      carrying the same payload plus the run id, ISO 8601 timestamp, and
//      caller machine-id.
//
// The `--backend` flag picks which 1Password item to resolve. Today the
// canonical pair is `minimax` (China token-plan) and `qwen` (Aliyun).
// Adding a new backend is a single map entry plus a corresponding 1Password
// item; no schema changes required.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// demoSpec describes one supported backend.
//
//	ItemEnv/FieldID/BaseURL/Model are deliberately declared per backend so
//	the operator can add a new provider without touching the rest of the
//	file. Keep them here in a single map so the wiring stays visible at a
//	glance.
type demoSpec struct {
	ItemEnv   string // env var carrying the 1Password item UUID for this backend
	FieldID   string // field id; never display names
	BaseURL   string // OpenAI-compatible chat-completions base
	ModelName string // upstream model id (sent in the body)
}

// backendSpecs is the canonical v18684-5 registry. Add new backends
// here; do NOT introduce ad-hoc strings elsewhere.
//
// The vault name and the item UUIDs are deliberately NOT in this file.
// This repository is public, and a vault name plus a set of item UUIDs is
// an internal map of the secret store — not a secret in itself, but
// exactly what the `public-repo-gate` job exists to keep out. Both are
// supplied by the operator's environment at run time:
//
//	HLXN_OP_VAULT           → 1Password vault name, shared by every backend
//	HLXN_OP_ITEM_<BACKEND>  → 26-char item UUID for that one backend
//
// Per `1password-uuid-required.mdc` an item must be referenced by its
// 26-char UUID and never by display name. resolveOPRef enforces that on
// the resolved value, which is a stronger check than pinning a literal
// here: it also catches an operator who exports a display name.
var backendSpecs = map[string]demoSpec{
	"minimax": {
		ItemEnv:   "HLXN_OP_ITEM_MINIMAX",
		FieldID:   "api-key",
		BaseURL:   "https://api.minimaxi.com/v1",
		ModelName: "MiniMax-M3",
	},
	"qwen": {
		ItemEnv:   "HLXN_OP_ITEM_QWEN",
		FieldID:   "password",
		BaseURL:   "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
		ModelName: "qwen3.7-max",
	},
}

// vaultEnv names the environment variable carrying the 1Password vault.
//
// It has no default on purpose. A baked-in default is the internal map
// this indirection exists to remove, and a *wrong* default is worse than
// no default: it would point `op read` at whatever vault happens to carry
// that name in the caller's account. Unset is a loud, immediate error.
const vaultEnv = "HLXN_OP_VAULT"

// opItemUUIDLen is the length of a 1Password item UUID. Enforced so a
// display name (which `op read` also accepts, and which leaks more than a
// UUID does) cannot be substituted by accident.
const opItemUUIDLen = 26

// demoNDJSONPath is the canonical audit stream for v18684-5 demos.
// Per `evidence-first.mdc` we always emit a durable artefact; here it
// is a single NDJSON line per run so downstream tooling can tail the
// stream and roll costs up.
const demoNDJSONPath = "~/logs/helixon-eval-demo.ndjson"

// demoResult is the JSON payload we write to stdout AND append to the
// NDJSON stream. Keep the schema flat so it indexes cleanly in SQL.
//
//	RunID  is a UTC timestamp with random suffix; sufficient for log
//	scraping without a full UUID dependency.
//	Status is one of ok, op-error, llm-error, shape-error.
type demoResult struct {
	RunID      string  `json:"run_id"`
	StartedAt  string  `json:"started_at"`
	DurationMS int64   `json:"duration_ms"`
	Backend    string  `json:"backend"`
	Model      string  `json:"model"`
	Status     string  `json:"status"`
	StatusCode int     `json:"status_code,omitempty"`
	PromptTok  int     `json:"prompt_tokens"`
	OutputTok  int     `json:"output_tokens"`
	TotalTok   int     `json:"total_tokens"`
	EstCostUSD float64 `json:"est_cost_usd"`
	MachineID  string  `json:"machine_id"`
	FirstChars string  `json:"first_chars,omitempty"`
	ErrSnippet string  `json:"err_snippet,omitempty"`
	NDJSONPath string  `json:"ndjson_path"`
}

func newDemoCmd() *cobra.Command {
	var (
		backend string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "issue one live chat-completion call against a configured backend and emit a JSON + NDJSON line",
		Long: `helixon-eval demo is the 1-2h CLI wrapper for live LLM demos (v18684-5).

It resolves the API key from the operator's 1Password via ` + "`op read --out-file -f`" + `,
calls the configured backend with a fixed probe prompt, tracks tokens via
the safety.CostEstimator pricing table, and writes both a stdout summary
and a single NDJSON line to ~/logs/helixon-eval-demo.ndjson.

The vault and item are read from the environment, not from source — this
repository is public and a vault name plus item UUIDs is an internal map
of the secret store. Export before running:

    HLXN_OP_VAULT           1Password vault name (all backends)
    HLXN_OP_ITEM_MINIMAX    26-char item UUID for --backend minimax
    HLXN_OP_ITEM_QWEN       26-char item UUID for --backend qwen

The probe prompt is intentionally tiny (<= 32 tokens) so the demo is cheap
enough to run idempotently and so the est_cost is bounded. Sprint plan
v18685-3 expands this to a full 7-task matrix via the same plumbing.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			spec, ok := backendSpecs[backend]
			if !ok {
				return fmt.Errorf("unknown --backend %q (supported: %s)",
					backend, strings.Join(supportedBackends(), ", "))
			}
			res := runDemoOnce(spec, backend)
			return writeDemoResult(out, res, asJSON)
		},
	}
	cmd.Flags().StringVar(&backend, "backend", "minimax",
		"backend key: minimax | qwen (see backendSpecs in demo.go)")
	cmd.Flags().BoolVar(&asJSON, "json", true,
		"emit JSON summary on stdout (default true)")
	return cmd
}

func supportedBackends() []string {
	out := make([]string, 0, len(backendSpecs))
	for k := range backendSpecs {
		out = append(out, k)
	}
	return out
}

// resolveOPRef resolves the vault name and item UUID for one backend from
// the operator's environment.
//
// Neither value is a secret, but together they map the secret store, so
// they live in the environment rather than in this public repository.
//
// Error strings here land in demoResult.ErrSnippet, which is written to
// stdout and appended to the NDJSON audit stream. They therefore report
// the *length* of a malformed item UUID and never the value itself: an
// operator who pastes the API key into HLXN_OP_ITEM_* by mistake must not
// have it echoed into a log file.
func resolveOPRef(spec demoSpec) (vault, itemUUID string, err error) {
	vault = strings.TrimSpace(os.Getenv(vaultEnv))
	if vault == "" {
		return "", "", fmt.Errorf("%s is unset; export the 1Password vault name before running `helixon-eval demo`", vaultEnv)
	}
	if spec.ItemEnv == "" {
		return "", "", errors.New("backend spec has no ItemEnv; every backend must name the env var carrying its item UUID")
	}
	itemUUID = strings.TrimSpace(os.Getenv(spec.ItemEnv))
	if itemUUID == "" {
		return "", "", fmt.Errorf("%s is unset; export the 1Password item UUID for this backend", spec.ItemEnv)
	}
	if len(itemUUID) != opItemUUIDLen {
		return "", "", fmt.Errorf("%s holds %d characters; expected a %d-character 1Password item UUID (display names are not accepted)",
			spec.ItemEnv, len(itemUUID), opItemUUIDLen)
	}
	return vault, itemUUID, nil
}

// runDemoOnce performs the actual demo: op read, llmcall, cost, NDJSON.
//
// Errors at any stage are captured into the demoResult struct (not
// returned) so the CLI exits with status 0 on a successful *call attempt*
// even when the upstream fails; this keeps the NDJSON stream usable
// for triage. A genuine operator-facing error (unknown backend, op
// not on PATH) is surfaced via RunE.
func runDemoOnce(spec demoSpec, backendKey string) demoResult {
	startedAt := time.Now().UTC()
	res := demoResult{
		RunID:      fmt.Sprintf("%s-%d", startedAt.Format("20060102T150405Z"), os.Getpid()),
		StartedAt:  startedAt.Format(time.RFC3339Nano),
		Backend:    backendKey,
		Model:      spec.ModelName,
		Status:     "unknown",
		NDJSONPath: demoNDJSONPath,
		MachineID:  demoMachineID(),
	}

	vault, itemUUID, err := resolveOPRef(spec)
	if err != nil {
		res.Status = "op-error"
		res.ErrSnippet = err.Error()
		res.DurationMS = time.Since(startedAt).Milliseconds()
		return res
	}

	apiKey, err := opReadSecretFn(vault, itemUUID, spec.FieldID)
	if err != nil {
		res.Status = "op-error"
		res.ErrSnippet = err.Error()
		res.DurationMS = time.Since(startedAt).Milliseconds()
		return res
	}

	cfg := llm.OpenAIDirectConfig{
		BaseURL: spec.BaseURL,
		APIKey:  apiKey,
		Model:   spec.ModelName,
		Timeout: 30 * time.Second,
	}
	client := llm.NewOpenAIDirectClient(cfg)

	prompt := "Reply with one word: OK."
	temperature := 0.0
	maxTokens := 16
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Complete(ctx, llm.CompletionRequest{
		Model:       spec.ModelName,
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
	})
	res.DurationMS = time.Since(startedAt).Milliseconds()
	if err != nil {
		res.Status = "llm-error"
		res.ErrSnippet = err.Error()
		return res
	}
	if len(resp.Choices) == 0 {
		res.Status = "shape-error"
		res.ErrSnippet = "no choices in response"
		return res
	}
	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	if n := len(content); n > 80 {
		content = content[:80]
	}
	res.FirstChars = content
	res.PromptTok = resp.Usage.PromptTokens
	res.OutputTok = resp.Usage.CompletionTokens
	res.TotalTok = resp.Usage.TotalTokens
	res.EstCostUSD = estimateCost(spec.ModelName, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	res.Status = "ok"
	return res
}

// opReadSecretFn is the indirection that lets tests stub out 1Password
// access. Production calls go through opReadSecret (below). The split
// keeps the live-call path explicit and the test path hermetic.
var opReadSecretFn = opReadSecret

// opReadSecret wraps `op read op://<vault>/<item-uuid>/<field>` with the
// safety patterns enforced by `1password-usage.mdc`:
//
//   - reads via temp file (--out-file -f); never argv, never pipe
//   - file is created 0600 and removed after capture
//   - bounded timeout to avoid hangs
//
// vault and itemUUID come from resolveOPRef, i.e. from the operator's
// environment; they are never literals in this repository.
//
// Returns the secret as a Go string in memory; caller MUST treat it as
// transient per `1password-redact.mdc`.
func opReadSecret(vault, itemUUID, fieldID string) (string, error) {
	if _, err := exec.LookPath("op"); err != nil {
		return "", errors.New("op CLI not on PATH; install 1Password CLI before running `helixon-eval demo`")
	}
	tmp, err := os.CreateTemp("", "op-key-*.txt")
	if err != nil {
		return "", fmt.Errorf("op-read: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	ref := fmt.Sprintf("op://%s/%s/%s", vault, itemUUID, fieldID)
	cmd := exec.Command("op", "read", "--force", "--out-file", tmpPath, ref) //nolint:gosec // G204: refs are operator-vetted, never argv-secret
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("op read %s: %w (%s)", ref, err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(tmpPath) //nolint:gosec // G304: tmpPath is mkstemp'd by us in this process
	if err != nil {
		return "", fmt.Errorf("op-read: capture: %w", err)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

// estimateCost returns the USD cost for the given token usage, using the
// safety.TokenCost pricing map for known model families. Unknown models
// fall back to 0.0 to keep the demo auditable (v18684-5 deliberately
// does NOT estimate costs for unknown providers).
func estimateCost(model string, in, out int) float64 {
	switch {
	case strings.HasPrefix(model, "MiniMax"):
		// MiniMaxi China token-plan is per-token; v18684-5 uses the published
		// rates on https://api.minimaxi.com/pricing (last verified 2026-07-18):
		//   MiniMax-M3 input  0.001 yuan / 1k tokens  ~= 0.00014 USD / 1k
		//   MiniMax-M3 output 0.008 yuan / 1k tokens  ~= 0.00112 USD / 1k
		// (1 yuan ~ 0.14 USD at the 2026-07-18 reference rate.)
		const inRMB, outRMB = 0.001, 0.008
		const fxRMBtoUSD = 0.14
		return (float64(in)/1000.0)*inRMB*fxRMBtoUSD +
			(float64(out)/1000.0)*outRMB*fxRMBtoUSD
	case strings.HasPrefix(model, "qwen"):
		// Aliyun Qwen token-plan pricing for `qwen3.7-max` is bundled into
		// the plan; treat as zero incremental cost in the demo.
		return 0.0
	default:
		return 0.0
	}
}

// demoMachineID returns a stable per-host tag. wsl3 uses the runtime
// hostname; other hosts prefix with $HOSTNAME. Used as NDJSON
// provenance per `async-multi-machine-coordination.mdc`.
func demoMachineID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

func writeDemoResult(w io.Writer, res demoResult, asJSON bool) error {
	// 1) STDOUT summary
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(w, "demo %s status=%s tokens=%d cost=%.4fUSD\n",
			res.RunID, res.Status, res.TotalTok, res.EstCostUSD)
	}

	// 2) NDJSON audit line (best-effort; failures here never break the demo)
	if err := appendDemoNDJSON(res); err != nil {
		_, _ = fmt.Fprintf(w, "warning: NDJSON append failed: %v\n", err)
	}
	return nil
}

// appendDemoNDJSON appends one line to the audit stream. It uses 0600
// perms on first creation so the secret-bearing key never sits in a
// world-readable file.
func appendDemoNDJSON(res demoResult) error {
	path := demoNDJSONPath
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { //nolint:gosec // G301: 0750 is correct for ~/logs runtime cache
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: operator-curated audit stream
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	return enc.Encode(res)
}
