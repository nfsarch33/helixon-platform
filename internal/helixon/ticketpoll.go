package helixon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/agentmetrics"
	"github.com/nfsarch33/helixon-platform/internal/helixon/controlplane"
)

// TicketPoller closes the autonomy gap in serve mode.
//
// Before v18779 the runtime registered with the board, re-registered on a
// timer as a heartbeat, and then waited for someone to talk to it. Nothing
// ever pulled work: an agent that is "running" and has an empty inbox is
// indistinguishable from an agent that is down. Task mode could execute a
// ticket, but only one, and only when a human typed its ID on a command line.
//
// The poller is the missing pull: search the board for ready work, claim one
// ticket, run it through the same agent loop (verifier gate included), and
// then either complete it with evidence or hand it back to a human with the
// failure attached. It is default-OFF; enabling autonomy is a deliberate act
// recorded in a config file, not a side effect of upgrading a binary.
//
// What it deliberately does NOT do:
//
//   - Complete a ticket whose run was cut short by shutdown. The work is
//     unfinished; saying "done" would be a lie written to a durable board.
//   - Complete a ticket whose verifier never passed. That is the whole point
//     of the Sprint-D gate, and a poller that "completed anyway" would undo
//     it from the outside.
//   - Retry an escalated ticket. A loop that re-claims the ticket it just
//     failed is how an agent burns a budget converging on nothing.
var (
	// ErrPollerNoBoard is returned when the poller is built without a board.
	ErrPollerNoBoard = errors.New("helixon: ticket poller requires a sprintboard client")
	// ErrPollerNoWorker is returned when the poller is built without work to do.
	ErrPollerNoWorker = errors.New("helixon: ticket poller requires a worker")
	// ErrPollerNoAgentName is returned when the poller has no identity. The
	// escalation comment needs an author, and an anonymous escalation is one
	// no human can route.
	ErrPollerNoAgentName = errors.New("helixon: ticket poller requires an agent name")
	// ErrTicketBudgetTooSmall is returned when the per-ticket deadline is
	// shorter than the agent's own run timeout. Claiming under that config
	// guarantees the agent is cut off mid-run on every single ticket, leaving
	// a trail of claimed-but-abandoned work on the board.
	ErrTicketBudgetTooSmall = errors.New("helixon: tickets.ticket_timeout is shorter than the agent timeout; the agent could never finish a ticket it claimed")
)

// Poller defaults. Interval is deliberately unhurried: this is a background
// pull against a SQLite-backed board, not a hot path.
const (
	DefaultTicketPollInterval = 30 * time.Second
	DefaultTicketMaxBackoff   = 5 * time.Minute
	DefaultTicketTimeout      = 15 * time.Minute
	DefaultTicketStatus       = "ready"
	DefaultTicketConcurrency  = 1
	// maxEvidenceBytes bounds what is written back to the board. Agent output
	// is model-controlled and unbounded; the board column is not.
	maxEvidenceBytes = 4000
)

// TicketPollerConfig configures the ticket-polling worker.
type TicketPollerConfig struct {
	// Enabled is the feature flag. Default false, everywhere.
	Enabled bool
	// Interval is the base poll period and the floor of the backoff.
	Interval time.Duration
	// MaxBackoff caps the idle backoff.
	MaxBackoff time.Duration
	// MaxConcurrent is how many tickets may be in flight at once.
	MaxConcurrent int
	// TicketTimeout is the hard per-ticket deadline.
	TicketTimeout time.Duration
	// Status, SprintID, Labels, PriorityMin, Limit narrow the board search.
	Status      string
	SprintID    string
	Labels      []string
	PriorityMin int
	Limit       int
}

