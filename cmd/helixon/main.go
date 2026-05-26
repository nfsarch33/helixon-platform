package main

import (
	"fmt"
	"os"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("helixon %s (commit=%s, date=%s)\n", version, commit, date)
		os.Exit(0)
	}
	fmt.Println("helixon-platform starting...")
	// Platform runtime will be wired here
	select {}
}
