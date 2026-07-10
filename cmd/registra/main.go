// Command registra is the Helixon Service Registry CLI.
//
// Subcommands:
//
//	list [--node=ALIAS] [--kind=KIND]   list services
//	show NAME                            show one service
//	nodes                                list fleet nodes
//	cells                                list LLM cells
//	credential TITLE                     look up a 1Password item by title
//	health [--node=ALIAS]                probe http health endpoints
//	summary                              human-readable summary
//
// v17714-1: refactored dispatch — each subcommand is its own helper so main()
// stays under CC 6 (was 34 before). Behaviour unchanged; covered by
// cmd/registra/main_test.go.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/registra"
)

const defaultRegistryPath = "/home/jaslian/Code/cursor-global-kb/inventory/services/registry.yaml"

func main() {
	os.Exit(runRegistra(os.Args[1:]))
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `usage: registra <subcommand> [flags]

subcommands:
  list           list services
  show NAME      show one service
  nodes          list fleet nodes
  cells          list LLM cells
  credential T   look up a 1Password item by title
  health         probe http health endpoints
  summary        human-readable summary
`)
}

// runRegistra is the testable entry point of the registra CLI. It returns
// the process exit code rather than calling os.Exit directly. See
// cmd/registra/main_test.go for the dispatch contract.
func runRegistra(args []string) int {
	registryPath, cmdArgs, err := splitArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		usage(os.Stderr)
		return 2
	}

	reg, err := registra.Load(registryPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "registra: %v\n", err)
		return 1
	}

	return dispatch(reg, cmdArgs)
}

// splitArgs parses the leading "--registry path" prefix (if present) and
// returns the registry path, remaining args, or an error if no subcommand
// was supplied. v17714-1: extracted from runRegistra to keep the
// dispatcher under CC ≤6.
func splitArgs(args []string) (string, []string, error) {
	if len(args) < 1 {
		return "", nil, fmt.Errorf("registra: missing subcommand")
	}
	registryPath, cmdArgs := splitRegistryFlag(args)
	if len(cmdArgs) < 1 {
		return "", nil, fmt.Errorf("registra: missing subcommand")
	}
	return registryPath, cmdArgs, nil
}

// dispatch routes a parsed subcommand to its handler. v17714-1: extracted
// from runRegistra to keep the dispatcher under CC ≤6. Uses a table
// lookup so adding a subcommand is a single new entry, not a new switch arm.
func dispatch(reg *registra.Registry, cmdArgs []string) int {
	cmd := cmdArgs[0]
	cmdArgs = cmdArgs[1:]
	if handler, ok := subcommands[cmd]; ok {
		return handler(reg, cmdArgs)
	}
	if isHelpAlias(cmd) {
		usage(os.Stdout)
		return 0
	}
	fmt.Fprintf(os.Stderr, "registra: unknown subcommand %q\n", cmd)
	usage(os.Stderr)
	return 2
}

// subcommands is the registry CLI's dispatch table. Each handler takes
// the loaded registry and any post-subcommand args, and returns an exit code.
// v17714-1: replaces the original 8-arm switch to keep dispatch ≤6.
var subcommands = map[string]func(*registra.Registry, []string) int{
	"list":       cmdList,
	"show":       cmdShow,
	"nodes":      cmdNodesNoArgs,
	"cells":      cmdCellsNoArgs,
	"credential": cmdCredential,
	"health":     cmdHealth,
	"summary":    cmdSummaryNoArgs,
}

// isHelpAlias reports whether the user asked for help with one of the
// canonical help spellings. v17714-1: extracted from dispatch for clarity.
func isHelpAlias(cmd string) bool {
	return cmd == "-h" || cmd == "--help" || cmd == "help"
}

// cmdNodesNoArgs adapts cmdNodes to the subcommand table signature.
// v17714-1: bridge between variadic handler and no-arg original.
func cmdNodesNoArgs(reg *registra.Registry, _ []string) int { return cmdNodes(reg) }

// cmdCellsNoArgs adapts cmdCells to the subcommand table signature.
// v17714-1: bridge between variadic handler and no-arg original.
func cmdCellsNoArgs(reg *registra.Registry, _ []string) int { return cmdCells(reg) }

// cmdSummaryNoArgs adapts cmdSummary to the subcommand table signature.
// v17714-1: bridge between variadic handler and no-arg original.
func cmdSummaryNoArgs(reg *registra.Registry, _ []string) int { return cmdSummary(reg) }

// splitRegistryFlag pulls --registry VALUE or --registry=VALUE from args and
// returns the registry path plus remaining args. Centralising the two-pass
// parsing keeps subcommand helpers single-purpose.
func splitRegistryFlag(args []string) (string, []string) {
	registryPath := defaultRegistryPath
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--registry" && i+1 < len(args) {
			registryPath = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(a, "--registry=") {
			registryPath = strings.TrimPrefix(a, "--registry=")
			continue
		}
		out = append(out, a)
	}
	return registryPath, out
}

func cmdList(reg *registra.Registry, args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // do not pollute stdout/stderr on parse error
	var node, kind string
	fs.StringVar(&node, "node", "", "filter by primary_node alias")
	fs.StringVar(&kind, "kind", "", "filter by service kind")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "registra list:", err)
		return 2
	}
	svcs := filterServices(reg.Services, node, kind)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tNODE\tADDR\tPORT\tHEALTH\tSPRINT")
	for _, s := range svcs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			s.Name, s.Kind, s.PrimaryNode, s.Address, s.Port, s.HealthPath, s.OwnerSprint)
	}
	if err := tw.Flush(); err != nil {
		return 1
	}
	return 0
}

