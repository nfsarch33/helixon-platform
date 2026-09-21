package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/llm"
)

// ctxCaptureProvider records the context its one response was served
// under, so the test can assert what the identity doer would see.
type ctxCaptureProvider struct {
	mu  sync.Mutex
	ctx context.Context
}

func (p *ctxCaptureProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	p.ctx = ctx
	p.mu.Unlock()
	return &llm.CompletionResponse{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "done"}}},
		Usage:   llm.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}, nil
}

// TestRunInfoAttached_ForTicketRuns is the must-be-present control of the
// v18846 export chain: a ticket run's model calls carry run_id, ticket_id
// and the agent identity in ctx, ready for the identity doer to stamp.
func TestRunInfoAttached_ForTicketRuns(t *testing.T) {
	provider := &ctxCaptureProvider{}
	agent, store := newTestAgent(t, provider, &mockToolExecutor{})
	sess, err := store.CreateSession(context.Background(), "agent-runinfo-test", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := agent.RunDurable(context.Background(), "run-info-1", sess.ID, "do the ticket",
		map[string]string{"channel": "ticket", "ticket_id": "T-9"}); err != nil {
		t.Fatalf("RunDurable: %v", err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.ctx == nil {
		t.Fatal("provider was never called")
	}
	info, ok := llm.RunInfoFromContext(provider.ctx)
	if !ok {
		t.Fatal("model call carried no run info")
	}
	if info.RunID != "run-info-1" {
		t.Errorf("RunID = %q, want run-info-1", info.RunID)
	}
	if info.TicketID != "T-9" {
		t.Errorf("TicketID = %q, want T-9", info.TicketID)
	}
	if info.AgentID == "" {
		t.Error("AgentID empty; owner identity missing")
	}
}

// TestRunInfoAttached_NonTicketRunNoTicketID is the must-be-absent
// control: REPL/task runs carry the run identity but no ticket id.
func TestRunInfoAttached_NonTicketRunNoTicketID(t *testing.T) {
	provider := &ctxCaptureProvider{}
	agent, store := newTestAgent(t, provider, &mockToolExecutor{})
	sess, err := store.CreateSession(context.Background(), "agent-runinfo-plain", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := agent.RunDurable(context.Background(), "run-info-2", sess.ID, "just answer", nil); err != nil {
		t.Fatalf("RunDurable: %v", err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	info, ok := llm.RunInfoFromContext(provider.ctx)
	if !ok {
		t.Fatal("model call carried no run info")
	}
	if info.TicketID != "" {
		t.Errorf("TicketID = %q, want empty for a non-ticket run", info.TicketID)
	}
	if info.RunID != "run-info-2" {
		t.Errorf("RunID = %q, want run-info-2", info.RunID)
	}
}

// silence unused time import if fixtures evolve.
var _ = time.Second
