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
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const version = "1.3.0"

// The vault name and every item UUID are deliberately NOT in this file.
// This repository is PUBLIC, and a vault name plus a set of item UUIDs is
// an internal map of the secret store: it says which vault exists, which
// items are in it, and what each one is for. Neither is a secret on its
// own -- reading an item still requires vault access, and an item UUID is
// an immutable identifier that cannot be rotated -- but the map is exactly
// what the `public-repo-gate` job exists to keep out. cmd/helixon-eval
// moved to this scheme in #90; this is the same change for the tool that
// actually renders production credentials.
//
// The operator supplies them at run time:
//
//	HLXN_OP_VAULT            vault name, shared by every entry
//	HLXN_OP_ITEM_<NAME>      26-char item UUID
//	HLXN_OP_FIELD_<NAME>     field ID, only where the field must be
//	                         referenced by ID rather than by label
//
// Both systemd units that invoke this tool load those from
// %h/.config/helixon/op-items.env via a NON-optional EnvironmentFile, so a
// host that has not been provisioned fails at unit start with a legible
// systemd error instead of rendering a credential-free env file.
const (
	// vaultEnv names the variable carrying the 1Password vault.
	//
	// It has no default on purpose. A baked-in default is the internal map
	// this indirection removes, and a WRONG default is worse than none: it
	// would point `op read` at whatever vault happens to carry that name in
	// the caller's account. Unset is a loud, immediate error.
	vaultEnv = "HLXN_OP_VAULT"

	// itemEnvPrefix / fieldEnvPrefix are asserted by a structural test so a
	// future entry cannot quietly reintroduce a literal.
	itemEnvPrefix  = "HLXN_OP_ITEM_"
	fieldEnvPrefix = "HLXN_OP_FIELD_"

	// opItemUUIDLen is the length of a 1Password item UUID. Enforced so a
	// display name -- which `op read` also accepts, and which leaks more
	// than a UUID does -- cannot be substituted by accident.
	opItemUUIDLen = 26
)

type EnvEntry struct {
	EnvVar string
	// ItemEnv is the NAME of the environment variable holding this entry's
	// 26-char item UUID -- never the UUID itself.
	//
	// Entries that must resolve to the SAME 1Password item share one
	// ItemEnv. That is load-bearing, not cosmetic: engramd's paid embedding
	// fallback and minimax-quota's key 1 are the same item, and all three
	// llm-cluster-router callers authenticate with the same bearer. Keying
	// on the item's identity rather than on the destination variable name
	// makes those couplings impossible to break by editing one site, and
	// keeps the tests that pin them meaningful.
	ItemEnv string
	// FieldEnv, when set, names the variable holding a field ID. Used only
	// where the field cannot be addressed by label; Field carries the
	// literal label otherwise. A label is not an identifier, so labels stay
	// in the file.
	FieldEnv string
	Field    string
	Extract  string // if set, apply this regex to op-read notesPlain value (use first capture group)
}

