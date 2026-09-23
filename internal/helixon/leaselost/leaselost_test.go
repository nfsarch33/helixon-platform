package leaselost

import (
	"errors"
	"testing"
)

// Tests are the contract (test-file guard): written by the ticket author, read-only for the student.

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		err    error
		want   Outcome
	}{
		{200, nil, Renewed}, {404, nil, BestEffort}, {409, nil, LeaseLost},
		{503, nil, Retry}, {429, nil, Retry}, {0, errors.New("dial"), Retry}, {418, nil, BestEffort},
	}
	for _, c := range cases {
		if got := Classify(c.status, c.err); got != c.want {
			t.Errorf("Classify(%d,%v) = %v, want %v", c.status, c.err, got, c.want)
		}
	}
}

func TestShouldCancelRun(t *testing.T) {
	for _, o := range []Outcome{Renewed, BestEffort, Retry} {
		if ShouldCancelRun(o) {
			t.Errorf("ShouldCancelRun(%v) = true, want false", o)
		}
	}
	if !ShouldCancelRun(LeaseLost) {
		t.Error("ShouldCancelRun(LeaseLost) = false, want true")
	}
}

func TestTracker(t *testing.T) {
	var tr Tracker
	for i, want := range []int{1, 2, 3} {
		if got := tr.Record(Retry); got != want {
			t.Fatalf("Record #%d = %d, want %d", i+1, got, want)
		}
	}
	if !tr.Exhausted(3) {
		t.Error("Exhausted(3) = false after 3 retries, want true")
	}
	if tr.Exhausted(0) {
		t.Error("Exhausted(0) = true, want false (max 0 disables)")
	}
	if got := tr.Record(Renewed); got != 0 {
		t.Errorf("Record(Renewed) = %d, want 0", got)
	}
	if tr.Exhausted(3) {
		t.Error("Exhausted(3) = true after a reset, want false")
	}
}

func TestString(t *testing.T) {
	want := map[Outcome]string{Renewed: "renewed", BestEffort: "best_effort", LeaseLost: "lease_lost", Retry: "retry"}
	for o, s := range want {
		if o.String() != s {
			t.Errorf("%d.String() = %q, want %q", int(o), o.String(), s)
		}
	}
}