//nolint:gocritic // hugeParam: value semantics keep the defaulted copy free of aliasing
func (c TicketPollerConfig) withDefaults() TicketPollerConfig {
	if c.Interval <= 0 {
		c.Interval = DefaultTicketPollInterval
	}
	if c.MaxBackoff < c.Interval {
		c.MaxBackoff = DefaultTicketMaxBackoff
	}
	if c.MaxBackoff < c.Interval {
		c.MaxBackoff = c.Interval
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = DefaultTicketConcurrency
	}
	if c.TicketTimeout <= 0 {
		c.TicketTimeout = DefaultTicketTimeout
	}
	if c.Status == "" {
		c.Status = DefaultTicketStatus
	}
	if c.Limit <= 0 {
		c.Limit = c.MaxConcurrent * 5
	}
	return c
}

// TicketBoard is the slice of the SprintBoard API the poller needs.
// *controlplane.SprintboardClient satisfies it; the interface exists so the
// tests can drive the poller against an httptest board or a stub without a
// live network.
type TicketBoard interface {
	SearchTickets(ctx context.Context, filter controlplane.TicketFilter) ([]controlplane.Ticket, error)
	ClaimTicket(ctx context.Context, ticketID string) error
	CompleteTicket(ctx context.Context, ticketID, evidence string) error
	AddComment(ctx context.Context, ticketID, author, body string) error
}

// TicketWorker executes one ticket and returns the evidence text. It is
// expected to surface agent.ErrNeedsHumanApproval / agent.ErrNoVerifierEvidence
// unwrapped-detectable, which is what tells the poller to escalate instead of
// completing.
type TicketWorker func(ctx context.Context, ticket controlplane.Ticket) (string, error)

// TicketPollerStats is the observable outcome tally. Tests assert on it; the
// runtime logs it.
type TicketPollerStats struct {
	Polls     int
	Claimed   int
	Conflicts int
	Completed int
	Escalated int
	Abandoned int
	Errors    int
}

// TicketPoller pulls ready work off the board and runs it.
type TicketPoller struct {
	cfg       TicketPollerConfig
	board     TicketBoard
	work      TicketWorker
	agentName string
	logger    *slog.Logger
	metrics   *agentmetrics.Metrics

	mu    sync.Mutex
	stats TicketPollerStats
	// reserved is the in-process lock set: a ticket in here is either in
	// flight or permanently escalated, and is never claimed again.
	reserved map[string]struct{}
	// escalated is the subset of reserved that must never be released.
	escalated map[string]struct{}
}

// TicketPollerOption is optional poller wiring. Options are variadic so adding
// a dependency does not churn every existing call site — including the tests,
// which are the callers most worth leaving alone.
type TicketPollerOption func(*TicketPoller)

// WithPollerMetrics attaches the runtime metrics. A nil *Metrics is accepted
// and inert, so the caller does not need a branch.
func WithPollerMetrics(m *agentmetrics.Metrics) TicketPollerOption {
	return func(p *TicketPoller) { p.metrics = m }
}

