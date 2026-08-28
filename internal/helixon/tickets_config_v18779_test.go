package helixon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"

	_ "modernc.org/sqlite"
)

// v18779: `tickets:` is the one block in this config whose default is OFF.
// These tests exist so nobody can flip that default by accident.

func TestTicketsConfig_DefaultsOff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		yaml string
	}{
		{"empty document", ""},
		{"no tickets block", "agent_id: a\nsprintboard:\n  url: http://127.0.0.1:9400\n"},
		{"block present but enabled omitted", "sprintboard:\n  url: http://x\ntickets:\n  interval: 10s\n"},
		{"explicitly false", "sprintboard:\n  url: http://x\ntickets:\n  enabled: false\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fc, err := DecodeFileConfig([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			cfg, err := fc.ToRuntimeConfig()
			if err != nil {
				t.Fatalf("ToRuntimeConfig: %v", err)
			}
			if cfg.Tickets.Enabled {
				t.Fatal("ticket polling must be OFF unless the config says otherwise")
			}
		})
	}
}

func TestTicketsConfig_Parses(t *testing.T) {
	t.Parallel()
	const doc = `
agent_id: puller
timeout: 5m
sprintboard:
  url: http://127.0.0.1:9400
tickets:
  enabled: true
  interval: 15s
  max_backoff: 2m
  max_concurrent: 3
  ticket_timeout: 20m
  status: ready
  sprint_id: v18779
  priority_min: 2
  labels: [go, infra]
  limit: 9
`
	fc, err := DecodeFileConfig([]byte(doc))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	cfg, err := fc.ToRuntimeConfig()
	if err != nil {
		t.Fatalf("ToRuntimeConfig: %v", err)
	}
	got := cfg.Tickets
	want := TicketPollerConfig{
		Enabled: true, Interval: 15 * time.Second, MaxBackoff: 2 * time.Minute,
		MaxConcurrent: 3, TicketTimeout: 20 * time.Minute, Status: "ready",
		SprintID: "v18779", PriorityMin: 2, Limit: 9,
	}
	want.Labels = []string{"go", "infra"}
	if got.Enabled != want.Enabled || got.Interval != want.Interval || got.MaxBackoff != want.MaxBackoff ||
		got.MaxConcurrent != want.MaxConcurrent || got.TicketTimeout != want.TicketTimeout ||
		got.Status != want.Status || got.SprintID != want.SprintID || got.PriorityMin != want.PriorityMin ||
		got.Limit != want.Limit || strings.Join(got.Labels, ",") != "go,infra" {
		t.Fatalf("tickets = %+v, want %+v", got, want)
	}
}

func TestTicketsConfig_RejectsBadInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		wantSub string
	}{
		{
			"enabled with no board",
			"tickets:\n  enabled: true\n",
			"sprintboard.url is empty",
		},
		{
			"bad interval",
			"sprintboard:\n  url: http://x\ntickets:\n  enabled: true\n  interval: soon\n",
			"tickets.interval",
		},
		{
			"bad ticket_timeout",
			"sprintboard:\n  url: http://x\ntickets:\n  enabled: true\n  ticket_timeout: later\n",
			"tickets.ticket_timeout",
		},
		{
			"bad max_backoff",
			"sprintboard:\n  url: http://x\ntickets:\n  enabled: true\n  max_backoff: ages\n",
			"tickets.max_backoff",
		},
		{
			"unknown key inside the block",
			"tickets:\n  enabbled: true\n",
			"enabbled",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fc, decErr := DecodeFileConfig([]byte(tc.yaml))
			var err error
			if decErr != nil {
				err = decErr
			} else {
				_, err = fc.ToRuntimeConfig()
			}
			if err == nil {
				t.Fatalf("expected an error mentioning %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Runtime wiring
// ---------------------------------------------------------------------------

func configuredRuntime(t *testing.T, cfg RuntimeConfig, opts ...ConfigOption) (*Runtime, error) {
	t.Helper()
	cfg.SessionDSN = "file:" + t.Name() + "?mode=memory&cache=shared"
	cfg.Logger = quietLogger()
	rt := NewRuntime(&stubProvider{resp: "ok"}, cfg)
	ctx := context.Background()
	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = rt.store.Close() })
	return rt, rt.Configure(ctx, opts...)
}

func TestRuntime_TicketPollerIsOffByDefault(t *testing.T) {
	rt, err := configuredRuntime(t, RuntimeConfig{AgentID: "a"})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if rt.TicketPoller() != nil {
		t.Fatal("a runtime with no tickets config must not have a poller")
	}
}

func TestRuntime_TicketPollerWiredWhenEnabled(t *testing.T) {
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL: "http://127.0.0.1:9400", AgentName: "a",
	}, quietLogger())
	rt, err := configuredRuntime(t, RuntimeConfig{
		AgentID: "a",
		Timeout: time.Minute,
		Tickets: TicketPollerConfig{Enabled: true, TicketTimeout: 5 * time.Minute},
	}, WithSprintboard(client))
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	p := rt.TicketPoller()
	if p == nil {
		t.Fatal("tickets.enabled must produce a poller")
	}
	if p.Config().TicketTimeout != 5*time.Minute {
		t.Fatalf("ticket timeout = %s", p.Config().TicketTimeout)
	}
}

