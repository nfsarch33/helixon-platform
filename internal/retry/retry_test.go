package retry

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultHTTPStatusRetryable(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{200, false},
		{201, false},
		{400, false}, // bad request: fail-fast
		{401, false}, // unauthorized: fail-fast
		{403, false}, // forbidden: fail-fast
		{404, false}, // not found: fail-fast
		{408, true},  // request timeout: retryable
		{429, true},  // too many requests: retryable
		{500, true},  // internal server error: retryable
		{502, true},  // bad gateway: retryable
		{503, true},  // service unavailable: retryable
		{504, true},  // gateway timeout: retryable
	}
	for _, tt := range tests {
		got := DefaultHTTPStatusRetryable(tt.status)
		assert.Equal(t, tt.want, got, "status %d", tt.status)
	}
}

func TestDo_SuccessFirstAttempt(t *testing.T) {
	p := Policy{}
	calls := 0
	result, err := Do(context.Background(), p, func(ctx context.Context, attempt int) (string, error) {
		calls++
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 1, calls)
}

func TestDo_4xxFailFast(t *testing.T) {
	p := Policy{MaxAttempts: 3, InitialBackoff: 10 * time.Millisecond}
	calls := 0
	_, err := Do(context.Background(), p, func(ctx context.Context, attempt int) (string, error) {
		calls++
		return "", &HTTPError{StatusCode: 404, Status: "Not Found"}
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls, "4xx should fail-fast (no retries)")
	var he *HTTPError
	require.True(t, errors.As(err, &he))
	assert.Equal(t, 404, he.StatusCode)
}

func TestDo_429RetriesThenExhausts(t *testing.T) {
	p := Policy{MaxAttempts: 3, InitialBackoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond}
	calls := 0
	_, err := Do(context.Background(), p, func(ctx context.Context, attempt int) (string, error) {
		calls++
		return "", &HTTPError{StatusCode: http.StatusTooManyRequests, Status: "Too Many Requests"}
	})
	require.Error(t, err)
	assert.Equal(t, 3, calls, "429 should retry up to MaxAttempts")
	assert.True(t, errors.Is(err, ErrExhausted), "should wrap ErrExhausted")
}

func TestDo_5xxRetriesThenSucceeds(t *testing.T) {
	p := Policy{MaxAttempts: 3, InitialBackoff: 5 * time.Millisecond}
	calls := 0
	result, err := Do(context.Background(), p, func(ctx context.Context, attempt int) (string, error) {
		calls++
		if calls < 2 {
			return "", &HTTPError{StatusCode: 503, Status: "Service Unavailable"}
		}
		return "recovered", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "recovered", result)
	assert.Equal(t, 2, calls, "should retry once then succeed")
}

func TestDo_NetworkErrorRetries(t *testing.T) {
	p := Policy{MaxAttempts: 3, InitialBackoff: 5 * time.Millisecond}
	calls := 0
	_, err := Do(context.Background(), p, func(ctx context.Context, attempt int) (string, error) {
		calls++
		return "", errors.New("connection reset by peer")
	})
	require.Error(t, err)
	assert.Equal(t, 3, calls, "non-HTTP errors should retry up to MaxAttempts")
}

func TestDo_ContextCancelled(t *testing.T) {
	p := Policy{MaxAttempts: 3, InitialBackoff: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := Do(ctx, p, func(ctx context.Context, attempt int) (string, error) {
		calls++
		return "", errors.New("transient")
	})
	require.Error(t, err)
	assert.Less(t, calls, 3, "context cancel should interrupt retries early")
	assert.True(t, errors.Is(err, context.Canceled) || errors.Is(err, ErrExhausted))
}

func TestPolicy_withDefaults(t *testing.T) {
	p := Policy{}.withDefaults()
	assert.Equal(t, 3, p.MaxAttempts)
	assert.Equal(t, 200*time.Millisecond, p.InitialBackoff)
	assert.Equal(t, 5*time.Second, p.MaxBackoff)
	assert.Equal(t, 0.2, p.JitterFraction)
	assert.NotNil(t, p.HTTPStatusRetryable)
}

func TestJitter(t *testing.T) {
	d := 1 * time.Second
	for i := 0; i < 100; i++ {
		j := jitter(d, 0.2)
		assert.GreaterOrEqual(t, j, 800*time.Millisecond, "lower bound")
		assert.LessOrEqual(t, j, 1200*time.Millisecond, "upper bound")
	}
}

func TestJitter_NegativeClamps(t *testing.T) {
	// jitter must never return < 0 even with extreme luck
	for i := 0; i < 100; i++ {
		j := jitter(1*time.Nanosecond, 1.0)
		assert.GreaterOrEqual(t, j, time.Duration(0))
	}
}