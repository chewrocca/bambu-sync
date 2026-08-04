// Package config loads runtime configuration from the environment.
//
// Nothing site-specific is compiled in. This repository is public; printer
// serials, Grafana hostnames, Slack channels and account identifiers are
// deployment data, not source.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the full runtime configuration.
type Config struct {
	// TokenFile is the path to the Bambu bearer token. It is a FILE, not a
	// value, because the process is long-lived and the token rotates roughly
	// quarterly: mounting the Secret as a volume and re-reading each cycle
	// means a rotation lands without a pod restart.
	TokenFile string

	// TokenExpiryFile holds the token's expiry date (YYYY-MM-DD). The token is
	// an opaque bearer string, not a JWT, so the expiry cannot be derived from
	// it — it comes from the login response's expiresIn and must be stored
	// alongside.
	TokenExpiryFile string

	BaseURL string

	// SlackWebhookFile is the incoming-webhook URL, again as a file.
	SlackWebhookFile string
	SlackEnabled     bool
	SlackUsername    string
	SlackIconEmoji   string

	// HumidityURL is the neighbouring MQTT exporter's /metrics endpoint.
	// Humidity is not in the cloud API; reading the exporter directly over
	// cluster networking avoids a Grafana round-trip and a read token.
	HumidityURL    string
	HumidityMetric string

	FullInterval time.Duration
	FastInterval time.Duration
	DailyAt      string // "HH:MM" local, for the full sync
	Location     *time.Location

	ListenAddr string

	LowGrams      float64
	HumidityWarn  float64
	HumidityCrit  float64
	FilterHours   float64
	TokenWarnDays int

	HistoryLimit   int
	FavoritesLimit int
	PrintInfoLimit int

	StoreURL string
}

// Load reads configuration from the environment, applying defaults that suit
// a single-printer deployment.
func Load() (*Config, error) {
	c := &Config{
		TokenFile:       env("BAMBU_TOKEN_FILE", "/etc/bambu/token"),
		TokenExpiryFile: env("BAMBU_TOKEN_EXPIRY_FILE", "/etc/bambu/expires"),
		BaseURL:         env("BAMBU_API_BASE_URL", "https://api.bambulab.com/v1"),

		SlackWebhookFile: env("SLACK_WEBHOOK_FILE", "/etc/bambu/slack-webhook"),
		SlackUsername:    env("SLACK_USERNAME", "Bambu Sync"),
		SlackIconEmoji:   env("SLACK_ICON_EMOJI", ":printer:"),

		HumidityURL:    env("HUMIDITY_METRICS_URL", ""),
		HumidityMetric: env("HUMIDITY_METRIC_NAME", "bambulab_ams_unit_humidity"),

		DailyAt:    env("SYNC_DAILY_AT", "07:00"),
		ListenAddr: env("LISTEN_ADDR", ":9110"),
		StoreURL:   env("BAMBU_STORE_URL", "https://us.store.bambulab.com"),
	}

	var err error
	// Slack alerting ships DISABLED. Absence of the webhook would be an
	// ambiguous signal — it cannot distinguish "deliberately off" from
	// "misconfigured" — so this is explicit.
	if c.SlackEnabled, err = envBool("SLACK_ALERTS_ENABLED", false); err != nil {
		return nil, err
	}
	if c.FastInterval, err = envDuration("SYNC_FAST_INTERVAL", 30*time.Minute); err != nil {
		return nil, err
	}
	if c.FullInterval, err = envDuration("SYNC_FULL_INTERVAL", 24*time.Hour); err != nil {
		return nil, err
	}
	if c.LowGrams, err = envFloat("LOW_GRAMS", 250); err != nil {
		return nil, err
	}
	// Thresholds sit deliberately above Bambu's stated <20% RH target: the AMS
	// reads ~21% in normal operation, so alerting at 20% would fire daily for
	// a condition that is fine. Bambu's own gradient is dry for 2-7 days at
	// 20% RH, degrading within 2-12 hours at 55%.
	if c.HumidityWarn, err = envFloat("HUMIDITY_WARN", 30); err != nil {
		return nil, err
	}
	if c.HumidityCrit, err = envFloat("HUMIDITY_CRIT", 45); err != nil {
		return nil, err
	}
	if c.FilterHours, err = envFloat("FILTER_INTERVAL_HOURS", 1440); err != nil {
		return nil, err
	}
	if c.TokenWarnDays, err = envInt("TOKEN_WARN_DAYS", 14); err != nil {
		return nil, err
	}
	if c.HistoryLimit, err = envInt("HISTORY_LIMIT", 100); err != nil {
		return nil, err
	}
	// Capped deliberately. Every favourite is a distinct label set and the
	// list only grows, so an uncapped version leaks cardinality forever.
	if c.FavoritesLimit, err = envInt("FAVORITES_LIMIT", 25); err != nil {
		return nil, err
	}
	// Same reasoning: print history only grows.
	if c.PrintInfoLimit, err = envInt("PRINT_INFO_LIMIT", 20); err != nil {
		return nil, err
	}

	tz := env("TZ", "UTC")
	if c.Location, err = time.LoadLocation(tz); err != nil {
		// distroless/static ships no tzdata; the binary embeds it via
		// _ "time/tzdata". If this fails the embed was dropped.
		return nil, fmt.Errorf("config: load timezone %q (is time/tzdata still imported?): %w", tz, err)
	}
	if _, err := time.ParseInLocation("15:04", c.DailyAt, c.Location); err != nil {
		return nil, fmt.Errorf("config: SYNC_DAILY_AT %q must be HH:MM: %w", c.DailyAt, err)
	}

	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config: %s=%q is not a boolean: %w", key, v, err)
	}
	return b, nil
}

func envFloat(key string, def float64) (float64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q is not a number: %w", key, v, err)
	}
	return f, nil
}

func envInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q is not an integer: %w", key, v, err)
	}
	return n, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q is not a duration: %w", key, v, err)
	}
	return d, nil
}

// ReadSecret reads a secret from a file and trims whitespace. Secrets are read
// per-cycle rather than cached so that an External Secrets rotation is picked
// up without a restart.
func ReadSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("config: read secret %s: %w", path, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("config: secret %s is empty", path)
	}
	return s, nil
}
