package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeRuntime struct {
	id        string
	phase     string
	heartbeat time.Duration
	channels  int
	tools     int
}

func (f *fakeRuntime) AgentID() string                { return f.id }
func (f *fakeRuntime) Phase() string                  { return f.phase }
func (f *fakeRuntime) HeartbeatEvery() time.Duration  { return f.heartbeat }
func (f *fakeRuntime) ChannelCount() int              { return f.channels }
func (f *fakeRuntime) RegisteredToolCount() int       { return f.tools }

func TestHandler_GETReturnsRuntimeSummary(t *testing.T) {
	t.Parallel()
	rv := &fakeRuntime{id: "helixon-claude", phase: "running", heartbeat: 30 * time.Second, channels: 2, tools: 7}
	srv := httptest.NewServer(Handler(rv))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got DashboardResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AgentID != "helixon-claude" || got.Phase != "running" {
		t.Errorf("payload = %+v", got)
	}
	if got.HeartbeatEvery != "30s" {
		t.Errorf("HeartbeatEvery = %q", got.HeartbeatEvery)
	}
	if got.Channels != 2 || got.Tools != 7 {
		t.Errorf("channels/tools = %d/%d", got.Channels, got.Tools)
	}
	if got.GeneratedAt == "" {
		t.Errorf("GeneratedAt empty")
	}
}

func TestHandler_NilRuntimeIsSafe(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(Handler(nil))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got DashboardResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Phase != "unknown" {
		t.Errorf("phase = %q, want unknown", got.Phase)
	}
}

func TestHandler_RejectsNonGET(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(Handler(nil))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/dashboard", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestMount_RegistersRoute(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	Mount(mux, &fakeRuntime{id: "x", phase: "configured"})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestMount_NilMuxIsNoop(t *testing.T) {
	t.Parallel()
	Mount(nil, nil) // must not panic
}
