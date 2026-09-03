package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nfsarch33/helixon-platform/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Kill -9 soak.
//
// The parent test spawns THIS test binary as a child (the os/exec helper
// pattern) that drives a real Agent against a real SQLite store, and sends
// it SIGKILL at a random point. It keeps re-spawning the child, which
// resumes the same run from the durable log, until a child reports a
// terminal state. Then it checks the invariants the sprint promises:
//
//   - zero lost work: every tool call the scripted model asked for is in
//     the effect log exactly once, and the run's steps are all closed;
//   - zero duplicated side effects: the effect log has no duplicate call id,
//     even though the process died mid-call many times. The read-only style
//     tool ("lookup") is re-executed on resume and writes under an
//     idempotency key; the mutating tool ("commit") is NEVER re-executed
//     when its outcome is unknown - the run stops for a human, which the
//     harness counts and verifies rather than papering over;
//   - the run row's terminal status matches what the effect log shows.
//
// Rounds and kill timing are seeded so a failure replays: set
// HLXN_SOAK_SEED to the seed the failure printed.

const (
	soakChildEnv  = "HLXN_SOAK_CHILD"
	soakDBEnv     = "HLXN_SOAK_DB"
	soakLogEnv    = "HLXN_SOAK_LOG"
	soakRunEnv    = "HLXN_SOAK_RUN"
	soakRoundsEnv = "HLXN_SOAK_ROUNDS"
	soakSeedEnv   = "HLXN_SOAK_SEED"
	soakLookups   = 6 // lookup tool calls before the commit
)

// soakProvider scripts a fixed conversation from the message history alone,
// so it produces the same next step in any process that resumes the run:
// N lookups, one commit, then the final answer.
type soakProvider struct{}

//nolint:gocritic // hugeParam: the llm.Provider contract takes the request by value
func (soakProvider) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	assistants := 0
	for _, m := range req.Messages {
		if m.Role == string(RoleAssistant) {
			assistants++
		}
	}
	usage := llm.Usage{PromptTokens: 10, CompletionTokens: 5}
	switch {
	case assistants < soakLookups:
		return assistantWithTools(llm.ToolCall{ID: fmt.Sprintf("lookup_%d", assistants+1), Type: "function",
			Function: llm.FunctionCall{Name: "lookup", Arguments: fmt.Sprintf(`{"n":%d}`, assistants+1)}}), nil
	case assistants == soakLookups:
		return assistantWithTools(llm.ToolCall{ID: "commit_1", Type: "function",
			Function: llm.FunctionCall{Name: "commit", Arguments: `{}`}}), nil
	default:
		return &llm.CompletionResponse{Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: "soak complete"}}}, Usage: usage}, nil
	}
}

// soakExecutor appends "<tool_call_id>" to the effect log. lookup writes
// under an idempotency key (it checks the log first), which is what makes it
// safe to re-execute; commit appends unconditionally, which is what makes it
// a mutation whose second execution would be a duplicated side effect. Both
// pause briefly so a SIGKILL lands mid-call often enough to matter.
type soakExecutor struct{ logPath string }

func (e soakExecutor) Available() []llm.Tool {
	return []llm.Tool{
		{Type: "function", Function: llm.FunctionDef{Name: "lookup", Description: "idempotent read"}},
		{Type: "function", Function: llm.FunctionDef{Name: "commit", Description: "non-idempotent write"}},
	}
}

