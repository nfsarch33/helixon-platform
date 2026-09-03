// Package consolefs serves the operator console (web/console, a Next.js
// static export) from the helixon binary. The export is embedded only when
// the binary is built with `-tags console`; the default build serves a
// clear "rebuild with -tags console" message at the same route, so the
// absence of the UI is never a blank page (docs/adr/0004).
package consolefs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
		//
		// The query has to come with it. This handler replaced
		// http.FileServer, whose localRedirect appends RawQuery; dropping it
		// here would silently empty the console's only stateful URL --
		// /console/runs/detail/?id=... reads its run id from the query, so a
		// redirect that loses it renders "No run selected" for a run that
		// exists. 301 is cached per request URI, so that URL would keep
		// failing from the browser's cache afterwards.
		if _, err := fs.Stat(files, path.Join(p, "index.html")); err == nil {
			// The target is built from Prefix, a path.Clean'd p, and this
			// request's own query: no scheme, no host, no way out of the
			// console.
			target := Prefix + p + "/"
			if q := r.URL.RawQuery; q != "" {
				target += "?" + q
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently) // #nosec G710 -- see above
			return
		}
		// Content-Type before WriteHeader: after it, the header map is no
		// longer consulted and the type would be sniffed from the body.
		if data, err := fs.ReadFile(files, "404.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(data)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
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
	// The embedded files have no modification time -- an embed.FS reports the
	// zero time -- so ServeContent has no validator to answer If-Modified-Since
	// with, and every reload of the console re-downloads every asset. The
	// content is fixed for the life of the binary, so its digest is an exact
	// strong validator, and ServeContent answers If-None-Match from it.
	sum := sha256.Sum256(data)
	w.Header().Set("Etag", `"`+hex.EncodeToString(sum[:])+`"`)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	return true
}
