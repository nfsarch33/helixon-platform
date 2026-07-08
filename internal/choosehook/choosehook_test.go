package choosehook

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DecideInput mirrors the JSON we expect Cursor to pass into the
// hook. Cursor's schema for beforeSubmitPrompt is proprietary; we
// keep this struct minimal so the contract is easy to test.
//
// All tests use the production constructor so any future field
// additions surface as test compile errors.
func newInput(prompt string) DecideInput {
	return DecideInput{Prompt: prompt, Surface: "editor"}
}

func TestClassifyTask_HeuristicRoutes(t *testing.T) {
	cases := []struct {
		prompt string
		want   Tier
	}{
		{"summarise this 12 words please", Tier0},
		{"discover the new login selector", Tier1},
		{"replay the cached pattern for /billing", Tier0},
		{"write a Go function that returns max", Tier2},
		{"audit this snippet for off by one", Tier3},
		{"evaluate the prompt quality from 1..10", Tier1},
		{"hello, fix this typo", Tier1},
	}
	for _, tc := range cases {
		t.Run(tc.prompt, func(t *testing.T) {
			got, err := ClassifyTask(newInput(tc.prompt))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDecide_ChoosesTierAndEmitsRedirect(t *testing.T) {
	// We supply a fake router via DecideWith so the test does not
	// depend on qwen36-matrix.yaml being on disk.
	in := newInput("write a Go function")
	dec := DecideWith(in, func(t Tier) (Decision, error) {
		return Decision{
			Tier:    t,
			CellID:  "C1",
			BaseURL: "http://127.0.0.1:8004/v1",
			Reason:  "mock",
		}, nil
	})
	out, err := dec()
	require.NoError(t, err)
	assert.Equal(t, "tier2", out.DecisionLabel)
	assert.Equal(t, "C1", out.CellID)
	assert.Equal(t, "http://127.0.0.1:8004/v1", out.BaseURL)
	assert.NotEmpty(t, out.CapturedPrompt, "prompt fingerprint must be set")
	assert.Regexp(t, `^fnv64a:[0-9a-f]{16}$`, out.CapturedPrompt)
}

func TestDecide_RedirectNone_WhenDisabled(t *testing.T) {
	in := DecideInput{Prompt: "fix typo", HookMode: "annotate"}
	dec := DecideWith(in, func(t Tier) (Decision, error) {
		return Decision{Tier: t, CellID: "X", BaseURL: "http://x/v1", Reason: "mock"}, nil
	})
	out, err := dec()
	require.NoError(t, err)
	assert.Equal(t, "annotate", out.HookMode)
	assert.Equal(t, "", out.BaseURL, "annotate mode must NOT rewrite base_url")
}

func TestDecide_RouterErrorFallsBackToLocalhost(t *testing.T) {
	in := newInput("hello world")
	dec := DecideWith(in, func(t Tier) (Decision, error) {
		return Decision{}, errFake("no cells")
	})
	out, err := dec()
	require.NoError(t, err)
	assert.Equal(t, "no_decision", out.DecisionLabel)
	assert.Equal(t, "no_ready_cell", out.Reason)
	assert.Empty(t, out.CellID)
}

func TestOutputJSON_MatchesContract(t *testing.T) {
	out := Output{
		SprintID:    "v14511",
		DecisionLabel: "tier2",
		CellID:      "C1",
		BaseURL:     "http://x/v1",
		HookMode:    "redirect",
		Reason:      "test",
		CapturedPrompt: "ping",
	}
	bb, err := json.Marshal(&out)
	require.NoError(t, err)
	for _, key := range []string{
		`"sprint_id":"v14511"`,
		`"decision_label":"tier2"`,
		`"cell_id":"C1"`,
		`"base_url":"http://x/v1"`,
		`"hook_mode":"redirect"`,
	} {
		assert.Contains(t, string(bb), key)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
