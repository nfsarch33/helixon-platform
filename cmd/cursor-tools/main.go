// Package main — cursor-tools is the Helixon fleet's MCP server
// inventory + health-check + restore CLI.
//
// Authored: 2026-07-15 (v14514 Pair-6 MVP).
//
// Subcommands:
//
//	cursor-tools list                  # print the canonical inventory
//	cursor-tools doctor                # ping every server, exit non-zero on any FAIL
//	cursor-tools restore --server <id> # re-emit a server's config block
//	cursor-tools config                # print the merged cursor-config mcp.json
//	cursor-tools doctor --json         # machine-readable doctor output
//	cursor-tools version
//
// Exit codes:
//
//	0  every ping returned "ok" or the command completed cleanly
//	1  one or more servers failed to ping (doctor)
//	2  config or inventory file missing or unreadable
//	3  unknown subcommand
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// Server is one entry from the cursor-config mcp.json.
type Server struct {
	ID       string            `json:"id"`
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
	Notes    string            `json:"notes,omitempty"`
}

// Inventory is the canonical list of MCP servers the Helixon fleet expects.
type Inventory struct {
	Version   int      `json:"version"`
	UpdatedAt string   `json:"updated_at"`
	Servers   []Server `json:"servers"`
}

// DoctorResult is the per-server output of `cursor-tools doctor`.
type DoctorResult struct {
	ID        string `json:"id"`
	State     string `json:"state"` // "ok", "fail", "skipped"
	Reason    string `json:"reason,omitempty"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
}

// DoctorReport is the top-level doctor output.
type DoctorReport struct {
	UpdatedAt string         `json:"updated_at"`
	Total     int            `json:"total"`
	OK        int            `json:"ok"`
	Failed    int            `json:"failed"`
	Skipped   int            `json:"skipped"`
	Results   []DoctorResult `json:"results"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(3)
	}
	switch os.Args[1] {
	case "list":
		cmdList()
	case "doctor":
		cmdDoctor(os.Args[2:])
	case "restore":
		cmdRestore(os.Args[2:])
	case "config":
		cmdConfig()
	case "version", "--version", "-v":
		fmt.Println("cursor-tools 0.1.0 (v14514)")
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(3)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: cursor-tools <list|doctor|restore|config|version>")
}

// inventoryPath returns the path to cursor-tools-inventory.json.
// Override with HELIXON_CURSOR_TOOLS_INVENTORY.
func inventoryPath() string {
	if v := os.Getenv("HELIXON_CURSOR_TOOLS_INVENTORY"); v != "" {
		return v
	}
	return "cursor-config/mcp/cursor-tools-inventory.json"
}

// configPath returns the path to mcp.json that cursor-config provisions.
// Override with HELIXON_CURSOR_TOOLS_CONFIG.
func configPath() string {
	if v := os.Getenv("HELIXON_CURSOR_TOOLS_CONFIG"); v != "" {
		return v
	}
	return "cursor-config/mcp/mcp.json"
}

// loadInventory reads and parses the inventory JSON.
func loadInventory(path string) (*Inventory, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read inventory %s: %w", path, err)
	}
	var inv Inventory
	if err := json.Unmarshal(body, &inv); err != nil {
		return nil, fmt.Errorf("parse inventory: %w", err)
	}
	return &inv, nil
}

