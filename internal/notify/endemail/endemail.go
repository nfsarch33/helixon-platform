// Package endemail provides a hardened END email template + audit
// integration for helixon-platform/cmd/send-end-email and any other
// closeout code path. The template produces both HTML and plain-text
// bodies (so the receiver sees something readable in any client) and
// emits a notifydb row on every attempt (success or failure).
//
// v17409-5 hardening: the v16101 END email had only HTML; some clients
// strip HTML aggressively. The plain-text fallback is now a first-class
// render alongside the HTML, not a degraded version. The audit row is
// written even when the SMTP/HTTP send returns an error.
package endemail

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/notify"
	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
)

// Template is the canonical END email payload (HTML + plain text).
type Template struct {
	Plan         string // e.g. "v17401-v17500"
	Subject      string // e.g. "[END] v17401-v17500 CLOSED GREEN"
	BodyMarkdown string // raw markdown body authored by closeout
	JobID        string // cost-attribution job id
	IdempKey     string // dedup key (e.g. "v17401-v17500-end")
	TenantID     string // "cursor-global-kb" or "helixon-platform"
}

// Build renders the template into a notify.Email with both HTML and
// plain-text bodies. The plain-text body is the markdown with the most
// common decorations stripped (headers become "HEADER", bullets become
// "  - ...", tables are removed). HTML preserves structure for modern
// clients.
func (t Template) Build() notify.Email {
	html := markdownToHTML(t.BodyMarkdown)
	text := markdownToPlainText(t.BodyMarkdown)
	return notify.Email{
		To:             []string{"jaslian@gmail.com"},
		Subject:        t.Subject,
		HTMLBody:       html,
		TextBody:       text,
		IdempotencyKey: t.IdempKey,
		JobID:          t.JobID,
		TenantID:       t.TenantID,
		CostEstimate:   0.001,
	}
}

// RenderAndAudit renders the template AND writes an audit row. Errors
// from the audit write are returned but do not block the email send.
// The returned notify.Email is the input to the live dispatcher.
//
// If db is nil the audit write is skipped silently (dry-run path).
func RenderAndAudit(ctx context.Context, t Template, db *notifydb.DB) (notify.Email, error) {
	m := t.Build()
	if db == nil {
		return m, nil
	}
	row := notifydb.Dispatch{
		ID:          m.IdempotencyKey,
		Vendor:      "endemail-template",
		Recipient:   strings.Join(m.To, ","),
		Subject:     m.Subject,
		Status:      "rendered",
		CreatedUnix: time.Now().Unix(),
		Attempt:     1,
	}
	if err := db.Insert(ctx, row); err != nil {
		return m, fmt.Errorf("notifydb insert: %w", err)
	}
	return m, nil
}

// --- converters ---

// markdownToHTML is the same minimal converter used by cmd/send-end-email;
// promoted to a shared location so the template and the CLI agree.
func markdownToHTML(md string) string {
	var b strings.Builder
	inCode := false
	for _, line := range strings.Split(md, "\n") {
		switch {
		case strings.HasPrefix(line, "```"):
			if inCode {
				b.WriteString("</pre>\n")
				inCode = false
			} else {
				b.WriteString("<pre>")
				inCode = true
			}
		case strings.HasPrefix(line, "# "):
			b.WriteString("<h1>")
			b.WriteString(strings.TrimPrefix(line, "# "))
			b.WriteString("</h1>\n")
		case strings.HasPrefix(line, "## "):
			b.WriteString("<h2>")
			b.WriteString(strings.TrimPrefix(line, "## "))
			b.WriteString("</h2>\n")
		case strings.HasPrefix(line, "### "):
			b.WriteString("<h3>")
			b.WriteString(strings.TrimPrefix(line, "### "))
			b.WriteString("</h3>\n")
		case strings.HasPrefix(line, "- "):
			b.WriteString("<li>")
			b.WriteString(strings.TrimPrefix(line, "- "))
			b.WriteString("</li>\n")
		case strings.HasPrefix(line, "|") && strings.Contains(line, "|"):
			b.WriteString(line)
			b.WriteString("\n")
		default:
			if inCode {
				b.WriteString(line)
				b.WriteString("\n")
			} else {
				b.WriteString("<p>")
				b.WriteString(line)
				b.WriteString("</p>\n")
			}
		}
	}
	if inCode {
		b.WriteString("</pre>\n")
	}
	return b.String()
}

// markdownToPlainText is the v17409-5 hardening: produce a readable
// plain-text version that survives clients that strip HTML. Headers
// become ALL-CAPS banners, bullets become "  - ", tables are dropped,
// code blocks become fenced ASCII.
func markdownToPlainText(md string) string {
	var b strings.Builder
	inCode := false
	for _, line := range strings.Split(md, "\n") {
		switch {
		case strings.HasPrefix(line, "```"):
			if inCode {
				b.WriteString("----\n")
				inCode = false
			} else {
				b.WriteString("----\n")
				inCode = true
			}
		case strings.HasPrefix(line, "# "):
			b.WriteString("\n=== ")
			b.WriteString(strings.ToUpper(strings.TrimPrefix(line, "# ")))
			b.WriteString(" ===\n\n")
		case strings.HasPrefix(line, "## "):
			b.WriteString("\n-- ")
			b.WriteString(strings.ToUpper(strings.TrimPrefix(line, "## ")))
			b.WriteString(" --\n\n")
		case strings.HasPrefix(line, "### "):
			b.WriteString("\n* ")
			b.WriteString(strings.TrimPrefix(line, "### "))
			b.WriteString("\n\n")
		case strings.HasPrefix(line, "- "):
			b.WriteString("  - ")
			b.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "- ")))
			b.WriteString("\n")
		case strings.HasPrefix(line, "* "):
			b.WriteString("  - ")
			b.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "* ")))
			b.WriteString("\n")
		case strings.HasPrefix(line, "|") && strings.Contains(line, "|"):
			// tables: emit as plain rows with the pipe separators
			b.WriteString(line)
			b.WriteString("\n")
		default:
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	if inCode {
		b.WriteString("----\n")
	}
	// Trim trailing blank lines but preserve leading whitespace
	// (e.g. indented bullet items in the source).
	out := b.String()
	for strings.HasSuffix(out, "\n\n\n") {
		out = strings.TrimSuffix(out, "\n")
	}
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out
}
