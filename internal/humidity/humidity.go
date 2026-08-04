// Package humidity reads AMS relative humidity from the neighbouring MQTT
// exporter.
//
// Humidity is not in the Bambu cloud API at all. The bash implementation this
// replaces shelled out to an interactive Grafana CLI to read it back out of
// Grafana Cloud — a round trip that existed only because that script ran
// off-cluster. In-cluster the exporter is one HTTP hop away, which removes a
// service-account token and the "best effort, might be unavailable" hedging
// with it.
package humidity

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/common/expfmt"
)

// Reading is an AMS humidity sample.
type Reading struct {
	Percent   float64
	Available bool
}

// State classifies a reading against the warn and critical thresholds.
func (r Reading) State(warn, crit float64) string {
	switch {
	case !r.Available:
		return "unavailable"
	case r.Percent >= crit:
		return "critical"
	case r.Percent >= warn:
		return "warn"
	default:
		return "ok"
	}
}

// Client reads a single gauge from a Prometheus exposition endpoint.
type Client struct {
	URL    string
	Metric string
	HTTP   *http.Client
}

// New returns a Client. An empty url disables the read entirely, which is the
// correct behaviour outside the cluster.
func New(url, metric string) *Client {
	return &Client{
		URL:    url,
		Metric: metric,
		HTTP:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Read returns the maximum value of the configured metric across all AMS
// units. Max rather than mean: one wet unit is the thing worth knowing about,
// and averaging would hide it.
//
// Errors are returned rather than swallowed, but callers should treat this as
// best-effort — the sync is still useful without humidity.
func (c *Client) Read(ctx context.Context) (Reading, error) {
	if c.URL == "" {
		return Reading{}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return Reading{}, fmt.Errorf("humidity: build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Reading{}, fmt.Errorf("humidity: GET %s: %w", c.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Reading{}, fmt.Errorf("humidity: GET %s: status %d", c.URL, resp.StatusCode)
	}

	families, err := (&expfmt.TextParser{}).TextToMetricFamilies(resp.Body)
	if err != nil {
		return Reading{}, fmt.Errorf("humidity: parse exposition: %w", err)
	}
	fam, ok := families[c.Metric]
	if !ok {
		return Reading{}, fmt.Errorf("humidity: metric %q not present at %s", c.Metric, c.URL)
	}

	out := Reading{}
	for _, m := range fam.GetMetric() {
		var v float64
		switch {
		case m.GetGauge() != nil:
			v = m.GetGauge().GetValue()
		case m.GetUntyped() != nil:
			v = m.GetUntyped().GetValue()
		default:
			continue
		}
		// The exporter reports humidity as a negative value on some firmware;
		// magnitude is what matters.
		if v < 0 {
			v = -v
		}
		if !out.Available || v > out.Percent {
			out.Percent = v
			out.Available = true
		}
	}
	if !out.Available {
		return Reading{}, fmt.Errorf("humidity: metric %q had no readable samples", c.Metric)
	}
	return out, nil
}
