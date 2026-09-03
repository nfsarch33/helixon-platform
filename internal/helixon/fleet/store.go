package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	// Registers the "sqlite" driver TaskStore opens. Pure Go, so the store
	// builds everywhere the platform does.
	_ "modernc.org/sqlite"
)

// ErrLeaseLost reports that the caller no longer holds a task's lease: the
// sweeper reclaimed it, or another worker claimed it after expiry. A worker
// seeing this must stop touching the task — its result belongs to the new
// owner now.
var ErrLeaseLost = errors.New("fleet: task lease lost")

// taskStorePragmaList mirrors the session store's DSN pragmas
// (internal/helixon/agent), for the reasons recorded there: journal_mode is
// the only one of these stored in the database file; the rest are
// per-CONNECTION, and database/sql pools connections, so a PRAGMA issued
// after Open reaches exactly one of them. WAL+NORMAL is the documented safe
// pairing — an application crash cannot corrupt the database, and the
// exposure is losing the most recent transactions to an OS or power failure,
// which the lease/reclaim protocol already tolerates: a lost claim is
// re-issued at expiry. Folding this and the session store's copy into one
// shared helper is deliberate follow-up work, not smuggled into this change.
var taskStorePragmaList = []string{
	"_pragma=journal_mode(WAL)",
	"_pragma=synchronous(NORMAL)",
	"_pragma=busy_timeout(5000)",
}

