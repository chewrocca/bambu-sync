// Package metrics owns the Prometheus surface.
//
// Metric names are inherited from the bash implementation this replaces and
// are deliberately unchanged, so existing dashboards keep working.
//
// Naming convention worth knowing: bambu_* is the CLOUD plane (this service —
// account history and inventory). bambulab_* is the DEVICE plane (the LAN MQTT
// exporter — live temperatures, fans, AMS state). Different data sources, same
// printer.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Set is the full metric surface. Vectors are Reset before each repopulation
// so that departed series stop being exported and the scrape marks them stale
// — a finished print must stop looking like a running one.
type Set struct {
	Up           prometheus.Gauge
	LastRun      prometheus.Gauge
	LastExitCode prometheus.Gauge
	TokenExpires prometheus.Gauge
	ProbeLastRun prometheus.Gauge

	SpoolRemainingGrams   *prometheus.GaugeVec
	SpoolRemainingPercent *prometheus.GaugeVec
	SpoolUsedGrams        *prometheus.GaugeVec
	PrintDurationSeconds  *prometheus.GaugeVec
	FilterHours           prometheus.Gauge
	FilterPercent         prometheus.Gauge
	PrintsSucceeded       prometheus.Gauge
	PrintsFailed          prometheus.Gauge
	PrintInfo             *prometheus.GaugeVec
	QueueItem             *prometheus.GaugeVec
	Favorites             prometheus.Gauge
}

// New registers the metric surface against reg.
func New(reg prometheus.Registerer) *Set {
	f := promauto(reg)
	s := &Set{
		Up: f.gauge("bambu_sync_up",
			"1 if the last full sync succeeded, 0 otherwise."),
		LastRun: f.gauge("bambu_sync_last_run_timestamp_seconds",
			"Unix timestamp of the last full sync attempt."),
		LastExitCode: f.gauge("bambu_sync_last_exit_code",
			"0 ok, 1 error, 2 token expired."),
		TokenExpires: f.gauge("bambu_token_expires_timestamp_seconds",
			"Unix timestamp at which the Bambu cloud token expires."),
		ProbeLastRun: f.gauge("bambu_print_probe_last_run_timestamp_seconds",
			"Unix timestamp of the last fast current-print poll."),

		FilterHours: f.gauge("bambu_filter_hours",
			"Cumulative print-hours against the activated carbon filter interval."),
		FilterPercent: f.gauge("bambu_filter_percent",
			"Filter wear as a percentage of its replacement interval."),
		PrintsSucceeded: f.gauge("bambu_prints_succeeded",
			"Count of successfully completed prints in the fetched history."),
		PrintsFailed: f.gauge("bambu_prints_failed",
			"Count of failed prints in the fetched history."),
		Favorites: f.gauge("bambu_makerworld_favorites",
			"Count of favourited MakerWorld designs."),

		SpoolRemainingGrams: f.gaugeVec("bambu_spool_remaining_grams",
			"Remaining filament weight per registered spool.",
			[]string{"name", "material", "color", "store"}),
		SpoolRemainingPercent: f.gaugeVec("bambu_spool_remaining_percent",
			"Remaining filament as a percentage of spool capacity.",
			[]string{"name", "material", "color", "store", "grams", "capacity", "loaded", "ams_slot"}),
		// Lifetime consumption per (colour, material) group.
		//
		// The `ambiguous` label is not decoration. Print history reports only
		// the broad material ("PLA"), never the variant, so two spools sharing
		// a colour and material share one usage figure — and it belongs to the
		// GROUP, not to either spool. ambiguous="true" means: do not sum this
		// across spools, and do not present it as this spool's own
		// consumption. An earlier implementation attributed the group total to
		// both spools and inflated a figure by 885 g.
		SpoolUsedGrams: f.gaugeVec("bambu_spool_used_grams",
			"Lifetime filament consumed for this spool's colour+material group. See the ambiguous label.",
			[]string{"name", "material", "color", "store", "ambiguous"}),

		PrintInfo: f.gaugeVec("bambu_print_info",
			"Recent prints. Value is grams used; labels carry model, URL, date, outcome and slicer profile.",
			// id is a uniqueness key, not something a dashboard displays.
			// Printing the same model twice in a day with the same filament
			// would otherwise produce a duplicate label set.
			//
			// profile is the slicer plate description ("Two Plates, 0.2mm
			// layer, 2 walls, 20% infill") — distinct from the model name, and
			// often the thing you actually want when comparing two runs of the
			// same model.
			[]string{"id", "name", "url", "date", "status", "material", "profile"}),

		// Duration carries the same label set as PrintInfo so the two join
		// cleanly. A separate metric rather than a label on PrintInfo, because
		// duration is a measurement to aggregate, not an identity to group by.
		PrintDurationSeconds: f.gaugeVec("bambu_print_duration_seconds",
			"Wall-clock duration of each recent print.",
			[]string{"id", "name", "url", "date", "status", "material", "profile"}),
		QueueItem: f.gaugeVec("bambu_queue_item",
			"MakerWorld favourites, i.e. the print queue. Value is that design's print count.",
			[]string{"name", "url", "creator"}),
	}
	return s
}

// ResetDynamic clears the label-bearing vectors ahead of a full repopulation.
// Without this, a spool that leaves the AMS or a print that finishes would
// keep exporting its old series forever.
func (s *Set) ResetDynamic() {
	s.SpoolRemainingGrams.Reset()
	s.SpoolRemainingPercent.Reset()
	s.SpoolUsedGrams.Reset()
	s.QueueItem.Reset()
	s.ResetPrintInfo()
}

// ResetPrintInfo clears the per-print series, for the fast poll.
func (s *Set) ResetPrintInfo() {
	s.PrintInfo.Reset()
	s.PrintDurationSeconds.Reset()
}

type factory struct{ reg prometheus.Registerer }

func promauto(reg prometheus.Registerer) factory { return factory{reg} }

func (f factory) gauge(name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	f.reg.MustRegister(g)
	return g
}

func (f factory) gaugeVec(name, help string, labels []string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	f.reg.MustRegister(g)
	return g
}
