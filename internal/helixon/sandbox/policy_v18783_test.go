package sandbox

import (
	"context"
	"errors"
	"testing"
)

// TestDefaultPolicy_WebFetchIsDenied is the v18783 defect-1 assertion.
//
// web_fetch was absent from the policy map, so it fell through to the
// PathGuard default, so it ran on the HOST — with live network — while the
// sandbox around it advertised --network=none. Deleting the "web_fetch" entry
// from DefaultPolicy fails this test.
func TestDefaultPolicy_WebFetchIsDenied(t *testing.T) {
	t.Parallel()
	if got := DefaultPolicy().For("web_fetch"); got != DispositionDeny {
		t.Fatalf("DefaultPolicy().For(\"web_fetch\") = %s, want deny: an unlisted network tool executes on the host", got)
	}
}

// TestDefaultPolicy_UnlistedToolIsDenied covers the general shape of the same
// hole: web_fetch was only the tool that happened to be registered. Reverting
// Default to DispositionPathGuard fails this test.
func TestDefaultPolicy_UnlistedToolIsDenied(t *testing.T) {
	t.Parallel()
	p := DefaultPolicy()
	if p.Default != DispositionDeny {
		t.Fatalf("DefaultPolicy().Default = %s, want deny", p.Default)
	}
	for _, tool := range []string{"a_tool_nobody_classified", "web_search", "http_post"} {
		if got := p.For(tool); got != DispositionDeny {
			t.Errorf("For(%q) = %s, want deny", tool, got)
		}
	}
}

// TestExecute_DeniedToolNeverReachesTheInnerExecutor is the behavioral half:
// the policy value is only worth something if Execute acts on it. A denied
// tool must be refused BEFORE the host-side handler runs, not after.
func TestExecute_DeniedToolNeverReachesTheInnerExecutor(t *testing.T) {
	t.Parallel()
	e, inner, _ := newTestExecutor(t, DefaultPolicy(), nil)

	out, err := e.Execute(context.Background(), "web_fetch", `{"url":"https://example.invalid/exfil"}`)
	if err == nil {
		t.Fatal("web_fetch must be refused by the gate")
	}
	if !errors.Is(err, ErrToolDenied) {
		t.Errorf("error = %v, want ErrToolDenied", err)
	}
	if out != "" {
		t.Errorf("a denied tool must return no output; got %q", out)
	}
	if len(inner.calls) != 0 {
		t.Fatalf("the host-side web_fetch handler must never be reached; inner saw %v", inner.calls)
	}
}

// TestExecute_ClassifiedToolsStillReachTheirHandlers is the POSITIVE CONTROL
// for the two tests above.
//
// It is the whole reason this file is not just three deny assertions. A
// policy of "deny everything" would pass every negative test in this package
// while making the agent useless, and this estate has already shipped a
// sandbox that passed eight containment tests while blocking its own Go
// toolchain. So: the legitimate tools must still get through, and this test
// fails if the default-deny flip is implemented by tightening the default
// WITHOUT classifying the tools the agent actually needs.
func TestExecute_ClassifiedToolsStillReachTheirHandlers(t *testing.T) {
	t.Parallel()

	// Every tool the repository registers, minus the two that are
	// deliberately refused (web_fetch, autoresearch_run) and minus shell,
	// which is intercepted by design and asserted separately in
	// TestExecute_ShellIsInterceptedNotForwarded.
	forwarded := []struct {
		tool string
		args string
	}{
		{"file_read", `{"path":"notes.txt"}`},
		{"file_write", `{"path":"notes.txt","content":"hi"}`},
		{"verifier_run", `{"check":"go_build"}`},
		{"memory", `{"op":"search","query":"x"}`},
		{"memory.read", `{"id":"m1"}`},
		{"memory.search", `{"query":"x"}`},
		{"memory.write", `{"content":"x"}`},
		{"sprintboard", `{"op":"sprint_status"}`},
		{"sprintboard.claim_ticket", `{"ticket_id":"T-1"}`},
		{"sprintboard.complete_ticket", `{"ticket_id":"T-1","evidence":"ok"}`},
	}
	for _, tc := range forwarded {
		t.Run(tc.tool, func(t *testing.T) {
			t.Parallel()
			e, inner, _ := newTestExecutor(t, DefaultPolicy(), nil)
			got, err := e.Execute(context.Background(), tc.tool, tc.args)
			if err != nil {
				t.Fatalf("%s must still be dispatched, got error: %v", tc.tool, err)
			}
			if got != "inner-result" {
				t.Errorf("%s output = %q, want the inner handler's result", tc.tool, got)
			}
			if len(inner.calls) != 1 || inner.calls[0] != tc.tool {
				t.Fatalf("%s never reached the inner executor; inner saw %v", tc.tool, inner.calls)
			}
		})
	}
}

// TestDefaultPolicy_EveryClassificationIsDeliberate pins the full table.
//
// Policy.For cannot distinguish "explicitly classified" from "fell through to
// the default" — both return a Disposition — so a table that reads correctly
// through For() can still be missing every entry. This reads the map directly,
// which is what makes it a real check that each tool was thought about.
func TestDefaultPolicy_EveryClassificationIsDeliberate(t *testing.T) {
	t.Parallel()
	want := map[string]Disposition{
		"shell":                       DispositionSandbox,
		"file_read":                   DispositionPathGuard,
		"file_write":                  DispositionPathGuard,
		"verifier_run":                DispositionAllow,
		"memory":                      DispositionAllow,
		"memory.read":                 DispositionAllow,
		"memory.search":               DispositionAllow,
		"memory.write":                DispositionAllow,
		"sprintboard":                 DispositionAllow,
		"sprintboard.claim_ticket":    DispositionAllow,
		"sprintboard.complete_ticket": DispositionAllow,
		"web_fetch":                   DispositionDeny,
		"autoresearch_run":            DispositionDeny,
	}
	got := DefaultPolicy().Tools
	for tool, d := range want {
		actual, listed := got[tool]
		if !listed {
			t.Errorf("%q is not listed in DefaultPolicy; it would fall through to the default", tool)
			continue
		}
		if actual != d {
			t.Errorf("DefaultPolicy Tools[%q] = %s, want %s", tool, actual, d)
		}
	}
	for tool := range got {
		if _, expected := want[tool]; !expected {
			t.Errorf("DefaultPolicy classifies %q, which this table does not know about; classify it here too", tool)
		}
	}
}
