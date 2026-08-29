package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// TestStorePragmasApplyToEveryPooledConnection is the positive control for the
// store configuration.
//
// synchronous and foreign_keys are per-CONNECTION settings and database/sql
// hands out a pool, so a `PRAGMA ...` issued through db.ExecContext after Open
// configures exactly one connection and silently leaves the rest at their
// defaults. That is how this store shipped: foreign_keys was set that way, and
// measured across eight pooled connections only one of them had it on — the
// ON DELETE CASCADE from turns to sessions was unenforced on the other seven.
//
// The test reserves several connections SIMULTANEOUSLY, so the pool is forced
// to open more than one, and checks each. Revert storePragmas to post-Open
// PRAGMA statements and this fails.
func TestStorePragmasApplyToEveryPooledConnection(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	const conns = 6
	held := make([]*sql.Conn, 0, conns)
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	// Reserved together and only released at the end, so these are guaranteed
	// to be distinct connections rather than one connection handed out twice.
	for i := 0; i < conns; i++ {
		c, err := store.db.Conn(ctx)
		require.NoError(t, err)
		held = append(held, c)
	}

	for i, c := range held {
		var sync int
		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync))
		// 1 == NORMAL. At the default 2 (FULL) every turn write costs an fsync
		// barrier, which is what made the agent loop's own deadline race a
		// durable write on a loaded host.
		assert.Equal(t, 1, sync, "conn %d: synchronous must be NORMAL", i)

		var fk int
		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk))
		assert.Equal(t, 1, fk, "conn %d: foreign_keys must be ON", i)

		var jm string
		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&jm))
		assert.Equal(t, "wal", jm, "conn %d: journal_mode must be WAL", i)
	}
}

// TestWithStorePragmasRespectsExistingQuery guards the DSN forms already in
// use in this repo — internal/helixon passes "file::memory:?cache=shared" and
// "file:<name>?mode=memory&cache=shared", which a naive dsn+"?..." would turn
// into a DSN with two query strings.
func TestWithStorePragmasRespectsExistingQuery(t *testing.T) {
	t.Run("bare path gets a query string", func(t *testing.T) {
		got := withStorePragmas("/tmp/agent.db")
		assert.Equal(t, "/tmp/agent.db?"+storePragmas, got)
	})

	t.Run("existing query string is extended", func(t *testing.T) {
		got := withStorePragmas("file::memory:?cache=shared")
		assert.Equal(t, "file::memory:?cache=shared&"+storePragmas, got)
	})

	t.Run("caller's own pragmas are left alone", func(t *testing.T) {
		dsn := "file:x.db?_pragma=synchronous(FULL)"
		assert.Equal(t, dsn, withStorePragmas(dsn))
	})

	// And the shared-cache in-memory form must still open and migrate.
	t.Run("in-memory DSN still works", func(t *testing.T) {
		store, err := NewSessionStore(context.Background(), "file::memory:?cache=shared")
		require.NoError(t, err)
		defer func() { _ = store.Close() }()
		_, err = store.CreateSession(context.Background(), "mem", nil)
		require.NoError(t, err)
	})
}

// TestForeignKeyCascadeIsEnforced is the behavioral half of the pragma guard:
// it asserts the effect the setting is there to produce, on a connection the
// test did not hand-configure.
func TestForeignKeyCascadeIsEnforced(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "fk.db")
	store, err := NewSessionStore(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	sess, err := store.CreateSession(ctx, "fk", nil)
	require.NoError(t, err)
	_, err = store.AppendTurn(ctx, sess.ID, RoleUser, "hi", nil, "", 0, 0)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sess.ID)
	require.NoError(t, err)

	turns, err := store.ListTurns(ctx, sess.ID, 0)
	require.NoError(t, err)
	assert.Empty(t, turns, "deleting a session must cascade to its turns")
}
