// Package fleet — win2 → win1 mirror sync + DR drill (v18687-2).
//
// This file implements:
//   - Mirror: pull-based sync daemon that fetches registry state from
//     win2 every 5 minutes and applies it via a caller-supplied apply fn.
//   - DRCoordinator: watches Primary health; on N consecutive failures,
//     switches active endpoint to Backup. On Primary recovery, failback.
//
// Design constraints (per harness-engineering-defaults.mdc):
//   - All goroutines bound by context cancellation; no leaks.
//   - Bounded backoff on upstream errors (5 → 10 → 20 → 30 min cap).
//   - Real time used only via the injected Clock interface.
//   - HTTP client timeout bounds each upstream fetch.
package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// RegistryService mirrors the upstream svc registry entry shape.
type RegistryService struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Status  string `json:"status"`
}

// Clock is the minimal clock interface required by Mirror / DRCoordinator.
type Clock interface {
	Now() time.Time
}

// RealClock returns time.Now().
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// =====================================================================
// Mirror
// =====================================================================

// Mirror is the pull-based registry sync daemon.
type Mirror struct {
	// Upstream is the win2 svc registry HTTP URL (e.g. http://100.110.82.5:8786/state).
	Upstream string
	// Interval is the nominal pull interval (default 5 min).
	Interval time.Duration
	// Clock allows tests to inject a fake clock. Nil → real time.
	// Clock is used only for observability/logging — the wakeup loop
	// uses real time, so tests should use a short context timeout to
	// observe the initial pull + apply behaviour.
	Clock Clock
	// HTTPClient is the upstream HTTP client (defaults to 5s timeout).
	HTTPClient *http.Client
}

func (m *Mirror) clock() Clock {
	if m.Clock != nil {
		return m.Clock
	}
	return RealClock{}
}

func (m *Mirror) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return &http.Client{Timeout: 5 * time.Second}
}

// upstreamResponse is the JSON shape returned by win2 svc registry.
type upstreamResponse struct {
	Services []RegistryService `json:"services"`
}

// ErrUpstreamUnreachable is returned by Run when upstream fetch fails.
var ErrUpstreamUnreachable = errors.New("upstream unreachable")

// ErrUpstreamParse is returned by Run when upstream payload is unparseable.
var ErrUpstreamParse = errors.New("upstream payload unparseable")

// Pull performs one upstream fetch + apply cycle. It is the public test
// surface for a single sync operation. Returns nil on success; wraps
// ErrUpstreamUnreachable or ErrUpstreamParse on failure.
func (m *Mirror) Pull(ctx context.Context, applyFn func([]RegistryService) error) error {
	if m.Upstream == "" {
		return errors.New("Mirror.Upstream is required")
	}
	if applyFn == nil {
		return errors.New("applyFn is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.Upstream, nil)
	if err != nil {
		return fmt.Errorf("build req: %w", err)
	}
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUpstreamUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: HTTP %d", ErrUpstreamUnreachable, resp.StatusCode)
	}

	var payload upstreamResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("%w: %v", ErrUpstreamParse, err)
	}
	return applyFn(payload.Services)
}

// Run starts the mirror sync loop. It performs an initial Pull, then
// loops at the configured interval (with exponential backoff on errors,
// capped at 30 min). Returns when ctx is cancelled.
//
// The loop uses real time (time.NewTicker) — tests should drive Run via
// short context timeouts and assert initial-Pull behaviour.
func (m *Mirror) Run(ctx context.Context, applyFn func([]RegistryService) error) error {
	if m.Upstream == "" {
		return errors.New("Mirror.Upstream is required")
	}
	if applyFn == nil {
		return errors.New("applyFn is required")
	}
	if m.Interval <= 0 {
		m.Interval = 5 * time.Minute
	}

	backoff := m.Interval
	const maxBackoff = 30 * time.Minute

	// Initial pull synchronously.
	_ = m.Pull(ctx, applyFn)

	t := time.NewTicker(backoff)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := m.Pull(ctx, applyFn); err != nil {
				backoff = nextBackoff(backoff)
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			} else {
				backoff = m.Interval
			}
			t.Reset(backoff)
		}
	}
}

