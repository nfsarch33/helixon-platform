package helixon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
)

// ---------------------------------------------------------------------------
// A fake SprintBoard that serves the LIVE route table.
//
// The tests drive the REAL controlplane client against it, so a rename of a
// path in the client fails here rather than in production. No test in this
// file touches the network beyond httptest's loopback listener.
// ---------------------------------------------------------------------------

type fakeBoard struct {
	mu sync.Mutex

	ready []controlplane.Ticket
	// heldBy makes a ticket lose the claim race for the poller: the board
	// answers 409 with the winner's name, exactly as the real one does.
	heldBy map[string]string

	searches  int
	claims    []string
	completed map[string]string
	comments  map[string][]string
	// searchErr, when set, makes every search fail (backoff exercise).
	searchErr bool
}

func newFakeBoard(ready ...controlplane.Ticket) *fakeBoard {
	return &fakeBoard{
		ready:     ready,
		heldBy:    map[string]string{},
		completed: map[string]string{},
		comments:  map[string][]string{},
	}
}

func (b *fakeBoard) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tickets/search", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		b.searches++
		fail := b.searchErr
		out := append([]controlplane.Ticket(nil), b.ready...)
		b.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tickets": out, "count": len(out)})
	})
	mux.HandleFunc("POST /api/v1/tickets/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		b.mu.Lock()
		b.claims = append(b.claims, id)
		holder, held := b.heldBy[id]
		if !held {
			// A successful claim moves the ticket out of `ready`, which is
			// what the real board does (ready -> in_progress).
			kept := b.ready[:0]
			for _, tk := range b.ready {
				if tk.ID != id {
					kept = append(kept, tk)
				}
			}
			b.ready = kept
		}
		b.mu.Unlock()
		if held {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "ticket_id": id, "claimed_by": holder})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "ticket_id": id, "claimed_by": "poller"})
	})
	mux.HandleFunc("POST /api/v1/tickets/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var req struct {
			Evidence string `json:"evidence"`
		}
		_ = json.Unmarshal(body, &req)
		b.mu.Lock()
		b.completed[r.PathValue("id")] = req.Evidence
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/v1/tickets/{id}/comments", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var req struct {
			Author string `json:"author"`
			Body   string `json:"body"`
		}
		_ = json.Unmarshal(body, &req)
		b.mu.Lock()
		id := r.PathValue("id")
		b.comments[id] = append(b.comments[id], req.Author+": "+req.Body)
		b.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (b *fakeBoard) snapshot() (claims []string, completed map[string]string, comments map[string][]string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	claims = append(claims, b.claims...)
	completed = map[string]string{}
	for k, v := range b.completed {
		completed[k] = v
	}
	comments = map[string][]string{}
	for k, v := range b.comments {
		comments[k] = append([]string(nil), v...)
	}
	return claims, completed, comments
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testPoller(t *testing.T, board TicketBoard, work TicketWorker, mutate func(*TicketPollerConfig)) *TicketPoller {
	t.Helper()
	cfg := TicketPollerConfig{
		Enabled:       true,
		Interval:      time.Millisecond,
		MaxBackoff:    5 * time.Millisecond,
		MaxConcurrent: 1,
		TicketTimeout: 2 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	p, err := NewTicketPoller(cfg, board, work, "poller", 0, quietLogger())
	if err != nil {
		t.Fatalf("NewTicketPoller: %v", err)
	}
	return p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func runPoller(t *testing.T, p *TicketPoller, until func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := p.Run(ctx); err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	}()
	waitFor(t, "poller condition", until)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

func TestTicketPoller_ClaimsRunsAndCompletesWithEvidence(t *testing.T) {
	board := newFakeBoard(controlplane.Ticket{
		ID: "T-1", Title: "make the thing work", Status: "ready",
		AcceptanceCriteria: "go test ./... is green",
	})
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL: srv.URL, AgentName: "poller",
	}, quietLogger())

	var gotPrompt string
	p := testPoller(t, client, func(_ context.Context, tk controlplane.Ticket) (string, error) {
		gotPrompt = TicketPrompt(tk)
		return "verifier passed; changed 2 files", nil
	}, nil)

	runPoller(t, p, func() bool { return p.Stats().Completed == 1 })

	claims, completed, comments := board.snapshot()
	if len(claims) != 1 || claims[0] != "T-1" {
		t.Fatalf("claims = %v, want exactly [T-1]", claims)
	}
	if got := completed["T-1"]; got != "verifier passed; changed 2 files" {
		t.Fatalf("evidence = %q", got)
	}
	if len(comments) != 0 {
		t.Fatalf("a completed ticket must not be commented on: %v", comments)
	}
	if !strings.Contains(gotPrompt, "go test ./... is green") {
		t.Errorf("prompt dropped the acceptance criteria: %q", gotPrompt)
	}
	if st := p.Stats(); st.Claimed != 1 || st.Completed != 1 || st.Escalated != 0 {
		t.Errorf("stats = %+v", st)
	}
}

// ---------------------------------------------------------------------------
// Lost race
// ---------------------------------------------------------------------------

func TestTicketPoller_LostClaimRaceIsSkippedNotFatal(t *testing.T) {
	board := newFakeBoard(
		controlplane.Ticket{ID: "T-taken", Title: "someone else's", Status: "ready"},
		controlplane.Ticket{ID: "T-mine", Title: "ours", Status: "ready"},
	)
	board.heldBy["T-taken"] = "other-agent"
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL: srv.URL, AgentName: "poller",
	}, quietLogger())

	p := testPoller(t, client, func(_ context.Context, tk controlplane.Ticket) (string, error) {
		return "done " + tk.ID, nil
	}, nil)

	runPoller(t, p, func() bool { return p.Stats().Completed == 1 })

	_, completed, _ := board.snapshot()
	if _, done := completed["T-taken"]; done {
		t.Fatal("a ticket we lost the race for must never be completed by us")
	}
	if completed["T-mine"] != "done T-mine" {
		t.Fatalf("completed = %v", completed)
	}
	if st := p.Stats(); st.Conflicts < 1 {
		t.Fatalf("conflicts = %d, want >= 1; a 409 must be counted, not swallowed", st.Conflicts)
	}
	if st := p.Stats(); st.Errors != 0 {
		t.Fatalf("errors = %d; a lost race is not an error", st.Errors)
	}
}

