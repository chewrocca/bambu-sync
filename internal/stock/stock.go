// Package stock joins live spool levels against lifetime usage from print
// history.
package stock

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chewrocca/bambu-sync/internal/bambu"
)

// Spool is one RFID-registered spool with its usage attribution resolved.
type Spool struct {
	Name     string
	Material string
	Color    string  // display form, "#RRGGBB" — see DisplayColor
	Left     float64 // remaining grams
	Capacity float64 // spool capacity grams
	Percent  float64 // remaining as % of capacity
	AMS      int
	Slot     string
	Loaded   bool
	Depleted bool

	// Used is lifetime grams consumed for this spool's (colour, material)
	// group — NOT necessarily by this spool alone. See Shared.
	Used float64

	// Shared is how many distinct spools share this spool's (colour, material)
	// key. When >1 the Used figure belongs to the GROUP, not to this spool.
	//
	// This exists because print history reports only the broad material
	// ("PLA"), never the variant ("PLA Matte" vs "PLA Basic"). A black PLA
	// Basic and a black PLA Matte are therefore indistinguishable in the usage
	// feed. Attributing Used to both would double-count — an earlier bash
	// version did exactly that and inflated one figure by 885 g.
	Shared int
}

// AmbiguousUsage reports whether this spool's usage cannot be attributed to it
// alone. Callers must not present Used as this spool's own consumption when
// this is true.
func (s Spool) AmbiguousUsage() bool { return s.Shared > 1 }

// SlotLabel renders the AMS position, or "" when not loaded.
func (s Spool) SlotLabel() string {
	if !s.Loaded {
		return ""
	}
	return fmt.Sprintf("AMS2 %d:%s", s.AMS, s.Slot)
}

// colorKey normalises a colour for joining. The two feeds disagree on format:
// print history gives "918669FF", the filament inventory gives "#918669FF".
// Without normalising, every lookup silently misses and usage reads zero —
// which looks like "nothing has been printed" rather than like a bug.
func colorKey(c string) string {
	return strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(c)), "#")
}

func groupKey(color, material string) string {
	return colorKey(color) + "|" + material
}

// displayColor renders a colour for use as a label value: "#RRGGBB", alpha
// dropped.
//
// The API reports 8-digit RGBA ("#918669FF"). The alpha channel is always
// opaque in practice and carries no information, but it is NOT cosmetic to
// keep it: the label value is part of the series identity, so including alpha
// would fork every spool series away from the ones the dashboards already
// query and colour-match on.
func displayColor(c string) string {
	k := colorKey(c)
	if len(k) > 6 {
		k = k[:6]
	}
	if k == "" {
		return ""
	}
	return "#" + k
}

// slotString renders the API's slotId, which arrives as a number when loaded
// and as an empty string otherwise.
func slotString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return fmt.Sprintf("%d", int(t))
	default:
		return fmt.Sprint(t)
	}
}

// Join builds the spool list from live inventory and print history.
//
// Sorted by usage descending, then name — so the spools that matter most are
// first in any rendering.
func Join(fil []bambu.Filament, tasks []bambu.Task) []Spool {
	// Lifetime grams consumed per (colour, material).
	used := map[string]float64{}
	for _, t := range tasks {
		for _, d := range t.AMSDetailMapping {
			used[groupKey(d.TargetColor, d.FilamentType)] += d.Weight
		}
	}

	// How many distinct spools share each key.
	shared := map[string]int{}
	for _, f := range fil {
		shared[groupKey(f.Color, f.FilamentType)]++
	}

	out := make([]Spool, 0, len(fil))
	for _, f := range fil {
		k := groupKey(f.Color, f.FilamentType)

		capacity := f.TotalNetWeight
		if capacity <= 0 {
			// Fall back rather than divide by zero. 1 kg is the standard
			// Bambu spool; a wrong capacity is better than a NaN percentage.
			capacity = 1000
		}

		name := f.FilamentName
		if name == "" {
			name = f.FilamentType
		}
		if name == "" {
			name = "?"
		}
		material := f.FilamentType
		if material == "" {
			material = "?"
		}

		out = append(out, Spool{
			Name:     name,
			Material: material,
			Color:    displayColor(f.Color),
			Left:     f.NetWeight,
			Capacity: capacity,
			Percent:  f.NetWeight / capacity * 100,
			AMS:      f.AMSID,
			Slot:     slotString(f.SlotID),
			Loaded:   f.InPrinter,
			Depleted: f.Depleted,
			Used:     used[k],
			Shared:   shared[k],
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Used != out[j].Used {
			return out[i].Used > out[j].Used
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Low returns spools at or below threshold grams, ignoring depleted ones.
// A depleted spool is a finished roll, not a reorder signal.
func Low(spools []Spool, thresholdGrams float64) []Spool {
	var out []Spool
	for _, s := range spools {
		if !s.Depleted && s.Left <= thresholdGrams {
			out = append(out, s)
		}
	}
	return out
}

// PrintCounts returns completed and failed counts from history.
// Status 2 = finished cleanly, 3 = failed; anything else is still running.
func PrintCounts(tasks []bambu.Task) (succeeded, failed int) {
	for _, t := range tasks {
		switch t.Status {
		case 2:
			succeeded++
		case 3:
			failed++
		}
	}
	return
}

// FilterHours returns total print-hours and that as a percentage of the
// 1,440-print-hour activated-carbon filter interval.
func FilterHours(tasks []bambu.Task, intervalHours float64) (hours, percent float64) {
	var secs float64
	for _, t := range tasks {
		secs += float64(t.CostTime)
	}
	hours = secs / 3600
	if intervalHours > 0 {
		percent = hours / intervalHours * 100
	}
	return
}
