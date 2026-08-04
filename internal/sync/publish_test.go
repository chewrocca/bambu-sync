package sync

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"

	"github.com/chewrocca/bambu-sync/internal/bambu"
	"github.com/chewrocca/bambu-sync/internal/config"
	"github.com/chewrocca/bambu-sync/internal/metrics"
	"github.com/chewrocca/bambu-sync/internal/stock"
	"github.com/chewrocca/bambu-sync/internal/store"
)

// publish is the function every dashboard depends on: it turns domain values
// into the exact label sets Grafana queries. A change here is invisible in
// code review and surfaces only as an empty panel, so these tests assert the
// EXPOSITION rather than the internals.
//
// Deliberately no client_golang/prometheus/testutil: it drags go-cmp and
// friends into go.mod, and this exporter's two-direct-dependency footprint is
// worth more than the convenience. expfmt is already a direct dependency.

func fixture(t *testing.T, printInfoLimit int) (*Syncer, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	return &Syncer{
		Cfg: &config.Config{
			PrintInfoLimit: printInfoLimit,
			FilterHours:    1440,
		},
		Metrics: metrics.New(reg),
		Store:   store.New(""), // zero base -> the real default storefront
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, reg
}

// render returns the text exposition, optionally narrowed to named families.
func render(t *testing.T, reg *prometheus.Registry, names ...string) string {
	t.Helper()
	keep := make(map[string]bool, len(names))
	for _, n := range names {
		keep[n] = true
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	enc := expfmt.NewEncoder(&b, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, f := range families {
		if len(keep) > 0 && !keep[f.GetName()] {
			continue
		}
		if err := enc.Encode(f); err != nil {
			t.Fatal(err)
		}
	}
	return strings.TrimSpace(b.String())
}

func assertExposition(t *testing.T, reg *prometheus.Registry, name, want string) {
	t.Helper()
	if got := render(t, reg, name); got != strings.TrimSpace(want) {
		t.Errorf("%s exposition mismatch\n--- got ---\n%s\n--- want ---\n%s",
			name, got, strings.TrimSpace(want))
	}
}

// value reads one series by label match. Empty labels means "the only series".
func value(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			match := true
			for k, v := range labels {
				found := false
				for _, l := range m.GetLabel() {
					if l.GetName() == k && l.GetValue() == v {
						found = true
						break
					}
				}
				if !found {
					match = false
					break
				}
			}
			if match {
				return m.GetGauge().GetValue()
			}
		}
	}
	t.Fatalf("no series %s matching %v", name, labels)
	return 0
}

// Two black PLA spools sharing a usage figure, plus one filament that is not
// in the verified catalog. Between them they cover both store-link paths and
// both sides of the ambiguity flag.
func fixtureSpools() []stock.Spool {
	return []stock.Spool{
		{
			Name: "PLA Basic", Material: "PLA", Color: "#000000",
			Left: 200, Capacity: 1000, Percent: 20,
			Used: 884.91, Shared: 2,
			Loaded: true, AMS: 0, Slot: "A",
		},
		{
			Name: "PLA Matte", Material: "PLA", Color: "#000000",
			Left: 750, Capacity: 1000, Percent: 75,
			Used: 884.91, Shared: 2,
		},
		{
			Name: "Bambu Nylon", Material: "PA", Color: "#123456",
			Left: 900, Capacity: 1000, Percent: 90,
			Used: 50, Shared: 1,
		},
	}
}

// Three prints: one clean, one failed, one multi-material. 87.5 g across four
// filament entries — deliberately more entries than prints, so an aggregate
// that counted prints instead of filament would show up.
func fixtureTasks() []bambu.Task {
	return []bambu.Task{
		{
			ID: 101, DesignTitle: "Benchy", Title: "0.2mm layer, 3 walls", DesignID: 55,
			Status: 2, StartTime: "2026-08-01T10:00:00Z", CostTime: 3600, Weight: 12.5,
			AMSDetailMapping: []bambu.AMSDetail{{FilamentType: "PLA", Weight: 12.5}},
		},
		{
			ID: 102, Title: "Gridfinity bin",
			Status: 3, StartTime: "2026-08-02T10:00:00Z", CostTime: 1800, Weight: 30,
			AMSDetailMapping: []bambu.AMSDetail{{FilamentType: "PETG", Weight: 30}},
		},
		{
			ID: 103, Title: "Multi-material tray",
			Status: 2, StartTime: "2026-08-03T10:00:00Z", CostTime: 7200, Weight: 45,
			AMSDetailMapping: []bambu.AMSDetail{
				{FilamentType: "PLA", Weight: 20},
				{FilamentType: "PETG", Weight: 25},
			},
		},
	}
}

// The 885 g bug, locked at the exposition layer. Two spools sharing a colour
// and material carry the GROUP's consumption and must be flagged
// ambiguous="true"; an unrecognised filament must fall back to the collection
// rather than to a guessed product slug.
//
// A golden test on purpose: the label NAMES and ORDER are the contract a
// dashboard binds to, and they should not be able to drift silently.
func TestPublishSpoolUsedGramsExposition(t *testing.T) {
	s, reg := fixture(t, 20)
	s.publish(fixtureSpools(), nil, nil, nil, 0, 0, 0, 0)

	assertExposition(t, reg, "bambu_spool_used_grams", `
# HELP bambu_spool_used_grams Lifetime filament consumed for this spool's colour+material group. See the ambiguous label.
# TYPE bambu_spool_used_grams gauge
bambu_spool_used_grams{ambiguous="false",color="#123456",material="PA",name="Bambu Nylon",store="https://us.store.bambulab.com/collections/3d-printer-filament"} 50
bambu_spool_used_grams{ambiguous="true",color="#000000",material="PLA",name="PLA Basic",store="https://us.store.bambulab.com/products/pla-basic-filament"} 884.91
bambu_spool_used_grams{ambiguous="true",color="#000000",material="PLA",name="PLA Matte",store="https://us.store.bambulab.com/products/pla-matte"} 884.91
`)
}

// The percent vector carries the widest label set in the exporter, including
// the AMS slot the inventory panel groups by. An unloaded spool must yield an
// EMPTY ams_slot, not a fabricated one.
func TestPublishSpoolRemainingPercentExposition(t *testing.T) {
	s, reg := fixture(t, 20)
	s.publish(fixtureSpools(), nil, nil, nil, 0, 0, 0, 0)

	assertExposition(t, reg, "bambu_spool_remaining_percent", `
# HELP bambu_spool_remaining_percent Remaining filament as a percentage of spool capacity.
# TYPE bambu_spool_remaining_percent gauge
bambu_spool_remaining_percent{ams_slot="",capacity="1000",color="#000000",grams="750",loaded="false",material="PLA",name="PLA Matte",store="https://us.store.bambulab.com/products/pla-matte"} 75
bambu_spool_remaining_percent{ams_slot="",capacity="1000",color="#123456",grams="900",loaded="false",material="PA",name="Bambu Nylon",store="https://us.store.bambulab.com/collections/3d-printer-filament"} 90
bambu_spool_remaining_percent{ams_slot="AMS2 0:A",capacity="1000",color="#000000",grams="200",loaded="true",material="PLA",name="PLA Basic",store="https://us.store.bambulab.com/products/pla-basic-filament"} 20
`)
}

// The documented bargain: bambu_print_info is CAPPED for cardinality, but the
// aggregates run over the FULL fetched history, which is how the detail the
// cap discards is recovered for free. If publish ever aggregated the capped
// slice instead, totals would silently shrink and nothing else would notice.
func TestPublishAggregatesCoverFullHistoryDespiteCap(t *testing.T) {
	s, reg := fixture(t, 2) // cap BELOW the three fixture prints
	s.publish(nil, fixtureTasks(), nil, nil, 2, 1, 100, 6.94)

	if got := count(t, reg, "bambu_print_info"); got != 2 {
		t.Errorf("print_info must honour PrintInfoLimit: want 2 series, got %d", got)
	}

	// 12.5 + 30 + 20 + 25 — every filament entry, not just the published two.
	if got := value(t, reg, "bambu_filament_used_grams_total", nil); got != 87.5 {
		t.Errorf("total grams must span the full history: want 87.5, got %v", got)
	}
	for _, tc := range []struct {
		material string
		prints   float64
		grams    float64
	}{
		{"PLA", 2, 32.5},  // tasks 101 and 103
		{"PETG", 2, 55.0}, // tasks 102 and 103
	} {
		l := map[string]string{"material": tc.material}
		if got := value(t, reg, "bambu_prints_by_material", l); got != tc.prints {
			t.Errorf("%s prints: want %v, got %v", tc.material, tc.prints, got)
		}
		if got := value(t, reg, "bambu_filament_used_grams", l); got != tc.grams {
			t.Errorf("%s grams: want %v, got %v", tc.material, tc.grams, got)
		}
	}

	// A multi-material print counts once against EACH material, so these
	// deliberately sum to more than the 3 prints. That is the documented
	// meaning ("how often do I reach for PETG"); a "fix" that made them
	// partition would be a behaviour change, not a bug fix.
	if got := value(t, reg, "bambu_prints_succeeded", nil); got != 2 {
		t.Errorf("succeeded: want 2, got %v", got)
	}
	if got := value(t, reg, "bambu_filter_percent", nil); got != 6.94 {
		t.Errorf("filter percent: want 6.94, got %v", got)
	}
}

// The newest prints are the ones worth keeping when the cap bites. Publishing
// in fetch order would silently drop today's print and keep a stale one.
func TestPublishPrintsKeepsNewestUnderCap(t *testing.T) {
	s, reg := fixture(t, 2)
	s.publish(nil, fixtureTasks(), nil, nil, 0, 0, 0, 0)

	got := render(t, reg, "bambu_print_info")
	for _, want := range []string{`id="103"`, `id="102"`} {
		if !strings.Contains(got, want) {
			t.Errorf("newest prints must survive the cap, missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, `id="101"`) {
		t.Error("the oldest print should be the one the cap drops")
	}

	// The slicer profile is a SEPARATE label from the model name. Task 101 is
	// the only fixture carrying both, and the cap drops it, so assert the
	// split on an uncapped run.
	s2, reg2 := fixture(t, 20)
	s2.publish(nil, fixtureTasks(), nil, nil, 0, 0, 0, 0)
	full := render(t, reg2, "bambu_print_info")
	for _, want := range []string{
		`name="Benchy"`,
		`profile="0.2mm layer, 3 walls"`,
		`url="https://makerworld.com/en/models/55"`,
		`date="2026-08-01"`, // truncated from the RFC3339 timestamp
		`status="failed"`,   // status 3
		`material="PETG"`,
	} {
		if !strings.Contains(full, want) {
			t.Errorf("missing %s in:\n%s", want, full)
		}
	}
	// Duration must carry the identical label set, or the two stop joining.
	if a, b := count(t, reg2, "bambu_print_info"), count(t, reg2, "bambu_print_duration_seconds"); a != b {
		t.Errorf("print_info and duration must stay aligned: %d vs %d", a, b)
	}
}

// Departed series must STOP being exported, or a spool removed from the AMS
// keeps showing stock you no longer have. This is what ResetDynamic exists
// for, and it is only safe because publish repopulates immediately after.
func TestPublishDropsDepartedSpools(t *testing.T) {
	s, reg := fixture(t, 20)
	s.publish(fixtureSpools(), nil, nil, nil, 0, 0, 0, 0)
	if got := count(t, reg, "bambu_spool_remaining_grams"); got != 3 {
		t.Fatalf("setup: want 3 spools, got %d", got)
	}

	// Next sync: two spools have left the AMS.
	s.publish(fixtureSpools()[:1], nil, nil, nil, 0, 0, 0, 0)
	if got := count(t, reg, "bambu_spool_remaining_grams"); got != 1 {
		t.Errorf("departed spools must stop being exported: want 1 series, got %d", got)
	}
	if strings.Contains(render(t, reg), "PLA Matte") {
		t.Error("a departed spool is still being exported")
	}
}

// End-to-end proof of the credential boundary: feed the raw API payload,
// INCLUDING dev_access_code, all the way through to the exposition. The
// reflection test in internal/bambu proves the field is not mapped; this
// proves nothing downstream reintroduces it as a label.
func TestPublishNeverExportsPrinterAccessCode(t *testing.T) {
	const payload = `{"devices":[{
		"dev_id":"00M09A1234567890",
		"name":"Workshop \"X2D\"",
		"dev_product_name":"X2D",
		"dev_model_name":"N6-V2",
		"dev_structure":"CoreXY",
		"dev_access_code":"31415926",
		"online":true
	}]}`

	var bind bambu.BindResponse
	if err := json.Unmarshal([]byte(payload), &bind); err != nil {
		t.Fatal(err)
	}

	s, reg := fixture(t, 20)
	s.publish(nil, nil, nil, bind.Devices, 0, 0, 0, 0)

	got := render(t, reg)
	if strings.Contains(got, "31415926") {
		t.Fatal("the printer's LAN access code reached the metrics exposition")
	}
	if strings.Contains(got, "access_code") {
		t.Error("an access-code label exists, even if empty")
	}

	// Scope the positive assertions to ONE family. Searching the whole
	// exposition lets a passing match come from a different metric -- which is
	// exactly how an earlier version of this test let an unsanitised
	// device_info name through, because device_online still sanitised it.
	info := render(t, reg, "bambu_device_info")

	// product_name is the only accurate model identification available, so
	// losing it is a real regression.
	if !strings.Contains(info, `product_name="X2D"`) {
		t.Errorf("device identity missing in:\n%s", info)
	}
	// The quote in the printer name would break label quoting; sanitise
	// REPLACES rather than escapes it.
	if !strings.Contains(info, `name="Workshop  X2D "`) {
		t.Errorf("printer name should be sanitised, got:\n%s", info)
	}
	if v := value(t, reg, "bambu_device_online", map[string]string{"serial": "00M09A1234567890"}); v != 1 {
		t.Errorf("device_online: want 1, got %v", v)
	}
}

// Favourites are arbitrary user text from MakerWorld and go straight into
// labels. A missing creator must degrade to "?" rather than to an empty label
// that reads as a real value.
func TestPublishFavourites(t *testing.T) {
	anon := bambu.Design{ID: 900, Title: "Bracket\nv2", PrintCount: 7}
	named := bambu.Design{ID: 901, Title: "Hook", PrintCount: 3}
	named.Creator.Name = "someone"

	s, reg := fixture(t, 20)
	s.publish(nil, nil, []bambu.Design{anon, named}, nil, 0, 0, 0, 0)

	got := render(t, reg, "bambu_queue_item")
	for _, want := range []string{
		`creator="?"`,
		`creator="someone"`,
		`url="https://makerworld.com/en/models/900"`,
		`name="Bracket v2"`, // newline replaced, not escaped
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	// Value is the design's public print count, not a local one.
	if v := value(t, reg, "bambu_queue_item", map[string]string{"creator": "someone"}); v != 3 {
		t.Errorf("queue_item value should be the public print count: want 3, got %v", v)
	}
	if got := value(t, reg, "bambu_makerworld_favorites", nil); got != 2 {
		t.Errorf("favourites count: want 2, got %v", got)
	}
}
