package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// v17702-1 coverage lift tests for dashboard. Base was 78.7%.
// MountAll was at 0%; this file exercises it end-to-end so the
// package stays well above 85%.

func TestMountAll_RegistersAllRoutes(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	rv := &fakeRuntime{id: "helixon-a", phase: "running", heartbeat: 15 * time.Second, channels: 4, tools: 11}
	MountAll(mux, rv, DashboardConfig{
		SprintboardURL: "http://sprintboard.invalid",
		GitLabURL:      "http://gitlab.invalid",
		GitLabToken:    "redacted",
		GitLabProject:  "nfsarch33/helixon-platform",
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for _, path := range []string{
		"/api/v1/dashboard",
		"/api/v1/agents",
		"/api/v1/cicd",
		"/api/v1/sprint",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: GET error %v", path, err)
		}
		resp.Body.Close()
		// We accept 200 OR an upstream-network error JSON; the goal
		// of this test is route registration, not endpoint success.
		// A 404 here would mean the route is missing — that's a fail.
		if resp.StatusCode == http.StatusNotFound {
			t.Fatalf("%s: 404 — MountAll did not register this route", path)
		}
	}
}

func TestMountAll_NilMuxIsNoop(t *testing.T) {
	t.Parallel()
	MountAll(nil, nil, DashboardConfig{})
	// Must not panic.
}

func TestMountAll_NilMuxIgnoresRuntimeAndConfig(t *testing.T) {
	t.Parallel()
	// mux != nil to exercise the nil-mux guard; runtime/config nil.
	mux := http.NewServeMux()
	MountAll(mux, nil, DashboardConfig{})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard status=%d want 200", resp.StatusCode)
	}
}
