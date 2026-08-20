package httpapi

import (
	"testing"
	"time"

	"github.com/sanix-darker/git-ci/internal/webui"
)

func TestRunTelemetryFiltersAndBuildsDiscreteHistograms(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	runs := []webui.RunView{
		{ID: "recent-ok", ProjectName: "alpha", Status: "SUCCEEDED", CreatedUnix: now.Add(-time.Hour).Unix(), DurationSeconds: 8},
		{ID: "recent-fail", ProjectName: "alpha", Status: "FAILED", CreatedUnix: now.Add(-2 * time.Hour).Unix(), DurationSeconds: 70},
		{ID: "other", ProjectName: "beta", Status: "RUNNING", CreatedUnix: now.Add(-3 * time.Hour).Unix()},
		{ID: "old", ProjectName: "alpha", Status: "FAILED", CreatedUnix: now.Add(-48 * time.Hour).Unix(), DurationSeconds: 400},
	}
	filter := webui.RunFilterView{Range: "24h", Project: "alpha"}
	filtered := filterRunViews(runs, filter, now)
	if len(filtered) != 2 {
		t.Fatalf("filtered runs = %d, want 2", len(filtered))
	}
	telemetry := buildRunTelemetry(runs, filter, now)
	if telemetry.Total != 2 || telemetry.Succeeded != 1 || telemetry.Failed != 1 || telemetry.PassRate != "50%" {
		t.Fatalf("telemetry = %#v", telemetry)
	}
	if len(telemetry.Volume) != 12 || len(telemetry.Duration) != 5 {
		t.Fatalf("histogram sizes = %d/%d", len(telemetry.Volume), len(telemetry.Duration))
	}
	for _, bar := range append(telemetry.Volume, telemetry.Duration...) {
		if bar.Level < 0 || bar.Level > 10 {
			t.Fatalf("invalid histogram level: %#v", bar)
		}
	}
}

func TestBuildGraphRowsGroupsParallelDependencyLevels(t *testing.T) {
	jobs := []webui.RunJobView{
		{Key: "prepare", Name: "Prepare"},
		{Key: "lint", Name: "Lint"},
		{Key: "test", Name: "Test", DependencyKeys: []string{"prepare"}},
		{Key: "deploy", Name: "Deploy", DependencyKeys: []string{"test", "lint"}},
	}
	rows := buildGraphRows(jobs)
	if len(rows) != 3 || len(rows[0].Jobs) != 2 || len(rows[1].Jobs) != 1 || len(rows[2].Jobs) != 1 {
		t.Fatalf("graph rows = %#v", rows)
	}
	if rows[2].Jobs[0].Key != "deploy" {
		t.Fatalf("final graph job = %#v", rows[2].Jobs[0])
	}
}
