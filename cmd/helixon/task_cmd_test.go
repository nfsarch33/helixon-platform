package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTaskCmd_RequiresTicketOrPrompt(t *testing.T) {
	t.Parallel()
	cmd := newTaskCmd()
	cmd.SetArgs([]string{})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when no --ticket or --prompt")
	}
	if want := "either --ticket or --prompt is required"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestTaskCmd_RequiresProvider(t *testing.T) {
	t.Parallel()
	cmd := newTaskCmd()
	cmd.SetArgs([]string{"--prompt", "test"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error when no provider")
	}
	if want := "task requires a configured LLM provider (kind != none)"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world foo bar", 10, "hello worl..."},
		{"", 5, ""},
		{"  spaces  ", 20, "spaces"},
	}
	for _, tc := range tests {
		got := truncate(tc.input, tc.maxLen)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
		}
	}
}

func TestDoEngramPersist_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": "mem-001", "content": "test"})
	}))
	defer srv.Close()

	var buf bytes.Buffer
	doEngramPersist(context.Background(), &buf, srv.URL, "test-agent", "prompt", "result")
	if got := buf.String(); got == "" {
		t.Fatal("expected output")
	}
}

func TestDoEngramPersist_FailureNonFatal(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	doEngramPersist(context.Background(), &buf, srv.URL, "test-agent", "prompt", "result")
	if got := buf.String(); got == "" {
		t.Fatal("expected warning output")
	}
}