// NewTicketPoller validates the wiring and returns a poller.
//
// agentBudget is the agent's own per-run timeout. A per-ticket deadline
// shorter than that is rejected here rather than discovered in production as
// a board full of half-done claims.
//
//nolint:gocritic // hugeParam: the config is copied into the poller by design
func NewTicketPoller(cfg TicketPollerConfig, board TicketBoard, work TicketWorker, agentName string, agentBudget time.Duration, logger *slog.Logger, opts ...TicketPollerOption) (*TicketPoller, error) {
	if board == nil {
		return nil, ErrPollerNoBoard
	}
	if work == nil {
		return nil, ErrPollerNoWorker
	}
	if strings.TrimSpace(agentName) == "" {
		return nil, ErrPollerNoAgentName
	}
	cfg = cfg.withDefaults()
	if agentBudget > 0 && cfg.TicketTimeout < agentBudget {
		return nil, fmt.Errorf("%w (ticket_timeout=%s agent timeout=%s)", ErrTicketBudgetTooSmall, cfg.TicketTimeout, agentBudget)
	}
	if logger == nil {
		logger = slog.Default()
	}
	p := &TicketPoller{
		cfg:       cfg,
		board:     board,
		work:      work,
		agentName: agentName,
		logger:    logger.With(slog.String("component", "helixon.ticketpoll")),
		reserved:  make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
}

// Stats returns a snapshot of the outcome tally.
func (p *TicketPoller) Stats() TicketPollerStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

// Config returns the effective (defaulted) configuration.
func (p *TicketPoller) Config() TicketPollerConfig {
	return p.cfg
}

// pollOutcome is what one poll cycle achieved, which decides how long to wait
// before the next one.
type pollOutcome int

const (
	// outcomeClaimed: a ticket was claimed and dispatched.
	outcomeClaimed pollOutcome = iota
	// outcomeIdle: the board had nothing for us.
	outcomeIdle
	// outcomeBusy: every concurrency slot is occupied.
	outcomeBusy
	// outcomeError: the board could not be reached or refused the claim.
	outcomeError
)

// Run polls until ctx is cancelled, then waits for in-flight tickets to stop.
// It returns nil on cancellation: shutdown is not a failure.
func (p *TicketPoller) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	slots := make(chan struct{}, p.cfg.MaxConcurrent)
	defer wg.Wait()

	p.logger.Info("ticket poller started",
		slog.String("agent", p.agentName),
		slog.String("status_filter", p.cfg.Status),
		slog.Duration("interval", p.cfg.Interval),
		slog.Int("max_concurrent", p.cfg.MaxConcurrent),
		slog.Duration("ticket_timeout", p.cfg.TicketTimeout),
	)

	backoff := p.cfg.Interval
	for {
		if ctx.Err() != nil {
			return nil
		}
		outcome := p.pollOnce(ctx, slots, &wg)
		wait := p.nextWait(outcome, &backoff, slots)
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				p.logger.Info("ticket poller stopping; draining in-flight tickets")
				return nil
			case <-timer.C:
			}
		}
	}
}

// nextWait converts a poll outcome into the delay before the next poll, and
// updates the idle backoff in place.
//
// A claim resets the backoff and, if a slot is still free, re-polls
// immediately — that is what makes MaxConcurrent > 1 mean anything. It cannot
// spin: each immediate re-poll consumes a slot, and once the slots are gone
// the outcome is Busy and the loop waits.
func (p *TicketPoller) nextWait(outcome pollOutcome, backoff *time.Duration, slots chan struct{}) time.Duration {
	switch outcome {
	case outcomeClaimed:
		*backoff = p.cfg.Interval
		if len(slots) < cap(slots) {
			return 0
		}
		return p.cfg.Interval
	case outcomeBusy:
		return p.cfg.Interval
	case outcomeIdle, outcomeError:
		wait := *backoff
		*backoff = growBackoff(*backoff, p.cfg.MaxBackoff)
		return wait
	default:
		return p.cfg.Interval
	}
}

// growBackoff doubles up to the ceiling. The `next <= 0` arm catches
// Duration overflow, which would otherwise turn a long backoff into an
// instant hot loop.
func growBackoff(current, ceiling time.Duration) time.Duration {
	next := current * 2
	if next > ceiling || next <= 0 {
		return ceiling
	}
	return next
}

// pollOnce runs one search/claim cycle. It takes a concurrency slot BEFORE
// searching so a busy poller does not ask the board for work it cannot take,
// and releases the slot unless a ticket was actually dispatched.
func (p *TicketPoller) pollOnce(ctx context.Context, slots chan struct{}, wg *sync.WaitGroup) pollOutcome {
	select {
	case slots <- struct{}{}:
	default:
		return outcomeBusy
	}
	release := func() { <-slots }

	if !p.hasBudget(ctx) {
		p.logger.Warn("skipping claim: remaining time is shorter than the per-ticket budget",
			slog.Duration("ticket_timeout", p.cfg.TicketTimeout))
		release()
		return outcomeIdle
	}

	p.bump(func(s *TicketPollerStats) { s.Polls++ })
	tickets, err := p.board.SearchTickets(ctx, p.filter())
	if err != nil {
		if ctx.Err() == nil {
			p.bump(func(s *TicketPollerStats) { s.Errors++ })
			p.logger.Warn("ticket search failed", slog.String("error", err.Error()))
		}
		release()
		return outcomeError
	}

	ticket, ok := p.claimNext(ctx, tickets)
	if !ok {
		release()
		return outcomeIdle
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer release()
		defer p.unreserve(ticket.ID)
		p.runTicket(ctx, ticket)
	}()
	return outcomeClaimed
}

