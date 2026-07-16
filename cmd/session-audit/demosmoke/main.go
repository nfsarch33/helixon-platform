// Smoke test: seeds the real notifydb with two rows so `session-audit` can
// be demonstrated end-to-end. v18654-2 evidence-gathering helper.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
)

func main() {
	dir := filepath.Join(os.Getenv("HOME"), "logs", "runx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
		os.Exit(2)
	}
	path := notifydb.DefaultPath()
	db, err := notifydb.Open(path, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(2)
	}
	defer db.Close()

	ctx := context.Background()
	for _, r := range []notifydb.Dispatch{
		{ID: "v18654-2-end", Vendor: "resend", Recipient: "jaslian@gmail.com", Subject: "[END] v18654-2 session audit", Status: "ok", CreatedUnix: 1752749000, Attempt: 1},
		{ID: "v18654-1-end", Vendor: "brevo", Recipient: "jaslian@gmail.com", Subject: "[END] v18654-1 s3 litestream", Status: "rendered", CreatedUnix: 1752748900, Attempt: 1},
	} {
		if err := db.Insert(ctx, r); err != nil {
			fmt.Fprintf(os.Stderr, "insert %s: %v\n", r.ID, err)
			os.Exit(2)
		}
		fmt.Printf("seeded %s -> %s\n", r.ID, path)
	}
}
