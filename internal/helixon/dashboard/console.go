package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/helixon/agent"
	"github.com/nfsarch33/helixon-platform/internal/helixon/memory"
)

// The operator console's read API (v18809, S4). Every panel of the console
// renders a payload from one of these endpoints; none of them invents data:
// runs come from the durable run store, costs from the same table, evals
// from the EvoSpine cycle ledger and the published textfile metrics, memory
// from the runtime's searcher. Absence is reported as absence (an empty
// list, a 503 with a reason), never as a placeholder.
//
//	GET /api/v1/runs?status=&limit=      -> {"runs":[RunRecord...]}
//	GET /api/v1/runs/{id}                -> {"run":RunRecord,"steps":[...],"turns":[...]}
//	GET /api/v1/costs                    -> {"last_24h":RunUsage,"last_7d":RunUsage,"all_time":RunUsage}
//	GET /api/v1/evals?limit=             -> {"ledger":[cycle...],"metrics":[{"name","labels","value","file"}],"ledger_path","textfile_dir"}
//	GET /api/v1/memory/search?q=&limit=  -> {"query","results":[...]}

// RunView is the read-only contract the console's run and cost endpoints
// depend on. *agent.SessionStore satisfies it; tests supply a fake.
type RunView interface {
	ListRuns(ctx context.Context, f agent.RunFilter) ([]agent.RunRecord, error)
	GetRun(ctx context.Context, id string) (*agent.RunRecord, error)
	ListSteps(ctx context.Context, runID string) ([]agent.RunStep, error)
	ListTurns(ctx context.Context, sessionID string, limit int) ([]agent.Turn, error)
	RunUsage(ctx context.Context, since time.Time) (agent.RunUsage, error)
}

// MemorySearcher is the slice of the hybrid searcher the console needs.
// *memory.HybridSearcher satisfies it.
type MemorySearcher interface {
	Search(ctx context.Context, query, appID, userID, tenantID string) ([]memory.HybridResult, error)
}

// ConsoleConfig locates the eval artifacts the console renders.
type ConsoleConfig struct {
	// EvalLedgerPath is the EvoSpine cycle ledger (NDJSON, one record per
	// cycle, written by `eval ab --cycle-record`).
	EvalLedgerPath string
	// TextfileDir holds the Prometheus textfiles the gates publish
	// (hlxn_student_score_ratio and friends).
	TextfileDir string
	// AppID / UserID / TenantID scope memory searches; empty = unscoped.
	AppID, UserID, TenantID string
}

// DefaultConsoleConfig points at the conventional locations under home.
func DefaultConsoleConfig() ConsoleConfig {
	home, _ := os.UserHomeDir()
	return ConsoleConfig{
		EvalLedgerPath: filepath.Join(home, "logs", "runx", "evospine-cycles.ndjson"),
		TextfileDir:    filepath.Join(home, ".local", "share", "node-exporter-textfile"),
	}
}

// MountConsole registers the console endpoints on mux. A nil RunView makes
// the run and cost endpoints answer 503; a nil MemorySearcher does the same
// for memory search.
func MountConsole(mux *http.ServeMux, rv RunView, mem MemorySearcher, cfg *ConsoleConfig) {
	if mux == nil {
		return
	}
	if cfg == nil {
		c := DefaultConsoleConfig()
		cfg = &c
	}
	mux.Handle("GET /api/v1/runs", RunsHandler(rv))
	mux.Handle("GET /api/v1/runs/{id}", RunHandler(rv))
	mux.Handle("GET /api/v1/costs", CostsHandler(rv))
	mux.Handle("GET /api/v1/evals", EvalsHandler(cfg))
	mux.Handle("GET /api/v1/memory/search", MemorySearchHandler(mem, cfg))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func limitParam(r *http.Request, def, ceiling int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return def
	}
	if n > ceiling {
		return ceiling
	}
	return n
}

// RunsHandler lists runs newest first; ?status= filters, ?limit= caps (100 default, 500 max).
func RunsHandler(rv RunView) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rv == nil {
			writeErr(w, http.StatusServiceUnavailable, "run store not initialized")
			return
		}
		runs, err := rv.ListRuns(r.Context(), agent.RunFilter{
			Status: agent.RunStatus(r.URL.Query().Get("status")),
			Limit:  limitParam(r, 100, 500),
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if runs == nil {
			runs = []agent.RunRecord{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "generated_at": time.Now().UTC().Format(time.RFC3339Nano)})
	})
}

// RunDetail is one run with its durable steps and the session's turns.
type RunDetail struct {
	Run   agent.RunRecord `json:"run"`
	Steps []agent.RunStep `json:"steps"`
	Turns []agent.Turn    `json:"turns"`
}

