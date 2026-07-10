package svcregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// makeService returns a fully-valid Service suitable for Register.
func makeService(name, host string, port int, proto string) Service {
	return Service{
		Name:        name,
		Host:        host,
		Port:        port,
		Protocol:    proto,
		Owner:       "ops@helixon",
		Status:      "up",
		LastSeenISO: "2026-07-05T08:00:00Z",
		TailscaleIP: "100.64.0.10",
	}
}

// ---- Service.Validate -----------------------------------------------------

func TestServiceValidate_AcceptsCanonicalEntry(t *testing.T) {
	s := makeService("helixon-platform", "wsl3", 8080, "http")
	if err := s.Validate(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestServiceValidate_RejectsBadFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Service)
		want   error
	}{
		{"blank-name", func(s *Service) { s.Name = "" }, ErrInvalidName},
		{"blank-host", func(s *Service) { s.Host = "" }, ErrInvalidHost},
		{"port-zero", func(s *Service) { s.Port = 0 }, ErrInvalidPort},
		{"port-too-high", func(s *Service) { s.Port = 70000 }, ErrInvalidPort},
		{"bad-proto", func(s *Service) { s.Protocol = "icmp" }, ErrInvalidProtocol},
		{"bad-status", func(s *Service) { s.Status = "alive" }, ErrInvalidStatus},
	}
	base := makeService("svc", "host", 80, "tcp")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.mutate(&s)
			err := s.Validate()
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestServiceKey(t *testing.T) {
	s := makeService("x", "host-a", 1, "tcp")
	if got, want := s.Key(), "host-a/x"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// ---- Registry.Register ----------------------------------------------------

func TestRegistryRegister_OK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc-registry.json")
	reg := New(path)
	if err := reg.Register(makeService("helixon-platform", "wsl3", 8080, "http")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got, want := reg.Size(), 1; got != want {
		t.Fatalf("Size=%d want %d", got, want)
	}
	if reg.metrics.Value(OpRegister, StatusOK) != 1 {
		t.Fatalf("metrics register/ok counter mismatch")
	}
}

func TestRegistryRegister_DetectsPortConflict(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	if err := reg.Register(makeService("a", "host1", 9100, "tcp")); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := reg.Register(makeService("b", "host1", 9100, "tcp"))
	if !errors.Is(err, ErrPortConflict) {
		t.Fatalf("expected ErrPortConflict, got %v", err)
	}
	if reg.metrics.Value(OpConflict, StatusOK) != 1 {
		t.Fatalf("conflict counter not incremented")
	}
}

func TestRegistryRegister_DifferentProtocolOK(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	if err := reg.Register(makeService("a", "h", 53, "tcp")); err != nil {
		t.Fatalf("tcp: %v", err)
	}
	if err := reg.Register(makeService("b", "h", 53, "udp")); err != nil {
		t.Fatalf("udp must coexist: %v", err)
	}
}

func TestRegistryRegister_DifferentHostOK(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	if err := reg.Register(makeService("a", "h1", 9100, "tcp")); err != nil {
		t.Fatalf("h1: %v", err)
	}
	if err := reg.Register(makeService("b", "h2", 9100, "tcp")); err != nil {
		t.Fatalf("h2 must coexist: %v", err)
	}
}

func TestRegistryRegister_RejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	s := makeService("", "h", 80, "tcp")
	err := reg.Register(s)
	if !errors.Is(err, ErrInvalidName) {
		t.Fatalf("got %v want ErrInvalidName", err)
	}
	if reg.metrics.Value(OpRegister, StatusError) != 1 {
		t.Fatalf("register/error counter not bumped")
	}
}

func TestRegistryRegister_UpdateExisting(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	a := makeService("svc", "h", 8080, "http")
	if err := reg.Register(a); err != nil {
		t.Fatalf("first: %v", err)
	}
	b := a
	b.Status = "down"
	if err := reg.Register(b); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, ok := reg.Get("h", "svc")
	if !ok || got.Status != "down" {
		t.Fatalf("status not updated: got=%+v ok=%v", got, ok)
	}
	if reg.Size() != 1 {
		t.Fatalf("update must not duplicate, size=%d", reg.Size())
	}
}

