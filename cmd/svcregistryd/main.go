// Command svcregistryd is the v3 Helixon Service Registry daemon.
//
// It exposes the package's HTTPServer (/healthz, /metrics, /api/v1/...)
// and persists snapshots to a JSON file under $HOME/.config. It is the
// runtime front-end for the internal/svcregistry package.
//
// Usage:
//
//	svcregistryd --addr :9103 --path ~/.config/svc-registry.json
//
// The package's defaults handle every operational concern (atomic
// rename, port-conflict detection, Prometheus counter) — this binary is
// intentionally thin.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/svcregistry"
)

const (
	defaultAddr = "0.0.0.0:9103"
	defaultPath = "~/.config/svc-registry.json"
)

func main() {
	var (
		addr    = flag.String("addr", defaultAddr, "HTTP listen address")
		path    = flag.String("path", defaultPath, "JSON snapshot path (~ expands to $HOME)")
		period  = flag.Duration("save-period", 30*time.Second, "periodic Save interval")
		showVer     = flag.Bool("version", false, "print version and exit")
		healthcheck = flag.Bool("healthcheck", false, "probe own /healthz endpoint (exit 0 ok, 1 bad); for distroless containers without curl")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("svcregistryd dev (v16122 Sprint 17)")
		return
	}

	if *healthcheck {
		// Self-probe: hit the same /healthz endpoint we expose.
		// Designed for distroless containers that have no curl/wget.
		addr := *addr
		// Translate 0.0.0.0 / [::] -> 127.0.0.1 for the probe target.
		target := strings.NewReplacer("0.0.0.0", "127.0.0.1", "[::]", "[::1]").Replace(addr)
		probe := "http://" + target + "/healthz"
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(probe)
		if err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: GET %s: %v\n", probe, err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "healthcheck: %s -> %d\n", probe, resp.StatusCode)
			os.Exit(1)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		fmt.Printf("ok (probe=%s)\n", probe)
		return
	}

	p := expandHome(*path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", filepath.Dir(p), err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	reg := svcregistry.New(p)
	if err := reg.Load(); err != nil {
		logger.Warn("load snapshot", "err", err, "path", p)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           svcregistry.NewHTTPServer(reg).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Periodic save goroutine.
	go func() {
		t := time.NewTicker(*period)
		defer t.Stop()
		for range t.C {
			if err := reg.Save(); err != nil {
				logger.Error("periodic save", "err", err)
			}
		}
	}()

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		logger.Info("shutdown signal received")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		if err := reg.Save(); err != nil {
			logger.Error("final save", "err", err)
		}
	}()

	logger.Info("svcregistryd listening",
		"addr", *addr, "path", p, "snapshot_size", reg.Size())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("http server", "err", err)
		os.Exit(1)
	}
	logger.Info("svcregistryd stopped")
}

func expandHome(p string) string {
	if len(p) >= 2 && p[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}