package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenObserveOperationalSignalContract(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(current), "..", "..", "observability", "openobserve", "gds-operational-signals.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		SchemaVersion  int    `json:"schema_version"`
		MinimumVersion string `json:"minimum_openobserve_version"`
		Stream         string `json:"stream_placeholder"`
		Dashboard      struct {
			Name, Description string
			Panels            []struct{ ID, Title, Signal, Query string } `json:"panels"`
		} `json:"dashboard"`
		Alerts []struct {
			ID, Severity, Operator, Query string
			Period                        int     `json:"period_minutes"`
			Threshold                     float64 `json:"threshold"`
		} `json:"alerts"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		t.Fatal(err)
	}
	if spec.SchemaVersion != 1 || spec.MinimumVersion != "0.90.3" || spec.Stream != "${GDS_OTEL_STREAM}" || len(spec.Dashboard.Panels) < 6 || len(spec.Alerts) < 4 {
		t.Fatalf("incomplete OpenObserve contract: %+v", spec)
	}
	seen := map[string]bool{}
	for _, panel := range spec.Dashboard.Panels {
		if panel.ID == "" || seen[panel.ID] || (panel.Signal != "logs" && panel.Signal != "metrics" && panel.Signal != "traces") ||
			!strings.HasPrefix(panel.Query, "SELECT ") || !strings.Contains(panel.Query, "${GDS_OTEL_STREAM}") || strings.Contains(panel.Query, ";") {
			t.Fatalf("unsafe dashboard panel: %+v", panel)
		}
		seen[panel.ID] = true
	}
	for _, alert := range spec.Alerts {
		if alert.ID == "" || seen[alert.ID] || alert.Period < 5 || alert.Period > 60 || alert.Threshold != 0 || alert.Operator != "greater-than" ||
			(alert.Severity != "high" && alert.Severity != "critical") || !strings.HasPrefix(alert.Query, "SELECT ") ||
			!strings.Contains(alert.Query, "${GDS_OTEL_STREAM}") || strings.Contains(alert.Query, ";") {
			t.Fatalf("unsafe alert: %+v", alert)
		}
		seen[alert.ID] = true
	}
}
