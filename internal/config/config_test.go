package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The defaults are the deployment contract — a bad default here changes
// behaviour silently, with no config to point at when it goes wrong.
func TestLoadDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		got, want any
	}{
		{"LowGrams", c.LowGrams, 250.0},
		{"HumidityWarn", c.HumidityWarn, 30.0},
		{"HumidityCrit", c.HumidityCrit, 45.0},
		{"FilterHours", c.FilterHours, 1440.0},
		{"TokenWarnDays", c.TokenWarnDays, 14},
		{"FastInterval", c.FastInterval, 30 * time.Minute},
		{"DailyAt", c.DailyAt, "07:00"},
		{"BaseURL", c.BaseURL, "https://api.bambulab.com/v1"},
		// Cardinality caps, not display limits: every entry is a distinct
		// label set and both lists only grow.
		{"FavoritesLimit", c.FavoritesLimit, 25},
		{"PrintInfoLimit", c.PrintInfoLimit, 20},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	// Alerting ships OFF. Enabling it is a deliberate act, not a side effect
	// of a webhook happening to be present.
	if c.SlackEnabled {
		t.Error("SLACK_ALERTS_ENABLED must default to false")
	}
}

// distroless/static carries no tzdata; the binary embeds it via
// _ "time/tzdata". If that import is ever dropped, a local-time schedule fails
// at runtime in the container but passes on a developer machine. This test
// fails in both places.
func TestLoadResolvesNamedTimezone(t *testing.T) {
	t.Setenv("TZ", "America/Chicago")
	c, err := Load()
	if err != nil {
		t.Fatalf("named timezone must resolve (is time/tzdata still imported?): %v", err)
	}
	if c.Location.String() != "America/Chicago" {
		t.Errorf("Location = %q, want America/Chicago", c.Location)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	for _, tc := range []struct{ key, val string }{
		{"SLACK_ALERTS_ENABLED", "yes-please"},
		{"LOW_GRAMS", "loads"},
		{"TOKEN_WARN_DAYS", "2.5"},
		{"SYNC_FAST_INTERVAL", "half an hour"},
		{"SYNC_DAILY_AT", "7am"},
		{"TZ", "Mars/Olympus_Mons"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			if _, err := Load(); err == nil {
				t.Errorf("%s=%q must be rejected, not silently defaulted", tc.key, tc.val)
			}
		})
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("LOW_GRAMS", "150")
	t.Setenv("SLACK_ALERTS_ENABLED", "true")
	t.Setenv("SYNC_FAST_INTERVAL", "15m")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.LowGrams != 150 || !c.SlackEnabled || c.FastInterval != 15*time.Minute {
		t.Errorf("overrides not applied: %v %v %v", c.LowGrams, c.SlackEnabled, c.FastInterval)
	}
}

// Secrets are read from files and trimmed. A trailing newline from `echo` or
// an editor must not end up in an Authorization header.
func TestReadSecretTrims(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("  abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSecret(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc123" {
		t.Errorf("ReadSecret = %q, want %q", got, "abc123")
	}
}

// An empty secret file is a misconfiguration, not an empty credential. Failing
// loudly beats sending "Bearer " and getting a confusing 401.
func TestReadSecretRejectsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("\n  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecret(p); err == nil {
		t.Error("an empty secret file must be an error")
	}
	if _, err := ReadSecret(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("a missing secret file must be an error")
	}
}

// pprof exposes goroutine stacks, heap contents and the process command line.
// This binary holds a full-account bearer token, so the default must be OFF
// and enabling it must be a deliberate act.
func TestPprofDefaultsOff(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.PprofEnabled {
		t.Error("ENABLE_PPROF must default to false")
	}

	t.Setenv("ENABLE_PPROF", "true")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.PprofEnabled {
		t.Error("ENABLE_PPROF=true should enable it")
	}
}
