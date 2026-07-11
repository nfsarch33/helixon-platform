package llm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrQueueFull is returned when the request queue has reached capacity.
	ErrQueueFull = errors.New("request queue full")
	// ErrQueueShutdown is returned when requests arrive during shutdown.
	ErrQueueShutdown = errors.New("request queue shutting down")
)

// QueueConfig controls the request queue behavior.
type QueueConfig struct {
	MaxPending        int           // max queued requests; 0 = 100
	MaxConcurrent     int           // max inflight requests; 0 = 4
	MaxMemoryMB       int64         // soft memory budget in MB; 0 = 512
	RequestTimeout    time.Duration // per-request timeout; 0 = 5min
	BackpressureDelay time.Duration // delay when memory pressure detected; 0 = 2s
}

// DefaultQueueConfig returns conservative defaults for OOM prevention.
func DefaultQueueConfig() QueueConfig {
	return QueueConfig{
		MaxPending:        100,
		MaxConcurrent:     4,
		MaxMemoryMB:       512,
		RequestTimeout:    5 * time.Minute,
		BackpressureDelay: 2 * time.Second,
	}
}

type queuedRequest struct {
	ctx     context.Context
	req     CompletionRequest
	respCh  chan *CompletionResponse
	errCh   chan error
	created time.Time
}

// QueuedProvider wraps a Provider with a bounded request queue for backpressure
// and OOM prevention. It limits both concurrency and queue depth.
type QueuedProvider struct {
	inner  Provider
	config QueueConfig

	pending     chan *queuedRequest
	inflight    atomic.Int64
	memoryBytes atomic.Int64

	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup

	// For testing: override memory estimation
	estimateMemory func(CompletionRequest) int64
}

// NewQueuedProvider creates a queue-wrapped provider.
func NewQueuedProvider(inner Provider, cfg QueueConfig) *QueuedProvider {
	if cfg.MaxPending <= 0 {
		cfg.MaxPending = 100
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 4
	}
	if cfg.MaxMemoryMB <= 0 {
		cfg.MaxMemoryMB = 512
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 5 * time.Minute
	}
	if cfg.BackpressureDelay <= 0 {
		cfg.BackpressureDelay = 2 * time.Second
	}

	q := &QueuedProvider{
		inner:          inner,
		config:         cfg,
		pending:        make(chan *queuedRequest, cfg.MaxPending),
		done:           make(chan struct{}),
		estimateMemory: defaultEstimateMemory,
	}

	q.wg.Add(cfg.MaxConcurrent)
	for i := 0; i < cfg.MaxConcurrent; i++ {
		go q.worker()
	}

	return q
}

// Complete enqueues a request. Blocks if queue is full (respects context).
//
// The orchestrator is split into four focused helpers so each has low CC
// and is independently testable (refactor v17804-4 from CC 15):
//
//	checkMemoryBudget - log + enforce soft memory ceiling with backpressure delay
//	newPendingRequest - construct the queuedRequest envelope
//	enqueueOrShutdown - put the request on the pending channel or fail fast
//	waitForResponse   - drain the response/error channels or surface shutdown
func (q *QueuedProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if err := q.checkMemoryBudget(ctx, req); err != nil {
		return nil, err
	}
	qr := newPendingRequest(ctx, req)
	if err := enqueueOrShutdown(ctx, q, qr); err != nil {
		return nil, err
	}
	return waitForResponse(ctx, qr, q.doneRef())
}

// checkMemoryBudget logs a backpressure warning and, if the memory ceiling
// is still exceeded after the configured delay, returns ErrQueueFull. Returns
// nil when the request fits, or an error wrapping the context when ctx is
// cancelled during the delay.
func (q *QueuedProvider) checkMemoryBudget(ctx context.Context, req CompletionRequest) error {
	memEst := q.estimateMemory(req)
	maxMem := q.config.MaxMemoryMB * 1024 * 1024
	if q.memoryBytes.Load()+memEst <= maxMem {
		return nil
	}
	log.Printf("[queue] backpressure: estimated memory %dMB would exceed %dMB limit",
		(q.memoryBytes.Load()+memEst)/(1024*1024), q.config.MaxMemoryMB)
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: backpressure delay", ctx.Err())
	case <-time.After(q.config.BackpressureDelay):
	}
	if q.memoryBytes.Load()+memEst > maxMem {
		return fmt.Errorf("%w: memory budget exceeded (%dMB / %dMB)",
			ErrQueueFull, q.memoryBytes.Load()/(1024*1024), q.config.MaxMemoryMB)
	}
	return nil
}

