// Package choosehook v14515 enforcement wrapper.
//
// This file is appended in v14515 to integrate the rtx + contextmode +
// headroom layers (docs/token-saving-strategy.md sections 4-6).
//
// The DecideWith pipeline becomes:
//
//   1. DecideInput arrives from Cursor.
//   2. contextmode.Trim (strip ANSI / base64; truncate).
//   3. rtx.Cache.Lookup(replay_id)  → if hit, replay cached Output.
//   4. tier classifier (existing).
//   5. cell picker (existing).
//   6. headroom.Check(cell, trimmed_prompt_tokens) → if reject, return
//      Output with DecisionLabel="rejected".
//   7. rtx.Cache.Store on the way out (best-effort).
//
// The wire format (DecideInput / Output) is unchanged so the v14511
// hook installer keeps working.
package choosehook

import (
	"strings"

	"github.com/nfsarch33/helixon-platform/internal/contextmode"
	"github.com/nfsarch33/helixon-platform/internal/headroom"
	"github.com/nfsarch33/helixon-platform/internal/rtx"
)

// (v14515) ReplayID is added to DecideInput in choosehook.go; this
// file just adds helpers that read it.
var _ = strings.HasPrefix // keep imports tidy if future edits drop strings

// EnforceConfig wires the v14515 helpers into DecideWith. A zero value
// disables enforcement (preserves v14511 behaviour).
type EnforceConfig struct {
	RTX         *rtx.Cache      // nil → no caching
	ContextOpts contextmode.Options // zero → defaults
	RejectOversized bool          // true → return DecisionLabel="rejected"
}

// EnforceOutput is what DecideWith returns when enforcement kicks in.
// It is structurally identical to Output but with a `RejectReason`
// populated when the prompt was rejected.
type EnforceOutput struct {
	Output
	RejectReason string `json:"reject_reason,omitempty"`
	CacheHit     bool   `json:"cache_hit,omitempty"`
	CacheKey     string `json:"cache_key,omitempty"`
	TrimmedBytes int    `json:"trimmed_bytes,omitempty"`
}

// Enforce runs the full v14515 pipeline. `base` is the Output that
// would have been produced by the v14511 DecideWith. `promptTokens`
// is the post-trim token estimate.
//
// The function returns the (possibly overridden) Output and a boolean
// indicating whether the prompt was rejected.
func (e EnforceConfig) Enforce(base Output, in DecideInput, promptTokens int) (EnforceOutput, bool) {
	out := EnforceOutput{Output: base}
	// 1. Trim context (idempotent on already-trimmed prompts).
	trimmed := contextmode.Trim(in.Prompt, e.ContextOpts, "decide-input")
	out.TrimmedBytes = len(in.Prompt) - len(trimmed)

	// 2. RTX lookup (replay or prompt).
	if e.RTX != nil {
		key := rtx.Key(trimmed, in.Surface+"|"+in.HookMode)
		if rec, ok := e.RTX.Lookup(trimmed, in.Surface+"|"+in.HookMode, in.ReplayID, rtx.Tier(labelToTierInt(base.DecisionLabel))); ok {
			out.DecisionLabel = "tier" + tierSuffix(base.DecisionLabel)
			out.CellID = rec.CellID
			out.BaseURL = base.BaseURL // URL is cell-static; rtx only stores response text
			out.Reason = "rtx-replay"
			out.CacheHit = true
			out.CacheKey = key
			return out, false
		}
	}

	// 3. Headroom check (only if we have a cell).
	if e.RejectOversized && base.CellID != "" {
		if err := headroom.Check(base.CellID, promptTokens); err != nil {
			out.DecisionLabel = "rejected"
			out.RejectReason = err.Error()
			return out, true
		}
	}

	// 4. Persist into RTX for next replay.
	if e.RTX != nil && base.CellID != "" {
		_ = e.RTX.Store(rtx.Record{
			Key:       rtx.Key(trimmed, in.Surface+"|"+in.HookMode),
			Prompt:    trimmed,
			StateHash: in.Surface + "|" + in.HookMode,
			ReplayID:  in.ReplayID,
			Tier:      rtx.Tier(labelToTierInt(base.DecisionLabel)),
			CellID:    base.CellID,
			Response:  base.Reason, // we don't have the model's actual response here
		})
	}
	return out, false
}

// tierSuffix converts a DecisionLabel like "tier2" or "" into the
// "0..3" suffix rtx expects. Defaults to Tier0 for "no_decision".
func tierSuffix(label string) string {
	switch label {
	case "tier0", "0":
		return "0"
	case "tier1", "1":
		return "1"
	case "tier2", "2":
		return "2"
	case "tier3", "3":
		return "3"
	}
	return "0"
}

// labelToTierInt parses a DecisionLabel ("tierN" or "N" or "" or
// "no_decision") into an int in 0..3. Unknown values default to 0.
func labelToTierInt(label string) int {
	switch label {
	case "tier0", "0", "no_decision", "":
		return 0
	case "tier1", "1":
		return 1
	case "tier2", "2":
		return 2
	case "tier3", "3":
		return 3
	}
	return 0
}