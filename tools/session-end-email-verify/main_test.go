package main

import "testing"

func TestTail_ShortString(t *testing.T) {
	got := tail("hello world", 100)
	if got != "hello world" {
		t.Errorf("expected passthrough, got %q", got)
	}
}

func TestTail_LongString(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	got := tail(long, 50)
	if len(got) != 53 { // 3 dots + 50 chars
		t.Errorf("expected length 53, got %d", len(got))
	}
	if got[:3] != "..." {
		t.Errorf("expected leading ..., got %q", got[:3])
	}
}

func TestTail_EmptyString(t *testing.T) {
	got := tail("", 100)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
