// Tests for the --via slack path of the notify CLI. v18776.
//
// The live-send cases point the bot transport at an httptest server via
// HLXN_SLACK_API_BASE, so nothing here touches slack.com.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// cliFakeToken is a distinctive non-credential string. Redaction assertions
// require it never to appear in stdout or stderr.
const cliFakeToken = "xoxb-1111-CLI-DISTINCTIVE-NOT-A-REAL-TOKEN-qqqq"

// noSlackEnv clears every Slack credential source so a test runs against a
// deterministic "unconfigured" baseline regardless of the ambient shell.
func noSlackEnv() map[string]string {
	return map[string]string{
		"SLACK_BOT_TOKEN":          "",
		"SLACK_CHANNEL":            "",
		"SENTRUX_SLACK_WEBHOOK":    "",
		"SLACK_WEBHOOK_URL":        "",
		"OP_SERVICE_ACCOUNT_TOKEN": "",
		"HLXN_SLACK_API_BASE":      "",
	}
}

func withEnv(base map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func TestViaIncludes(t *testing.T) {
	tests := []struct {
		name string
		via  string
		want map[string]bool
	}{
		{"email only", "email", map[string]bool{"email": true, "telegram": false, "slack": false}},
		{"telegram only", "telegram", map[string]bool{"email": false, "telegram": true, "slack": false}},
		{"slack only", "slack", map[string]bool{"email": false, "telegram": false, "slack": true}},
		{"both stays email+telegram", "both", map[string]bool{"email": true, "telegram": true, "slack": false}},
		{"all includes slack", "all", map[string]bool{"email": true, "telegram": true, "slack": true}},
		{"comma list", "email,slack", map[string]bool{"email": true, "telegram": false, "slack": true}},
		{"comma list with spaces", " telegram , slack ", map[string]bool{"email": false, "telegram": true, "slack": true}},
		{"case insensitive", "SLACK", map[string]bool{"email": false, "telegram": false, "slack": true}},
		{"both plus slack", "both,slack", map[string]bool{"email": true, "telegram": true, "slack": true}},
		{"empty matches nothing", "", map[string]bool{"email": false, "telegram": false, "slack": false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for want, expect := range tc.want {
				if got := viaIncludes(tc.via, want); got != expect {
					t.Errorf("viaIncludes(%q, %q) = %v; want %v", tc.via, want, got, expect)
				}
			}
		})
	}
}

func TestValidateVia(t *testing.T) {
	tests := []struct {
		via     string
		wantErr bool
	}{
		{"email", false},
		{"telegram", false},
		{"slack", false},
		{"both", false},
		{"all", false},
		{"email,slack", false},
		{"both, slack", false},
		{"EMAIL", false},
		{"", true},
		{"   ", true},
		{"carrier-pigeon", true},
		{"email,carrier-pigeon", true},
		{",", true},
	}
	for _, tc := range tests {
		t.Run("via="+tc.via, func(t *testing.T) {
			err := validateVia(tc.via)
			if tc.wantErr && err == nil {
				t.Fatalf("validateVia(%q) = nil; want error", tc.via)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateVia(%q) = %v; want nil", tc.via, err)
			}
		})
	}
}

func TestRunNotify_RejectsUnknownVia(t *testing.T) {
	rc, _, stderr := runNotify(t, []string{
		"--dry-run", "--idempotency-key", "v18776-bad-via", "--via", "carrier-pigeon",
	}, noSlackEnv())
	if rc != 2 {
		t.Fatalf("rc = %d; want 2", rc)
	}
	if !strings.Contains(stderr, "carrier-pigeon") {
		t.Fatalf("stderr should name the bad value: %q", stderr)
	}
}

func TestSlackMode(t *testing.T) {
	tests := []struct {
		name string
		f    notifyFlags
		want string
	}{
		{"bot token wins", notifyFlags{slackToken: cliFakeToken, slackWebhook: "https://hooks.slack.com/services/a/b/c"}, slackModeBot},
		{"webhook when no bot token", notifyFlags{slackWebhook: "https://hooks.slack.com/services/a/b/c"}, slackModeWebhook},
		{"op fallback", notifyFlags{opToken: "ops_stub"}, slackModeOp},
		{"nothing configured", notifyFlags{}, slackModeUnconfigured},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := slackMode(&tc.f); got != tc.want {
				t.Fatalf("slackMode = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestRunNotify_SlackUnconfiguredIsBlocked(t *testing.T) {
	rc, stdout, _ := runNotify(t, []string{
		"--dry-run", "--idempotency-key", "v18776-slack-blocked", "--via", "slack",
	}, noSlackEnv())
	if rc != 0 {
		t.Fatalf("rc = %d; want 0", rc)
	}
	audit := extractAudit(t, stdout)
	if audit["slack_blocker"] == nil {
		t.Fatalf("slack_blocker missing from audit: %v", audit)
	}
	if audit["slack_mode"] != slackModeUnconfigured {
		t.Errorf("slack_mode = %v; want %q", audit["slack_mode"], slackModeUnconfigured)
	}
}

func TestRunNotify_SlackDryRunDoesNotSend(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	rc, stdout, _ := runNotify(t, []string{
		"--dry-run", "--idempotency-key", "v18776-slack-dry", "--via", "slack",
		"--slack-channel", "#fleet-critical",
	}, withEnv(noSlackEnv(), "SLACK_BOT_TOKEN", cliFakeToken, "HLXN_SLACK_API_BASE", srv.URL))

	if rc != 0 {
		t.Fatalf("rc = %d; want 0", rc)
	}
	audit := extractAudit(t, stdout)
	if audit["slack_result"] != "dry-run" {
		t.Errorf("slack_result = %v; want dry-run", audit["slack_result"])
	}
	if audit["slack_mode"] != slackModeBot {
		t.Errorf("slack_mode = %v; want %q", audit["slack_mode"], slackModeBot)
	}
	if calls.Load() != 0 {
		t.Errorf("dry-run performed %d network calls; want 0", calls.Load())
	}
}

func TestRunNotify_SlackBotLiveSendSucceeds(t *testing.T) {
	var gotChannel, gotText, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Channel string `json:"channel"`
			Text    string `json:"text"`
		}
		_ = json.Unmarshal(raw, &body)
		gotChannel, gotText = body.Channel, body.Text
		_, _ = io.WriteString(w, `{"ok":true,"channel":"C123","ts":"1.2"}`)
	}))
	t.Cleanup(srv.Close)

	bodyFile := writeBodyFile(t, "fleet is green")
	rc, stdout, stderr := runNotify(t, []string{
		"--idempotency-key", "v18776-slack-live", "--via", "slack",
		"--subject", "[V18776]", "--body-file", bodyFile,
		"--slack-channel", "#fleet-critical", "--cost",
	}, withEnv(noSlackEnv(), "SLACK_BOT_TOKEN", cliFakeToken, "HLXN_SLACK_API_BASE", srv.URL))

	if rc != 0 {
		t.Fatalf("rc = %d; want 0. stderr=%q", rc, stderr)
	}
	audit := extractAudit(t, stdout)
	if audit["slack_result"] != "sent" {
		t.Fatalf("slack_result = %v; want sent (audit=%v)", audit["slack_result"], audit)
	}
	if gotChannel != "#fleet-critical" {
		t.Errorf("channel = %q; want #fleet-critical", gotChannel)
	}
	if !strings.Contains(gotText, "[V18776]") || !strings.Contains(gotText, "fleet is green") {
		t.Errorf("text = %q; want subject + body", gotText)
	}
	if gotAuth != "Bearer "+cliFakeToken {
		t.Errorf("Authorization not a bearer token: %q", gotAuth)
	}
	if audit["slack_cost_estimate_usd"] == nil {
		t.Errorf("slack_cost_estimate_usd missing with --cost")
	}
	assertNoTokenLeak(t, stdout, stderr)
}

