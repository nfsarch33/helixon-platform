package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrTurnNotFound    = errors.New("turn not found")
)

// Role represents a conversation participant.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Turn is a single message in a conversation, including optional tool calls.
type Turn struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"session_id"`
	Role       Role            `json:"role"`
	Content    string          `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	TokensIn   int             `json:"tokens_in"`
	TokensOut  int             `json:"tokens_out"`
	CreatedAt  time.Time       `json:"created_at"`
}

// Session groups a sequence of turns under a single conversation.
type Session struct {
	ID        string            `json:"id"`
	AgentID   string            `json:"agent_id"`
	Meta      map[string]string `json:"meta,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// SessionStore manages sessions and turns backed by SQLite.
type SessionStore struct {
	db *sql.DB
	mu sync.RWMutex
}

// storePragmas are applied to every pooled connection via the DSN.
//
// They are carried in the DSN rather than issued as `PRAGMA ...` statements
// after Open because journal_mode is the only one of the three that is stored
// in the database file; synchronous and foreign_keys are per-CONNECTION, and
// database/sql hands out a pool. Issuing them through db.ExecContext sets them
// on whichever single connection served the statement — measured on this
// package, 7 of 8 pooled connections still had foreign_keys OFF, so the
// ON DELETE CASCADE from turns to sessions was unenforced on most of them.
//
// synchronous=NORMAL is the pairing SQLite documents for WAL: WAL cannot
// corrupt the database at NORMAL, and an application crash is fully safe; the
// exposure is losing the most recent transactions to an OS or power failure.
// That is the right trade for a conversation log, and the cost of the
// alternative is not small — at the default FULL every AppendTurn is an fsync
// barrier, measured here at 220ms mean / 406ms worst idle and 1.4-5.2s per
// write under -race at load average 9, against 1.6ms for a read.
//
// busy_timeout matches internal/notify/notifydb: connections racing to
// initialize a fresh WAL database return SQLITE_BUSY immediately without it.
const storePragmas = "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"

// withStorePragmas appends storePragmas to a caller-supplied DSN, respecting
// any query string it already carries — session DSNs in this repo include
// forms like "file::memory:?cache=shared". A DSN that already sets its own
// _pragma is left alone; the caller has been explicit.
func withStorePragmas(dsn string) string {
	if strings.Contains(dsn, "_pragma=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + storePragmas
}

// NewSessionStore opens (or creates) a SQLite database and initializes the schema.
func NewSessionStore(ctx context.Context, dsn string) (*SessionStore, error) {
	db, err := sql.Open("sqlite", withStorePragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &SessionStore{db: db}, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT PRIMARY KEY,
	agent_id   TEXT NOT NULL,
	meta       TEXT DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS turns (
	id           TEXT PRIMARY KEY,
	session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	role         TEXT NOT NULL,
	content      TEXT NOT NULL DEFAULT '',
	tool_calls   TEXT,
	tool_call_id TEXT DEFAULT '',
	tokens_in    INTEGER DEFAULT 0,
	tokens_out   INTEGER DEFAULT 0,
	created_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_turns_session ON turns(session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent_id);

CREATE VIRTUAL TABLE IF NOT EXISTS turns_fts USING fts5(content, session_id, content=turns, content_rowid=rowid);

CREATE TRIGGER IF NOT EXISTS turns_ai AFTER INSERT ON turns BEGIN
	INSERT INTO turns_fts(rowid, content, session_id)
	VALUES (new.rowid, new.content, new.session_id);
END;
`
	_, err := db.ExecContext(ctx, ddl)
	return err
}