// ---------------------------------------------------------------------------
// Escalation
// ---------------------------------------------------------------------------

func TestTicketPoller_EscalatesInsteadOfCompleting(t *testing.T) {
	tests := []struct {
		name       string
		workErr    error
		partial    string
		wantPhrase string
	}{
		{
			name:       "verifier failed repeatedly",
			workErr:    fmt.Errorf("agent run: %w", agent.ErrNeedsHumanApproval),
			partial:    "I tried three times",
			wantPhrase: "the verifier failed repeatedly",
		},
		{
			name:       "state changed with no evidence",
			workErr:    fmt.Errorf("agent run: %w", agent.ErrNoVerifierEvidence),
			wantPhrase: "no passing verifier evidence",
		},
		{
			name:       "ordinary failure",
			workErr:    errors.New("provider exploded"),
			wantPhrase: "did not finish successfully",
		},
		{
			name:       "empty output is not evidence",
			partial:    "   ",
			wantPhrase: "no final output",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			board := newFakeBoard(controlplane.Ticket{ID: "T-esc", Title: "hard one", Status: "ready"})
			srv := board.server(t)
			client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
				BaseURL: srv.URL, AgentName: "poller",
			}, quietLogger())

			p := testPoller(t, client, func(_ context.Context, _ controlplane.Ticket) (string, error) {
				return tc.partial, tc.workErr
			}, nil)

			runPoller(t, p, func() bool { return p.Stats().Escalated == 1 })

			_, completed, comments := board.snapshot()
			if len(completed) != 0 {
				t.Fatalf("an escalated ticket must NOT be completed, got %v", completed)
			}
			got := comments["T-esc"]
			if len(got) != 1 {
				t.Fatalf("comments = %v, want exactly one escalation comment", got)
			}
			if !strings.HasPrefix(got[0], "poller: ") {
				t.Errorf("comment is not authored by the agent: %q", got[0])
			}
			if !strings.Contains(got[0], tc.wantPhrase) {
				t.Errorf("comment %q does not explain %q", got[0], tc.wantPhrase)
			}
			if tc.workErr != nil && !strings.Contains(got[0], tc.workErr.Error()) {
				t.Errorf("comment %q does not carry the failure evidence %q", got[0], tc.workErr)
			}
			if tc.partial != "" && strings.TrimSpace(tc.partial) != "" &&
				!strings.Contains(got[0], strings.TrimSpace(tc.partial)) {
				t.Errorf("comment %q dropped the agent's last output", got[0])
			}
		})
	}
}

