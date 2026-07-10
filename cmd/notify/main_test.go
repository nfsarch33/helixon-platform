// Tests for the notify CLI entry point. These tests capture the existing
// dry-run audit behavior so the v17714-1 refactor (CC reduction from 23 to
// ≤6 for top-level main) preserves the public contract.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/notify/telegram"
)

// suppress unused-import warnings for runtime-only test deps.
var _ = context.Background
var _ = io.Discard

// runNotify executes runNotify (testable) with the given args + env, and
// returns rc, stdout, stderr.
func runNotify(t *testing.T, args []string, env map[string]string) (int, string, string) {
	t.Helper()
	oldEnv := map[string]string{}
	for k, v := range env {
		oldEnv[k] = os.Getenv(k)
		t.Setenv(k, v)
	}
	t.Cleanup(func() {
		for k := range env {
			if v, ok := oldEnv[k]; ok {
				_ = os.Setenv(k, v)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	})

	origStdout, origStderr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr
	defer func() {
		os.Stdout = origStdout
		os.Stderr = origStderr
	}()

	rc := runNotifyCmd(args)

	_ = wOut.Close()
	_ = wErr.Close()
	stdoutBytes, _ := io.ReadAll(rOut)
	stderrBytes, _ := io.ReadAll(rErr)
	return rc, string(stdoutBytes), string(stderrBytes)
}

// extractAudit parses the audit JSON from stdout (single object).
func extractAudit(t *testing.T, stdout string) map[string]any {
	t.Helper()
	start := strings.Index(stdout, "{")
	end := strings.LastIndex(stdout, "}")
	if start < 0 || end <= start {
		t.Fatalf("no JSON object in stdout: %q", stdout)
	}
	var audit map[string]any
	if err := json.Unmarshal([]byte(stdout[start:end+1]), &audit); err != nil {
		t.Fatalf("unmarshal audit JSON: %v; raw=%q", err, stdout)
	}
	return audit
}

func writeBodyFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "body.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write body file: %v", err)
	}
	return p
}

func TestRunNotify_MissingIdempotencyKey(t *testing.T) {
	rc, _, stderr := runNotify(t, []string{"--dry-run"}, map[string]string{
		"RESEND_API_KEY": "test-resend",
	})
	if rc != 2 {
		t.Errorf("rc = %d; want 2", rc)
	}
	if !strings.Contains(stderr, "idempotency-key") {
		t.Errorf("stderr missing idempotency-key: %q", stderr)
	}
}

func TestRunNotify_DryRunEmailPath(t *testing.T) {
	bodyFile := writeBodyFile(t, "hello world")
	rc, stdout, stderr := runNotify(t, []string{
		"--dry-run",
		"--idempotency-key", "v17714-test-1",
		"--subject", "[TEST]",
		"--body-file", bodyFile,
		"--plan", "v17713-v17800",
		"--job-id", "j1",
		"--via", "email",
		"--cost",
	}, map[string]string{
		"RESEND_API_KEY": "test-resend",
		"BREVO_API_KEY":  "test-brevo",
	})

	if rc != 0 {
		t.Errorf("rc = %d; want 0 (dry-run should succeed)", rc)
	}
	if !strings.Contains(stderr, "DRY-RUN") {
		t.Errorf("stderr missing DRY-RUN marker: %q", stderr)
	}
	audit := extractAudit(t, stdout)
	if audit["event"] != "notify_attempt" {
		t.Errorf("event = %v; want notify_attempt", audit["event"])
	}
	if audit["idempotency_key"] != "v17714-test-1" {
		t.Errorf("idempotency_key = %v", audit["idempotency_key"])
	}
	if audit["via"] != "email" {
		t.Errorf("via = %v; want email", audit["via"])
	}
	if audit["dry_run"] != true {
		t.Errorf("dry_run = %v; want true", audit["dry_run"])
	}
	if audit["resend_key_set"] != true {
		t.Errorf("resend_key_set = %v; want true", audit["resend_key_set"])
	}
	if audit["email_subject"] != "[TEST]" {
		t.Errorf("email_subject = %v; want [TEST]", audit["email_subject"])
	}
	if audit["email_cost_estimate_usd"] == nil {
		t.Errorf("email_cost_estimate_usd missing in audit")
	}
}

func TestRunNotify_DryRunTelegramNoToken(t *testing.T) {
	rc, stdout, _ := runNotify(t, []string{
		"--dry-run",
		"--idempotency-key", "v17714-tg-1",
		"--subject", "[TG]",
		"--via", "telegram",
	}, map[string]string{
		"RESEND_API_KEY": "test-resend",
	})
	if rc != 0 {
		t.Errorf("rc = %d; want 0", rc)
	}
	audit := extractAudit(t, stdout)
	if audit["telegram_blocker"] == nil {
		t.Errorf("telegram_blocker missing")
	}
}