func TestRunNotify_SlackBotOkFalseIsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slack answers 200 even on failure, and echoes nothing useful.
		// The CLI must still report a failure, not a send.
		w.WriteHeader(http.StatusOK)
		//nolint:gosec // G705: the fixture deliberately reflects the credential back so the redaction assertion below has something real to catch.
		_, _ = io.WriteString(w, `{"ok":false,"error":"invalid_auth for `+r.Header.Get("Authorization")+`"}`)
	}))
	t.Cleanup(srv.Close)

	rc, stdout, stderr := runNotify(t, []string{
		"--idempotency-key", "v18776-slack-okfalse", "--via", "slack",
		"--subject", "[FAIL]", "--slack-channel", "#fleet-critical",
	}, withEnv(noSlackEnv(), "SLACK_BOT_TOKEN", cliFakeToken, "HLXN_SLACK_API_BASE", srv.URL))

	if rc != 1 {
		t.Fatalf("rc = %d; want 1 (a failed send must not exit 0). stderr=%q", rc, stderr)
	}
	audit := extractAudit(t, stdout)
	if audit["slack_result"] != "failed" {
		t.Fatalf("slack_result = %v; want failed", audit["slack_result"])
	}
	errStr, _ := audit["slack_error"].(string)
	if !strings.Contains(errStr, "invalid_auth") {
		t.Errorf("slack_error = %q; want it to name invalid_auth", errStr)
	}
	assertNoTokenLeak(t, stdout, stderr)
}