// ---- Registry.Unregister --------------------------------------------------

func TestRegistryUnregister_OK(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	if err := reg.Register(makeService("a", "h", 80, "tcp")); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := reg.Unregister("h", "a"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if reg.Size() != 0 {
		t.Fatalf("expected empty, got size=%d", reg.Size())
	}
}

func TestRegistryUnregister_NotFound(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	err := reg.Unregister("h", "absent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ---- Registry.List --------------------------------------------------------

func TestRegistryList_DeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	for _, h := range []string{"z", "a", "m"} {
		_ = reg.Register(makeService("svc-"+h, h, 80, "tcp"))
	}
	got := reg.List()
	if len(got) != 3 || got[0].Host != "a" || got[2].Host != "z" {
		t.Fatalf("expected sorted by host, got %+v", got)
	}
}

// ---- Registry.Save / Load round-trip -------------------------------------

func TestRegistrySaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc-registry.json")
	reg := New(path)
	for i, h := range []string{"wsl2", "wsl3", "oracle-jump"} {
		_ = reg.Register(makeService(
			"svc-"+h, h, 8000+i, "tcp",
		))
	}
	if err := reg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Contains(data, []byte("svc-wsl3")) {
		t.Fatalf("snapshot missing wsl3: %s", data)
	}
	reg2 := New(path)
	if err := reg2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reg2.Size() != 3 {
		t.Fatalf("after load size=%d", reg2.Size())
	}
	for _, s := range reg.List() {
		got, ok := reg2.Get(s.Host, s.Name)
		if !ok {
			t.Fatalf("missing after load: %s", s.Key())
		}
		if got.Port != s.Port || got.Protocol != s.Protocol {
			t.Fatalf("mismatch %s got=%+v want=%+v", s.Key(), got, s)
		}
	}
}

func TestRegistryLoad_MissingFileIsOK(t *testing.T) {
	reg := New("/nonexistent/path/svc-registry.json")
	if err := reg.Load(); err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if reg.Size() != 0 {
		t.Fatalf("expected empty, got %d", reg.Size())
	}
}

func TestRegistryLoad_CorruptFileIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := New(path)
	if err := reg.Load(); err == nil {
		t.Fatalf("expected error on corrupt JSON")
	}
}

// ---- Conflicts -----------------------------------------------------------

func TestRegistryConflicts_NoCollisions(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	_ = reg.Register(makeService("a", "h1", 80, "tcp"))
	_ = reg.Register(makeService("b", "h1", 81, "tcp"))
	_ = reg.Register(makeService("c", "h2", 80, "tcp"))
	if c := reg.Conflicts(); len(c) != 0 {
		t.Fatalf("expected no conflicts, got %d groups", len(c))
	}
}

func TestRegistryConflicts_DetectsGroup(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	// Seed a conflict directly (bypassing Register's guard) to simulate
	// a stale snapshot that two operators wrote simultaneously.
	if !reg.rawInsert(makeService("a", "h1", 80, "tcp")) {
		t.Fatal("rawInsert a")
	}
	if !reg.rawInsert(makeService("b", "h1", 80, "tcp")) {
		t.Fatal("rawInsert b")
	}
	c := reg.Conflicts()
	if len(c) != 1 || len(c[0]) != 2 {
		t.Fatalf("expected 1 group of 2, got %+v", c)
	}
}

// ---- Metrics --------------------------------------------------------------

