// runx-public-repo-gate: allow-file *
// secrets-bootstrap — read 1Password secrets via op CLI.
//
// Usage:
//
//	secrets-bootstrap vault item field [--export VAR]
//	secrets-bootstrap --service NAME --out FILE
//
// v14547: Added --service/--out mode for systemd EnvironmentFile generation.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const version = "1.2.0"

type EnvEntry struct {
	EnvVar  string
	Vault   string
	Item    string
	Field   string
	Extract string // if set, apply this regex to op-read notesPlain value (use first capture group)
}

var serviceMap = map[string][]EnvEntry{
	"engramd": {
		{EnvVar: "ENGRAM_EMBED_KEY", Vault: "HelixonSafe", Item: "ripotpfq43jzlreor4zo2ay734", Field: "api-key"},
	},
	"sprintboard-api": {
		{EnvVar: "SPRINTBOARD_API_TOKEN", Vault: "HelixonSafe", Item: "w7uspwgtg4y5gh6m4fdnxtu6lu", Field: "password"},
	},
	"llm-router": {
		{EnvVar: "LLM_ROUTER_TOKEN", Vault: "HelixonSafe", Item: "hfri3ziy6cjfec4xha7wkfkkri", Field: "password"},
		// v18774: the router's minimax-m3 node now goes through the
		// HelixChannel edge (auth_header: X-HLXN-Token in the router
		// config) instead of straight to the provider. The config
		// expands api_key: "${HELIXCHANNEL_GATEWAY_TOKEN}", and the
		// router REFUSES TO BOOT when an auth_header node's key
		// expands empty -- this entry is load-bearing for llm-router
		// startup, not an optional extra.
		{EnvVar: "HELIXCHANNEL_GATEWAY_TOKEN", Vault: "HelixonSafe", Item: "w4lshnyh2yfzhvcyf6ezspyhv4", Field: "token"},
	},
	"svcregistryd": {
		{EnvVar: "SVCREGISTRY_TOKEN", Vault: "HelixonSafe", Item: "62ruxw2zud5fp7jpxgi2cgjb64", Field: "password"},
	},
	// v18774: the fleet agent's provider is the local llm-cluster-router
	// (openai-compat, base_url http://127.0.0.1:8787/v1), so the only
	// credential it needs is the router's own bearer token -- the same
	// item the llm-router service reads. The previous two entries
	// extracted OPENAI_BASE_URL / OPENAI_API_KEY from a secure note via
	// `^export OPENAI_API_KEY=(.+)$` regexes; the note's content had
	// drifted, extraction silently yielded nothing (secrets-bootstrap
	// still exited 0), and `helixon serve` then crash-looped 300+ times
	// on "api_key env var OPENAI_API_KEY is not set" without a single
	// alert. A direct field read cannot drift the same way.
	"fleet-agent": {
		{EnvVar: "LLM_ROUTER_TOKEN", Vault: "HelixonSafe", Item: "hfri3ziy6cjfec4xha7wkfkkri", Field: "password"},
	},
	// v18776: per-key MiniMax coding-plan quota polling. The collector
	// (`helix-dev-tools minimax-quota`) labels its metrics by ORDINAL
	// only -- key_ordinal="1|2|3" -- so a key value never reaches a
	// metric, a log or a dashboard. The ordering here IS the ordinal, so
	// reordering these entries silently renames every series.
	//
	// Key 1 is deliberately the same plan engramd uses for its PAID
	// embedding fallback (ENGRAM_EMBED_KEY above). That coupling is why
	// key 1 drains faster than keys 2 and 3, and a test pins it so the
	// relationship cannot be broken by accident.
	"minimax-quota": {
		{EnvVar: "MINIMAX_API_KEY_1", Vault: "HelixonSafe", Item: "ripotpfq43jzlreor4zo2ay734", Field: "api-key"},
		{EnvVar: "MINIMAX_API_KEY_2", Vault: "HelixonSafe", Item: "hblg7fxbhxnlmnv3us3i6jo2wu", Field: "api-key"},
		{EnvVar: "MINIMAX_API_KEY_3", Vault: "HelixonSafe", Item: "jdxsktex2y2euuuul4g6r5e6fa", Field: "api-key"},
	},
	// v18776: the Alertmanager-to-email bridge, which exists because the
	// Slack webhook this fleet alerted through had been revoked and every
	// notification failed silently for the whole retained journal.
	//
	// The Field here is a 1Password FIELD ID, not a label, and that is
	// deliberate: this item's field is labeled "api key" WITH A SPACE,
	// and `op read op://vault/item/api key` cannot resolve a spaced
	// label. The ID is stable and unambiguous. A test rejects any Field
	// containing a space so this cannot regress.
	"alert-notifier": {
		{EnvVar: "RESEND_API_KEY", Vault: "HelixonSafe", Item: "pwjkp2gii6cnaqwwj4fmesdxd4", Field: "t6x353nppiwc4spgc4mkpjenyy"},
	},
}

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	exportVar := flag.String("export", "", "also export the value to this env var (KEY=val)")
	timeoutSec := flag.Int("timeout", 10, "timeout in seconds for the op CLI call")
	serviceName := flag.String("service", "", "service name to bootstrap env for")
	outPath := flag.String("out", "", "output env file path (used with --service)")
	listServices := flag.Bool("list", false, "list known service names and exit")
	strict := flag.Bool("strict", false, "fail (and write no env file) if any entry cannot be resolved")
	flag.Parse()

	os.Exit(dispatch(cliArgs{
		ShowVersion:  *showVersion,
		ListServices: *listServices,
		ServiceName:  *serviceName,
		OutPath:      *outPath,
		TimeoutSec:   *timeoutSec,
		ExportVar:    *exportVar,
		Strict:       *strict,
		Args:         flag.Args(),
	}))
}

