package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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

// The defaults TestLoadDefaults did not cover. These are not decoration:
// the three secret paths are the container contract -- they must match the
// volumeMount in deploy/example.yaml and in the GitOps manifests, and a
// silent change to one strands a running pod on a file that is not there.
func TestLoadStringDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, got, want string }{
		// Secret paths. Files, not env values, so a rotated Kubernetes Secret
		// is picked up without a restart.
		{"TokenFile", c.TokenFile, "/etc/bambu/token"},
		{"TokenExpiryFile", c.TokenExpiryFile, "/etc/bambu/expires"},
		{"SlackWebhookFile", c.SlackWebhookFile, "/etc/bambu/slack-webhook"},

		// The /v1 is mandatory; without it every endpoint 404s.
		{"BaseURL", c.BaseURL, "https://api.bambulab.com/v1"},

		// Regional storefront. The slugs are global, the host is not.
		{"StoreURL", c.StoreURL, "https://us.store.bambulab.com"},

		{"HumidityMetric", c.HumidityMetric, "bambulab_ams_unit_humidity"},
		{"ListenAddr", c.ListenAddr, ":9110"},
		{"SlackUsername", c.SlackUsername, "Bambu Sync"},
		{"SlackIconEmoji", c.SlackIconEmoji, ":printer:"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	// Humidity is opt-in: an unset URL disables the whole best-effort read
	// rather than defaulting to some guessed exporter address.
	if c.HumidityURL != "" {
		t.Errorf("HumidityURL must default to empty (disabled), got %q", c.HumidityURL)
	}
}

// HistoryLimit had NO assertion anywhere, despite being the field the
// documented bargain rests on: bambu_print_info is capped at 20 for
// cardinality, and the aggregates recover the discarded detail by running over
// the full fetched history.
//
// The limit is applied at FETCH time, so the publish tests cannot catch this —
// they assert that publish aggregates everything it is handed, not that it is
// handed 100. Shrink this default and every total silently drops while the
// whole suite stays green.
func TestHistoryLimitDefaultsWellAboveThePublishedCap(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HistoryLimit != 100 {
		t.Errorf("HISTORY_LIMIT = %d, want 100", c.HistoryLimit)
	}
	if c.HistoryLimit <= c.PrintInfoLimit {
		t.Errorf("history (%d) must exceed the published cap (%d), or the "+
			"aggregates add nothing the capped list does not already show",
			c.HistoryLimit, c.PrintInfoLimit)
	}
	if c.FullInterval != 24*time.Hour {
		t.Errorf("SYNC_FULL_INTERVAL = %v, want 24h", c.FullInterval)
	}
}

// Every environment variable the code honours must appear in the README's
// configuration table. Three did not — HISTORY_LIMIT, SYNC_FULL_INTERVAL and
// BAMBU_STORE_URL — which makes them knobs you can only discover by reading
// the source. This test is the reason that cannot happen again.
func TestEveryEnvVarIsDocumented(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	// Matches env("X", …), envBool("X", …), envInt("X", …), and friends.
	re := regexp.MustCompile(`env(?:Bool|Float|Int|Duration)?\("([A-Z][A-Z0-9_]*)"`)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		seen[m[1]] = true
	}
	if len(seen) < 10 {
		t.Fatalf("only found %d env vars in config.go; the matcher is broken", len(seen))
	}

	var undocumented []string
	for name := range seen {
		if !strings.Contains(string(readme), name) {
			undocumented = append(undocumented, name)
		}
	}
	sort.Strings(undocumented)
	if len(undocumented) > 0 {
		t.Errorf("environment variables absent from README.md: %s",
			strings.Join(undocumented, ", "))
	}
}