func TestMetrics_CounterByOpAndStatus(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	_ = reg.Register(makeService("a", "h", 80, "tcp"))
	_ = reg.Register(makeService("", "h", 80, "tcp")) // error
	_ = reg.Unregister("h", "a")
	_ = reg.Unregister("h", "absent") // error
	_ = reg.List()

	if v := reg.metrics.Value(OpRegister, StatusOK); v != 1 {
		t.Fatalf("register/ok=%d want 1", v)
	}
	if v := reg.metrics.Value(OpRegister, StatusError); v != 1 {
		t.Fatalf("register/error=%d want 1", v)
	}
	if v := reg.metrics.Value(OpUnregister, StatusOK); v != 1 {
		t.Fatalf("unregister/ok=%d want 1", v)
	}
	if v := reg.metrics.Value(OpUnregister, StatusError); v != 1 {
		t.Fatalf("unregister/error=%d want 1", v)
	}
	if v := reg.metrics.Value(OpList, StatusOK); v < 1 {
		t.Fatalf("list/ok=%d want >=1", v)
	}
}

// ---- Concurrency ---------------------------------------------------------

func TestRegistry_ConcurrentRegister(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	var wg sync.WaitGroup
	const N = 100
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Unique (host, name, port) per goroutine. The Registry key
			// is host/name, so we vary name to actually populate N
			// distinct entries (port alone would still collide).
			s := makeService("svc-"+itoa(i), "h", 8000+i, "tcp")
			if err := reg.Register(s); err != nil {
				t.Errorf("goroutine %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if got := reg.Size(); got != N {
		t.Fatalf("size=%d want %d", got, N)
	}
}

// itoa avoids importing strconv just for one test.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ---- HTTPServer ----------------------------------------------------------

func TestHTTPServer_Healthz(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	srv := httptest.NewServer(NewHTTPServer(reg).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ok") {
		t.Fatalf("body=%q", body)
	}
}

func TestHTTPServer_PostAndGet(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	srv := httptest.NewServer(NewHTTPServer(reg).Handler())
	defer srv.Close()

	svc := makeService("helixon-platform", "wsl3", 8080, "http")
	body, _ := json.Marshal(svc)
	resp, err := http.Post(srv.URL+"/api/v1/services", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("POST status=%d", resp.StatusCode)
	}

	resp2, err := http.Get(srv.URL + "/api/v1/services")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var got []Service
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "helixon-platform" {
		t.Fatalf("got %+v", got)
	}

	resp3, err := http.Get(srv.URL + "/api/v1/services/wsl3/helixon-platform")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Fatalf("single GET status=%d", resp3.StatusCode)
	}
}

func TestHTTPServer_PortConflictReturns409(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	srv := httptest.NewServer(NewHTTPServer(reg).Handler())
	defer srv.Close()

	svc := makeService("a", "h1", 9100, "tcp")
	body, _ := json.Marshal(svc)
	resp, _ := http.Post(srv.URL+"/api/v1/services", "application/json", bytes.NewReader(body))
	resp.Body.Close()

	svc2 := makeService("b", "h1", 9100, "tcp")
	body2, _ := json.Marshal(svc2)
	resp2, err := http.Post(srv.URL+"/api/v1/services", "application/json", bytes.NewReader(body2))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 409 {
		t.Fatalf("expected 409, got %d", resp2.StatusCode)
	}
}

func TestHTTPServer_DeleteAndConflicts(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	// Seed a conflict directly to simulate a stale snapshot.
	if !reg.rawInsert(makeService("a", "h1", 9100, "tcp")) {
		t.Fatal("rawInsert a")
	}
	if !reg.rawInsert(makeService("b", "h1", 9100, "tcp")) {
		t.Fatal("rawInsert b")
	}

	srv := httptest.NewServer(NewHTTPServer(reg).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/conflicts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var c [][]Service
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if len(c) != 1 {
		t.Fatalf("expected 1 conflict group, got %d", len(c))
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/services/h1/a", nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 204 {
		t.Fatalf("DELETE status=%d", resp2.StatusCode)
	}
}

func TestHTTPServer_MetricsEndpoint(t *testing.T) {
	dir := t.TempDir()
	reg := New(filepath.Join(dir, "r.json"))
	_ = reg.Register(makeService("a", "h1", 9100, "tcp"))

	srv := httptest.NewServer(NewHTTPServer(reg).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "svcregistry_operations_total") {
		t.Fatalf("metrics output missing counter: %s", body)
	}
}
