package llm

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimited_AllowsWithinLimit(t *testing.T) {
	mock := &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "ok"}}},
		},
	}
	rl := NewRateLimitedProvider("test", mock, RateLimitConfig{
		RequestsPerMinute: 5,
		Window:            time.Minute,
	})

	for i := 0; i < 5; i++ {
		resp, err := rl.Complete(context.Background(), CompletionRequest{
			Messages: []Message{{Role: "user", Content: "hi"}},
		})
		require.NoError(t, err)
		assert.Equal(t, "ok", resp.Choices[0].Message.Content)
	}
}

func TestRateLimited_BlocksWhenExceeded(t *testing.T) {
	mock := &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "ok"}}},
		},
	}
	rl := NewRateLimitedProvider("test", mock, RateLimitConfig{
		RequestsPerMinute: 2,
		Window:            100 * time.Millisecond,
		BurstSize:         0,
	})

	// Use up the limit
	for i := 0; i < 2; i++ {
		_, err := rl.Complete(context.Background(), CompletionRequest{
			Messages: []Message{{Role: "user", Content: "hi"}},
		})
		require.NoError(t, err)
	}

	// Third request should block until window expires, then succeed
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	resp, err := rl.Complete(ctx, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Choices[0].Message.Content)
}

func TestRateLimited_ContextCancelledWhileWaiting(t *testing.T) {
	mock := &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "ok"}}},
		},
	}
	rl := NewRateLimitedProvider("test", mock, RateLimitConfig{
		RequestsPerMinute: 1,
		Window:            10 * time.Second,
		BurstSize:         0,
	})

	// Use up the limit
	_, err := rl.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	// Second request with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = rl.Complete(ctx, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context")
}

func TestRateLimited_BurstAllowsExtraRequests(t *testing.T) {
	mock := &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "ok"}}},
		},
	}
	rl := NewRateLimitedProvider("test", mock, RateLimitConfig{
		RequestsPerMinute: 2,
		Window:            time.Minute,
		BurstSize:         3,
	})

	// Should allow 2+3=5 requests in burst
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := rl.Complete(ctx, CompletionRequest{
			Messages: []Message{{Role: "user", Content: "hi"}},
		})
		cancel()
		require.NoError(t, err)
	}
}

func TestRateLimited_Stats(t *testing.T) {
	mock := &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "ok"}}},
		},
	}
	rl := NewRateLimitedProvider("test", mock, RateLimitConfig{
		RequestsPerMinute: 10,
		Window:            time.Minute,
		BurstSize:         5,
	})

	active, max := rl.Stats()
	assert.Equal(t, 0, active)
	assert.Equal(t, 15, max)

	_, _ = rl.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})

	active, max = rl.Stats()
	assert.Equal(t, 1, active)
	assert.Equal(t, 15, max)
}

func TestRateLimited_ConcurrentRequests(t *testing.T) {
	mock := &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "ok"}}},
		},
	}
	rl := NewRateLimitedProvider("test", mock, RateLimitConfig{
		RequestsPerMinute: 50,
		Window:            time.Minute,
		BurstSize:         0,
	})

	var wg sync.WaitGroup
	errCh := make(chan error, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := rl.Complete(context.Background(), CompletionRequest{
				Messages: []Message{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRateLimited_DefaultConfig(t *testing.T) {
	cfg := DefaultRateLimitConfig()
	assert.Equal(t, 10, cfg.RequestsPerMinute)
	assert.Equal(t, time.Minute, cfg.Window)
	assert.Equal(t, 2, cfg.BurstSize)
}

func TestRateLimited_WindowZeroDefaultsToMinute(t *testing.T) {
	rl := NewRateLimitedProvider("test", &testProvider{}, RateLimitConfig{
		RequestsPerMinute: 5,
	})
	assert.Equal(t, time.Minute, rl.config.Window)
}
