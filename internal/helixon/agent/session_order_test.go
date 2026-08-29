package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// fakeClock hands out a caller-controlled wall-clock reading. It exists to make
// the store's ordering testable against clocks that tie, stall or run backwards
// -- all of which the real one does.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time      { return c.t }
func (c *fakeClock) Set(t time.Time)     { c.t = t }
func (c *fakeClock) Add(d time.Duration) { c.t = c.t.Add(d) }

// newClockedStore returns a store whose wall clock the test drives.
func newClockedStore(t *testing.T, start time.Time) (*SessionStore, *fakeClock) {
	t.Helper()
	store := newTestStore(t)
	clock := &fakeClock{t: start}
	store.now = clock.Now
	return store, clock
}

// appendRoles appends one turn per role, letting the caller move the clock
// between appends, and returns the roles in the order they were written.
func appendRoles(t *testing.T, store *SessionStore, sessionID string, roles []Role, tick func(i int)) {
	t.Helper()
	for i, r := range roles {
		if tick != nil {
			tick(i)
		}
		_, err := store.AppendTurn(context.Background(), sessionID, r, string(r), nil, "", 0, 0)
		require.NoError(t, err)
	}
}

func gotRoles(turns []Turn) []Role {
	roles := make([]Role, len(turns))
	// Indexed, not ranged by value: Turn is 152 bytes and gocritic's
	// rangeValCopy is a required check here.
	for i := range turns {
		roles[i] = turns[i].Role
	}
	return roles
}

// TestListTurnsOrderSurvivesClockRewind is the regression test for the real
// defect. The wsl1 wall clock steps backwards ~1.24s every ~31s, so a turn can
// carry an earlier created_at than the turn before it. Ordering on created_at
// -- with or without a tie-break, since these stamps are distinct and genuinely
// inverted -- reorders the conversation and shows the model a tool result
// before the assistant turn that requested it.
func TestListTurnsOrderSurvivesClockRewind(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 29, 4, 6, 15, 135586217, time.UTC)
	store, clock := newClockedStore(t, base)

	sess, err := store.CreateSession(ctx, "clock-rewind", nil)
	require.NoError(t, err)

	want := []Role{RoleUser, RoleAssistant, RoleTool, RoleAssistant}
	// Turn 2 lands just before the step; turn 3 lands just after it, 1.249s in
	// the past -- the exact shape measured on this host.
	appendRoles(t, store, sess.ID, want, func(i int) {
		switch i {
		case 1:
			clock.Add(200 * time.Millisecond)
		case 2:
			clock.Add(-1249 * time.Millisecond)
		case 3:
			clock.Add(200 * time.Millisecond)
		}
	})

	turns, err := store.ListTurns(ctx, sess.ID, 0)
	require.NoError(t, err)
	require.Len(t, turns, 4)

	assert.Equal(t, want, gotRoles(turns),
		"turns must come back in append order even though the clock ran backwards mid-conversation")
	assert.Equal(t, []int64{1, 2, 3, 4}, []int64{turns[0].Seq, turns[1].Seq, turns[2].Seq, turns[3].Seq},
		"seq must be a dense 1-based sequence in append order")

	// Guard the premise: the stamps really are inverted, so this test would pass
	// vacuously if it ever stopped exercising the rewind.
	require.True(t, turns[2].CreatedAt.Before(turns[1].CreatedAt),
		"precondition: turn 3 must carry an earlier stamp than turn 2")
}

// TestListTurnsOrderSurvivesFrozenClock covers the degenerate case: every turn
// written inside one clock tick, so all created_at values are byte-identical
// and SQLite is free to return them in any order it likes.
func TestListTurnsOrderSurvivesFrozenClock(t *testing.T) {
	ctx := context.Background()
	frozen := time.Date(2026, 8, 29, 4, 6, 15, 135586217, time.UTC)
	store, _ := newClockedStore(t, frozen)

	sess, err := store.CreateSession(ctx, "frozen-clock", nil)
	require.NoError(t, err)

	want := []Role{RoleUser, RoleAssistant, RoleTool, RoleAssistant, RoleUser, RoleAssistant}
	appendRoles(t, store, sess.ID, want, nil)

	turns, err := store.ListTurns(ctx, sess.ID, 0)
	require.NoError(t, err)
	require.Len(t, turns, len(want))
	assert.Equal(t, want, gotRoles(turns),
		"turns sharing one created_at must still come back in append order")

	for i := range turns {
		assert.Equal(t, int64(i+1), turns[i].Seq)
		assert.True(t, turns[i].CreatedAt.Equal(frozen), "precondition: all stamps identical")
	}
}

// TestListTurnsOrderSurvivesWholeSecondStamp covers a second way created_at
// ordering breaks. Stamps are stored as RFC3339Nano text, which drops trailing
// zeros, so a turn landing on an exact second is written "…:15Z" while the next
// is "…:15.2Z". Compared as text, '.' sorts before 'Z', putting the later turn
// first.
func TestListTurnsOrderSurvivesWholeSecondStamp(t *testing.T) {
	ctx := context.Background()
	wholeSecond := time.Date(2026, 8, 29, 4, 6, 15, 0, time.UTC)
	store, clock := newClockedStore(t, wholeSecond)

	sess, err := store.CreateSession(ctx, "whole-second", nil)
	require.NoError(t, err)

	want := []Role{RoleUser, RoleAssistant, RoleTool}
	appendRoles(t, store, sess.ID, want, func(i int) {
		if i > 0 {
			clock.Add(200 * time.Millisecond)
		}
	})

	turns, err := store.ListTurns(ctx, sess.ID, 0)
	require.NoError(t, err)
	require.Len(t, turns, 3)
	assert.Equal(t, want, gotRoles(turns),
		"a turn stamped on an exact second must not sort after the sub-second turns that follow it")

	// Guard the premise: the stored text really does sort the wrong way.
	var first, second string
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT created_at FROM turns WHERE session_id = ? AND seq = 1`, sess.ID).Scan(&first))
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT created_at FROM turns WHERE session_id = ? AND seq = 2`, sess.ID).Scan(&second))
	require.Greater(t, first, second,
		"precondition: whole-second stamp %q must sort after sub-second stamp %q as text", first, second)
}

