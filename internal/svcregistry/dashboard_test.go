package svcregistry

import (
	"encoding/json"
	"os"
	"testing"
)

// TestDashboardJSONIsValid asserts the bundled Grafana dashboard file is
// valid JSON and has the required title/uid. This is the smoke test
// that Grafana's "Import dashboard" UI will be the first to fail on
// when the file is malformed, so we catch it locally.
func TestDashboardJSONIsValid(t *testing.T) {
	const rel = "dashboards/svcregistry.json"
	data, err := os.ReadFile(rel)
	if err != nil {
		t.Skipf("dashboard not present in package dir: %v", err)
	}
	var d map[string]any
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatalf("dashboard JSON invalid: %v", err)
	}
	if d["title"] != "Helixon Service Registry (v3)" {
		t.Fatalf("dashboard title = %v, want Helixon Service Registry (v3)", d["title"])
	}
	if d["uid"] != "helixon-svcregistry-v3" {
		t.Fatalf("dashboard uid = %v, want helixon-svcregistry-v3", d["uid"])
	}
	panels, ok := d["panels"].([]any)
	if !ok || len(panels) < 3 {
		t.Fatalf("dashboard panels = %v, want >=3", d["panels"])
	}
}