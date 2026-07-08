// Package main — helixon-slo-ack is the SLO ack/paging wiring CLI for the
// Helixon fleet agents. It polls Alertmanager, applies silences via the
// Alertmanager v2 API, and appends rows to an incidents NDJSON ledger.
//
// Authored: 2026-07-15 (v14513 Pair-5 Review).
//
// Usage:
//   helixon-slo-ack --alertmanager http://localhost:9093 \
//     --incidents session-handoffs/incidents.ndjson \
//     --ack-window 5m --sprint v14513
//
// Exit codes:
//   0  no firing P0 alerts
//   2  one or more P0 alerts were acked
//   3  alertmanager unreachable
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Silence is the subset of the Alertmanager v2 silence payload we need.
type Silence struct {
	Matchers []Matcher `json:"matchers"`
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
	CreatedBy string   `json:"createdBy"`
	Comment   string   `json:"comment"`
}

// Matcher is a single label equality match.
type Matcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

// SilenceResponse is the JSON Alertmanager returns from /api/v2/silences.
type SilenceResponse struct {
	SilenceID string `json:"silenceID"`
}

// Alert is a single entry in Alertmanager's GET /api/v2/alerts response.
type Alert struct {
	Labels map[string]string `json:"labels"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
}

// Incident is one row appended to incidents.ndjson.
type Incident struct {
	TS            string `json:"ts"`
	Alertname     string `json:"alertname"`
	Severity      string `json:"severity"`
	Sprint        string `json:"sprint"`
	AckWithinMin  int    `json:"ack_within_min"`
	RootCause     string `json:"root_cause,omitempty"`
	Runbook       string `json:"runbook,omitempty"`
	SilenceID     string `json:"silence_id"`
}

func main() {
	amURL := flag.String("alertmanager", "http://localhost:9093", "Alertmanager base URL")
	incidentsPath := flag.String("incidents", "session-handoffs/incidents.ndjson", "incidents NDJSON ledger")
	sprintID := flag.String("sprint", "v14513", "sprint id (label value)")
	ackWindow := flag.Duration("ack-window", 5*time.Minute, "silence window for P0 acks")
	ackWindowP1 := flag.Duration("ack-window-p1", 15*time.Minute, "silence window for P1 acks")
	createdBy := flag.String("created-by", "helixon-slo-ack", "silence createdBy")
	dryRun := flag.Bool("dry-run", false, "do not POST silences or append ledger")
	flag.Parse()

	alerts, err := listAlerts(*amURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "alertmanager unreachable: %v\n", err)
		os.Exit(3)
	}

	p0 := filter(alerts, "page")
	p1 := filter(alerts, "notify")

	if len(p0) == 0 && len(p1) == 0 {
		fmt.Fprintf(os.Stdout, "no firing P0 or P1 alerts\n")
		os.Exit(0)
	}

	for _, a := range p0 {
		if err := ackAlert(*amURL, a, *ackWindow, *sprintID, *createdBy, *incidentsPath, *dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "ack %s failed: %v\n", a.Labels["alertname"], err)
		}
	}
	for _, a := range p1 {
		if err := ackAlert(*amURL, a, *ackWindowP1, *sprintID, *createdBy, *incidentsPath, *dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "ack %s failed: %v\n", a.Labels["alertname"], err)
		}
	}

	if len(p0) > 0 {
		fmt.Fprintf(os.Stdout, "acked %d P0 + %d P1 firing alerts\n", len(p0), len(p1))
		os.Exit(2)
	}
	fmt.Fprintf(os.Stdout, "acked %d P1 alerts (no P0)\n", len(p1))
	os.Exit(0)
}

func listAlerts(amURL string) ([]Alert, error) {
	resp, err := http.Get(amURL + "/api/v2/alerts?active=true&silenced=false")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/v2/alerts: status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var alerts []Alert
	if err := json.Unmarshal(body, &alerts); err != nil {
		return nil, fmt.Errorf("decode alerts: %w", err)
	}
	return alerts, nil
}

func filter(alerts []Alert, severity string) []Alert {
	var out []Alert
	for _, a := range alerts {
		if a.Status.State != "active" {
			continue
		}
		if a.Labels["severity"] == severity {
			out = append(out, a)
		}
	}
	return out
}

func ackAlert(amURL string, a Alert, window time.Duration, sprintID, createdBy, incidentsPath string, dryRun bool) error {
	alertname := a.Labels["alertname"]
	severity := a.Labels["severity"]
	if alertname == "" {
		return fmt.Errorf("alert has no alertname label")
	}

	now := time.Now().UTC()
	silence := Silence{
		Matchers: []Matcher{
			{Name: "alertname", Value: alertname, IsRegex: false, IsEqual: true},
			{Name: "severity", Value: severity, IsRegex: false, IsEqual: true},
		},
		StartsAt: now,
		EndsAt:   now.Add(window),
		CreatedBy: createdBy,
		Comment:   fmt.Sprintf("auto-ack by %s for sprint %s", createdBy, sprintID),
	}

	var silenceID string
	if !dryRun {
		body, _ := json.Marshal(silence)
		resp, err := http.Post(amURL+"/api/v2/silences", "application/json", bytes.NewReader(body))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("POST silence: status %d body %s", resp.StatusCode, string(body))
		}
		var sr SilenceResponse
		if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
			return fmt.Errorf("decode silence response: %w", err)
		}
		silenceID = sr.SilenceID
	} else {
		silenceID = "dry-run-no-id"
	}

	row := Incident{
		TS:           now.Format(time.RFC3339),
		Alertname:    alertname,
		Severity:     severity,
		Sprint:       sprintID,
		AckWithinMin: int(window.Minutes()),
		SilenceID:    silenceID,
	}
	if !dryRun {
		if err := appendNDJSON(incidentsPath, row); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stdout, "acked %s severity=%s silence=%s\n", alertname, severity, silenceID)
	return nil
}

func appendNDJSON(path string, row Incident) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(row)
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// URL helper kept around for the test suite.
func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

// Used by tests to assert ordering
func _sortAlertsByName(in []Alert) []Alert {
	out := append([]Alert{}, in...)
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if strings.Compare(out[i].Labels["alertname"], out[j].Labels["alertname"]) > 0 {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}