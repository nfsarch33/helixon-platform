package leaselost

import (
	"errors"
	"testing"
)

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

func TestString(t *testing.T) {
	want := map[Outcome]string{Renewed: "renewed", BestEffort: "best_effort", LeaseLost: "lease_lost", Retry: "retry"}
	for o, s := range want {
		if o.String() != s {
			t.Errorf("%d.String() = %q, want %q", int(o), o.String(), s)
		}
	}
}
