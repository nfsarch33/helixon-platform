// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// ServeOptions configures the long-lived control plane.
type ServeOptions struct {
	ConfigPath  string
	HTTPAddr    string
	RegistryURL string
	Logger      *slog.Logger
	Clock       Clock
	HTTPClient  *http.Client
}

// Serve runs the long-lived control plane until ctx is cancelled.
func Serve(ctx context.Context, opts ServeOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opts.Logger == nil {
		return errors.New("logger required")
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = DefaultConfigPath
	}
	if opts.HTTPAddr == "" {
		opts.HTTPAddr = DefaultHTTPAddr
	}
	if opts.RegistryURL == "" {
		opts.RegistryURL = DefaultRegistryURL
	}
	if opts.Clock == nil {
		opts.Clock = RealClock{}
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: HeartbeatTimeout}
	}

	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("load config %s: %w", opts.ConfigPath, err)
	}

	priv, _, err := EnsureHostKey(cfg.HostKeyPath, false)
	if err != nil && !errors.Is(err, ErrHostKeyExists) {
		return fmt.Errorf("ensure host key: %w", err)
	}

	allow := newAllowlist(cfg.PeerAllowlist)
	srv := &http.Server{
		Addr:              opts.HTTPAddr,
		Handler:           newControlHandler(cfg, priv, allow, opts.Logger),
		ReadHeaderTimeout: 5 * time.Second,
	}

	hbDone := make(chan struct{})
	go runHeartbeat(ctx, heartbeatOpts{
		Cfg:        cfg,
		Logger:     opts.Logger,
		Clock:      opts.Clock,
		HTTPClient: opts.HTTPClient,
		URL:        opts.RegistryURL,
	}, hbDone)

	opts.Logger.Info("serve: control plane listening", "addr", opts.HTTPAddr, "node_id", cfg.NodeID)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	<-hbDone
	return nil
}

type heartbeatOpts struct {
	Cfg        Config
	Logger     *slog.Logger
	Clock      Clock
	HTTPClient *http.Client
	URL        string
}

func runHeartbeat(ctx context.Context, opts heartbeatOpts, done chan<- struct{}) {
	defer close(done)

	t := opts.Clock.NewTimer(HeartbeatInterval)
	backoff := InitialBackoff
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			opts.Logger.Info("heartbeat: shutdown")
			return
		case <-t.C():
		}

		if err := publishHeartbeat(ctx, opts); err != nil {
			opts.Logger.Warn("heartbeat: publish failed", "err", err, "backoff", backoff)
			t = opts.Clock.NewTimer(backoff)
			backoff = nextBackoff(backoff)
			continue
		}

		backoff = InitialBackoff
		t = opts.Clock.NewTimer(HeartbeatInterval)
	}
}

func publishHeartbeat(ctx context.Context, opts heartbeatOpts) error {
	payload := map[string]any{
		"node_id":   opts.Cfg.NodeID,
		"ts":        opts.Clock.Now().UTC().Format(time.RFC3339Nano),
		"version":   Version,
		"http_addr": opts.Cfg.HTTPAddr,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.URL+"/v1/heartbeat", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > MaxBackoff {
		return MaxBackoff
	}
	return next
}

// allowlist is a thread-safe set of allowed peer node IDs.
type allowlist struct {
	mu      sync.RWMutex
	members map[string]struct{}
}

func newAllowlist(members []string) *allowlist {
	m := make(map[string]struct{}, len(members))
	for _, id := range members {
		m[id] = struct{}{}
	}
	return &allowlist{members: m}
}

func (a *allowlist) Allows(id string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.members[id]
	return ok
}

func (a *allowlist) Set(members []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.members = make(map[string]struct{}, len(members))
	for _, id := range members {
		a.members[id] = struct{}{}
	}
}

// newControlHandler builds the HTTP control plane.
func newControlHandler(cfg Config, priv ed25519.PrivateKey, allow *allowlist, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version": Version,
			"node_id": cfg.NodeID,
		})
	})
	stub := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			peer := r.Header.Get("X-Helixon-Peer")
			if !allow.Allows(peer) {
				logger.Warn("control: denied (peer not in allowlist)", "endpoint", name, "peer", peer, "src", r.RemoteAddr)
				http.Error(w, "peer not allowed", http.StatusForbidden)
				return
			}
			logger.Info("control: stub invoked", "endpoint", name, "peer", peer, "src", r.RemoteAddr)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotImplemented)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"endpoint": name,
				"status":   "stub",
				"node_id":  cfg.NodeID,
				"key_fp":   PublicKeyFingerprint(priv.Public().(ed25519.PublicKey)),
				"hint":     "MVP v0.5.0: implement via follow-up gRPC PR (T-A.3)",
			})
		}
	}
	mux.HandleFunc("/v1/control/exec", stub("exec"))
	mux.HandleFunc("/v1/control/upload", stub("upload"))
	mux.HandleFunc("/v1/control/download", stub("download"))
	mux.HandleFunc("/v1/control/sysconfig", stub("sysconfig"))
	return mux
}
