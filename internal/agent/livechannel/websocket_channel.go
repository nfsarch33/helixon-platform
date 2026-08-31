// Package livechannel provides a real-time WebSocket channel for Helixon Agent
// activity events. Subscribers receive a stream of structured events
// (agent_started, step_completed, tool_invoked, agent_completed) as the
// agent runs.
//
// Server endpoint: ws://host:port/agent/live
//
// Optional bearer-token authentication is enforced when ChannelConfig.AuthToken
// is set (Authorization: Bearer <token>).
package livechannel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// EventType enumerates the kinds of events emitted on the channel.
type EventType string

const (
	EventAgentStarted   EventType = "agent_started"
	EventStepCompleted  EventType = "step_completed"
	EventToolInvoked    EventType = "tool_invoked"
	EventAgentCompleted EventType = "agent_completed"
)

// Event is one message on the channel. JSON-serializable.
type Event struct {
	Type      EventType      `json:"type"`
	JobID     string         `json:"job_id"`
	Timestamp time.Time      `json:"timestamp"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// ChannelConfig configures a Channel.
type ChannelConfig struct {
	// ChannelBuffer is the per-subscriber outbound buffer size. Default 16.
	ChannelBuffer int
	// AuthToken, when non-empty, requires Authorization: Bearer <AuthToken>.
	AuthToken string
	// WriteTimeout is the maximum time to wait for a subscriber's write.
	// Default 5s.
	WriteTimeout time.Duration
}

// Channel is the in-process pub/sub hub for live agent events.
//
// All methods are safe for concurrent use. A single Channel instance is the
// appropriate scope for one agent process or one agent fleet.
type Channel struct {
	cfg    ChannelConfig
	mu     sync.RWMutex
	subs   map[*Subscriber]struct{}
	closed atomic.Bool
	subCnt atomic.Int64
	wg     sync.WaitGroup
	stopCh chan struct{}
}

// Subscriber represents one WebSocket connection subscribed to the channel.
type Subscriber struct {
	id        int64
	conn      *websocket.Conn
	ch        chan Event
	done      chan struct{}
	closeOnce sync.Once
}

var nextSubscriberID atomic.Int64

// NewChannel creates a Channel with the given config. Defaults are applied
// for any zero-valued fields.
func NewChannel(cfg ChannelConfig) *Channel {
	if cfg.ChannelBuffer <= 0 {
		cfg.ChannelBuffer = 16
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	return &Channel{
		cfg:    cfg,
		subs:   make(map[*Subscriber]struct{}),
		stopCh: make(chan struct{}),
	}
}

// Publish fans out an event to all current subscribers. If no subscribers
// exist, the event is dropped (the call returns immediately, never blocks).
//
// Publish is safe for concurrent use.
func (c *Channel) Publish(ev Event) {
	if c.closed.Load() {
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for sub := range c.subs {
		// Non-blocking send: drop event for slow subscribers rather than
		// blocking the producer. Slow subscribers are eventually cleaned up
		// when their write fails.
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

// teardown signals the subscriber's loops to exit and closes its connection.
// It is idempotent and safe to call from any goroutine.
//
// Callers must not hold Channel.mu when calling teardown, and must not take
// Channel.mu from inside the once-func. sync.Once is itself a lock -- a second
// caller blocks inside Do until the first caller's function returns -- so a
// path that holds mu while entering the Once deadlocks against a path that
// holds the Once while taking mu. Close and Unsubscribe both did exactly that
// and hung. Every mutation of the subscriber set now happens under mu and is
// finished before the Once is entered.
func (s *Subscriber) teardown() {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.conn.Close()
	})
}

// Subscribe registers a WebSocket connection as a new subscriber and returns
// the Subscriber handle (used to unregister). If the channel is already
// closed, the returned Subscriber is already torn down: its done channel is
// closed and the connection is closed, so callers blocking on sub.done return
// immediately.
func (c *Channel) Subscribe(conn *websocket.Conn) *Subscriber {
	sub := &Subscriber{
		id:   nextSubscriberID.Add(1),
		conn: conn,
		ch:   make(chan Event, c.cfg.ChannelBuffer),
		done: make(chan struct{}),
	}

	c.mu.Lock()
	if c.closed.Load() {
		// Close has already swept the subscriber set. Registering now would
		// put SubscriberCount back above zero after Close returned, and would
		// run c.wg.Add against the c.wg.Wait in Close -- a WaitGroup misuse
		// panic. Close sets c.closed before it takes c.mu, so this check under
		// c.mu cannot miss a Close that is already under way.
		c.mu.Unlock()
		sub.teardown()
		return sub
	}
	c.subs[sub] = struct{}{}
	c.subCnt.Add(1)
	// Both loops join the WaitGroup under c.mu, so the Add is ordered before
	// the c.wg.Wait in Close, which only runs after acquiring c.mu.
	c.wg.Add(2)
	c.mu.Unlock()

	go c.readLoop(sub)
	go c.writeLoop(sub)
	return sub
}

// Unsubscribe removes a subscriber from the fan-out set and tears it down.
// It is idempotent.
func (c *Channel) Unsubscribe(sub *Subscriber) {
	c.mu.Lock()
	if _, ok := c.subs[sub]; ok {
		delete(c.subs, sub)
		c.subCnt.Add(-1)
	}
	c.mu.Unlock()
	// Outside the lock: see the ordering note on teardown.
	sub.teardown()
}

// SubscriberCount returns the current number of active subscribers.
func (c *Channel) SubscriberCount() int {
	return int(c.subCnt.Load())
}

// WaitForSubscribers blocks until at least n subscribers are registered or
// the timeout elapses.
func (c *Channel) WaitForSubscribers(n int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.SubscriberCount() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Close stops accepting new subscribers, closes all active ones, and waits for
// their read and write loops to exit. It is idempotent.
func (c *Channel) Close() {
	if c.closed.Swap(true) {
		return
	}
	close(c.stopCh)

	// Snapshot and clear the subscriber set under the lock, then release it.
	// The teardown below takes each subscriber's closeOnce; running it here
	// under c.mu is the lock-order inversion that deadlocked Close against
	// Unsubscribe. See the ordering note on teardown.
	c.mu.Lock()
	subs := make([]*Subscriber, 0, len(c.subs))
	for sub := range c.subs {
		subs = append(subs, sub)
	}
	c.subs = make(map[*Subscriber]struct{})
	c.subCnt.Store(0)
	c.mu.Unlock()

	for _, sub := range subs {
		sub.teardown()
	}
	c.wg.Wait()
}

// ValidateToken returns true if the request's Authorization header matches
// the configured AuthToken (when one is configured). When AuthToken is empty,
// any token is accepted.
func (c *Channel) ValidateToken(authHeader string) bool {
	if c.cfg.AuthToken == "" {
		return true
	}
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return false
	}
	return strings.TrimPrefix(authHeader, "Bearer ") == c.cfg.AuthToken
}

// ServeWS is the HTTP handler that upgrades the connection to WebSocket and
// subscribes it. Returns HTTP 401 if the auth token check fails.
func (c *Channel) ServeWS(w http.ResponseWriter, r *http.Request) {
	if c.closed.Load() {
		http.Error(w, "channel closed", http.StatusServiceUnavailable)
		return
	}
	if !c.ValidateToken(r.Header.Get("Authorization")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote the response on failure.
		return
	}
	sub := c.Subscribe(conn)
	// ServeWS blocks until the subscriber is removed; this keeps the request
	// alive so the server doesn't return early.
	<-sub.done
}

var websocketUpgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true }, //nolint:revive // unused-parameter required by interface
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// readLoop drains incoming messages from the WebSocket. We don't expect
// client-to-server messages today, but we must read to keep the connection
// alive (and detect close). On any read error, unsubscribe.
func (c *Channel) readLoop(sub *Subscriber) {
	defer c.wg.Done()
	defer c.Unsubscribe(sub)
	for {
		if _, _, err := sub.conn.ReadMessage(); err != nil {
			return
		}
	}
}

// writeLoop copies events from the subscriber's channel to the WebSocket
// connection. On any write error or close, it returns (causing unsubscribe).
func (c *Channel) writeLoop(sub *Subscriber) {
	defer c.wg.Done()
	defer c.Unsubscribe(sub)
	for {
		select {
		case <-sub.done:
			return
		case <-c.stopCh:
			return
		case ev, ok := <-sub.ch:
			if !ok {
				return
			}
			body, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_ = sub.conn.SetWriteDeadline(time.Now().Add(c.cfg.WriteTimeout))
			if err := sub.conn.WriteMessage(websocket.TextMessage, body); err != nil {
				return
			}
		}
	}
}

// ContextPublish publishes an event with a context for cancellation. Used
// in tests and callers that want a context-aware publish.
func (c *Channel) ContextPublish(ctx context.Context, ev Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.Publish(ev)
	return nil
}

// ErrChannelClosed is returned by ServeWS when the channel has been closed.
var ErrChannelClosed = errors.New("channel closed")

// Ensure the ServeWS signature matches http.HandlerFunc.
var _ http.HandlerFunc = (*Channel)(nil).ServeWS

// fmtString returns the channel URL pattern for logging.
func (c *Channel) fmtString() string {
	return fmt.Sprintf("channel(subs=%d,closed=%v)", c.SubscriberCount(), c.closed.Load())
}

// String implements fmt.Stringer for log lines.
func (c *Channel) String() string { return c.fmtString() }