var serviceMap = map[string][]EnvEntry{
	"engramd": {
		{EnvVar: "ENGRAM_EMBED_KEY", ItemEnv: "HLXN_OP_ITEM_MINIMAX_1", Field: "api-key"},
	},
	"sprintboard-api": {
		{EnvVar: "SPRINTBOARD_API_TOKEN", ItemEnv: "HLXN_OP_ITEM_SPRINTBOARD", Field: "password"},
	},
	"llm-router": {
		{EnvVar: "LLM_ROUTER_TOKEN", ItemEnv: "HLXN_OP_ITEM_LLM_ROUTER", Field: "password"},
		// v18774: the router's minimax-m3 node now goes through the
		// HelixChannel edge (auth_header: X-HLXN-Token in the router
		// config) instead of straight to the provider. The config
		// expands api_key: "${HELIXCHANNEL_GATEWAY_TOKEN}", and the
		// router REFUSES TO BOOT when an auth_header node's key
		// expands empty -- this entry is load-bearing for llm-router
		// startup, not an optional extra.
		{EnvVar: "HELIXCHANNEL_GATEWAY_TOKEN", ItemEnv: "HLXN_OP_ITEM_HELIXCHANNEL_GATEWAY", Field: "token"},
	},
	"svcregistryd": {
		{EnvVar: "SVCREGISTRY_TOKEN", ItemEnv: "HLXN_OP_ITEM_SVCREGISTRY", Field: "password"},
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
		{EnvVar: "LLM_ROUTER_TOKEN", ItemEnv: "HLXN_OP_ITEM_LLM_ROUTER", Field: "password"},
	},
	// v18778: evospined (the EvoSpine DRL runtime) is the THIRD caller of
	// the local llm-cluster-router, and the only one secrets-bootstrap did
	// not know about. Its config -- cursor-global-kb/configs/
	// helixon-control-plane/serve.yaml -- sets provider.base_url
	// http://127.0.0.1:8787/v1 and provider.api_key: "${OPENAI_API_KEY}",
	// so OPENAI_API_KEY is the name the runtime expands (helixon's
	// expandEnv resolves exactly one ${VAR}); the VALUE it needs is the
	// router's own bearer -- the same item llm-router and fleet-agent read.
	// The agent authenticates TO the router, so the three must never
	// diverge.
	//
	// This entry is LOAD-BEARING for switching the router's client auth on,
	// not an optional extra. Until now `--service evospined` was an unknown
	// service, secrets-bootstrap exited non-zero, evospined.service's
	// trailing `; touch` swallowed that, and the runtime started on the
	// literal placeholder sk-noop-replaced-at-runtime-by-secrets-bootstrap.
	// That is invisible today only because the router's auth_token expands
	// empty and proxy.BearerAuthFunc short-circuits without reading the
	// Authorization header; the moment client auth is switched on, every
	// evospined completion 401s.
	//
	// It stays silent at STARTUP either way: expandEnv uses os.LookupEnv,
	// so a set-but-wrong key passes config validation and fails only
	// per-request. Startup success proves nothing here.
	"evospined": {
		{EnvVar: "OPENAI_API_KEY", ItemEnv: "HLXN_OP_ITEM_LLM_ROUTER", Field: "password"},
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
		{EnvVar: "MINIMAX_API_KEY_1", ItemEnv: "HLXN_OP_ITEM_MINIMAX_1", Field: "api-key"},
		{EnvVar: "MINIMAX_API_KEY_2", ItemEnv: "HLXN_OP_ITEM_MINIMAX_2", Field: "api-key"},
		{EnvVar: "MINIMAX_API_KEY_3", ItemEnv: "HLXN_OP_ITEM_MINIMAX_3", Field: "api-key"},
	},
	// v18776: the Alertmanager-to-email bridge, which exists because the
	// Slack webhook this fleet alerted through had been revoked and every
	// notification failed silently for the whole retained journal.
	//
	// The field here is a 1Password FIELD ID, not a label, and that is
	// deliberate: this item's field is labeled "api key" WITH A SPACE,
	// and `op read op://vault/item/api key` cannot resolve a spaced
	// label. The ID is stable and unambiguous. A test rejects any resolved
	// field containing a space so this cannot regress.
	//
	// Because that ID is a 26-char identifier like an item UUID, it moves
	// to the environment too (FieldEnv) rather than sitting in a public
	// file. Entries whose field is an ordinary label keep it inline.
	"alert-notifier": {
		{EnvVar: "RESEND_API_KEY", ItemEnv: "HLXN_OP_ITEM_RESEND", FieldEnv: "HLXN_OP_FIELD_RESEND"},
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
//
// Rendered into one buffer and written once, rather than as ~20 separate
// Fprint calls. Each of those is a separate unchecked error return, and a
// banner is not worth twenty errcheck exemptions.
func printUsage(w *os.File) {
	var b strings.Builder
	b.WriteString("usage: secrets-bootstrap <vault> <item> <field> [--export VAR]\n")
	b.WriteString("       secrets-bootstrap --service NAME --out FILE [--strict] [--list]\n\n")
	b.WriteString("--service mode resolves its 1Password references from the environment.\n")
	b.WriteString("Tell the operator what to export rather than making them read the source:\n\n")
	fmt.Fprintf(&b, "  %s\n", vaultEnv)
	b.WriteString("      1Password vault name, shared by every entry. No default.\n")
	fmt.Fprintf(&b, "  %s<NAME>\n", itemEnvPrefix)
	fmt.Fprintf(&b, "      %d-character item UUID. Entries that must resolve to the same\n", opItemUUIDLen)
	b.WriteString("      item share one variable.\n")
	fmt.Fprintf(&b, "  %s<NAME>\n", fieldEnvPrefix)
	b.WriteString("      Field ID, only where the field cannot be addressed by label.\n\n")
	b.WriteString("The variables each service needs, in the form it needs them:\n")
	for _, name := range sortedServiceNames() {
		fmt.Fprintf(&b, "  %s:\n", name)
		for _, e := range serviceMap[name] {
			if e.FieldEnv != "" {
				fmt.Fprintf(&b, "      %s <- %s + %s\n", e.EnvVar, e.ItemEnv, e.FieldEnv)
				continue
			}
			fmt.Fprintf(&b, "      %s <- %s (field %q)\n", e.EnvVar, e.ItemEnv, e.Field)
		}
	}
	_, _ = io.WriteString(w, b.String())
}

// sortedServiceNames gives the usage banner and --list a stable order.
// Ranging a map directly made both outputs reorder between runs, which
// turns any diff of them into noise.
func sortedServiceNames() []string {
	names := make([]string, 0, len(serviceMap))
	for name := range serviceMap {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	for _, name := range sortedServiceNames() {
		fmt.Println(name)
	}
}

// resolveEntryRef turns an EnvEntry's env-var NAMES into the concrete
// vault/item/field triple, validating as it goes.
//
// Every error here is a CONFIGURATION error -- an operator who has not
// provisioned %h/.config/helixon/op-items.env, or who exported a display
// name where a UUID belongs. That is categorically different from an
// op-read failure, which can be a transient vault problem, and the two are
// handled differently on purpose: see bootstrapServiceEnv.
//
// Errors report the offending value by LENGTH and never by value. An
// operator who pastes an API key into HLXN_OP_ITEM_* must not have it
// echoed into the env file, the journal, or stderr.
func resolveEntryRef(e *EnvEntry) (vault, item, field string, err error) {
	vault = strings.TrimSpace(os.Getenv(vaultEnv))
	if vault == "" {
		return "", "", "", fmt.Errorf("%s is unset; export the 1Password vault name (see %%h/.config/helixon/op-items.env)", vaultEnv)
	}
	if e.ItemEnv == "" {
		return "", "", "", fmt.Errorf("entry %s has no ItemEnv; every entry must name the variable carrying its item UUID", e.EnvVar)
	}
	item = strings.TrimSpace(os.Getenv(e.ItemEnv))
	if item == "" {
		return "", "", "", fmt.Errorf("%s is unset; export the 1Password item UUID for %s", e.ItemEnv, e.EnvVar)
	}
	if len(item) != opItemUUIDLen {
		return "", "", "", fmt.Errorf("%s holds %d characters; expected a %d-character 1Password item UUID (display names are not accepted)",
			e.ItemEnv, len(item), opItemUUIDLen)
	}

	field = e.Field
	if e.FieldEnv != "" {
		field = strings.TrimSpace(os.Getenv(e.FieldEnv))
		if field == "" {
			return "", "", "", fmt.Errorf("%s is unset; export the 1Password field ID for %s", e.FieldEnv, e.EnvVar)
		}
	}
	if field == "" {
		return "", "", "", fmt.Errorf("entry %s resolves to an empty field", e.EnvVar)
	}
	if strings.ContainsAny(field, " \t") {
		// `op read op://vault/item/api key` cannot resolve a spaced label;
		// the field ID must be used instead.
		return "", "", "", fmt.Errorf("entry %s resolves to a field containing whitespace; use the field ID, not the label", e.EnvVar)
	}
	return vault, item, field, nil
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
		cmd := exec.Command("op", "read", ref) //nolint:gosec // G204 fixed argv; ref is an operator-config secret reference
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
		line, cfgErr := formatEnvLine(e, timeoutSec)
		if cfgErr != nil {
			// A missing or malformed item reference means this host was
			// never provisioned. Publishing a partial file here is how the
			// last outage stayed invisible: the unit's ExecStartPre would
			// be satisfied and the service would start with no credential.
			// Fail closed, leave nothing behind, and name the variable.
			_ = w.Flush()
			_ = f.Close()
			_ = os.Remove(tmpPath)
			fmt.Fprintf(os.Stderr, "config: %s: %v\n", name, redact(cfgErr.Error()))
			return fmt.Errorf("config: %s: %w", name, cfgErr)
		}
		// formatEnvLine signals a failed op read by returning a comment
		// line rather than an assignment; that is the only signal available
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
//
// The returned error is non-nil ONLY for a configuration error -- an
// unresolvable item reference. That is fatal for the whole run regardless
// of --strict, because it means the host was never provisioned: retrying
// cannot fix it, and every other entry in the service will fail the same
// way. A failed op read is NOT fatal here; it keeps the pre-existing
// comment-line behaviour that --strict governs.
func formatEnvLine(e EnvEntry, timeoutSec int) (string, error) {
	vault, item, field, err := resolveEntryRef(&e)
	if err != nil {
		return "", err
	}
	val, err := opRead(vault, item, resolveField(field), timeoutSec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skip %s: %v\n", e.EnvVar, redact(err.Error()))
		return fmt.Sprintf("# %s=<unavailable: %s>\n", e.EnvVar, redact(err.Error())), nil
	}
	return formatEnvLineFromValue(e, val), nil
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
