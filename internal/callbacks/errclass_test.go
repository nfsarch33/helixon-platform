package callbacks_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nfsarch33/helixon-platform/internal/callbacks"
)

func TestClassifyError_Timeout(t *testing.T) {
	t.Parallel()
	assert.Equal(t, callbacks.ErrorClassTimeout, callbacks.ClassifyError(context.DeadlineExceeded))
	assert.Equal(t, callbacks.ErrorClassTimeout, callbacks.ClassifyError(context.Canceled))
	assert.Equal(t, callbacks.ErrorClassTimeout,
		callbacks.ClassifyError(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)))
}

func TestClassifyError_Transient(t *testing.T) {
	t.Parallel()
	transients := []string{
		"connection refused",
		"connection reset by peer",
		"temporary failure in name resolution",
		"service unavailable: try again",
		"too many requests",
		"rate limit exceeded",
		"i/o timeout",
		"dial tcp 10.0.0.1:8080: connect: connection refused",
	}
	for _, msg := range transients {
		assert.Equal(t, callbacks.ErrorClassTransient,
			callbacks.ClassifyError(errors.New(msg)), "should classify %q as transient", msg)
	}
}

func TestClassifyError_Permanent(t *testing.T) {
	t.Parallel()
	permanents := []string{
		"not found",
		"permission denied",
		"unauthorized access",
		"invalid argument: field X",
		"validation failed for input",
		"schema mismatch on column Y",
		"unsupported operation",
	}
	for _, msg := range permanents {
		assert.Equal(t, callbacks.ErrorClassPermanent,
			callbacks.ClassifyError(errors.New(msg)), "should classify %q as permanent", msg)
	}
}

func TestClassifyError_Unknown(t *testing.T) {
	t.Parallel()
	assert.Equal(t, callbacks.ErrorClassUnknown, callbacks.ClassifyError(errors.New("something weird")))
	assert.Equal(t, callbacks.ErrorClassUnknown, callbacks.ClassifyError(nil))
}

func TestClassifiedError_Unwrap(t *testing.T) {
	t.Parallel()
	inner := errors.New("original error")
	ce := &callbacks.ClassifiedError{
		Original:  inner,
		Class:     callbacks.ErrorClassTransient,
		Component: "fetcher",
		RunID:     "run-1",
		Retryable: true,
	}
	assert.ErrorIs(t, ce, inner)
	assert.Contains(t, ce.Error(), "transient")
	assert.Contains(t, ce.Error(), "fetcher")
}

func TestClassifiedErrorContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assert.Nil(t, callbacks.ClassifiedErrorFromContext(ctx))

	ce := &callbacks.ClassifiedError{
		Original:  errors.New("test"),
		Class:     callbacks.ErrorClassPermanent,
		Component: "validator",
	}
	ctx = callbacks.WithClassifiedError(ctx, ce)
	got := callbacks.ClassifiedErrorFromContext(ctx)
	require.NotNil(t, got)
	assert.Equal(t, callbacks.ErrorClassPermanent, got.Class)
	assert.Equal(t, "validator", got.Component)
}

func TestErrorClassifyingHandler_OnError(t *testing.T) {
	t.Parallel()
	inner := &callbacks.NoopHandler{}
	h := &callbacks.ErrorClassifyingHandler{Inner: inner}

	info := &callbacks.RunInfo{
		ComponentName: "web-fetch",
		RunID:         "run-42",
	}

	ctx := h.OnError(context.Background(), info, errors.New("connection refused"))
	ce := callbacks.ClassifiedErrorFromContext(ctx)
	require.NotNil(t, ce)
	assert.Equal(t, callbacks.ErrorClassTransient, ce.Class)
	assert.True(t, ce.Retryable)
	assert.Equal(t, "web-fetch", ce.Component)
	assert.Equal(t, "run-42", ce.RunID)
}

func TestErrorClassifyingHandler_OnError_Permanent(t *testing.T) {
	t.Parallel()
	h := &callbacks.ErrorClassifyingHandler{Inner: nil}

	info := &callbacks.RunInfo{ComponentName: "parser", RunID: "run-99"}
	ctx := h.OnError(context.Background(), info, errors.New("validation failed"))
	ce := callbacks.ClassifiedErrorFromContext(ctx)
	require.NotNil(t, ce)
	assert.Equal(t, callbacks.ErrorClassPermanent, ce.Class)
	assert.False(t, ce.Retryable)
}

func TestErrorClassifyingHandler_Passthrough(t *testing.T) {
	t.Parallel()
	h := &callbacks.ErrorClassifyingHandler{Inner: &callbacks.NoopHandler{}}

	info := &callbacks.RunInfo{ComponentName: "pass", RunID: "p-1"}
	ctx := h.OnStart(context.Background(), info, "input")
	assert.NotNil(t, ctx)
	ctx = h.OnEnd(ctx, info, "output")
	assert.NotNil(t, ctx)
}

func TestErrorClassifyingHandler_NilInner(t *testing.T) {
	t.Parallel()
	h := &callbacks.ErrorClassifyingHandler{Inner: nil}

	info := &callbacks.RunInfo{ComponentName: "nil-inner", RunID: "n-1"}
	ctx := h.OnStart(context.Background(), info, nil)
	assert.NotNil(t, ctx)
	ctx = h.OnEnd(ctx, info, nil)
	assert.NotNil(t, ctx)
}
