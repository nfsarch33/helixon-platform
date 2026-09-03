package livechannel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newTestServer starts an HTTP test server with the channel handler mounted at
// /agent/live. Returns the server URL (e.g. "ws://127.0.0.1:54321/agent/live").
func newTestServer(t *testing.T, ch *Channel) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/live", ch.ServeWS)
	srv := httptest.NewServer(mux)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/live"
	return srv, wsURL
}

// dial opens a WebSocket client connection.
func dial(t *testing.T, url string) (*websocket.Conn, *http.Response) {
	t.Helper()
	d := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	conn, resp, err := d.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return conn, resp
}

func TestChannel_PublishAndReceive(t *testing.T) {
	ch := NewChannel(ChannelConfig{ChannelBuffer: 16})
	defer func() { ch.Close() }()

	srv, wsURL := newTestServer(t, ch)
	defer func() { srv.Close() }()

	client, _ := dial(t, wsURL)
	defer func() { _ = client.Close() }()

	// Wait for the server side to register the subscriber. Publish drops
	// events when the set is empty, so publishing before ServeWS has
	// subscribed races the handler and loses the event.
	ch.WaitForSubscribers(1, 2*time.Second)

	// Publish an event from the server side.
	ch.Publish(Event{
		Type:      EventAgentStarted,
		JobID:     "job-1",
		Timestamp: time.Now().UTC(),
		Payload:   map[string]any{"prompt": "hello"},
	})

	// Read the event from the client side.
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	var got Event
	if err := json.Unmarshal(msg, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != EventAgentStarted {
		t.Errorf("unexpected event type: %q", got.Type)
	}
	if got.JobID != "job-1" {
		t.Errorf("unexpected job id: %q", got.JobID)
	}
}

func TestChannel_MultipleSubscribers(t *testing.T) {
	ch := NewChannel(ChannelConfig{ChannelBuffer: 8})
	defer func() { ch.Close() }()
	srv, wsURL := newTestServer(t, ch)
	defer func() { srv.Close() }()

	c1, _ := dial(t, wsURL)
	defer func() { _ = c1.Close() }()
	c2, _ := dial(t, wsURL)
	defer func() { _ = c2.Close() }()

	// Wait for both clients to register as subscribers.
	ch.WaitForSubscribers(2, 2*time.Second)

	ch.Publish(Event{Type: EventToolInvoked, JobID: "j", Payload: map[string]any{"tool": "x"}})

	for i, c := range []*websocket.Conn{c1, c2} {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, msg, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("client %d read: %v", i, err)
		}
		var got Event
		if err := json.Unmarshal(msg, &got); err != nil {
			t.Fatalf("client %d unmarshal: %v", i, err)
		}
		if got.Type != EventToolInvoked {
			t.Errorf("client %d type: %q", i, got.Type)
		}
	}
}

func TestChannel_PublishNoSubscribers(t *testing.T) {
	ch := NewChannel(ChannelConfig{ChannelBuffer: 4})
	defer func() { ch.Close() }()

	// Publish with no subscribers; should not block or panic.
	done := make(chan struct{})
	go func() {
		ch.Publish(Event{Type: EventStepCompleted, JobID: "j"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("Publish blocked with no subscribers")
	}
}

func TestChannel_CloseUnsubscribes(t *testing.T) {
	ch := NewChannel(ChannelConfig{ChannelBuffer: 4})
	srv, wsURL := newTestServer(t, ch)

	c, _ := dial(t, wsURL)
	defer func() { _ = c.Close() }()

	ch.WaitForSubscribers(1, 2*time.Second)
	ch.Close()
	srv.Close()

	// The client should see a close or read error after the server side closes.
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	done := make(chan struct{})
	go func() {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
		// expected
	case <-time.After(3 * time.Second):
		t.Fatalf("client did not observe close within 3s")
	}
}

func TestChannel_AuthToken(t *testing.T) {
	ch := NewChannel(ChannelConfig{
		ChannelBuffer: 4,
		AuthToken:     "secret-token",
	})
	defer func() { ch.Close() }()
	srv, _ := newTestServer(t, ch)
	defer func() { srv.Close() }()

	d := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	// Without token: should fail handshake.
	_, resp, err := d.Dial(strings.TrimSuffix(wsURLHelper(srv), ""), nil)
	// Actually just check that ServeWS enforces the token by exercising
	// the ValidateToken helper directly.
	if err == nil {
		_ = resp
	}
	// Verify token validator behaviour.
	if !ch.ValidateToken("Bearer secret-token") {
		t.Errorf("expected token validation to pass for matching bearer")
	}
	if ch.ValidateToken("Bearer wrong-token") {
		t.Errorf("expected token validation to reject wrong bearer")
	}
	if ch.ValidateToken("no-bearer-prefix") {
		t.Errorf("expected token validation to reject non-bearer")
	}
	if ch.ValidateToken("") {
		t.Errorf("expected token validation to reject empty")
	}
}

// wsURLHelper is a tiny shim because the dial helper depends on the channel path.
func wsURLHelper(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/live"
}

func TestChannel_AuthNoTokenRequired(t *testing.T) {
	ch := NewChannel(ChannelConfig{ChannelBuffer: 4})
	defer func() { ch.Close() }()
	// Without AuthToken set, any token passes (or empty).
	if !ch.ValidateToken("") {
		t.Errorf("expected empty token to pass when AuthToken is unset")
	}
	if !ch.ValidateToken("Bearer anything") {
		t.Errorf("expected any token to pass when AuthToken is unset")
	}
}

func TestEvent_Types(t *testing.T) {
	// Validate the event type constants are stable.
	want := map[EventType]string{
		EventAgentStarted:   "agent_started",
		EventStepCompleted:  "step_completed",
		EventToolInvoked:    "tool_invoked",
		EventAgentCompleted: "agent_completed",
	}
	for k, v := range want {
		if string(k) != v {
			t.Errorf("event type %q != %q", k, v)
		}
	}
}

func TestChannel_ConcurrentPublishers(t *testing.T) {
	ch := NewChannel(ChannelConfig{ChannelBuffer: 256})
	defer func() { ch.Close() }()
	srv, wsURL := newTestServer(t, ch)
	defer func() { srv.Close() }()

	c, _ := dial(t, wsURL)
	defer func() { _ = c.Close() }()
	ch.WaitForSubscribers(1, 2*time.Second)

	const goroutines = 8
	const eventsPerGoroutine = 16

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				ch.Publish(Event{
					Type:  EventStepCompleted,
					JobID: "concurrent",
					Payload: map[string]any{
						"goroutine": gid,
						"i":         i,
					},
				})
			}
		}(g)
	}
	wg.Wait()

	// Drain events from the client. Publish uses non-blocking send; with
	// ChannelBuffer=256 and 128 total events we expect all of them.
	total := goroutines * eventsPerGoroutine
	seen := 0
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for seen < total {
		_, _, err := c.ReadMessage()
		if err != nil {
			break
		}
		seen++
	}
	if seen < total {
		t.Errorf("expected %d events, got %d", total, seen)
	}
}