func TestRunNotify_SlackBotRejectsAppLevelToken(t *testing.T) {
	rc, stdout, stderr := runNotify(t, []string{
		"--idempotency-key", "v18776-slack-xapp", "--via", "slack",
		"--slack-channel", "#fleet-critical",
	}, withEnv(noSlackEnv(), "SLACK_BOT_TOKEN", "xapp-1-A000-000-deadbeefdeadbeef",
		"HLXN_SLACK_API_BASE", "http://127.0.0.1:1"))

	if rc != 1 {
		t.Fatalf("rc = %d; want 1. stderr=%q", rc, stderr)
	}
	audit := extractAudit(t, stdout)
	errStr, _ := audit["slack_error"].(string)
	if !strings.Contains(errStr, "xoxb-") {
		t.Fatalf("slack_error should explain the token type: %q", errStr)
	}
}

func TestRunNotify_SlackWebhookModeSelected(t *testing.T) {
	rc, stdout, _ := runNotify(t, []string{
		"--dry-run", "--idempotency-key", "v18776-slack-wh", "--via", "slack",
	}, withEnv(noSlackEnv(), "SENTRUX_SLACK_WEBHOOK", "https://hooks.slack.com/services/T0/B0/XXXXXXXX"))
	if rc != 0 {
		t.Fatalf("rc = %d; want 0", rc)
	}
	audit := extractAudit(t, stdout)
	if audit["slack_mode"] != slackModeWebhook {
		t.Fatalf("slack_mode = %v; want %q", audit["slack_mode"], slackModeWebhook)
	}
	if audit["slack_webhook_set"] != true {
		t.Errorf("slack_webhook_set = %v; want true", audit["slack_webhook_set"])
	}
}

// TestRunNotify_SlackWebhookNeverInAudit is the webhook-secret gate: the
// audit event records only that a webhook was configured, never its value.
func TestRunNotify_SlackWebhookNeverInAudit(t *testing.T) {
	const secretPath = "SECRETWEBHOOKPATHSEGMENT"
	rc, stdout, stderr := runNotify(t, []string{
		"--idempotency-key", "v18776-slack-wh-leak", "--via", "slack", "--subject", "[WH]",
	}, withEnv(noSlackEnv(), "SENTRUX_SLACK_WEBHOOK", "https://not-slack.example.com/services/"+secretPath))
	if rc != 1 {
		t.Fatalf("rc = %d; want 1 (bad webhook host must fail)", rc)
	}
	if strings.Contains(stdout+stderr, secretPath) {
		t.Fatalf("webhook path leaked into CLI output: %q", stdout+stderr)
	}
}

func TestRunNotify_SlackOpModeSelected(t *testing.T) {
	rc, stdout, _ := runNotify(t, []string{
		"--dry-run", "--idempotency-key", "v18776-slack-op", "--via", "slack",
	}, withEnv(noSlackEnv(), "OP_SERVICE_ACCOUNT_TOKEN", "ops_stub_value_not_a_real_token"))
	if rc != 0 {
		t.Fatalf("rc = %d; want 0", rc)
	}
	audit := extractAudit(t, stdout)
	if audit["slack_mode"] != slackModeOp {
		t.Fatalf("slack_mode = %v; want %q", audit["slack_mode"], slackModeOp)
	}
}

