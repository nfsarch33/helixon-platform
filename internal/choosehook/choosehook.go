// Package choosehook is the v14511 Cursor beforeSubmitPrompt
// decision engine.
//
// Cursor invokes a hook shell command (per ~/.cursor/hooks.json)
// before every prompt submit. The hook decides:
//
//  1. which tier (tier0..tier3) the prompt belongs to;
//  2. which cell in qwen36-matrix.yaml should serve the call;
//  3. whether to rewrite the base_url (mode "redirect", the
//     canonical move) or just stamp metadata onto the request
//     (mode "annotate", used when the agent should not be
//     forced off its current model).
//
// decide.go is the only entry point. The Choose function is the
// production router; tests use DecideWith to inject a stub.
package choosehook

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nfsarch33/helixon-platform/internal/costobs"
	"github.com/nfsarch33/helixon-platform/internal/llm/qwen36"
)

// Tier is a re-export of qwen36.Tier so callers do not need to
// import the router package directly. We replicate the type
// instead of aliasing because the hook JSON contract is the
// string "tierN" and the integer is internal-only.
type Tier = qwen36.Tier

const (
	Tier0 = qwen36.Tier0
	Tier1 = qwen36.Tier1
	Tier2 = qwen36.Tier2
	Tier3 = qwen36.Tier3
)

// DecideInput is the JSON Cursor passes to the hook. We accept
// just the fields we need so the contract stays small.
type DecideInput struct {
	Prompt   string `json:"prompt"`
	Surface  string `json:"surface,omitempty"` // editor|chat|review
	HookMode string `json:"hook_mode,omitempty"`
	// ReplayID (v14515) lets subagents pass their loop-guard id so
	// identical prompts in the same agent loop get a cache hit
	// instead of re-billing the cell.
	ReplayID string `json:"replay_id,omitempty"`
}

// Output is the JSON the hook writes to stdout. Cursor reads
// stdout to decide what to do (redirect | annotate | abstain).
type Output struct {
	SprintID       string  `json:"sprint_id"`
	DecisionLabel  string  `json:"decision_label"`     // tier0|tier1|tier2|tier3|no_decision
	CellID         string  `json:"cell_id,omitempty"`
	BaseURL        string  `json:"base_url,omitempty"`
	HookMode       string  `json:"hook_mode"`
	Reason         string  `json:"reason"`
	CapturedPrompt string  `json:"captured_prompt_sha256,omitempty"`
	EstCostUSD     float64 `json:"est_cost_usd,omitempty"`
}

// Decision is one row from the chosen router (mirrors
// qwen36.Pick output + cost attribution).
type Decision struct {
	Tier    Tier
	CellID  string
	BaseURL string
	Reason  string
}

// Router is the minimal interface Decide needs. We type it as a
// function so production passes qwen36.Pick and tests can pass a
// stub without bringing the matrix file into play.
type Router func(t Tier) (Decision, error)

// ClassifyTask turns a DecideInput into a tier using a small
// keyword heuristic. We intentionally do NOT call out to the LLM
// for this; the hook must execute < 50 ms and the heuristic
// already covers the four bands in
// internal/llm/semantic_router.go's DefaultSemanticRouterConfig.
//
// Order matters: more specific patterns first (visual before
// discovery before synthesis).
func ClassifyTask(in DecideInput) (Tier, error) {
	p := strings.ToLower(in.Prompt)
	switch {
	case contains(p, "replay", "cached"):
		return Tier0, nil
	case contains(p, "summari", "extract", "regex", "json {"):
		return Tier0, nil
	case contains(p, "discover", "find element", "css selector", "xpath"):
		return Tier1, nil
	case contains(p, "write a", "implement", "refactor", "synthesize", "mutate"):
		return Tier2, nil
	case contains(p, "audit", "review", "off-by-one", "race condition", "justify"):
		return Tier3, nil
	case contains(p, "image", "screenshot", "visual"):
		// No VLM cell currently exists; fall back to tier3 which
		// has the longest context and the MTP-draft speculative
		// decoding that helps on visual VLM-adjacent prompts.
		return Tier3, nil
	case contains(p, "score", "rubric", "evaluate", "rate this"):
		return Tier1, nil
	}
	return Tier1, nil
}

