// Package consolefs serves the operator console (web/console, a Next.js
// static export) from the helixon binary. The export is embedded only when
// the binary is built with `-tags console`; the default build serves a
// clear "rebuild with -tags console" message at the same route, so the
// absence of the UI is never a blank page (docs/adr/0004).
package consolefs

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// Prefix is the URL prefix the console is served under.
const Prefix = "/console/"

// Handler serves the console under Prefix. index.html is served for the
// prefix root and for any directory path (Next.js exports one index.html
// per route with trailingSlash); a directory route without its slash is
// redirected to it; unknown paths get the app's 404 page when it exists.
//
// Files are served with http.ServeContent rather than http.FileServer: the
// latter canonicalises ".../index.html" to "./", which under a stripped
// prefix becomes a redirect loop (caught by the tagged test).
func Handler() http.Handler {
	files := Files()
	if files == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "the operator console is not built into this binary; rebuild with -tags console after `npm run build:embed` in web/console", http.StatusNotFound)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, Prefix)
		p = path.Clean("/" + p)[1:] // no "..", no leading slash
		if p == "" || strings.HasSuffix(r.URL.Path, "/") {
			p = path.Join(p, "index.html")
		}
		if serve(w, r, files, p) {
			return
		}
		// A directory route without its slash: send the client to the
		// canonical form so relative asset paths resolve.
		if _, err := fs.Stat(files, path.Join(p, "index.html")); err == nil {
			// The target is always under the console prefix: p is path.Clean'd
			// and carries no scheme or host, so this cannot leave the console.
			http.Redirect(w, r, Prefix+p+"/", http.StatusMovedPermanently) // #nosec G710 -- see above
			return
		}
		w.WriteHeader(http.StatusNotFound)
		if data, err := fs.ReadFile(files, "404.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		_, _ = w.Write([]byte("404 page not found\n"))
	})
}

// serve writes the named file if it exists and is not a directory.
func serve(w http.ResponseWriter, r *http.Request, files fs.FS, name string) bool {
	info, err := fs.Stat(files, name)
	if err != nil || info.IsDir() {
		return false
	}
	data, err := fs.ReadFile(files, name)
	if err != nil {
		return false
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	return true
}
