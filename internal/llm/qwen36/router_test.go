// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package qwen36

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture returns a deterministic Matrix covering the v14510 cells
// present on real hardware plus a non-ready cell. Tests below
// exercise the tier router's pick precedence using this fixture.
func fixture() *Matrix {
	return &Matrix{
		SchemaVersion: 1,
		Cells: map[string]Cell{
			"C1": {
				ID: "C1", Node: "wsl1", GPUClass: "rtx3090", GPUSlot: "gpu1",
				ModelID: "qwen36-27b-int4", Engine: "vllm", HostPort: 8004,
				MaxModelLen: 65536, MinFreeMib: 4096, Status: "ready",
			},
			"C2": {
				ID: "C2", Node: "wsl1", GPUClass: "rtx3090", GPUSlot: "gpu2",
				ModelID: "qwen36-27b-q4", Engine: "llama.cpp", HostPort: 8005,
				MaxModelLen: 65536, MinFreeMib: 4096, Status: "ready",
			},
			"C7": {
				ID: "C7", Node: "wsl1", GPUClass: "rtx3090-dual", GPUSlot: "gpu0+gpu1",
				ModelID: "qwen36-27b-mtp-q8", Engine: "llama.cpp", HostPort: 8010,
				MaxModelLen: 32768, MinFreeMib: 49152, Status: "ready",
				SpecType: "draft-mtp",
			},
			"C8": {
				ID: "C8", Node: "wsl2", GPUClass: "rtx4070ti-super", GPUSlot: "gpu0",
				ModelID: "qwen36-9b-q4", Engine: "llama.cpp", HostPort: 8007,
				MaxModelLen: 32768, MinFreeMib: 3072, Status: "ready",
			},
			"C99": {
				ID: "C99", Node: "wsl1", GPUClass: "rtx2070", GPUSlot: "gpu0",
				ModelID: "qwen36-4b-q4-blocked", Engine: "llama.cpp", HostPort: 8008,
				MaxModelLen: 8192, MinFreeMib: 1536, Status: "metadata_blocked",
			},
		},
	}
}

func TestRoute_PicksSmallestForTier0(t *testing.T) {
	m := fixture()
	pick, err := Pick(m, Tier0)
	require.NoError(t, err)
	assert.Equal(t, "C8", pick.ID, "tier0 must pick the smallest model (C8 = 9B q4)")
}

func TestRoute_PicksLargeContextForTier1(t *testing.T) {
	m := fixture()
	pick, err := Pick(m, Tier1)
	require.NoError(t, err)
	// C1 and C2 both have 65536 max_model_len; pick the one with
	// the lower host_port for stable ordering (= C1).
	assert.Equal(t, "C1", pick.ID)
}

func TestRoute_PicksMTPCellForTier3(t *testing.T) {
	m := fixture()
	pick, err := Pick(m, Tier3)
	require.NoError(t, err)
	// tier3 prefers cells that can do speculative decoding.
	assert.Equal(t, "C7", pick.ID)
}

func TestRoute_RejectsOutOfRangeTier(t *testing.T) {
	m := fixture()
	_, err := Pick(m, Tier(99))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tier")
}

func TestRoute_RejectsInvalidTierNegative(t *testing.T) {
	m := fixture()
	_, err := Pick(m, Tier(-1))
	require.Error(t, err)
}

func TestRoute_EmptyMatrixReturnsNoReadyCell(t *testing.T) {
	m := &Matrix{SchemaVersion: 1, Cells: map[string]Cell{
		"C99": {ID: "C99", Status: "metadata_blocked"},
	}}
	_, err := Pick(m, Tier0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoReadyCell), "err must wrap ErrNoReadyCell")
}

func TestRoute_AllMetadataBlockedReturnsErrNoReadyCell(t *testing.T) {
	m := &Matrix{SchemaVersion: 1, Cells: map[string]Cell{
		"A": {ID: "A", Status: "metadata_blocked"},
		"B": {ID: "B", Status: "metadata_blocked"},
	}}
	_, err := Pick(m, Tier2)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoReadyCell))
}

