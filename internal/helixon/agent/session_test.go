package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *SessionStore {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	store, err := NewSessionStore(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestCreateAndGetSession(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	meta := map[string]string{"model": "qwen3:4b", "workspace": "/tmp/test"}
	sess, err := store.CreateSession(ctx, "helixon-study", meta)
	require.NoError(t, err)
	assert.NotEmpty(t, sess.ID)
	assert.Equal(t, "helixon-study", sess.AgentID)
	assert.Equal(t, meta, sess.Meta)

	got, err := store.GetSession(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, sess.ID, got.ID)
	assert.Equal(t, sess.AgentID, got.AgentID)
	assert.Equal(t, "qwen3:4b", got.Meta["model"])
}

func TestGetSessionNotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetSession(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestAppendAndListTurns(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess, err := store.CreateSession(ctx, "test-agent", nil)
	require.NoError(t, err)

	turn1, err := store.AppendTurn(ctx, sess.ID, RoleUser, "hello world", nil, "", 10, 0)
	require.NoError(t, err)
	assert.Equal(t, RoleUser, turn1.Role)
	assert.Equal(t, "hello world", turn1.Content)

	tc := json.RawMessage(`[{"id":"tc1","type":"function","function":{"name":"search","arguments":"{}"}}]`)
	turn2, err := store.AppendTurn(ctx, sess.ID, RoleAssistant, "", tc, "", 10, 20)
	require.NoError(t, err)
	assert.Equal(t, RoleAssistant, turn2.Role)

	turn3, err := store.AppendTurn(ctx, sess.ID, RoleTool, "search result", nil, "tc1", 0, 0)
	require.NoError(t, err)
	assert.Equal(t, "tc1", turn3.ToolCallID)

	turns, err := store.ListTurns(ctx, sess.ID, 0)
	require.NoError(t, err)
	assert.Len(t, turns, 3)
	assert.Equal(t, RoleUser, turns[0].Role)
	assert.Equal(t, RoleAssistant, turns[1].Role)
	assert.Equal(t, RoleTool, turns[2].Role)
}

func TestSessionTokenUsage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess, err := store.CreateSession(ctx, "test-agent", nil)
	require.NoError(t, err)

	_, err = store.AppendTurn(ctx, sess.ID, RoleUser, "msg1", nil, "", 100, 0)
	require.NoError(t, err)
	_, err = store.AppendTurn(ctx, sess.ID, RoleAssistant, "reply1", nil, "", 100, 200)
	require.NoError(t, err)
	_, err = store.AppendTurn(ctx, sess.ID, RoleUser, "msg2", nil, "", 150, 0)
	require.NoError(t, err)
	_, err = store.AppendTurn(ctx, sess.ID, RoleAssistant, "reply2", nil, "", 250, 300)
	require.NoError(t, err)

	tokIn, tokOut, err := store.SessionTokenUsage(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, 600, tokIn)
	assert.Equal(t, 500, tokOut)
}

func TestSearchTurns(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	sess, err := store.CreateSession(ctx, "test-agent", nil)
	require.NoError(t, err)

	_, err = store.AppendTurn(ctx, sess.ID, RoleUser, "tell me about kubernetes deployments", nil, "", 0, 0)
	require.NoError(t, err)
	_, err = store.AppendTurn(ctx, sess.ID, RoleAssistant, "kubernetes uses pods and services", nil, "", 0, 0)
	require.NoError(t, err)
	_, err = store.AppendTurn(ctx, sess.ID, RoleUser, "what about docker containers?", nil, "", 0, 0)
	require.NoError(t, err)

	results, err := store.SearchTurns(ctx, "kubernetes", 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)
	found := false
	for _, r := range results {
		if r.Content == "tell me about kubernetes deployments" || r.Content == "kubernetes uses pods and services" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected kubernetes-related turn in search results")
}

func TestMultipleSessions(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	s1, err := store.CreateSession(ctx, "agent-1", nil)
	require.NoError(t, err)
	s2, err := store.CreateSession(ctx, "agent-2", nil)
	require.NoError(t, err)

	_, _ = store.AppendTurn(ctx, s1.ID, RoleUser, "session1 msg", nil, "", 0, 0)
	_, _ = store.AppendTurn(ctx, s2.ID, RoleUser, "session2 msg", nil, "", 0, 0)

	turns1, err := store.ListTurns(ctx, s1.ID, 0)
	require.NoError(t, err)
	assert.Len(t, turns1, 1)
	assert.Equal(t, "session1 msg", turns1[0].Content)

	turns2, err := store.ListTurns(ctx, s2.ID, 0)
	require.NoError(t, err)
	assert.Len(t, turns2, 1)
	assert.Equal(t, "session2 msg", turns2[0].Content)
}
