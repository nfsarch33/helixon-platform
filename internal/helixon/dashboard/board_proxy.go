package dashboard

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// BoardProxy forwards the console's operator verbs to the SprintBoard API.
//
// The console is a static export served by the agent itself (ADR-0004), so
// its fetches are same-origin: the agent is the only place a board route for
// the browser can exist. The surface is deliberately the OPERATOR verbs --
// list sprints, list a sprint's tickets, requeue, resolve. Claim and complete
// are agent verbs executed against the board directly by the fleet agents;
// they are not proxied here, and nothing else is either: an allow-listed
// reverse proxy cannot grow into an open one by accident of pattern matching.
type BoardProxy struct {
	baseURL string
	client  *http.Client
}

// NewBoardProxy creates a proxy for the board at baseURL.
func NewBoardProxy(baseURL string) *BoardProxy {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:9400"
	}
	return &BoardProxy{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// MountBoardProxy registers the operator verb routes on mux:
//   - GET  /api/v1/board/sprints
//   - GET  /api/v1/board/sprints/{id}/tickets
//   - POST /api/v1/board/tickets/{id}/requeue
//   - POST /api/v1/board/tickets/{id}/resolve
func MountBoardProxy(mux *http.ServeMux, baseURL string) {
	if mux == nil {
		return
	}
	p := NewBoardProxy(baseURL)
	mux.HandleFunc("GET /api/v1/board/sprints", p.forward("GET", "/api/v1/sprints"))
	mux.HandleFunc("GET /api/v1/board/sprints/{id}/tickets",
		p.forward("GET", "/api/v1/sprints/{id}/tickets"))
	mux.HandleFunc("POST /api/v1/board/tickets/{id}/requeue",
		p.forward("POST", "/api/v1/tickets/{id}/requeue"))
	mux.HandleFunc("POST /api/v1/board/tickets/{id}/resolve",
		p.forward("POST", "/api/v1/tickets/{id}/resolve"))
}

// forward returns a handler that proxies to a fixed board path. A literal
// "{id}" in boardPath is replaced with the request's {id} path value.
func (p *BoardProxy) forward(method, boardPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := strings.ReplaceAll(boardPath, "{id}", r.PathValue("id"))
		p.serve(w, r, method, target)
	}
}

// serve proxies one request to the board and relays status, body and content
// type verbatim. An unreachable board is a 502 naming the upstream -- the
// console renders that as an error state instead of a silent empty page.
func (p *BoardProxy) serve(w http.ResponseWriter, r *http.Request, method, boardPath string) {
	var body io.Reader
	if r.Body != nil {
		body = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), method, p.baseURL+boardPath, body)
	if err != nil {
		http.Error(w, fmt.Sprintf("board proxy: %v", err), http.StatusInternalServerError)
		return
	}
	if r.ContentLength > 0 {
		req.ContentLength = r.ContentLength
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, fmt.Sprintf(`{"error":"sprintboard unreachable: %s"}`, err))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4*1024*1024))
}