func TestRoute_Tier0SingleReadyCellPicksIt(t *testing.T) {
	m := &Matrix{SchemaVersion: 1, Cells: map[string]Cell{
		"C1": {ID: "C1", Status: "ready", MinFreeMib: 8192, MaxModelLen: 4096},
	}}
	pick, err := Pick(m, Tier0)
	require.NoError(t, err)
	assert.Equal(t, "C1", pick.ID)
}

func TestRoute_Tier0LockedSelectsAcrossLargerPool(t *testing.T) {
	m := &Matrix{SchemaVersion: 1, Cells: map[string]Cell{
		"LARGE": {ID: "LARGE", Status: "ready", MinFreeMib: 49152, MaxModelLen: 32768, Engine: "llama.cpp"},
		"MID":   {ID: "MID", Status: "ready", MinFreeMib: 8192, MaxModelLen: 32768, Engine: "llama.cpp"},
		"SMALL": {ID: "SMALL", Status: "ready", MinFreeMib: 2048, MaxModelLen: 16384, Engine: "llama.cpp"},
	}}
	pick, err := Pick(m, Tier0)
	require.NoError(t, err)
	assert.Equal(t, "SMALL", pick.ID, "tier0 = cheap drafting prefers lowest min_free_mib")
}

func TestRoute_Tier3PrefersSpeculative(t *testing.T) {
	m := &Matrix{SchemaVersion: 1, Cells: map[string]Cell{
		"MID":   {ID: "MID", Status: "ready", MinFreeMib: 8192, MaxModelLen: 32768},
		"DRAFT": {ID: "DRAFT", Status: "ready", MinFreeMib: 49152, MaxModelLen: 32768, SpecType: "draft-mtp"},
	}}
	pick, err := Pick(m, Tier3)
	require.NoError(t, err)
	assert.Equal(t, "DRAFT", pick.ID, "tier3 must surface MTP-capable cells for speculative decoding")
}

func TestRoute_Tier3NoSpeculativeFallsBackToLargest(t *testing.T) {
	m := &Matrix{SchemaVersion: 1, Cells: map[string]Cell{
		"MID": {ID: "MID", Status: "ready", MinFreeMib: 8192, MaxModelLen: 16384},
		"BIG": {ID: "BIG", Status: "ready", MinFreeMib: 49152, MaxModelLen: 65536},
	}}
	pick, err := Pick(m, Tier3)
	require.NoError(t, err)
	assert.Equal(t, "BIG", pick.ID, "tier3 fallback prefers largest max_model_len")
}

func TestRoute_Tier2PrefersVLLM(t *testing.T) {
	m := &Matrix{SchemaVersion: 1, Cells: map[string]Cell{
		"GGUF": {ID: "GGUF", Status: "ready", MaxModelLen: 65536, MinFreeMib: 4096, Engine: "llama.cpp"},
		"VLLM": {ID: "VLLM", Status: "ready", MaxModelLen: 65536, MinFreeMib: 4096, Engine: "vllm"},
	}}
	pick, err := Pick(m, Tier2)
	require.NoError(t, err)
	assert.Equal(t, "VLLM", pick.ID, "tier2 prefers vllm for code synthesis throughput")
}

func TestScore_StableForSameInputs(t *testing.T) {
	c := Cell{ID: "C1", Status: "ready", MinFreeMib: 4096, MaxModelLen: 65536}
	a := scoreTier1(c)
	b := scoreTier1(c)
	assert.Equal(t, a, b)
}

func TestScore_Tier0InverselyPrefersMinFreeMib(t *testing.T) {
	big := Cell{ID: "BIG", Status: "ready", MinFreeMib: 49152, MaxModelLen: 32768}
	small := Cell{ID: "SMALL", Status: "ready", MinFreeMib: 2048, MaxModelLen: 32768}
	assert.Greater(t, scoreTier0(small), scoreTier0(big), "smaller min_free_mib wins under tier0")
}
