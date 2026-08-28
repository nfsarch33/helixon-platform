package sandbox

import (
	"strings"
	"sync"
	"testing"
)

func TestBoundedBuffer_TableDriven(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		limit         int
		writes        []string
		wantRetained  string
		wantTruncated bool
		wantTotal     int
	}{
		{name: "under the cap", limit: 16, writes: []string{"abc", "def"}, wantRetained: "abcdef", wantTotal: 6},
		{name: "exactly at the cap", limit: 6, writes: []string{"abc", "def"}, wantRetained: "abcdef", wantTotal: 6},
		{name: "single write over the cap", limit: 4, writes: []string{"abcdefgh"}, wantRetained: "abcd", wantTruncated: true, wantTotal: 8},
		{name: "second write crosses the cap", limit: 4, writes: []string{"ab", "cdef"}, wantRetained: "abcd", wantTruncated: true, wantTotal: 6},
		{name: "writes after the cap are dropped", limit: 2, writes: []string{"ab", "cd", "ef"}, wantRetained: "ab", wantTruncated: true, wantTotal: 6},
		{name: "empty write after the cap does not set truncated", limit: 2, writes: []string{"ab", ""}, wantRetained: "ab", wantTotal: 2},
		{name: "non-positive max falls back to the default", limit: 0, writes: []string{"abc"}, wantRetained: "abc", wantTotal: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := NewBoundedBuffer(tt.limit)
			for _, w := range tt.writes {
				n, err := b.Write([]byte(w))
				// The contract: ALWAYS report the full write. A short
				// write or an error here reaches os/exec, which tears the
				// pipe down and EPIPEs the child.
				if err != nil {
					t.Fatalf("Write(%q) returned err %v; the buffer must never fail a write", w, err)
				}
				if n != len(w) {
					t.Fatalf("Write(%q) reported %d bytes; must always claim the full %d", w, n, len(w))
				}
			}
			if got := b.String(); got != tt.wantRetained {
				t.Errorf("retained = %q, want %q", got, tt.wantRetained)
			}
			if got := b.Truncated(); got != tt.wantTruncated {
				t.Errorf("Truncated() = %v, want %v", got, tt.wantTruncated)
			}
			if got := b.Total(); got != tt.wantTotal {
				t.Errorf("Total() = %d, want %d", got, tt.wantTotal)
			}
		})
	}
}

// TestBoundedBuffer_NeverGrowsPastMax is the memory-safety assertion: the
// unbounded-output P0 in this estate was a buffer that kept every byte a
// child produced.
func TestBoundedBuffer_NeverGrowsPastMax(t *testing.T) {
	t.Parallel()
	const limit = 1024
	b := NewBoundedBuffer(limit)
	chunk := []byte(strings.Repeat("x", 4096))
	for i := 0; i < 500; i++ {
		if _, err := b.Write(chunk); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if got := len(b.String()); got != limit {
		t.Fatalf("retained %d bytes; the cap is %d", got, limit)
	}
	if b.Total() != 500*4096 {
		t.Fatalf("Total() = %d, want %d", b.Total(), 500*4096)
	}
	if !b.Truncated() {
		t.Fatal("Truncated() must be true after discarding ~2MiB")
	}
}

// TestBoundedBuffer_ConcurrentWrites: os/exec hands the same writer to both
// stdout and stderr, so it is written from two goroutines.
func TestBoundedBuffer_ConcurrentWrites(t *testing.T) {
	t.Parallel()
	b := NewBoundedBuffer(64)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = b.Write([]byte("abcdefgh"))
			}
		}()
	}
	wg.Wait()
	if got := len(b.String()); got != 64 {
		t.Fatalf("retained %d bytes, want the 64-byte cap", got)
	}
	if b.Total() != 8*100*8 {
		t.Fatalf("Total() = %d, want %d", b.Total(), 8*100*8)
	}
}
