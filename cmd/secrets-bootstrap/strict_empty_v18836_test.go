package main

import (
	"os"
	"strings"
	"testing"
)

// v18836: an op read that SUCCEEDS against an unpopulated field renders
// KEY="" -- a line --strict used to accept. The board's shared bearer is
// exactly such a field today, and a service that sends an empty bearer is
// rejected, not treated as anonymous. Strict must refuse it.

func TestUnresolvedReason(t *testing.T) {
	t.Parallel()
	e := EnvEntry{EnvVar: "SPRINTBOARD_API_TOKEN"}
	cases := []struct {
		name, line, want string
	}{
		{"failed read is unavailable", "# SPRINTBOARD_API_TOKEN=<unavailable: x>\n", "unavailable"},
		{"successful empty read is empty", formatEnvLineFromValue(e, ""), "empty"},
		{"populated value is resolved", formatEnvLineFromValue(e, strings.Repeat("v", 32)), ""},
		{"a value that merely CONTAINS two quotes is resolved", formatEnvLineFromValue(e, `a""b`), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := unresolvedReason(tc.line); got != tc.want {
				t.Fatalf("unresolvedReason(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

// The two sides of the board's bearer read the SAME item and field, so they
// cannot drift onto different credentials.
func TestFleetAgentAndBoardShareTheBearerItem(t *testing.T) {
	t.Parallel()
	find := func(svc string) *EnvEntry {
		for i := range serviceMap[svc] {
			if serviceMap[svc][i].EnvVar == "SPRINTBOARD_API_TOKEN" {
				return &serviceMap[svc][i]
			}
		}
		return nil
	}
	board, agent := find("sprintboard-api"), find("fleet-agent")
	if board == nil || agent == nil {
		t.Fatalf("SPRINTBOARD_API_TOKEN must be mapped for both services: board=%v agent=%v", board != nil, agent != nil)
	}
	if board.ItemEnv != agent.ItemEnv || board.Field != agent.Field {
		t.Fatalf("board reads (%s,%s), agent reads (%s,%s); they must match", board.ItemEnv, board.Field, agent.ItemEnv, agent.Field)
	}
	if !strings.HasPrefix(board.ItemEnv, "HLXN_OP_ITEM_") {
		t.Fatalf("item env %q does not follow the HLXN_ convention", board.ItemEnv)
	}
}

// An optional credential must not fail --strict. The outage this prevents: the
// live fleet-agent unit runs the bootstrap with --strict and no fallback, so a
// fatal verdict over an unprovisioned board bearer deletes the whole env file,
// takes the router token with it, fails ExecStartPre, and the unit never
// starts -- over a credential the agent is designed to run without.
func TestStrict_OptionalEntryDoesNotFail(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "") // every opRead fails fast
	provisionOPEnv(t)
	out := t.TempDir() + "/optional.env"
	saved := serviceMap["__opt_test"]
	serviceMap["__opt_test"] = []EnvEntry{
		{EnvVar: "OPTIONAL_ONE", ItemEnv: "HLXN_OP_ITEM_SPRINTBOARD", Field: "password", Optional: true},
	}
	defer func() {
		if saved == nil {
			delete(serviceMap, "__opt_test")
		} else {
			serviceMap["__opt_test"] = saved
		}
	}()
	if err := bootstrapServiceEnv("__opt_test", out, 2, true); err != nil {
		t.Fatalf("strict must not fail for an optional entry, got: %v", err)
	}
	if _, statErr := os.Stat(out); statErr != nil {
		t.Fatalf("the env file must still be written: %v", statErr)
	}
}

// evospined is the THIRD board caller (S1 census 2026-09-22: its pinned
// dc8fcf5 binary registered anonymously every ~30s). Its replacement binary
// reads sprintboard.token from this env var, so the mapping must exist, must
// share the board's item/field, and must be Optional for the same
// --strict reason as the fleet agent's.
func TestEvospinedBearerMapping(t *testing.T) {
	t.Parallel()
	var bearer *EnvEntry
	for i := range serviceMap["evospined"] {
		if serviceMap["evospined"][i].EnvVar == "SPRINTBOARD_API_TOKEN" {
			bearer = &serviceMap["evospined"][i]
		}
	}
	if bearer == nil {
		t.Fatal("evospined must map SPRINTBOARD_API_TOKEN: without it the runtime registers anonymously and the board's required mode 401s every registration")
	}
	board, agent := "sprintboard-api", "fleet-agent"
	if bearer.ItemEnv != serviceMap[board][0].ItemEnv || bearer.Field != serviceMap[board][0].Field {
		t.Fatalf("evospined reads (%s,%s); the board reads (%s,%s); they must match",
			bearer.ItemEnv, bearer.Field, serviceMap[board][0].ItemEnv, serviceMap[board][0].Field)
	}
	if !bearer.Optional {
		t.Error("the board bearer must be Optional: --strict would otherwise take evospined down over an unprovisioned credential")
	}
	_ = agent // readability: the fleet-agent entry is covered by its own test above
}

// THE CONTROL. A non-optional entry must still fail --strict, or the tightening
// this sits beside has been switched off rather than scoped.
func TestStrict_NonOptionalEntryStillFails(t *testing.T) {
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
	provisionOPEnv(t)
	out := t.TempDir() + "/required.env"
	saved := serviceMap["__req_test"]
	serviceMap["__req_test"] = []EnvEntry{
		{EnvVar: "REQUIRED_ONE", ItemEnv: "HLXN_OP_ITEM_SPRINTBOARD", Field: "password"},
	}
	defer func() {
		if saved == nil {
			delete(serviceMap, "__req_test")
		} else {
			serviceMap["__req_test"] = saved
		}
	}()
	if err := bootstrapServiceEnv("__req_test", out, 2, true); err == nil {
		t.Fatal("strict must still fail for a non-optional unresolved entry")
	}
}

// The two entries the fleet agent carries differ in exactly this property, and
// the difference is load-bearing in both directions.
func TestFleetAgent_BearerOptional_RouterTokenNot(t *testing.T) {
	t.Parallel()
	var board, router *EnvEntry
	for i := range serviceMap["fleet-agent"] {
		switch serviceMap["fleet-agent"][i].EnvVar {
		case "SPRINTBOARD_API_TOKEN":
			board = &serviceMap["fleet-agent"][i]
		case "LLM_ROUTER_TOKEN":
			router = &serviceMap["fleet-agent"][i]
		}
	}
	if board == nil || router == nil {
		t.Fatal("fleet-agent must map both the board bearer and the router token")
	}
	if !board.Optional {
		t.Error("the board bearer must be Optional: --strict would otherwise take the unit down over an unprovisioned credential")
	}
	if router.Optional {
		t.Error("the router token must NOT be Optional: an agent that starts without it 401s on its first claimed ticket")
	}
}
