package llm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueuedProvider_BasicCompletion(t *testing.T) {
	mock := &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "queued-ok"}}},
		},
	}
	q := NewQueuedProvider(mock, QueueConfig{
		MaxPending:    10,
		MaxConcurrent: 2,
		MaxMemoryMB:   100,
	})
	defer func() {
		_ = q.Shutdown(context.Background())
	}()

	resp, err := q.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "queued-ok", resp.Choices[0].Message.Content)
}

func TestQueuedProvider_PropagatesErrors(t *testing.T) {
	mock := &testProvider{err: errors.New("inner failed")}
	q := NewQueuedProvider(mock, QueueConfig{
		MaxPending:    10,
		MaxConcurrent: 2,
		MaxMemoryMB:   100,
	})
	defer func() {
		_ = q.Shutdown(context.Background())
	}()

	_, err := q.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inner failed")
}

func TestQueuedProvider_ConcurrencyLimit(t *testing.T) {
	var inflight atomic.Int64
	var maxInflight atomic.Int64

	slow := &testProvider{
		fn: func(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
			cur := inflight.Add(1)
			defer inflight.Add(-1)
			for {
				old := maxInflight.Load()
				if cur <= old || maxInflight.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			return &CompletionResponse{
				Choices: []Choice{{Message: Message{Content: "ok"}}},
			}, nil
		},
	}

	q := NewQueuedProvider(slow, QueueConfig{
		MaxPending:    20,
		MaxConcurrent: 3,
		MaxMemoryMB:   100,
	})
	defer func() {
		_ = q.Shutdown(context.Background())
	}()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = q.Complete(context.Background(), CompletionRequest{
				Messages: []Message{{Role: "user", Content: "hi"}},
			})
		}()
	}
	wg.Wait()

	assert.LessOrEqual(t, maxInflight.Load(), int64(3),
		"should never exceed MaxConcurrent")
}

func TestQueuedProvider_ContextCancelled(t *testing.T) {
	slow := &testProvider{
		fn: func(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
			time.Sleep(5 * time.Second)
			return &CompletionResponse{}, nil
		},
	}
	q := NewQueuedProvider(slow, QueueConfig{
		MaxPending:    10,
		MaxConcurrent: 1,
		MaxMemoryMB:   100,
	})
	defer func() {
		_ = q.Shutdown(context.Background())
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := q.Complete(ctx, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
}

func TestQueuedProvider_ShutdownRejectsNew(t *testing.T) {
	mock := &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "ok"}}},
		},
	}
	q := NewQueuedProvider(mock, QueueConfig{
		MaxPending:    10,
		MaxConcurrent: 2,
		MaxMemoryMB:   100,
	})

	err := q.Shutdown(context.Background())
	require.NoError(t, err)

	_, err = q.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrQueueShutdown)
}

func TestQueuedProvider_MemoryBackpressure(t *testing.T) {
	mock := &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "ok"}}},
		},
	}
	q := NewQueuedProvider(mock, QueueConfig{
		MaxPending:        10,
		MaxConcurrent:     2,
		MaxMemoryMB:       1,
		BackpressureDelay: 10 * time.Millisecond,
	})
	defer func() {
		_ = q.Shutdown(context.Background())
	}()

	q.estimateMemory = func(_ CompletionRequest) int64 {
		return 2 * 1024 * 1024
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := q.Complete(ctx, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "very large request"}},
	})
	require.Error(t, err)
}

func TestQueuedProvider_Stats(t *testing.T) {
	mock := &testProvider{
		fn: func(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
			time.Sleep(100 * time.Millisecond)
			return &CompletionResponse{
				Choices: []Choice{{Message: Message{Content: "ok"}}},
			}, nil
		},
	}
	q := NewQueuedProvider(mock, QueueConfig{
		MaxPending:    10,
		MaxConcurrent: 1,
		MaxMemoryMB:   100,
	})
	defer func() {
		_ = q.Shutdown(context.Background())
	}()

	stats := q.Stats()
	assert.Equal(t, 0, stats.PendingRequests)
	assert.Equal(t, int64(0), stats.InflightRequests)
	assert.Equal(t, int64(100), stats.MemoryLimitMB)
}

func TestQueuedProvider_DefaultConfig(t *testing.T) {
	cfg := DefaultQueueConfig()
	assert.Equal(t, 100, cfg.MaxPending)
	assert.Equal(t, 4, cfg.MaxConcurrent)
	assert.Equal(t, int64(512), cfg.MaxMemoryMB)
	assert.Equal(t, 5*time.Minute, cfg.RequestTimeout)
	assert.Equal(t, 2*time.Second, cfg.BackpressureDelay)
}

func TestQueuedProvider_GracefulShutdownDrains(t *testing.T) {
	var completed atomic.Int64
	slow := &testProvider{
		fn: func(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
			time.Sleep(50 * time.Millisecond)
			completed.Add(1)
			return &CompletionResponse{
				Choices: []Choice{{Message: Message{Content: "ok"}}},
			}, nil
		},
	}
	q := NewQueuedProvider(slow, QueueConfig{
		MaxPending:    10,
		MaxConcurrent: 2,
		MaxMemoryMB:   100,
	})

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = q.Complete(context.Background(), CompletionRequest{
				Messages: []Message{{Role: "user", Content: "hi"}},
			})
		}()
	}

	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := q.Shutdown(ctx)
	require.NoError(t, err)
	wg.Wait()
}

func TestDefaultEstimateMemory(t *testing.T) {
	req := CompletionRequest{
		Messages: []Message{{Role: "user", Content: "short"}},
	}
	est := defaultEstimateMemory(req)
	assert.Equal(t, int64(1024), est, "minimum 1KB for short messages")

	req = CompletionRequest{
		Messages: []Message{{Role: "user", Content: string(make([]byte, 1000))}},
	}
	est = defaultEstimateMemory(req)
	assert.Equal(t, int64(4000), est, "~4 bytes per char overhead")
}