// newPendingRequest builds a fresh queuedRequest envelope with buffered
// response and error channels of capacity 1 (matches worker behaviour).
// Extracted from Complete so tests can construct envelopes without spinning
// a full QueuedProvider.
func newPendingRequest(ctx context.Context, req CompletionRequest) *queuedRequest {
	return &queuedRequest{
		ctx:     ctx,
		req:     req,
		respCh:  make(chan *CompletionResponse, 1),
		errCh:   make(chan error, 1),
		created: time.Now(),
	}
}

// enqueueOrShutdown pushes the request onto the pending channel and returns
// the qr pointer (for the caller to wait on). Returns ErrQueueShutdown when
// the provider is closed, or ctx.Err() if the caller cancels while waiting.
func enqueueOrShutdown(ctx context.Context, q *QueuedProvider, qr *queuedRequest) error {
	select {
	case <-q.done:
		return ErrQueueShutdown
	default:
	}
	select {
	case q.pending <- qr:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-q.done:
		return ErrQueueShutdown
	}
}

// waitForResponse blocks until the worker delivers a response, an error, or
// the done/ctx channel fires. On done it drains any in-flight value before
// returning ErrQueueShutdown so callers do not lose work.
func waitForResponse(ctx context.Context, qr *queuedRequest, done *chan struct{}) (*CompletionResponse, error) {
	if done == nil {
		select {
		case resp := <-qr.respCh:
			return resp, nil
		case err := <-qr.errCh:
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var doneCh <-chan struct{} = *done
	select {
	case resp := <-qr.respCh:
		return resp, nil
	case err := <-qr.errCh:
		return nil, err
	case <-doneCh:
		select {
		case resp := <-qr.respCh:
			return resp, nil
		case err := <-qr.errCh:
			return nil, err
		default:
			return nil, ErrQueueShutdown
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// doneRef returns a pointer to the receiver's done channel. Exists so the
// test suite can exercise waitForResponse with a stand-alone struct without
// reaching into unexported fields.
func (q *QueuedProvider) doneRef() *chan struct{} {
	return &q.done
}

func (q *QueuedProvider) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.done:
			// Drain remaining pending requests before exiting
			for {
				select {
				case qr, ok := <-q.pending:
					if !ok {
						return
					}
					q.processRequest(qr)
				default:
					return
				}
			}
		case qr, ok := <-q.pending:
			if !ok {
				return
			}
			q.processRequest(qr)
		}
	}
}

func (q *QueuedProvider) processRequest(qr *queuedRequest) {
	memEst := q.estimateMemory(qr.req)
	q.memoryBytes.Add(memEst)
	q.inflight.Add(1)

	defer func() {
		q.inflight.Add(-1)
		q.memoryBytes.Add(-memEst)
	}()

	ctx := qr.ctx
	if q.config.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.config.RequestTimeout)
		defer cancel()
	}

	resp, err := q.inner.Complete(ctx, qr.req)
	if err != nil {
		qr.errCh <- err
		return
	}
	qr.respCh <- resp
}

func defaultEstimateMemory(req CompletionRequest) int64 {
	var total int64
	for _, m := range req.Messages {
		total += int64(len(m.Content)) * 4 // ~4 bytes per char for tokenization overhead
	}
	if total < 1024 {
		total = 1024
	}
	return total
}

// Shutdown gracefully drains and stops the queue.
func (q *QueuedProvider) Shutdown(ctx context.Context) error {
	q.once.Do(func() {
		close(q.done)
	})

	ch := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(ch)
	}()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// QueueStats returns current queue state for observability.
type QueueStats struct {
	PendingRequests  int
	InflightRequests int64
	MemoryUsedMB     int64
	MemoryLimitMB    int64
}

// Stats returns a snapshot of queue state.
func (q *QueuedProvider) Stats() QueueStats {
	return QueueStats{
		PendingRequests:  len(q.pending),
		InflightRequests: q.inflight.Load(),
		MemoryUsedMB:     q.memoryBytes.Load() / (1024 * 1024),
		MemoryLimitMB:    q.config.MaxMemoryMB,
	}
}
