// runx-public-repo-gate: allow-file fleet_host_alias
//
// v18846-2: `helixon runs export` emits one NDJSON row per iteration for
// runs in the durable session store, joining runs x turns so each row
// carries the full eval-workstream tuple: run_id, ticket_id, iteration,
// messages, assistant_content, tool_calls, usage, verifier_passed,
// mutated, status, escalation_reason.
//
// This is TELEMETRY (ADR-090 decision 2): it reads what the runtime
// already stores and adds no capture path anywhere. Scope per the Q1a
// operator decision: --ticket restricts the export to ticket runs (the
// channel=ticket meta stamp runTicketWork sets); with no --ticket every
// run exports, because the columns are per-run facts, not ticket facts.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

// defaultExportDB matches where serve/repl create the session store when
// no DSN is configured, so `runs export` works against the working copy
// by default.
const defaultExportDB = "helixon-sessions.db"

// exportFilter narrows the export. Zero value exports everything.
type exportFilter struct {
	// Ticket restricts the export to runs whose meta carries this
	// ticket_id (ticket-runs-only scope, Q1a).
	Ticket string
	// Since excludes runs created before this instant.
	Since time.Time
}

// exportRuns streams one row per assistant iteration of every matching
// run, in (run, iteration) order, to w as NDJSON. A nil verifier_passed
// marshals as JSON null — the must-be-absent control: no verifier run
// reads null, never false.
func exportRuns(ctx context.Context, db *sql.DB, filter exportFilter) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, session_id, user_message, status, err, meta, created_at,
		       verifier_passed, mutated, verifier_failures
		FROM runs
		ORDER BY created_at ASC, rowid ASC`)
	if err != nil {
		return nil, fmt.Errorf("runs export: query runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type runRow struct {
		id, session, userMsg, status, errStr, createdAt string
		meta                                            map[string]string
		verifierPassed, mutated, verifierFailures       sql.NullInt64
	}

	var out []map[string]any
	for rows.Next() {
		var r runRow
		var metaJSON string
		if err := rows.Scan(&r.id, &r.session, &r.userMsg, &r.status, &r.errStr, &metaJSON, &r.createdAt,
			&r.verifierPassed, &r.mutated, &r.verifierFailures); err != nil {
			return nil, fmt.Errorf("runs export: scan run: %w", err)
		}
		_ = json.Unmarshal([]byte(metaJSON), &r.meta) // empty/odd meta exports as no ticket
		ticketID := r.meta["ticket_id"]
		if filter.Ticket != "" && ticketID != filter.Ticket {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, r.createdAt)
		if err != nil || !filter.Since.IsZero() && created.Before(filter.Since) {
			if err != nil {
				continue // unparseable stamp: exclude rather than guess
			}
			continue
		}

		iters, err := exportRunTurns(ctx, db, r.session)
		if err != nil {
			return nil, err
		}
		escalation := ""
		if r.status == "needs_human" || r.status == "failed" {
			escalation = r.errStr
		}
		for _, it := range iters {
			out = append(out, map[string]any{
				"run_id":            r.id,
				"ticket_id":         ticketID,
				"iteration":         it.ordinal,
				"messages":          r.userMsg,
				"assistant_content": it.content,
				"tool_calls":        it.toolCalls,
				"usage":             map[string]int64{"tokens_in": it.tokensIn, "tokens_out": it.tokensOut},
				"verifier_passed":   nullIntToBool(r.verifierPassed),
				"mutated":           nullIntToBool(r.mutated),
				"verifier_failures": nullIntToAny(r.verifierFailures),
				"status":            r.status,
				"escalation_reason": escalation,
				"created_at":        r.createdAt,
			})
		}
	}
	return out, rows.Err()
}

// exportedIteration is one assistant turn of a run's session.
type exportedIteration struct {
	ordinal   int
	content   string
	toolCalls any
	tokensIn  int64
	tokensOut int64
}

// exportRunTurns returns the run's assistant turns in conversation order,
// numbered from 1. The run's user turn seeds the conversation; every
// assistant reply is one iteration of the loop.
func exportRunTurns(ctx context.Context, db *sql.DB, sessionID string) ([]exportedIteration, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT role, content, tool_calls, tokens_in, tokens_out FROM turns
		WHERE session_id = ? ORDER BY seq ASC, created_at ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("runs export: query turns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []exportedIteration
	for rows.Next() {
		var role, content string
		var toolCalls sql.NullString
		var in, outToks int64
		if err := rows.Scan(&role, &content, &toolCalls, &in, &outToks); err != nil {
			return nil, fmt.Errorf("runs export: scan turn: %w", err)
		}
		if role != "assistant" {
			continue
		}
		var calls any
		if toolCalls.Valid && toolCalls.String != "" {
			_ = json.Unmarshal([]byte(toolCalls.String), &calls) // NULL/empty stays nil
		}
		out = append(out, exportedIteration{
			ordinal:   len(out) + 1,
			content:   content,
			toolCalls: calls,
			tokensIn:  in,
			tokensOut: outToks,
		})
	}
	return out, rows.Err()
}

// nullIntToBool converts a NULL-able 0/1 column into a JSON boolean,
// preserving NULL as JSON null (the must-be-absent control).
func nullIntToBool(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64 != 0
}

// nullIntToAny converts a NULL-able column into a JSON-friendly value:
// nil (null) when the column was NULL, int64 otherwise.
func nullIntToAny(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// newRunsExportCmd builds `helixon runs export`.
func newRunsExportCmd() *cobra.Command {
	var dbPath, since, ticket, format string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export durable runs as NDJSON (one row per iteration)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "ndjson" {
				return fmt.Errorf("unsupported --format %q (only ndjson)", format)
			}
			var sinceT time.Time
			if since != "" {
				t, err := time.Parse(time.RFC3339, since)
				if err != nil {
					return fmt.Errorf("invalid --since %q: %w", since, err)
				}
				sinceT = t
			}
			db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
			if err != nil {
				return fmt.Errorf("open %s: %w", dbPath, err)
			}
			defer func() { _ = db.Close() }()
			rows, err := exportRuns(cmd.Context(), db, exportFilter{Ticket: ticket, Since: sinceT})
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, r := range rows {
				raw, err := json.Marshal(r)
				if err != nil {
					return fmt.Errorf("marshal row: %w", err)
				}
				if _, err := fmt.Fprintln(w, string(raw)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", defaultExportDB, "path to the session store database")
	cmd.Flags().StringVar(&since, "since", "", "export only runs created at/after this RFC3339 time")
	cmd.Flags().StringVar(&ticket, "ticket", "", "export only this ticket's runs (ticket-runs-only scope)")
	cmd.Flags().StringVar(&format, "format", "ndjson", "output format (ndjson)")
	return cmd
}

// writeExportRows is the test seam for the NDJSON writer loop.
func writeExportRows(w io.Writer, rows []map[string]any) error {
	for _, r := range rows {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, string(raw)); err != nil {
			return err
		}
	}
	return nil
}
