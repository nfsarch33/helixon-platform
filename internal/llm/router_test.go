package llm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testProvider is a minimal mock for router/queue/ratelimit tests.
type testProvider struct {
	resp *CompletionResponse
	err  error
	fn   func(context.Context, CompletionRequest) (*CompletionResponse, error)
}

func (tp *testProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if tp.fn != nil {
		return tp.fn(ctx, req)
	}
	return tp.resp, tp.err
}

func TestTieredRouter_RoutesByTierPriority(t *testing.T) {
	tier1 := NewTieredProvider("free", TierFree, &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "from-free"}}},
		},
	})
	tier3 := NewTieredProvider("paygo", TierPayAsYouGo, &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "from-paygo"}}},
		},
	})
	router := NewTieredRouter([]*TieredProvider{tier3, tier1})

	resp, err := router.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "from-free", resp.Choices[0].Message.Content)
}

func TestTieredRouter_FallsBackOnFailure(t *testing.T) {
	tier1 := NewTieredProvider("free", TierFree, &testProvider{
		err: errors.New("tier1 down"),
	})
	tier2 := NewTieredProvider("capped", TierCapped, &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "from-capped"}}},
		},
	})
	router := NewTieredRouter([]*TieredProvider{tier1, tier2})

	resp, err := router.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "from-capped", resp.Choices[0].Message.Content)
}

func TestTieredRouter_AllProvidersFail(t *testing.T) {
	tier1 := NewTieredProvider("free", TierFree, &testProvider{err: errors.New("fail1")})
	tier2 := NewTieredProvider("capped", TierCapped, &testProvider{err: errors.New("fail2")})
	router := NewTieredRouter([]*TieredProvider{tier1, tier2})

	_, err := router.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAllProvidersFailed)
}

func TestTieredRouter_SkipsCoolingDown(t *testing.T) {
	tier1 := NewTieredProvider("free", TierFree, &testProvider{err: errors.New("fail")})
	tier1.cooldown = 1 * time.Hour
	tier2 := NewTieredProvider("capped", TierCapped, &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "from-capped"}}},
		},
	})
	router := NewTieredRouter([]*TieredProvider{tier1, tier2})

	// First call: tier1 fails, falls back to tier2
	resp, err := router.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "from-capped", resp.Choices[0].Message.Content)

	// Second call: tier1 should be in cooldown, goes straight to tier2
	resp, err = router.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi again"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "from-capped", resp.Choices[0].Message.Content)
}

func TestTieredRouter_RecoveryAfterCooldown(t *testing.T) {
	tier1 := NewTieredProvider("free", TierFree, &testProvider{err: errors.New("fail")})
	tier1.cooldown = 10 * time.Millisecond
	tier2 := NewTieredProvider("capped", TierCapped, &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "from-capped"}}},
		},
	})
	router := NewTieredRouter([]*TieredProvider{tier1, tier2})

	// First call fails tier1, uses tier2
	_, err := router.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	// Wait for cooldown to expire
	time.Sleep(20 * time.Millisecond)

	// Replace tier1 with a working provider
	tier1.Provider = &testProvider{
		resp: &CompletionResponse{
			Choices: []Choice{{Message: Message{Content: "recovered-free"}}},
		},
	}

	resp, err := router.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "recovered-free", resp.Choices[0].Message.Content)
}

func TestTieredRouter_EmptyProviders(t *testing.T) {
	router := NewTieredRouter([]*TieredProvider{})
	_, err := router.Complete(context.Background(), CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAllProvidersFailed)
}

func TestTieredProvider_HealthTracking(t *testing.T) {
	tp := NewTieredProvider("test", TierFree, &testProvider{})
	assert.True(t, tp.isAvailable())

	tp.recordFailure()
	tp.recordFailure()
	tp.recordFailure()
	assert.False(t, tp.isAvailable(), "should be unhealthy after 3 consecutive failures")

	tp.recordSuccess()
	assert.True(t, tp.isAvailable(), "should recover after success")
}
