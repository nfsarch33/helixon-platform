package alertnotifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadStateTolerance(t *testing.T) {
	t.Parallel()
	valid := `{"version":1,"active":{"abc":{"fingerprint":"abc","alertname":"X","severity":"critical",` +
		`"labels":{"alertname":"X"},"starts_at":"2026-08-27T14:12:18Z","first_seen_unix":100,` +
		`"last_notified_unix":200}},"last_run_unix":300,"last_success_unix":250,"updated_unix":300}`

	tests := []struct {
		name       string
		write      bool
		contents   string
		wantActive int
		wantNote   string
		wantLastOK int64
	}{
		{name: "absent file starts empty", write: false, wantActive: 0, wantNote: "absent"},
		{name: "empty file starts empty", write: true, contents: "", wantActive: 0, wantNote: "empty"},
		{name: "corrupt file starts empty", write: true, contents: "{{{not json", wantActive: 0, wantNote: "corrupt"},
		{name: "truncated file starts empty", write: true, contents: `{"version":1,"active":{"a`, wantActive: 0, wantNote: "corrupt"},
		{name: "future version starts empty", write: true, contents: `{"version":99,"active":{}}`, wantActive: 0, wantNote: "version 99"},
		{name: "null active map is normalized", write: true, contents: `{"version":1,"active":null}`, wantActive: 0},
		{name: "valid file round-trips", write: true, contents: valid, wantActive: 1, wantLastOK: 250},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "state.json")
			if tc.write {
				if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
					t.Fatalf("seed state: %v", err)
				}
			}
			st, note := LoadState(path)
			if len(st.Active) != tc.wantActive {
				t.Fatalf("active = %d, want %d (note %q)", len(st.Active), tc.wantActive, note)
			}
			if st.Active == nil {
				t.Fatal("Active map must never be nil")
			}
			if tc.wantNote != "" && !strings.Contains(note, tc.wantNote) {
				t.Fatalf("note = %q, want containing %q", note, tc.wantNote)
			}
			if tc.wantNote == "" && note != "" {
				t.Fatalf("unexpected note %q", note)
			}
			if tc.wantLastOK != 0 && st.LastSuccessUnix != tc.wantLastOK {
				t.Fatalf("LastSuccessUnix = %d, want %d", st.LastSuccessUnix, tc.wantLastOK)
			}
		})
	}
}

func TestSaveStateRoundTripAndAtomicity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "state.json")

	st := NewState()
	st.Active["fp1"] = Entry{
		Fingerprint:      "fp1",
		AlertName:        "TailscaleNodeDown",
		Severity:         "critical",
		Labels:           map[string]string{"alertname": "TailscaleNodeDown", "instance": "c5"},
		StartsAt:         time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC),
		FirstSeenUnix:    1000,
		LastNotifiedUnix: 1000,
	}
	st.LastRunUnix = 1200
	st.LastSuccessUnix = 1100

	if err := SaveState(path, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, note := LoadState(path)
	if note != "" {
		t.Fatalf("unexpected note after save: %q", note)
	}
	if len(got.Active) != 1 || got.Active["fp1"].AlertName != "TailscaleNodeDown" {
		t.Fatalf("round-trip lost the entry: %+v", got)
	}
	if got.LastRunUnix != 1200 || got.LastSuccessUnix != 1100 {
		t.Fatalf("round-trip lost timestamps: %+v", got)
	}
	assertNoTempFiles(t, filepath.Dir(path), filepath.Base(path))

	// Overwriting must also leave the directory clean.
	st.LastRunUnix = 1300
	if err := SaveState(path, st); err != nil {
		t.Fatalf("SaveState overwrite: %v", err)
	}
	assertNoTempFiles(t, filepath.Dir(path), filepath.Base(path))
}

func TestWriteFileAtomicMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		perm os.FileMode
	}{
		{name: "textfile mode 0644", perm: 0o644},
		{name: "private mode 0600", perm: 0o600},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "out.txt")
			if err := WriteFileAtomic(path, []byte("payload\n"), tc.perm); err != nil {
				t.Fatalf("WriteFileAtomic: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if info.Mode().Perm() != tc.perm {
				t.Fatalf("mode = %v, want %v", info.Mode().Perm(), tc.perm)
			}
			raw, err := os.ReadFile(path) //nolint:gosec // G304: test-owned temp path
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(raw) != "payload\n" {
				t.Fatalf("content = %q", raw)
			}
			assertNoTempFiles(t, dir, "out.txt")
		})
	}
}

func TestWriteFileAtomicUnwritableDirectory(t *testing.T) {
	t.Parallel()
	// A path whose parent is an existing regular file cannot be created.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	err := WriteFileAtomic(filepath.Join(blocker, "child", "out.txt"), []byte("x"), 0o644)
	if err == nil {
		t.Fatal("expected an error writing beneath a regular file")
	}
	if !strings.Contains(err.Error(), "create directory") {
		t.Fatalf("error = %v, want a create-directory failure", err)
	}
}

// assertNoTempFiles fails when the atomic write left a scratch file behind.
func assertNoTempFiles(t *testing.T, dir, final string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == final {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") || strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("atomic write left a temp file behind: %q", e.Name())
		}
	}
}
