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

	BuildInfo         *prometheus.GaugeVec
	SyncDuration      prometheus.Gauge
	APIErrors         *prometheus.CounterVec
	SpoolDepleted     *prometheus.GaugeVec
	DeviceInfo        *prometheus.GaugeVec
	DeviceOnline      *prometheus.GaugeVec
	PrintsByMaterial  *prometheus.GaugeVec
	FilamentUsedGrams *prometheus.GaugeVec
	FilamentGramsAll  prometheus.Gauge
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
		// The ONLY accurate model identification available. The LAN MQTT
		// payload carries no module list and a null print.sn, so MQTT
		// exporters fall through to a legacy device.type table where the
		// X2D's type == 1 is already claimed by the X1C -- an X2D is reported
		// as an X1C. The cloud API simply says "X2D".
		//
		// dev_access_code is NOT and must never be a label: it is the
		// printer's LAN credential, and a label would put it in Grafana Cloud
		// in plaintext, indexed, indefinitely.
		// Standard exporter hygiene: lets a running pod be traced to a tag.
		// Especially useful here -- the image is distroless, so there is no
		// shell to exec in and ask.
		BuildInfo: f.gaugeVec("bambu_sync_build_info",
			"Build information. Always 1.",
			[]string{"version"}),
		// Which endpoint failed, not merely that something did. The API is
		// undocumented and can change one endpoint at a time, so "tasks is
		// failing but filament is fine" is the diagnosis that saves an hour.
		APIErrors: f.counterVec("bambu_api_errors_total",
			"Bambu cloud API request failures, by endpoint.",
			[]string{"endpoint"}),
		// A finished roll. Deliberately its own metric rather than a label on
		// bambu_spool_remaining_grams: adding a label there would change every
		// existing series identity and break the dashboards.
		SpoolDepleted: f.gaugeVec("bambu_spool_depleted",
			"1 if the spool is marked depleted (a finished roll, not a reorder signal).",
			[]string{"name", "material", "color"}),
		DeviceInfo: f.gaugeVec("bambu_device_info",
			"Bound printer identity from the cloud account. Always 1.",
			[]string{"name", "serial", "product_name", "model_name", "structure"}),
		// Cloud-side reachability, which is a DIFFERENT question from the MQTT
		// exporter's bambulab_printer_up: that says "can I reach it on the
		// LAN". Together they separate "the printer is off" from "my network
		// is broken".
		DeviceOnline: f.gaugeVec("bambu_device_online",
			"1 if Bambu's cloud considers the printer online.",
			[]string{"name", "serial"}),

		// Aggregates over the FULL fetched history, not the 20 published as
		// bambu_print_info. They cost no extra API call and collapse to one
		// series per material, so they recover detail the cardinality cap
		// throws away.
		PrintsByMaterial: f.gaugeVec("bambu_prints_by_material",
			"Prints that used this material. A multi-material print counts once per material.",
			[]string{"material"}),
		FilamentUsedGrams: f.gaugeVec("bambu_filament_used_grams",
			"Filament consumed per material across the fetched print history.",
			[]string{"material"}),

		QueueItem: f.gaugeVec("bambu_queue_item",
			"MakerWorld favourites, i.e. the print queue. Value is that design's print count.",
			[]string{"name", "url", "creator"}),
	}
	// Safe to sum, unlike bambu_spool_used_grams: this aggregates PRINTS, so
	// the colour+material ambiguity never arises and each gram is counted once.
	s.FilamentGramsAll = f.gauge("bambu_filament_used_grams_total",
		"Total filament consumed across the fetched print history.")
	// How long the upstream took. The neighbouring MQTT exporter publishes an
	// equivalent; without one, a slow-but-succeeding API looks identical to a
	// fast one right up until it times out.
	s.SyncDuration = f.gauge("bambu_sync_duration_seconds",
		"Wall-clock duration of the last full sync.")
	return s
}

// ResetDynamic clears the label-bearing vectors ahead of a full repopulation.
// Without this, a spool that leaves the AMS or a print that finishes would
// keep exporting its old series forever.
func (s *Set) ResetDynamic() {
	s.SpoolRemainingGrams.Reset()
	s.SpoolRemainingPercent.Reset()
	s.SpoolUsedGrams.Reset()
	s.SpoolDepleted.Reset()
	s.QueueItem.Reset()
	s.DeviceInfo.Reset()
	s.DeviceOnline.Reset()
	s.PrintsByMaterial.Reset()
	s.FilamentUsedGrams.Reset()
	s.ResetPrintInfo()
}

// ResetPrintInfo clears every per-print series. Only the FULL sync may call
// this: it republishes the last 20 prints immediately afterwards. The fast
// poll fetches a single task, so a reset there would collapse the history to
// one row -- use DeletePartialMatch on status="running" instead.
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

func (f factory) counterVec(name, help string, labels []string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	f.reg.MustRegister(c)
	return c
}

func (f factory) gaugeVec(name, help string, labels []string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	f.reg.MustRegister(g)
	return g
}
