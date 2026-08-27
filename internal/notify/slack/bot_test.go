// Tests for the Slack Web API (chat.postMessage) bot transport. No real
// network: every case runs against httptest. v18776.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify/metrics"
)

// fakeBotToken is a distinctive non-credential string. The redaction tests
// assert this exact value never reaches an error string or a log line.
const fakeBotToken = "xoxb-0000-DISTINCTIVE-NOT-A-REAL-TOKEN-zzzz"

// botServer spins up an httptest server whose handler is invoked for every
// attempt, and returns the server plus a pointer to the attempt counter.
func botServer(t *testing.T, h func(w http.ResponseWriter, r *http.Request, attempt int32)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(w, r, calls.Add(1))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// newTestBot builds a BotClient pointed at the test server with the sleep
// hook captured so retries do not spend wall-clock time.
func newTestBot(t *testing.T, baseURL string, slept *[]time.Duration) *BotClient {
	t.Helper()
	c, err := NewBot(BotConfig{
		Token:   fakeBotToken,
		Channel: "#fleet-critical",
		BaseURL: baseURL,
		Sleep: func(d time.Duration) {
			if slept != nil {
				*slept = append(*slept, d)
			}
		},
	})
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	return c
}

func TestNewBot_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     BotConfig
		wantErr error
	}{
		{"empty token", BotConfig{Channel: "#c"}, ErrBotTokenRequired},
		{"whitespace token", BotConfig{Token: "   ", Channel: "#c"}, ErrBotTokenRequired},
		{"empty channel", BotConfig{Token: fakeBotToken}, ErrBotChannelRequired},
		{"app-level token rejected", BotConfig{Token: "xapp-1-A0-000-deadbeef", Channel: "#c"}, ErrAppLevelToken},
		{"valid", BotConfig{Token: fakeBotToken, Channel: "#c"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewBot(tc.cfg)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("NewBot err = %v; want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && got == nil {
				t.Fatal("NewBot returned nil client without error")
			}
		})
	}
}

func TestNewBot_DefaultsAndAccessors(t *testing.T) {
	c, err := NewBot(BotConfig{Token: fakeBotToken, Channel: " #ops "})
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	if c.Channel() != "#ops" {
		t.Fatalf("Channel() = %q; want #ops", c.Channel())
	}
	if c.baseURL != botAPIBaseURL {
		t.Fatalf("baseURL = %q; want %q", c.baseURL, botAPIBaseURL)
	}
	if c.maxAttempts != defaultBotAttempts {
		t.Fatalf("maxAttempts = %d; want %d", c.maxAttempts, defaultBotAttempts)
	}
	if c.httpc.Timeout != defaultBotTimeout {
		t.Fatalf("timeout = %v; want %v", c.httpc.Timeout, defaultBotTimeout)
	}
}

func TestBotPost_RequestShape(t *testing.T) {
	var gotAuth, gotCT, gotPath, gotMethod string
	var gotBody postMessageRequest
	srv, _ := botServer(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		gotMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = io.WriteString(w, `{"ok":true,"channel":"C1","ts":"1.2"}`)
	})
	c := newTestBot(t, srv.URL, nil)
	if err := c.Post(context.Background(), "hello fleet"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q; want POST", gotMethod)
	}
	if gotPath != botPostMessagePath {
		t.Errorf("path = %q; want %q", gotPath, botPostMessagePath)
	}
	if gotAuth != "Bearer "+fakeBotToken {
		t.Errorf("Authorization header not a bearer token: %q", gotAuth)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("Content-Type = %q; want application/json", gotCT)
	}
	if gotBody.Channel != "#fleet-critical" || gotBody.Text != "hello fleet" {
		t.Errorf("body = %+v; want channel/text populated", gotBody)
	}
}

