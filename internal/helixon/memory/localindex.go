package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// localIndexPragmaList is applied to every pooled connection of the local
// FTS5 mirror through the DSN, for the reasons recorded on
// internal/helixon/agent.storePragmaList: journal_mode is the only one stored
// in the database file; synchronous and busy_timeout are per-CONNECTION, and
// database/sql hands out a pool, so a `PRAGMA ...` statement issued after Open
// reaches whichever single connection served it.
//
// The measured cost of leaving SQLite's defaults (journal_mode=DELETE,
// synchronous=FULL) in place: every INSERT into local_memories, plus its FTS5
// trigger, is an fsync barrier. Under the full-suite race run this package's
// SQLite-backed tests slowed 3-8x (FTS-only 0.9s -> 3.1s, green-path mirror
// 1.2s -> 9.5s) while the HTTP-only test stayed at 0.00s, and EnsureSchema
// alone overran a 10s context. WAL + NORMAL takes the fsync off the write
// path; an application crash stays fully safe, and the exposure is the most
// recent transactions on an OS or power failure, which a search mirror of the
// canonical Engram store can always rebuild.
//
// busy_timeout matches the other stores in this repository: connections
// racing to initialise a fresh WAL database return SQLITE_BUSY immediately
// without it.
var localIndexPragmaList = []string{
	"_pragma=journal_mode(WAL)",
	"_pragma=synchronous(NORMAL)",
	"_pragma=busy_timeout(5000)",
}

// withLocalIndexPragmas adds the mirror's pragmas to a caller-supplied DSN,
// respecting any query string it already carries. The merge is PER PRAGMA: a
// caller who tunes one (say a longer busy_timeout) keeps their value and still
// gets the rest, because losing synchronous(NORMAL) silently puts every write
// back behind an fsync barrier.
func withLocalIndexPragmas(dsn string) string {
	add := make([]string, 0, len(localIndexPragmaList))
	for _, p := range localIndexPragmaList {
		// "_pragma=synchronous(NORMAL)" -> key "_pragma=synchronous("
		key := p[:strings.IndexByte(p, '(')+1]
		if !strings.Contains(dsn, key) {
			add = append(add, p)
		}
	}
	if len(add) == 0 {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + strings.Join(add, "&")
}

// MemoryIndexDSN opens the mirror in process memory: no file, no journal,
// gone when the handle closes. It is what an ephemeral agent run wants and
// what the package's own tests use, so the substrate under a temp directory
// cannot decide whether hybrid-search semantics pass.
const MemoryIndexDSN = ":memory:"

// OpenLocalIndex opens the SQLite database that backs the local FTS5 mirror
// with the pragmas every pooled connection needs, and verifies it answers.
// It does not create the schema: pass the handle to NewHybridSearcher and
// call EnsureSchema. This is the only supported way to open the mirror;
// opening it with a bare sql.Open leaves the defaults above in place.
//
// MemoryIndexDSN is pinned to a single pooled connection: every connection
// to ":memory:" is its own empty database, so a pool of them would make the
// schema vanish between statements.
func OpenLocalIndex(ctx context.Context, dsn string) (*sql.DB, error) {
	inMemory := dsn == MemoryIndexDSN
	if !inMemory {
		dsn = withLocalIndexPragmas(dsn)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("memory: open local index: %w", err)
	}
	if inMemory {
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("memory: ping local index: %w", err)
	}
	return db, nil
}
