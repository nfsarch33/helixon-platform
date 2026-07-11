package llm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestEnqueueHelper_ReturnsShutdownWhenClosed proves enqueueOrShutdown never
// blocks once the queue is shutdown.
func TestEnqueueHelper_ReturnsShutdownWhenClosed(t *testing.T) {
	t.Parallel()
	q := newTestQueue()
	close(q.done)
	qr := newPendingRequest(context.Background(), CompletionRequest{})
	err := enqueueOrShutdown(context.Background(), q, qr)
	if !errors.Is(err, ErrQueueShutdown) {
		t.Errorf("got %v, want ErrQueueShutdown", err)
	}
}

// TestEnqueueHelper_HappyPath returns nil on success.
func TestEnqueueHelper_HappyPath(t *testing.T) {
	t.Parallel()
	q := newTestQueue()
	defer close(q.done)
	qr := newPendingRequest(context.Background(), CompletionRequest{})
	if err := enqueueOrShutdown(context.Background(), q, qr); err != nil {
		t.Fatalf("enqueueOrShutdown: %v", err)
	}
}

// TestEnqueueHelper_RespectsContextCancel confirms ctx.Done wins when the
// pending channel is full (capacity 0).
func TestEnqueueHelper_RespectsContextCancel(t *testing.T) {
	t.Parallel()
	q := newTestQueue()
	defer close(q.done)
	q.pending = make(chan *queuedRequest) // capacity 0 -> all sends block
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	qr := newPendingRequest(context.Background(), CompletionRequest{})
	err := enqueueOrShutdown(ctx, q, qr)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

// TestWaitForResponse_HappyPath receives a response from respCh.
func TestWaitForResponse_HappyPath(t *testing.T) {
	t.Parallel()
	qr := newPendingRequest(context.Background(), CompletionRequest{})
	go func() {
		qr.respCh <- &CompletionResponse{Choices: []Choice{{Message: Message{Content: "hello"}}}}
	}()
	resp, err := waitForResponse(context.Background(), qr, nil)
	if err != nil {
		t.Fatalf("waitForResponse: %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "hello" {
		t.Errorf("response content: got %+v, want 'hello'", resp)
	}
}

// TestWaitForResponse_ErrorPath surfaces errors that come through errCh.
func TestWaitForResponse_ErrorPath(t *testing.T) {
	t.Parallel()
	qr := newPendingRequest(context.Background(), CompletionRequest{})
	want := errors.New("test error")
	go func() {
		qr.errCh <- want
	}()
	_, err := waitForResponse(context.Background(), qr, nil)
	if !errors.Is(err, want) {
		t.Errorf("got %v, want %v", err, want)
	}
}

// TestWaitForResponse_ShutdownDrains: when done is signaled the helper must
// drain any in-flight response before returning ErrQueueShutdown.
func TestWaitForResponse_ShutdownDrains(t *testing.T) {
	t.Parallel()
	qr := newPendingRequest(context.Background(), CompletionRequest{})
	// Pre-load the resp channel so the drain branch always finds a value.
	qr.respCh <- &CompletionResponse{Choices: []Choice{{Message: Message{Content: "late"}}}}
	doneCh := make(chan struct{})
	close(doneCh)
	resp, err := waitForResponse(context.Background(), qr, &doneCh)
	if err != nil {
		t.Fatalf("drain path should have delivered resp; got err %v", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "late" {
		t.Errorf("got %+v, want late response", resp)
	}
}

// TestWaitForResponse_ShutdownImmediateNoDrain returns ErrQueueShutdown when
// done is signaled and no resp/err is pending.
func TestWaitForResponse_ShutdownImmediateNoDrain(t *testing.T) {
	t.Parallel()
	qr := newPendingRequest(context.Background(), CompletionRequest{})
	doneCh := make(chan struct{})
	close(doneCh)
	_, err := waitForResponse(context.Background(), qr, &doneCh)
	if !errors.Is(err, ErrQueueShutdown) {
		t.Errorf("got %v, want ErrQueueShutdown", err)
	}
}

// TestWaitForResponse_ContextCancel returns ctx.Err() when done before either
// channel produces a value.
func TestWaitForResponse_ContextCancel(t *testing.T) {
	t.Parallel()
	qr := newPendingRequest(context.Background(), CompletionRequest{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := waitForResponse(ctx, qr, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

// newTestQueue returns a minimal QueuedProvider that satisfies the helpers
// tests without spinning worker goroutines.
func newTestQueue() *QueuedProvider {
	return &QueuedProvider{
		pending: make(chan *queuedRequest, 1),
		done:    make(chan struct{}),
		wg:      sync.WaitGroup{},
	}
}

// silence unused imports in case tests are filtered.
var _ = atomic.LoadInt64
