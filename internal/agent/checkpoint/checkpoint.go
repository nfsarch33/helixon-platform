// Package checkpoint emits periodic Agentrace events during long agent
// runs. v17007 MVP-6.
//
// Triggers:
//   - every N tool calls (default 10)
//   - every T minutes (default 30)
//   - on demand via Force()
//
// Event schema:
//
//	{
//	  "ts": "ISO8601",
//	  "event": "agentrace_checkpoint",
//	  "elapsed_min": 23,
//	  "files_written": 4,
//	  "tests_passing": 12,
//	  "tests_failing": 0,
//	  "budget_remaining_pct": 65,
//	  "carry_signal": null | "partial_save" | "blocked" | "rate_limited"
//	}
package checkpoint

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// CarrySignal is the optional carry state emitted with each checkpoint.
type CarrySignal string

const (
	SignalNone        CarrySignal = ""
	SignalPartialSave CarrySignal = "partial_save"
	SignalBlocked     CarrySignal = "blocked"
	SignalRateLimited CarrySignal = "rate_limited"
)

// Config controls checkpoint cadence.
type Config struct {
	EveryNToolCalls int           // default 10
	EveryTMinutes   time.Duration // default 30 min
	OutputPath      string        // NDJSON file; default ~/logs/runx/agentrace-checkpoint.ndjson
}

// Checkpoint is the agentrace event.
type Checkpoint struct {
	Timestamp          time.Time   `json:"ts"`
	Event              string      `json:"event"` // always "agentrace_checkpoint"
	ElapsedMin         int         `json:"elapsed_min"`
	FilesWritten       int         `json:"files_written"`
	TestsPassing       int         `json:"tests_passing"`
	TestsFailing       int         `json:"tests_failing"`
	BudgetRemainingPct int         `json:"budget_remaining_pct"`
	CarrySignal        CarrySignal `json:"carry_signal"`
}

// Emitter is the public API.
type Emitter struct {
	mu          sync.Mutex
	cfg         Config
	startedAt   time.Time
	lastEmitted time.Time
	toolCalls   int
	files       int
	testsPass   int
	testsFail   int
	budgetPct   int
	signal      CarrySignal
}

// New returns an Emitter with default config if cfg is zero.
func New(cfg Config) *Emitter {
	if cfg.EveryNToolCalls <= 0 {
		cfg.EveryNToolCalls = 10
	}
	if cfg.EveryTMinutes <= 0 {
		cfg.EveryTMinutes = 30 * time.Minute
	}
	if cfg.OutputPath == "" {
		cfg.OutputPath = os.ExpandEnv("$HOME/logs/runx/agentrace-checkpoint.ndjson")
	}
	now := time.Now()
	return &Emitter{cfg: cfg, startedAt: now, lastEmitted: now, budgetPct: 100}
}

// OnToolCall increments the tool-call counter; emits a checkpoint if
// threshold reached.
func (e *Emitter) OnToolCall() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.toolCalls++
	if e.toolCalls%e.cfg.EveryNToolCalls == 0 {
		return e.emitLocked()
	}
	return nil
}

// Tick checks the time-based threshold; emits if due.
func (e *Emitter) Tick() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Since(e.lastEmitted) >= e.cfg.EveryTMinutes {
		return e.emitLocked()
	}
	return nil
}

// Force emits a checkpoint immediately.
func (e *Emitter) Force() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.emitLocked()
}

// SetCounts updates the running counters (files written, tests passing/failing).
func (e *Emitter) SetCounts(files, testsPass, testsFail int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.files = files
	e.testsPass = testsPass
	e.testsFail = testsFail
}

// SetBudget updates the budget remaining percent.
func (e *Emitter) SetBudget(pct int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.budgetPct = pct
}

// SetSignal updates the carry signal for the next checkpoint.
func (e *Emitter) SetSignal(s CarrySignal) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.signal = s
}

func (e *Emitter) emitLocked() error {
	cp := Checkpoint{
		Timestamp:          time.Now().UTC(),
		Event:              "agentrace_checkpoint",
		ElapsedMin:         int(time.Since(e.startedAt).Minutes()),
		FilesWritten:       e.files,
		TestsPassing:       e.testsPass,
		TestsFailing:       e.testsFail,
		BudgetRemainingPct: e.budgetPct,
		CarrySignal:        e.signal,
	}
	line, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := os.OpenFile(e.cfg.OutputPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644) //nolint:gosec // G302 file perms 0750 acceptable for non-secret runtime files
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}
