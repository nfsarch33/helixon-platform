package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// v18860-1 renew-409: RenewClaim must keep the renew verdicts apart. A 409 is
// definitive (the lease was swept or taken) and is surfaced as
// ErrTicketNotClaimedBy so the poller can cancel the run; 404 and 5xx stay
// generic errors, because a board that predates the route or a sick board is
// best-effort, never a cancellation signal.
func TestSprintboardRenewLeaseLostIsTyped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		status   int
		wantLost bool
	}{
		{name: "409 is lease lost", status: http.StatusConflict, wantLost: true},
		{name: "404 stays generic", status: http.StatusNotFound},
		{name: "500 stays generic", status: http.StatusInternalServerError},
		{name: "204 renews", status: http.StatusNoContent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"renew refused"}`))
			}))
			t.Cleanup(srv.Close)
			c := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL, AgentName: "a"}, nil)

			err := c.RenewClaim(context.Background(), "T-1")
			if tc.status < 400 {
				if err != nil {
					t.Fatalf("RenewClaim on %d: %v", tc.status, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("RenewClaim on %d returned nil, want an error", tc.status)
			}
			if got := errors.Is(err, ErrTicketNotClaimedBy); got != tc.wantLost {
				t.Fatalf("errors.Is(err, ErrTicketNotClaimedBy) = %v on status %d, want %v (err: %v)", got, tc.status, tc.wantLost, err)
			}
		})
	}
}

// TestSprintboardRenewTransportErrorStaysGeneric pins the other half of the
// classification: a transport failure is not — and must never look like — a
// lease verdict, whatever the poller decides to do with it later.
func TestSprintboardRenewTransportErrorStaysGeneric(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // dead server: every call is a transport error
	c := NewSprintboardClient(SprintboardConfig{BaseURL: srv.URL, AgentName: "a"}, nil)

	err := c.RenewClaim(context.Background(), "T-1")
	if err == nil {
		t.Fatal("RenewClaim on a dead server returned nil, want a transport error")
	}
	if errors.Is(err, ErrTicketNotClaimedBy) {
		t.Fatalf("a transport error must not read as lease lost: %v", err)
	}
}
