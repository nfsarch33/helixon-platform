//go:build console

package consolefs

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandler_ServesTheExport runs only with -tags console (CI builds the
// app first): the embedded export must serve the console index at the
// prefix, each route's index.html for its directory, and the app's assets.
func TestHandler_ServesTheExport(t *testing.T) {
	// Not `Files() == nil`: fs.Sub never stats, so it cannot return nil for a
	// literal directory name, and an absent out/ is a compile error long
	// before this runs. Asserting the index is present is the check that can
	// actually fail -- on a half-copied or stale export.
	if _, err := fs.Stat(Files(), "index.html"); err != nil {
		t.Fatalf("built with -tags console but the export is not staged (%v); run `npm run build:embed` in web/console first", err)
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
	// A directory route without its slash redirects to the canonical form AND
	// keeps the query: /console/runs/detail?id=... is the console's only
	// stateful URL, and losing the id renders an existing run as absent.
	resp, err := http.Get(srv.URL + Prefix + "runs/detail?id=r-9f3a2b7c&x=1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Request.URL.Path != Prefix+"runs/detail/" {
		t.Fatalf("a directory route without the slash must redirect to it, landed on %s", resp.Request.URL.Path)
	}
	if resp.Request.URL.RawQuery != "id=r-9f3a2b7c&x=1" {
		t.Fatalf("the redirect dropped the query: %q", resp.Request.URL.RawQuery)
	}

	// The export's own assets, which the route assertions above never touch:
	// every one of them serves index.html, so a binary carrying nothing but
	// the HTML would pass them.
	var asset string
	_ = fs.WalkDir(Files(), "_next", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && asset == "" && strings.HasSuffix(p, ".js") {
			asset = p
		}
		return nil
	})
	if asset == "" {
		t.Fatal("the embedded export carries no _next/*.js asset; this is not a real Next.js export")
	}
	resp, err = http.Get(srv.URL + Prefix + asset)
	if err != nil {
		t.Fatal(err)
	}
	etag := resp.Header.Get("Etag")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("%s: status %d, content-type %q", asset, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if etag == "" {
		t.Fatal("an embedded asset has no ETag, so every reload re-downloads it")
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+Prefix+asset, nil)
	req.Header.Set("If-None-Match", etag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("a matching If-None-Match returned %d, want 304", resp.StatusCode)
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
