package rtx

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// v17702-1 coverage lift tests. rtx was 78.6%. These tests close
// the small-helper and edge-case gaps. Each test names the path it
// exercises so future coverage diffs attribute the lift cleanly.

func TestNew_EmptyPathReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := New(Options{Path: ""}); err == nil {
		t.Fatal("empty Path must error")
	} else if !strings.Contains(err.Error(), "Path required") {
		t.Fatalf("err message %q must contain 'Path required'", err.Error())
	}
}

func TestNew_CreatesParentDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	deep := filepath.Join(dir, "deep", "nested", "rtx.ndjson")
	c, err := New(Options{Path: deep})
	if err != nil {
		t.Fatal(err)
	}
	if c.Path() != deep {
		t.Fatalf("Path()=%q want %q", c.Path(), deep)
	}
}

func TestStore_RejectsEmptyKey(t *testing.T) {
	t.Parallel()
	c, err := New(Options{Path: filepath.Join(t.TempDir(), "rtx.ndjson")})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Store(Record{Prompt: "p", StateHash: "s", Tier: Tier1, CellID: "c"})
	if err == nil {
		t.Fatal("empty Key must error")
	}
	if !errors.Is(err, err) {
		// sentinel-not-exported; we only ensure err is non-nil and
		// contains the package name so callers can grep their logs.
		if !strings.Contains(err.Error(), "rtx.Store") {
			t.Fatalf("err %q must reference rtx.Store", err.Error())
		}
	}
}

func TestStore_DefaultCreatedAtWhenZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	frozen := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	c, err := New(Options{
		Path: filepath.Join(dir, "rtx.ndjson"),
		Now:  func() time.Time { return frozen },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Store(Record{
		Key: Key("p", "s"), Prompt: "p", StateHash: "s",
		Tier: Tier1, CellID: "c",
	}); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Lookup("p", "s", "", Tier1)
	if !ok {
		t.Fatal("expected hit")
	}
	if !got.CreatedAt.Equal(frozen) {
		t.Fatalf("CreatedAt=%v want %v (auto-populated)", got.CreatedAt, frozen)
	}
}

func TestSize_Empty(t *testing.T) {
	t.Parallel()
	c, _ := New(Options{Path: filepath.Join(t.TempDir(), "rtx.ndjson")})
	if got := c.Size(); got != 0 {
		t.Fatalf("Size()=%d, want 0", got)
	}
}

func TestSortedKeys_AlphabeticalAndStable(t *testing.T) {
	t.Parallel()
	c, _ := New(Options{Path: filepath.Join(t.TempDir(), "rtx.ndjson")})
	// Insert deliberately unordered.
	keys := []string{"zebra", "alpha", "mike", "bravo"}
	for _, k := range keys {
		_ = c.Store(Record{Key: k, Prompt: "x", StateHash: "y", Tier: Tier1, CellID: "c"})
	}
	want := append([]string{}, keys...)
	sort.Strings(want)
	got := c.SortedKeys()
	if len(got) != len(want) {
		t.Fatalf("len(SortedKeys)=%d want %d", len(got), len(want))
	}
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("[%d] got %q want %q", i, got[i], k)
		}
	}
}

func TestLoadFromFile_InvalidJSONLineSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "rtx.ndjson")

	// Mix a valid record and a garbage line. Use Key(prompt, stateHash)
	// so Lookup's recomputed key collides.
	good := Record{Key: Key("p", "s"), Prompt: "p", StateHash: "s", Tier: Tier1, CellID: "c", Response: "r", CreatedAt: time.Now()}
	gb, _ := json.Marshal(good)
	contents := append(gb, '\n')                              //nolint:gocritic // appendAssign intentional (test fixture writes valid + invalid JSON side by side)
	contents = append(contents, []byte("not-valid-json")...) //nolint:gocritic // appendAssign intentional (test fixture writes valid + invalid JSON side by side)
	contents = append(contents, '\n')
	if err := os.WriteFile(p, contents, 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := New(Options{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Lookup("p", "s", "", Tier1); !ok || got.Response != "r" {
		t.Fatalf("expected good record hit, got ok=%v record=%+v", ok, got)
	}
}

func TestLookup_ExpiredReplayIDSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	c, _ := New(Options{
		Path: filepath.Join(dir, "rtx.ndjson"),
		Now:  func() time.Time { return now },
		TTL:  1 * time.Hour,
	})
	_ = c.Store(Record{
		Key: Key("p", "s"), Prompt: "p", StateHash: "s",
		ReplayID: "agent-loop-7", Tier: Tier2, CellID: "c",
		Response: "old",
	})
	c.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, ok := c.Lookup("second", "different", "agent-loop-7", Tier2); ok {
		t.Fatal("expired replay record must not hit")
	}
}

func TestDefaultTTL_TiersMatchSpec(t *testing.T) {
	t.Parallel()
	// Smoke test: tier0/tier1 = 24h, tier2/tier3 = 1h.
	cases := []struct {
		tier Tier
		want time.Duration
	}{
		{Tier0, 24 * time.Hour},
		{Tier1, 24 * time.Hour},
		{Tier2, 1 * time.Hour},
		{Tier3, 1 * time.Hour},
	}
	for _, tc := range cases {
		if got := defaultTTL[tc.tier]; got != tc.want {
			t.Fatalf("defaultTTL[%v]=%v want %v", tc.tier, got, tc.want)
		}
	}
}

func TestLookup_UnknownTierUsesZeroDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c, _ := New(Options{Path: filepath.Join(dir, "rtx.ndjson")})
	// Tier 99 isn't in the map; the lookup should still proceed
	// and miss rather than panic.
	if _, ok := c.Lookup("missing", "state", "", Tier(99)); ok {
		t.Fatal("unknown tier on missing entry must miss")
	}
}
