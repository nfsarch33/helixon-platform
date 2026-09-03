package dashboard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
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

// writeJSON encodes first and writes only what encoded.
//
// json.NewEncoder(w).Encode commits the status line before it can fail, and
// it can: one non-finite float anywhere in the payload makes Encode return an
// error after 200 has already gone out, and the client is handed a 200 with
// an empty body. Every consumer of this API reads that as "loaded, nothing
// here" and waits forever. These payloads are bounded, so buffering costs one
// copy and turns that case into an honest 500 that says what happened.
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		body, _ := json.Marshal(map[string]string{"error": "response could not be encoded: " + err.Error()})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(append(body, '\n'))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
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
		// An unknown status must not answer 200 with an empty list: a typo in
		// a filter would then read exactly like "nothing needs a human", which
		// is the one answer an operator acts on by walking away.
		status := agent.RunStatus(r.URL.Query().Get("status"))
		if status != "" && !knownRunStatus(status) {
			writeErr(w, http.StatusBadRequest, "unknown status "+strconv.Quote(string(status))+"; want one of "+strings.Join(runStatusNames(), ", "))
			return
		}
		runs, err := rv.ListRuns(r.Context(), agent.RunFilter{
			Status: status,
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

// RunDetail is one run with its durable steps and the turns of the SESSION
// the run belongs to -- not of the run alone. A session outlives its runs and
// the store keys turns by session, so these are the conversation the loop saw,
// which is wider than this run. TurnsScope says so in the payload, and the
// console labels the panel with it rather than implying the narrower thing.
//
// Both lists are bounded by ?limit= (200 by default, 1000 max): the console
// polls this endpoint, and a long-running session would otherwise grow an
// unbounded response with no way to ask for less.
type RunDetail struct {
	Run            agent.RunRecord `json:"run"`
	Steps          []agent.RunStep `json:"steps"`
	Turns          []agent.Turn    `json:"turns"`
	TurnsScope     string          `json:"turns_scope"`
	Limit          int             `json:"limit"`
	StepsTruncated bool            `json:"steps_truncated"`
	TurnsTruncated bool            `json:"turns_truncated"`
}

// knownRunStatus reports whether s is a status the store can hold.
func knownRunStatus(s agent.RunStatus) bool {
	switch s {
	case agent.RunRunning, agent.RunCompleted, agent.RunFailed, agent.RunNeedsHuman:
		return true
	}
	return false
}

func runStatusNames() []string {
	return []string{string(agent.RunRunning), string(agent.RunCompleted), string(agent.RunFailed), string(agent.RunNeedsHuman)}
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
		limit := limitParam(r, 200, 1000)
		turns, err := rv.ListTurns(r.Context(), run.SessionID, limit)
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
		detail := RunDetail{Run: *run, Steps: steps, Turns: turns, TurnsScope: "session", Limit: limit}
		// The bound belongs to the endpoint, not to whichever RunView is
		// behind it: the store applies it in SQL, but a view that ignored it
		// would otherwise hand the console an unbounded response through an
		// endpoint that advertises a limit.
		//
		// The store returns turns oldest first, so a clipped list is the start
		// of the session, not its tail. Saying which end was kept is the
		// difference between a short conversation and a truncated one.
		if len(detail.Turns) > limit {
			detail.Turns = detail.Turns[:limit]
		}
		if len(detail.Turns) >= limit {
			detail.TurnsTruncated = true
		}
		if len(detail.Steps) > limit {
			detail.Steps = detail.Steps[:limit]
			detail.StepsTruncated = true
		}
		writeJSON(w, http.StatusOK, detail)
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
		// The windows select runs by the time they were CREATED, because that
		// is the only timestamp the runs table indexes them by. A run that
		// started 30 hours ago and is still going contributes nothing to
		// last_24h, and one created inside the window contributes all of its
		// tokens however long it ran. That is a defensible reading of "spend
		// in the last day" but it is not the only one, so the payload names
		// the basis and the console labels the panel with it.
		out := map[string]any{"generated_at": now.Format(time.RFC3339Nano), "basis": "run_created_at"}
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
	// Prometheus writes NaN for "no value" and +Inf for an unbounded one, and
	// both are legal in a textfile. Neither is representable in JSON, and
	// encoding one used to fail the whole /evals response after its 200 had
	// been sent. A sample that cannot be carried is omitted rather than
	// carried wrongly; writeJSON is the belt to this pair of braces.
	if math.IsNaN(v) || math.IsInf(v, 0) {
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
