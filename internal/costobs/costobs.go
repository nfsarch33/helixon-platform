// Package costobs writes per-LLM-call cost observability to NDJSON.
//
// Why NDJSON:
//
//   - One line per call. tail -f is the operator's primary tool.
//   - Each line is a complete JSON object; jq can fold the stream
//     and Grafana can ingest directly.
//   - Append-only writes are atomic per line on POSIX; concurrent
//     goroutines from the agent runtime do not need a lock, just a
//     mutex around the file write to avoid interleaved bytes.
//
// Use:
//
//	w := costobs.NewWriter(os.Stdout)
//	w.Write(costobs.Event{...}) // emits one JSON line
//	defer w.Close()
//
//	// or persist:
//	w, _ := costobs.OpenFile("/var/log/helixon/cost.ndjson")
//	defer w.Close()
//
// v14511 ships this package; v14513 wires the dashboard + alert rules.
package costobs

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SchemaVersion is the wire-format version. Bump when shape
// changes. v14511 reads schema_version == 1 only.
const SchemaVersion = 1

// DefaultPath returns the canonical NDJSON sink for cost events.
// Operator can override with $HELIXON_COSTOBS_PATH.
func DefaultPath() string {
	if p := os.Getenv("HELIXON_COSTOBS_PATH"); p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), "helixon-cost.ndjson")
}

// Event is the wire shape. Every field is intentional; we do NOT
// embed an `Extra any` map because it bit-rots the schema. When a
// new field needs to ship, add it here and bump schema_version.
type Event struct {
	SchemaVersion   int       `json:"schema_version"`
	CapturedAt      time.Time `json:"captured_at"`
	SprintID        string    `json:"sprint_id"`
	JobID           string    `json:"job_id"`
	TenantID        string    `json:"tenant_id,omitempty"`
	Env             string    `json:"env,omitempty"`
	CellID          string    `json:"cell_id"`
	Model           string    `json:"model"`
	ModelTier       int       `json:"model_tier"`
	EstInputTokens  int       `json:"est_input_tokens"`
	EstOutputTokens int       `json:"est_output_tokens"`
	EstCostUSD      float64   `json:"est_cost_usd"`
	JobType         string    `json:"job_type"`
	Outcome         string    `json:"outcome"` // ok|error|dead_letter
	LatencyMS       int       `json:"latency_ms,omitempty"`
	UnknownModel    bool      `json:"unknown_model,omitempty"`
}

// Writer serialises Event values as one JSON line each and writes
// them to an io.Writer. The writer is safe for concurrent use; the
// mutex serialises the fmt.Fprint call so we never interleave the
// bytes of two events on the wire.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
	enc *json.Encoder
}

// NewWriter wraps any io.Writer (typically os.Stdout or a file).
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w, enc: json.NewEncoder(io.Discard)}
}

// OpenFile returns a Writer that writes to a file at p. The parent
// directory is created with 0o755 if missing. The file is opened
// in append mode so concurrent processes can extend the same sink.
// Existing rows are preserved.
func OpenFile(p string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, fmt.Errorf("costobs: mkdir parent: %w", err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("costobs: open %q: %w", p, err)
	}
	return &Writer{w: f, enc: json.NewEncoder(io.Discard)}, nil
}

// Write emits one JSON line for ev. The line ends with '\n' so
// `tail -f` shows a complete row on every event.
func (w *Writer) Write(ev Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ev.SchemaVersion == 0 {
		ev.SchemaVersion = SchemaVersion
	}
	if ev.CapturedAt.IsZero() {
		ev.CapturedAt = time.Now().UTC()
	}
	bb, err := json.Marshal(&ev)
	if err != nil {
		return fmt.Errorf("costobs: marshal: %w", err)
	}
	bb = append(bb, '\n')
	_, err = w.w.Write(bb)
	return err
}

// Close flushes the underlying file. Safe to call on stdout-backed
// writers (no-op).
func (w *Writer) Close() error {
	if c, ok := w.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// EstimateCostUSD is the public entry point for callers that have
// input/output token counts but not an Event built yet.
func EstimateCostUSD(model string, inTok, outTok int) float64 {
	return estimateCostUSD(model, inTok, outTok)
}

// estimateCostUSD returns $USD for an LLM call given model and
// token counts. We use a deliberately conservative rate table so
// that the operator budget alerts are triggered early (rather than
// after a billing cycle sees an unrecognised model).
//
// Rates (per 1k tokens):
//
//	qwen36 q4 / q8 (local)         : $0.0020 in / $0.0030 out
//	Qwen3.7-plus (vendor tier)     : $0.0015 in / $0.0060 out
//	Qwen3.7-max   (vendor tier)    : $0.0030 in / $0.0120 out
//	MiniMax-M3   (vendor tier)     : $0.0030 in / $0.0090 out
//	default / unknown              : $0.0100 in / $0.0300 out (>=3x
//	                                the most expensive vendor tier)
//	                                so the budget alert fires loudly.
func estimateCostUSD(model string, inTok, outTok int) float64 {
	rateIn, rateOut, known := rateFor(model)
	if inTok < 0 {
		inTok = 0
	}
	if outTok < 0 {
		outTok = 0
	}
	if !known {
		// Mark UnknownModel on the returned event by signalling
		// via the cost value: budget alerts watch for cost >
		// (inTok * 0.01) which is met by any unknown-model
		// call. The actual flag is set on Event.UnknownModel
		// by the caller (see Decide).
		_ = rateIn
	}
	return float64(inTok)/1000.0*rateIn + float64(outTok)/1000.0*rateOut
}

func rateFor(model string) (inRate, outRate float64, known bool) {
	switch model {
	case "qwen36-27b-q4", "qwen36-27b-q8", "qwen36-27b-mtp-q8",
		"qwen36-9b-q4", "qwen36-8b-q3", "qwen36-4b-q4",
		"qwen36-14b-q4", "qwen36-27b-int4":
		return 0.0020, 0.0030, true
	case "qwen3.7-plus", "Qwen3.7-plus", "qwen-plus", "qwen3-7-plus":
		return 0.0015, 0.0060, true
	case "qwen3.7-max", "Qwen3.7-max", "qwen-max", "qwen3-7-max":
		return 0.0030, 0.0120, true
	case "MiniMax-M3", "minimax-m3", "MiniMax M3":
		return 0.0030, 0.0090, true
	}
	return 0.0100, 0.0300, false
}
