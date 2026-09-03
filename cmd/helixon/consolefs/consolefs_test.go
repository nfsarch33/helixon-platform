package consolefs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandler_WithoutTheTagSaysHow: the default build must not serve a blank
// page; it names the rebuild step. With -tags console this test is skipped
// and TestHandler_ServesTheExport (files_console_test.go) runs instead.
func TestHandler_WithoutTheTagSaysHow(t *testing.T) {
	if Files() != nil {
		t.Skip("console embedded in this build")
	}
	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + Prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "-tags console") {
		t.Fatalf("status %d body %q, want 404 naming the rebuild", resp.StatusCode, body)
	}
}