// claimNext walks the candidates and returns the first one this agent
// successfully claimed. A lost race (409) is counted and skipped, never
// fatal: another agent doing the work is a good outcome, not an error.
//
//nolint:gocritic // rangeValCopy: the ticket is returned by value to the caller
func (p *TicketPoller) claimNext(ctx context.Context, tickets []controlplane.Ticket) (controlplane.Ticket, bool) {
	for _, t := range tickets {
		if t.ID == "" || !p.reserve(t.ID) {
			continue
		}
		if err := p.board.ClaimTicket(ctx, t.ID); err != nil {
			p.unreserve(t.ID)
			if errors.Is(err, controlplane.ErrClaimConflict) {
				p.bump(func(s *TicketPollerStats) { s.Conflicts++ })
				p.logger.Info("lost claim race", slog.String("ticket", t.ID))
				continue
			}
			if ctx.Err() == nil {
				p.bump(func(s *TicketPollerStats) { s.Errors++ })
				p.logger.Warn("claim failed", slog.String("ticket", t.ID), slog.String("error", err.Error()))
			}
			return controlplane.Ticket{}, false
		}
		p.bump(func(s *TicketPollerStats) { s.Claimed++ })
		p.metrics.TicketClaimed()
		p.logger.Info("claimed ticket", slog.String("ticket", t.ID), slog.String("title", t.Title))
		return t, true
	}
	return controlplane.Ticket{}, false
}

// runTicket executes one claimed ticket under a hard deadline and reports the
// outcome back to the board.
//
//nolint:gocritic // hugeParam: the ticket is a value snapshot handed to a goroutine
func (p *TicketPoller) runTicket(parent context.Context, ticket controlplane.Ticket) {
	ctx, cancel := context.WithTimeout(parent, p.cfg.TicketTimeout)
	defer cancel()

	started := time.Now()
	result, err := p.work(ctx, ticket)

	// Shutdown mid-ticket. The run did not fail and it did not succeed; it
	// was interrupted by us. Writing either verdict to a durable board would
	// be a fabrication, so the ticket is left claimed for the next start (or
	// a human) and the fact is logged.
	//
	// The duration histogram is not observed either, for the same reason: an
	// interrupted run has no terminal outcome, and filing it under one would
	// make a fleet restart look like a wave of fast completions.
	if parent.Err() != nil {
		p.bump(func(s *TicketPollerStats) { s.Abandoned++ })
		p.logger.Warn("shutdown during ticket; left claimed and unreported",
			slog.String("ticket", ticket.ID))
		return
	}

	// A detached context: the parent is alive, but the per-ticket deadline may
	// have fired, and the report must still get out.
	reportCtx, reportCancel := context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
	defer reportCancel()

	if err != nil {
		p.escalate(reportCtx, ticket, err, result)
		p.metrics.ObserveRunDuration(agentmetrics.RunEscalated, time.Since(started))
		return
	}
	evidence := truncateEvidence(result, maxEvidenceBytes)
	if evidence == "" {
		// An empty final message is not evidence. Treat it the same way the
		// completion gate treats a missing verifier verdict: escalate.
		p.escalate(reportCtx, ticket, errors.New("agent produced no final output"), "")
		p.metrics.ObserveRunDuration(agentmetrics.RunEscalated, time.Since(started))
		return
	}
	if cerr := p.board.CompleteTicket(reportCtx, ticket.ID, evidence); cerr != nil {
		p.bump(func(s *TicketPollerStats) { s.Errors++ })
		p.logger.Error("ticket completed but the board rejected the report",
			slog.String("ticket", ticket.ID), slog.String("error", cerr.Error()))
		return
	}
	p.bump(func(s *TicketPollerStats) { s.Completed++ })
	p.metrics.TicketCompleted()
	p.metrics.ObserveRunDuration(agentmetrics.RunCompleted, time.Since(started))
	p.logger.Info("ticket completed", slog.String("ticket", ticket.ID))
}