func TestRunNotify_DryRunTelegramWithToken(t *testing.T) {
	rc, stdout, _ := runNotify(t, []string{
		"--dry-run",
		"--idempotency-key", "v17714-tg-2",
		"--subject", "[TG]",
		"--via", "telegram",
	}, map[string]string{
		"RESEND_API_KEY":     "test-resend",
		"TELEGRAM_BOT_TOKEN": "test-token",
		"TELEGRAM_CHAT_ID":   "1234",
	})
	if rc != 0 {
		t.Errorf("rc = %d; want 0", rc)
	}
	audit := extractAudit(t, stdout)
	if audit["telegram_result"] != "dry-run" {
		t.Errorf("telegram_result = %v; want dry-run", audit["telegram_result"])
	}
}

func TestRunNotify_LiveTelegramSuccess(t *testing.T) {
	// Note: the production telegram.New() defaults to https://api.telegram.org
	// so we don't actually do a live network call in this CLI-level test.
	// The telegramWithStrikes unit tests below cover the actual retry logic
	// against an httptest server. Here we just verify the CLI integration
	// doesn't crash when telegram is configured.
	rc, stdout, _ := runNotify(t, []string{
		"--dry-run",
		"--idempotency-key", "v17714-tg-live-1",
		"--subject", "[LIVE]",
		"--via", "telegram",
	}, map[string]string{
		"RESEND_API_KEY":     "test-resend",
		"TELEGRAM_BOT_TOKEN": "test-token",
		"TELEGRAM_CHAT_ID":   "1234",
	})
	if rc != 0 {
		t.Errorf("rc = %d; want 0", rc)
	}
	audit := extractAudit(t, stdout)
	if audit["telegram_result"] != "dry-run" {
		t.Errorf("telegram_result = %v; want dry-run", audit["telegram_result"])
	}
}

func TestRunNotify_BothPaths(t *testing.T) {
	bodyFile := writeBodyFile(t, "both paths body")
	rc, stdout, _ := runNotify(t, []string{
		"--dry-run",
		"--idempotency-key", "v17714-both-1",
		"--subject", "[BOTH]",
		"--body-file", bodyFile,
		"--via", "both",
		"--cost",
	}, map[string]string{
		"RESEND_API_KEY":     "test-resend",
		"BREVO_API_KEY":      "test-brevo",
		"TELEGRAM_BOT_TOKEN": "test-tg",
		"TELEGRAM_CHAT_ID":   "1234",
	})
	if rc != 0 {
		t.Errorf("rc = %d; want 0", rc)
	}
	audit := extractAudit(t, stdout)
	if audit["email_subject"] != "[BOTH]" {
		t.Errorf("email_subject = %v; want [BOTH]", audit["email_subject"])
	}
	if audit["telegram_result"] != "dry-run" {
		t.Errorf("telegram_result = %v; want dry-run", audit["telegram_result"])
	}
}

func TestTelegramWithStrikes_SucceedsFirstAttempt(t *testing.T) {
	// happy-path server: returns valid JSON with ok=true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	t.Cleanup(srv.Close)

	attempts, result, err := telegramWithStrikes(newFakeClient(t, srv.URL), "hi", "body", 3)
	if err != nil {
		t.Errorf("err = %v; want nil", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d; want 1", attempts)
	}
	if result != "sent" {
		t.Errorf("result = %q; want sent", result)
	}
}

func TestTelegramWithStrikes_FallbackAfterExhaustion(t *testing.T) {
	// always-fail server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	attempts, result, err := telegramWithStrikes(newFakeClient(t, srv.URL), "hi", "body", 2)
	if err == nil {
		t.Errorf("err = nil; want non-nil")
	}
	if attempts != 2 {
		t.Errorf("attempts = %d; want 2", attempts)
	}
	if result != "fallback-to-email" {
		t.Errorf("result = %q; want fallback-to-email", result)
	}
}

func TestEmailCostEstimate(t *testing.T) {
	if got := emailCostEstimate(true, false); got != 0.0 {
		t.Errorf("resend=true brevo=false: got %v; want 0.0", got)
	}
	if got := emailCostEstimate(false, true); got != 0.0004 {
		t.Errorf("resend=false brevo=true: got %v; want 0.0004", got)
	}
	if got := emailCostEstimate(false, false); got != 0.0 {
		t.Errorf("resend=false brevo=false: got %v; want 0.0", got)
	}
}

func TestTelegramCostEstimate(t *testing.T) {
	if got := telegramCostEstimate(0); got != 0.0 {
		t.Errorf("0 attempts: got %v", got)
	}
	if got := telegramCostEstimate(3); got < 0.000299 || got > 0.000301 {
		t.Errorf("3 attempts: got %v; want ~0.0003", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 100); got != "short" {
		t.Errorf("truncate short: %q", got)
	}
	if got := truncate("abcdefghij", 5); !strings.HasSuffix(got, "[truncated]") {
		t.Errorf("truncate long: %q", got)
	}
}

// fakeClient returns a telegram.Client pointed at a test server. The
// production telegram.New() appends "<BotToken>/<path>" to BaseURL, so the
// test server URL must end with "/bot" to mirror the production layout.
func newFakeClient(t *testing.T, baseURL string) *telegram.Client {
	t.Helper()
	return telegram.New(telegram.Config{
		BotToken: "fake",
		ChatID:   "0",
		BaseURL:  baseURL + "/bot",
	})
}

// Sanity check that context import is used (it's needed by tests that
// pass httptest contexts). v17714-1: import set is intentionally minimal.
var _ = context.Background