func TestTicketPoller_DoesNotRetryAnEscalatedTicket(t *testing.T) {
	// The board keeps offering the same ticket (a board that does not
	// transition on claim, or a filter that includes in_progress). Without
	// the escalation hold, the poller would re-claim and re-fail it forever.
	board := newFakeBoard(controlplane.Ticket{ID: "T-stuck", Title: "loops", Status: "ready"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/tickets/search":
			board.mu.Lock()
			board.searches++
			board.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tickets": []controlplane.Ticket{{ID: "T-stuck", Title: "loops", Status: "ready"}},
			})
		case strings.HasSuffix(r.URL.Path, "/claim"):
			board.mu.Lock()
			board.claims = append(board.claims, "T-stuck")
			board.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "ticket_id": "T-stuck"})
		case strings.HasSuffix(r.URL.Path, "/comments"):
			board.mu.Lock()
			board.comments["T-stuck"] = append(board.comments["T-stuck"], "c")
			board.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL: srv.URL, AgentName: "poller",
	}, quietLogger())
	p := testPoller(t, client, func(_ context.Context, _ controlplane.Ticket) (string, error) {
		return "", agent.ErrNeedsHumanApproval
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()
	waitFor(t, "first escalation", func() bool { return p.Stats().Escalated == 1 })
	// Let the poller run many more cycles; it must not touch the ticket again.
	waitFor(t, "further poll cycles", func() bool { return p.Stats().Polls >= 8 })
	cancel()
	<-done

	claims, _, comments := board.snapshot()
	if len(claims) != 1 {
		t.Fatalf("claims = %v, want exactly one; an escalated ticket must not be re-claimed", claims)
	}
	if len(comments["T-stuck"]) != 1 {
		t.Fatalf("comments = %v, want exactly one escalation", comments["T-stuck"])
	}
	if st := p.Stats(); st.Escalated != 1 {
		t.Fatalf("escalated = %d, want 1", st.Escalated)
	}
}

// ---------------------------------------------------------------------------
// Backoff
// ---------------------------------------------------------------------------

func TestTicketPoller_NextWaitBackoff(t *testing.T) {
	t.Parallel()
	p := testPoller(t, stubBoard{}, func(context.Context, controlplane.Ticket) (string, error) { return "", nil },
		func(c *TicketPollerConfig) {
			c.Interval = time.Second
			c.MaxBackoff = 8 * time.Second
		})

	tests := []struct {
		name     string
		outcome  pollOutcome
		start    time.Duration
		slotFree bool
		wantWait time.Duration
		wantNext time.Duration
	}{
		{"idle doubles", outcomeIdle, time.Second, false, time.Second, 2 * time.Second},
		{"idle doubles again", outcomeIdle, 2 * time.Second, false, 2 * time.Second, 4 * time.Second},
		{"idle is capped", outcomeIdle, 8 * time.Second, false, 8 * time.Second, 8 * time.Second},
		{"error backs off too", outcomeError, 2 * time.Second, false, 2 * time.Second, 4 * time.Second},
		{"a claim resets the backoff", outcomeClaimed, 8 * time.Second, true, 0, time.Second},
		{"a claim with no free slot waits one interval", outcomeClaimed, 8 * time.Second, false, time.Second, time.Second},
		{"busy waits one interval and keeps the backoff", outcomeBusy, 4 * time.Second, false, time.Second, 4 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			slots := make(chan struct{}, 1)
			if !tc.slotFree {
				slots <- struct{}{}
			}
			backoff := tc.start
			got := p.nextWait(tc.outcome, &backoff, slots)
			if got != tc.wantWait {
				t.Errorf("wait = %s, want %s", got, tc.wantWait)
			}
			if backoff != tc.wantNext {
				t.Errorf("next backoff = %s, want %s", backoff, tc.wantNext)
			}
		})
	}
}

