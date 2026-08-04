// Package sync fetches Bambu cloud state and publishes it as metrics.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/chewrocca/bambu-sync/internal/bambu"
	"github.com/chewrocca/bambu-sync/internal/config"
	"github.com/chewrocca/bambu-sync/internal/humidity"
	"github.com/chewrocca/bambu-sync/internal/metrics"
	"github.com/chewrocca/bambu-sync/internal/notify"
	"github.com/chewrocca/bambu-sync/internal/stock"
)

// Syncer holds the collaborators a run needs. It is deliberately a value with
// an explicit Run method rather than a package-level function, so a future
// HTTP trigger is a new caller rather than a rewrite.
type Syncer struct {
	Cfg      *config.Config
	Client   *bambu.Client
	Metrics  *metrics.Set
	Humidity *humidity.Client
	Slack    *notify.Slack
	Log      *slog.Logger

	// prevFailed is the previous run's failed-print count, held in memory.
	// This replaces the old on-disk state file: a restart simply re-seeds
	// without alerting, which is better than the documented behaviour it
	// replaces ("expect one spurious alert the first time").
	prevFailed int
	seeded     bool
}

// Options controls a single run.
type Options struct {
	// Alerting is false for startup and fast runs. Most alerts are STANDING
	// conditions — a spool at 200 g is low on every run until it is swapped —
	// so evaluating them on every cycle would re-ping on every restart and
	// every fast poll. Only the scheduled full run alerts.
	Alerting bool
}

// Run performs a full sync: fetch, publish metrics, and optionally alert.
//
// This is the seam. A CronJob, a ticker, or an HTTP handler are all just
// callers.
func (s *Syncer) Run(ctx context.Context, opts Options) error {
	start := time.Now()
	s.Metrics.LastRun.Set(float64(start.Unix()))

	err := s.run(ctx, opts)

	switch {
	case err == nil:
		s.Metrics.Up.Set(1)
		s.Metrics.LastExitCode.Set(0)
	case errors.Is(err, bambu.ErrTokenExpired):
		s.Metrics.Up.Set(0)
		s.Metrics.LastExitCode.Set(2)
	default:
		s.Metrics.Up.Set(0)
		s.Metrics.LastExitCode.Set(1)
	}
	return err
}

func (s *Syncer) run(ctx context.Context, opts Options) error {
	// Read secrets per-cycle. The pod is long-lived and the token rotates
	// roughly quarterly; re-reading means an External Secrets refresh lands
	// without a restart.
	token, err := config.ReadSecret(s.Cfg.TokenFile)
	if err != nil {
		return err
	}
	s.publishTokenExpiry()

	tasks, err := s.Client.Tasks(ctx, token, s.Cfg.HistoryLimit)
	if err != nil {
		return s.handleFetchErr(ctx, "tasks", err, opts)
	}
	fil, err := s.Client.Filament(ctx, token)
	if err != nil {
		return s.handleFetchErr(ctx, "filament", err, opts)
	}
	favs, err := s.Client.Favorites(ctx, token, s.Cfg.FavoritesLimit)
	if err != nil {
		return s.handleFetchErr(ctx, "favorites", err, opts)
	}

	spools := stock.Join(fil.Hits, tasks.Hits)
	low := stock.Low(spools, s.Cfg.LowGrams)
	succeeded, failed := stock.PrintCounts(tasks.Hits)
	filterHrs, filterPct := stock.FilterHours(tasks.Hits, s.Cfg.FilterHours)

	// Humidity is best-effort by design: the sync is still useful without it.
	hum, humErr := s.Humidity.Read(ctx)
	if humErr != nil {
		s.Log.Warn("humidity unavailable", "err", humErr)
	}
	humState := hum.State(s.Cfg.HumidityWarn, s.Cfg.HumidityCrit)

	s.publish(spools, tasks.Hits, favs.Hits, succeeded, failed, filterHrs, filterPct)

	var alerts []notify.Alert
	if opts.Alerting {
		alerts = s.buildAlerts(low, hum, humState, failed, filterPct)
	}

	// Seed the failure baseline without alerting on the first run after start.
	if !s.seeded {
		s.seeded = true
	}
	s.prevFailed = failed

	// One structured record per run. The discriminator lets Loki isolate it
	// from ordinary log lines.
	s.Log.Info("sync complete",
		"event", "run_summary",
		"spools_total", len(spools),
		"spools_low", len(low),
		"prints_succeeded", succeeded,
		"prints_failed", failed,
		"filter_hours", int(filterHrs),
		"filter_percent", filterPct,
		"humidity_percent", hum.Percent,
		"humidity_state", humState,
		"favorites", len(favs.Hits),
		"alerts_fired", len(alerts),
		"alerting_enabled", opts.Alerting,
	)

	if len(alerts) > 0 && s.Cfg.SlackEnabled {
		hook, err := config.ReadSecret(s.Cfg.SlackWebhookFile)
		if err != nil {
			s.Log.Error("slack webhook unreadable", "err", err)
		} else if err := s.Slack.Post(ctx, hook, alerts); err != nil {
			s.Log.Error("slack post failed", "err", err)
		}
	}
	return nil
}

