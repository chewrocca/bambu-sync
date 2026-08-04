package stock

import (
	"testing"

	"github.com/chewrocca/bambu-sync/internal/bambu"
)

// The two feeds format colour differently: print history gives "918669FF",
// filament inventory gives "#918669FF". If they are not normalised the join
// misses entirely and usage reads zero — which looks like "nothing printed"
// rather than like a bug, so it is worth a test.
func TestJoinNormalisesColourAcrossFeeds(t *testing.T) {
	fil := []bambu.Filament{
		{FilamentName: "PLA Basic", FilamentType: "PLA", Color: "#918669FF", NetWeight: 400, TotalNetWeight: 1000},
	}
	tasks := []bambu.Task{
		{AMSDetailMapping: []bambu.AMSDetail{{TargetColor: "918669ff", FilamentType: "PLA", Weight: 250}}},
	}

	got := Join(fil, tasks)
	if len(got) != 1 {
		t.Fatalf("want 1 spool, got %d", len(got))
	}
	if got[0].Used != 250 {
		t.Errorf("usage did not join across colour formats: want 250, got %v", got[0].Used)
	}
}

// The regression that matters. Print history reports only the broad material
// ("PLA"), never the variant, so two black PLA spools share one usage key. The
// bash version attributed the group total to BOTH spools and inflated a figure
// by 885 g. Usage must be reported once, flagged as ambiguous.
func TestJoinDoesNotDoubleCountSharedColourMaterial(t *testing.T) {
	fil := []bambu.Filament{
		{FilamentName: "PLA Basic", FilamentType: "PLA", Color: "#000000FF", NetWeight: 500, TotalNetWeight: 1000},
		{FilamentName: "PLA Matte", FilamentType: "PLA", Color: "#000000FF", NetWeight: 700, TotalNetWeight: 1000},
	}
	tasks := []bambu.Task{
		{AMSDetailMapping: []bambu.AMSDetail{{TargetColor: "000000FF", FilamentType: "PLA", Weight: 885}}},
	}

	got := Join(fil, tasks)
	if len(got) != 2 {
		t.Fatalf("want 2 spools, got %d", len(got))
	}
	for _, s := range got {
		if !s.AmbiguousUsage() {
			t.Errorf("%s: shared colour+material must be flagged ambiguous (Shared=%d)", s.Name, s.Shared)
		}
		if s.Used != 885 {
			t.Errorf("%s: want group total 885, got %v", s.Name, s.Used)
		}
	}
	// The point of the flag: a caller summing Used across spools would get
	// 1770 g from 885 g of filament. AmbiguousUsage is what stops that.
	var naiveTotal float64
	for _, s := range got {
		naiveTotal += s.Used
	}
	if naiveTotal != 1770 {
		t.Fatalf("test premise wrong: naive sum should be 1770, got %v", naiveTotal)
	}
}

// The API reports 8-digit RGBA; the label must be 6-digit "#RRGGBB". Alpha is
// always opaque and carries no information, but the label value is part of the
// series identity — including it would fork every spool series away from the
// ones the dashboards already query and colour-match on. Caught by a parity
// diff against the implementation this replaces.
func TestJoinLabelColourDropsAlpha(t *testing.T) {
	fil := []bambu.Filament{
		{FilamentName: "ABS", FilamentType: "ABS", Color: "#87909AFF", NetWeight: 500, TotalNetWeight: 1000},
	}
	got := Join(fil, nil)
	if got[0].Color != "#87909A" {
		t.Errorf("label colour = %q, want %q", got[0].Color, "#87909A")
	}
}

func TestDisplayColour(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"#918669FF", "#918669"},
		{"918669ff", "#918669"},
		{"#FFFFFF", "#FFFFFF"},
		{"", ""},
	} {
		if got := displayColor(tc.in); got != tc.want {
			t.Errorf("displayColor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A spool whose colour+material is unique owns its usage outright.
func TestJoinUniqueKeyIsNotAmbiguous(t *testing.T) {
	fil := []bambu.Filament{
		{FilamentName: "PETG Basic", FilamentType: "PETG", Color: "#FF0000FF", NetWeight: 900, TotalNetWeight: 1000},
	}
	tasks := []bambu.Task{
		{AMSDetailMapping: []bambu.AMSDetail{{TargetColor: "FF0000FF", FilamentType: "PETG", Weight: 100}}},
	}
	got := Join(fil, tasks)
	if got[0].AmbiguousUsage() {
		t.Error("unique colour+material must not be flagged ambiguous")
	}
}

// totalNetWeight is absent or zero on some records; percent must not be NaN.
func TestJoinFallsBackWhenCapacityMissing(t *testing.T) {
	fil := []bambu.Filament{
		{FilamentName: "PLA", FilamentType: "PLA", Color: "#FFF", NetWeight: 500, TotalNetWeight: 0},
	}
	got := Join(fil, nil)
	if got[0].Capacity != 1000 {
		t.Errorf("want 1000 g fallback capacity, got %v", got[0].Capacity)
	}
	if got[0].Percent != 50 {
		t.Errorf("want 50%%, got %v", got[0].Percent)
	}
}

// A depleted spool is a finished roll, not a reorder signal.
func TestLowIgnoresDepleted(t *testing.T) {
	spools := []Spool{
		{Name: "empty", Left: 0, Depleted: true},
		{Name: "low", Left: 200},
		{Name: "fine", Left: 800},
	}
	got := Low(spools, 250)
	if len(got) != 1 || got[0].Name != "low" {
		t.Fatalf("want only the low non-depleted spool, got %+v", got)
	}
}

// Status codes are confirmed against real history: 2 done, 3 failed, other
// still running. An unknown code must not be silently counted as either.
func TestPrintCountsIgnoresRunning(t *testing.T) {
	tasks := []bambu.Task{{Status: 2}, {Status: 2}, {Status: 3}, {Status: 1}, {Status: 99}}
	ok, failed := PrintCounts(tasks)
	if ok != 2 || failed != 1 {
		t.Errorf("want 2 succeeded / 1 failed, got %d / %d", ok, failed)
	}
}

func TestFilterHours(t *testing.T) {
	tasks := []bambu.Task{{CostTime: 3600}, {CostTime: 1800}}
	hours, pct := FilterHours(tasks, 1440)
	if hours != 1.5 {
		t.Errorf("want 1.5 h, got %v", hours)
	}
	if want := 1.5 / 1440 * 100; pct != want {
		t.Errorf("want %v%%, got %v", want, pct)
	}
}

// slotId arrives as a number when loaded and as "" otherwise. An empty field
// must survive rather than shifting later fields — the bash version hit
// exactly this class of bug with tab-delimited parsing.
func TestSlotStringHandlesBothShapes(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"", ""},
		{float64(3), "3"},
		{"2", "2"},
	} {
		if got := slotString(tc.in); got != tc.want {
			t.Errorf("slotString(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
