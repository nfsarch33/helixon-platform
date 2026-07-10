// Package helixoneval is the v16129 Sprint 18 HelixonEval R3 binary.
//
// It benchmarks Helixon platform/fleet agent task completion across the
// three judge model back-ends (qwen3.7-plus, qwen3.7-max, MiniMax M3) and
// applies the G-Eval rubrics from helixon-autoresearch/eval/rubrics.go to
// the resulting traces.
//
// Sprint 18 ships STAGING EVAL ONLY — Aliyun quota is exhausted, so the
// runner consumes cached or synthesised offline traces (no live API
// calls). The next sprint will plumb the live judge path back in once
// quota is restored.
package helixoneval

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Model identifies the judge model a Case was scored on. The four
// supported identifiers match the four keys the Sprint 18 brief lists
// verbatim: qwen3.7-plus, qwen3.7-max, MiniMax-M3 (cached/offline
// only) and offline-fixture (synthesised).
type Model string

const (
	ModelQwen37Plus Model = "qwen3.7-plus"
	ModelQwen37Max  Model = "qwen3.7-max"
	ModelMiniMaxM3  Model = "MiniMax-M3"
	ModelOfflineFix Model = "offline-fixture"
)

// Case is a single scored trace. Score is in [0,1] (mean of all applied
// rubrics for the model). TerminationReason is one of "completed",
// "self_improve_term", "max_steps", "error".
type Case struct {
	ID                string             `json:"id"`
	Task              string             `json:"task"`
	Model             Model              `json:"model"`
	Score             float64            `json:"score"`
	RubricScores      map[string]float64 `json:"rubric_scores"`
	Steps             int                `json:"steps"`
	TerminationReason string             `json:"termination_reason"`
	StartedAt         time.Time          `json:"started_at"`
	DurationMS        int64              `json:"duration_ms"`
	Source            string             `json:"source"` // "cache", "synth"
}

// Registry holds Cases keyed by ID. It is safe for concurrent use by
// multiple goroutines; the brief's spec calls for a 100-goroutine
// concurrent register test (see v16122 regression template).
type Registry struct {
	mu    sync.RWMutex
	cases map[string]Case
	order []string // insertion order, used for deterministic Run output
}

// NewRegistry returns an empty Registry ready for Add/Run calls.
func NewRegistry() *Registry {
	return &Registry{cases: make(map[string]Case)}
}

// ErrDuplicateCase is returned by Add when a Case with the same ID is
// already registered. The Runner (Run) ignores this so re-running the
// same task overwrites the prior score — the canonical pattern from
// helixon-autoresearch/eval/harness.go.
var ErrDuplicateCase = errors.New("duplicate case id")

