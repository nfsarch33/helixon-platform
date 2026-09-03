package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Durable runs.
//
// A run is one invocation of the agent loop on a session: the user message it
// started with, who is executing it, and how it ended. A step is one tool
// call inside a run. Both live in the same database as the turns because they
// describe the same conversation, and they exist so that a process killed
// mid-run can be resumed from the last completed step by any worker rather
// than re-running the whole conversation (re-paying every model call and
// re-executing every side effect) or leaving it claimed forever.
//
// The lease pattern is the fleet TaskStore's: a CAS claim decided by
// RowsAffected, a TTL lease stored as Unix nanoseconds, a renewal that
// doubles as the executor's stop signal, and an owner-guarded finish so a
// reclaimed worker's late result cannot overwrite the new owner's.

var (
	ErrRunNotFound  = errors.New("run not found")
	ErrStepNotFound = errors.New("run step not found")
)

// RunStatus is a run's lifecycle state.
type RunStatus string

const (
	RunRunning    RunStatus = "running"
	RunCompleted  RunStatus = "completed"
	RunFailed     RunStatus = "failed"
	RunNeedsHuman RunStatus = "needs_human"
)

// StepStatus is a tool call's lifecycle state inside a run.
type StepStatus string

const (
	// StepPending means the tool was dispatched and its outcome is unknown: a
	// crash between dispatch and FinishStep leaves a step here.
	StepPending StepStatus = "pending"
	StepDone    StepStatus = "done"
	StepFailed  StepStatus = "failed"
)

