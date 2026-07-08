// report.go — Sprint 18 HelixonEval report types and scoring.
package helixoneval

import (
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// Report is the aggregate of Cases produced by a Registry run. The
// Report.Aggregate method is the second of the four spec'd TDD
// targets (Registry.Add, Registry.Run, Report.Aggregate, Report.Score).
type Report struct {
	GeneratedAt   time.Time           `json:"generated_at"`
	Sprint        string              `json:"sprint"`
	Cases         []Case              `json:"cases"`
	ModelStats    map[Model]ModelStat `json:"model_stats"`
	OverallScore  float64             `json:"overall_score"`
	Pass          bool                `json:"pass"`
	Threshold     float64             `json:"threshold"`
}

// ModelStat is the per-model aggregate: count, mean score, p50 steps.
type ModelStat struct {
	Count     int     `json:"count"`
	MeanScore float64 `json:"mean_score"`
	MedianSteps int   `json:"median_steps"`
	Completions int    `json:"completions"`
}

// Aggregate consumes the supplied Registry, groups Cases by Model, and
// fills ModelStats, OverallScore and Pass. Threshold is the minimum
// mean score (across all models) for the report to be considered a
// pass. Sprint 18 default threshold is 0.7.
func (r *Report) Aggregate(reg *Registry, sprint string, threshold float64) {
	if reg == nil {
		// Guard: an empty registry produces an empty report.
		r.Cases = nil
		r.ModelStats = map[Model]ModelStat{}
		r.OverallScore = 0
		r.Pass = false
		r.Sprint = sprint
		r.GeneratedAt = time.Now().UTC()
		r.Threshold = threshold
		return
	}
	cases := reg.casesInOrder()
	r.Cases = cases
	r.Sprint = sprint
	r.GeneratedAt = time.Now().UTC()
	r.Threshold = threshold
	r.ModelStats = make(map[Model]ModelStat, 4)

	byModel := make(map[Model][]Case)
	for _, c := range cases {
		byModel[c.Model] = append(byModel[c.Model], c)
	}
	for model, group := range byModel {
		r.ModelStats[model] = summarize(group)
	}
	// Overall: mean of all per-model mean scores.
	if len(r.ModelStats) == 0 {
		r.OverallScore = 0
		r.Pass = false
		return
	}
	var sum float64
	for _, s := range r.ModelStats {
		sum += s.MeanScore
	}
	r.OverallScore = sum / float64(len(r.ModelStats))
	r.Pass = r.OverallScore >= threshold
}

// casesInOrder is an internal helper that returns the registry's cases
// in insertion order with the registry's RLock held. Lives on
// Registry so Aggregate does not need to know about the order slice.
func (r *Registry) casesInOrder() []Case {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Case, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.cases[id])
	}
	return out
}

// summarize produces a ModelStat from a slice of Cases for the same Model.
func summarize(group []Case) ModelStat {
	if len(group) == 0 {
		return ModelStat{}
	}
	var sum float64
	steps := make([]int, 0, len(group))
	completed := 0
	for _, c := range group {
		sum += c.Score
		steps = append(steps, c.Steps)
		if c.TerminationReason == "completed" {
			completed++
		}
	}
	return ModelStat{
		Count:       len(group),
		MeanScore:   sum / float64(len(group)),
		MedianSteps: median(steps),
		Completions: completed,
	}
}

// median returns the integer median of the supplied slice. Even-length
// slices round down.
func median(steps []int) int {
	if len(steps) == 0 {
		return 0
	}
	sorted := append([]int(nil), steps...)
	sort.Ints(sorted)
	return sorted[len(sorted)/2]
}

// Score is a convenience wrapper around Aggregate that returns the
// OverallScore. Useful for one-line assertions in golden tests and
// the optional regression harness in the CLI.
func (r *Report) Score(reg *Registry, threshold float64) float64 {
	r.Aggregate(reg, "v16129", threshold)
	return r.OverallScore
}

// WriteText formats the report as a human-readable Markdown summary and
// writes it to w. Empty w defaults to os.Stdout.
func (r *Report) WriteText(w io.Writer) error {
	if w == nil {
		w = os.Stdout
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# HelixonEval Report — %s\n\n", r.Sprint))
	b.WriteString(fmt.Sprintf("Generated: %s\n\n", r.GeneratedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("Overall: %.3f (threshold %.2f) — %s\n\n",
		r.OverallScore, r.Threshold, passLabel(r.Pass)))
	b.WriteString("## Per-model\n\n")
	b.WriteString("| Model | Count | Mean | Median Steps | Completions |\n")
	b.WriteString("|---|---|---|---|---|\n")
	models := make([]Model, 0, len(r.ModelStats))
	for m := range r.ModelStats {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i] < models[j] })
	for _, m := range models {
		s := r.ModelStats[m]
		b.WriteString(fmt.Sprintf("| %s | %d | %.3f | %d | %d |\n",
			m, s.Count, s.MeanScore, s.MedianSteps, s.Completions))
	}
	b.WriteString("\n## Per-case\n\n")
	b.WriteString("| Case | Model | Score | Steps | Termination |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, c := range r.Cases {
		b.WriteString(fmt.Sprintf("| %s | %s | %.3f | %d | %s |\n",
			c.ID, c.Model, c.Score, c.Steps, c.TerminationReason))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// passLabel renders PASS/FAIL for the report.
func passLabel(p bool) string {
	if p {
		return "PASS"
	}
	return "FAIL"
}

// roundScore rounds a float64 to three decimal places (matches the
// canonical rounding used by all rubric dashboards).
func roundScore(v float64) float64 {
	return math.Round(v*1000) / 1000
}