// Add inserts a Case. Returns ErrDuplicateCase if ID is already
// present; the caller may decide to ignore and overwrite (see Runner).
func (r *Registry) Add(c Case) error {
	if c.ID == "" {
		return fmt.Errorf("helixoneval: case ID is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.cases[c.ID]; dup {
		return ErrDuplicateCase
	}
	r.cases[c.ID] = c
	r.order = append(r.order, c.ID)
	return nil
}

// Upsert inserts or overwrites by ID. Used by Runner when re-running a
// task across multiple models so the same Case.ID can be scored on
// qwen3.7-plus, qwen3.7-max, and MiniMax-M3 with different Model
// fields. Same ID with different model = distinct case; the Registry
// stores per-model keys.
func (r *Registry) Upsert(c Case) error {
	if c.ID == "" {
		return fmt.Errorf("helixoneval: case ID is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.cases[c.ID]; !exists {
		r.order = append(r.order, c.ID)
	}
	r.cases[c.ID] = c
	return nil
}

// Get returns the Case with the given ID, or false if not present.
func (r *Registry) Get(id string) (Case, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.cases[id]
	return c, ok
}

// Len returns the number of registered Cases.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.cases)
}

// IDs returns the registered Case IDs in insertion order. Used by
// `helixon-eval list-tasks` and the regression test.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Runner executes a task across a set of models, producing one Case per
// model. It is the entry point used by the `run` subcommand.
type Runner struct {
	registry *Registry
	source   TraceSource
}

// TraceSource abstracts where the trace data comes from. The shipped
// implementations are CacheSource (reads pre-recorded JSON lines from
// helixon-evolver), SynthSource (deterministic offline generator) and
// the no-op nil-safe NullSource used when --model=offline-fixture is
// requested but the fixture file is missing.
type TraceSource interface {
	Fetch(taskID string, model Model) (Trace, bool)
}

// Trace is the raw material a Runner turns into a Case. Steps captures
// each agent turn (kept as a count here; the full step log lives in
// helixon-autoresearch/eval/tasks.go).
type Trace struct {
	TaskID            string
	Model             Model
	Steps             int
	RubricScores      map[string]float64
	TerminationReason string
	StartedAt         time.Time
	DurationMS        int64
}

// Score returns the mean of the trace's per-rubric scores (0 if no
// rubric scores). Used by tests and any external reader that wants
// the same arithmetic as the Runner.
func (t Trace) Score() float64 { return mean(t.RubricScores) }

// NewRunner wires a Registry to a TraceSource.
func NewRunner(reg *Registry, src TraceSource) *Runner {
	if src == nil {
		src = NullSource{}
	}
	return &Runner{registry: reg, source: src}
}

// Run executes taskID on each model and stores the resulting Cases.
// Models with no cached trace return an error and are skipped; the
// surviving models are recorded. Always returns the slice of stored
// IDs for the report writer.
func (r *Runner) Run(taskID string, models []Model) ([]string, error) {
	if taskID == "" {
		return nil, fmt.Errorf("helixoneval: taskID is empty")
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("helixoneval: no models supplied")
	}
	stored := make([]string, 0, len(models))
	for _, m := range models {
		trace, ok := r.source.Fetch(taskID, m)
		if !ok {
			continue
		}
		caseID := fmt.Sprintf("%s::%s", taskID, m)
		c := Case{
			ID:                caseID,
			Task:              taskID,
			Model:             m,
			Score:             mean(trace.RubricScores),
			RubricScores:      trace.RubricScores,
			Steps:             trace.Steps,
			TerminationReason: trace.TerminationReason,
			StartedAt:         trace.StartedAt,
			DurationMS:        trace.DurationMS,
			Source:            sourceLabel(r.source),
		}
		if err := r.registry.Upsert(c); err != nil {
			return stored, err
		}
		stored = append(stored, caseID)
	}
	if len(stored) == 0 {
		return stored, fmt.Errorf("helixoneval: no traces for task %q", taskID)
	}
	return stored, nil
}

// RunAll executes every task known to the supplied TaskCatalog on every
// model. Used by the `run --all` subcommand and the regression test.
func (r *Runner) RunAll(models []Model, catalog TaskCatalog) (int, error) {
	count := 0
	for _, taskID := range catalog.Tasks() {
		ids, err := r.Run(taskID, models)
		if err != nil {
			return count, err
		}
		count += len(ids)
	}
	return count, nil
}

// mean returns the arithmetic mean of the map values, 0 if empty.
func mean(m map[string]float64) float64 {
	if len(m) == 0 {
		return 0
	}
	var sum float64
	for _, v := range m {
		sum += v
	}
	return sum / float64(len(m))
}

// sourceLabel inspects the TraceSource for human-readable provenance
// in the resulting Case.Source field.
func sourceLabel(s TraceSource) string {
	switch s.(type) {
	case CacheSource:
		return "cache"
	case SynthSource:
		return "synth"
	case NullSource:
		return "none"
	default:
		return "custom"
	}
}

// TaskCatalog lists the tasks the Runner should execute when RunAll is
// invoked. The Sprint 18 golden set is the canonical five (see
// task_golden.go for the production catalog).
type TaskCatalog interface {
	Tasks() []string
}

// StaticCatalog is a deterministic TaskCatalog backed by a slice.
type StaticCatalog struct {
	TasksList []string
}

// Tasks returns the catalog's task IDs in sorted order so the report
// output is reproducible across runs.
func (s StaticCatalog) Tasks() []string {
	out := append([]string(nil), s.TasksList...)
	sort.Strings(out)
	return out
}

// NullSource returns no traces; useful as a no-op default.
type NullSource struct{}

// Fetch always returns false for NullSource.
func (NullSource) Fetch(string, Model) (Trace, bool) { return Trace{}, false }