func contains(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// DecideWith returns a callable closure that lets the caller plug
// in any Router (production or stub).
func DecideWith(in DecideInput, router Router) func() (Output, error) {
	return func() (Output, error) {
		out := decide(in, router)
		return out, nil
	}
}

// decide is the pure decision function. Production uses Decide /
// DecideWith; tests use DecideWithFn. The cost-observability
// side effect is fired here so both call paths benefit.
func decide(in DecideInput, router Router) Output {
	out := Output{
		SprintID: "v14511",
		HookMode: in.HookMode,
	}
	if out.HookMode == "" {
		out.HookMode = "redirect"
	}
	out.CapturedPrompt = promptFingerprint(in.Prompt)

	tier, terr := ClassifyTask(in)
	if terr != nil {
		out.DecisionLabel = "no_decision"
		out.Reason = "classify_failed"
		writeCost(costEvent(out, tier, "error"))
		return out
	}

	d, err := router(tier)
	if err != nil {
		out.DecisionLabel = "no_decision"
		out.Reason = "no_ready_cell"
		writeCost(costEvent(out, tier, "error"))
		return out
	}

	out.DecisionLabel = tierLabel(tier)
	out.CellID = d.CellID
	out.BaseURL = d.BaseURL
	out.Reason = d.Reason
	if out.HookMode == "annotate" {
		// Annotate mode: never rewrite; let the existing client
		// keep using its own endpoint. The agent reads the
		// X-Helixon-Tier header off stdout / staging instead.
		out.BaseURL = ""
	}
	writeCost(costEvent(out, tier, "ok"))
	return out
}

// costEvent is the cost-observability row for the decision. We
// keep the Event construction in one place so any future column
// additions land in one branch.
func costEvent(out Output, tier Tier, outcome string) costobs.Event {
	model := modelFromCell(out.CellID)
	return costobs.Event{
		SprintID:       out.SprintID,
		JobID:          out.CapturedPrompt,
		CellID:         out.CellID,
		Model:          model,
		ModelTier:      int(tier),
		JobType:        "cursor.beforeSubmitPrompt",
		Outcome:        outcome,
		EstCostUSD:     costobs.EstimateCostUSD(model, 64, 256),
		EstInputTokens: 64,
		EstOutputTokens: 256,
	}
}

// Decide is the canonical entry point used by the choose-llm CLI
// sub-command. It opens the matrix, runs the classifier + router,
// and returns the JSON-serialisable Output.
func Decide(in DecideInput, matrixPath, hostOverride string) (Output, error) {
	m, err := qwen36.LoadFile(matrixPath)
	if err != nil {
		out := Output{SprintID: "v14511", DecisionLabel: "no_decision", HookMode: in.HookMode, Reason: "matrix_load_failed"}
		writeCost(costEvent(out, Tier1, "error"))
		return out, fmt.Errorf("choosehook: %w", err)
	}
	r := func(t Tier) (Decision, error) {
		cell, err := qwen36.Pick(m, t)
		if err != nil {
			return Decision{}, err
		}
		return Decision{
			Tier:    t,
			CellID:  cell.ID,
			BaseURL: cell.BaseURL(hostOverride),
			Reason:  "qwen36.Pick",
		}, nil
	}
	out := decide(in, r)
	return out, nil
}

// DecideWithFn is the in-process variant used by tests; production
// code should call Decide instead. Exposed here only so package
// doc can mention a public test API.
func DecideWithFn(in DecideInput, router Router) (Output, error) {
	out := decide(in, router)
	return out, nil
}

// tierLabel, modelFromCell, promptFingerprint, writeCost are small
// helpers, kept package-local so we do not over-export.

func tierLabel(t Tier) string {
	switch t {
	case Tier0:
		return "tier0"
	case Tier1:
		return "tier1"
	case Tier2:
		return "tier2"
	case Tier3:
		return "tier3"
	}
	return "unknown"
}

// modelFromCell is a best-effort reverse lookup from the canonical
// matrix in cursor-global-kb. Until we wire a richer per-cell
// decision row into choose-llm we accept that unknowns resolve
// to "qwen36-27b-q4".
func modelFromCell(cellID string) string {
	switch cellID {
	case "C1":
		return "qwen36-27b-int4"
	case "C2":
		return "qwen36-27b-q4"
	case "C7":
		return "qwen36-27b-mtp-q8"
	case "C8":
		return "qwen36-9b-q4"
	}
	return "qwen36-27b-q4"
}

func promptFingerprint(p string) string {
	// SHA-256 hex of the prompt is enough to keep PII out of the
	// NDJSON; the operator can later join / diff on hash.
	// Implemented as a simple fingerprint to avoid pulling in
	// crypto/sha256 just for this; production callers wire it to
	// internal/cryptohash when available.
	h := uint64(14695981039346656037)
	for _, b := range []byte(p) {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return fmt.Sprintf("fnv64a:%016x", h)
}

// tokenEstimate is rough; production should swap for an actual
// tiktoken-like library per model. We bound to 0..64k because
// every matrix cell has max_model_len <= 64k.
func tokenEstimate(p string) int {
	n := len(p) / 4
	if n < 16 {
		n = 16
	}
	if n > 65536 {
		n = 65536
	}
	return n
}

// writeCost sends one event to the cost NDJSON sink. We use
// HELIXON_COSTOBS_PATH when set, otherwise write to DefaultPath().
// Returns silently on error to keep the hook latency bound; the
// cost log is observability, not a critical path.
func writeCost(ev costobs.Event) {
	w, err := costobs.OpenFile(costobs.DefaultPath())
	if err != nil {
		return
	}
	defer w.Close()
	_ = w.Write(ev)
}

// ErrMatrixUnreadable is returned when the matrix file cannot be
// loaded; the cursor hook should fall back to no_decision in that
// case rather than blocking the submit.
var ErrMatrixUnreadable = errors.New("choosehook: matrix unreadable")