func TestTicketPoller_BacksOffWhenTheBoardIsDown(t *testing.T) {
	board := newFakeBoard()
	board.searchErr = true
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL: srv.URL, AgentName: "poller",
	}, quietLogger())
	p := testPoller(t, client, func(context.Context, controlplane.Ticket) (string, error) {
		t.Error("worker must not run when the board search fails")
		return "", nil
	}, nil)

	runPoller(t, p, func() bool { return p.Stats().Errors >= 2 })
	if st := p.Stats(); st.Claimed != 0 {
		t.Fatalf("claimed = %d, want 0", st.Claimed)
	}
}

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

func TestTicketPoller_ShutdownMidTicketReportsNothing(t *testing.T) {
	board := newFakeBoard(controlplane.Ticket{ID: "T-slow", Title: "long job", Status: "ready"})
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL: srv.URL, AgentName: "poller",
	}, quietLogger())

	started := make(chan struct{})
	var once sync.Once
	p := testPoller(t, client, func(ctx context.Context, _ controlplane.Ticket) (string, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return "half-finished work", ctx.Err()
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never started")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not drain the in-flight ticket and return")
	}

	_, completed, comments := board.snapshot()
	if len(completed) != 0 {
		t.Fatalf("shutdown must never complete a ticket, got %v", completed)
	}
	if len(comments) != 0 {
		t.Fatalf("shutdown must not escalate either; the run was interrupted, not failed: %v", comments)
	}
	if st := p.Stats(); st.Abandoned != 1 {
		t.Fatalf("abandoned = %d, want 1 (stats = %+v)", st.Abandoned, st)
	}
}

// ---------------------------------------------------------------------------
// The board itself misbehaving
// ---------------------------------------------------------------------------

// failingBoard is a board whose individual operations can be made to fail, so
// the poller's own error handling is exercised rather than assumed.
type failingBoard struct {
	mu        sync.Mutex
	tickets   []controlplane.Ticket
	failClaim bool
	failDone  bool
	failNote  bool
	claims    int
	completes int
	comments  int
}

func (b *failingBoard) SearchTickets(context.Context, controlplane.TicketFilter) ([]controlplane.Ticket, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]controlplane.Ticket(nil), b.tickets...), nil
}

func (b *failingBoard) ClaimTicket(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.claims++
	if b.failClaim {
		return errors.New("board: 500 db locked")
	}
	return nil
}

func (b *failingBoard) CompleteTicket(context.Context, string, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.completes++
	if b.failDone {
		return errors.New("board: 500 complete rejected")
	}
	return nil
}

func (b *failingBoard) AddComment(context.Context, string, string, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.comments++
	if b.failNote {
		return errors.New("board: 500 comment rejected")
	}
	return nil
}

func (b *failingBoard) counts() (claims, completes, comments int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.claims, b.completes, b.comments
}

