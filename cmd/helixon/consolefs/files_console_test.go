//go:build console

package consolefs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandler_ServesTheExport runs only with -tags console (CI builds the
// app first): the embedded export must serve the console index at the
// prefix, each route's index.html for its directory, and the app's assets.
func TestHandler_ServesTheExport(t *testing.T) {
	if Files() == nil {
		t.Fatal("built with -tags console but nothing is embedded; run `npm run build:embed` in web/console first")
	}
	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)
	for _, path := range []string{Prefix, Prefix + "runs/", Prefix + "evals/", Prefix + "costs/", Prefix + "memory/", Prefix + "runs/detail/"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Helixon console") {
			t.Fatalf("%s: status %d, body does not contain the console title", path, resp.StatusCode)
		}
	}
	resp, err := http.Get(srv.URL + Prefix + "runs")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Request.URL.Path != Prefix+"runs/" {
		t.Fatalf("a directory route without the slash must redirect to it, landed on %s", resp.Request.URL.Path)
	}
	resp, err = http.Get(srv.URL + Prefix + "definitely-not-a-route/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route: status %d, want 404", resp.StatusCode)
	}
}
