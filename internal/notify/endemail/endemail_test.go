// Tests for the endemail package. v17409-5 TDD coverage.
package endemail_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nfsarch33/helixon-platform/internal/notify/endemail"
	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
)

func TestTemplate_Build_ProducesHTMAndTextBodies(t *testing.T) {
	tmpl := endemail.Template{
		Plan:    "v17401-v17500",
		Subject: "[END] v17401-v17500 CLOSED GREEN",
		BodyMarkdown: `# Closeout

- one
- two

## Detail
fine text
`,
		JobID:    "v17401-v17500-end",
		IdempKey: "v17401-v17500-end",
		TenantID: "cursor-global-kb",
	}
	m := tmpl.Build()
	if m.Subject != "[END] v17401-v17500 CLOSED GREEN" {
		t.Errorf("Subject: want match, got %q", m.Subject)
	}
	if m.HTMLBody == "" {
		t.Error("HTMLBody: want non-empty")
	}
	if m.TextBody == "" {
		t.Error("TextBody: want non-empty (v17409-5 hardening)")
	}
	if !strings.Contains(m.HTMLBody, "<h1>") {
		t.Error("HTMLBody should contain <h1> for top-level header")
	}
	if !strings.Contains(m.TextBody, "===") {
		t.Error("TextBody should contain === banner (plain-text hardening)")
	}
}

func TestTemplate_Build_TargetsJaslian(t *testing.T) {
	m := endemail.Template{}.Build()
	if len(m.To) != 1 || m.To[0] != "jaslian@gmail.com" {
		t.Fatalf("To: want [jaslian@gmail.com], got %v", m.To)
	}
}

func TestTemplate_Build_HasIdempotencyKey(t *testing.T) {
	m := endemail.Template{IdempKey: "v17401-end"}.Build()
	if m.IdempotencyKey != "v17401-end" {
		t.Errorf("IdempotencyKey: want v17401-end, got %q", m.IdempotencyKey)
	}
}

func TestRenderAndAudit_WritesRow(t *testing.T) {
	dir := t.TempDir()
	db, err := notifydb.Open(dir+"/audit.sqlite3", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tmpl := endemail.Template{
		Plan:         "v17401-v17500",
		Subject:      "[END] v17401-v17500",
		BodyMarkdown: "# hi",
		JobID:        "v17401-end",
		IdempKey:     "v17401-end",
	}
	_, err = endemail.RenderAndAudit(context.Background(), tmpl, db)
	if err != nil {
		t.Fatalf("RenderAndAudit: %v", err)
	}
	row, found, _ := db.Get(context.Background(), "v17401-end")
	if !found {
		t.Fatal("audit row not written")
	}
	if row.Status != "rendered" {
		t.Errorf("Status: want rendered, got %q", row.Status)
	}
	if row.Vendor != "endemail-template" {
		t.Errorf("Vendor: want endemail-template, got %q", row.Vendor)
	}
}

func TestRenderAndAudit_NilDBSkipsAudit(t *testing.T) {
	tmpl := endemail.Template{IdempKey: "x", Subject: "s", BodyMarkdown: "y"}
	m, err := endemail.RenderAndAudit(context.Background(), tmpl, nil)
	if err != nil {
		t.Fatalf("RenderAndAudit with nil db: %v", err)
	}
	if m.Subject != "s" {
		t.Errorf("Subject: want s, got %q", m.Subject)
	}
}

func TestRenderAndAudit_Idempotent(t *testing.T) {
	dir := t.TempDir()
	db, _ := notifydb.Open(dir+"/audit.sqlite3", nil)
	defer db.Close()

	tmpl := endemail.Template{IdempKey: "dup-1", Subject: "s", BodyMarkdown: "y"}
	for i := 0; i < 3; i++ {
		if _, err := endemail.RenderAndAudit(context.Background(), tmpl, db); err != nil {
			t.Fatalf("RenderAndAudit #%d: %v", i, err)
		}
	}
	counts, _ := db.CountByVendor(context.Background())
	if counts["endemail-template"] != 1 {
		t.Fatalf("count: want 1 unique row, got %d", counts["endemail-template"])
	}
}

func TestMarkdownToPlainText_Headers(t *testing.T) {
	md := "# Top\n\nbody\n## Sub\n\nmore"
	// We can't call the unexported function directly, but the TextBody
	// path of Build goes through it. Use Build to verify.
	m := endemail.Template{BodyMarkdown: md}.Build()
	if !strings.Contains(m.TextBody, "=== TOP ===") {
		t.Errorf("TextBody: want === TOP ===, got:\n%s", m.TextBody)
	}
	if !strings.Contains(m.TextBody, "-- SUB --") {
		t.Errorf("TextBody: want -- SUB --, got:\n%s", m.TextBody)
	}
}

func TestMarkdownToPlainText_Bullets(t *testing.T) {
	md := "- a\n- b\n- c"
	m := endemail.Template{BodyMarkdown: md}.Build()
	for _, want := range []string{"  - a", "  - b", "  - c"} {
		if !strings.Contains(m.TextBody, want) {
			t.Errorf("TextBody: want %q, got:\n%s", want, m.TextBody)
		}
	}
}

func TestMarkdownToPlainText_CodeFences(t *testing.T) {
	md := "before\n```\ncode line\n```\nafter"
	m := endemail.Template{BodyMarkdown: md}.Build()
	if !strings.Contains(m.TextBody, "----") {
		t.Error("TextBody: want code fence ----")
	}
	if !strings.Contains(m.TextBody, "code line") {
		t.Error("TextBody: want code content preserved")
	}
}

func TestMarkdownToPlainText_TableRowPassthrough(t *testing.T) {
	md := "| a | b |\n| c | d |"
	m := endemail.Template{BodyMarkdown: md}.Build()
	if !strings.Contains(m.TextBody, "| a | b |") {
		t.Errorf("TextBody: want pipe-row preserved, got:\n%s", m.TextBody)
	}
}

func TestBuild_TextBodyTrimmed(t *testing.T) {
	md := "  \n\n  body  \n\n  "
	m := endemail.Template{BodyMarkdown: md}.Build()
	// v17409-5: leading whitespace inside the body is preserved (e.g.
	// indented bullet items), but trailing blank lines are trimmed and
	// the body must end with a single newline.
	if !strings.HasSuffix(m.TextBody, "\n") {
		t.Error("TextBody: want trailing newline")
	}
	if strings.HasSuffix(m.TextBody, "\n\n\n") {
		t.Error("TextBody: want trailing blank lines collapsed")
	}
}
