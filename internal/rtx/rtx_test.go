package rtx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKey_DeterministicAndUnique(t *testing.T) {
	a := Key("hello", "state1")
	b := Key("hello", "state1")
	c := Key("hello", "state2")
	d := Key("hello world", "state1")
	if a != b {
		t.Fatalf("same input → different key: %s vs %s", a, b)
	}
	if a == c {
		t.Fatalf("state change must change key")
	}
	if a == d {
		t.Fatalf("prompt change must change key")
	}
	if !strings.HasPrefix(a, "fnv64a:") {
		t.Fatalf("key must be namespaced: %s", a)
	}
}

func TestCache_StoreAndLookup(t *testing.T) {
	dir := t.TempDir()
	c, err := New(Options{Path: filepath.Join(dir, "rtx.ndjson")})
	if err != nil {
		t.Fatal(err)
	}
	rec := Record{
		Key:       Key("p", "s"),
		Prompt:    "p",
		StateHash: "s",
		Tier:      Tier2,
		CellID:    "qwen36-27b-q8",
		Response:  "hello back",
		CostUSD:   0.01,
	}
	if err := c.Store(rec); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Lookup("p", "s", "", Tier2)
	if !ok {
		t.Fatal("expected hit")
	}
	if got.Response != "hello back" || got.CellID != "qwen36-27b-q8" {
		t.Fatalf("got %+v", got)
	}
}

func TestCache_ReplayIDBypassesPrompt(t *testing.T) {
	dir := t.TempDir()
	c, _ := New(Options{Path: filepath.Join(dir, "rtx.ndjson")})
	if err := c.Store(Record{
		Key:       Key("first prompt", "state-A"),
		Prompt:    "first prompt",
		StateHash: "state-A",
		ReplayID:  "agent-loop-7",
		Tier:      Tier1,
		CellID:    "local-4b",
		Response:  "answer-1",
	}); err != nil {
		t.Fatal(err)
	}
	// Second agent iteration with the same replay_id but a fresh prompt.
	got, ok := c.Lookup("second prompt", "state-B", "agent-loop-7", Tier1)
	if !ok {
		t.Fatal("replay_id should bypass prompt/state")
	}
	if got.Response != "answer-1" {
		t.Fatalf("replay_id returned wrong record: %+v", got)
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	dir := t.TempDir()
	frozen := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	c, _ := New(Options{
		Path: filepath.Join(dir, "rtx.ndjson"),
		Now:  func() time.Time { return frozen },
		TTL:  1 * time.Hour, // overrides tier default
	})
	_ = c.Store(Record{
		Key: Key("p", "s"), Prompt: "p", StateHash: "s",
		Tier: Tier2, CellID: "c", Response: "r",
	})
	// 30 min later: hit
	c.now = func() time.Time { return frozen.Add(30 * time.Minute) }
	if _, ok := c.Lookup("p", "s", "", Tier2); !ok {
		t.Fatal("30-min-old entry should still be a hit")
	}
	// 2 h later: miss
	c.now = func() time.Time { return frozen.Add(2 * time.Hour) }
	if _, ok := c.Lookup("p", "s", "", Tier2); ok {
		t.Fatal("2-h-old entry should be expired")
	}
}

func TestCache_ConcurrentStore(t *testing.T) {
	dir := t.TempDir()
	c, _ := New(Options{Path: filepath.Join(dir, "rtx.ndjson")})
	done := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			done <- c.Store(Record{
				Key:       Key("prompt", "state") + string(rune('a'+i)),
				Prompt:    "prompt",
				StateHash: "state",
				Tier:      Tier1,
				CellID:    "cell",
				Response:  "ok",
			})
		}(i)
	}
	for i := 0; i < 20; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent store: %v", err)
		}
	}
	if c.Size() < 20 {
		t.Fatalf("expected >=20 records, got %d", c.Size())
	}
	// NDJSON file should be valid JSONL.
	data, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range splitLines(string(data)) {
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad NDJSON line %q: %v", line, err)
		}
	}
}

func TestCache_LoadFromExistingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rtx.ndjson")
	// Pre-seed the file.
	rec := Record{
		Key: Key("x", "y"), Prompt: "x", StateHash: "y",
		Tier: Tier0, CellID: "echo", Response: "pong",
		CreatedAt: time.Now(),
	}
	b, _ := json.Marshal(rec)
	_ = os.WriteFile(p, append(b, '\n'), 0o644)

	c, err := New(Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := c.Lookup("x", "y", "", Tier0)
	if !ok {
		t.Fatal("expected hit after load")
	}
	if got.Response != "pong" {
		t.Fatalf("got %+v", got)
	}
}

// helpers
func splitLines(s string) []string {
	var out []string
	start := 0
	for i, r := range s {
		if r == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