func TestBotPost_Outcomes(t *testing.T) {
	tests := []struct {
		name         string
		handler      func(w http.ResponseWriter, r *http.Request, attempt int32)
		wantErr      bool
		wantErrParts []string
		wantAttempts int32
		wantStatus   metrics.Status
	}{
		{
			name: "ok true succeeds on first attempt",
			handler: func(w http.ResponseWriter, _ *http.Request, _ int32) {
				_, _ = io.WriteString(w, `{"ok":true}`)
			},
			wantAttempts: 1,
			wantStatus:   metrics.StatusSuccess,
		},
		{
			name: "http 200 with ok false invalid_auth is an error",
			handler: func(w http.ResponseWriter, _ *http.Request, _ int32) {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
			},
			wantErr:      true,
			wantErrParts: []string{"invalid_auth"},
			wantAttempts: 1,
			wantStatus:   metrics.StatusBadRequest,
		},
		{
			name: "http 200 with ok false channel_not_found is an error",
			handler: func(w http.ResponseWriter, _ *http.Request, _ int32) {
				_, _ = io.WriteString(w, `{"ok":false,"error":"channel_not_found"}`)
			},
			wantErr:      true,
			wantErrParts: []string{"channel_not_found"},
			wantAttempts: 1,
			wantStatus:   metrics.StatusBadRequest,
		},
		{
			name: "ok false with empty error code still reports",
			handler: func(w http.ResponseWriter, _ *http.Request, _ int32) {
				_, _ = io.WriteString(w, `{"ok":false}`)
			},
			wantErr:      true,
			wantErrParts: []string{"unknown_error"},
			wantAttempts: 1,
			wantStatus:   metrics.StatusBadRequest,
		},
		{
			name: "http 500 is retried then succeeds",
			handler: func(w http.ResponseWriter, _ *http.Request, attempt int32) {
				if attempt < 2 {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			},
			wantAttempts: 2,
			wantStatus:   metrics.StatusSuccess,
		},
		{
			name: "http 500 exhausts retries",
			handler: func(w http.ResponseWriter, _ *http.Request, _ int32) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr:      true,
			wantErrParts: []string{"HTTP 500"},
			wantAttempts: defaultBotAttempts,
			wantStatus:   metrics.StatusDeadLetter,
		},
		{
			name: "http 404 is not retried",
			handler: func(w http.ResponseWriter, _ *http.Request, _ int32) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantErr:      true,
			wantErrParts: []string{"HTTP 404"},
			wantAttempts: 1,
			wantStatus:   metrics.StatusBadRequest,
		},
		{
			name: "malformed json is not retried",
			handler: func(w http.ResponseWriter, _ *http.Request, _ int32) {
				_, _ = io.WriteString(w, `not-json`)
			},
			wantErr:      true,
			wantErrParts: []string{"decode"},
			wantAttempts: 1,
			wantStatus:   metrics.StatusDeadLetter,
		},
		{
			name: "oversized body is rejected without retry",
			handler: func(w http.ResponseWriter, _ *http.Request, _ int32) {
				_, _ = w.Write(bytes.Repeat([]byte("A"), int(maxAPIBodyBytes)+512))
			},
			wantErr:      true,
			wantErrParts: []string{"bounded read limit"},
			wantAttempts: 1,
			wantStatus:   metrics.StatusDeadLetter,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := botServer(t, tc.handler)
			reg := metrics.NewRegistry(nil)
			c := newTestBot(t, srv.URL, nil).WithMetrics(reg)
			err := c.Post(context.Background(), "hi")
			if tc.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want nil error, got %v", err)
			}
			for _, part := range tc.wantErrParts {
				if err == nil || !strings.Contains(err.Error(), part) {
					t.Errorf("error %v does not contain %q", err, part)
				}
			}
			if got := calls.Load(); got != tc.wantAttempts {
				t.Errorf("server calls = %d; want %d", got, tc.wantAttempts)
			}
			if got := reg.StatusFor(metrics.VendorSlack, tc.wantStatus); got < 1 {
				t.Errorf("metric %v count = %d; want >=1", tc.wantStatus, got)
			}
			if got := reg.Attempts(metrics.VendorSlack); got != 1 {
				t.Errorf("Attempts = %d; want 1 (one logical send)", got)
			}
		})
	}
}