// RunRecord is the durable state of one run.
type RunRecord struct {
	ID           string            `json:"id"`
	SessionID    string            `json:"session_id"`
	UserMessage  string            `json:"user_message"`
	Status       RunStatus         `json:"status"`
	Owner        string            `json:"owner,omitempty"`
	LeaseUntil   time.Time         `json:"lease_until"`
	Attempts     int               `json:"attempts"`
	Iterations   int               `json:"iterations"`
	TokensIn     int               `json:"tokens_in"`
	TokensOut    int               `json:"tokens_out"`
	FinalContent string            `json:"final_content,omitempty"`
	Err          string            `json:"err,omitempty"`
	Meta         map[string]string `json:"meta,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// RunStep is one tool call inside a run.
type RunStep struct {
	RunID string `json:"run_id"`
	Seq   int64  `json:"seq"`
	// Iteration is the loop iteration whose assistant turn requested the call.
	// Tool call ids are only unique within one model response for some
	// providers ("call_0" every turn), so the step key is (run, iteration, id).
	Iteration  int        `json:"iteration"`
	ToolCallID string     `json:"tool_call_id"`
	Tool       string     `json:"tool"`
	Args       string     `json:"args"`
	Status     StepStatus `json:"status"`
	Result     string     `json:"result,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at"`
}

const runsDDL = `
CREATE TABLE IF NOT EXISTS runs (
	id            TEXT PRIMARY KEY,
	session_id    TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	user_message  TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT 'running',
	owner         TEXT NOT NULL DEFAULT '',
	lease_until   INTEGER NOT NULL DEFAULT 0,
	attempts      INTEGER NOT NULL DEFAULT 0,
	iterations    INTEGER NOT NULL DEFAULT 0,
	tokens_in     INTEGER NOT NULL DEFAULT 0,
	tokens_out    INTEGER NOT NULL DEFAULT 0,
	final_content TEXT NOT NULL DEFAULT '',
	err           TEXT NOT NULL DEFAULT '',
	meta          TEXT NOT NULL DEFAULT '{}',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_status_lease ON runs(status, lease_until);
CREATE INDEX IF NOT EXISTS idx_runs_session ON runs(session_id);

CREATE TABLE IF NOT EXISTS run_steps (
	run_id       TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
	seq          INTEGER NOT NULL,
	iteration    INTEGER NOT NULL DEFAULT 0,
	tool_call_id TEXT NOT NULL,
	tool         TEXT NOT NULL,
	args         TEXT NOT NULL DEFAULT '',
	status       TEXT NOT NULL DEFAULT 'pending',
	result       TEXT NOT NULL DEFAULT '',
	started_at   TEXT NOT NULL,
	finished_at  TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (run_id, iteration, tool_call_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_run_steps_seq ON run_steps(run_id, seq);
`

// migrateRuns creates the run tables. Both statements are IF NOT EXISTS, so a
// database from before durable runs gains them on open and a later open is a
// no-op; there is nothing to backfill because a run that predates the tables
// was never resumable.
func migrateRuns(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, runsDDL); err != nil {
		return fmt.Errorf("run tables: %w", err)
	}
	return nil
}

// StartRun records a run and its user turn in ONE transaction and returns the
// run. It is idempotent on id: a second call finds the existing run (created
// = false) and appends nothing, so a caller that retried after a lost reply
// pays for one conversation.
func (s *SessionStore) StartRun(ctx context.Context, id, sessionID, userMessage string, meta map[string]string) (*RunRecord, bool, error) {
	return s.startRun(ctx, id, sessionID, userMessage, meta, "", 0)
}

// StartRunClaimed is StartRun with the row born claimed by owner for ttl
// (attempts = 1), so there is no instant in which the run exists unclaimed
// and a recovery sweep could take it from the caller that created it. On an
// existing run nothing is claimed; the caller claims through ClaimRun.
func (s *SessionStore) StartRunClaimed(ctx context.Context, id, sessionID, userMessage string, meta map[string]string, owner string, ttl time.Duration) (*RunRecord, bool, error) {
	if owner == "" {
		return nil, false, errors.New("start run claimed: owner is required")
	}
	return s.startRun(ctx, id, sessionID, userMessage, meta, owner, ttl)
}

func (s *SessionStore) startRun(ctx context.Context, id, sessionID, userMessage string, meta map[string]string, owner string, ttl time.Duration) (*RunRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if meta == nil {
		meta = map[string]string{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, false, fmt.Errorf("marshal run meta: %w", err)
	}
	now := s.now()
	stamp := now.Format(time.RFC3339Nano)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("start run: begin: %w", err)
	}
	var leaseUntil int64
	attempts := 0
	if owner != "" {
		leaseUntil = now.Add(ttl).UnixNano()
		attempts = 1
	}
	res, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO runs (id, session_id, user_message, status, owner, lease_until, attempts, meta, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, sessionID, userMessage, string(RunRunning), owner, leaseUntil, attempts, string(metaJSON), stamp, stamp)
	if err != nil {
		_ = tx.Rollback()
		return nil, false, fmt.Errorf("start run: insert: %w", err)
	}
	n, _ := res.RowsAffected()
	created := n == 1
	if created {
		if _, err := appendTurnTx(ctx, tx, sessionID, RoleUser, userMessage, nil, "", 0, 0, now); err != nil {
			_ = tx.Rollback()
			return nil, false, fmt.Errorf("start run: user turn: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("start run: commit: %w", err)
	}
	run, err := s.getRun(ctx, s.db, id)
	if err != nil {
		return nil, false, err
	}
	return run, created, nil
}

// GetRun returns a run by id.
func (s *SessionStore) GetRun(ctx context.Context, id string) (*RunRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getRun(ctx, s.db, id)
}

type rowScanner interface {
	Scan(dest ...any) error
}

const runColumns = `id, session_id, user_message, status, owner, lease_until, attempts, iterations, tokens_in, tokens_out, final_content, err, meta, created_at, updated_at`

func (s *SessionStore) getRun(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (*RunRecord, error) {
	run, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	return run, err
}

func scanRun(row rowScanner) (*RunRecord, error) {
	var r RunRecord
	var status, metaStr, createdStr, updatedStr string
	var leaseNanos int64
	if err := row.Scan(&r.ID, &r.SessionID, &r.UserMessage, &status, &r.Owner, &leaseNanos, &r.Attempts,
		&r.Iterations, &r.TokensIn, &r.TokensOut, &r.FinalContent, &r.Err, &metaStr, &createdStr, &updatedStr); err != nil {
		return nil, err
	}
	r.Status = RunStatus(status)
	if leaseNanos > 0 {
		r.LeaseUntil = time.Unix(0, leaseNanos).UTC()
	}
	if err := json.Unmarshal([]byte(metaStr), &r.Meta); err != nil {
		r.Meta = map[string]string{}
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
	return &r, nil
}

// ClaimRun takes the lease on a running run for owner: it succeeds when the
// run is running and either unowned, owned by the same owner, or its lease
// has expired. Exactly one of any set of concurrent claimants succeeds; the
// decision is the single UPDATE's RowsAffected. Every successful claim counts
// an attempt, so a retry budget survives the crash that made the retry
// necessary.
func (s *SessionStore) ClaimRun(ctx context.Context, id, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET owner = ?, lease_until = ?, attempts = attempts + 1, updated_at = ?
		 WHERE id = ? AND status = ? AND (owner = '' OR owner = ? OR lease_until < ?)`,
		owner, now.Add(ttl).UnixNano(), now.Format(time.RFC3339Nano),
		id, string(RunRunning), owner, now.UnixNano())
	if err != nil {
		return false, fmt.Errorf("claim run: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RenewRun extends the lease for its current owner. A false return means
// the owner no longer holds the run (reclaimed after expiry, or finished)
// and must stop executing it.
func (s *SessionStore) RenewRun(ctx context.Context, id, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET lease_until = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND owner = ? AND lease_until >= ?`,
		now.Add(ttl).UnixNano(), now.Format(time.RFC3339Nano),
		id, string(RunRunning), owner, now.UnixNano())
	if err != nil {
		return false, fmt.Errorf("renew run: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// FinishRun records a terminal status and the result. It is owner-guarded:
// a worker whose lease was reclaimed cannot overwrite the new owner's run.
// A false return means the write was refused.
func (s *SessionStore) FinishRun(ctx context.Context, id, owner string, status RunStatus, result *RunResult, runErr error) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status == RunRunning {
		return false, fmt.Errorf("finish run: %q is not a terminal status", status)
	}
	errStr := ""
	if runErr != nil {
		errStr = runErr.Error()
	}
	var iterations, in, out int
	final := ""
	if result != nil {
		iterations, in, out, final = result.Iterations, result.TokensIn, result.TokensOut, result.FinalContent
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, iterations = ?, tokens_in = ?, tokens_out = ?, final_content = ?, err = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND owner = ?`,
		string(status), iterations, in, out, final, errStr, now.Format(time.RFC3339Nano),
		id, string(RunRunning), owner)
	if err != nil {
		return false, fmt.Errorf("finish run: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ListInterruptedRuns returns running runs whose lease has lapsed, including
// runs that were never claimed (a crash between StartRun and ClaimRun). This
// is the resumable set a worker reclaims at start-up.
func (s *SessionStore) ListInterruptedRuns(ctx context.Context) ([]RunRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runColumns+` FROM runs WHERE status = ? AND lease_until < ? ORDER BY created_at ASC, rowid ASC`,
		string(RunRunning), s.now().UnixNano())
	if err != nil {
		return nil, fmt.Errorf("list interrupted runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []RunRecord
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// BeginStep records that a tool call is about to be dispatched. It is
// idempotent on (run, iteration, tool_call_id): a step that already exists is
// returned with created = false, which is how a resumed run learns a tool
// call was already attempted. seq is dense per run and assigned in the INSERT.
func (s *SessionStore) BeginStep(ctx context.Context, runID string, iteration int, toolCallID, tool, args string) (*RunStep, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO run_steps (run_id, seq, iteration, tool_call_id, tool, args, status, started_at)
		 VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM run_steps WHERE run_id = ?), ?, ?, ?, ?, ?, ?)`,
		runID, runID, iteration, toolCallID, tool, args, string(StepPending), now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, false, fmt.Errorf("begin step: %w", err)
	}
	n, _ := res.RowsAffected()
	step, err := s.getStep(ctx, runID, iteration, toolCallID)
	if err != nil {
		return nil, false, err
	}
	return step, n == 1, nil
}

// FinishStep records a step's outcome.
func (s *SessionStore) FinishStep(ctx context.Context, runID string, iteration int, toolCallID string, status StepStatus, result string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		`UPDATE run_steps SET status = ?, result = ?, finished_at = ? WHERE run_id = ? AND iteration = ? AND tool_call_id = ?`,
		string(status), result, s.now().Format(time.RFC3339Nano), runID, iteration, toolCallID)
	if err != nil {
		return fmt.Errorf("finish step: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrStepNotFound
	}
	return nil
}

// ListSteps returns a run's steps in seq order.
func (s *SessionStore) ListSteps(ctx context.Context, runID string) ([]RunStep, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, seq, iteration, tool_call_id, tool, args, status, result, started_at, finished_at
		 FROM run_steps WHERE run_id = ? ORDER BY seq ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []RunStep
	for rows.Next() {
		st, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	return out, rows.Err()
}

func (s *SessionStore) getStep(ctx context.Context, runID string, iteration int, toolCallID string) (*RunStep, error) {
	st, err := scanStep(s.db.QueryRowContext(ctx,
		`SELECT run_id, seq, iteration, tool_call_id, tool, args, status, result, started_at, finished_at
		 FROM run_steps WHERE run_id = ? AND iteration = ? AND tool_call_id = ?`, runID, iteration, toolCallID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrStepNotFound
	}
	return st, err
}

func scanStep(row rowScanner) (*RunStep, error) {
	var st RunStep
	var status, startedStr, finishedStr string
	if err := row.Scan(&st.RunID, &st.Seq, &st.Iteration, &st.ToolCallID, &st.Tool, &st.Args, &status, &st.Result, &startedStr, &finishedStr); err != nil {
		return nil, err
	}
	st.Status = StepStatus(status)
	st.StartedAt, _ = time.Parse(time.RFC3339Nano, startedStr)
	if finishedStr != "" {
		st.FinishedAt, _ = time.Parse(time.RFC3339Nano, finishedStr)
	}
	return &st, nil
}

// ToolTurnExists reports whether the session holds a tool turn for the
// given tool call id AFTER the assistant turn at afterSeq - the durable proof
// that this iteration's dispatch produced an outcome. The seq bound matters:
// some providers reuse tool call ids on every response.
func (s *SessionStore) ToolTurnExists(ctx context.Context, sessionID string, afterSeq int64, toolCallID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM turns WHERE session_id = ? AND role = ? AND tool_call_id = ? AND seq > ?`,
		sessionID, string(RoleTool), toolCallID, afterSeq).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("tool turn lookup: %w", err)
	}
	return n > 0, nil
}

// DeadLetterExhausted fails every interrupted run (running, lease lapsed)
// that has already been claimed maxAttempts times or more, and returns how
// many it failed. The recovery sweep calls it before resuming the rest, so a
// run that dies on every attempt stops being retried instead of looping.
func (s *SessionStore) DeadLetterExhausted(ctx context.Context, maxAttempts int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, err = ?, updated_at = ?
		 WHERE status = ? AND lease_until < ? AND attempts >= ?`,
		string(RunFailed), fmt.Sprintf("attempts exhausted (%d)", maxAttempts), now.Format(time.RFC3339Nano),
		string(RunRunning), now.UnixNano(), maxAttempts)
	if err != nil {
		return 0, fmt.Errorf("dead-letter exhausted runs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// AppendRunTurn is AppendTurn under the run's lease: the turn is written only
// while owner still holds the running run, in the same transaction as that
// check, so a worker whose lease was reclaimed (a zombie blocked past its
// TTL, not yet told by its renewal tick) cannot write into a conversation
// another worker now owns. A refused write is ErrLeaseLost.
func (s *SessionStore) AppendRunTurn(ctx context.Context, runID, owner, sessionID string, role Role, content string, toolCalls json.RawMessage, toolCallID string, tokensIn, tokensOut int) (*Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("append run turn: begin: %w", err)
	}
	var held int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE id = ? AND owner = ? AND status = ?`,
		runID, owner, string(RunRunning)).Scan(&held); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("append run turn: lease check: %w", err)
	}
	if held == 0 {
		_ = tx.Rollback()
		return nil, ErrLeaseLost
	}
	turn, err := appendTurnTx(ctx, tx, sessionID, role, content, toolCalls, toolCallID, tokensIn, tokensOut, now)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("append run turn: commit: %w", err)
	}
	return turn, nil
}

// RunFilter narrows ListRuns. A zero filter lists every run, newest first.
type RunFilter struct {
	// Status keeps only runs in this state ("" = any).
	Status RunStatus
	// Limit caps the result (<= 0 = 100).
	Limit int
}

// ListRuns returns runs newest first (created_at, then rowid, so two runs
// created in the same clock tick keep insertion order).
func (s *SessionStore) ListRuns(ctx context.Context, f RunFilter) ([]RunRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + runColumns + ` FROM runs`
	args := []any{}
	if f.Status != "" {
		q += ` WHERE status = ?`
		args = append(args, string(f.Status))
	}
	q += ` ORDER BY created_at DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []RunRecord
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// RunUsage is what the runs since a point in time cost: counts by status
// and the provider-reported tokens they consumed.
type RunUsage struct {
	Since      time.Time `json:"since"`
	Runs       int       `json:"runs"`
	Running    int       `json:"running"`
	Completed  int       `json:"completed"`
	Failed     int       `json:"failed"`
	NeedsHuman int       `json:"needs_human"`
	TokensIn   int       `json:"tokens_in"`
	TokensOut  int       `json:"tokens_out"`
}

// RunUsage sums the runs created at or after since. A zero since counts
// every run. Tokens are the counts FinishRun recorded, so a run still in
// flight contributes zero until it ends.
func (s *SessionStore) RunUsage(ctx context.Context, since time.Time) (RunUsage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := RunUsage{Since: since.UTC()}
	rows, err := s.db.QueryContext(ctx,
		`SELECT status, COUNT(*), COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0) FROM runs WHERE created_at >= ? GROUP BY status`,
		since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return u, fmt.Errorf("run usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var status string
		var n, in, out int
		if err := rows.Scan(&status, &n, &in, &out); err != nil {
			return u, fmt.Errorf("run usage: scan: %w", err)
		}
		u.Runs += n
		u.TokensIn += in
		u.TokensOut += out
		switch RunStatus(status) {
		case RunRunning:
			u.Running += n
		case RunCompleted:
			u.Completed += n
		case RunFailed:
			u.Failed += n
		case RunNeedsHuman:
			u.NeedsHuman += n
		}
	}
	return u, rows.Err()
}

// appendTurnTx is AppendTurn's body against an explicit transaction, so a run
// row and its user turn can be committed together. The caller holds s.mu.
func appendTurnTx(ctx context.Context, tx *sql.Tx, sessionID string, role Role, content string, toolCalls json.RawMessage, toolCallID string, tokensIn, tokensOut int, now time.Time) (*Turn, error) {
	turn := &Turn{
		ID: uuid.New().String(), SessionID: sessionID, Role: role, Content: content,
		ToolCalls: toolCalls, ToolCallID: toolCallID, TokensIn: tokensIn, TokensOut: tokensOut, CreatedAt: now,
	}
	var tcStr *string
	if len(toolCalls) > 0 {
		v := string(toolCalls)
		tcStr = &v
	}
	err := tx.QueryRowContext(ctx,
		`INSERT INTO turns (id, session_id, role, content, tool_calls, tool_call_id, tokens_in, tokens_out, created_at, seq)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM turns WHERE session_id = ?))
		 RETURNING seq`,
		turn.ID, turn.SessionID, string(turn.Role), turn.Content, tcStr, turn.ToolCallID, turn.TokensIn, turn.TokensOut,
		now.Format(time.RFC3339Nano), sessionID,
	).Scan(&turn.Seq)
	if err != nil {
		return nil, fmt.Errorf("insert turn: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), sessionID); err != nil {
		return nil, fmt.Errorf("touch session: %w", err)
	}
	return turn, nil
}
