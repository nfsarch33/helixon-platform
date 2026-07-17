package main

import (
	"fmt"
	"io"
	"os"
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
	want := []string{"engramd", "sprintboard-api", "llm-router", "svcregistryd", "fleet-agent"}
	for _, name := range want {
		if _, ok := serviceMap[name]; !ok {
			t.Errorf("serviceMap missing %q", name)
		}
	}
}

func TestServiceMap_FleetAgentExtract(t *testing.T) {
	entries, ok := serviceMap["fleet-agent"]
	if !ok || len(entries) < 2 {
		t.Fatalf("fleet-agent entries missing or wrong count")
	}
	// Verify the entries have _extract field set
	for _, e := range entries {
		if e.Field != "_extract" {
			t.Errorf("expected _extract field, got %q", e.Field)
		}
		if e.Extract == "" {
			t.Errorf("expected non-empty Extract regex")
		}
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

func TestFormatEnvLine_OpReadFails(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	e := EnvEntry{
		EnvVar: "TEST_VAR",
		Vault:  "vault",
		Item:   "item",
		Field:  "password",
	}
	line := formatEnvLine(e, 1)
	if !strings.Contains(line, "# TEST_VAR=<unavailable:") {
		t.Errorf("expected unavailable marker, got %q", line)
	}
}

func TestFormatEnvLine_ExtractFailure(t *testing.T) {
	// Test extract failure path by injecting a vault entry whose opRead succeeds
	// but Extract regex does not match. We can't easily mock opRead without a
	// real op CLI, so we use a regex that will not match the "op-read-failed"
	// placeholder. Instead we verify that when opRead fails AND Extract is set,
	// we still get the unavailable marker (extract is never reached).
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	e := EnvEntry{
		EnvVar:  "TEST_VAR",
		Vault:   "vault",
		Item:    "item",
		Field:   "_extract",
		Extract: `^does-not-match=(.+)$`,
	}
	line := formatEnvLine(e, 1)
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
	err := bootstrapServiceEnv("totally-fake-svc", "/tmp/nope.env", 5)
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
	err := bootstrapServiceEnv("engramd", "/proc/cannot-create/secrets.env", 5)
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
	e := EnvEntry{EnvVar: "FOO", Vault: "v", Item: "i", Field: "f"}
	line := formatEnvLineFromValue(e, "bar")
	if line != `FOO="bar"`+"\n" {
		t.Errorf("got %q, want %q", line, `FOO="bar"`+"\n")
	}
}

func TestFormatEnvLineFromValue_WithExtract(t *testing.T) {
	e := EnvEntry{
		EnvVar: "KEY1",
		Vault:  "v", Item: "i", Field: "_extract",
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
		EnvVar: "KEYX",
		Vault:  "v", Item: "i", Field: "_extract",
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
			{EnvVar: "MY_KEY", Vault: "v", Item: "i", Field: "f"},
		},
	}
	t.Cleanup(func() { serviceMap = oldMap })

	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	defer func() { os.Stderr = old }()
	err := bootstrapServiceEnv("_test_svc", outPath, 5)
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
