package sync

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/chewrocca/bambu-sync/internal/bambu"
	"github.com/chewrocca/bambu-sync/internal/config"
	"github.com/chewrocca/bambu-sync/internal/metrics"
	"github.com/chewrocca/bambu-sync/internal/store"
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

// fastFixture stands up a Syncer whose client talks to a scripted API, and
// returns how many times the filament endpoint was hit — the fast poll's cost
// against an API with undocumented rate limits is worth asserting, not
// assuming.
func fastFixture(t *testing.T, filament string) (*Syncer, *prometheus.Registry, func() int) {
	t.Helper()

	var filHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.URL.Path, "/filament"):
			filHits++
			if filament == "" {
				http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, filament)
		case strings.Contains(req.URL.Path, "/tasks"):
			_, _ = io.WriteString(w, `{"hits":[{"id":9,"title":"live","status":1,"startTime":"2026-08-04T00:00:00Z"}]}`)
		default:
			http.Error(w, `{"error":"no route"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := prometheus.NewRegistry()
	return &Syncer{
		Cfg:     &config.Config{PrintInfoLimit: 20, TokenFile: tokenFile},
		Client:  bambu.New(srv.URL + "/v1"),
		Metrics: metrics.New(reg),
		Store:   store.New(""),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, reg, func() int { return filHits }
}

const twoSpools = `{"hits":[
	{"filamentName":"PLA Basic","filamentType":"PLA","color":"#000000FF","netWeight":900,"totalNetWeight":1000,"amsId":0,"slotId":1,"inPrinter":true},
	{"filamentName":"PLA Matte","filamentType":"PLA","color":"#123456FF","netWeight":400,"totalNetWeight":1000,"slotId":"","inPrinter":false}
]}`

// The point of the fast spool refresh: AMS slot allocation and remaining
// weight move between daily syncs, and a slot column a day stale is one
// nobody trusts. One extra unpaginated GET buys that.
func TestFastPollRefreshesSpools(t *testing.T) {
	s, reg, filHits := fastFixture(t, twoSpools)
	s.lastTasks = []bambu.Task{
		{AMSDetailMapping: []bambu.AMSDetail{{TargetColor: "000000FF", FilamentType: "PLA", Weight: 120}}},
	}

	if err := s.RunFast(context.Background()); err != nil {
		t.Fatalf("fast poll: %v", err)
	}

	if got := value(t, reg, "bambu_spools_registered", nil); got != 2 {
		t.Errorf("want 2 spools published, got %v", got)
	}
	if got := count(t, reg, "bambu_spool_remaining_percent"); got != 2 {
		t.Errorf("want 2 percent series, got %d", got)
	}
	// The slot is the whole reason this runs on the fast cadence.
	if got := value(t, reg, "bambu_spool_remaining_percent",
		map[string]string{"name": "PLA Basic", "ams_slot": "AMS2 0:1", "loaded": "true"}); got != 90 {
		t.Errorf("loaded spool must carry its live slot: want 90%%, got %v", got)
	}
	// Usage re-joined against the CACHED history, not refetched.
	if got := value(t, reg, "bambu_spool_used_grams", map[string]string{"name": "PLA Basic"}); got != 120 {
		t.Errorf("usage must re-join against cached history: want 120, got %v", got)
	}
	if n := filHits(); n != 1 {
		t.Errorf("fast poll must cost exactly one filament request, got %d", n)
	}
}

// The fast poll must not be able to hold a stale spool list alive, nor to
// invent one. With no full sync cached yet, every spool would report Used=0 —
// "never printed" rather than "not known yet" — so it publishes nothing and
// leaves the previous values standing.
func TestFastPollSkipsSpoolsWithoutCachedHistory(t *testing.T) {
	s, reg, filHits := fastFixture(t, twoSpools)

	if err := s.RunFast(context.Background()); err != nil {
		t.Fatalf("fast poll: %v", err)
	}
	if got := count(t, reg, "bambu_spool_remaining_percent"); got != 0 {
		t.Errorf("want no spool series without cached history, got %d", got)
	}
	if n := filHits(); n != 0 {
		t.Errorf("must not spend a request it cannot use, got %d", n)
	}
}

// A filament hiccup must not take down the current-print refresh that is the
// reason this poll exists — same best-effort contract as devices and humidity
// in the full sync. The failure has to be VISIBLE, though: counted against the
// endpoint that failed, not swallowed.
func TestFastPollSurvivesFilamentFailure(t *testing.T) {
	s, reg, _ := fastFixture(t, "") // filament 500s
	s.lastTasks = []bambu.Task{}

	if err := s.RunFast(context.Background()); err != nil {
		t.Fatalf("a filament failure must not fail the fast poll: %v", err)
	}
	if got := count(t, reg, "bambu_print_info"); got != 1 {
		t.Errorf("the print refresh must still land: want 1 series, got %d", got)
	}
	if got := value(t, reg, "bambu_api_errors_total", map[string]string{"endpoint": "filament"}); got != 1 {
		t.Errorf("the failure must be counted against its endpoint, got %v", got)
	}
}

// The narrow reset is the whole reason publishSpools is separable: refreshing
// spools must not delete the print, queue or device series the fast poll has
// no data to republish. This is the ResetPrintInfo mistake in the other
// direction.
func TestSpoolRefreshDoesNotClearOtherSeries(t *testing.T) {
	s, reg := fixture(t, 20)
	s.publish(fixtureSpools(), fixtureTasks(), nil, nil, 2, 1, 100, 6.94)

	before := count(t, reg, "bambu_print_info")
	if before == 0 {
		t.Fatal("setup: expected print series")
	}

	s.publishSpools(fixtureSpools()[:1])

	if got := count(t, reg, "bambu_print_info"); got != before {
		t.Errorf("spool refresh must leave print history alone: want %d, got %d", before, got)
	}
	// And the departed spools must actually depart.
	if got := count(t, reg, "bambu_spool_remaining_percent"); got != 1 {
		t.Errorf("want 1 spool series after refresh, got %d", got)
	}
}
