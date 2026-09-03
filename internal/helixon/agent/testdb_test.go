package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// testDBPath returns a path for a throwaway SQLite database, preferring a
// RAM-backed directory. On this estate's vhdx substrate a file open costs
// 1-50s under load, and with every test opening its own database that alone
// pushed the package past go test's 10-minute deadline in a full run. These
// tests check semantics, not the disk (the S1 lesson: only memory is
// load-proof), so the database lives in tmpfs when the host has one.
func testDBPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(testDBDir(t), name)
}

// testDBDir returns a throwaway directory for database files, RAM-backed
// when /dev/shm exists (Linux), otherwise t.TempDir().
func testDBDir(t *testing.T) string {
	t.Helper()
	if fi, err := os.Stat("/dev/shm"); err == nil && fi.IsDir() {
		if dir, err := os.MkdirTemp("/dev/shm", "hlxn-agent-test-"); err == nil {
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return dir
		}
	}
	return t.TempDir()
}