// handleFetchErr alerts on an expired token before returning. On the Mac the
// scheduled task posted this, because the script exited first; in Kubernetes
// there is no wrapper, so the service must announce its own death.
func (s *Syncer) handleFetchErr(ctx context.Context, what string, err error, opts Options) error {
	if errors.Is(err, bambu.ErrTokenExpired) && opts.Alerting && s.Cfg.SlackEnabled {
		if hook, rerr := config.ReadSecret(s.Cfg.SlackWebhookFile); rerr == nil {
			_ = s.Slack.Post(ctx, hook, []notify.Alert{{
				Title: "*Bambu token expired or revoked*",
				Body: "The cloud token no longer authenticates, so print history, " +
					"filament stock and favourites have stopped updating.\n" +
					"Re-authenticate and update both the token and its expiry date.",
			}})
		}
	}
	return fmt.Errorf("sync: fetch %s: %w", what, err)
}

// RunFast refreshes only the current print. It exists because the cloud API is
// the only source of the MakerWorld model identity — the MQTT exporter's
// subtask_name is the slicer PROFILE, not the model — and a daily cadence
// would leave a finished print showing as running for up to a day.
//
// It deliberately touches neither bambu_sync_up nor the failure diff: a job
// running every 30 minutes must not be able to hold the health signal green
// through a total failure of the daily sync.
func (s *Syncer) RunFast(ctx context.Context) error {
	token, err := config.ReadSecret(s.Cfg.TokenFile)
	if err != nil {
		return err
	}
	tasks, err := s.Client.Tasks(ctx, token, 1)
	if err != nil {
		return fmt.Errorf("sync: fast poll: %w", err)
	}
	s.Metrics.ResetPrintInfo()
	s.publishPrints(tasks.Hits)
	s.Metrics.ProbeLastRun.Set(float64(time.Now().Unix()))
	return nil
}

func (s *Syncer) publishTokenExpiry() {
	raw, err := config.ReadSecret(s.Cfg.TokenExpiryFile)
	if err != nil {
		// Not fatal: the token still works, we just cannot warn ahead.
		s.Log.Warn("token expiry unavailable", "err", err)
		return
	}
	t, err := time.ParseInLocation("2006-01-02", raw, s.Cfg.Location)
	if err != nil {
		s.Log.Warn("token expiry unparseable", "value", raw, "err", err)
		return
	}
	s.Metrics.TokenExpires.Set(float64(t.Unix()))
}

func (s *Syncer) publish(spools []stock.Spool, tasks []bambu.Task, favs []bambu.Design,
	succeeded, failed int, filterHrs, filterPct float64) {

	s.Metrics.ResetDynamic()

	for _, sp := range spools {
		s.Metrics.SpoolRemainingGrams.WithLabelValues(
			sp.Name, sp.Material, sp.Color, s.Cfg.StoreURL,
		).Set(sp.Left)

		s.Metrics.SpoolRemainingPercent.WithLabelValues(
			sp.Name, sp.Material, sp.Color, s.Cfg.StoreURL,
			strconv.FormatFloat(sp.Left, 'f', -1, 64),
			strconv.FormatFloat(sp.Capacity, 'f', -1, 64),
			strconv.FormatBool(sp.Loaded),
			sp.SlotLabel(),
		).Set(sp.Percent)
	}

	s.publishPrints(tasks)

	for _, d := range favs {
		s.Metrics.QueueItem.WithLabelValues(
			sanitise(d.Title),
			fmt.Sprintf("https://makerworld.com/en/models/%d", d.ID),
			sanitise(creatorOr(d, "?")),
		).Set(float64(d.PrintCount))
	}

	s.Metrics.Favorites.Set(float64(len(favs)))
	s.Metrics.PrintsSucceeded.Set(float64(succeeded))
	s.Metrics.PrintsFailed.Set(float64(failed))
	s.Metrics.FilterHours.Set(filterHrs)
	s.Metrics.FilterPercent.Set(filterPct)
}

func (s *Syncer) publishPrints(tasks []bambu.Task) {
	sorted := append([]bambu.Task(nil), tasks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].StartTime > sorted[j].StartTime
	})
	// Capped: every row is a distinct label set and print history only grows,
	// so an uncapped version would leak cardinality forever.
	if len(sorted) > s.Cfg.PrintInfoLimit {
		sorted = sorted[:s.Cfg.PrintInfoLimit]
	}

	for _, t := range sorted {
		name := t.DesignTitle
		if name == "" {
			name = t.Title
		}
		if name == "" {
			name = "untitled"
		}
		url := ""
		if t.DesignID != 0 {
			url = fmt.Sprintf("https://makerworld.com/en/models/%d", t.DesignID)
		}
		date := t.StartTime
		if len(date) >= 10 {
			date = date[:10]
		}
		material := ""
		if len(t.AMSDetailMapping) > 0 {
			material = t.AMSDetailMapping[0].FilamentType
		}

		s.Metrics.PrintInfo.WithLabelValues(
			strconv.FormatInt(t.ID, 10),
			sanitise(name), url, date, statusLabel(t.Status), material,
		).Set(t.Weight)
	}
}

func statusLabel(code int) string {
	switch code {
	case 2:
		return "ok"
	case 3:
		return "failed"
	default:
		return "running"
	}
}

func creatorOr(d bambu.Design, def string) string {
	if d.Creator.Name == "" {
		return def
	}
	return d.Creator.Name
}