// cliArgs is the structured input to dispatch, derived from CLI flags.
type cliArgs struct {
	ShowVersion  bool
	ListServices bool
	ServiceName  string
	OutPath      string
	TimeoutSec   int
	ExportVar    string
	Strict       bool
	Args         []string
}

// dispatch handles the main CLI logic for testing.
//
// Return values:
//
//	0 — success (or handled exit like --version / --list)
//	1 — opRead failure
//	2 — usage / configuration error
func dispatch(a cliArgs) int {
	if a.ShowVersion {
		fmt.Printf("secrets-bootstrap %s\n", version)
		return 0
	}

	if a.ListServices {
		listServiceNames()
		return 0
	}

	if a.ServiceName != "" {
		if a.OutPath == "" {
			fmt.Fprintln(os.Stderr, "--out is required with --service")
			return 2
		}
		if err := bootstrapServiceEnv(a.ServiceName, a.OutPath, a.TimeoutSec, a.Strict); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0
	}

	if len(a.Args) != 3 {
		printUsage(os.Stderr)
		return 2
	}
	vault, item, field := a.Args[0], a.Args[1], a.Args[2]
	val, err := opRead(vault, item, field, a.TimeoutSec)
	if err != nil {
		fmt.Fprintln(os.Stderr, redact(fmt.Sprintf("op read failed: %v", err)))
		return 1
	}
	printValueAndExport(val, a.ExportVar)
	return 0
}

// printUsage emits the standard usage banner to the given writer.
func printUsage(w *os.File) {
	fmt.Fprintln(w, "usage: secrets-bootstrap <vault> <item> <field> [--export VAR]")
	fmt.Fprintln(w, "       secrets-bootstrap --service NAME --out FILE [--strict] [--list]")
}

// printValueAndExport emits the secret value and optional export statement.
func printValueAndExport(val, exportVar string) {
	fmt.Print(val)
	if exportVar != "" {
		fmt.Printf("\nexport %s=%q", exportVar, val)
	}
}

// listServiceNames prints all known service names to stdout (extracted for testability).
func listServiceNames() {
	for name := range serviceMap {
		fmt.Println(name)
	}
}

func opRead(vault, item, field string, timeoutSec int) (string, error) {
	token := os.Getenv("OP_SERVICE_ACCOUNT_TOKEN")
	if token == "" {
		return "", fmt.Errorf("OP_SERVICE_ACCOUNT_TOKEN not set; cannot proceed")
	}
	ref := fmt.Sprintf("op://%s/%s/%s", vault, item, field)
	return opReadWithExecutor(ref, timeoutSec, defaultOpExecutor(token, ref))
}

// opExecutor abstracts the op CLI invocation for testability.
type opExecutor func() ([]byte, error)

// opReadWithExecutor runs an op-read with a bounded timeout using a caller-
// supplied executor (returns stdout bytes and an error). Returns ("", error)
// on timeout. Exposed for tests.
func opReadWithExecutor(ref string, timeoutSec int, run opExecutor) (string, error) {
	done := make(chan struct {
		val string
		err error
	}, 1)
	go func() {
		out, err := run()
		if err != nil {
			done <- struct {
				val string
				err error
			}{"", err}
			return
		}
		done <- struct {
			val string
			err error
		}{strings.TrimRight(string(out), "\n"), nil}
	}()
	select {
	case r := <-done:
		return r.val, r.err
	case <-time.After(time.Duration(timeoutSec) * time.Second):
		return "", fmt.Errorf("op read %q timed out after %ds", ref, timeoutSec)
	}
}

// defaultOpExecutor returns a real opExecutor that invokes the `op` CLI.
func defaultOpExecutor(token, ref string) opExecutor {
	return func() ([]byte, error) {
		cmd := exec.Command("op", "read", ref)
		cmd.Env = append(os.Environ(), "OP_SERVICE_ACCOUNT_TOKEN="+token)
		return cmd.Output()
	}
}

