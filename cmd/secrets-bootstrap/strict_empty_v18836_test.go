package main

import (
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