// TestListTurnsSeqBackfilledForLegacyDatabase opens a database written by the
// pre-seq schema and checks the migration recovers conversation order from
// rowid rather than from the created_at stamps, which are already inverted.
func TestListTurnsSeqBackfilledForLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "legacy.db")

	// Build the old schema and populate it directly, bypassing the store.
	legacy, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = legacy.ExecContext(ctx, `
CREATE TABLE sessions (
	id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, meta TEXT DEFAULT '{}',
	created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE turns (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	role TEXT NOT NULL, content TEXT NOT NULL DEFAULT '', tool_calls TEXT,
	tool_call_id TEXT DEFAULT '', tokens_in INTEGER DEFAULT 0,
	tokens_out INTEGER DEFAULT 0, created_at TEXT NOT NULL
);`)
	require.NoError(t, err)

	_, err = legacy.ExecContext(ctx,
		`INSERT INTO sessions (id, agent_id, created_at, updated_at) VALUES ('s1', 'legacy', 'x', 'x')`)
	require.NoError(t, err)

	// Inserted in conversation order, but stamped across a backwards clock step.
	legacyTurns := []struct{ role, createdAt string }{
		{"user", "2026-08-29T04:06:15.135586217Z"},
		{"assistant", "2026-08-29T04:06:15.335586217Z"},
		{"tool", "2026-08-29T04:06:14.086265450Z"}, // clock stepped back here
		{"assistant", "2026-08-29T04:06:14.286265450Z"},
	}
	for i, lt := range legacyTurns {
		_, err = legacy.ExecContext(ctx,
			`INSERT INTO turns (id, session_id, role, content, created_at) VALUES (?, 's1', ?, ?, ?)`,
			[]string{"t1", "t2", "t3", "t4"}[i], lt.role, lt.role, lt.createdAt)
		require.NoError(t, err)
	}
	require.NoError(t, legacy.Close())

	// Reopening through the store runs the migration.
	store, err := NewSessionStore(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	turns, err := store.ListTurns(ctx, "s1", 0)
	require.NoError(t, err)
	require.Len(t, turns, 4)
	assert.Equal(t, []Role{RoleUser, RoleAssistant, RoleTool, RoleAssistant}, gotRoles(turns),
		"legacy rows must be backfilled from rowid, not from their inverted stamps")
	assert.Equal(t, []int64{1, 2, 3, 4}, []int64{turns[0].Seq, turns[1].Seq, turns[2].Seq, turns[3].Seq})

	// A turn appended after the migration must continue the sequence.
	appended, err := store.AppendTurn(ctx, "s1", RoleUser, "after migration", nil, "", 0, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(5), appended.Seq)
}

// TestMigrationIsIdempotent guards the fact that migrate() runs on every store
// open, not just the first. A second run must not fail on the already-present
// column, must not renumber anything, and must leave order intact.
func TestMigrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "idempotent.db")

	store, err := NewSessionStore(ctx, dsn)
	require.NoError(t, err)
	store.now = (&fakeClock{t: time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC)}).Now

	sess, err := store.CreateSession(ctx, "idempotent", nil)
	require.NoError(t, err)
	want := []Role{RoleUser, RoleAssistant, RoleTool}
	appendRoles(t, store, sess.ID, want, nil)
	require.NoError(t, store.Close())

	// Reopen twice more; migrate() re-runs each time.
	for i := range 2 {
		reopened, err := NewSessionStore(ctx, dsn)
		require.NoError(t, err, "reopen %d must not fail on the existing column", i)

		turns, err := reopened.ListTurns(ctx, sess.ID, 0)
		require.NoError(t, err)
		require.Len(t, turns, len(want))
		assert.Equal(t, want, gotRoles(turns), "reopen %d must not disturb order", i)
		for j := range turns {
			assert.Equal(t, int64(j+1), turns[j].Seq, "reopen %d must not renumber seq", i)
		}
		require.NoError(t, reopened.Close())
	}
}

// TestAppendTurnSeqIsPerSession checks the sequence restarts per conversation
// rather than running across the whole table.
func TestAppendTurnSeqIsPerSession(t *testing.T) {
	ctx := context.Background()
	store, _ := newClockedStore(t, time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC))

	s1, err := store.CreateSession(ctx, "a", nil)
	require.NoError(t, err)
	s2, err := store.CreateSession(ctx, "b", nil)
	require.NoError(t, err)

	// Interleave the two conversations.
	for i := 0; i < 3; i++ {
		t1, err := store.AppendTurn(ctx, s1.ID, RoleUser, "s1", nil, "", 0, 0)
		require.NoError(t, err)
		assert.Equal(t, int64(i+1), t1.Seq)

		t2, err := store.AppendTurn(ctx, s2.ID, RoleUser, "s2", nil, "", 0, 0)
		require.NoError(t, err)
		assert.Equal(t, int64(i+1), t2.Seq)
	}
}
