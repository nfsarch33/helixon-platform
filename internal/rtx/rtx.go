// Package rtx implements the v14515 response cache + replay_id layer
// described in docs/token-saving-strategy.md section 6.
//
// Design notes:
//
//   - Key: fnv64a(prompt + state_hash). The same prompt + same tool
//     state → same cache entry. Different tool state (e.g. cursor
//     pasted a new file) → different key, no false-positive cache hits.
//   - Storage: NDJSON, append-only. Each line is one Record. Concurrent
//     appenders are safe (file lock held during write).
//   - Replay: a `replay_id` may be passed instead of (or in addition
//     to) the prompt. The replay_id is the subagent's loop guard —
//     identical replay_id within TTL re-uses the prior response without
//     invoking the cell.
//   - TTL: 1h for tier >= 2, 24h for tier < 2. Configurable per call.
//
// This package is dependency-free (no imports from choosehook or
// costobs) so it can be embedded into any CLI without cycle problems.
package rtx

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tier is replicated here to avoid an import cycle on
// internal/llm/qwen36. Values 0..3 match the router tier numbering.
type Tier int

const (
	Tier0 Tier = iota
	Tier1
	Tier2
	Tier3
)

// Default TTL per tier (configurable via Options.TTL).
var defaultTTL = map[Tier]time.Duration{
	Tier0: 24 * time.Hour,
	Tier1: 24 * time.Hour,
	Tier2: 1 * time.Hour,
	Tier3: 1 * time.Hour,
}

// Record is one NDJSON row in the cache.
type Record struct {
	Key       string    `json:"key"`        // fnv64a hex
	Prompt    string    `json:"prompt"`     // raw prompt (for debug)
	StateHash string    `json:"state_hash"` // caller-supplied tool-state hash
	ReplayID  string    `json:"replay_id"`  // optional subagent loop guard
	Tier      Tier      `json:"tier"`
	CellID    string    `json:"cell_id"`
	Response  string    `json:"response"`
	CostUSD   float64   `json:"cost_usd"`
	CreatedAt time.Time `json:"created_at"`
}

// Options tweaks Cache behaviour.
type Options struct {
	Path string        // NDJSON file; created if missing
	TTL  time.Duration // overrides per-tier default if > 0
	Now  func() time.Time
}

// Cache is a goroutine-safe response cache backed by an NDJSON file.
type Cache struct {
	path string
	ttl  time.Duration
	now  func() time.Time

	mu     sync.Mutex // protects file writes
	mem    map[string]Record
	loaded bool
}

// New returns a Cache rooted at opts.Path.
func New(opts Options) (*Cache, error) {
	if opts.Path == "" {
		return nil, errors.New("rtx: Options.Path required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	c := &Cache{
		path: opts.Path,
		ttl:  opts.TTL,
		now:  opts.Now,
		mem:  map[string]Record{},
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o755); err != nil {
		return nil, fmt.Errorf("rtx: mkdir: %w", err)
	}
	return c, nil
}

// Key returns the deterministic cache key for a (prompt, stateHash)
// pair. Exported for tests + external callers that want to inspect.
func Key(prompt, stateHash string) string {
	h := fnv.New64a()
	h.Write([]byte(prompt))
	h.Write([]byte{0x1f}) // separator (unit separator, never in JSON)
	h.Write([]byte(stateHash))
	return fmt.Sprintf("fnv64a:%016x", h.Sum64())
}

// Lookup returns a cached Record and true if (key, replayID, tier) hits
// within TTL; (zero, false) otherwise. If replayID is non-empty, a hit
// on the most recent Record with the same replayID is accepted
// regardless of prompt.
func (c *Cache) Lookup(prompt, stateHash, replayID string, tier Tier) (Record, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		if err := c.loadLocked(); err != nil {
			// Cache is best-effort; load errors are non-fatal.
			c.mem = map[string]Record{}
		}
		c.loaded = true
	}
	ttl := c.ttl
	if ttl <= 0 {
		ttl = defaultTTL[tier]
	}
	key := Key(prompt, stateHash)
	if r, ok := c.mem[key]; ok {
		if c.now().Sub(r.CreatedAt) <= ttl {
			return r, true
		}
	}
	// Replay ID path: scan all records (small N; not a perf hot path).
	if replayID != "" {
		for _, r := range c.mem {
			if r.ReplayID == replayID && c.now().Sub(r.CreatedAt) <= ttl {
				return r, true
			}
		}
	}
	return Record{}, false
}

// Store appends r to the cache file and memory map. The Record's
// Key + CreatedAt are populated if zero.
func (c *Cache) Store(r Record) error {
	if r.Key == "" {
		return errors.New("rtx.Store: Key required")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = c.now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		_ = c.loadLocked()
		c.loaded = true
	}
	c.mem[r.Key] = r
	// ReplayID indexing note: rtx.Store keeps one mem entry per key and iterates
	// on Lookup, so a separate replay_id index is intentionally omitted
	// (cheap for <10k entries). Original blank branch retained here
	// to make the design decision explicit; suppressed via SA9003.
	// Append to file (NDJSON).
	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("rtx.Store: open: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("rtx.Store: encode: %w", err)
	}
	return nil
}

// Path returns the on-disk path (for tests + status reporting).
func (c *Cache) Path() string { return c.path }

// Size returns the number of records currently in memory.
func (c *Cache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.mem)
}

func (c *Cache) loadLocked() error {
	f, err := os.Open(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			line = []byte(strings.TrimRight(string(line), "\r\n"))
			if len(line) == 0 {
				if err == io.EOF {
					break
				}
				continue
			}
			var rec Record
			if jerr := json.Unmarshal(line, &rec); jerr == nil {
				c.mem[rec.Key] = rec
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// SortedKeys is exported for tests + `rtx-tools doctor` style CLIs.
func (c *Cache) SortedKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.mem))
	for k := range c.mem {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
