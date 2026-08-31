// runx-public-repo-gate: allow-file *
package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRedact_NoToken(t *testing.T) {
	got := redact("op read failed: not found")
	if got != "op read failed: not found" {
		t.Errorf("expected no redaction, got %q", got)
	}
}

func TestRedact_WithToken(t *testing.T) {
	input := "op read failed: ops_eyJAbcDefGhiJklMnoPqrStuVwxYz.0123456789abcdef0123456789abcdef0123456789abcdef"
	got := redact(input)
	if !strings.Contains(got, "ops_eyJ[REDACTED]") {
		t.Errorf("expected redaction marker, got %q", got)
	}
	// Must NOT contain the full token
	if strings.Contains(got, "0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Errorf("token suffix leaked: %q", got)
	}
}

func TestRedact_TokenAtEnd(t *testing.T) {
	input := "prefix ops_eyJShort"
	got := redact(input)
	if !strings.Contains(got, "ops_eyJ[REDACTED]") {
		t.Errorf("expected redaction marker, got %q", got)
	}
}

func TestVersionConstant(t *testing.T) {
	if version == "" {
		t.Fatal("version constant empty")
	}
}

func TestExtractFromNotes_NoPattern(t *testing.T) {
	got, err := extractFromNotes("hello world", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestExtractFromNotes_ValidPattern(t *testing.T) {
	notes := "export FOO=bar\n# comment\nexport BAZ=qux"
	got, err := extractFromNotes(notes, `^export BAZ=(.+)$`)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "qux" {
		t.Errorf("got %q, want %q", got, "qux")
	}
}

func TestExtractFromNotes_NoMatch(t *testing.T) {
	_, err := extractFromNotes("plain text", `^does-not-match=(.+)$`)
	if err == nil {
		t.Errorf("expected error for non-matching pattern")
	}
}

func TestExtractFromNotes_InvalidPattern(t *testing.T) {
	_, err := extractFromNotes("text", "[unclosed")
	if err == nil {
		t.Errorf("expected error for invalid regex")
	}
}

func TestExtractFromNotes_TrimsWhitespace(t *testing.T) {
	notes := "key=  value  "
	got, err := extractFromNotes(notes, `^key=(.+)$`)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "value" {
		t.Errorf("got %q, want %q (trimmed)", got, "value")
	}
}

func TestParentDir(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/a/b/c", "/a/b"},
		{"a/b/c", "a/b"},
		{"file", ""},
		{"/single", ""},
		{"/", ""},
	}
	for _, tc := range tests {
		got := parentDir(tc.in)
		if got != tc.want {
			t.Errorf("parentDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOpRead_MissingToken(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	_, err := opRead("any-vault", "any-item", "any-field", 1)
	if err == nil {
		t.Fatal("expected error when token missing")
	}
	if !strings.Contains(err.Error(), "OP_SERVICE_ACCOUNT_TOKEN") {
		t.Errorf("expected token-related error, got %v", err)
	}
}

func TestServiceMap_KnownServices(t *testing.T) {
	want := []string{"engramd", "sprintboard-api", "llm-router", "svcregistryd", "fleet-agent", "evospined", "minimax-quota", "alert-notifier"}
	for _, name := range want {
		if _, ok := serviceMap[name]; !ok {
			t.Errorf("serviceMap missing %q", name)
		}
	}
}

// TestServiceMap_MinimaxQuotaHasThreeOrdinalKeys pins the shape the quota
// collector depends on: exactly three keys, named by ORDINAL only. The
// collector labels its metrics key_ordinal="1|2|3", so a fourth entry or a
// renamed env var silently drops a plan from the dashboard rather than
// failing loudly.
func TestServiceMap_MinimaxQuotaHasThreeOrdinalKeys(t *testing.T) {
	entries, ok := serviceMap["minimax-quota"]
	if !ok || len(entries) != 3 {
		t.Fatalf("minimax-quota entries = %d, want exactly 3", len(entries))
	}
	seenItems := map[string]bool{}
	for i, e := range entries {
		wantVar := fmt.Sprintf("MINIMAX_API_KEY_%d", i+1)
		if e.EnvVar != wantVar {
			t.Errorf("entry %d EnvVar = %q, want %q", i, e.EnvVar, wantVar)
		}
		if e.Field != "api-key" {
			t.Errorf("entry %d Field = %q, want api-key", i, e.Field)
		}
		if e.Extract != "" {
			t.Errorf("entry %d must be a direct field read, got Extract=%q", i, e.Extract)
		}
		if seenItems[e.ItemEnv] {
			t.Errorf("entry %d reuses item ref %q; the three plans must be distinct", i, e.ItemEnv)
		}
		seenItems[e.ItemEnv] = true
	}
}

// TestServiceMap_MinimaxQuotaKeyOneMatchesEngramFallback records a real
// coupling rather than a coincidence: engramd's PAID embedding fallback reads
// the same plan as quota key 1. That is why key 1 drains faster than the other
// two, and anyone re-pointing either entry needs to see the other move.
func TestServiceMap_MinimaxQuotaKeyOneMatchesEngramFallback(t *testing.T) {
	quota, ok := serviceMap["minimax-quota"]
	if !ok || len(quota) == 0 {
		t.Fatal("minimax-quota missing")
	}
	engram, ok := serviceMap["engramd"]
	if !ok || len(engram) == 0 {
		t.Fatal("engramd missing")
	}
	// Comparing the item REFERENCE (the variable name) rather than a literal
	// UUID is what keeps this coupling enforceable now that the UUIDs live in
	// the environment: sharing one variable makes the two entries resolve to
	// the same item by construction, and this test fails the moment someone
	// gives either its own variable.
	if quota[0].ItemEnv != engram[0].ItemEnv {
		t.Errorf("quota key 1 item ref %q != engramd embed-fallback item ref %q; if this is now intentional, update the comment in serviceMap",
			quota[0].ItemEnv, engram[0].ItemEnv)
	}
}

// TestBootstrapServiceEnv_StrictFailsWhenNothingResolves covers the failure
// mode this estate has already paid for: secrets-bootstrap exits 0 after
// resolving NOTHING, the unit's ExecStartPre is satisfied, and the service
// then crash-loops on a missing credential with no alert. Exiting 0 proves
// nothing, so --strict makes an unresolved entry a hard failure.
//
// Strict is opt-in precisely so this change cannot alter the startup
// behavior of the five services already wired to the permissive path.
func TestBootstrapServiceEnv_StrictFailsWhenNothingResolves(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "") // every opRead now fails fast
	provisionOPEnv(t)
	out := t.TempDir() + "/strict.env"

	err := bootstrapServiceEnv("fleet-agent", out, 2, true)
	if err == nil {
		t.Fatal("strict mode returned nil after resolving no entries")
	}
	if !strings.Contains(err.Error(), "unresolved") {
		t.Errorf("error %q should name the unresolved entries", err)
	}
	// A half-written env file is worse than none: it looks plausible to
	// the next reader and to the unit that sources it.
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("strict failure must not leave an env file behind at %s", out)
	}
}

// TestBootstrapServiceEnv_NonStrictPreservesLegacyBehaviour pins that the
// default path is unchanged, so existing units keep starting exactly as they
// do today.
func TestBootstrapServiceEnv_NonStrictPreservesLegacyBehaviour(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	provisionOPEnv(t)
	out := t.TempDir() + "/legacy.env"

	if err := bootstrapServiceEnv("fleet-agent", out, 2, false); err != nil {
		t.Fatalf("non-strict mode must not fail on unresolved entries, got %v", err)
	}
	b, readErr := os.ReadFile(out) //nolint:gosec // test-controlled temp path
	if readErr != nil {
		t.Fatalf("non-strict mode must still write the file: %v", readErr)
	}
	if !strings.Contains(string(b), "# LLM_ROUTER_TOKEN=<unavailable") {
		t.Errorf("expected an <unavailable> comment line, got:\n%s", b)
	}
}

// TestBootstrapServiceEnv_UnknownServiceIsRejected: a typo in a unit file must
// fail loudly rather than render an empty env file.
func TestBootstrapServiceEnv_UnknownServiceIsRejected(t *testing.T) {
	out := t.TempDir() + "/unknown.env"
	err := bootstrapServiceEnv("no-such-service", out, 2, false)
	if err == nil {
		t.Fatal("unknown service returned nil")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("unknown service must not create an env file")
	}
}

// TestBootstrapServiceEnv_OutputIsOwnerOnly: the rendered file holds live
// credentials and must never be group- or world-readable.
func TestBootstrapServiceEnv_OutputIsOwnerOnly(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	provisionOPEnv(t)
	out := t.TempDir() + "/perm.env"
	if err := bootstrapServiceEnv("fleet-agent", out, 2, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("env file mode = %o, want 600", perm)
	}
	if _, err := os.Stat(out + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind after a successful render")
	}
}

// TestServiceMap_AlertNotifierResolvesFieldByID guards the exact trap that
// cost time on 2026-08-28: the Resend item's field is LABELED "api key" with
// a space, and `op read op://vault/item/api key` fails on the space. The
// stable field ID is the only reference that resolves.
func TestServiceMap_AlertNotifierResolvesFieldByID(t *testing.T) {
	entries, ok := serviceMap["alert-notifier"]
	if !ok || len(entries) != 1 {
		t.Fatalf("alert-notifier entries = %d, want exactly 1 (RESEND_API_KEY)", len(entries))
	}
	e := entries[0]
	if e.EnvVar != "RESEND_API_KEY" {
		t.Errorf("EnvVar = %q, want RESEND_API_KEY", e.EnvVar)
	}
	if strings.Contains(e.Field, " ") {
		t.Errorf("Field %q contains a space; op read cannot resolve a spaced field label, use the field ID", e.Field)
	}
	if e.Extract != "" {
		t.Errorf("alert-notifier must be a direct field read, got Extract=%q", e.Extract)
	}
}

// TestServiceMap_FleetAgentUsesRouterToken replaces the v14571-era
// _extract pin. The old entries regex-extracted OPENAI_* from a secure
// note; the note drifted, extraction silently yielded nothing, and the
// fleet agent crash-looped 300+ times with secrets-bootstrap reporting
// success. The new wiring is a direct field read of the same item the
// llm-router service uses -- no regex, nothing to drift.
func TestServiceMap_FleetAgentUsesRouterToken(t *testing.T) {
	entries, ok := serviceMap["fleet-agent"]
	if !ok || len(entries) != 1 {
		t.Fatalf("fleet-agent entries = %d, want exactly 1 (LLM_ROUTER_TOKEN)", len(entries))
	}
	e := entries[0]
	if e.EnvVar != "LLM_ROUTER_TOKEN" {
		t.Errorf("EnvVar = %q, want LLM_ROUTER_TOKEN", e.EnvVar)
	}
	if e.Field == "_extract" || e.Extract != "" {
		t.Errorf("fleet-agent must use a direct field read, not _extract (Field=%q Extract=%q)", e.Field, e.Extract)
	}
	// Same credential the llm-router service reads: the agent
	// authenticates TO the router, so the two must never diverge.
	var routerTokenItem string
	for _, re := range serviceMap["llm-router"] {
		if re.EnvVar == "LLM_ROUTER_TOKEN" {
			routerTokenItem = re.ItemEnv
		}
	}
	if routerTokenItem == "" || e.ItemEnv != routerTokenItem {
		t.Errorf("fleet-agent token item ref %q must match llm-router's LLM_ROUTER_TOKEN item ref %q", e.ItemEnv, routerTokenItem)
	}
}

// routerTokenItem returns the item REFERENCE (the environment variable name)
// the llm-router service reads for its own client-auth bearer. Every service
// that CALLS the router must read the same item; a helper keeps the tests
// from re-deriving it.
func routerTokenItem(t *testing.T) string {
	t.Helper()
	for _, e := range serviceMap["llm-router"] {
		if e.EnvVar == "LLM_ROUTER_TOKEN" {
			return e.ItemEnv
		}
	}
	t.Fatal("llm-router serviceMap lacks LLM_ROUTER_TOKEN; every router caller pins to it")
	return ""
}

// TestServiceMap_RouterCallersShareOneToken is the guard for switching the
// router's client auth ON. Three services now speak to the router at
// 127.0.0.1:8787: the router itself (which holds the token), the fleet
// agent, and evospined. They authenticate with the SAME credential, so
// re-pointing one entry without the others must fail the build rather than
// produce a fleet where some callers 401 and nobody notices.
//
// The env var NAMES deliberately differ: each consumer's YAML decides what
// it expands, and evospined's serve.yaml expands ${OPENAI_API_KEY}. Pinning
// the name here is the point -- an entry under any other name renders an
// env file the runtime never reads, which is indistinguishable from no
// entry at all.
func TestServiceMap_RouterCallersShareOneToken(t *testing.T) {
	want := routerTokenItem(t)
	cases := []struct {
		service string
		envVar  string
		why     string
	}{
		{"fleet-agent", "LLM_ROUTER_TOKEN", "wsl1-fleet-agent.yaml expands ${LLM_ROUTER_TOKEN} once router auth is on"},
		{"evospined", "OPENAI_API_KEY", "helixon-control-plane/serve.yaml expands provider.api_key: \"${OPENAI_API_KEY}\""},
	}
	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			entries, ok := serviceMap[tc.service]
			if !ok || len(entries) != 1 {
				t.Fatalf("%s entries = %d, want exactly 1 (%s)", tc.service, len(entries), tc.envVar)
			}
			e := entries[0]
			if e.EnvVar != tc.envVar {
				t.Errorf("%s EnvVar = %q, want %q (%s)", tc.service, e.EnvVar, tc.envVar, tc.why)
			}
			if e.ItemEnv != want {
				t.Errorf("%s item ref %q != llm-router's LLM_ROUTER_TOKEN item ref %q; router callers must not diverge from the router",
					tc.service, e.ItemEnv, want)
			}
			// _extract is the drift mechanism that once crash-looped the
			// fleet agent 300+ times while secrets-bootstrap exited 0: the
			// secure note's text changed, the regex stopped matching, and
			// nothing said so. A direct field read cannot drift that way.
			if e.Field == "_extract" || e.Extract != "" {
				t.Errorf("%s must be a direct field read, got Field=%q Extract=%q", tc.service, e.Field, e.Extract)
			}
			if strings.Contains(e.Field, " ") {
				t.Errorf("%s Field %q contains a space; op read cannot resolve a spaced field label", tc.service, e.Field)
			}
		})
	}
}