// escalate posts the failure evidence as a comment and leaves the ticket
// claimed and un-completed. The board has no status-patch route, so a comment
// is the only escalation surface it offers; the alternative — completing with
// the error as "evidence" — is exactly the false-green the verifier gate
// exists to prevent.
//
//nolint:gocritic // hugeParam: see runTicket
func (p *TicketPoller) escalate(ctx context.Context, ticket controlplane.Ticket, cause error, partial string) {
	p.holdEscalated(ticket.ID)
	p.bump(func(s *TicketPollerStats) { s.Escalated++ })
	// Counted BEFORE the comment is attempted. An escalation whose comment
	// fails to post is the worst case in this whole file — the ticket is stuck
	// and no human has been told — so it must be the case the counter is most
	// certain to catch, not the one it misses.
	p.metrics.Escalated(EscalationReason(cause))
	body := EscalationComment(p.agentName, cause, partial)
	if err := p.board.AddComment(ctx, ticket.ID, p.agentName, body); err != nil {
		p.bump(func(s *TicketPollerStats) { s.Errors++ })
		p.logger.Error("escalation comment failed; ticket is stuck with no human-visible reason",
			slog.String("ticket", ticket.ID), slog.String("error", err.Error()))
		return
	}
	p.logger.Warn("ticket escalated to a human and NOT completed",
		slog.String("ticket", ticket.ID), slog.String("cause", cause.Error()))
}

// EscalationComment renders the human-facing escalation text.
//
// It is exported because task mode needs the same words. `helixon task` used
// to answer a failed run by calling CompleteTicket with "error: ..." as the
// evidence, which marks a ticket DONE on the board because the work failed —
// the exact false-green the poller was built to refuse. There is one
// escalation vocabulary now, and both entry points render it from here rather
// than each inventing its own way to describe a failure.
func EscalationComment(agentName string, cause error, partial string) string {
	var b strings.Builder
	b.WriteString("Automated escalation from ")
	b.WriteString(agentName)
	b.WriteString(".\n\nThis ticket was claimed and executed but NOT completed: ")
	switch {
	case errors.Is(cause, agent.ErrNeedsHumanApproval):
		b.WriteString("the verifier failed repeatedly, so the run stopped for a human rather than retrying.")
	case errors.Is(cause, agent.ErrNoVerifierEvidence):
		b.WriteString("the run changed state but produced no passing verifier evidence.")
	default:
		b.WriteString("the run did not finish successfully.")
	}
	b.WriteString("\n\nFailure: ")
	b.WriteString(cause.Error())
	if trimmed := truncateEvidence(partial, maxEvidenceBytes); trimmed != "" {
		b.WriteString("\n\nLast agent output:\n")
		b.WriteString(trimmed)
	}
	b.WriteString("\n\nThe ticket remains claimed by this agent and will not be retried automatically.")
	return b.String()
}

// EscalationReason classifies a run failure into the frozen `reason` label
// domain of hlxn_agent_escalations_total.
//
// It deliberately mirrors the branches of EscalationComment: the words a human
// reads on the board and the label an alert routes on must describe the same
// three situations, or the counter that pages someone will disagree with the
// comment that person then goes and reads.
//
// The two verifier stop conditions collapse into one reason on purpose. "The
// checks kept failing" and "the run changed state and never proved anything"
// are one operational fact — the gate refused to call this done — and an
// operator does the same thing about both.
func EscalationReason(cause error) string {
	switch {
	case errors.Is(cause, agent.ErrNeedsHumanApproval), errors.Is(cause, agent.ErrNoVerifierEvidence):
		return agentmetrics.ReasonVerifierFailed
	case errors.Is(cause, agent.ErrBudgetExhaust):
		return agentmetrics.ReasonBudgetExhausted
	default:
		return agentmetrics.ReasonRunError
	}
}

