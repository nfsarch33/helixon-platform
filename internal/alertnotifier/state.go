package alertnotifier

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StateVersion is the schema version of the on-disk state file. A future
// incompatible change bumps this; an unrecognized version is treated the
// same as a corrupt file (start empty) rather than crashing the alerter.
const StateVersion = 1

// DefaultStatePath is where the notifier remembers what it has already
// told a human about.
const DefaultStatePath = "/home/jaslian/.local/state/hlxn/alert-notifier-state.json"

// stateFileMode is 0600: the state file records label sets from the
// monitoring plane and needs no wider audience.
const stateFileMode os.FileMode = 0o600

// Entry is one remembered firing alert.
type Entry struct {
	Fingerprint      string            `json:"fingerprint"`
	AlertName        string            `json:"alertname"`
	Severity         string            `json:"severity"`
	Labels           map[string]string `json:"labels"`
	StartsAt         time.Time         `json:"starts_at"`
	FirstSeenUnix    int64             `json:"first_seen_unix"`
	LastNotifiedUnix int64             `json:"last_notified_unix"`
}

// State is the full cross-run memory of the notifier.
type State struct {
	Version int `json:"version"`
	// Active maps fingerprint -> entry for every alert that was firing at
	// the end of the last successfully-delivered run.
	Active map[string]Entry `json:"active"`
	// LastRunUnix is the last run that obtained a valid alert list.
	LastRunUnix int64 `json:"last_run_unix"`
	// LastSuccessUnix is the last run whose send was accepted by the vendor.
	LastSuccessUnix int64 `json:"last_success_unix"`
	// UpdatedUnix stamps the write itself.
	UpdatedUnix int64 `json:"updated_unix"`
}

// NewState returns an empty, well-formed state.
func NewState() State {
	return State{Version: StateVersion, Active: map[string]Entry{}}
}

// LoadState reads the state file. An absent, empty, unreadable, corrupt,
// or future-versioned file is NOT an error: it yields an empty state and a
// human-readable note. An alerter that refuses to run because its
// bookkeeping file is damaged is an alerter that stops alerting, which is
// the exact failure this whole component exists to end. The cost of
// starting empty is one duplicate digest.
func LoadState(path string) (State, string) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: operator-configured state path, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return NewState(), "state file absent; treating as first run"
		}
		return NewState(), fmt.Sprintf("state file unreadable (%v); treating as first run", err)
	}
	if len(raw) == 0 {
		return NewState(), "state file empty; treating as first run"
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return NewState(), fmt.Sprintf("state file corrupt (%v); treating as first run", err)
	}
	if st.Version != StateVersion {
		return NewState(), fmt.Sprintf("state file version %d != %d; treating as first run", st.Version, StateVersion)
	}
	if st.Active == nil {
		st.Active = map[string]Entry{}
	}
	return st, ""
}

// SaveState atomically persists the state file.
func SaveState(path string, st State) error {
	st.Version = StateVersion
	if st.Active == nil {
		st.Active = map[string]Entry{}
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	raw = append(raw, '\n')
	return WriteFileAtomic(path, raw, stateFileMode)
}

// WriteFileAtomic writes data to path via a same-directory temp file plus
// rename, so a reader (or a crashed run) never observes a half-written
// file and no temp file is left behind on the happy path. The temp file is
// created in the destination directory because rename(2) is only atomic
// within a filesystem.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	// 0750: the node-exporter textfile collector on this fleet runs as the
	// same user, so no world-execute bit is needed to reach the .prom file.
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return fmt.Errorf("create directory %s: %w", dir, mkErr)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	// CreateTemp always makes the file 0600; widen it deliberately when the
	// caller needs a reader (the node-exporter textfile collector).
	if err = os.Chmod(tmp, perm); err != nil { //nolint:gosec // G302: caller-chosen mode; the textfile collector requires 0644
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename temp file into place: %w", err)
	}
	return nil
}
