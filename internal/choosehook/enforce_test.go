package choosehook

import (
	"path/filepath"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/rtx"
)

func TestEnforce_RTXHitReplays(t *testing.T) {
	dir := t.TempDir()
	cache, err := rtx.New(rtx.Options{Path: filepath.Join(dir, "rtx.ndjson")})
	if err != nil {
		t.Fatal(err)
	}
	// Seed cache.
	_ = cache.Store(rtx.Record{
		Key:       rtx.Key("hello world", "chat|redirect"),
		Prompt:    "hello world",
		StateHash: "chat|redirect",
		Tier:      rtx.Tier1,
		CellID:    "qwen36-27b-q4",
		Response:  "echo: hello",
	})

	cfg := EnforceConfig{RTX: cache, RejectOversized: true}
	base := Output{
		SprintID:      "v14515",
		DecisionLabel: "tier1",
		CellID:        "qwen36-27b-q4",
		BaseURL:       "http://localhost:8080/v1",
		HookMode:      "redirect",
		Reason:        "fresh",
	}
	out, rejected := cfg.Enforce(base, DecideInput{
		Prompt:   "hello world",
		Surface:  "chat",
		HookMode: "redirect",
	}, 16)
	if rejected {
		t.Fatal("hit should not reject")
	}
	if !out.CacheHit {
		t.Fatal("expected cache hit")
	}
	if out.CellID != "qwen36-27b-q4" {
		t.Fatalf("expected cell from cache, got %q", out.CellID)
	}
	if out.Reason != "rtx-replay" {
		t.Fatalf("expected rtx-replay reason, got %q", out.Reason)
	}
}

func TestEnforce_HeadroomRejects(t *testing.T) {
	cfg := EnforceConfig{RejectOversized: true}
	base := Output{
		DecisionLabel: "tier2",
		CellID:        "qwen36-27b-q8",
	}
	out, rejected := cfg.Enforce(base, DecideInput{
		Prompt: "x",
	}, 100_000)
	if !rejected {
		t.Fatal("oversized prompt should reject")
	}
	if out.DecisionLabel != "rejected" {
		t.Fatalf("expected DecisionLabel=rejected, got %q", out.DecisionLabel)
	}
	if out.RejectReason == "" {
		t.Fatal("expected reject reason to be populated")
	}
}

func TestEnforce_HeadroomPasses(t *testing.T) {
	cfg := EnforceConfig{RejectOversized: true}
	base := Output{DecisionLabel: "tier2", CellID: "qwen36-27b-q8"}
	out, rejected := cfg.Enforce(base, DecideInput{Prompt: "tiny"}, 16)
	if rejected {
		t.Fatal("tiny prompt should pass")
	}
	if out.DecisionLabel != "tier2" {
		t.Fatalf("expected unchanged label, got %q", out.DecisionLabel)
	}
}

func TestEnforce_TrimsContext(t *testing.T) {
	cfg := EnforceConfig{}
	base := Output{DecisionLabel: "tier1", CellID: "qwen36-27b-q4"}
	// 9000 chars + ANSI noise
	big := "\x1b[31m" + repeat("x", 9000) + "\x1b[0m"
	out, _ := cfg.Enforce(base, DecideInput{Prompt: big}, 100)
	if out.TrimmedBytes <= 0 {
		t.Fatalf("expected trimmed_bytes > 0, got %d", out.TrimmedBytes)
	}
}

func TestEnforce_StoresOnPass(t *testing.T) {
	dir := t.TempDir()
	cache, _ := rtx.New(rtx.Options{Path: filepath.Join(dir, "rtx.ndjson")})
	cfg := EnforceConfig{RTX: cache}
	base := Output{DecisionLabel: "tier1", CellID: "qwen36-27b-q4"}
	if _, rejected := cfg.Enforce(base, DecideInput{Prompt: "store me", Surface: "chat", HookMode: "redirect"}, 16); rejected {
		t.Fatal("should not reject")
	}
	if cache.Size() < 1 {
		t.Fatal("expected cache to have at least 1 record after pass")
	}
}

func TestEnforce_NoRTXIsNoop(t *testing.T) {
	cfg := EnforceConfig{}
	base := Output{DecisionLabel: "tier0", CellID: "local-echo"}
	out, _ := cfg.Enforce(base, DecideInput{Prompt: "hi"}, 4)
	if out.CacheHit {
		t.Fatal("without rtx there should be no cache hit")
	}
}

// repeat is a tiny helper to keep the test self-contained.
func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}