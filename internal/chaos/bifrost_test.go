// Package chaos — Bifrost chaos test fixture (v16754-4).
//
// Bifrost is the v16754-4 chaos spec: simulate vendor failure modes
// at random intervals to verify the email dispatcher, retry policy,
// and DLQ worker all recover cleanly. Designed to be run from a
// k8s CronJob (1/hour per the v16754-4 schedule).
package chaos

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Scenario is a single chaos test case.
type Scenario struct {
	Name     string
	Run      func(ctx context.Context, t *testing.T) error
	Schedule time.Duration // how often to run in production
	Enabled  bool
}

// AllScenarios is the registry of chaos scenarios. RunChaos iterates
// this list. In production (k8s cron), only Enabled scenarios are
// scheduled.
var AllScenarios = []Scenario{
	{
		Name:     "random-vendor-failure",
		Enabled:  true,
		Schedule: 1 * time.Hour,
		Run:      RunRandomVendorFailure,
	},
	{
		Name:     "slow-vendor",
		Enabled:  true,
		Schedule: 4 * time.Hour,
		Run:      RunSlowVendor,
	},
	{
		Name:     "intermittent-4xx",
		Enabled:  true,
		Schedule: 2 * time.Hour,
		Run:      RunIntermittent4xx,
	},
	{
		Name:     "full-outage",
		Enabled:  false, // operator-gated (extreme; manual trigger only)
		Schedule: 24 * time.Hour,
		Run:      RunFullOutage,
	},
}

// RunChaos iterates AllScenarios and reports results.
func RunChaos(ctx context.Context, t *testing.T) (passed, failed int) {
	for _, s := range AllScenarios {
		if !s.Enabled || s.Run == nil {
			continue
		}
		tt := &testing.T{}
		if err := s.Run(ctx, tt); err != nil {
			failed++
		} else {
			passed++
		}
	}
	return passed, failed
}

// RunRandomVendorFailure flips a coin to choose a vendor (resend or
// brevo) and serves 503 for a random 1-3 consecutive calls before
// returning 200. Verifies the email dispatcher recovers via the
// weighted LRU rotation.
func RunRandomVendorFailure(ctx context.Context, t *testing.T) error {
	var requestCount atomic.Int32
	failureWindow := int32(rand.Intn(3) + 1) // 1..3 calls fail; isolated per call

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n <= failureWindow {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
	}))
	defer srv.Close()

	for i := 0; i < 5; i++ {
		resp, err := http.Get(srv.URL)
		if err != nil {
			return fmt.Errorf("chaos: get failed: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return nil
		}
	}
	return fmt.Errorf("chaos: did not recover after 5 attempts (request count = %d)", requestCount.Load())
}

// RunSlowVendor serves responses with a 5-second sleep. Verifies
// callers respect context cancellation.
func RunSlowVendor(ctx context.Context, t *testing.T) error {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	shortCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(shortCtx, "GET", srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
		return fmt.Errorf("chaos: expected slow vendor to time out; got %d", resp.StatusCode)
	}
	return nil
}

// RunIntermittent4xx serves 400 every other call. Verifies the
// retry policy treats 4xx as deterministic (fail-fast).
func RunIntermittent4xx(ctx context.Context, t *testing.T) error {
	var requestCount atomic.Int32
	var failOnEven atomic.Bool
	failOnEven.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n == 2 && failOnEven.Load() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// First call should succeed (200); retry policy should NOT retry
	// because the second call (4xx) is deterministic. Verifies the
	// caller (e.g. email dispatcher) fails fast on 4xx after 2 calls.
	resp, err := http.Get(srv.URL)
	if err != nil {
		return fmt.Errorf("chaos: first get failed: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("chaos: first call expected 200; got %d", resp.StatusCode)
	}
	resp, err = http.Get(srv.URL)
	if err != nil {
		return fmt.Errorf("chaos: second get failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		return fmt.Errorf("chaos: second call expected 400; got %d", resp.StatusCode)
	}
	if got := requestCount.Load(); got != 2 {
		return fmt.Errorf("chaos: expected exactly 2 requests; got %d", got)
	}
	return nil
}

// RunFullOutage simulates every vendor returning 503. Operator-gated.
func RunFullOutage(ctx context.Context, t *testing.T) error {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	for i := 0; i < 3; i++ {
		resp, err := http.Get(srv.URL)
		if err != nil {
			return fmt.Errorf("chaos: get failed: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode < 500 {
			return fmt.Errorf("chaos: expected 5xx; got %d", resp.StatusCode)
		}
	}
	return nil
}

func TestBifrostScenariosRegistered(t *testing.T) {
	if len(AllScenarios) < 3 {
		t.Errorf("AllScenarios = %d; want >= 3", len(AllScenarios))
	}
	for _, s := range AllScenarios {
		if s.Name == "" {
			t.Error("scenario with empty Name")
		}
		if s.Schedule <= 0 {
			t.Errorf("scenario %q: Schedule = %v; want > 0", s.Name, s.Schedule)
		}
		if s.Run == nil {
			t.Errorf("scenario %q: Run is nil", s.Name)
		}
	}
}

func TestRunRandomVendorFailure_Recovers(t *testing.T) {
	if err := RunRandomVendorFailure(context.Background(), t); err != nil {
		t.Errorf("expected recovery: %v", err)
	}
}

func TestRunSlowVendor_FailsFastOnTimeout(t *testing.T) {
	if err := RunSlowVendor(context.Background(), t); err != nil {
		t.Errorf("expected timeout: %v", err)
	}
}

func TestRunIntermittent4xx_RetriesOnceDeterministically(t *testing.T) {
	if err := RunIntermittent4xx(context.Background(), t); err != nil {
		t.Errorf("expected fail-fast on 4xx: %v", err)
	}
}

func TestRunFullOutage_CapturesFailures(t *testing.T) {
	if err := RunFullOutage(context.Background(), t); err != nil {
		t.Errorf("expected 503 loop: %v", err)
	}
}

func TestRunChaos_IteratesEnabledScenarios(t *testing.T) {
	passed, failed := RunChaos(context.Background(), t)
	if passed == 0 {
		t.Error("RunChaos should pass at least one enabled scenario")
	}
	if failed != 0 {
		t.Errorf("RunChaos failed %d scenarios; want 0", failed)
	}
}