func TestBotPost_RateLimitHonoursRetryAfter(t *testing.T) {
	srv, calls := botServer(t, func(w http.ResponseWriter, _ *http.Request, attempt int32) {
		if attempt < 2 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	var slept []time.Duration
	c := newTestBot(t, srv.URL, &slept)
	if err := c.Post(context.Background(), "hi"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d; want 2", calls.Load())
	}
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Fatalf("slept = %v; want [7s] from Retry-After", slept)
	}
}

func TestBotPost_RetryAfterIsClamped(t *testing.T) {
	srv, _ := botServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		w.Header().Set("Retry-After", "99999")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	var slept []time.Duration
	c := newTestBot(t, srv.URL, &slept)
	if err := c.Post(context.Background(), "hi"); err == nil {
		t.Fatal("want error after exhausting retries")
	}
	for _, d := range slept {
		if d > maxRetryAfter {
			t.Fatalf("slept %v exceeds clamp %v", d, maxRetryAfter)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		header string
		want   time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"3", 3 * time.Second},
		{"not-a-number", 0},
		{"-5", 0},
		{"99999", maxRetryAfter},
	}
	for _, tc := range tests {
		t.Run("header="+tc.header, func(t *testing.T) {
			if got := parseRetryAfter(tc.header); got != tc.want {
				t.Fatalf("parseRetryAfter(%q) = %v; want %v", tc.header, got, tc.want)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	if got := retryDelay(1, 5*time.Second); got != 5*time.Second {
		t.Errorf("explicit Retry-After should win, got %v", got)
	}
	first := retryDelay(1, 0)
	second := retryDelay(2, 0)
	if second <= first {
		t.Errorf("backoff not increasing: %v then %v", first, second)
	}
	if got := retryDelay(20, 0); got > maxBackoff {
		t.Errorf("backoff %v exceeds cap %v", got, maxBackoff)
	}
}

func TestBotPost_ContextCancelStopsRetries(t *testing.T) {
	srv, _ := botServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	// No sleep hook: the real ctx-aware wait must abort.
	c, err := NewBot(BotConfig{Token: fakeBotToken, Channel: "#c", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Post(ctx, "hi"); err == nil {
		t.Fatal("want error from canceled context")
	}
}

func TestBotPostTo_ChannelOverride(t *testing.T) {
	var got postMessageRequest
	srv, _ := botServer(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	c := newTestBot(t, srv.URL, nil)
	if err := c.PostTo(context.Background(), "#other", "hi"); err != nil {
		t.Fatalf("PostTo: %v", err)
	}
	if got.Channel != "#other" {
		t.Fatalf("channel = %q; want #other", got.Channel)
	}
}

func TestBotPostTo_EmptyChannelRejected(t *testing.T) {
	c := newTestBot(t, "http://127.0.0.1:1", nil)
	if err := c.PostTo(context.Background(), "  ", "hi"); !errors.Is(err, ErrBotChannelRequired) {
		t.Fatalf("err = %v; want ErrBotChannelRequired", err)
	}
}

// TestBotPost_NeverLeaksToken is the redaction gate: even when the upstream
// echoes the credential back in the response body, neither the returned
// error nor any log line may contain it.
func TestBotPost_NeverLeaksToken(t *testing.T) {
	srv, _ := botServer(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		w.WriteHeader(http.StatusInternalServerError)
		//nolint:gosec // G705: the fixture deliberately reflects the credential back so the redaction assertion below has something real to catch.
		_, _ = io.WriteString(w, `{"ok":false,"error":"leaked `+r.Header.Get("Authorization")+`"}`)
	})

	var logBuf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })

	var slept []time.Duration
	c := newTestBot(t, srv.URL, &slept)
	err := c.Post(context.Background(), "hi")
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), fakeBotToken) {
		t.Fatalf("error string leaked the token: %q", err.Error())
	}
	if strings.Contains(logBuf.String(), fakeBotToken) {
		t.Fatalf("log output leaked the token: %q", logBuf.String())
	}
}

func TestRedact(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		secrets []string
		want    string
	}{
		{"no secrets", "plain message", nil, "plain message"},
		{"replaces secret", "token=" + fakeBotToken, []string{fakeBotToken}, "token=" + RedactedLabel},
		{"ignores empty", "abc", []string{""}, "abc"},
		{"ignores short secrets to avoid mangling", "abcdef", []string{"abc"}, "abcdef"},
		{
			"replaces every occurrence",
			fakeBotToken + " and " + fakeBotToken,
			[]string{fakeBotToken},
			RedactedLabel + " and " + RedactedLabel,
		},
		{
			"replaces webhook url",
			"post to https://hooks.slack.com/services/T/B/SECRETPART failed",
			[]string{"https://hooks.slack.com/services/T/B/SECRETPART"},
			"post to " + RedactedLabel + " failed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Redact(tc.in, tc.secrets...); got != tc.want {
				t.Fatalf("Redact = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestSanitizeAPIError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "unknown_error"},
		{"plain", "invalid_auth", "invalid_auth"},
		{"strips control chars", "bad\x00code\n", "badcode"},
		{"truncates", strings.Repeat("x", maxAPIErrorChars+50), strings.Repeat("x", maxAPIErrorChars) + "..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeAPIError(tc.in); got != tc.want {
				t.Fatalf("sanitizeAPIError = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestBotPost_NetworkErrorIsRetried(t *testing.T) {
	srv, _ := botServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		w.WriteHeader(http.StatusOK)
	})
	dead := srv.URL
	srv.Close() // force connection-refused on every attempt
	var slept []time.Duration
	c, err := NewBot(BotConfig{
		Token: fakeBotToken, Channel: "#c", BaseURL: dead, MaxAttempts: 2,
		Sleep: func(d time.Duration) { slept = append(slept, d) },
	})
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	if err := c.Post(context.Background(), "hi"); err == nil {
		t.Fatal("want error from dead endpoint")
	}
	if len(slept) != 1 {
		t.Fatalf("slept = %v; want exactly one backoff between 2 attempts", slept)
	}
}

func TestBotWithMetrics_NilRegistryDoesNotPanic(t *testing.T) {
	srv, _ := botServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	c := newTestBot(t, srv.URL, nil)
	if err := c.Post(context.Background(), "hi"); err != nil {
		t.Fatalf("Post with nil metrics: %v", err)
	}
}

func TestClientWithChannel(t *testing.T) {
	c := NewFromURL(validWebhook).WithChannel("#ops")
	if c.Channel() != "#ops" {
		t.Fatalf("Channel() = %q; want #ops", c.Channel())
	}
}

// TestWebhookErrorDoesNotLeakPath asserts the malformed-webhook guard reports
// only the scheme+host, never the secret path segment.
func TestWebhookErrorDoesNotLeakPath(t *testing.T) {
	const secretPath = "SUPER-SECRET-PATH-SEGMENT"
	c := New(Config{WebhookURL: "https://evil.example.com/services/" + secretPath})
	err := c.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("want error for non-slack webhook host")
	}
	if strings.Contains(err.Error(), secretPath) {
		t.Fatalf("error leaked the webhook path: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "must start with") {
		t.Fatalf("error lost its guidance: %q", err.Error())
	}
}

func TestSafeURLLabel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://hooks.slack.com/services/T/B/X", "https://hooks.slack.com"},
		{"http://example.com/a/b", "http://example.com"},
		{"", "<empty>"},
		{"://nonsense", "<malformed>"},
		{"not-a-url", "<malformed>"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := safeURLLabel(tc.in); got != tc.want {
				t.Fatalf("safeURLLabel(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
