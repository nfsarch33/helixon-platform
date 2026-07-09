// Tests for the persistence package (v17006-4/5 RED tests).
package persistence

import (
	"context"
	"testing"
)

// TestPersist_SaveAndResume asserts Save → Load roundtrip preserves state.
func TestPersist_SaveAndResume(t *testing.T) {
	ctx := context.Background()
	be := NewInMemoryBackend()
	p := NewPersist(be)

	state := State{
		Version:   1,
		AgentID:   "agent-test-1",
		SessionID: "session-1",
		KVState:   map[string]any{"key": "value"},
		TokensIn:  100,
		TokensOut: 50,
		MachineID: "win3-wsl3",
		SprintID:  "v17006",
	}

	if err := p.Save(ctx, state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	resumed, err := p.Resume(ctx, "agent-test-1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.AgentID != state.AgentID {
		t.Fatalf("AgentID mismatch: %s vs %s", resumed.AgentID, state.AgentID)
	}
	if resumed.TokensIn != 100 || resumed.TokensOut != 50 {
		t.Fatalf("token counts wrong: %+v", resumed)
	}
	if resumed.KVState["key"] != "value" {
		t.Fatalf("kv state wrong: %+v", resumed.KVState)
	}
}

// TestPersist_ResumeNoState asserts resume of unknown agent returns ErrNoState.
func TestPersist_ResumeNoState(t *testing.T) {
	ctx := context.Background()
	be := NewInMemoryBackend()
	p := NewPersist(be)

	_, err := p.Resume(ctx, "unknown-agent")
	if err != ErrNoState {
		t.Fatalf("want ErrNoState, got %v", err)
	}
}

// TestPersist_OverwriteSave asserts saving twice keeps the latest state.
func TestPersist_OverwriteSave(t *testing.T) {
	ctx := context.Background()
	be := NewInMemoryBackend()
	p := NewPersist(be)

	for i := 1; i <= 3; i++ {
		state := State{
			Version:  i,
			AgentID:  "agent-overwrite",
			TokensIn: i * 100,
		}
		if err := p.Save(ctx, state); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	resumed, _ := p.Resume(ctx, "agent-overwrite")
	if resumed.Version != 3 {
		t.Fatalf("Version: want 3, got %d", resumed.Version)
	}
	if resumed.TokensIn != 300 {
		t.Fatalf("TokensIn: want 300, got %d", resumed.TokensIn)
	}
}