func extractFromNotes(notes, pattern string) (string, error) {
	if pattern == "" {
		return strings.TrimSpace(notes), nil
	}
	re, err := regexp.Compile("(?m)" + pattern)
	if err != nil {
		return "", fmt.Errorf("invalid extract regex: %w", err)
	}
	m := re.FindStringSubmatch(notes)
	if m == nil {
		return "", fmt.Errorf("pattern %q did not match notesPlain", pattern)
	}
	return strings.TrimSpace(m[1]), nil
}

// bootstrapServiceEnv renders one service's credentials into an env file.
//
// strict controls what an UNRESOLVED entry means. In the default (permissive)
// mode an entry that cannot be read becomes a "# KEY=<unavailable>" comment
// and the command still succeeds -- which is how a service once crash-looped
// 300+ times behind an ExecStartPre that reported success. In strict mode any
// unresolved entry is a hard error and no env file is left behind, so a unit
// fails fast and legibly instead of starting without its credentials.
//
// Strict is opt-in so that enabling it is a deliberate, per-unit rollout
// rather than a silent behavior change for every service already wired up.
func bootstrapServiceEnv(name, outPath string, timeoutSec int, strict bool) error {
	entries, ok := serviceMap[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown service %q (use --list to see known services)\n", name)
		return fmt.Errorf("unknown service %q", name)
	}
	if dir := parentDir(outPath); dir != "" {
		_ = os.MkdirAll(dir, 0700)
	}
	tmpPath := outPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600) //nolint:gosec // G304 file op with operator/cli-provided path
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", tmpPath, err)
		return fmt.Errorf("open %s: %w", tmpPath, err)
	}
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "# Generated by secrets-bootstrap %s at %s for service %q\n", version, time.Now().UTC().Format(time.RFC3339), name)
	var unresolved []string
	for _, e := range entries {
		line := formatEnvLine(e, timeoutSec)
		// formatEnvLine signals failure by returning a comment line
		// rather than an assignment; that is the only signal available
		// without changing its contract, which other callers rely on.
		if strings.HasPrefix(line, "# ") {
			unresolved = append(unresolved, e.EnvVar)
		}
		fmt.Fprint(w, line)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "flush: %v\n", err)
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("flush: %w", err)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "close: %v\n", err)
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close: %w", err)
	}
	if strict && len(unresolved) > 0 {
		// Do not publish a half-populated env file: it reads as
		// plausible to both the next operator and the unit that
		// sources it, which is exactly how this failure stayed
		// invisible last time.
		_ = os.Remove(tmpPath)
		fmt.Fprintf(os.Stderr, "strict: %d unresolved entr(y|ies): %s\n", len(unresolved), strings.Join(unresolved, ", "))
		return fmt.Errorf("strict: unresolved entries: %s", strings.Join(unresolved, ", "))
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		fmt.Fprintf(os.Stderr, "rename: %v\n", err)
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	_ = syscall.Chmod(outPath, 0600)
	return nil
}

// resolveField maps "_extract" sentinel field to "notesPlain" for op-read API.
func resolveField(field string) string {
	if field == "_extract" {
		return "notesPlain"
	}
	return field
}

// formatEnvLine renders a single EnvEntry as a quoted KEY="value" line (or a
// "# KEY=<unavailable>" comment when the op read fails). Exposed for tests
// that do not want to call opRead for real.
func formatEnvLine(e EnvEntry, timeoutSec int) string {
	val, err := opRead(e.Vault, e.Item, resolveField(e.Field), timeoutSec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skip %s: %v\n", e.EnvVar, redact(err.Error()))
		return fmt.Sprintf("# %s=<unavailable: %s>\n", e.EnvVar, redact(err.Error()))
	}
	return formatEnvLineFromValue(e, val)
}

// formatEnvLineFromValue renders the env line given a successfully read value.
// This split is intentional so tests can cover the post-opRead logic without
// invoking the op CLI.
func formatEnvLineFromValue(e EnvEntry, val string) string {
	if e.Extract != "" {
		extracted, eerr := extractFromNotes(val, e.Extract)
		if eerr != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", e.EnvVar, redact(eerr.Error()))
			return fmt.Sprintf("# %s=<extract failed>\n", e.EnvVar)
		}
		val = extracted
	}
	return fmt.Sprintf("%s=%q\n", e.EnvVar, val)
}

func parentDir(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return ""
	}
	return p[:idx]
}

func redact(s string) string {
	const prefix = "ops_eyJ"
	if idx := strings.Index(s, prefix); idx >= 0 {
		end := idx + 60
		if end >= len(s) {
			return s[:idx] + prefix + "[REDACTED]"
		}
		return s[:idx] + prefix + "[REDACTED]" + s[end:]
	}
	return s
}