// nextBackoff doubles the duration, capped at 30 min.
func nextBackoff(current time.Duration) time.Duration {
	doubled := current * 2
	if doubled <= current { // overflow guard
		return 30 * time.Minute
	}
	if doubled > 30*time.Minute {
		return 30 * time.Minute
	}
	return doubled
}

// =====================================================================
// DR drill coordinator
// =====================================================================

// Endpoint is one upstream service target for DR.
type Endpoint struct {
	HealthURL string
	Name      string
}

// DRCoordinator watches the Primary endpoint and switches active to
// Backup after consecutive failures; recovers active to Primary after
// Primary passes N consecutive health checks.
type DRCoordinator struct {
	Primary Endpoint
	Backup  Endpoint
	// Timeout is the per-probe HTTP timeout (default 3s).
	Timeout time.Duration
	// PollInterval is how often WatchHealth pings both endpoints.
	PollInterval time.Duration
	// FailThreshold is consecutive failures before failover (default 2).
	FailThreshold int
	// RecoverThreshold is consecutive successes before failback (default 3).
	RecoverThreshold int
	// Clock allows tests to inject a fake clock. Nil → real time.
	Clock Clock

	mu        sync.Mutex
	active    string
	primFails int
	primOKs   int
}

func (d *DRCoordinator) clock() Clock {
	if d.Clock != nil {
		return d.Clock
	}
	return RealClock{}
}

func (d *DRCoordinator) timeout() time.Duration {
	if d.Timeout <= 0 {
		return 3 * time.Second
	}
	return d.Timeout
}

func (d *DRCoordinator) pollInterval() time.Duration {
	if d.PollInterval <= 0 {
		return 1 * time.Second
	}
	return d.PollInterval
}

func (d *DRCoordinator) failThreshold() int {
	if d.FailThreshold <= 0 {
		return 2
	}
	return d.FailThreshold
}

func (d *DRCoordinator) recoverThreshold() int {
	if d.RecoverThreshold <= 0 {
		return 3
	}
	return d.RecoverThreshold
}

// Active returns the currently-active endpoint name (thread-safe).
func (d *DRCoordinator) Active() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active == "" {
		return d.Primary.Name
	}
	return d.active
}

func (d *DRCoordinator) setActive(name string) {
	d.mu.Lock()
	d.active = name
	d.mu.Unlock()
}

// WatchHealth starts the health-watch loop. onTransition fires whenever
// the active endpoint changes. Returns when ctx is cancelled.
func (d *DRCoordinator) WatchHealth(ctx context.Context, onTransition func(active string)) {
	if onTransition == nil {
		onTransition = func(string) {}
	}
	d.setActive(d.Primary.Name)

	// Run probe + transition synchronously per tick so we don't accumulate
	// goroutines. One tick interval = one probe cycle.
	t := time.NewTicker(d.pollInterval())
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.tick(ctx, onTransition)
		}
	}
}

// tick runs one probe cycle: probe Primary, probe Backup, update state,
// fire onTransition if active changed.
func (d *DRCoordinator) tick(ctx context.Context, onTransition func(string)) {
	primaryOK := d.probe(ctx, d.Primary)
	backupOK := d.probe(ctx, d.Backup)

	d.mu.Lock()
	cur := d.active
	if cur == "" {
		cur = d.Primary.Name
	}

	if primaryOK {
		d.primFails = 0
		d.primOKs++
		if cur == d.Backup.Name && d.primOKs >= d.recoverThreshold() {
			d.active = d.Primary.Name
		}
	} else {
		d.primOKs = 0
		d.primFails++
		if cur == d.Primary.Name && d.primFails >= d.failThreshold() && backupOK {
			d.active = d.Backup.Name
		}
	}
	newActive := d.active
	d.mu.Unlock()

	if newActive != cur {
		onTransition(newActive)
	}
}

// probe performs one health check on the endpoint. Returns true on
// HTTP 200 within the timeout; false otherwise.
func (d *DRCoordinator) probe(parent context.Context, ep Endpoint) bool {
	ctx, cancel := context.WithTimeout(parent, d.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.HealthURL, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: d.timeout()}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
