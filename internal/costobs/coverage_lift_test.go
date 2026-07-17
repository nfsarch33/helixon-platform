package costobs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// v17702-1 coverage lift tests. costobs was 67.5%; these tests
// close the small-helper and edge-case gaps that the original
// suite skipped. Each test is named after the function path it
// exercises so future coverage diffs are easy to attribute.

func TestDefaultPath_FallsBackToTempWhenUnset(t *testing.T) {
	t.Setenv("HELIXON_COSTOBS_PATH", "")
	got := DefaultPath()
	// On Linux we expect /tmp; on macOS we expect /private/tmp.
	// Either way it must be under os.TempDir().
	tmp := filepath.ToSlash(os.TempDir())
	if !strings.HasPrefix(got, tmp) {
		t.Fatalf("DefaultPath()=%q must start with TempDir %q", got, tmp)
	}
	if !strings.HasSuffix(got, "helixon-cost.ndjson") {
		t.Fatalf("DefaultPath()=%q must end with helixon-cost.ndjson", got)
	}
}

func TestEstimateCostUSD_NegativeTokensClamped(t *testing.T) {
	t.Parallel()
	got := EstimateCostUSD("qwen36-27b-q4", -1, -1)
	if got != 0 {
		t.Fatalf("negative tokens must clamp to 0; got %v", got)
	}
}

func TestEstimateCostUSD_AllKnownModels(t *testing.T) {
	t.Parallel()
	models := []string{
		"qwen36-27b-q4", "qwen36-27b-q8", "qwen36-27b-mtp-q8",
		"qwen36-9b-q4", "qwen36-8b-q3", "qwen36-4b-q4",
		"qwen36-14b-q4", "qwen36-27b-int4",
		"qwen3.7-plus", "Qwen3.7-plus", "qwen-plus", "qwen3-7-plus",
		"qwen3.7-max", "Qwen3.7-max", "qwen-max", "qwen3-7-max",
		"MiniMax-M3", "minimax-m3", "MiniMax M3",
	}
	for _, m := range models {
		got := EstimateCostUSD(m, 1000, 1000)
		// All known models must be cheaper than the unknown-model
		// penalty ($0.04 per (in+out) at 1k each).
		if got >= 0.04 {
			t.Fatalf("model %q has suspiciously high cost %v", m, got)
		}
		if got <= 0 {
			t.Fatalf("model %q must have positive cost, got %v", m, got)
		}
	}
}

func TestRateFor_UnknownModelPenalty(t *testing.T) {
	t.Parallel()
	known := []string{"made-up-1", "totally-bogus", ""}
	for _, m := range known {
		_, _, ok := rateFor(m)
		if ok {
			t.Fatalf("model %q must be classified unknown", m)
		}
	}
}

func TestWrite_DefaultsSchemaVersionAndCapturedAt(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	ev := Event{SprintID: "v17702", JobID: "x", CellID: "C1", Model: "qwen36-27b-q4", Outcome: "ok"}
	// SchemaVersion=0 and CapturedAt=zero trigger the default branches.
	require.NoError(t, w.Write(ev))
	var m map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &m))
	if int(m["schema_version"].(float64)) != SchemaVersion {
		t.Fatalf("default schema_version must be %d, got %v", SchemaVersion, m["schema_version"])
	}
	if _, ok := m["captured_at"]; !ok {
		t.Fatal("captured_at must be auto-populated when zero")
	}
}

func TestWrite_PopulatedSchemaVersionHonored(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	ev := Event{SchemaVersion: 99, SprintID: "v17702", JobID: "x", CellID: "C1", Model: "qwen36-27b-q4", Outcome: "ok"}
	require.NoError(t, w.Write(ev))
	var m map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &m))
	if int(m["schema_version"].(float64)) != 99 {
		t.Fatalf("explicit schema_version must be preserved; got %v", m["schema_version"])
	}
}

func TestClose_NoCloserIsNoop(t *testing.T) {
	t.Parallel()
	w := NewWriter(&bytes.Buffer{})
	if err := w.Close(); err != nil {
		t.Fatalf("Close on non-closer must be no-op; got %v", err)
	}
}

func TestOpenFile_MkdirParentOnDeepPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Three levels of nesting, none of which exist a priori.
	deep := filepath.Join(dir, "a", "b", "c", "sink.ndjson")
	w, err := OpenFile(deep)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()
	require.NoError(t, w.Write(Event{SchemaVersion: SchemaVersion, SprintID: "v17702", CellID: "C1", Model: "qwen36-27b-q4", Outcome: "ok"}))
	st, err := os.Stat(deep)
	require.NoError(t, err)
	assert.Greater(t, st.Size(), int64(0))
}