// filterServices returns the services that match node (alias) and kind. An
// empty filter means "match all". When both filters are set the result is the
// conjunction. v17714-1: pull the inline filter loop out of cmdList so the
// helper stays at CC <=6.
func filterServices(in []registra.Service, node, kind string) []registra.Service {
	out := in
	if node != "" {
		out = regServicesForNode(out, node)
	}
	if kind != "" {
		var k []registra.Service
		for _, s := range out {
			if s.Kind == kind {
				k = append(k, s)
			}
		}
		out = k
	}
	return out
}

// regServicesForNode is a local equivalent of (*Registry).ServicesForNode
// that operates on a pre-filtered slice. It mirrors the original semantics
// (sort by Port ascending) so callers and tests see the same order.
func regServicesForNode(in []registra.Service, node string) []registra.Service {
	var out []registra.Service
	for _, s := range in {
		if s.PrimaryNode == node {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func cmdShow(reg *registra.Registry, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "show: need NAME")
		return 2
	}
	s, ok := reg.FindService(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "show: no service %q\n", args[0])
		return 1
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	fmt.Println(string(b))
	return 0
}

func cmdNodes(reg *registra.Registry) int {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ALIAS\tHOSTNAME\tTAILNET_IP\tROLE\tUSER\tSSH_PORT")
	for _, n := range reg.Nodes {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n",
			n.Alias, n.CanonicalHostname, n.TailscaleIP, n.Role, n.User, n.SSHPort)
	}
	if err := tw.Flush(); err != nil {
		return 1
	}
	return 0
}

func cmdCells(reg *registra.Registry) int {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CELL\tNODE\tGPU\tMODEL\tENGINE\tPORT\tSTATUS")
	for _, c := range reg.LLMCells {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			c.CellID, c.Node, c.GPUClass, c.ModelID, c.Engine, c.HostPort, c.Status)
	}
	if err := tw.Flush(); err != nil {
		return 1
	}
	return 0
}

func cmdCredential(reg *registra.Registry, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "credential: need TITLE")
		return 2
	}
	c, ok := reg.FindCredentialByTitle(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "credential: no item %q\n", args[0])
		return 1
	}
	fmt.Printf("id=%s\ntitle=%s\nvault=%s\ncategory=%s\nop_uri=%s\n",
		c.ID, c.Title, c.Vault, c.Category, c.OPURI)
	return 0
}

func cmdHealth(reg *registra.Registry, args []string) int {
	var node string
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&node, "node", "", "limit to services on this node")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "registra health:", err)
		return 2
	}

	svcs := reg.Services
	if node != "" {
		svcs = reg.ServicesForNode(node)
	}
	return probeHealth(svcs)
}

// probeHealth probes the HealthPath of each service over HTTP, prints a
// traffic-light line per service, and returns 1 if any probe failed.
// v17714-1: pull out from cmdHealth so the dispatcher stays at CC ≤6.
func probeHealth(svcs []registra.Service) int {
	client := &http.Client{Timeout: 3 * time.Second}
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })
	pass, fail := 0, 0
	for _, s := range svcs {
		switch row := probeOneService(client, s); row.kind {
		case rowSkip:
			fmt.Printf("[skip] %-30s %s\n", s.Name, row.detail)
		case rowPass:
			fmt.Printf("[ OK ] %-30s %s\n", s.Name, row.detail)
			pass++
		case rowFail:
			fmt.Printf("[FAIL] %-30s %s\n", s.Name, row.detail)
			fail++
		}
	}
	fmt.Printf("\nhealth: pass=%d fail=%d total=%d\n", pass, fail, pass+fail)
	if fail > 0 {
		return 1
	}
	return 0
}

type healthRowKind int

const (
	rowSkip healthRowKind = iota
	rowPass
	rowFail
)

type healthRow struct {
	kind   healthRowKind
	detail string
}

// probeOneService issues a single HTTP health probe and classifies the
// outcome as skip / pass / fail. v17714-1: extracted from probeHealth
// to keep the dispatcher below CC ≤6.
func probeOneService(client *http.Client, s registra.Service) healthRow {
	if s.HealthPath == "" || s.Port == 0 {
		return healthRow{kind: rowSkip, detail: "no health_path"}
	}
	url := fmt.Sprintf("http://%s:%d%s", s.Address, s.Port, s.HealthPath)
	resp, err := client.Get(url)
	if err != nil {
		return healthRow{kind: rowFail, detail: fmt.Sprintf("%s err=%v", url, err)}
	}
	resp.Body.Close()
	if isHealthStatusOK(resp.StatusCode) {
		return healthRow{kind: rowPass, detail: fmt.Sprintf("%s status=%d", url, resp.StatusCode)}
	}
	return healthRow{kind: rowFail, detail: fmt.Sprintf("%s status=%d", url, resp.StatusCode)}
}

// isHealthStatusOK returns true when statusCode is in the 2xx-3xx range
// considered healthy. v17714-1: extracted to keep probeOneService simple.
func isHealthStatusOK(statusCode int) bool {
	return statusCode >= 200 && statusCode < 400
}

func cmdSummary(reg *registra.Registry) int {
	fmt.Printf("helixon service registry\n")
	fmt.Printf("  registry_version : %s\n", reg.RegistryVersion)
	fmt.Printf("  schema_version   : %d\n", reg.SchemaVersion)
	fmt.Printf("  central_node     : %s\n", reg.CentralNode)
	fmt.Printf("  services         : %d\n", len(reg.Services))
	fmt.Printf("  nodes            : %d\n", len(reg.Nodes))
	fmt.Printf("  llm_cells        : %d\n", len(reg.LLMCells))
	fmt.Printf("  credentials      : %d\n", len(reg.CredentialsIndex))
	fmt.Printf("  source_files     : %d\n", len(reg.SourceFiles))
	return 0
}
