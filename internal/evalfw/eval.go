// Package evalfw provides a minimal evaluation framework for agent performance
// measurement. It runs test suites of Cases, collects metrics (latency, accuracy,
// token usage), and produces a SuiteResult with a pass/fail/warn verdict.
package evalfw

import (
	"context"
	"time"
)

// Verdict represents the outcome of a case or suite evaluation.
type Verdict string

const (
	VerdictPass Verdict = "PASS"
	VerdictFail Verdict = "FAIL"
	VerdictWarn Verdict = "WARN"
)

// Case is a single evaluation scenario.
type Case struct {
	Name string
	Tags []string
	Fn   func(ctx context.Context) CaseResult
}

// CaseResult is the outcome of running a single Case.
type CaseResult struct {
	Name     string             `json:"name"`
	Verdict  Verdict            `json:"verdict"`
	Error    string             `json:"error,omitempty"`
	Metrics  map[string]float64 `json:"metrics,omitempty"`
	Duration time.Duration      `json:"duration"`
}

// Suite groups related Cases under a name.
type Suite struct {
	Name  string
	Cases []Case
}

// SuiteResult aggregates the outcomes of all Cases in a Suite.
type SuiteResult struct {
	Name       string        `json:"name"`
	TotalCases int           `json:"total_cases"`
	Passed     int           `json:"passed"`
	Failed     int           `json:"failed"`
	Warned     int           `json:"warned"`
	Verdict    Verdict       `json:"verdict"`
	Duration   time.Duration `json:"duration"`
	Cases      []CaseResult  `json:"cases"`
}

// RunnerConfig configures the evaluation runner.
type RunnerConfig struct {
	Timeout time.Duration
}

func (c RunnerConfig) withDefaults() RunnerConfig {
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	return c
}

// Runner executes evaluation suites.
type Runner struct {
	config RunnerConfig
}

// NewRunner creates a Runner with the given config.
func NewRunner(cfg RunnerConfig) *Runner {
	return &Runner{config: cfg.withDefaults()}
}

// RunSuite executes all Cases in the Suite sequentially, respecting the
// per-case context timeout derived from the Runner's config.
func (r *Runner) RunSuite(ctx context.Context, suite Suite) (*SuiteResult, error) {
	start := time.Now()
	result := &SuiteResult{
		Name:  suite.Name,
		Cases: make([]CaseResult, 0, len(suite.Cases)),
	}

	for _, c := range suite.Cases {
		caseCtx, cancel := context.WithTimeout(ctx, r.config.Timeout)
		caseStart := time.Now()
		cr := c.Fn(caseCtx)
		cr.Name = c.Name
		cr.Duration = time.Since(caseStart)
		cancel()

		result.Cases = append(result.Cases, cr)

		switch cr.Verdict {
		case VerdictPass:
			result.Passed++
		case VerdictFail:
			result.Failed++
		case VerdictWarn:
			result.Warned++
		}
	}

	result.TotalCases = len(suite.Cases)
	result.Duration = time.Since(start)
	result.Verdict = aggregateVerdict(result.Failed, result.Warned)

	return result, nil
}

func aggregateVerdict(failed, warned int) Verdict {
	if failed > 0 {
		return VerdictFail
	}
	if warned > 0 {
		return VerdictWarn
	}
	return VerdictPass
}
