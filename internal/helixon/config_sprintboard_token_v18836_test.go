package helixon

import (
	"strings"
	"testing"
)

// v18836: sprintboard.token is the one credential whose failure mode is an
// EMPTY value rather than a missing one (the secret bootstrap renders an
// unpopulated vault field as KEY=""). expandEnv passes that through, so the
// length check here is what stands between the agent and a 401 loop.

// A repeated byte, not a hex-looking literal: a fixture that a secret scanner
// reads as a credential fails the full-history gate for every commit after it.
var goodBoardToken = strings.Repeat("b", 36)

func decodeSprintboard(t *testing.T, token string) (RuntimeConfig, error) {
	t.Helper()
	doc := "agent_id: a\nsprintboard:\n  url: http://127.0.0.1:9400\n"
	if token != "" {
		doc += "  token: " + token + "\n"
	}
	fc, err := DecodeFileConfig([]byte(doc))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return fc.ToRuntimeConfig()
}

func TestSprintboardToken_AbsentIsAllowed(t *testing.T) {
	t.Parallel()
	cfg, err := decodeSprintboard(t, "")
	if err != nil {
		t.Fatalf("ToRuntimeConfig: %v", err)
	}
	if cfg.SprintboardToken != "" {
		t.Fatalf("token = %q, want empty", cfg.SprintboardToken)
	}
}

func TestSprintboardToken_LiteralIsCarried(t *testing.T) {
	t.Parallel()
	cfg, err := decodeSprintboard(t, goodBoardToken)
	if err != nil {
		t.Fatalf("ToRuntimeConfig: %v", err)
	}
	if cfg.SprintboardToken != goodBoardToken {
		t.Fatalf("token = %q, want %q", cfg.SprintboardToken, goodBoardToken)
	}
}

func TestSprintboardToken_EnvIsExpanded(t *testing.T) {
	t.Setenv("HLXN_TEST_BOARD_TOKEN_OK", goodBoardToken)
	cfg, err := decodeSprintboard(t, `"${HLXN_TEST_BOARD_TOKEN_OK}"`)
	if err != nil {
		t.Fatalf("ToRuntimeConfig: %v", err)
	}
	if cfg.SprintboardToken != goodBoardToken {
		t.Fatalf("token = %q, want the env value", cfg.SprintboardToken)
	}
}

// The case this file exists for: the variable is SET and EMPTY, which is what
// the secret bootstrap renders for an unpopulated vault field. It must load as
// "no token" (the client then sends no header) so the config can ship ahead
// of the credential; it must NOT become a 32-byte refusal.
func TestSprintboardToken_SetButEmptyMeansNoToken(t *testing.T) {
	t.Setenv("HLXN_TEST_BOARD_TOKEN_EMPTY", "")
	cfg, err := decodeSprintboard(t, `"${HLXN_TEST_BOARD_TOKEN_EMPTY}"`)
	if err != nil {
		t.Fatalf("an empty expansion must load as no token, got: %v", err)
	}
	if cfg.SprintboardToken != "" {
		t.Fatalf("token = %q, want empty", cfg.SprintboardToken)
	}
}

func TestSprintboardToken_ShortIsRefused(t *testing.T) {
	t.Parallel()
	_, err := decodeSprintboard(t, strings.Repeat("b", 31))
	if err == nil || !strings.Contains(err.Error(), "31 bytes") {
		t.Fatalf("a 31-byte token must be refused naming its length, got: %v", err)
	}
}

func TestSprintboardToken_UnsetEnvNamesTheField(t *testing.T) {
	t.Parallel()
	_, err := decodeSprintboard(t, `"${HLXN_TEST_BOARD_TOKEN_NEVER_SET_V18836}"`)
	if err == nil || !strings.Contains(err.Error(), "sprintboard.token env var") {
		t.Fatalf("error must name sprintboard.token, not api_key: %v", err)
	}
	if strings.Contains(err.Error(), "api_key") {
		t.Fatalf("error blames api_key for a sprintboard.token failure: %v", err)
	}
}

// The api_key messages are unchanged by the generalisation (byte-for-byte).
func TestExpandEnv_APIKeyMessagesUnchanged(t *testing.T) {
	t.Parallel()
	if _, err := expandEnv("${}"); err == nil || err.Error() != "helixon: empty env var reference in api_key" {
		t.Fatalf("empty ref message changed: %v", err)
	}
	if _, err := expandEnv("${HLXN_TEST_NEVER_SET_V18836_APIKEY}"); err == nil ||
		err.Error() != `helixon: api_key env var "HLXN_TEST_NEVER_SET_V18836_APIKEY" is not set` {
		t.Fatalf("unset message changed: %v", err)
	}
}

// The two sides of the shared bearer must agree on the same bytes. The board
// trims what it reads from this variable; the secret bootstrap renders the
// vault field with %q, so whitespace around a provisioned value survives into
// the environment. If only one side trims, every write is a 401 whose cause is
// invisible from either side's logs.
func TestSprintboardToken_ResolvedValueIsTrimmed(t *testing.T) {
	t.Setenv("HLXN_TEST_BOARD_TOKEN_PADDED", "  "+goodBoardToken+"\n")
	cfg, err := decodeSprintboard(t, `"${HLXN_TEST_BOARD_TOKEN_PADDED}"`)
	if err != nil {
		t.Fatalf("ToRuntimeConfig: %v", err)
	}
	if cfg.SprintboardToken != goodBoardToken {
		t.Fatalf("token = %q, want the trimmed value %q", cfg.SprintboardToken, goodBoardToken)
	}
}

// A value that is ONLY whitespace is no token, not a short one: it must load
// as absent (no header) rather than trip the length check.
func TestSprintboardToken_WhitespaceOnlyIsNoToken(t *testing.T) {
	t.Setenv("HLXN_TEST_BOARD_TOKEN_BLANK", "   \t\n")
	cfg, err := decodeSprintboard(t, `"${HLXN_TEST_BOARD_TOKEN_BLANK}"`)
	if err != nil {
		t.Fatalf("a whitespace-only token must load as no token, got: %v", err)
	}
	if cfg.SprintboardToken != "" {
		t.Fatalf("token = %q, want empty", cfg.SprintboardToken)
	}
}
