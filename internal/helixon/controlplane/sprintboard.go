// Package controlplane holds the clients that talk to the Helixon control
// plane services.
//
// SprintboardClient in this file is THE SprintBoard client. It speaks the
// dialect the live server at :9400 actually routes:
//
//	POST /api/v1/agents
//	GET  /api/v1/tickets/search
//	POST /api/v1/tickets/{id}/claim
//	POST /api/v1/tickets/{id}/complete
//	POST /api/v1/tickets/{id}/comments
//
// v18779 deleted a second, older client (internal/helixon/sprintboard.go)
// that targeted /api/v1/agents/register, /api/v1/agents/heartbeat and a flat
// /api/v1/tickets/claim. None of those routes exist; every call it made 404'd
// against the running board, and its tests passed only because they asserted
// against an httptest server that mirrored the wrong paths back. A client that
// is green in CI and 404s in production is worse than no client, so it is
// gone rather than deprecated: there is now exactly one thing to pick.
package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/leaselost"
)

// DefaultSprintboardURL is the live board's address. The previous default,
// http://localhost:8585, pointed at nothing in this deployment: an operator
// who omitted sprintboard.url got a client that silently failed every call.
const DefaultSprintboardURL = "http://127.0.0.1:9400"

// ErrClaimConflict reports that another agent already owns the ticket. It is
// an ordinary outcome of a poll race, not a failure: the caller moves on to
// the next ticket rather than treating it as an error worth stopping for.
var ErrClaimConflict = errors.New("controlplane: ticket already claimed by another agent")

// ErrTicketNotClaimedBy reports that the renew route answered 409: the claim
// lease was swept or taken by another agent. It is the one renew outcome that
// is definitive — the caller no longer owns the ticket, so continuing the run
// or reporting on it writes someone else's ticket. A board that predates the
// renew route answers 404, which stays a generic error and best-effort.
var ErrTicketNotClaimedBy = errors.New("controlplane: ticket not claimed by this agent")

// SprintboardConfig configures the sprintboard auto-registration.
type SprintboardConfig struct {
	BaseURL      string
	AgentName    string
	Capabilities string
	TenantID     string // optional: stamped on every outbound payload (v18685-1)
	// Token is the shared bearer the board expects once its auth mode is
	// required (v18836). Empty sends no Authorization header, which the board
	// accepts only while it is still in bootstrap mode.
	Token  string
	Logger *slog.Logger
}

