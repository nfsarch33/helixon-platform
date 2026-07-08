package smoke

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPrompts_ReadsTenPrompts(t *testing.T) {
	prompts, err := LoadPromptsFile("../../eval-harness/prompts-10.json")
	require.NoError(t, err)
	assert.Len(t, prompts, 10, "v14510 deliverable is the 10-prompt smoke")

	countByTier := map[int]int{}
	for _, p := range prompts {
		countByTier[p.Tier]++
	}
	assert.Equal(t, 4, countByTier[0], "4 tier0 prompts")
	assert.Equal(t, 3, countByTier[1], "3 tier1 prompts")
	assert.Equal(t, 1, countByTier[2], "1 tier2 prompt")
	assert.Equal(t, 2, countByTier[3], "2 tier3 prompts")
}

func TestCheckSubstringContainment(t *testing.T) {
	r := Rubric{ContainsSubstrings: []string{"alpha", "beta"}}
	assert.True(t, r.Accepts("alpha and beta both present"))
	assert.False(t, r.Accepts("only alpha"))
}

func TestCheckSubstringsAny(t *testing.T) {
	r := Rubric{ContainsSubstringsAny: []string{"hello", "world"}}
	assert.True(t, r.Accepts("hello there"))
	assert.True(t, r.Accepts("world news"))
	assert.False(t, r.Accepts("nothing matching"))
}

func TestCheckMaxWords(t *testing.T) {
	r := Rubric{MaxWords: 5}
	assert.True(t, r.Accepts("one two three four"))
	assert.False(t, r.Accepts("one two three four five six"))
}

func TestCheckMinNewlines(t *testing.T) {
	r := Rubric{MinNewlines: 3}
	assert.True(t, r.Accepts("a\nb\nc\nd"))
	assert.False(t, r.Accepts("a\nb\nc"))
}

func TestCheckMaxCompletionTokens(t *testing.T) {
	r := Rubric{MaxCompletionTokens: 4}
	shortResp := "two words"
	longResp := "this response is more than four words long definitely"
	assert.True(t, r.Accepts(shortResp))
	assert.False(t, r.Accepts(longResp))
}

func TestCheckRegex(t *testing.T) {
	r := Rubric{Regex: "^[1-9]$|^10$"}
	assert.True(t, r.Accepts("5"))
	assert.True(t, r.Accepts("10"))
	assert.False(t, r.Accepts("0"))
	assert.False(t, r.Accepts("11"))
}

func TestCheckJSONArrayMinLen(t *testing.T) {
	r := Rubric{JSONArrayMinLen: 3}
	assert.True(t, r.Accepts(`["a","b","c","d"]`))
	assert.True(t, r.Accepts(`["a","b","c"]`))
	assert.False(t, r.Accepts(`["a","b"]`))
	assert.False(t, r.Accepts(`not-json`))
}

func TestCheckMinWords(t *testing.T) {
	r := Rubric{MinWords: 5}
	assert.True(t, r.Accepts("one two three four five"))
	assert.False(t, r.Accepts("one two"))
}

func TestReportAggregation_SummarisesScoreboard(t *testing.T) {
	results := []Result{
		{ID: "a", Passed: true, Tier: 0},
		{ID: "b", Passed: false, Tier: 0},
		{ID: "c", Passed: true, Tier: 1},
		{ID: "d", Passed: true, Tier: 1},
		{ID: "e", Passed: false, Tier: 2},
	}
	board := Aggregate(results)
	assert.Equal(t, 3, board.Passed)
	assert.Equal(t, 5, board.Total)
	assert.InDelta(t, 60.0, board.Percentage(), 0.01)
	assert.Equal(t, 1, board.ByTier[0].Passed)
	assert.Equal(t, 2, board.ByTier[0].Total)
	assert.Equal(t, 2, board.ByTier[1].Passed)
	assert.Equal(t, 2, board.ByTier[1].Total)
	assert.Equal(t, 0, board.ByTier[2].Passed)
	assert.Equal(t, 1, board.ByTier[2].Total)
	assert.Equal(t, 0, board.ByTier[3].Passed)
	assert.Equal(t, 0, board.ByTier[3].Total, "by-tier must always include tier 3 (default 0)")
}