// CreateSession starts a new conversation session.
func (s *SessionStore) CreateSession(ctx context.Context, agentID string, meta map[string]string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	sess := &Session{
		ID:        uuid.New().String(),
		AgentID:   agentID,
		Meta:      meta,
		CreatedAt: now,
		UpdatedAt: now,
	}

	metaJSON, err := json.Marshal(sess.Meta)
	if err != nil {
		return nil, fmt.Errorf("marshal meta: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, agent_id, meta, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		sess.ID, sess.AgentID, string(metaJSON), sess.CreatedAt.Format(time.RFC3339Nano), sess.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	return sess, nil
}

// GetSession retrieves a session by ID.
func (s *SessionStore) GetSession(ctx context.Context, id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, meta, created_at, updated_at FROM sessions WHERE id = ?`, id,
	)

	sess := &Session{}
	var metaStr, createdStr, updatedStr string
	if err := row.Scan(&sess.ID, &sess.AgentID, &metaStr, &createdStr, &updatedStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}
	if err := json.Unmarshal([]byte(metaStr), &sess.Meta); err != nil {
		return nil, fmt.Errorf("unmarshal meta: %w", err)
	}
	sess.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
	sess.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
	return sess, nil
}

// AppendTurn adds a message to a session and returns the created Turn.
func (s *SessionStore) AppendTurn(ctx context.Context, sessionID string, role Role, content string, toolCalls json.RawMessage, toolCallID string, tokensIn, tokensOut int) (*Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	turn := &Turn{
		ID:         uuid.New().String(),
		SessionID:  sessionID,
		Role:       role,
		Content:    content,
		ToolCalls:  toolCalls,
		ToolCallID: toolCallID,
		TokensIn:   tokensIn,
		TokensOut:  tokensOut,
		CreatedAt:  now,
	}

	var tcStr *string
	if len(toolCalls) > 0 {
		s := string(toolCalls)
		tcStr = &s
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO turns (id, session_id, role, content, tool_calls, tool_call_id, tokens_in, tokens_out, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		turn.ID, turn.SessionID, string(turn.Role), turn.Content, tcStr, turn.ToolCallID, turn.TokensIn, turn.TokensOut, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("insert turn: %w", err)
	}

	_, _ = s.db.ExecContext(ctx,
		`UPDATE sessions SET updated_at = ? WHERE id = ?`,
		now.Format(time.RFC3339Nano), sessionID,
	)

	return turn, nil
}

// ListTurns retrieves all turns for a session, ordered chronologically.
func (s *SessionStore) ListTurns(ctx context.Context, sessionID string, limit int) ([]Turn, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 1000
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, role, content, tool_calls, tool_call_id, tokens_in, tokens_out, created_at 
		 FROM turns WHERE session_id = ? ORDER BY created_at ASC LIMIT ?`, sessionID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}
	defer rows.Close()

	var turns []Turn
	for rows.Next() {
		var t Turn
		var tcStr sql.NullString
		var createdStr string
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Role, &t.Content, &tcStr, &t.ToolCallID, &t.TokensIn, &t.TokensOut, &createdStr); err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		if tcStr.Valid {
			t.ToolCalls = json.RawMessage(tcStr.String)
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
		turns = append(turns, t)
	}
	return turns, rows.Err()
}

// SearchTurns performs FTS5 full-text search across turn content.
func (s *SessionStore) SearchTurns(ctx context.Context, query string, limit int) ([]Turn, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT t.id, t.session_id, t.role, t.content, t.tool_calls, t.tool_call_id, t.tokens_in, t.tokens_out, t.created_at
		 FROM turns t
		 JOIN turns_fts f ON t.rowid = f.rowid
		 WHERE turns_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`, query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search turns: %w", err)
	}
	defer rows.Close()

	var turns []Turn
	for rows.Next() {
		var t Turn
		var tcStr sql.NullString
		var createdStr string
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Role, &t.Content, &tcStr, &t.ToolCallID, &t.TokensIn, &t.TokensOut, &createdStr); err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		if tcStr.Valid {
			t.ToolCalls = json.RawMessage(tcStr.String)
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
		turns = append(turns, t)
	}
	return turns, rows.Err()
}

// SessionTokenUsage returns aggregate token counts for a session.
func (s *SessionStore) SessionTokenUsage(ctx context.Context, sessionID string) (tokensIn, tokensOut int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0) FROM turns WHERE session_id = ?`, sessionID,
	)
	err = row.Scan(&tokensIn, &tokensOut)
	return
}

// Close releases the database connection.
func (s *SessionStore) Close() error {
	return s.db.Close()
}
