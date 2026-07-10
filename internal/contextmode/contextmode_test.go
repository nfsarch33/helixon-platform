package contextmode

import (
	"strings"
	"testing"
)

func TestStrip_RemovesAnsi(t *testing.T) {
	in := "\x1b[31mERROR\x1b[0m: bad input"
	want := "ERROR: bad input"
	if got := Strip(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStrip_RemovesBase64(t *testing.T) {
	long := strings.Repeat("A", 300)
	in := "header " + long + " trailer"
	out := Strip(in)
	if strings.Contains(out, long) {
		t.Fatal("expected base64 blob to be replaced")
	}
	if !strings.Contains(out, "[base64-blob]") {
		t.Fatalf("expected marker in output: %q", out)
	}
}

func TestStrip_RemovesNULs(t *testing.T) {
	in := "before\x00\x00\x00after"
	if got := Strip(in); got != "beforeafter" {
		t.Fatalf("got %q", got)
	}
}

func TestTruncate_ShortPassThrough(t *testing.T) {
	in := "hello"
	if got := Truncate(in, 100, "test"); got != in {
		t.Fatalf("short input should pass through, got %q", got)
	}
}

func TestTruncate_LongTruncates(t *testing.T) {
	in := strings.Repeat("x", 10_000)
	out := Truncate(in, 100, "long-paste")
	if len(out) > 100*4+200 { // generous headroom for marker
		t.Fatalf("truncate too long: %d chars", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation marker: %q", out)
	}
}

func TestFormatImportPath_AddsHint(t *testing.T) {
	in := "lots of text\nfoo.go:42..50\nmore text"
	out := FormatImportPath(in)
	if !strings.Contains(out, "[context-mode]") {
		t.Fatalf("expected hint: %q", out)
	}
	if !strings.Contains(out, "foo.go:42..50") {
		t.Fatalf("expected path to remain: %q", out)
	}
}

func TestFormatImportPath_NoMatchPassThrough(t *testing.T) {
	in := "no path here"
	if got := FormatImportPath(in); got != in {
		t.Fatalf("got %q", got)
	}
}

func TestFormatImportPath_HintIdempotent(t *testing.T) {
	in := "[context-mode] hint present\nfoo.go:1..2\nstuff"
	out := FormatImportPath(in)
	if strings.Count(out, "[context-mode]") > 1 {
		t.Fatalf("hint should appear once: %q", out)
	}
}

func TestTrim_EndToEnd(t *testing.T) {
	in := "\x1b[31m" + strings.Repeat("x", 8000) + "\x1b[0m\nfoo.go:10..20"
	out := Trim(in, Default(), "tool-output")
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("ANSI not stripped: %q", out)
	}
	if len(out) > 2048*4+300 {
		t.Fatalf("trim too long: %d", len(out))
	}
	if !strings.Contains(out, "[context-mode]") {
		t.Fatalf("expected import-path hint: %q", out)
	}
}