func TestRuntime_TicketPollerConfigureFailsLoudly(t *testing.T) {
	tests := []struct {
		name    string
		cfg     RuntimeConfig
		opts    []ConfigOption
		wantSub string
	}{
		{
			name:    "enabled with no sprintboard client",
			cfg:     RuntimeConfig{AgentID: "a", Tickets: TicketPollerConfig{Enabled: true}},
			wantSub: "no sprintboard client is wired",
		},
		{
			name: "per-ticket budget shorter than the agent timeout",
			cfg: RuntimeConfig{
				AgentID: "a",
				Timeout: 30 * time.Minute,
				Tickets: TicketPollerConfig{Enabled: true, TicketTimeout: time.Minute},
			},
			opts: []ConfigOption{WithSprintboard(controlplane.NewSprintboardClient(
				controlplane.SprintboardConfig{BaseURL: "http://127.0.0.1:9400", AgentName: "a"}, nil))},
			wantSub: "shorter than the agent timeout",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := configuredRuntime(t, tc.cfg, tc.opts...)
			if err == nil {
				t.Fatalf("expected Configure to fail with %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestRuntime_TicketPollerBudgetErrorIsTyped(t *testing.T) {
	_, err := configuredRuntime(t, RuntimeConfig{
		AgentID: "a",
		Timeout: 30 * time.Minute,
		Tickets: TicketPollerConfig{Enabled: true, TicketTimeout: time.Minute},
	}, WithSprintboard(controlplane.NewSprintboardClient(
		controlplane.SprintboardConfig{BaseURL: "http://127.0.0.1:9400", AgentName: "a"}, nil)))
	if !errors.Is(err, ErrTicketBudgetTooSmall) {
		t.Fatalf("err = %v, want wrapped ErrTicketBudgetTooSmall", err)
	}
}

// TestRuntime_TicketWorkerRunsTheAgentLoop proves the poller's worker is the
// real agent loop with a real session, not a stub that would make every
// poller test above vacuous.
func TestRuntime_TicketWorkerRunsTheAgentLoop(t *testing.T) {
	rt, err := configuredRuntime(t, RuntimeConfig{AgentID: "worker"})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	out, err := rt.runTicketWork(context.Background(), controlplane.Ticket{
		ID: "T-42", Title: "do a thing", AcceptanceCriteria: "it works",
	})
	if err != nil {
		t.Fatalf("runTicketWork: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want the model's final content", out)
	}
	turns, serr := rt.store.SearchTurns(context.Background(), "SprintBoard", 10)
	if serr != nil {
		t.Fatalf("SearchTurns: %v", serr)
	}
	if len(turns) == 0 {
		t.Fatal("a ticket run must leave its turns in the session store for audit")
	}
}

func TestTicketPromptCarriesTheTicket(t *testing.T) {
	t.Parallel()
	got := TicketPrompt(controlplane.Ticket{
		ID: "T-7", Title: "fix the flake", Description: "it fails on Tuesdays",
		AcceptanceCriteria: "100 green runs",
	})
	for _, want := range []string{"T-7", "fix the flake", "it fails on Tuesdays", "100 green runs", "verifier"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
}
