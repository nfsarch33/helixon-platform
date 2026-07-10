package costobs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Sample data lives here so every test reuses the same well-formed
// fixture. We deliberately keep cost values in $0.0001 resolution
// so the NDJSON stays readable in a tail.
//nolint:unparam // model is parameterised so test callers can override for future multi-model fixtures; today they all use the same model.
func sampleEvent(model string, tier int, inTok, outTok int, cellID string) Event {
	return Event{
		SchemaVersion:   SchemaVersion,
		CapturedAt:      time.Date(2026, 7, 9, 1, 0, 0, 0, time.UTC),
		SprintID:        "v14511",
		JobID:           "job-001",
		TenantID:        "tenant-A",
		Env:             "dev",
		CellID:          cellID,
		Model:           model,
		ModelTier:       tier,
		EstInputTokens:  inTok,
		EstOutputTokens: outTok,
		EstCostUSD:      estimateCostUSD(model, inTok, outTok),
		JobType:         "llm.chat_completion",
		Outcome:         "ok",
	}
}

func TestEstimateCostUSD_Qwen36Q8Stable(t *testing.T) {
	// Snapshot test: $0.0000020 per input token, $0.0000030 per
	// output token for qwen36 Q8 (cheaper than MiniMax-M3 but
	// roughly half the cost of running on a vendor API).
	got := estimateCostUSD("qwen36-27b-mtp-q8", 1000, 500)
	want := 0.0020 + 0.0015
	assert.InDelta(t, want, got, 0.000001, "rate table must be stable")
}

func TestEstimateCostUSD_VendorCapPenalty(t *testing.T) {
	// For an unknown model the cost falls back to a conservative
	// rate (5x vendor ceiling); we log + raise an event-level
	// UnknownModel so the dashboard can surface this.
	got := estimateCostUSD("totally-unknown-model", 1000, 500)
	require.Greater(t, got, 0.01, "unknown model should be more expensive than qwen36")
}

func TestNDJSONRoundTrip_IsSingleLinePerCall(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	ev := sampleEvent("qwen36-27b-q4", 1, 1024, 512, "C2")
	require.NoError(t, w.Write(ev))
	// Two writes must produce exactly two non-empty lines.
	require.NoError(t, w.Write(ev))
	out := strings.TrimSpace(buf.String())
	lines := strings.Split(out, "\n")
	require.Len(t, lines, 2)
	for _, ln := range lines {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(ln), &m), "line is not valid JSON: %q", ln)
		assert.Equal(t, SchemaVersion, int(m["schema_version"].(float64)))
	}
}

func TestNDJSONAppend_ToFileIsAppendOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cost.ndjson")

	w, err := OpenFile(p)
	require.NoError(t, err)
	defer w.Close()

	require.NoError(t, w.Write(sampleEvent("qwen36-27b-q4", 0, 100, 50, "C1")))
	require.NoError(t, w.Write(sampleEvent("qwen36-27b-q4", 1, 200, 100, "C2")))
	require.NoError(t, w.Close())

	// Re-open in append mode (the canonical Cursor flow).
	w2, err := OpenFile(p)
	require.NoError(t, err)
	defer w2.Close()
	require.NoError(t, w2.Write(sampleEvent("qwen36-27b-q4", 2, 400, 200, "C1")))
	require.NoError(t, w2.Close())

	raw, err := os.ReadFile(p)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	assert.Len(t, lines, 3, "append must preserve prior rows")
}

func TestNDJSONConcurrentWrites_AllLinesWellFormed(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	const N = 200
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ev := sampleEvent("qwen36-27b-q4", i%4, i*10, i*5, "C2")
			_ = w.Write(ev)
		}(i)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	require.Len(t, lines, N, "each goroutine produced one line; got %d", len(lines))
	for _, ln := range lines {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(ln), &m))
	}
}

func TestNDJSONDefaultPath_HonoursXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HELIXON_COSTOBS_PATH", filepath.Join(dir, "cost.ndjson"))
	assert.Equal(t, filepath.Join(dir, "cost.ndjson"), DefaultPath())
}

func TestNDJSONOpenFile_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deeply", "nested", "cost.ndjson")
	w, err := OpenFile(p)
	require.NoError(t, err)
	defer w.Close()
	require.NoError(t, w.Write(sampleEvent("qwen36-27b-q4", 0, 10, 10, "C1")))
	_, err = os.Stat(p)
	assert.NoError(t, err)
}
