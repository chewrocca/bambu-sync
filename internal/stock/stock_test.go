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

// A print using two materials counts once against EACH -- these answer "how
// often do I reach for PETG", not "partition my prints".
func TestByMaterialCountsMultiMaterialPrintsOnce(t *testing.T) {
	tasks := []bambu.Task{
		{AMSDetailMapping: []bambu.AMSDetail{
			{FilamentType: "PLA", Weight: 100},
			{FilamentType: "PETG", Weight: 50},
			{FilamentType: "PLA", Weight: 25}, // same material, two slots
		}},
		{AMSDetailMapping: []bambu.AMSDetail{{FilamentType: "PLA", Weight: 10}}},
	}

	got := ByMaterial(tasks)
	by := map[string]MaterialUsage{}
	for _, u := range got {
		by[u.Material] = u
	}

	if by["PLA"].Prints != 2 {
		t.Errorf("PLA prints = %d, want 2 (two slots in one print is still one print)", by["PLA"].Prints)
	}
	if by["PLA"].Grams != 135 {
		t.Errorf("PLA grams = %v, want 135", by["PLA"].Grams)
	}
	if by["PETG"].Prints != 1 || by["PETG"].Grams != 50 {
		t.Errorf("PETG = %+v, want 1 print / 50 g", by["PETG"])
	}
	if got[0].Material != "PLA" {
		t.Errorf("want most-used first, got %q", got[0].Material)
	}
}

// Unlike per-spool usage, this IS safe to sum: it aggregates prints, so the
// colour+material ambiguity never arises. Each gram is counted once.
func TestTotalFilamentGrams(t *testing.T) {
	tasks := []bambu.Task{
		{AMSDetailMapping: []bambu.AMSDetail{{Weight: 100}, {Weight: 50}}},
		{AMSDetailMapping: []bambu.AMSDetail{{Weight: 25}}},
		{}, // a print with no AMS detail must not break the sum
	}
	if got := TotalFilamentGrams(tasks); got != 175 {
		t.Errorf("TotalFilamentGrams = %v, want 175", got)
	}
}

// The 18-to-19 bug. Two rolls of the SAME product are identical in every field
// the filament endpoint gives us, so the metric label sets they produce are
// identical too — and a Prometheus GaugeVec keyed on those labels holds one
// series, not two. Buying a second black PLA Basic left the dashboard's spool
// count unchanged.
//
// Join must therefore hand out an ordinal that separates them, and it must NOT
// hand one to a spool that has no duplicate: the label is empty in that case
// precisely so existing series identities survive the change.
func TestJoinDistinguishesIdenticalSpools(t *testing.T) {
	fil := []bambu.Filament{
		{FilamentName: "PLA Basic", FilamentType: "PLA", Color: "#000000FF", NetWeight: 1000, TotalNetWeight: 1000},
		{FilamentName: "PLA Basic", FilamentType: "PLA", Color: "#000000FF", NetWeight: 1000, TotalNetWeight: 1000},
		{FilamentName: "Bambu Nylon", FilamentType: "PA", Color: "#123456FF", NetWeight: 900, TotalNetWeight: 1000},
	}

	got := Join(fil, nil)
	if len(got) != 3 {
		t.Fatalf("want 3 spools, got %d", len(got))
	}

	labels := map[string][]string{}
	for _, s := range got {
		labels[s.Name] = append(labels[s.Name], s.InstanceLabel())
	}

	// Empty then "2": the first roll keeps the identity it already had and
	// only the duplicate forks a new series.
	if want := []string{"", "2"}; !equalStrings(labels["PLA Basic"], want) {
		t.Errorf("duplicated product: want spool labels %q, got %q", want, labels["PLA Basic"])
	}
	if want := []string{""}; !equalStrings(labels["Bambu Nylon"], want) {
		t.Errorf("unduplicated product must stay unlabelled: want %q, got %q", want, labels["Bambu Nylon"])
	}
}

// The ordinal keys on the values that are actually PUBLISHED, not on the raw
// API fields. An unnamed spool falls back to its material for the name label,
// so two unnamed PLA rolls collide in the exposition even though nothing in
// the response says "PLA Basic" — the collision the fallbacks create is still
// a collision.
func TestJoinDistinguishesSpoolsThatCollideOnlyAfterFallbacks(t *testing.T) {
	fil := []bambu.Filament{
		{FilamentType: "PLA", Color: "#000000FF", NetWeight: 400, TotalNetWeight: 1000},
		{FilamentType: "PLA", Color: "#000000", NetWeight: 900, TotalNetWeight: 1000},
	}

	got := Join(fil, nil)
	if len(got) != 2 {
		t.Fatalf("want 2 spools, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, s := range got {
		key := s.Name + "|" + s.Material + "|" + s.Color + "|" + s.InstanceLabel()
		if seen[key] {
			t.Fatalf("two spools share the published identity %q", key)
		}
		seen[key] = true
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
