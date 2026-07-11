package main

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// TestReplEchoMode_DirectCall exercises the extracted echo-mode helper
// directly (no cobra wrapping), proving that the helper preserves the
// behaviour the old newReplCmd.RunE had for the no-provider branch.
func TestReplEchoMode_DirectCall(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := runReplEchoMode(&out, strings.NewReader("hello world\n:quit\n"))
	if err != nil {
		t.Fatalf("runReplEchoMode: %v", err)
	}
	if !strings.Contains(out.String(), "echo: hello world") {
		t.Errorf("expected echoed line, got %q", out.String())
	}
}

// TestReplEchoMode_BlankLinesIgnored confirms the helper skips blank input.
func TestReplEchoMode_BlankLinesIgnored(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := runReplEchoMode(&out, strings.NewReader("\n\nhello\n\n:quit\n"))
	if err != nil {
		t.Fatalf("runReplEchoMode: %v", err)
	}
	got := out.String()
	if strings.Count(got, "echo: ") != 1 {
		t.Errorf("expected exactly 1 echo line, got %d in %q", strings.Count(got, "echo: "), got)
	}
}

// TestReplSetupRuntime_RegistersBuiltins confirms the extracted setup helper
// registers the 3 builtin tools (Shell + FileRead + FileWrite) so dispatch mode
// can find them via rt.RegisteredToolCount().
func TestReplSetupRuntime_RegistersBuiltins(t *testing.T) {
	t.Parallel()
	rt, err := setupReplRuntime(helixon.RuntimeConfig{Logger: nil}, nil)
	if err != nil {
		t.Fatalf("setupReplRuntime: %v", err)
	}
	if got := rt.RegisteredToolCount(); got != 3 {
		t.Errorf("expected 3 registered builtin tools, got %d", got)
	}
}

// TestReplDispatchMode_EchoesProviderResponse wires the dispatch helper
// against a runtime whose provider echoes (mock) the input line, proving
// the extracted helper preserves the old newReplCmd.RunE behaviour for the
// dispatch branch.
func TestReplDispatchMode_EchoesProviderResponse(t *testing.T) {
	t.Parallel()
	rt, err := setupReplRuntime(helixon.RuntimeConfig{Logger: nil}, llm.NewMockProvider())
	if err != nil {
		t.Fatalf("setupReplRuntime: %v", err)
	}
	var out bytes.Buffer
	in := strings.NewReader("hello dispatch\n:quit\n")
	if err := runReplDispatchMode(&out, in, context.Background(), rt); err != nil {
		t.Fatalf("runReplDispatchMode: %v", err)
	}
	got := out.String()
	// mock provider returns a literal "Mock response\n"; we just need to prove
	// the loop wired the message through HandleMessage and the helper wrote
	// the response back to out (NOT echoed as "echo: ..." which would mean we
	// took the wrong branch).
	if !strings.Contains(got, "Mock response") {
		t.Errorf("expected provider response 'Mock response' from dispatch, got %q", got)
	}
	if strings.Contains(got, "echo: hello dispatch") {
		t.Errorf("dispatch branch must not echo; got echo fallback in %q", got)
	}
}

// TestReplPrompt_NoProvider confirms the prompt text for echo mode.
func TestReplPrompt_NoProvider(t *testing.T) {
	t.Parallel()
	got := replPrompt("agent-x", 0, true)
	if !strings.Contains(got, "agent-x") {
		t.Errorf("expected agent_id in prompt, got %q", got)
	}
	if !strings.Contains(got, "no provider") {
		t.Errorf("expected 'no provider' marker, got %q", got)
	}
}

// TestReplPrompt_WithProvider confirms the prompt text for dispatch mode.
func TestReplPrompt_WithProvider(t *testing.T) {
	t.Parallel()
	got := replPrompt("agent-x", 5, false)
	if !strings.Contains(got, "agent-x") {
		t.Errorf("expected agent_id in prompt, got %q", got)
	}
	if !strings.Contains(got, "tools=5") {
		t.Errorf("expected tools=5 in prompt, got %q", got)
	}
}

// TestReplScannerLoop_TerminatesOnQuit proves the shared scanner loop helper
// returns cleanly on :quit/:exit.
func TestReplScannerLoop_TerminatesOnQuit(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	scanner := bufio.NewScanner(strings.NewReader("a\n:exit\nb\n"))
	handler := func(line string) error {
		_, _ = out.WriteString("handled:" + line + "\n")
		return nil
	}
	if err := runReplScannerLoop(&out, scanner, handler); err != nil {
		t.Fatalf("runReplScannerLoop: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "handled:a") {
		t.Errorf("expected 'handled:a', got %q", got)
	}
	if strings.Contains(got, "handled:b") {
		t.Errorf("expected to stop before 'b' (quit precedes it), got %q", got)
	}
}
