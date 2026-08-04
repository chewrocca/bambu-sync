// Command bambu-sync exports Bambu Lab cloud-account state as Prometheus
// metrics, and alerts on the conditions worth acting on.
//
// It is a companion to the LAN MQTT exporter, not a replacement: that reads
// the device (temperatures, fans, live AMS state), this reads the account
// (print history, RFID filament inventory, MakerWorld favourites). Different
// data sources, same printer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	// distroless/static carries no timezone database, so the daily schedule
	// would fail to resolve a local time without this. Load-bearing.
	_ "time/tzdata"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"

	"github.com/chewrocca/bambu-sync/internal/bambu"
	"github.com/chewrocca/bambu-sync/internal/config"
	"github.com/chewrocca/bambu-sync/internal/humidity"
	"github.com/chewrocca/bambu-sync/internal/metrics"
	"github.com/chewrocca/bambu-sync/internal/notify"
	"github.com/chewrocca/bambu-sync/internal/store"
	"github.com/chewrocca/bambu-sync/internal/sync"
)

// version is set at build time via -ldflags "-X main.version=...". It is
// surfaced as bambu_sync_build_info so a running pod can be traced back to a
// tag without shelling in -- which matters here, because the image is
// distroless and there is no shell to exec into.
var version = "dev"

func main() {
	once := flag.Bool("once", false, "run a single full sync, print metrics to stdout, and exit")
	alerting := flag.Bool("alerting", false, "with -once, also evaluate and send alerts")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(*once, *alerting, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(once, alerting bool, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	// One Set: metrics.New registers against reg, so calling it twice would
	// panic on duplicate registration.
	m := metrics.New(reg)
	m.BuildInfo.WithLabelValues(version).Set(1)

	s := &sync.Syncer{
		Cfg:      cfg,
		Client:   bambu.New(cfg.BaseURL),
		Metrics:  m,
		Humidity: humidity.New(cfg.HumidityURL, cfg.HumidityMetric),
		Slack:    notify.New(cfg.SlackUsername, cfg.SlackIconEmoji),
		Store:    store.New(cfg.StoreURL),
		Log:      log,
	}

	// -once exists for parity checking against the implementation this
	// replaces: run it on the same day and diff the exposition output.
	if once {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.Run(ctx, sync.Options{Alerting: alerting}); err != nil {
			return err
		}
		return dumpMetrics(reg)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	// Probes deliberately report only on the HTTP server. A dead Bambu token
	// means the UPSTREAM is broken, not this process — failing liveness on
	// sync errors would turn an expired token into a CrashLoop, which is loud
	// in exactly the wrong way. Sync health is bambu_sync_up and Slack.
	probe := func(w http.ResponseWriter, _ *http.Request) {
		// A failed write to a probe response is the kubelet hanging up; there
		// is nothing useful to do about it and nowhere useful to report it.
		_, _ = io.WriteString(w, "ok\n")
	}
	mux.HandleFunc("/health", probe)
	mux.HandleFunc("/ready", probe)

	// pprof is OPT-IN. The handlers expose goroutine stacks, heap contents and
	// the process command line; this binary holds a full-account bearer token,
	// so they are not something to serve by default on a public image.
	//
	// Registered explicitly rather than via the net/http/pprof blank import,
	// which attaches to http.DefaultServeMux -- a package-level side effect
	// that would expose them on any server sharing that mux, whether or not
	// this flag is set.
	if cfg.PprofEnabled {
		log.Warn("pprof enabled", "path", "/debug/pprof", "addr", cfg.ListenAddr)
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	go loop(ctx, s, cfg, log)

	<-ctx.Done()
	log.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// loop drives both cadences.
//
// The startup run populates metrics immediately but does NOT alert: most
// alerts are standing conditions, so alerting on startup would re-ping on
// every image bump, OOM and node reboot.
func loop(ctx context.Context, s *sync.Syncer, cfg *config.Config, log *slog.Logger) {
	do := func(alerting bool) {
		runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := s.Run(runCtx, sync.Options{Alerting: alerting}); err != nil {
			log.Error("sync failed", "err", err, "alerting", alerting)
		}
	}

	do(false)
	if err := s.RunFast(ctx); err != nil {
		log.Error("fast poll failed", "err", err)
	}

	fast := time.NewTicker(cfg.FastInterval)
	defer fast.Stop()

	daily := time.NewTimer(untilNext(time.Now(), cfg))
	defer daily.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			if err := s.RunFast(ctx); err != nil {
				log.Error("fast poll failed", "err", err)
			}
		case <-daily.C:
			do(true)
			daily.Reset(untilNext(time.Now(), cfg))
		}
	}
}

// untilNext returns the duration to the next scheduled full sync. Computed
// against a wall clock in the configured location rather than by adding 24h,
// so the run stays at the same local time across DST.
func untilNext(now time.Time, cfg *config.Config) time.Duration {
	now = now.In(cfg.Location)
	hhmm, err := time.ParseInLocation("15:04", cfg.DailyAt, cfg.Location)
	if err != nil {
		return cfg.FullInterval
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), hhmm.Hour(), hhmm.Minute(), 0, 0, cfg.Location)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(now)
}

// dumpMetrics writes the exposition text to stdout for -once parity checks.
func dumpMetrics(g prometheus.Gatherer) error {
	families, err := g.Gather()
	if err != nil {
		return fmt.Errorf("gather metrics: %w", err)
	}
	enc := expfmt.NewEncoder(os.Stdout, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range families {
		if err := enc.Encode(mf); err != nil {
			return fmt.Errorf("encode %s: %w", mf.GetName(), err)
		}
	}
	return nil
}