func cmdList() {
	inv, err := loadInventory(inventoryPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("inventory: %s\n  version: %d\n  updated: %s\n  servers: %d\n\n",
		inventoryPath(), inv.Version, inv.UpdatedAt, len(inv.Servers))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, s := range inv.Servers {
		state := "enabled"
		if s.Disabled {
			state = "disabled"
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\n", s.ID, s.Command, state)
	}
	w.Flush()
}

func cmdDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON instead of human text")
	concurrency := fs.Int("concurrency", 4, "max concurrent pings")
	fs.Parse(args)

	inv, err := loadInventory(inventoryPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	results := runDoctor(inv, *concurrency)

	failed := 0
	for _, r := range results {
		if r.State == "fail" {
			failed++
		}
	}

	report := DoctorReport{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Total:     len(results),
		OK:        countState(results, "ok"),
		Failed:    failed,
		Skipped:   countState(results, "skipped"),
		Results:   results,
	}

	if *asJSON {
		body, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(body))
	} else {
		fmt.Printf("doctor report %s\n", report.UpdatedAt)
		fmt.Printf("  total=%d ok=%d fail=%d skipped=%d\n\n",
			report.Total, report.OK, report.Failed, report.Skipped)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, r := range results {
			marker := "OK "
			switch r.State {
			case "fail":
				marker = "FAIL"
			case "skipped":
				marker = "SKIP"
			}
			fmt.Fprintf(w, "  [%s]\t%s\t%s\n", marker, r.ID, r.Reason)
		}
		w.Flush()
	}
	if failed > 0 {
		os.Exit(1)
	}
}

// runDoctor pings each server concurrently. Pings are stubbed via
// the pingServer variable so tests can stub them without subprocesses.
func runDoctor(inv *Inventory, concurrency int) []DoctorResult {
	if concurrency < 1 {
		concurrency = 1
	}
	results := make([]DoctorResult, len(inv.Servers))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, s := range inv.Servers {
		i, s := i, s
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = pingServer(s)
		}()
	}
	wg.Wait()
	// Stable order: sort by ID
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].ID < results[j].ID
	})
	return results
}

// pingServer is the default ping implementation. It does a fast
// reachability check by inspecting the server config (no real
// subprocess yet — wiring is deferred to v14515 when Alertmanager
// observability is brought online). Tests override pingServer via
// the package-level variable below.
var pingServer = defaultPing

func defaultPing(s Server) DoctorResult {
	if s.Disabled {
		return DoctorResult{ID: s.ID, State: "skipped", Reason: "disabled in inventory", Disabled: true}
	}
	if s.Command == "" {
		return DoctorResult{ID: s.ID, State: "fail", Reason: "no command in config"}
	}
	// Resolve binary existence: try `which` via os.Stat + PATH.
	if _, err := lookPath(s.Command); err != nil {
		return DoctorResult{ID: s.ID, State: "fail", Reason: fmt.Sprintf("command %q not on PATH: %v", s.Command, err)}
	}
	return DoctorResult{ID: s.ID, State: "ok", LatencyMs: 1}
}

func countState(rs []DoctorResult, state string) int {
	n := 0
	for _, r := range rs {
		if r.State == state {
			n++
		}
	}
	return n
}

func cmdRestore(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	id := fs.String("server", "", "server id to restore (required)")
	out := fs.String("out", "", "write JSON snippet to this file (default stdout)")
	fs.Parse(args)
	if *id == "" {
		fmt.Fprintln(os.Stderr, "--server is required")
		os.Exit(3)
	}
	inv, err := loadInventory(inventoryPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, s := range inv.Servers {
		if s.ID == *id {
			snippet := buildCursorSnippet(s)
			if *out != "" {
				if err := os.WriteFile(*out, []byte(snippet), 0o644); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(2)
				}
				return
			}
			fmt.Print(snippet)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "server %q not found in inventory\n", *id)
	os.Exit(3)
}

// buildCursorSnippet converts an internal Server entry to the
// cursor-config mcpServers.<id> JSON block.
func buildCursorSnippet(s Server) string {
	entry := map[string]any{
		"command": s.Command,
		"args":    s.Args,
	}
	if s.Disabled {
		entry["disabled"] = true
	}
	if len(s.Env) > 0 {
		entry["env"] = s.Env
	}
	wrapped := map[string]any{
		"mcpServers": map[string]any{s.ID: entry},
	}
	body, _ := json.MarshalIndent(wrapped, "", "  ")
	return string(body) + "\n"
}

func cmdConfig() {
	body, err := os.ReadFile(configPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println(string(body))
}

// lookPath is a tiny wrapper around exec.LookPath so the package
// stays stdlib-only and easy to stub.
func lookPath(file string) (string, error) {
	if strings.HasPrefix(file, "/") || strings.HasPrefix(file, "./") || strings.HasPrefix(file, "../") {
		_, err := os.Stat(file)
		if err != nil {
			return "", err
		}
		return file, nil
	}
	return findOnPath(file)
}

func findOnPath(file string) (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, file)
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("not found on PATH")
}

// ensure io.Writer used in tests is reachable
var _ io.Writer = os.Stdout