// TicketMemorySummary renders what loop memory records about a finished ticket.
//
// The shape follows the precedent already in the tree — `helixon task`'s Engram
// persistence writes "Agent X executed task. Prompt: ... Result: ..." — with the
// ticket identified and the OUTCOME stated. Recording only the successes would
// build a memory that believes everything works.
//
// Returns "" when there is nothing worth writing, so the caller has one thing
// to check rather than a policy to re-derive.
//
//nolint:gocritic // hugeParam: the ticket is a value snapshot, see runTicket
func TicketMemorySummary(agentName string, ticket controlplane.Ticket, result string, runErr error) string {
	outcome := "completed"
	if runErr != nil {
		outcome = "FAILED: " + runErr.Error()
	}
	body := strings.TrimSpace(result)
	if body == "" && runErr == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Agent %s executed SprintBoard ticket %s (%s). Outcome: %s.",
		agentName, ticket.ID, truncateEvidence(ticket.Title, memorySummaryTitleBytes), outcome)
	if body != "" {
		b.WriteString(" Result: ")
		b.WriteString(truncateEvidence(body, memorySummaryResultBytes))
	}
	return b.String()
}

// Bounds on what reaches the memory store. Agent output is model-controlled and
// unbounded; a vector store's embedding call is not free.
const (
	memorySummaryTitleBytes  = 200
	memorySummaryResultBytes = 1000
)

// truncateEvidence bounds model-controlled text before it is written to the board.
func truncateEvidence(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n...[truncated]"
}

// filter builds the board search query.
func (p *TicketPoller) filter() controlplane.TicketFilter {
	return controlplane.TicketFilter{
		Status:      p.cfg.Status,
		SprintID:    p.cfg.SprintID,
		Labels:      p.cfg.Labels,
		PriorityMin: p.cfg.PriorityMin,
		Limit:       p.cfg.Limit,
	}
}

// hasBudget reports whether there is enough time left to finish a ticket.
// Claiming work the agent provably cannot finish is how a board fills with
// abandoned in-progress rows.
func (p *TicketPoller) hasBudget(ctx context.Context) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(deadline) >= p.cfg.TicketTimeout
}

// reserve claims an in-process lock on a ticket ID, so two workers cannot
// race for the same ticket and an escalated ticket is never picked up again
// by this process.
func (p *TicketPoller) reserve(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, taken := p.reserved[id]; taken {
		return false
	}
	p.reserved[id] = struct{}{}
	return true
}

func (p *TicketPoller) unreserve(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, held := p.escalatedIDs()[id]; held {
		return
	}
	delete(p.reserved, id)
}

// holdEscalated converts the reservation into a permanent one for this
// process, so the ticket is not immediately re-claimed and re-failed.
func (p *TicketPoller) holdEscalated(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.escalated == nil {
		p.escalated = make(map[string]struct{})
	}
	p.escalated[id] = struct{}{}
	p.reserved[id] = struct{}{}
}

func (p *TicketPoller) escalatedIDs() map[string]struct{} {
	if p.escalated == nil {
		return map[string]struct{}{}
	}
	return p.escalated
}

func (p *TicketPoller) bump(f func(*TicketPollerStats)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(&p.stats)
}

// TicketPrompt renders the instruction handed to the agent loop for a ticket.
// It is a package-level function so a test can assert the ticket's acceptance
// criteria actually reach the model rather than being dropped on the floor.
//
//nolint:gocritic // hugeParam: a prompt builder takes the ticket by value
func TicketPrompt(t controlplane.Ticket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Execute SprintBoard ticket %s.\n\nTitle: %s\n", t.ID, t.Title)
	if t.Description != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", t.Description)
	}
	if t.AcceptanceCriteria != "" {
		fmt.Fprintf(&b, "\nAcceptance criteria:\n%s\n", t.AcceptanceCriteria)
	}
	b.WriteString("\nUse the available tools to do the work, then run the verifier to prove it. " +
		"Report what you changed and the verifier result.")
	return b.String()
}