// TestBootstrapServiceEnv_EvospinedIsAKnownService pins the boundary that
// actually failed: evospined.service already runs
// `secrets-bootstrap --service evospined` and hides the exit code behind a
// trailing `; touch`. While the service was unregistered that call returned
// "unknown service" forever and the unit started on a placeholder key.
// Reaching the render path at all is what this asserts.
func TestBootstrapServiceEnv_EvospinedIsAKnownService(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "") // no op CLI needed; every read fails fast
	provisionOPEnv(t)
	out := t.TempDir() + "/evospined.env"

	if err := bootstrapServiceEnv("evospined", out, 2, false); err != nil {
		t.Fatalf("evospined must be a known service, got %v", err)
	}
	b, err := os.ReadFile(out) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("expected an env file: %v", err)
	}
	if !strings.Contains(string(b), "OPENAI_API_KEY") {
		t.Errorf("rendered file must mention OPENAI_API_KEY, got:\n%s", b)
	}
}

// TestServiceMap_LLMRouterCarriesGatewayToken pins the v18774 addition:
// the router's minimax-m3 node authenticates to the HelixChannel edge,
// and the router refuses to boot when an auth_header node's key expands
// empty -- so this entry going missing is a router outage, not a
// degraded feature.
func TestServiceMap_LLMRouterCarriesGatewayToken(t *testing.T) {
	var found bool
	for _, e := range serviceMap["llm-router"] {
		if e.EnvVar != "HELIXCHANNEL_GATEWAY_TOKEN" {
			continue
		}
		found = true
		if e.Field == "_extract" || e.Extract != "" {
			t.Errorf("gateway token must be a direct field read, got Field=%q Extract=%q", e.Field, e.Extract)
		}
		if !strings.HasPrefix(e.ItemEnv, itemEnvPrefix) {
			t.Errorf("gateway token item ref = %q, want the %q prefix", e.ItemEnv, itemEnvPrefix)
		}
	}
	if !found {
		t.Fatal("llm-router serviceMap lacks HELIXCHANNEL_GATEWAY_TOKEN")
	}
}