// RunHandler serves one run with its steps and turns.
func RunHandler(rv RunView) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rv == nil {
			writeErr(w, http.StatusServiceUnavailable, "run store not initialized")
			return
		}
		id := r.PathValue("id")
		run, err := rv.GetRun(r.Context(), id)
		if errors.Is(err, agent.ErrRunNotFound) {
			writeErr(w, http.StatusNotFound, "run not found")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		steps, err := rv.ListSteps(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		turns, err := rv.ListTurns(r.Context(), run.SessionID, 0)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if steps == nil {
			steps = []agent.RunStep{}
		}
		if turns == nil {
			turns = []agent.Turn{}
		}
		writeJSON(w, http.StatusOK, RunDetail{Run: *run, Steps: steps, Turns: turns})
	})
}

// CostsHandler reports token usage over three windows from the runs table.
func CostsHandler(rv RunView) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rv == nil {
			writeErr(w, http.StatusServiceUnavailable, "run store not initialized")
			return
		}
		now := time.Now().UTC()
		out := map[string]any{"generated_at": now.Format(time.RFC3339Nano)}
		for name, since := range map[string]time.Time{"last_24h": now.Add(-24 * time.Hour), "last_7d": now.Add(-7 * 24 * time.Hour), "all_time": {}} {
			u, err := rv.RunUsage(r.Context(), since)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			out[name] = u
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// TextfileMetric is one sample parsed from a Prometheus textfile.
type TextfileMetric struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
	File   string            `json:"file"`
}

// EvalsHandler serves the newest cycle records and every hlxn_* sample in
// the textfile directory. A missing ledger is an empty list plus the path
// that was looked at, not an error: the console must show "no cycle yet".
func EvalsHandler(cfg *ConsoleConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := limitParam(r, 20, 200)
		ledger, lerr := readLedger(cfg.EvalLedgerPath, limit)
		metrics, merr := readTextfiles(cfg.TextfileDir, "hlxn_")
		out := map[string]any{
			"ledger": ledger, "ledger_path": cfg.EvalLedgerPath,
			"metrics": metrics, "textfile_dir": cfg.TextfileDir,
			"generated_at": time.Now().UTC().Format(time.RFC3339Nano),
		}
		if lerr != nil {
			out["ledger_error"] = lerr.Error()
		}
		if merr != nil {
			out["metrics_error"] = merr.Error()
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// readLedger returns the last limit records of an NDJSON ledger, newest
// first. A missing file is an empty ledger.
func readLedger(path string, limit int) ([]map[string]any, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-configured ledger path
	if errors.Is(err, os.ErrNotExist) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return []map[string]any{}, err
	}
	defer func() { _ = f.Close() }()
	var recs []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue // a torn line is skipped, not fatal
		}
		recs = append(recs, m)
	}
	if err := sc.Err(); err != nil {
		return []map[string]any{}, err
	}
	if len(recs) > limit {
		recs = recs[len(recs)-limit:]
	}
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	if recs == nil {
		recs = []map[string]any{}
	}
	return recs, nil
}

// readTextfiles parses every *.prom file in dir and keeps the samples whose
// metric name starts with prefix. A missing directory yields no samples.
func readTextfiles(dir, prefix string) ([]TextfileMetric, error) {
	out := []TextfileMetric{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".prom") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- operator-configured textfile directory
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if m, ok := parseSample(line, prefix); ok {
				m.File = e.Name()
				out = append(out, m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].File < out[j].File
	})
	return out, nil
}

// parseSample reads one exposition line: name{labels} value.
func parseSample(line, prefix string) (TextfileMetric, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, prefix) {
		return TextfileMetric{}, false
	}
	nameEnd := strings.IndexAny(line, "{ ")
	if nameEnd < 0 {
		return TextfileMetric{}, false
	}
	m := TextfileMetric{Name: line[:nameEnd]}
	rest := line[nameEnd:]
	if strings.HasPrefix(rest, "{") {
		end := strings.Index(rest, "}")
		if end < 0 {
			return TextfileMetric{}, false
		}
		m.Labels = map[string]string{}
		for _, kv := range strings.Split(rest[1:end], ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
			if ok {
				m.Labels[k] = strings.Trim(v, "\"")
			}
		}
		rest = rest[end+1:]
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
	if err != nil {
		return TextfileMetric{}, false
	}
	m.Value = v
	return m, true
}

// MemorySearchHandler proxies a query to the runtime's memory searcher.
func MemorySearchHandler(mem MemorySearcher, cfg *ConsoleConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mem == nil {
			writeErr(w, http.StatusServiceUnavailable, "memory not configured on this runtime")
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			writeErr(w, http.StatusBadRequest, "q is required")
			return
		}
		limit := limitParam(r, 10, 50)
		results, err := mem.Search(r.Context(), q, cfg.AppID, cfg.UserID, cfg.TenantID)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		if len(results) > limit {
			results = results[:limit]
		}
		if results == nil {
			results = []memory.HybridResult{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"query": q, "results": results, "generated_at": time.Now().UTC().Format(time.RFC3339Nano)})
	})
}
