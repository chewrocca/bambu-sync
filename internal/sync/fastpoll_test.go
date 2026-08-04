package sync

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/chewrocca/bambu-sync/internal/bambu"
	"github.com/chewrocca/bambu-sync/internal/config"
	"github.com/chewrocca/bambu-sync/internal/metrics"
)

func count(t *testing.T, reg *prometheus.Registry, name string) int {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return len(f.GetMetric())
		}
	}
	return 0
}

// Regression, found in production. The fast poll fetches ONE task at limit=1.
// It used to Reset the whole print vector first, so 30 minutes after every
// full sync the history collapsed from 20 rows to 1 — and the Recent prints
// panel with it. It must delete only the stale status="running" series.
func TestFastPollKeepsHistoryAndClearsStaleRunning(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := &Syncer{
		Cfg:     &config.Config{PrintInfoLimit: 20, StoreURL: "https://example.invalid"},
		Metrics: metrics.New(reg),
	}

	// A full sync: 3 finished prints plus one currently running.
	s.publishPrints([]bambu.Task{
		{ID: 1, Title: "a", Status: 2, StartTime: "2026-08-01T00:00:00Z"},
		{ID: 2, Title: "b", Status: 2, StartTime: "2026-08-02T00:00:00Z"},
		{ID: 3, Title: "c", Status: 3, StartTime: "2026-08-03T00:00:00Z"},
		{ID: 4, Title: "d", Status: 1, StartTime: "2026-08-04T00:00:00Z"},
	})
	if got := count(t, reg, "bambu_print_info"); got != 4 {
		t.Fatalf("setup: want 4 series, got %d", got)
	}

	// What the fast poll does: drop running, publish the one task it fetched.
	running := prometheus.Labels{"status": "running"}
	s.Metrics.PrintInfo.DeletePartialMatch(running)
	s.Metrics.PrintDurationSeconds.DeletePartialMatch(running)
	s.publishPrints([]bambu.Task{
		{ID: 4, Title: "d", Status: 2, StartTime: "2026-08-04T00:00:00Z"}, // now finished
	})

	// 3 finished from the full sync + the now-finished task 4.
	if got := count(t, reg, "bambu_print_info"); got != 4 {
		t.Errorf("fast poll must preserve history: want 4 series, got %d", got)
	}
	if got := count(t, reg, "bambu_print_duration_seconds"); got != 4 {
		t.Errorf("duration must track print_info: want 4 series, got %d", got)
	}

	// The stale running series for task 4 must be gone, or a finished print
	// keeps showing as current.
	families, _ := reg.Gather()
	for _, f := range families {
		if f.GetName() != "bambu_print_info" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "status" && l.GetValue() == "running" {
					t.Error("a stale status=running series survived the fast poll")
				}
			}
		}
	}
}