func TestResolveField(t *testing.T) {
	tests := []struct{ in, want string }{
		{"_extract", "notesPlain"},
		{"password", "password"},
		{"api-key", "api-key"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := resolveField(tc.in); got != tc.want {
			t.Errorf("resolveField(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// testItemEnv / testFieldEnv are the variable names the unit tests provision.
// They are deliberately NOT any real entry's variable, so a test can never
// accidentally depend on an operator's actual environment.
const (
	testItemEnv  = "HLXN_OP_ITEM_UNIT_TEST"
	testFieldEnv = "HLXN_OP_FIELD_UNIT_TEST"
	// A syntactically valid 26-char item UUID that addresses nothing.
	testItemUUID  = "aaaaaaaaaaaaaaaaaaaaaaaaaa"
	testFieldUUID = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// provisionOPEnv gives a test the environment a real host gets from
// %h/.config/helixon/op-items.env. Tests that render an env file must call
// it: without a provisioned map the tool now fails closed by design, which
// is the whole point of the change.
func provisionOPEnv(t *testing.T) {
	t.Helper()
	t.Setenv(vaultEnv, "unit-test-vault")
	t.Setenv(testItemEnv, testItemUUID)
	t.Setenv(testFieldEnv, testFieldUUID)
	for _, entries := range serviceMap {
		for _, e := range entries {
			if e.ItemEnv != "" {
				t.Setenv(e.ItemEnv, testItemUUID)
			}
			if e.FieldEnv != "" {
				t.Setenv(e.FieldEnv, testFieldUUID)
			}
		}
	}
}

func TestFormatEnvLine_OpReadFails(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	provisionOPEnv(t)
	e := EnvEntry{
		EnvVar:  "TEST_VAR",
		ItemEnv: testItemEnv,
		Field:   "password",
	}
	line, err := formatEnvLine(e, 1)
	if err != nil {
		t.Fatalf("a failed op read is not a configuration error: %v", err)
	}
	if !strings.Contains(line, "# TEST_VAR=<unavailable:") {
		t.Errorf("expected unavailable marker, got %q", line)
	}
}

// TestFormatEnvLine_UnresolvedRefIsFatal is the fail-closed guard. An
// unprovisioned host must not get a renderable line: the previous outage in
// this estate was a unit whose ExecStartPre succeeded having resolved
// nothing, so the service started with no credential and crash-looped
// unalerted. A configuration error has to be an error, not a comment.
func TestFormatEnvLine_UnresolvedRefIsFatal(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	provisionOPEnv(t)
	t.Setenv(vaultEnv, "unit-test-vault")
	t.Setenv(testItemEnv, "") // provisioned nowhere

	_, err := formatEnvLine(EnvEntry{EnvVar: "TEST_VAR", ItemEnv: testItemEnv, Field: "password"}, 1)
	if err == nil {
		t.Fatal("an unset item reference must be a hard error, not a rendered comment line")
	}
	if !strings.Contains(err.Error(), testItemEnv) {
		t.Errorf("error must name the variable the operator has to export, got: %v", err)
	}
}

// TestResolveEntryRef_ErrorNeverEchoesValue: resolveEntryRef's errors reach
// stderr and the caller's error string. An operator who pastes an API key
// into HLXN_OP_ITEM_* must have it reported by LENGTH, never by value.
func TestResolveEntryRef_ErrorNeverEchoesValue(t *testing.T) {
	const pastedSecret = "sk-live-not-a-uuid-0000000000000000"
	t.Setenv(vaultEnv, "unit-test-vault")
	t.Setenv(testItemEnv, pastedSecret)

	_, _, _, err := resolveEntryRef(&EnvEntry{EnvVar: "TEST_VAR", ItemEnv: testItemEnv, Field: "password"})
	if err == nil {
		t.Fatal("expected an error for a non-UUID item reference")
	}
	if strings.Contains(err.Error(), pastedSecret) {
		t.Errorf("error text echoes the value into stderr and the env file: %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(len(pastedSecret))) {
		t.Errorf("error should report the observed length so the operator can debug, got: %v", err)
	}
}

// TestResolveEntryRef_VaultHasNoDefault: a baked-in default vault is the
// internal map this change removes, and a WRONG default is worse than none --
// it would point `op read` at whatever vault carries that name in the
// caller's account.
func TestResolveEntryRef_VaultHasNoDefault(t *testing.T) {
	t.Setenv(vaultEnv, "")
	t.Setenv(testItemEnv, testItemUUID)

	_, _, _, err := resolveEntryRef(&EnvEntry{EnvVar: "TEST_VAR", ItemEnv: testItemEnv, Field: "password"})
	if err == nil {
		t.Fatal("an unset vault must be an error; there must be no default")
	}
	if !strings.Contains(err.Error(), vaultEnv) {
		t.Errorf("error must name %s, got: %v", vaultEnv, err)
	}
}

// TestServiceMap_CarriesNoSecretStoreMap is the structural regression guard,
// and it is deliberately structural rather than a denylist of the nine
// identifiers this change removed: a denylist would have to re-commit them to
// a PUBLIC repository inside the very test that guards against them.
func TestServiceMap_CarriesNoSecretStoreMap(t *testing.T) {
	uuidLike := regexp.MustCompile(`^[a-z0-9]{26}$`)
	envNameLike := regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

	for service, entries := range serviceMap {
		for i, e := range entries {
			if e.ItemEnv == "" {
				t.Errorf("%s[%d] (%s) has no ItemEnv; every entry must reference its item through the environment", service, i, e.EnvVar)
				continue
			}
			if uuidLike.MatchString(e.ItemEnv) {
				t.Errorf("%s[%d] ItemEnv is a literal item UUID; it must name a %s* variable", service, i, itemEnvPrefix)
			}
			if !envNameLike.MatchString(e.ItemEnv) || !strings.HasPrefix(e.ItemEnv, itemEnvPrefix) {
				t.Errorf("%s[%d] ItemEnv = %q; want a variable name with the %q prefix", service, i, e.ItemEnv, itemEnvPrefix)
			}
			if uuidLike.MatchString(e.Field) {
				t.Errorf("%s[%d] Field is a literal 26-char identifier; use FieldEnv so it lives in the environment", service, i)
			}
			if e.FieldEnv != "" && !strings.HasPrefix(e.FieldEnv, fieldEnvPrefix) {
				t.Errorf("%s[%d] FieldEnv = %q; want the %q prefix", service, i, e.FieldEnv, fieldEnvPrefix)
			}
		}
	}
}

// TestSourceCarriesNoVaultName reads this package's own source and fails if
// a vault name literal reappears. serviceMap alone cannot catch a vault
// baked into a helper or a default.
// It scans the TEST file too, not just main.go. A guard that exempts itself
// leaves the obvious hiding place open, and the fixtures here are exactly
// where a real id would be pasted "just for a test".
func TestSourceCarriesNoVaultName(t *testing.T) {
	// Assembled from fragments on purpose. Written as plain literals these
	// patterns match THEMSELVES once this file is in scope, and the guard
	// reports a leak that is only its own source.
	vaultLike := []*regexp.Regexp{
		regexp.MustCompile(`(?i)` + "helixon" + `\s*` + "safe"),
		regexp.MustCompile(`(?i)` + "cursor" + "_" + "ironclaw"),
	}
	quotedID := regexp.MustCompile(`"[a-z0-9]{26}"`)

	for _, name := range []string{"main.go", "main_test.go"} {
		b, err := os.ReadFile(name) //nolint:gosec // fixed, in-package filenames
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, re := range vaultLike {
			if re.Match(b) {
				t.Errorf("%s contains a vault name literal (%s); the vault must come from %s", name, re, vaultEnv)
			}
		}
		for _, m := range quotedID.FindAllString(string(b), -1) {
			id := strings.Trim(m, `"`)
			// The synthetic fixtures are a single repeated character; a real
			// 1Password id is not, so allowing them costs nothing.
			if strings.Count(id, string(id[0])) == len(id) {
				continue
			}
			t.Errorf("%s contains a quoted 26-char identifier %q; item and field ids must come from the environment", name, m)
		}
	}
}

func TestFormatEnvLine_ExtractFailure(t *testing.T) {
	// Test extract failure path by injecting a vault entry whose opRead succeeds
	// but Extract regex does not match. We can't easily mock opRead without a
	// real op CLI, so we use a regex that will not match the "op-read-failed"
	// placeholder. Instead we verify that when opRead fails AND Extract is set,
	// we still get the unavailable marker (extract is never reached).
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	provisionOPEnv(t)
	e := EnvEntry{
		EnvVar:  "TEST_VAR",
		ItemEnv: testItemEnv,
		Field:   "_extract",
		Extract: `^does-not-match=(.+)$`,
	}
	line, err := formatEnvLine(e, 1)
	if err != nil {
		t.Fatalf("a failed op read is not a configuration error: %v", err)
	}
	if !strings.Contains(line, "# TEST_VAR=<unavailable:") {
		t.Errorf("expected unavailable marker, got %q", line)
	}
}

func TestListServiceNames(t *testing.T) {
	// Capture stdout by redirecting via a custom writer isn't easy in Go test,
	// so instead verify the function runs without panicking and the underlying
	// serviceMap contains at least the known services.
	old := os.Stdout
	defer func() { os.Stdout = old }()
	_, _ = io.Discard.Write(nil)
	r, w, _ := os.Pipe()
	os.Stdout = w
	listServiceNames()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "engramd") {
		t.Errorf("expected engramd in output, got %q", string(out))
	}
}

func TestPrintUsage(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	printUsage(os.Stderr)
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "secrets-bootstrap") {
		t.Errorf("expected usage banner, got %q", string(out))
	}
	if !strings.Contains(string(out), "--service") {
		t.Errorf("expected --service in usage, got %q", string(out))
	}
}

func TestPrintValueAndExport_NoExport(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	printValueAndExport("secret", "")
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if string(out) != "secret" {
		t.Errorf("got %q, want %q", string(out), "secret")
	}
}

func TestPrintValueAndExport_WithExport(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	printValueAndExport("secret", "MY_VAR")
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "secret") {
		t.Errorf("expected secret in output, got %q", string(out))
	}
	if !strings.Contains(string(out), "export MY_VAR=\"secret\"") {
		t.Errorf("expected export statement, got %q", string(out))
	}
}

// TestDispatch_StrictServiceFailsLoudly proves the flag is actually wired
// through the CLI, not merely present on the function: a unit that adds
// --strict must get a non-zero exit when a credential cannot be resolved.
// Without this, ExecStartPre would keep reporting success and the whole point
// of strict mode would be lost at the boundary that matters.
func TestDispatch_StrictServiceFailsLoudly(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	provisionOPEnv(t)
	out := t.TempDir() + "/dispatch-strict.env"

	rc := dispatch(cliArgs{ServiceName: "minimax-quota", OutPath: out, TimeoutSec: 2, Strict: true})
	if rc == 0 {
		t.Error("dispatch returned 0 for a strict run that resolved nothing")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("strict dispatch must not leave an env file behind")
	}
}

// TestDispatch_NonStrictServiceStillSucceeds is the paired guard: the default
// path must keep its current exit code so no existing unit changes behavior.
func TestDispatch_NonStrictServiceStillSucceeds(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	provisionOPEnv(t)
	out := t.TempDir() + "/dispatch-legacy.env"

	if rc := dispatch(cliArgs{ServiceName: "minimax-quota", OutPath: out, TimeoutSec: 2}); rc != 0 {
		t.Errorf("dispatch rc = %d, want 0 for the permissive default", rc)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("permissive dispatch should still write the file: %v", err)
	}
}

func TestDispatch_ShowVersion(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	rc := dispatch(cliArgs{ShowVersion: true, TimeoutSec: 5})
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if rc != 0 {
		t.Errorf("expected rc=0 for --version, got %d", rc)
	}
	if !strings.Contains(string(out), "secrets-bootstrap") {
		t.Errorf("expected version banner, got %q", string(out))
	}
}

func TestDispatch_ListServices(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	rc := dispatch(cliArgs{ListServices: true, TimeoutSec: 5})
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if rc != 0 {
		t.Errorf("expected rc=0 for --list, got %d", rc)
	}
	if !strings.Contains(string(out), "engramd") {
		t.Errorf("expected engramd in --list output, got %q", string(out))
	}
}

func TestDispatch_ServiceMissingOut(t *testing.T) {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()
	rc := dispatch(cliArgs{ServiceName: "engramd", OutPath: "", TimeoutSec: 5})
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if rc != 2 {
		t.Errorf("expected rc=2 for missing --out, got %d", rc)
	}
	if !strings.Contains(string(out), "--out is required") {
		t.Errorf("expected --out message, got %q", string(out))
	}
}

func TestDispatch_NoArgs(t *testing.T) {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()
	rc := dispatch(cliArgs{TimeoutSec: 5})
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if rc != 2 {
		t.Errorf("expected rc=2 for no args, got %d", rc)
	}
	if !strings.Contains(string(out), "usage:") {
		t.Errorf("expected usage banner, got %q", string(out))
	}
}

func TestDispatch_WrongArgCount(t *testing.T) {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()
	rc := dispatch(cliArgs{TimeoutSec: 5, Args: []string{"a", "b"}})
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if rc != 2 {
		t.Errorf("expected rc=2 for wrong arg count, got %d", rc)
	}
	if !strings.Contains(string(out), "usage:") {
		t.Errorf("expected usage banner, got %q", string(out))
	}
}

func TestDispatch_OpReadFailure(t *testing.T) {
	// Use a service-mode failure with an unknown service to exercise the
	// bootstrapServiceEnv error path.
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()
	rc := dispatch(cliArgs{ServiceName: "nonexistent-service-xyz", OutPath: "/tmp/nope.env", TimeoutSec: 5})
	_ = w.Close()
	_, _ = io.ReadAll(r)
	if rc != 1 {
		t.Errorf("expected rc=1 for unknown service bootstrap failure, got %d", rc)
	}
}

func TestOpReadWithExecutor_Success(t *testing.T) {
	executor := func() ([]byte, error) {
		return []byte("supersecret\n"), nil
	}
	val, err := opReadWithExecutor("op://v/i/f", 5, executor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "supersecret" {
		t.Errorf("expected trimmed value, got %q", val)
	}
}

func TestOpReadWithExecutor_Error(t *testing.T) {
	executor := func() ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}
	_, err := opReadWithExecutor("op://v/i/f", 5, executor)
	if err == nil || err.Error() != "boom" {
		t.Errorf("expected boom error, got %v", err)
	}
}

func TestOpReadWithExecutor_Timeout(t *testing.T) {
	executor := func() ([]byte, error) {
		time.Sleep(2 * time.Second)
		return []byte("never"), nil
	}
	_, err := opReadWithExecutor("op://v/i/f", 1, executor)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout error, got %v", err)
	}
}

func TestOpRead_TokenMissing(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	provisionOPEnv(t)
	_, err := opRead("v", "i", "f", 5)
	if err == nil || !strings.Contains(err.Error(), "OP_SERVICE_ACCOUNT_TOKEN not set") {
		t.Errorf("expected token missing error, got %v", err)
	}
}

func TestBootstrapServiceEnv_UnknownService(t *testing.T) {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()
	err := bootstrapServiceEnv("totally-fake-svc", "/tmp/nope.env", 5, false)
	_ = w.Close()
	_, _ = io.ReadAll(r)
	if err == nil {
		t.Error("expected error for unknown service")
	}
}

func TestBootstrapServiceEnv_BadOutPath(t *testing.T) {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()
	// Use a path that should fail to open (e.g. /proc/cannot-create)
	err := bootstrapServiceEnv("engramd", "/proc/cannot-create/secrets.env", 5, false)
	_ = w.Close()
	_, _ = io.ReadAll(r)
	if err == nil {
		t.Error("expected error when out path is not writable")
	}
}

func TestParentDir_Variants(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/tmp/foo.env", "/tmp"},
		{"foo.env", ""},
		{"/", ""},
		{"/a/b/c.env", "/a/b"},
	}
	for _, c := range cases {
		if got := parentDir(c.in); got != c.want {
			t.Errorf("parentDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatEnvLineFromValue_Success(t *testing.T) {
	e := EnvEntry{EnvVar: "FOO", ItemEnv: testItemEnv, Field: "f"}
	line := formatEnvLineFromValue(e, "bar")
	if line != `FOO="bar"`+"\n" {
		t.Errorf("got %q, want %q", line, `FOO="bar"`+"\n")
	}
}

func TestFormatEnvLineFromValue_WithExtract(t *testing.T) {
	e := EnvEntry{
		EnvVar:  "KEY1",
		ItemEnv: testItemEnv, Field: "_extract",
		Extract: `^export \w+=(\S+)$`,
	}
	line := formatEnvLineFromValue(e, "export KEY1=secret123")
	if line != `KEY1="secret123"`+"\n" {
		t.Errorf("got %q, want %q", line, `KEY1="secret123"`+"\n")
	}
}

func TestFormatEnvLineFromValue_ExtractMismatch(t *testing.T) {
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()
	e := EnvEntry{
		EnvVar:  "KEYX",
		ItemEnv: testItemEnv, Field: "_extract",
		Extract: `^nomatch=(.+)$`,
	}
	line := formatEnvLineFromValue(e, "no-match-content")
	_ = w.Close()
	_, _ = io.ReadAll(r)
	if !strings.Contains(line, "<extract failed>") {
		t.Errorf("expected extract failed marker, got %q", line)
	}
}

func TestBootstrapServiceEnv_Success(t *testing.T) {
	// With no OP token, opRead returns an error and the file should still
	// be created with an unavailable marker. This covers the full file
	// write / rename / chmod path.
	dir := t.TempDir()
	outPath := dir + "/test.env"

	oldMap := serviceMap
	serviceMap = map[string][]EnvEntry{
		"_test_svc": {
			{EnvVar: "MY_KEY", ItemEnv: testItemEnv, Field: "f"},
		},
	}
	t.Cleanup(func() { serviceMap = oldMap })

	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	provisionOPEnv(t)
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()
	err := bootstrapServiceEnv("_test_svc", outPath, 5, false)
	_ = w.Close()
	_, _ = io.ReadAll(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, rerr := os.ReadFile(outPath) //nolint:gosec // G304 test fixture
	if rerr != nil {
		t.Fatalf("could not read output: %v", rerr)
	}
	if !strings.Contains(string(data), "MY_KEY=<unavailable") {
		t.Errorf("expected unavailable marker, got %q", string(data))
	}
	if !strings.Contains(string(data), "secrets-bootstrap") {
		t.Errorf("expected generation header, got %q", string(data))
	}
}
