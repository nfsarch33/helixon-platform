package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestEvalRunSmoke(t *testing.T) {
	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetArgs([]string{"eval", "run", "--suite", "smoke"})

	err := root.Execute()
	if err != nil {
		t.Fatalf("eval run smoke failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Suite: smoke") {
		t.Errorf("expected output to contain 'Suite: smoke', got: %s", out)
	}
	if !strings.Contains(out, "PASS") {
		t.Errorf("expected PASS verdict, got: %s", out)
	}
}

func TestEvalRunUnknownSuite(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"eval", "run", "--suite", "nonexistent"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for unknown suite, got nil")
	}
	if !strings.Contains(err.Error(), "unknown eval suite") {
		t.Errorf("expected 'unknown eval suite' error, got: %v", err)
	}
}

func TestResolveSuite(t *testing.T) {
	suite, err := resolveSuite("smoke")
	if err != nil {
		t.Fatalf("resolveSuite(smoke) failed: %v", err)
	}
	if suite.Name != "smoke" {
		t.Errorf("expected name=smoke, got %q", suite.Name)
	}
	if len(suite.Cases) < 3 {
		t.Errorf("expected at least 3 cases in smoke suite, got %d", len(suite.Cases))
	}
}
