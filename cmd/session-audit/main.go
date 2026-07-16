// Command session-audit queries the notifydb SQLite store for session-end
// email audit rows. v18654-2: closes the loop from endemail.RenderAndAudit
// to a queryable audit surface.
//
// Usage:
//
//	session-audit --plan v18652-v18655 [--db /path/to/notifydb.sqlite3] [--json]
//
// Exit codes:
//
//	0 = success (rows printed)
//	2 = db open error
//
// The default DB path is ~/logs/runx/notifydb.sqlite3 (set by
// notifydb.DefaultPath). Output is NDJSON by default (line per row).
// Pass --json for a single JSON array.
//
// v18654-2
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/nfsarch33/helixon-platform/internal/notify/notifydb"
)

func main() {
	plan := flag.String("plan", "", "plan prefix to filter on (e.g. v18652- matches all v18652-* rows)")
	dbPath := flag.String("db", "", "path to notifydb SQLite file (default: ~/logs/runx/notifydb.sqlite3)")
	jsonOut := flag.Bool("json", false, "emit a single JSON array instead of NDJSON")
	flag.Parse()

	os.Exit(runSessionAudit(*plan, *dbPath, *jsonOut))
}

func runSessionAudit(plan, dbPath string, jsonOut bool) int {
	path := dbPath
	if path == "" {
		path = notifydb.DefaultPath()
	}
	db, err := notifydb.Open(path, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: open %s: %v\n", path, err)
		return 2
	}
	defer db.Close()

	prefix := plan
	if prefix == "" {
		prefix = "%"
	}
	rows, err := db.ListByPlan(context.Background(), prefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: ListByPlan: %v\n", err)
		return 2
	}

	if jsonOut {
		out, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: marshal: %v\n", err)
			return 2
		}
		fmt.Println(string(out))
		return 0
	}
	for _, r := range rows {
		line, _ := json.Marshal(r)
		fmt.Println(string(line))
	}
	return 0
}
