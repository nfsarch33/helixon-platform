package headroom

import (
	"errors"
	"strings"
	"testing"
)

func TestCheck_PassesForTinyPrompt(t *testing.T) {
	if err := Check("qwen36-27b-q8", 100); err != nil {
		t.Fatalf("tiny prompt should fit: %v", err)
	}
}

func TestCheck_RejectsOversizedPrompt(t *testing.T) {
	err := Check("qwen36-27b-q8", 100_000)
	if err == nil {
		t.Fatal("100k tokens on 32k cell should reject")
	}
	var h *HeadroomError
	if !errors.As(err, &h) {
		t.Fatalf("expected *HeadroomError, got %T", err)
	}
	if !strings.Contains(h.Error(), "shrink prompt") {
		t.Fatalf("error should hint at action: %s", h.Error())
	}
}

func TestCheck_ReservesResponsePerTier(t *testing.T) {
	// Local-echo: 8k context, 256 reserved response, 64 system → 7680 available.
	if err := Check("local-echo", 7_680); err != nil {
		t.Fatalf("7680 should fit local-echo: %v", err)
	}
	if err := Check("local-echo", 7_681); err == nil {
		t.Fatal("7681 should reject local-echo")
	}
}

func TestCheck_UnknownCellFailsSafe(t *testing.T) {
	if err := Check("brand-new-cell-2027", 100); err != nil {
		t.Fatalf("100 tokens on unknown cell (32k default) should fit: %v", err)
	}
	if err := Check("brand-new-cell-2027", 50_000); err == nil {
		t.Fatal("50k tokens on unknown cell should reject")
	}
}

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 1}, // 5/4 = 1
		{strings.Repeat("x", 400), 100},
		{strings.Repeat("y", 401), 100},
		{strings.Repeat("z", 800), 200},
	}
	for _, c := range cases {
		got := EstimateTokens(c.in)
		if got != c.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestForCell_EndToEnd(t *testing.T) {
	short := "hello"
	if err := ForCell("qwen36-27b-q8", short); err != nil {
		t.Fatalf("short prompt should pass: %v", err)
	}
	huge := strings.Repeat("lorem ipsum dolor sit amet ", 10_000)
	if err := ForCell("qwen36-27b-q8", huge); err == nil {
		t.Fatal("huge prompt should reject")
	}
}