func (c SprintboardConfig) withDefaults() SprintboardConfig {
	if c.BaseURL == "" {
		c.BaseURL = DefaultSprintboardURL
	}
	if c.AgentName == "" {
		c.AgentName = "helixon-agent"
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// SprintboardClient handles sprintboard registration and ticket operations.
type SprintboardClient struct {
	cfg    SprintboardConfig
	http   *http.Client
	logger *slog.Logger
}

// NewSprintboardClient creates a client for the sprintboard API.
func NewSprintboardClient(cfg SprintboardConfig, logger *slog.Logger) *SprintboardClient {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	return &SprintboardClient{
		cfg:    cfg,
		http:   &http.Client{Timeout: 10 * time.Second},
		logger: logger.With(slog.String("component", "helixon.controlplane.sprintboard")),
	}
}

// newRequest builds every request this client sends, so the bearer and the
// content type cannot be remembered on one verb and forgotten on another.
func (c *SprintboardClient) newRequest(ctx context.Context, method, target string, body []byte) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	return req, nil
}

// AgentRegistration is the payload for auto-registration.
type AgentRegistration struct {
	AgentID      string `json:"agent_id"`
	TenantID     string `json:"tenant_id,omitempty"`
	Capabilities string `json:"capabilities"`
	Status       string `json:"status"`
	RegisteredAt string `json:"registered_at"`
}

// Register auto-registers this agent with the sprintboard on startup.
func (c *SprintboardClient) Register(ctx context.Context) error {
	reg := AgentRegistration{
		AgentID:      c.cfg.AgentName,
		TenantID:     c.cfg.TenantID,
		Capabilities: c.cfg.Capabilities,
		Status:       "active",
		RegisteredAt: time.Now().UTC().Format(time.RFC3339),
	}

	data, _ := json.Marshal(reg)
	if _, _, err := c.doPostChecked(ctx, "/api/v1/agents", data); err != nil {
		return fmt.Errorf("sprintboard register: %w", err)
	}

	c.logger.Info("registered with sprintboard",
		slog.String("agent", c.cfg.AgentName),
		slog.String("url", c.cfg.BaseURL),
	)
	return nil
}

// Ticket mirrors the fields the live board returns from
// GET /api/v1/tickets/search. Unlisted fields are ignored on decode.
type Ticket struct {
	ID                 string   `json:"id"`
	SprintID           string   `json:"sprint_id,omitempty"`
	Title              string   `json:"title"`
	Description        string   `json:"description,omitempty"`
	Status             string   `json:"status"`
	OwnerAgent         string   `json:"owner_agent,omitempty"`
	Priority           int      `json:"priority"`
	AcceptanceCriteria string   `json:"acceptance_criteria,omitempty"`
	Labels             []string `json:"labels,omitempty"`
	ClaimedBy          string   `json:"claimed_by,omitempty"`
}

// TicketFilter is the query for GET /api/v1/tickets/search. Zero-valued
// fields are omitted from the query string.
type TicketFilter struct {
	Query       string
	Status      string
	Owner       string
	SprintID    string
	Labels      []string
	PriorityMin int
	Limit       int
}

func (f TicketFilter) values() url.Values {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("q", f.Query)
	set("status", f.Status)
	set("owner", f.Owner)
	set("sprint_id", f.SprintID)
	for _, l := range f.Labels {
		if l != "" {
			q.Add("label", l)
		}
	}
	if f.PriorityMin > 0 {
		q.Set("priority_min", strconv.Itoa(f.PriorityMin))
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	return q
}

// SearchTickets returns the tickets matching the filter. This is the only
// discovery route the board exposes; there is no "next ready ticket"
// endpoint, so the caller filters and orders what comes back.
func (c *SprintboardClient) SearchTickets(ctx context.Context, filter TicketFilter) ([]Ticket, error) {
	target := c.cfg.BaseURL + "/api/v1/tickets/search"
	if q := filter.values().Encode(); q != "" {
		target += "?" + q
	}
	req, err := c.newRequest(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sprintboard search: %w", err)
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("sprintboard search: error %d: %s", resp.StatusCode, string(data))
	}
	var payload struct {
		Tickets []Ticket `json:"tickets"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("sprintboard search: decode: %w", err)
	}
	return payload.Tickets, nil
}

// claimResult is the board's reply to a claim attempt. The board answers a
// lost race with 409 AND success=false; both are treated as a conflict, so a
// server that changes its mind about the status code cannot turn a lost race
// into a silent double-claim.
type claimResult struct {
	Success   bool   `json:"success"`
	TicketID  string `json:"ticket_id"`
	ClaimedBy string `json:"claimed_by"`
}

// ClaimTicket atomically claims a ticket for this agent. It returns
// ErrClaimConflict (wrapped) when another agent won the race.
func (c *SprintboardClient) ClaimTicket(ctx context.Context, ticketID string) error {
	data, _ := json.Marshal(map[string]string{
		"agent_id":  c.cfg.AgentName,
		"tenant_id": c.cfg.TenantID,
	})
	path := fmt.Sprintf("/api/v1/tickets/%s/claim", ticketID)
	status, body, err := c.doPost(ctx, path, data)
	if err != nil {
		return fmt.Errorf("sprintboard claim %s: %w", ticketID, err)
	}
	if status == http.StatusConflict {
		return fmt.Errorf("sprintboard claim %s: %w (held by %q)", ticketID, ErrClaimConflict, claimHolder(body))
	}
	if status >= 400 {
		return fmt.Errorf("sprintboard claim %s: error %d: %s", ticketID, status, string(body))
	}
	var res claimResult
	if err := json.Unmarshal(body, &res); err == nil && !res.Success && res.ClaimedBy != "" && res.ClaimedBy != c.cfg.AgentName {
		return fmt.Errorf("sprintboard claim %s: %w (held by %q)", ticketID, ErrClaimConflict, res.ClaimedBy)
	}
	return nil
}

// claimHolder extracts the winning agent from a conflict body, best effort.
func claimHolder(body []byte) string {
	var res claimResult
	if err := json.Unmarshal(body, &res); err != nil {
		return "unknown"
	}
	if res.ClaimedBy == "" {
		return "unknown"
	}
	return res.ClaimedBy
}

// RenewClaim extends the claim lease on a ticket this agent already holds
// (v18860-1). The board refreshes claimed_at so the stale-claim sweeper does
// not release a ticket out from under a long run — which v18855's in-place
// infra retry deliberately makes possible (a bounded retry chain can hold a
// claim well past any fixed staleness window). A 404 means the running board
// predates the renew route: the poller logs and carries on, so renewal is
// best-effort until every board deployment is current. A 409 is definitive
// and is returned as ErrTicketNotClaimedBy (classified by
// internal/helixon/leaselost): the lease was swept or taken, and the caller
// must cancel the run rather than renew it again.
func (c *SprintboardClient) RenewClaim(ctx context.Context, ticketID string) error {
	data, _ := json.Marshal(map[string]string{"agent_id": c.cfg.AgentName})
	path := fmt.Sprintf("/api/v1/tickets/%s/renew", ticketID)
	status, body, err := c.doPost(ctx, path, data)
	if err != nil {
		return fmt.Errorf("sprintboard renew %s: %w", ticketID, err)
	}
	if leaselost.Classify(status, nil) == leaselost.LeaseLost {
		return fmt.Errorf("sprintboard renew %s: error %d: %s: %w", ticketID, status, string(body), ErrTicketNotClaimedBy)
	}
	if status >= 400 {
		return fmt.Errorf("sprintboard renew %s: error %d: %s", ticketID, status, string(body))
	}
	return nil
}

// CompleteTicket marks a ticket as completed with evidence.
func (c *SprintboardClient) CompleteTicket(ctx context.Context, ticketID, evidence string) error {
	data, _ := json.Marshal(map[string]string{
		"agent_id": c.cfg.AgentName,
		"evidence": evidence,
	})
	path := fmt.Sprintf("/api/v1/tickets/%s/complete", ticketID)
	_, _, err := c.doPostChecked(ctx, path, data)
	return err
}

// AddComment posts a comment on a ticket. The board has no status-patch
// route, so a comment is the only way an agent can hand work back to a human
// with the reason attached.
func (c *SprintboardClient) AddComment(ctx context.Context, ticketID, author, body string) error {
	if author == "" || body == "" {
		return errors.New("sprintboard comment: author and body are required")
	}
	data, _ := json.Marshal(map[string]string{"author": author, "body": body})
	path := fmt.Sprintf("/api/v1/tickets/%s/comments", ticketID)
	_, _, err := c.doPostChecked(ctx, path, data)
	return err
}

// SprintStatus returns the current sprint status.
func (c *SprintboardClient) SprintStatus(ctx context.Context, sprintID string) (map[string]any, error) {
	req, err := c.newRequest(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/sprints/%s", c.cfg.BaseURL, sprintID), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	_ = resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("sprintboard error %d: %s", resp.StatusCode, string(data))
	}

	var status map[string]any
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("decode sprint status: %w", err)
	}
	return status, nil
}

// doPost performs the request and returns the status code and body WITHOUT
// deciding whether the status is an error. ClaimTicket needs to tell 409
// ("someone else got there first", routine) apart from 500 ("the board is
// broken"), and it cannot do that once the transport has flattened both into
// one generic error string.
func (c *SprintboardClient) doPost(ctx context.Context, path string, body []byte) (int, []byte, error) {
	req, err := c.newRequest(ctx, http.MethodPost, c.cfg.BaseURL+path, body)
	if err != nil {
		return 0, nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	_ = resp.Body.Close()
	return resp.StatusCode, data, nil
}

// doPostChecked is doPost for the callers that want any 4xx/5xx to be an error.
//
//nolint:unparam // the status is part of the contract even where callers discard it.
func (c *SprintboardClient) doPostChecked(ctx context.Context, path string, body []byte) (int, []byte, error) {
	status, data, err := c.doPost(ctx, path, body)
	if err != nil {
		return status, data, err
	}
	if status >= 400 {
		return status, data, fmt.Errorf("sprintboard error %d: %s", status, string(data))
	}
	return status, data, nil
}
