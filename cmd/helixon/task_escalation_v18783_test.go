package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/helixon"
	"github.com/nfsarch33/helixon-platform/internal/llm"

	_ "modernc.org/sqlite"
)

// recordingBoard observes which write verb task mode chose. The distinction
// between the two slices IS the defect: a failed run that lands in completed
// is a ticket marked DONE on a durable board because the work failed.
type recordingBoard struct {
	mu         sync.Mutex
	completed  []string
	comments   []comment
	commentErr error
}

type comment struct{ ticket, author, body string }

func (b *recordingBoard) CompleteTicket(_ context.Context, ticketID, evidence string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.completed = append(b.completed, ticketID+"|"+evidence)
	return nil
}

func (b *recordingBoard) AddComment(_ context.Context, ticketID, author, body string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.comments = append(b.comments, comment{ticketID, author, body})
	return b.commentErr
}

func (b *recordingBoard) snapshot() ([]string, []comment) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.completed...), append([]comment(nil), b.comments...)
}

// taskTestRuntime builds a configured runtime driven by a scripted provider.
// An empty response list makes the first model call fail, which is the
// cheapest way to produce a GENERIC run error — the class that used to be
// reported as a completion.
func taskTestRuntime(t *testing.T, responses []*llm.CompletionResponse) *helixon.Runtime {
	t.Helper()
	cfg := serveTestConfig(t)
	rt := helixon.NewRuntime(&scriptedCLIProvider{responses: responses}, cfg)
	if err := rt.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	g, err := newGuardrails(cfg)
	if err != nil {
		t.Fatalf("newGuardrails: %v", err)
	}
	if err := rt.Configure(context.Background(), g.configOptions()...); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { _ = rt.Shutdown(context.Background()) })
	return rt
}

// TestExecuteAndReport_GenericRunErrorEscalatesAndNeverCompletes is the
// v18783 defect-2 assertion.
//
// `helixon task` used to answer a generic run failure with
// CompleteTicket("error: ..."), marking the ticket DONE because the work
// failed. Restoring that call fails this test on the completed-tickets
// assertion; dropping the AddComment call fails it on the comment assertion.
func TestExecuteAndReport_GenericRunErrorEscalatesAndNeverCompletes(t *testing.T) {
	t.Parallel()
	rt := taskTestRuntime(t, nil) // provider fails on the first call
	board := &recordingBoard{}
	var out bytes.Buffer

	_, err := executeAndReport(context.Background(), rt, "do the thing", "TICKET-7", "task-agent", board, &out)
	if err == nil {
		t.Fatal("a failed run must return an error")
	}

	completed, comments := board.snapshot()
	if len(completed) != 0 {
		t.Fatalf("the ticket was completed on a FAILED run: %v", completed)
	}
	if len(comments) != 1 {
		t.Fatalf("expected exactly one escalation comment, got %d: %v", len(comments), comments)
	}
	c := comments[0]
	if c.ticket != "TICKET-7" {
		t.Errorf("comment ticket = %q, want TICKET-7", c.ticket)
	}
	if c.author != "task-agent" {
		t.Errorf("comment author = %q, want the agent id (an anonymous escalation cannot be routed)", c.author)
	}
	// The comment must carry the evidence a human needs, in the poller's
	// vocabulary — it is rendered by helixon.EscalationComment, not by a
	// second copy of the same words living in cmd/.
	for _, want := range []string{"NOT completed", "Failure:", "scripted provider exhausted"} {
		if !strings.Contains(c.body, want) {
			t.Errorf("escalation comment missing %q; got %q", want, c.body)
		}
	}
	if !strings.Contains(out.String(), "NOT completed") {
		t.Errorf("stdout must say the ticket was not completed; got %q", out.String())
	}
}

// TestExecuteAndReport_SuccessStillCompletesTheTicket is the POSITIVE
// CONTROL for the test above.
//
// "Never call CompleteTicket" is trivially satisfiable by never calling it at
// all, which would silently strand every successful ticket in progress
// forever. A successful run must still complete, with the response as
// evidence and no escalation comment.
func TestExecuteAndReport_SuccessStillCompletesTheTicket(t *testing.T) {
	t.Parallel()
	rt := taskTestRuntime(t, []*llm.CompletionResponse{
		{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "investigated; nothing to change"}}}},
	})
	board := &recordingBoard{}
	var out bytes.Buffer

	resp, err := executeAndReport(context.Background(), rt, "investigate", "TICKET-8", "task-agent", board, &out)
	if err != nil {
		t.Fatalf("a successful run must not error: %v", err)
	}
	if !strings.Contains(resp, "investigated") {
		t.Errorf("response = %q, want the agent's answer", resp)
	}

	completed, comments := board.snapshot()
	if len(completed) != 1 {
		t.Fatalf("a successful run must complete the ticket exactly once; got %v", completed)
	}
	if !strings.Contains(completed[0], "TICKET-8") || !strings.Contains(completed[0], "investigated") {
		t.Errorf("completion = %q, want the ticket id and the response as evidence", completed[0])
	}
	if len(comments) != 0 {
		t.Errorf("a successful run must not escalate; got %v", comments)
	}
	if !strings.Contains(out.String(), "completed") {
		t.Errorf("stdout must report the completion; got %q", out.String())
	}
}

// TestExecuteAndReport_CompletionIsNotAFallbackWhenEscalationFails closes the
// obvious repair of the wrong shape: if the escalation comment cannot be
// posted, the answer is a louder failure, never "complete it instead".
func TestExecuteAndReport_CompletionIsNotAFallbackWhenEscalationFails(t *testing.T) {
	t.Parallel()
	rt := taskTestRuntime(t, nil)
	board := &recordingBoard{commentErr: errors.New("board unreachable")}
	var out bytes.Buffer

	if _, err := executeAndReport(context.Background(), rt, "do the thing", "TICKET-9", "task-agent", board, &out); err == nil {
		t.Fatal("a failed run must return an error")
	}
	if completed, _ := board.snapshot(); len(completed) != 0 {
		t.Fatalf("a failed escalation must not fall back to completing the ticket: %v", completed)
	}
	if !strings.Contains(out.String(), "escalation comment failed") {
		t.Errorf("a failed escalation must be visible on stdout; got %q", out.String())
	}
}

// TestExecuteAndReport_NoBoardIsNotACrash keeps the --prompt path (no ticket,
// no sprintboard) working: there is nothing to report to, and that is fine.
func TestExecuteAndReport_NoBoardIsNotACrash(t *testing.T) {
	t.Parallel()
	rt := taskTestRuntime(t, nil)
	var out bytes.Buffer
	if _, err := executeAndReport(context.Background(), rt, "do the thing", "", "task-agent", nil, &out); err == nil {
		t.Fatal("a failed run must still return an error without a board")
	}
}
