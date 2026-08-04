package sync

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chewrocca/bambu-sync/internal/config"
	"github.com/chewrocca/bambu-sync/internal/humidity"
	"github.com/chewrocca/bambu-sync/internal/stock"
)

func testSyncer(t *testing.T, expiry string) *Syncer {
	t.Helper()
	dir := t.TempDir()
	expiryFile := filepath.Join(dir, "expires")
	if expiry != "" {
		if err := os.WriteFile(expiryFile, []byte(expiry), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &Syncer{
		Cfg: &config.Config{
			TokenExpiryFile: expiryFile,
			Location:        time.UTC,
			LowGrams:        250,
			HumidityWarn:    30,
			HumidityCrit:    45,
			FilterHours:     1440,
			TokenWarnDays:   14,
			StoreURL:        "https://example.invalid/store",
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// Silence is the normal state. Nothing actionable must produce no message at
// all — an "all clear" every morning trains you to ignore the channel.
func TestBuildAlertsSilentWhenNothingWrong(t *testing.T) {
	s := testSyncer(t, "")
	got := s.buildAlerts(nil, humidity.Reading{Percent: 21, Available: true}, "ok", 0, 5)
	if len(got) != 0 {
		t.Errorf("want silence, got %d alerts: %+v", len(got), got)
	}
}

func TestBuildAlertsLowFilamentNamesEverySpoolAndLinksTheStore(t *testing.T) {
	s := testSyncer(t, "")
	low := []stock.Spool{
		{Name: "PLA Basic", Material: "PLA", Left: 200},
		{Name: "PETG Basic", Material: "PETG", Left: 120},
	}
	got := s.buildAlerts(low, humidity.Reading{}, "unavailable", 0, 5)
	if len(got) != 1 {
		t.Fatalf("want 1 alert, got %d", len(got))
	}
	for _, want := range []string{"PLA Basic", "PETG Basic", "200", "120", s.Cfg.StoreURL} {
		if !strings.Contains(got[0].Title+got[0].Body, want) {
			t.Errorf("alert missing %q:\n%s\n%s", want, got[0].Title, got[0].Body)
		}
	}
}

// Thresholds sit deliberately ABOVE Bambu's stated <20% target, because the
// AMS reads ~21% in normal operation. Alerting at 21% would fire daily for a
// condition that is fine — the whole point of the chosen numbers.
func TestBuildAlertsHumidityThresholds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pct       float64
		state     string
		wantAlert bool
		wantWord  string
	}{
		{"normal operation is silent", 21, "ok", false, ""},
		{"just below warn is silent", 29, "ok", false, ""},
		{"at warn", 30, "warn", true, "elevated"},
		{"at critical", 45, "critical", true, "critical"},
		{"unavailable is silent", 0, "unavailable", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSyncer(t, "")
			got := s.buildAlerts(nil, humidity.Reading{Percent: tc.pct, Available: tc.state != "unavailable"}, tc.state, 0, 5)
			if tc.wantAlert && len(got) == 0 {
				t.Fatalf("want an alert at %v%% (%s)", tc.pct, tc.state)
			}
			if !tc.wantAlert && len(got) != 0 {
				t.Fatalf("want silence at %v%% (%s), got %+v", tc.pct, tc.state, got)
			}
			if tc.wantAlert && !strings.Contains(strings.ToLower(got[0].Title), tc.wantWord) {
				t.Errorf("want %q in title, got %q", tc.wantWord, got[0].Title)
			}
		})
	}
}

// A restart must be silent. The baseline is seeded on the first run after
// start WITHOUT alerting — otherwise every image bump would replay history as
// if it had just happened.
func TestBuildAlertsFailedPrintOnlyAfterSeeding(t *testing.T) {
	s := testSyncer(t, "")

	// First run after start: 3 historical failures, not yet seeded.
	if got := s.buildAlerts(nil, humidity.Reading{}, "unavailable", 3, 5); len(got) != 0 {
		t.Fatalf("unseeded run must not alert on historical failures, got %+v", got)
	}

	// Seed, as a completed run would.
	s.seeded, s.prevFailed = true, 3

	if got := s.buildAlerts(nil, humidity.Reading{}, "unavailable", 3, 5); len(got) != 0 {
		t.Errorf("unchanged failure count must not alert, got %+v", got)
	}

	got := s.buildAlerts(nil, humidity.Reading{}, "unavailable", 5, 5)
	if len(got) != 1 {
		t.Fatalf("want 1 alert for 2 new failures, got %d", len(got))
	}
	if !strings.Contains(got[0].Title, "2 new") {
		t.Errorf("want the NEW count, not the total, got %q", got[0].Title)
	}
}

func TestBuildAlertsFilterAt90Percent(t *testing.T) {
	s := testSyncer(t, "")
	if got := s.buildAlerts(nil, humidity.Reading{}, "unavailable", 0, 89.9); len(got) != 0 {
		t.Errorf("below 90%% must be silent, got %+v", got)
	}
	got := s.buildAlerts(nil, humidity.Reading{}, "unavailable", 0, 90)
	if len(got) != 1 || !strings.Contains(got[0].Title, "filter") {
		t.Fatalf("want a filter alert at 90%%, got %+v", got)
	}
}

// The predictive half of token alerting. The stored date can be wrong — a
// password change revokes early — so this precedes, rather than replaces,
// alerting on an actual 401.
func TestTokenExpiryAlert(t *testing.T) {
	day := func(d int) string {
		return time.Now().UTC().AddDate(0, 0, d).Format("2006-01-02")
	}

	t.Run("far future is silent", func(t *testing.T) {
		s := testSyncer(t, day(90))
		if _, ok := s.tokenExpiryAlert(); ok {
			t.Error("want silence 90 days out")
		}
	})

	t.Run("inside the warning window", func(t *testing.T) {
		s := testSyncer(t, day(10))
		a, ok := s.tokenExpiryAlert()
		if !ok {
			t.Fatal("want an alert 10 days out")
		}
		if !strings.Contains(a.Title, "expires in") {
			t.Errorf("want a forward-looking title, got %q", a.Title)
		}
		// The footgun: updating the token but not the date leaves this firing
		// forever against a stale value. The message must say so.
		if !strings.Contains(a.Body, "BOTH") {
			t.Errorf("body must warn to update both token and expiry, got %q", a.Body)
		}
	})

	t.Run("already expired reads as past tense", func(t *testing.T) {
		s := testSyncer(t, day(-3))
		a, ok := s.tokenExpiryAlert()
		if !ok {
			t.Fatal("want an alert after expiry")
		}
		if !strings.Contains(a.Title, "expired") {
			t.Errorf("want past tense once expired, got %q", a.Title)
		}
	})

	t.Run("missing or unparseable file is silent, not fatal", func(t *testing.T) {
		s := testSyncer(t, "")
		if _, ok := s.tokenExpiryAlert(); ok {
			t.Error("a missing expiry file must not alert")
		}
		s2 := testSyncer(t, "not-a-date")
		if _, ok := s2.tokenExpiryAlert(); ok {
			t.Error("an unparseable expiry must not alert")
		}
	})
}

// Model names are arbitrary user text from MakerWorld. Prometheus label values
// cannot carry a raw backslash, double quote or newline; replacing rather than
// escaping keeps the quoting unambiguous through one fewer layer.
func TestSanitiseStripsLabelBreakingCharacters(t *testing.T) {
	got := sanitise("a\"b\\c\nd\re")
	if strings.ContainsAny(got, "\"\\\n\r") {
		t.Errorf("sanitise left a label-breaking character: %q", got)
	}
	if want := "a b c d e"; got != want {
		t.Errorf("sanitise = %q, want %q", got, want)
	}
}