func TestTicketPoller_BoardFailuresAreCountedNotFatal(t *testing.T) {
	tests := []struct {
		name       string
		board      *failingBoard
		workErr    error
		wantClaim  bool
		wantDone   bool
		wantNote   bool
		wantErrors int
	}{
		{
			name:       "claim rejected with a non-conflict error",
			board:      &failingBoard{tickets: []controlplane.Ticket{{ID: "T-1"}}, failClaim: true},
			wantClaim:  true,
			wantErrors: 1,
		},
		{
			name:       "complete rejected",
			board:      &failingBoard{tickets: []controlplane.Ticket{{ID: "T-1"}}, failDone: true},
			wantClaim:  true,
			wantDone:   true,
			wantErrors: 1,
		},
		{
			name:       "escalation comment rejected",
			board:      &failingBoard{tickets: []controlplane.Ticket{{ID: "T-1"}}, failNote: true},
			workErr:    agent.ErrNeedsHumanApproval,
			wantClaim:  true,
			wantNote:   true,
			wantErrors: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := testPoller(t, tc.board, func(context.Context, controlplane.Ticket) (string, error) {
				return "some output", tc.workErr
			}, nil)
			runPoller(t, p, func() bool { return p.Stats().Errors >= tc.wantErrors })

			claims, completes, comments := tc.board.counts()
			if tc.wantClaim && claims == 0 {
				t.Error("expected a claim attempt")
			}
			if tc.wantDone != (completes > 0) {
				t.Errorf("completes = %d, wantAttempt = %v", completes, tc.wantDone)
			}
			if tc.wantNote != (comments > 0) {
				t.Errorf("comments = %d, wantAttempt = %v", comments, tc.wantNote)
			}
			if st := p.Stats(); st.Errors < tc.wantErrors {
				t.Errorf("errors = %d, want >= %d (stats %+v)", st.Errors, tc.wantErrors, st)
			}
			if st := p.Stats(); st.Completed != 0 {
				t.Errorf("completed = %d; a rejected report is not a completion", st.Completed)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------------

func TestTicketPoller_ConcurrencyCapIsRespected(t *testing.T) {
	board := newFakeBoard(
		controlplane.Ticket{ID: "T-a", Status: "ready"},
		controlplane.Ticket{ID: "T-b", Status: "ready"},
		controlplane.Ticket{ID: "T-c", Status: "ready"},
	)
	srv := board.server(t)
	client := controlplane.NewSprintboardClient(controlplane.SprintboardConfig{
		BaseURL: srv.URL, AgentName: "poller",
	}, quietLogger())

	var mu sync.Mutex
	var inFlight, peak int
	release := make(chan struct{})
	p := testPoller(t, client, func(context.Context, controlplane.Ticket) (string, error) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		return "ok", nil
	}, func(c *TicketPollerConfig) { c.MaxConcurrent = 2 })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = p.Run(ctx) }()

	waitFor(t, "both slots busy", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return peak == 2
	})
	// Give the poller ample opportunity to over-claim: the interval is 1ms,
	// so this is dozens of cycles with every slot already occupied.
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	gotPeak := peak
	mu.Unlock()
	claims, _, _ := board.snapshot()
	close(release)
	cancel()
	<-done

	if gotPeak != 2 {
		t.Fatalf("peak in-flight = %d, want exactly the configured cap of 2", gotPeak)
	}
	if len(claims) != 2 {
		t.Fatalf("claims = %v, want exactly 2; a full poller must not keep claiming", claims)
	}
}

func TestTicketPoller_RefusesToClaimWithoutEnoughBudget(t *testing.T) {
	t.Parallel()
	searches := 0
	board := stubBoard{searches: &searches}
	p := testPoller(t, board, func(context.Context, controlplane.Ticket) (string, error) {
		t.Error("must not run work when the remaining time is short")
		return "", nil
	}, func(c *TicketPollerConfig) { c.TicketTimeout = time.Hour })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	slots := make(chan struct{}, 1)
	var wg sync.WaitGroup
	if got := p.pollOnce(ctx, slots, &wg); got != outcomeIdle {
		t.Fatalf("outcome = %v, want outcomeIdle", got)
	}
	wg.Wait()
	if searches != 0 {
		t.Fatal("the board must not even be searched when there is no budget to finish a ticket")
	}
	if len(slots) != 0 {
		t.Fatal("the concurrency slot must be released when no ticket was claimed")
	}
}

func TestNewTicketPollerValidatesWiring(t *testing.T) {
	t.Parallel()
	work := func(context.Context, controlplane.Ticket) (string, error) { return "", nil }
	tests := []struct {
		name    string
		board   TicketBoard
		work    TicketWorker
		agent   string
		budget  time.Duration
		cfg     TicketPollerConfig
		wantErr error
	}{
		{"no board", nil, work, "a", 0, TicketPollerConfig{}, ErrPollerNoBoard},
		{"no worker", stubBoard{}, nil, "a", 0, TicketPollerConfig{}, ErrPollerNoWorker},
		{"no agent name", stubBoard{}, work, "  ", 0, TicketPollerConfig{}, ErrPollerNoAgentName},
		{
			"ticket budget shorter than the agent timeout",
			stubBoard{}, work, "a", 10 * time.Minute,
			TicketPollerConfig{TicketTimeout: time.Minute},
			ErrTicketBudgetTooSmall,
		},
		{"ok", stubBoard{}, work, "a", time.Minute, TicketPollerConfig{TicketTimeout: 5 * time.Minute}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := NewTicketPoller(tc.cfg, tc.board, tc.work, tc.agent, tc.budget, nil)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if p == nil {
					t.Fatal("expected a poller")
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestTicketPollerConfigDefaults(t *testing.T) {
	t.Parallel()
	got := TicketPollerConfig{}.withDefaults()
	if got.Interval != DefaultTicketPollInterval {
		t.Errorf("Interval = %s", got.Interval)
	}
	if got.MaxBackoff != DefaultTicketMaxBackoff {
		t.Errorf("MaxBackoff = %s", got.MaxBackoff)
	}
	if got.MaxConcurrent != DefaultTicketConcurrency {
		t.Errorf("MaxConcurrent = %d", got.MaxConcurrent)
	}
	if got.TicketTimeout != DefaultTicketTimeout {
		t.Errorf("TicketTimeout = %s", got.TicketTimeout)
	}
	if got.Status != DefaultTicketStatus {
		t.Errorf("Status = %q", got.Status)
	}
	if got.Limit != DefaultTicketConcurrency*5 {
		t.Errorf("Limit = %d", got.Limit)
	}
	if got.Enabled {
		t.Error("the zero config must be DISABLED; autonomy is never a default")
	}
	// An interval longer than the configured max backoff must not produce a
	// backoff shorter than the interval.
	wide := TicketPollerConfig{Interval: time.Minute, MaxBackoff: time.Second}.withDefaults()
	if wide.MaxBackoff < wide.Interval {
		t.Errorf("MaxBackoff %s < Interval %s", wide.MaxBackoff, wide.Interval)
	}
}

func TestTicketPollerFilterCarriesTheConfig(t *testing.T) {
	t.Parallel()
	p := testPoller(t, stubBoard{}, func(context.Context, controlplane.Ticket) (string, error) { return "", nil },
		func(c *TicketPollerConfig) {
			c.Status = "ready"
			c.SprintID = "v18779"
			c.Labels = []string{"go"}
			c.PriorityMin = 3
			c.Limit = 7
		})
	f := p.filter()
	if f.Status != "ready" || f.SprintID != "v18779" || f.PriorityMin != 3 || f.Limit != 7 || len(f.Labels) != 1 {
		t.Fatalf("filter = %+v", f)
	}
}

func TestTruncateEvidenceBoundsModelOutput(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", maxEvidenceBytes+500)
	got := truncateEvidence(long, maxEvidenceBytes)
	if len(got) <= maxEvidenceBytes || !strings.HasSuffix(got, "[truncated]") {
		t.Fatalf("len = %d, suffix = %q", len(got), got[len(got)-20:])
	}
	if truncateEvidence("  spaced  ", 100) != "spaced" {
		t.Error("evidence should be trimmed")
	}
}

func TestGrowBackoffIsCapped(t *testing.T) {
	t.Parallel()
	if got := growBackoff(time.Second, 4*time.Second); got != 2*time.Second {
		t.Errorf("got %s", got)
	}
	if got := growBackoff(3*time.Second, 4*time.Second); got != 4*time.Second {
		t.Errorf("got %s, want the cap", got)
	}
	if got := growBackoff(time.Duration(1<<62), time.Minute); got != time.Minute {
		t.Errorf("overflow must clamp to the cap, got %s", got)
	}
}

// stubBoard is an inert board for the wiring/validation tests.
type stubBoard struct{ searches *int }

func (s stubBoard) SearchTickets(context.Context, controlplane.TicketFilter) ([]controlplane.Ticket, error) {
	if s.searches != nil {
		*s.searches++
	}
	return nil, nil
}
func (s stubBoard) ClaimTicket(context.Context, string) error            { return nil }
func (s stubBoard) CompleteTicket(context.Context, string, string) error { return nil }
func (s stubBoard) AddComment(context.Context, string, string, string) error {
	return nil
}