func (e soakExecutor) Execute(_ context.Context, name, args string) (string, error) {
	time.Sleep(15 * time.Millisecond)
	var id string
	if name == "lookup" {
		id = "lookup_" + strings.TrimSuffix(strings.TrimPrefix(args, `{"n":`), "}")
		if soakLogHas(e.logPath, id) {
			return "cached", nil
		}
	} else {
		id = "commit_1"
	}
	f, err := os.OpenFile(e.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(id + "\n"); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	time.Sleep(15 * time.Millisecond)
	return "ok:" + id, nil
}

func soakLogHas(path, id string) bool {
	for _, line := range soakLogLines(path) {
		if line == id {
			return true
		}
	}
	return false
}

func soakLogLines(path string) []string {
	data, err := os.ReadFile(path) // #nosec G304 G703 -- fixture path under the test's TempDir
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// TestSoakChildProcess is the child's main. It is skipped unless invoked by
// the parent with the environment set, so `go test ./...` never runs it as
// a test of its own.
func TestSoakChildProcess(t *testing.T) {
	if os.Getenv(soakChildEnv) != "1" {
		t.Skip("soak child: only runs when spawned by TestKillNineSoak")
	}
	os.Exit(soakChild())
}

// soakChild is the child process body. It returns the exit code instead of
// calling os.Exit itself, so its defers run.
func soakChild() int {
	ctx := context.Background()
	store, err := NewSessionStore(ctx, os.Getenv(soakDBEnv))
	if err != nil {
		fmt.Println("CHILD-ERROR open store:", err)
		return 3
	}
	defer func() { _ = store.Close() }()
	a := New(soakProvider{}, soakExecutor{logPath: os.Getenv(soakLogEnv)}, store, Config{
		MaxIterations: 20, MaxTokens: 1_000_000, Timeout: time.Minute, LeaseTTL: 2 * time.Second, MaxRunAttempts: 1000,
		Completion: CompletionPolicy{Enabled: false, MutatingTools: []string{"commit"}},
	})
	runID := os.Getenv(soakRunEnv)
	// A killed predecessor's lease is honored until it lapses. Wait for that
	// here, in-process, so the reclaim figure the parent asserts on is the
	// lease window alone: a fresh -race test binary plus a SQLite open on a
	// loaded host costs seconds, and measured from the parent that start-up
	// would be blamed on the lease.
	if waited := waitForLeaseLapse(ctx, store, runID); waited > 0 {
		fmt.Printf("CHILD-RECLAIM-MS %d\n", waited.Milliseconds())
	}
	// Readiness marker: the store is open and the run is claimable, so the
	// parent's kill timer starts now and lands inside the run, not before it.
	_ = os.WriteFile(os.Getenv(soakDBEnv)+".ready", []byte("ready"), 0o600) // #nosec G703 -- path under the parent test's TempDir
	var res *RunResult
	if _, gerr := store.GetRun(ctx, runID); errors.Is(gerr, ErrRunNotFound) {
		sess, serr := store.CreateSession(ctx, "soak", nil)
		if serr != nil {
			fmt.Println("CHILD-ERROR session:", serr)
			return 3
		}
		res, err = a.RunDurable(ctx, runID, sess.ID, "run the soak", nil)
	} else {
		res, err = a.Resume(ctx, runID)
	}
	switch {
	case err == nil:
		fmt.Println("CHILD-DONE completed")
	case errors.Is(err, ErrInterruptedMutation):
		fmt.Println("CHILD-DONE needs_human")
	case errors.Is(err, ErrLeaseHeld):
		// Cannot happen after waitForLeaseLapse unless a second live worker
		// exists; the parent treats it as a failure.
		fmt.Println("CHILD-LEASE-HELD")
	case errors.Is(err, ErrLeaseLost), errors.Is(err, context.Canceled), strings.Contains(err.Error(), "context canceled"):
		// This worker's own renewal was refused (a tick delayed past the TTL
		// under load) and it stopped itself: the attempt is over, the run is
		// resumable. The parent treats it like a kill it did not send.
		fmt.Println("CHILD-CANCELED", err)
	case errors.Is(err, ErrRunFinished):
		// The predecessor finished the run and was killed before it could say
		// so (Resume is strict: an ended run always returns ErrRunFinished).
		// Report the stored status, which is what the parent verifies.
		run, gerr := store.GetRun(ctx, runID)
		switch {
		case gerr == nil && run.Status == RunCompleted:
			fmt.Println("CHILD-DONE completed")
		case gerr == nil && run.Status == RunNeedsHuman:
			fmt.Println("CHILD-DONE needs_human")
		default:
			fmt.Println("CHILD-DONE finished:", err, res != nil)
		}
	default:
		fmt.Println("CHILD-ERROR run:", err)
		return 4
	}
	return 0
}

// waitForLeaseLapse blocks while another owner's lease on the run is live and
// returns how long it waited (zero when the run was free at first sight).
func waitForLeaseLapse(ctx context.Context, store *SessionStore, runID string) time.Duration {
	start := time.Now()
	sawLive := false
	for {
		run, err := store.GetRun(ctx, runID)
		if err != nil || run.Status != RunRunning || run.Owner == "" || !run.LeaseUntil.After(time.Now()) {
			break
		}
		sawLive = true
		if time.Since(start) > 3*soakLeaseTTL {
			fmt.Println("CHILD-ERROR lease never lapsed:", run.Owner, run.LeaseUntil)
			os.Exit(5)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawLive {
		return 0
	}
	return time.Since(start)
}

// parseReclaim extracts the child's lease-wait report, if it printed one.
func parseReclaim(text string) (time.Duration, bool) {
	i := strings.Index(text, "CHILD-RECLAIM-MS ")
	if i < 0 {
		return 0, false
	}
	var ms int64
	if _, err := fmt.Sscanf(text[i:], "CHILD-RECLAIM-MS %d", &ms); err != nil {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}

type soakOutcome struct {
	earlyOnly   bool // kill policy: only while the commit cannot have started
	selfCancels int  // attempts the child ended itself after losing its lease
	status      string
	spawns      int
	kills       int
	midRun      int // kills that landed after the child had produced an effect
	leaseWaits  int // resumes refused because the dead worker's lease was still live
	maxReclaim  time.Duration
	effects     []string
	needsHum    bool
}

// soakLeaseTTL mirrors the child's LeaseTTL; the exit tier is reclaim < 2x TTL.
const soakLeaseTTL = 2 * time.Second

// soakMaxKills bounds the kills per round so a round always terminates; the
// doctor harness lowers it to fit its time budget.
func soakMaxKills() int {
	if v := os.Getenv("HLXN_SOAK_MAX_KILLS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 8
}

// soakEarlyOnly picks the round's kill policy. Odd rounds kill only while the
// run has fewer than half its lookups recorded, so the commit is never the
// interrupted step and the round MUST complete through resume. Even rounds
// kill anywhere, so the commit's unknown-outcome stop is exercised too. A
// commit's fsync dominates the run's wall time on this host, which is why
// unrestricted kills alone would show the needs_human path almost every time
// and the completed path almost never.
func soakEarlyOnly(round int) bool { return round%2 == 1 }

// waitReady blocks until the child has opened its store (the marker file),
// so the kill timer measures run time, not SQLite open time on a slow host.
func waitReady(marker string, done <-chan error) bool {
	deadline := time.After(2 * time.Minute) // a -race binary start plus a SQLite open on a loaded host
	for {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
		select {
		case <-done:
			return false
		case <-deadline:
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// waitEffects blocks until the effect log holds at least n lines or the child
// exits; it returns false when the child exited first.
func waitEffects(logPath string, n int, done <-chan error) bool {
	deadline := time.After(2 * time.Minute)
	for {
		if len(soakLogLines(logPath)) >= n {
			return true
		}
		select {
		case <-done:
			return false
		case <-deadline:
			return false
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// runSoakRound spawns/kills children until one reports a terminal state.
func runSoakRound(t *testing.T, rng *rand.Rand, dir string, round int) soakOutcome {
	t.Helper()
	db := filepath.Join(dir, fmt.Sprintf("soak-%d.db", round))
	logPath := filepath.Join(dir, fmt.Sprintf("soak-%d.log", round))
	runID := fmt.Sprintf("soak-run-%d", round)
	out := soakOutcome{earlyOnly: soakEarlyOnly(round)}
	maxKills := soakMaxKills()
	for attempt := 0; attempt < maxKills+40; attempt++ {
		_ = os.Remove(db + ".ready")
		cmd := exec.Command(os.Args[0], "-test.run=^TestSoakChildProcess$", "-test.v") //nolint:gosec // the test binary re-executing itself
		cmd.Env = append(os.Environ(), soakChildEnv+"=1", soakDBEnv+"="+db, soakLogEnv+"="+logPath, soakRunEnv+"="+runID)
		var stdout strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stdout
		require.NoError(t, cmd.Start())
		out.spawns++
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		killed := false
		if attempt < maxKills && waitReady(db+".ready", done) {
			// The run is live. Kill on PROGRESS, not on a clock: wait until the
			// run has logged one or two more effects than it had, then a few
			// milliseconds of jitter, and SIGKILL. That lands the kill in the
			// window that matters (a tool has run, its outcome may not be
			// recorded yet) on any substrate, fast or slow; a wall-clock timer
			// mostly missed the run on a RAM-backed disk.
			effectsBefore := len(soakLogLines(logPath))
			target := effectsBefore + 1 + rng.Intn(2)
			if target > soakLookups+1 {
				target = soakLookups + 1
			}
			if effectsBefore >= soakLookups+1 || (out.earlyOnly && target >= soakLookups/2) {
				<-done // nothing left to interrupt under this policy: let it finish
			} else if waitEffects(logPath, target, done) {
				time.Sleep(time.Duration(rng.Intn(20)) * time.Millisecond)
				_ = cmd.Process.Signal(syscall.SIGKILL)
				killed = true
				<-done
				if len(soakLogLines(logPath)) > effectsBefore {
					out.midRun++
				}
			}
		} else {
			<-done
		}
		text := stdout.String()
		if d, ok := parseReclaim(text); ok {
			out.leaseWaits++
			if d > out.maxReclaim {
				out.maxReclaim = d
			}
		}
		if killed {
			out.kills++
			continue
		}
		if strings.Contains(text, "CHILD-CANCELED") {
			out.selfCancels++
			continue
		}
		require.NotContains(t, text, "CHILD-LEASE-HELD", "round %d: the run was still leased after the child waited for the lease to lapse", round)
		switch {
		case strings.Contains(text, "CHILD-DONE completed"):
			out.status = "completed"
		case strings.Contains(text, "CHILD-DONE needs_human"):
			out.status = "needs_human"
			out.needsHum = true
		default:
			t.Fatalf("round %d: child ended without a terminal verdict:\n%s", round, text)
		}
		break
	}
	out.effects = soakLogLines(logPath)
	require.NotEmpty(t, out.status, "round %d never reached a terminal state", round)

	// Invariants from the durable state.
	ctx := context.Background()
	store, err := NewSessionStore(ctx, db)
	require.NoError(t, err)
	defer func() { _ = store.Close() }()
	run, err := store.GetRun(ctx, runID)
	require.NoError(t, err)
	steps, err := store.ListSteps(ctx, runID)
	require.NoError(t, err)
	interrupted, err := store.ListInterruptedRuns(ctx)
	require.NoError(t, err)
	assert.Empty(t, interrupted, "round %d: a terminal run must not be listed as interrupted", round)

	seen := map[string]int{}
	for _, e := range out.effects {
		seen[e]++
	}
	for id, n := range seen {
		assert.Equal(t, 1, n, "round %d: effect %s recorded %d times (duplicated side effect)", round, id, n)
	}
	switch out.status {
	case "completed":
		assert.Equal(t, RunCompleted, run.Status)
		assert.Equal(t, "soak complete", run.FinalContent)
		for i := 1; i <= soakLookups; i++ {
			assert.Equal(t, 1, seen[fmt.Sprintf("lookup_%d", i)], "round %d: lookup_%d missing (lost work)", round, i)
		}
		assert.Equal(t, 1, seen["commit_1"], "round %d: the commit must have happened exactly once", round)
		require.Len(t, steps, soakLookups+1)
		for i := range steps {
			assert.Equal(t, StepDone, steps[i].Status, "round %d: step %s not closed", round, steps[i].ToolCallID)
		}
	case "needs_human":
		assert.Equal(t, RunNeedsHuman, run.Status)
		// The only way to stop for a human is the commit's unknown outcome.
		var commit *RunStep
		for i := range steps {
			if steps[i].ToolCallID == "commit_1" {
				commit = &steps[i]
			}
		}
		require.NotNil(t, commit, "round %d: needs_human without a commit step", round)
		assert.Equal(t, StepFailed, commit.Status)
		assert.LessOrEqual(t, seen["commit_1"], 1, "round %d: at most one commit effect", round)
		for i := 1; i <= soakLookups; i++ {
			assert.Equal(t, 1, seen[fmt.Sprintf("lookup_%d", i)], "round %d: lookup_%d missing before the commit", round, i)
		}
	}
	return out
}

// TestKillNineSoak is the sprint's exit-tier evidence: N rounds of a run
// SIGKILLed at random points and resumed by a fresh process, with the
// invariants checked from the durable state after every round.
func TestKillNineSoak(t *testing.T) {
	if testing.Short() {
		t.Skip("soak: skipped in -short")
	}
	rounds := 2
	if v := os.Getenv(soakRoundsEnv); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err)
		rounds = n
	}
	seed := time.Now().UnixNano()
	if v := os.Getenv(soakSeedEnv); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		require.NoError(t, err)
		seed = n
	}
	t.Logf("soak seed=%d rounds=%d (replay with %s=%d)", seed, rounds, soakSeedEnv, seed)
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // reproducible soak, not crypto

	dir := testDBDir(t) // RAM-backed: the children share these files
	completed, needsHuman, kills, midRun, spawns := 0, 0, 0, 0, 0
	for r := 1; r <= rounds; r++ {
		out := runSoakRound(t, rng, dir, r)
		kills += out.kills
		midRun += out.midRun
		spawns += out.spawns
		if out.status == "completed" {
			completed++
		} else {
			needsHuman++
		}
		policy := "kill-anywhere"
		if out.earlyOnly {
			policy = "kill-before-commit"
		}
		t.Logf("round %d (%s): %s after %d kills (%d mid-run, %d spawns, %d lease waits, %d self-cancels, reclaim<=%s), %d effects", r, policy, out.status, out.kills, out.midRun, out.spawns, out.leaseWaits, out.selfCancels, out.maxReclaim.Round(time.Millisecond), len(out.effects))
		assert.Less(t, out.maxReclaim, 2*soakLeaseTTL+time.Second, "round %d: a dead worker's run must be reclaimed within 2x the lease TTL", r)
		if out.earlyOnly {
			assert.Equal(t, "completed", out.status, "round %d: with no kill inside the commit the run must complete through resume", r)
		}
	}
	t.Logf("SOAK rounds=%d completed=%d needs_human=%d kills=%d mid_run_kills=%d spawns=%d", rounds, completed, needsHuman, kills, midRun, spawns)
	assert.Greater(t, kills, 0, "the soak must actually have killed something")
	assert.Greater(t, midRun, 0, "at least one kill must have interrupted a run that had already produced effects; otherwise resume was never exercised")
	assert.Equal(t, rounds, completed+needsHuman, "every round ends in a terminal state")
}