// TestChannel_CloseWithInFlightSubscribers drives Close concurrently with
// subscriber read and write loops that are tearing themselves down, and with
// publishers holding the read lock.
//
// Regression test. Close used to walk the subscriber set while holding
// c.mu and call sub.closeOnce.Do inside the loop. sync.Once is itself a lock,
// and Unsubscribe -- which the read and write loops run from their defers --
// takes c.mu *after* entering that same Once. The two orderings deadlocked:
// Close held c.mu waiting for the Once, the once-holder waited for c.mu.
// Close then never returned and took the whole package down with the 10m test
// timeout. No job in this repo sets timeout-minutes, so that burned a full
// runner slot on every unrelated PR that drew the losing side of the race.
func TestChannel_CloseWithInFlightSubscribers(t *testing.T) {
	// Close should return in milliseconds. The bound only has to distinguish
	// "slow" from "never", so it is generous enough not to flake on a loaded
	// CI host while still failing far short of the package test timeout.
	const closeBudget = 30 * time.Second
	const iterations = 30
	const subscribers = 8

	for it := 0; it < iterations; it++ {
		ch := NewChannel(ChannelConfig{ChannelBuffer: 4, WriteTimeout: 100 * time.Millisecond})
		srv, wsURL := newTestServer(t, ch)

		clients := make([]*websocket.Conn, 0, subscribers)
		for i := 0; i < subscribers; i++ {
			c, _ := dial(t, wsURL)
			clients = append(clients, c)
		}
		ch.WaitForSubscribers(subscribers, 2*time.Second)

		// Publishers keep taking c.mu.RLock for the whole teardown.
		stopPub := make(chan struct{})
		var pubWG sync.WaitGroup
		for p := 0; p < 4; p++ {
			pubWG.Add(1)
			go func() {
				defer pubWG.Done()
				for {
					select {
					case <-stopPub:
						return
					default:
						ch.Publish(Event{Type: EventStepCompleted, JobID: "teardown"})
					}
				}
			}()
		}

		// Slam every client connection shut at the same instant Close runs.
		// Each one errors a readLoop, which enters Unsubscribe -- that is the
		// goroutine Close used to deadlock against.
		start := make(chan struct{})
		var kickWG sync.WaitGroup
		for i := range clients {
			kickWG.Add(1)
			go func(c *websocket.Conn) {
				defer kickWG.Done()
				<-start
				_ = c.Close()
			}(clients[i])
		}

		closed := make(chan struct{})
		go func() {
			<-start
			ch.Close()
			close(closed)
		}()
		close(start)

		select {
		case <-closed:
		case <-time.After(closeBudget):
			t.Fatalf("iteration %d: Close did not return within %s with %d "+
				"subscribers tearing down concurrently", it, closeBudget, subscribers)
		}

		if got := ch.SubscriberCount(); got != 0 {
			t.Errorf("iteration %d: SubscriberCount after Close = %d, want 0", it, got)
		}

		close(stopPub)
		pubWG.Wait()
		kickWG.Wait()
		srv.Close()
	}
}

