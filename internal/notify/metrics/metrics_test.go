// Tests for the notify metrics package. v17409-6 TDD coverage.
package metrics_test

import (
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
	"go.opentelemetry.io/otel/metric/noop"
)

func TestRegistry_IncSend(t *testing.T) {
	r := metrics.NewRegistry(noop.NewMeterProvider().Meter("t"))
	r.IncSend(metrics.VendorResend, metrics.StatusSuccess)
	r.IncSend(metrics.VendorResend, metrics.StatusSuccess)
	r.IncSend(metrics.VendorResend, metrics.StatusBadRequest)
	if got := r.StatusFor(metrics.VendorResend, metrics.StatusSuccess); got != 2 {
		t.Errorf("StatusFor success: want 2, got %d", got)
	}
	if got := r.StatusFor(metrics.VendorResend, metrics.StatusBadRequest); got != 1 {
		t.Errorf("StatusFor bad_request: want 1, got %d", got)
	}
}

func TestRegistry_IncAttempt(t *testing.T) {
	r := metrics.NewRegistry(noop.NewMeterProvider().Meter("t"))
	r.IncAttempt(metrics.VendorResend)
	r.IncAttempt(metrics.VendorResend)
	r.IncAttempt(metrics.VendorBrevo)
	snap := r.Snapshot()
	if snap.Attempts[metrics.AttemptKey{Vendor: metrics.VendorResend}] != 2 {
		t.Errorf("attempts resend: want 2, got %d", snap.Attempts[metrics.AttemptKey{Vendor: metrics.VendorResend}])
	}
	if snap.Attempts[metrics.AttemptKey{Vendor: metrics.VendorBrevo}] != 1 {
		t.Errorf("attempts brevo: want 1, got %d", snap.Attempts[metrics.AttemptKey{Vendor: metrics.VendorBrevo}])
	}
}

func TestRegistry_ObserveSend(t *testing.T) {
	r := metrics.NewRegistry(noop.NewMeterProvider().Meter("t"))
	r.ObserveSend(metrics.VendorBrevo, metrics.StatusSuccess, 100)
	r.ObserveSend(metrics.VendorBrevo, metrics.StatusSuccess, 250)
	snap := r.Snapshot()
	dur := snap.SendDur[metrics.SendKey{Vendor: metrics.VendorBrevo, Status: metrics.StatusSuccess}]
	if dur != 350 {
		t.Errorf("duration sum: want 350, got %v", dur)
	}
}

func TestRegistry_TotalByVendor(t *testing.T) {
	r := metrics.NewRegistry(noop.NewMeterProvider().Meter("t"))
	r.IncSend(metrics.VendorResend, metrics.StatusSuccess)
	r.IncSend(metrics.VendorResend, metrics.StatusBadRequest)
	r.IncSend(metrics.VendorResend, metrics.StatusDeadLetter)
	r.IncSend(metrics.VendorBrevo, metrics.StatusSuccess)
	if got := r.TotalByVendor(metrics.VendorResend); got != 3 {
		t.Errorf("resend total: want 3, got %d", got)
	}
	if got := r.TotalByVendor(metrics.VendorBrevo); got != 1 {
		t.Errorf("brevo total: want 1, got %d", got)
	}
}

func TestRegistry_NilMeterDoesNotPanic(t *testing.T) {
	r := metrics.NewRegistry(nil)
	r.IncSend(metrics.VendorResend, metrics.StatusSuccess)
	r.ObserveSend(metrics.VendorResend, metrics.StatusSuccess, 1)
	r.IncAttempt(metrics.VendorResend)
	if r.StatusFor(metrics.VendorResend, metrics.StatusSuccess) != 1 {
		t.Error("status counter should still work with nil meter")
	}
}

func TestRegistry_ConcurrentIncrementsAreSafe(t *testing.T) {
	r := metrics.NewRegistry(noop.NewMeterProvider().Meter("t"))
	const n = 200
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			r.IncSend(metrics.VendorResend, metrics.StatusSuccess)
			r.IncAttempt(metrics.VendorResend)
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	if got := r.StatusFor(metrics.VendorResend, metrics.StatusSuccess); got != n {
		t.Errorf("send count: want %d, got %d", n, got)
	}
	if got := r.Snapshot().Attempts[metrics.AttemptKey{Vendor: metrics.VendorResend}]; got != n {
		t.Errorf("attempts: want %d, got %d", n, got)
	}
}

func TestRegistry_VendorConstants(t *testing.T) {
	if string(metrics.VendorResend) != "resend" {
		t.Errorf("VendorResend: want resend, got %q", metrics.VendorResend)
	}
	if string(metrics.VendorBrevo) != "brevo" {
		t.Errorf("VendorBrevo: want brevo, got %q", metrics.VendorBrevo)
	}
	if string(metrics.VendorTelegram) != "telegram" {
		t.Errorf("VendorTelegram: want telegram, got %q", metrics.VendorTelegram)
	}
}

func TestRegistry_StatusConstants(t *testing.T) {
	cases := []struct {
		got, want metrics.Status
	}{
		{metrics.StatusSuccess, "ok"},
		{metrics.StatusBadRequest, "bad_request"},
		{metrics.StatusTransient, "transient"},
		{metrics.StatusDeadLetter, "dead_letter"},
	}
	for _, c := range cases {
		if string(c.got) != string(c.want) {
			t.Errorf("status: want %q, got %q", c.want, c.got)
		}
	}
}
