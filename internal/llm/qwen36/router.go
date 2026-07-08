package qwen36

import (
	"errors"
	"fmt"
	"sort"
)

// Tier is the priority band a caller asks the router for. We mirror
// the closeout plan's tier0..tier3 naming; higher tier number = harder
// problem class.
type Tier int

const (
	// Tier0 = pattern replay / cheap drafting. Pick the smallest
	// ready cell we have (lowest min_free_mib, lowest context).
	Tier0 Tier = 0
	// Tier1 = heuristic planning. Prefer the ready cell with the
	// largest context window so we can hold the whole plan in
	// memory; break ties by engine preference.
	Tier1 Tier = 1
	// Tier2 = code synthesis. Prefer a vllm-served cell for
	// throughput + continuous batching; fall back to llama.cpp
	// when vllm is not ready.
	Tier2 Tier = 2
	// Tier3 = deep review / reasoning. Prefer a cell that supports
	// speculative decoding (SpecType != ""); fall back to the
	// largest ready cell when no spec-capable cell is available.
	Tier3 Tier = 3
)

// ErrNoReadyCell is returned when no cells in the matrix are
// status==ready. The CLI surfaces this as a clear "go fix your
// matrix" message instead of pretending the call succeeded.
var ErrNoReadyCell = errors.New("qwen36 router: no ready cells")

// Pick selects the best cell for the requested tier. It is the
// only entry point the cmd/choose-llm CLI and the v14511 hook
// should call. Errors are wrapped with the offending tier so
// operators can paste them straight into an issue.
func Pick(m *Matrix, tier Tier) (Cell, error) {
	if tier < Tier0 || tier > Tier3 {
		return Cell{}, fmt.Errorf("tier %d out of range [0..3]", tier)
	}
	ready := m.Ready()
	if len(ready) == 0 {
		return Cell{}, ErrNoReadyCell
	}

	var scored []scoredCell
	for _, c := range ready {
		switch tier {
		case Tier0:
			scored = append(scored, scoredCell{cell: c, score: scoreTier0(c)})
		case Tier1:
			scored = append(scored, scoredCell{cell: c, score: scoreTier1(c)})
		case Tier2:
			scored = append(scored, scoredCell{cell: c, score: scoreTier2(c)})
		case Tier3:
			scored = append(scored, scoredCell{cell: c, score: scoreTier3(c)})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		// Higher score wins. Stable so ties resolve in input
		// order; we sort the input so the more deterministic
		// tie-breaker (cell ID ascending) makes the pick
		// reproducible across runs.
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].cell.ID < scored[j].cell.ID
	})
	return scored[0].cell, nil
}

// scoredCell pairs a candidate cell with its tier-specific score.
// Keeping the pair opaque forces callers to go through Pick.
type scoredCell struct {
	cell  Cell
	score float64
}

// scoreTier0 = "cheap drafting". Lower min_free_mib wins (means the
// cell can run on a smaller GPU footprint, which on the v14503 fleet
// correlates with smaller parameter count). Lower max_model_len is a
// secondary tie-breaker (cheap cell -> short context window).
func scoreTier0(c Cell) float64 {
	return float64(2_000_000-c.MinFreeMib) + float64(20_000-c.MaxModelLen)/100.0
}

// scoreTier1 = "heuristic planning". Higher max_model_len wins so
// the planner can hold the whole artifact in one context window.
// Engine is a small tie-breaker: vllm > llama.cpp > ollama (vllm
// gives continuous batching which matters once we stream tool
// calls).
func scoreTier1(c Cell) float64 {
	return float64(c.MaxModelLen) + engineBoost(c)
}

// scoreTier2 = "code synthesis". Same context preference as
// tier1 but with a stronger vllm boost (code iterates on long
// sequences; continuous batching matters more here).
func scoreTier2(c Cell) float64 {
	return float64(c.MaxModelLen) + 5000*engineBoost(c)
}

// scoreTier3 = "deep review / reasoning". Prefer cells that can do
// speculative decoding; when none are spec-capable, fall back to
// the largest max_model_len we have.
func scoreTier3(c Cell) float64 {
	specBoost := 0.0
	if c.SpecType != "" {
		specBoost = 1_000_000
	}
	return specBoost + float64(c.MaxModelLen)
}

// engineBoost returns a small additive score that nudges the tier1/2
// routers toward vllm for higher throughput. llama.cpp + ollama are
// equally weighted because llama.cpp is what the operator-side
// bench uses (v14503-03).
func engineBoost(c Cell) float64 {
	switch c.Engine {
	case "vllm":
		return 1
	case "llama.cpp", "ollama":
		return 0
	default:
		return 0
	}
}

// scoreTier0 is referenced by the score test file directly so it
// has to be exported across the package — Go already sees it because
// the test file is in the same package, but the linter wants us
// to be explicit. We pin the names so accidental renames break
// the test compile, mirroring the matrix_test.go pattern.
var _ = scoreTier0
var _ = scoreTier1
var _ = scoreTier2
var _ = scoreTier3
