// Command svcregistry-bridge ingests the canonical Helixon service
// registry (registry.yaml, built by v14540) and POSTs each service into
// the runtime svcregistry daemon so it appears in /api/v1/services,
// Grafana dashboards, and Prometheus metrics.
//
// This is the bridge that closes the loop between the schema-time SOT
// (registra) and the runtime inventory (svcregistryd).
//
// Usage:
//
//	svcregistry-bridge \
//	  --registry /home/jaslian/Code/cursor-global-kb/inventory/services/registry.yaml \
//	  --api http://127.0.0.1:9103 \
//	  --owner cursor-v14541
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/registra"
)

func main() {
	var (
		registryPath = flag.String("registry",
			"/home/jaslian/Code/cursor-global-kb/inventory/services/registry.yaml",
			"path to registry.yaml")
		apiURL = flag.String("api", "http://127.0.0.1:9103",
			"svcregistryd base URL")
		owner = flag.String("owner", "cursor-v14541",
			"owner string stamped on every registered service")
		dryRun = flag.Bool("dry-run", false,
			"print what would be registered without POSTing")
		timeout = flag.Duration("timeout", 10*time.Second,
			"HTTP timeout per request")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	reg, err := registra.Load(*registryPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load registry: %v\n", err)
		os.Exit(1)
	}

	ok, fail, skip := 0, 0, 0
	for _, s := range reg.Services {
		if s.Port == 0 {
			logger.Info("skip", "name", s.Name, "reason", "no port")
			skip++
			continue
		}
		// Resolve tailscale IP for primary_node.
		tsIP := ""
		if n, ok := reg.FindNodeByAlias(s.PrimaryNode); ok {
			tsIP = n.TailscaleIP
		}
		body := map[string]any{
			"name":         s.Name,
			"host":         s.Address,
			"port":         s.Port,
			"protocol":     "http",
			"owner":        *owner,
			"status":       "up",
			"last_seen_iso": time.Now().UTC().Format(time.RFC3339),
			"tailscale_ip": tsIP,
		}
		if *dryRun {
			logger.Info("would-register", "name", s.Name, "port", s.Port, "ts_ip", tsIP)
			ok++
			continue
		}
		if err := postJSON(*apiURL+"/api/v1/services", body, *timeout); err != nil {
			logger.Error("register", "name", s.Name, "err", err)
			fail++
			continue
		}
		logger.Info("registered", "name", s.Name, "port", s.Port)
		ok++
	}
	fmt.Printf("bridge: registered=%d failed=%d skipped=%d\n", ok, fail, skip)
	if fail > 0 {
		os.Exit(1)
	}
}

func postJSON(url string, body any, timeout time.Duration) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, string(bb))
	}
	return nil
}

// bridge is the testable inner function (separated from main).
func bridge(yamlPath, apiURL, owner string) error {
	reg, err := registra.Load(yamlPath)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, s := range reg.Services {
		if s.Port == 0 {
			continue
		}
		tsIP := ""
		if n, ok := reg.FindNodeByAlias(s.PrimaryNode); ok {
			tsIP = n.TailscaleIP
		}
		body := map[string]any{
			"name":          s.Name,
			"host":          s.Address,
			"port":          s.Port,
			"protocol":      "http",
			"owner":         owner,
			"status":        "up",
			"last_seen_iso": time.Now().UTC().Format(time.RFC3339),
			"tailscale_ip":  tsIP,
		}
		b, _ := json.Marshal(body)
		req, err := http.NewRequest(http.MethodPost, apiURL+"/api/v1/services", bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("POST %s: status=%d", s.Name, resp.StatusCode)
		}
	}
	return nil
}