// withTaskStorePragmas adds the store's pragmas to a caller-supplied DSN,
// merging PER PRAGMA so a caller who tunes one keeps their value and still
// gets the rest.
func withTaskStorePragmas(dsn string) string {
	add := make([]string, 0, len(taskStorePragmaList))
	for _, p := range taskStorePragmaList {
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

// TaskStore is the durable backing for the fleet handler's task state.
//
// Without it the handler's tasks live in a process-memory map: a restart
// loses every in-flight task and the results of completed ones, so partially
// finished fan-out work is re-run — and re-paid. TaskStore externalizes that
// state (SQLite now; every statement is plain single-row CAS SQL, portable to
// Postgres unchanged).
//
// Ownership model: a task is executed under a LEASE. Claiming is one
// compare-and-swap UPDATE checked by RowsAffected, so exactly one concurrent
// claimer wins with no advisory locks. The lease is renewed while the
// executor runs; a task whose lease expires is requeued by ReclaimExpired
// (crash recovery), and a worker whose lease was lost has its Finish REFUSED,
// which closes the duplicate-execution hole: a reclaimed worker cannot
// overwrite the new owner's result.
//
// Lease deadlines are stored as Unix nanoseconds and compared numerically.
// The other timestamps are RFC3339Nano text for readability; none of them is
// ever used for ordering or expiry — this host's wall clock is known to tie,
// stall and step backwards, and a sub-second text-comparison artifact is not
// a foundation for an expiry decision.
type TaskStore struct {
	db *sql.DB
	mu sync.Mutex

	// now supplies wall-clock stamps and lease deadlines. Overridable so tests
	// can freeze or advance it. Correctness must never depend on two processes
	// agreeing on it more closely than one lease TTL.
	now func() time.Time
}

// NewTaskStore opens (or creates) the SQLite database at dsn and ensures the
// schema exists. The same dsn may be opened by several processes; that is the
// point.
func NewTaskStore(ctx context.Context, dsn string) (*TaskStore, error) {
	db, err := sql.Open("sqlite", withTaskStorePragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := migrateTasks(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate fleet tasks: %w", err)
	}
	return &TaskStore{db: db, now: func() time.Time { return time.Now().UTC() }}, nil
}

func migrateTasks(ctx context.Context, db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS fleet_tasks (
	id               TEXT PRIMARY KEY,
	agent_name       TEXT NOT NULL DEFAULT '',
	prompt           TEXT NOT NULL,
	ticket_id        TEXT NOT NULL DEFAULT '',
	priority         INTEGER NOT NULL DEFAULT 0,
	status           TEXT NOT NULL,
	result           TEXT NOT NULL DEFAULT '',
	error            TEXT NOT NULL DEFAULT '',
	submitted_at     TEXT NOT NULL,
	started_at       TEXT,
	completed_at     TEXT,
	attempts         INTEGER NOT NULL DEFAULT 0,
	timeout_secs     INTEGER NOT NULL DEFAULT 0,
	metadata         TEXT NOT NULL DEFAULT '{}',
	lease_owner      TEXT NOT NULL DEFAULT '',
	lease_expires_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_fleet_tasks_status ON fleet_tasks(status);
CREATE INDEX IF NOT EXISTS idx_fleet_tasks_lease ON fleet_tasks(status, lease_expires_at);
`
	_, err := db.ExecContext(ctx, ddl)
	return err
}

// Close releases the database connection.
func (s *TaskStore) Close() error { return s.db.Close() }

// Insert persists a newly submitted task. The primary key makes duplicate
// submission of the same task ID an error rather than a silent overwrite,
// which is what an idempotency key is supposed to do.
func (s *TaskStore) Insert(ctx context.Context, rec *TaskRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	metaJSON := []byte("{}")
	if rec.Metadata != nil {
		var err error
		metaJSON, err = json.Marshal(rec.Metadata)
		if err != nil {
			return fmt.Errorf("marshal metadata: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO fleet_tasks
		 (id, agent_name, prompt, ticket_id, priority, status, submitted_at, attempts, timeout_secs, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.AgentName, rec.Prompt, rec.TicketID, rec.Priority, string(rec.Status),
		rec.SubmittedAt.Format(time.RFC3339Nano), rec.Attempts, rec.TimeoutSecs, string(metaJSON))
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

// taskColumns is the SELECT list scanTask expects, in scan order.
const taskColumns = `id, agent_name, prompt, ticket_id, priority, status, result, error,
	submitted_at, started_at, completed_at, attempts, timeout_secs, metadata, lease_owner, lease_expires_at`

// scanTask reads one row produced by a taskColumns SELECT.
func scanTask(scan func(...any) error) (TaskRecord, error) {
	var (
		rec                           TaskRecord
		status, metaStr, submittedStr string
		startedStr, completedStr      sql.NullString
		leaseExpires                  sql.NullInt64
	)
	err := scan(&rec.ID, &rec.AgentName, &rec.Prompt, &rec.TicketID, &rec.Priority, &status,
		&rec.Result, &rec.Error, &submittedStr, &startedStr, &completedStr,
		&rec.Attempts, &rec.TimeoutSecs, &metaStr, &rec.LeaseOwner, &leaseExpires)
	if err != nil {
		return TaskRecord{}, err
	}
	rec.Status = TaskStatus(status)
	rec.SubmittedAt, _ = time.Parse(time.RFC3339Nano, submittedStr)
	if startedStr.Valid {
		t, _ := time.Parse(time.RFC3339Nano, startedStr.String)
		rec.StartedAt = &t
	}
	if completedStr.Valid {
		t, _ := time.Parse(time.RFC3339Nano, completedStr.String)
		rec.CompletedAt = &t
	}
	if leaseExpires.Valid {
		t := time.Unix(0, leaseExpires.Int64).UTC()
		rec.LeaseExpiresAt = &t
	}
	if metaStr != "" && metaStr != "{}" && metaStr != "null" {
		if err := json.Unmarshal([]byte(metaStr), &rec.Metadata); err != nil {
			return TaskRecord{}, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}
	return rec, nil
}

// Get returns one task by ID; the second return is false when it is absent.
func (s *TaskStore) Get(ctx context.Context, id string) (TaskRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM fleet_tasks WHERE id = ?`, id)
	rec, err := scanTask(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRecord{}, false, nil
	}
	if err != nil {
		return TaskRecord{}, false, fmt.Errorf("get task: %w", err)
	}
	return rec, true, nil
}

// List returns every task in the store, newest submission first.
func (s *TaskStore) List(ctx context.Context) ([]TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM fleet_tasks ORDER BY rowid DESC`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []TaskRecord
	for rows.Next() {
		rec, err := scanTask(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Claim atomically takes ownership of a pending task for one lease TTL. This
// is the CAS the whole ownership model rests on: one UPDATE guarded on
// status, verified by RowsAffected, so exactly one concurrent claimer wins —
// across goroutines, store handles, or processes.
func (s *TaskStore) Claim(ctx context.Context, id, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx,
		`UPDATE fleet_tasks SET status = ?, lease_owner = ?, lease_expires_at = ?
		 WHERE id = ? AND status = ?`,
		string(TaskStatusClaimed), owner, s.now().Add(ttl).UnixNano(),
		id, string(TaskStatusPending))
	if err != nil {
		return false, fmt.Errorf("claim task: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim task rows: %w", err)
	}
	return n == 1, nil
}

// Renew extends the lease of a task the caller still owns. Returning false
// without error means the lease is lost — the caller must treat that as a
// cancellation signal, not a retryable hiccup.
func (s *TaskStore) Renew(ctx context.Context, id, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx,
		`UPDATE fleet_tasks SET lease_expires_at = ?
		 WHERE id = ? AND lease_owner = ? AND status IN (?, ?)`,
		s.now().Add(ttl).UnixNano(), id, owner,
		string(TaskStatusClaimed), string(TaskStatusRunning))
	if err != nil {
		return false, fmt.Errorf("renew lease: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("renew lease rows: %w", err)
	}
	return n == 1, nil
}

// MarkRunning transitions a claimed task to running, stamping started_at
// exactly once (COALESCE keeps the first start across a reclaim, so Duration
// reflects the task's real wall-clock history, not the last attempt's).
func (s *TaskStore) MarkRunning(ctx context.Context, id, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx,
		`UPDATE fleet_tasks SET status = ?, started_at = COALESCE(started_at, ?)
		 WHERE id = ? AND lease_owner = ? AND status = ?`,
		string(TaskStatusRunning), s.now().Format(time.RFC3339Nano),
		id, owner, string(TaskStatusClaimed))
	if err != nil {
		return false, fmt.Errorf("mark running: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark running rows: %w", err)
	}
	return n == 1, nil
}

// BumpAttempt increments the attempt counter and returns the new value. Each
// executor invocation consumes one attempt and the count survives restarts,
// so a task cannot stretch its retry budget by crashing its worker. Returns
// ErrLeaseLost when the caller no longer owns the task.
func (s *TaskStore) BumpAttempt(ctx context.Context, id, owner string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRowContext(ctx,
		`UPDATE fleet_tasks SET attempts = attempts + 1
		 WHERE id = ? AND lease_owner = ? RETURNING attempts`, id, owner)
	var n int
	if err := row.Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrLeaseLost
		}
		return 0, fmt.Errorf("bump attempt: %w", err)
	}
	return n, nil
}

// Finish writes a terminal status and clears the lease. It returns false when
// the caller no longer owns the task: a worker whose lease was reclaimed must
// not overwrite the new owner's state, and a refused Finish is the caller's
// signal that its side effects may now be duplicated by the new owner.
func (s *TaskStore) Finish(ctx context.Context, id, owner string, status TaskStatus, result, errMsg string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx,
		`UPDATE fleet_tasks SET status = ?, result = ?, error = ?, completed_at = ?,
		        lease_owner = '', lease_expires_at = NULL
		 WHERE id = ? AND lease_owner = ?`,
		string(status), result, errMsg, s.now().Format(time.RFC3339Nano), id, owner)
	if err != nil {
		return false, fmt.Errorf("finish task: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("finish task rows: %w", err)
	}
	return n == 1, nil
}

// ReclaimExpired requeues tasks whose lease has expired and dead-letters the
// ones whose attempt budget is already spent. It is the crash-recovery path:
// nothing else touches a lost task, so the sweeper cadence bounds how long a
// crashed worker's task can sit invisible. A fresh process sweeping the same
// store thereby recovers everything its predecessor held — restart recovery
// and crash recovery are the same mechanism.
func (s *TaskStore) ReclaimExpired(ctx context.Context, maxAttempts int) (requeued, deadlettered []TaskRecord, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("reclaim begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM fleet_tasks
		 WHERE status IN (?, ?) AND lease_expires_at IS NOT NULL AND lease_expires_at < ?`,
		string(TaskStatusClaimed), string(TaskStatusRunning), now.UnixNano())
	if err != nil {
		return nil, nil, fmt.Errorf("reclaim select: %w", err)
	}
	var expired []TaskRecord
	for rows.Next() {
		rec, scanErr := scanTask(rows.Scan)
		if scanErr != nil {
			_ = rows.Close()
			return nil, nil, fmt.Errorf("reclaim scan: %w", scanErr)
		}
		expired = append(expired, rec)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return nil, nil, fmt.Errorf("reclaim rows: %w", rowsErr)
	}
	_ = rows.Close()

	nowStr := now.Format(time.RFC3339Nano)
	for i := range expired {
		rec := &expired[i]
		if rec.Attempts >= maxAttempts {
			msg := fmt.Sprintf("lease expired after %d attempts; attempt budget spent (last owner %q)",
				rec.Attempts, rec.LeaseOwner)
			if _, err := tx.ExecContext(ctx,
				`UPDATE fleet_tasks SET status = ?, error = ?, completed_at = ?,
				        lease_owner = '', lease_expires_at = NULL
				 WHERE id = ?`,
				string(TaskStatusFailed), msg, nowStr, rec.ID); err != nil {
				return nil, nil, fmt.Errorf("reclaim dead-letter: %w", err)
			}
			rec.Status = TaskStatusFailed
			rec.Error = msg
			rec.LeaseOwner = ""
			rec.LeaseExpiresAt = nil
			deadlettered = append(deadlettered, *rec)
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE fleet_tasks SET status = ?, lease_owner = '', lease_expires_at = NULL
			 WHERE id = ?`,
			string(TaskStatusPending), rec.ID); err != nil {
			return nil, nil, fmt.Errorf("reclaim requeue: %w", err)
		}
		rec.Status = TaskStatusPending
		rec.LeaseOwner = ""
		rec.LeaseExpiresAt = nil
		requeued = append(requeued, *rec)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("reclaim commit: %w", err)
	}
	return requeued, deadlettered, nil
}
