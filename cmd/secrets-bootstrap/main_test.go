package main

import (
	"strings"
	"testing"
)

func TestRedact_NoToken(t *testing.T) {
	got := redact("op read failed: not found")
	if got != "op read failed: not found" {
		t.Errorf("expected no redaction, got %q", got)
	}
}

func TestRedact_WithToken(t *testing.T) {
	input := "op read failed: ops_eyJAbcDefGhiJklMnoPqrStuVwxYz.0123456789abcdef0123456789abcdef0123456789abcdef"
	got := redact(input)
	if !strings.Contains(got, "ops_eyJ[REDACTED]") {
		t.Errorf("expected redaction marker, got %q", got)
	}
	// Must NOT contain the full token
	if strings.Contains(got, "0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Errorf("token suffix leaked: %q", got)
	}
}

func TestVersionConstant(t *testing.T) {
	if version == "" {
		t.Fatal("version constant empty")
	}
}
