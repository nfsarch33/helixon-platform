package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFilter_SeverityAndState(t *testing.T) {
	now := time.Now().UTC()
	alerts := []Alert{
		{Labels: map[string]string{"alertname": "A", "severity": "page"}, Status: struct{ State string `json:"state"` }{State: "active"}},
		{Labels: map[string]string{"alertname": "B", "severity": "page"}, Status: struct{ State string `json:"state"` }{State: "suppressed"}},
		{Labels: map[string]string{"alertname": "C", "severity": "notify"}, Status: struct{ State string `json:"state"` }{State: "active"}},
		{Labels: map[string]string{"alertname": "D", "severity": "log"}, Status: struct{ State string `json:"state"` }{State: "active"}},
	}
	_ = now
	p0 := filter(alerts, "page")
	if len(p0) != 1 || p0[0].Labels["alertname"] != "A" {
		t.Fatalf("p0 wrong: %+v", p0)
	}
	p1 := filter(alerts, "notify")
	if len(p1) != 1 || p1[0].Labels["alertname"] != "C" {
		t.Fatalf("p1 wrong: %+v", p1)
	}
}

func TestAckAlert_DryRunAppendsNothing(t *testing.T) {
	// In dry-run mode, ackAlert must NOT call the HTTP server or append
	// to the incidents ledger.
	dir := t.TempDir()
	inc := filepath.Join(dir, "incidents.ndjson")

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(SilenceResponse{SilenceID: "x"})
	}))
	defer srv.Close()

	a := Alert{
		Labels: map[string]string{"alertname": "Qwen36High5xx", "severity": "page"},
		Status: struct{ State string `json:"state"` }{State: "active"},
	}
	if err := ackAlert(srv.URL, a, 5*time.Minute, "v14513", "test", inc, true); err != nil {
		t.Fatalf("ackAlert dry-run failed: %v", err)
	}
	if calls != 0 {
		t.Fatalf("dry-run must not call HTTP server, got %d calls", calls)
	}
	if _, err := os.Stat(inc); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create incidents file: %v", err)
	}
}

func TestAckAlert_WetRunPostsSilenceAndAppendsRow(t *testing.T) {
	dir := t.TempDir()
	inc := filepath.Join(dir, "incidents.ndjson")

	var (
		mu       sync.Mutex
		gotPath  string
		gotBody  Silence
		gotCalls int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotCalls++
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		mu.Unlock()
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(SilenceResponse{SilenceID: "sil-123"})
	}))
	defer srv.Close()

	a := Alert{
		Labels: map[string]string{"alertname": "ControlPlaneDown", "severity": "page"},
		Status: struct{ State string `json:"state"` }{State: "active"},
	}
	if err := ackAlert(srv.URL, a, 5*time.Minute, "v14513", "test", inc, false); err != nil {
		t.Fatalf("ackAlert wet-run failed: %v", err)
	}
	if gotCalls != 1 || gotPath != "/api/v2/silences" {
		t.Fatalf("expected 1 POST to /api/v2/silences, got path=%q calls=%d", gotPath, gotCalls)
	}
	if len(gotBody.Matchers) != 2 {
		t.Fatalf("expected 2 matchers, got %d", len(gotBody.Matchers))
	}
	if gotBody.Matchers[0].Name != "alertname" || gotBody.Matchers[0].Value != "ControlPlaneDown" {
		t.Fatalf("first matcher wrong: %+v", gotBody.Matchers[0])
	}
	if gotBody.Matchers[1].Name != "severity" || gotBody.Matchers[1].Value != "page" {
		t.Fatalf("second matcher wrong: %+v", gotBody.Matchers[1])
	}

	body, err := os.ReadFile(inc)
	if err != nil {
		t.Fatalf("read incidents: %v", err)
	}
	line := strings.TrimSpace(string(body))
	if line == "" {
		t.Fatal("incidents file empty")
	}
	var got Incident
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("decode incident: %v", err)
	}
	if got.Alertname != "ControlPlaneDown" {
		t.Fatalf("incident alertname: %q", got.Alertname)
	}
	if got.SilenceID != "sil-123" {
		t.Fatalf("incident silence_id: %q", got.SilenceID)
	}
	if got.Sprint != "v14513" {
		t.Fatalf("incident sprint: %q", got.Sprint)
	}
	if got.Severity != "page" {
		t.Fatalf("incident severity: %q", got.Severity)
	}
}

func TestAppendNDJSON_AppendOnly(t *testing.T) {
	dir := t.TempDir()
	inc := filepath.Join(dir, "incidents.ndjson")

	for i := 0; i < 3; i++ {
		if err := appendNDJSON(inc, Incident{Alertname: "X", Sprint: "v14513", AckWithinMin: 5}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	body, _ := os.ReadFile(inc)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
}

func TestListAlerts_Reachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "active=true") {
			t.Errorf("missing active=true in query: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Alert{
			{Labels: map[string]string{"alertname": "Foo", "severity": "page"}, Status: struct{ State string `json:"state"` }{State: "active"}},
		})
	}))
	defer srv.Close()

	got, err := listAlerts(srv.URL)
	if err != nil {
		t.Fatalf("listAlerts: %v", err)
	}
	if len(got) != 1 || got[0].Labels["alertname"] != "Foo" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestListAlerts_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := listAlerts(srv.URL); err == nil {
		t.Fatal("expected error for 500 status")
	}
}