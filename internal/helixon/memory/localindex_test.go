package memory

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithLocalIndexPragmasMergesWithCallerPragmas - a caller who sets one
// pragma must not silently lose the rest; losing synchronous(NORMAL) puts
// every mirror write back behind an fsync barrier, and nothing announces it.
func TestWithLocalIndexPragmasMergesWithCallerPragmas(t *testing.T) {
	got := withLocalIndexPragmas("/var/lib/helixon/memory.db?_pragma=busy_timeout(10000)")

	assert.Contains(t, got, "_pragma=busy_timeout(10000)", "caller's value must win")
	assert.NotContains(t, got, "_pragma=busy_timeout(5000)", "ours must not be added twice")
	for _, required := range []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
	} {
		assert.Contains(t, got, required, "must survive a caller-supplied pragma")
	}
	assert.Equal(t, 1, strings.Count(got, "?"), "must stay a single query string")

	bare := withLocalIndexPragmas(filepath.Join("some", "dir", "memory.db"))
	assert.Equal(t, 1, strings.Count(bare, "?"))
	assert.Equal(t, 3, strings.Count(bare, "_pragma="), "all three pragmas on a bare path")
}

// TestOpenLocalIndex_EveryPooledConnectionCarriesThePragmas is the pooled-
// pragma trap pinned as an assertion: hold several connections at once and
// read the per-connection settings on each. A `PRAGMA` issued through
// db.Exec after Open would satisfy one of them and leave the others at
// SQLite's defaults.
//
// This is the one test in the package that must touch a file (the pragmas
// only mean something on one), so its budget is sized for a host whose
// write path stalls under load: the same open measured 1.7s idle and 27s
// beside a CI job on this substrate.
func TestOpenLocalIndex_EveryPooledConnectionCarriesThePragmas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db, err := OpenLocalIndex(ctx, filepath.Join(t.TempDir(), "idx.db"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	const pool = 4
	db.SetMaxOpenConns(pool)
	conns := make([]*sql.Conn, 0, pool)
	for i := 0; i < pool; i++ {
		c, err := db.Conn(ctx)
		require.NoError(t, err, "conn %d", i)
		conns = append(conns, c)
	}
	for i, c := range conns {
		var sync, busy int
		var journal string
		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync))
		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal))
		require.NoError(t, c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy))
		assert.Equal(t, 1, sync, "conn %d: synchronous must be NORMAL", i)
		assert.Equal(t, "wal", journal, "conn %d: journal_mode must be WAL", i)
		assert.Equal(t, 5000, busy, "conn %d: busy_timeout must be set", i)
		require.NoError(t, c.Close())
	}
}

// TestEnsureSchema_IdempotentAndIndexable: the schema is created in one
// transaction, can be applied twice, and the mirror is searchable afterwards.
func TestEnsureSchema_IdempotentAndIndexable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := OpenLocalIndex(ctx, MemoryIndexDSN)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	h := NewHybridSearcher(db, nil, HybridSearchConfig{}, nil)
	require.NoError(t, h.EnsureSchema(ctx))
	require.NoError(t, h.EnsureSchema(ctx), "second EnsureSchema must be a no-op")

	require.NoError(t, h.IndexLocal(ctx, "doc-1", "durable local index schema"))
	results, err := h.Search(ctx, "durable", "", "", "")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "doc-1", results[0].ID)
	assert.Equal(t, "fts5", results[0].Source)
}

// TestOpenLocalIndex_MemoryIndexKeepsItsSchema: the in-memory DSN must be
// served by one connection, or the schema created on one connection is
// invisible to the next statement. Several concurrent searches after a
// write is the shape that exposes a pool.
func TestOpenLocalIndex_MemoryIndexKeepsItsSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := OpenLocalIndex(ctx, MemoryIndexDSN)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	h := NewHybridSearcher(db, nil, HybridSearchConfig{}, nil)
	require.NoError(t, h.EnsureSchema(ctx))
	require.NoError(t, h.IndexLocal(ctx, "m-1", "memory resident index"))

	const readers = 8
	errs := make(chan error, readers)
	for i := 0; i < readers; i++ {
		go func() {
			results, err := h.Search(ctx, "resident", "", "", "")
			if err == nil && len(results) != 1 {
				err = fmt.Errorf("want 1 result, got %d", len(results))
			}
			errs <- err
		}()
	}
	for i := 0; i < readers; i++ {
		require.NoError(t, <-errs)
	}
}

// TestOpenLocalIndex_RejectsUnopenablePath: a directory that cannot be
// created surfaces at open time, not on the first write.
func TestOpenLocalIndex_RejectsUnopenablePath(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "missing", "deeper", "idx.db")
	db, err := OpenLocalIndex(ctx, dsn)
	if err == nil {
		_ = db.Close()
		t.Fatal("expected an error opening a database in a directory that does not exist")
	}
	assert.Contains(t, err.Error(), "local index")
}