func TestRunNotify_EmailAndSlackCombination(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	bodyFile := writeBodyFile(t, "combined body")
	rc, stdout, stderr := runNotify(t, []string{
		"--idempotency-key", "v18776-combo", "--via", "email,slack",
		"--subject", "[COMBO]", "--body-file", bodyFile,
		"--slack-channel", "#fleet-critical",
	}, withEnv(noSlackEnv(),
		"SLACK_BOT_TOKEN", cliFakeToken,
		"HLXN_SLACK_API_BASE", srv.URL,
		"RESEND_API_KEY", "test-resend"))

	if rc != 0 {
		t.Fatalf("rc = %d; want 0. stderr=%q", rc, stderr)
	}
	audit := extractAudit(t, stdout)
	if audit["email_subject"] != "[COMBO]" {
		t.Errorf("email path lost: email_subject = %v", audit["email_subject"])
	}
	if audit["slack_result"] != "sent" {
		t.Errorf("slack_result = %v; want sent", audit["slack_result"])
	}
	if calls.Load() != 1 {
		t.Errorf("slack calls = %d; want 1", calls.Load())
	}
}

// TestRunNotify_BothStillExcludesSlack pins the backward-compatible meaning
// of --via both: email + telegram, never slack.
func TestRunNotify_BothStillExcludesSlack(t *testing.T) {
	rc, stdout, _ := runNotify(t, []string{
		"--dry-run", "--idempotency-key", "v18776-both-compat", "--via", "both",
		"--subject", "[BOTH]",
	}, withEnv(noSlackEnv(), "RESEND_API_KEY", "test-resend", "SLACK_BOT_TOKEN", cliFakeToken))
	if rc != 0 {
		t.Fatalf("rc = %d; want 0", rc)
	}
	audit := extractAudit(t, stdout)
	if _, ok := audit["slack_mode"]; ok {
		t.Fatalf("--via both must not engage slack: %v", audit)
	}
	if audit["email_subject"] != "[BOTH]" {
		t.Errorf("email_subject = %v; want [BOTH]", audit["email_subject"])
	}
}

func TestSlackChannelFallsBackToEnv(t *testing.T) {
	var gotChannel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Channel string `json:"channel"`
		}
		_ = json.Unmarshal(raw, &body)
		gotChannel = body.Channel
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	rc, _, stderr := runNotify(t, []string{
		"--idempotency-key", "v18776-chan-env", "--via", "slack", "--subject", "[ENV]",
	}, withEnv(noSlackEnv(), "SLACK_BOT_TOKEN", cliFakeToken,
		"SLACK_CHANNEL", "#from-env", "HLXN_SLACK_API_BASE", srv.URL))
	if rc != 0 {
		t.Fatalf("rc = %d; want 0. stderr=%q", rc, stderr)
	}
	if gotChannel != "#from-env" {
		t.Fatalf("channel = %q; want #from-env", gotChannel)
	}
}

func TestSlackText(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		body    string
		want    []string
	}{
		{"subject and body", "[S]", "hello", []string{"[S]", "hello"}},
		{"body only", "", "hello", []string{"hello"}},
		{"subject only", "[S]", "", []string{"[S]"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := slackText(tc.subject, tc.body)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("slackText = %q; want it to contain %q", got, want)
				}
			}
		})
	}
}

func TestSlackText_ControlCharsStrippedAndBounded(t *testing.T) {
	got := slackText("[S]", "a\x00b"+strings.Repeat("x", 8000))
	if strings.ContainsRune(got, 0x00) {
		t.Error("slackText did not strip control characters")
	}
	if len(got) > 4100 {
		t.Errorf("slackText length %d; want bounded near 4000", len(got))
	}
}

func TestSlackCostEstimate(t *testing.T) {
	if got := slackCostEstimate(0); got != 0.0 {
		t.Errorf("0 sends: got %v", got)
	}
	if got := slackCostEstimate(2); got < 0.00019 || got > 0.00021 {
		t.Errorf("2 sends: got %v; want ~0.0002", got)
	}
}

func assertNoTokenLeak(t *testing.T, streams ...string) {
	t.Helper()
	for _, s := range streams {
		if strings.Contains(s, cliFakeToken) {
			t.Fatalf("output leaked the bot token: %q", s)
		}
	}
}