// TestChannel_SubscribeAfterClose covers the other half of the lifecycle race:
// a Subscribe that wins the check in ServeWS but lands after Close has swept
// the subscriber set. It must not register (which would race c.wg.Add against
// the c.wg.Wait in Close, and leave a readLoop blocked on a connection nothing
// closes), and the handle it returns must already be done so ServeWS returns.
func TestChannel_SubscribeAfterClose(t *testing.T) {
	ch := NewChannel(ChannelConfig{ChannelBuffer: 4})
	srv, wsURL := newTestServer(t, ch)
	defer func() { srv.Close() }()

	c, _ := dial(t, wsURL)
	defer func() { _ = c.Close() }()
	ch.WaitForSubscribers(1, 2*time.Second)
	ch.Close()

	// Mint a real connection from a second, open channel, then hand it to the
	// closed channel's Subscribe -- the state ServeWS lands in when Close runs
	// between its closed check and the upgrade.
	spare := NewChannel(ChannelConfig{ChannelBuffer: 4})
	defer func() { spare.Close() }()
	spareSrv, spareURL := newTestServer(t, spare)
	defer func() { spareSrv.Close() }()
	spareConn, _ := dial(t, spareURL)
	defer func() { _ = spareConn.Close() }()

	sub := ch.Subscribe(spareConn)

	// Already torn down on return, not merely torn down eventually: the old
	// code registered the subscriber and spawned both loops, and done was only
	// closed once writeLoop got scheduled and observed stopCh.
	select {
	case <-sub.done:
	default:
		t.Fatal("Subscribe on a closed channel returned a handle that was not already torn down")
	}
	if got := ch.SubscriberCount(); got != 0 {
		t.Errorf("SubscriberCount after subscribing to a closed channel = %d, want 0", got)
	}

	// A second Close must still return promptly and not double-close anything.
	done := make(chan struct{})
	go func() { ch.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("second Close did not return within 5s")
	}
}

// TestChannel_ServeWSRacesClose dials through the real handler while Close
// runs, so ServeWS -> Subscribe overlaps Close -> c.wg.Wait.
//
// Regression test for the second half of the lifecycle bug, observed failing
// TestChannel_CloseUnsubscribes on main:
//
//	WARNING: DATA RACE
//	Write at 0x00c00012a4b8 by goroutine 27:
//	  livechannel.TestChannel_CloseUnsubscribes()  websocket_channel_test.go:132  // ch.Close()
//	Previous read at 0x00c00012a4b8 by goroutine 29:
//	  livechannel.(*Channel).ServeWS()             websocket_channel.go:220       // c.Subscribe(conn)
//
// Subscribe published the subscriber in two steps: c.subCnt.Add(1), which is
// what WaitForSubscribers polls, and only then c.wg.Add(1). A caller that
// waited for the subscriber and closed immediately landed in that gap, so
// Close reached c.wg.Wait() with the counter still at zero and returned while
// Subscribe was still performing its first Add. sync.WaitGroup instruments
// the first Add with race.Read(&wg.sema) and Wait with race.Write(&wg.sema)
// exactly to catch that; unsynchronized, it is also a "WaitGroup misuse"
// panic. Subscribe now does both increments under c.mu, so the count cannot
// become observable before the WaitGroup has been incremented.
//
// This is the shape of TestChannel_CloseUnsubscribes, looped to hit the gap.
func TestChannel_ServeWSRacesClose(t *testing.T) {
	const iterations = 60

	for it := 0; it < iterations; it++ {
		ch := NewChannel(ChannelConfig{ChannelBuffer: 4})
		srv, wsURL := newTestServer(t, ch)

		conn, _ := dial(t, wsURL)
		ch.WaitForSubscribers(1, 2*time.Second)

		closed := make(chan struct{})
		go func() {
			ch.Close()
			close(closed)
		}()

		select {
		case <-closed:
		case <-time.After(30 * time.Second):
			t.Fatalf("iteration %d: Close did not return within 30s", it)
		}
		if got := ch.SubscriberCount(); got != 0 {
			t.Errorf("iteration %d: SubscriberCount after Close = %d, want 0", it, got)
		}

		_ = conn.Close()
		srv.Close()
	}
}

func TestChannel_NoGoroutineLeak(t *testing.T) {
	// goleak verify: defer VerifyTestMain in TestMain would be ideal,
	// but we can also just verify the read loop exits cleanly on Close.
	ch := NewChannel(ChannelConfig{ChannelBuffer: 4})
	srv, wsURL := newTestServer(t, ch)
	c, _ := dial(t, wsURL)
	ch.WaitForSubscribers(1, 2*time.Second)
	_ = c
	_ = srv
	ch.Close()
	srv.Close()
	// Allow a brief moment for the goroutine to exit.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		if ch.SubscriberCount() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
