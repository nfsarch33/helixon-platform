// Command registra is the Helixon Service Registry CLI.
//
// Subcommands:
//   list [--node=ALIAS] [--kind=KIND]   list services
//   show NAME                            show one service
//   nodes                                list fleet nodes
//   cells                                list LLM cells
//   credential TITLE                     look up a 1Password item by title
//   health [--node=ALIAS]                probe http health endpoints
//   summary                              human-readable summary
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

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	// Two-pass flag parsing: pull global --registry flag first regardless of position.
	registryPath := defaultRegistryPath
	args := make([]string, 0, len(os.Args)-1)
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		if a == "--registry" && i+1 < len(os.Args) {
			registryPath = os.Args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(a, "--registry=") {
			registryPath = strings.TrimPrefix(a, "--registry=")
			continue
		}
		args = append(args, a)
	}

	cmd := args[0]
	cmdArgs := args[1:]

	reg, err := registra.Load(registryPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "registra: %v\n", err)
		os.Exit(1)
	}

	switch cmd {
	case "list":
		node := ""
		kind := ""
		fs2 := flag.NewFlagSet("list", flag.ExitOnError)
		fs2.StringVar(&node, "node", "", "filter by primary_node alias")
		fs2.StringVar(&kind, "kind", "", "filter by service kind")
		_ = fs2.Parse(cmdArgs)
		svcs := reg.Services
		if node != "" {
			svcs = reg.ServicesForNode(node)
		}
		if kind != "" {
			var out []registra.Service
			for _, s := range svcs {
				if s.Kind == kind {
					out = append(out, s)
				}
			}
			svcs = out
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tKIND\tNODE\tADDR\tPORT\tHEALTH\tSPRINT")
		for _, s := range svcs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
				s.Name, s.Kind, s.PrimaryNode, s.Address, s.Port, s.HealthPath, s.OwnerSprint)
		}
		tw.Flush()
	case "show":
		if len(cmdArgs) < 1 {
			fmt.Fprintln(os.Stderr, "show: need NAME")
			os.Exit(2)
		}
		s, ok := reg.FindService(cmdArgs[0])
		if !ok {
			fmt.Fprintf(os.Stderr, "show: no service %q\n", cmdArgs[0])
			os.Exit(1)
		}
		b, _ := json.MarshalIndent(s, "", "  ")
		fmt.Println(string(b))
	case "nodes":
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ALIAS\tHOSTNAME\tTAILNET_IP\tROLE\tUSER\tSSH_PORT")
		for _, n := range reg.Nodes {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n",
				n.Alias, n.CanonicalHostname, n.TailscaleIP, n.Role, n.User, n.SSHPort)
		}
		tw.Flush()
	case "cells":
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "CELL\tNODE\tGPU\tMODEL\tENGINE\tPORT\tSTATUS")
		for _, c := range reg.LLMCells {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
				c.CellID, c.Node, c.GPUClass, c.ModelID, c.Engine, c.HostPort, c.Status)
		}
		tw.Flush()
	case "credential":
		if len(cmdArgs) < 1 {
			fmt.Fprintln(os.Stderr, "credential: need TITLE")
			os.Exit(2)
		}
		c, ok := reg.FindCredentialByTitle(cmdArgs[0])
		if !ok {
			fmt.Fprintf(os.Stderr, "credential: no item %q\n", cmdArgs[0])
			os.Exit(1)
		}
		fmt.Printf("id=%s\ntitle=%s\nvault=%s\ncategory=%s\nop_uri=%s\n",
			c.ID, c.Title, c.Vault, c.Category, c.OPURI)
	case "health":
		fs2 := flag.NewFlagSet("health", flag.ExitOnError)
		node := fs2.String("node", "", "limit to services on this node")
		_ = fs2.Parse(cmdArgs)
		svcs := reg.Services
		if *node != "" {
			svcs = reg.ServicesForNode(*node)
		}
		client := &http.Client{Timeout: 3 * time.Second}
		sort.Slice(svcs, func(i, j int) bool { return svcs[i].Name < svcs[j].Name })
		pass, fail := 0, 0
		for _, s := range svcs {
			if s.HealthPath == "" || s.Port == 0 {
				fmt.Printf("[skip] %-30s no health_path\n", s.Name)
				continue
			}
			url := fmt.Sprintf("http://%s:%d%s", s.Address, s.Port, s.HealthPath)
			resp, err := client.Get(url)
			if err != nil {
				fmt.Printf("[FAIL] %-30s %s err=%v\n", s.Name, url, err)
				fail++
				continue
			}
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				fmt.Printf("[ OK ] %-30s %s status=%d\n", s.Name, url, resp.StatusCode)
				pass++
			} else {
				fmt.Printf("[FAIL] %-30s %s status=%d\n", s.Name, url, resp.StatusCode)
				fail++
			}
		}
		fmt.Printf("\nhealth: pass=%d fail=%d total=%d\n", pass, fail, pass+fail)
		if fail > 0 {
			os.Exit(1)
		}
	case "summary":
		fmt.Printf("helixon service registry\n")
		fmt.Printf("  registry_version : %s\n", reg.RegistryVersion)
		fmt.Printf("  schema_version   : %d\n", reg.SchemaVersion)
		fmt.Printf("  central_node     : %s\n", reg.CentralNode)
		fmt.Printf("  services         : %d\n", len(reg.Services))
		fmt.Printf("  nodes            : %d\n", len(reg.Nodes))
		fmt.Printf("  llm_cells        : %d\n", len(reg.LLMCells))
		fmt.Printf("  credentials      : %d\n", len(reg.CredentialsIndex))
		fmt.Printf("  source_files     : %d\n", len(reg.SourceFiles))
	case "-h", "--help", "help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "registra: unknown subcommand %q\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
}