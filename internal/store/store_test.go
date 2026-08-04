package store

import (
	"strings"
	"testing"
)

// Regression: the Go rewrite initially pointed EVERY spool at the store root,
// losing the per-product links the bash implementation had. The whole value of
// the link is landing on the thing you would reorder.
func TestKnownFilamentsLinkToTheirProduct(t *testing.T) {
	l := New("https://us.store.bambulab.com")
	for name, wantSlug := range map[string]string{
		"PLA Basic":  "pla-basic-filament",
		"PLA Matte":  "pla-matte",
		"PLA Silk+":  "pla-silk-upgrade",
		"PLA Pure":   "pla-pure",
		"PLA Wood":   "pla-wood",
		"PETG Basic": "petg-basic",
		"ABS":        "abs-filament",
	} {
		got := l.URL(name)
		want := "https://us.store.bambulab.com/products/" + wantSlug
		if got != want {
			t.Errorf("%s -> %s, want %s", name, got, want)
		}
		if !l.Known(name) {
			t.Errorf("%s should be Known", name)
		}
	}
}

// The slugs are counter-intuitive and were verified against the store, not
// guessed. Lock the surprising ones so nobody "corrects" them later.
func TestCounterIntuitiveSlugsAreNotSimplified(t *testing.T) {
	l := New("")
	for name, mustNotEndIn := range map[string]string{
		"PLA Silk+": "/pla-silk-multi-color", // a DIFFERENT product
		"PLA Basic": "/pla-basic",            // real slug has -filament
		"ABS":       "/abs",                  // real slug has -filament
	} {
		if got := l.URL(name); strings.HasSuffix(got, mustNotEndIn) {
			t.Errorf("%s resolved to the wrong-but-plausible slug %s", name, got)
		}
	}
}

// An unknown filament must land on the collection, never a guessed product
// slug that 404s, and never the bare store front.
func TestUnknownFilamentFallsBackToTheCollection(t *testing.T) {
	l := New("https://us.store.bambulab.com")
	got := l.URL("PLA Galaxy Sparkle Deluxe")
	want := "https://us.store.bambulab.com" + FallbackPath
	if got != want {
		t.Errorf("unknown filament -> %s, want %s", got, want)
	}
	if l.Known("PLA Galaxy Sparkle Deluxe") {
		t.Error("unknown filament must not report as Known")
	}
}

// The storefront is regional; the slugs are not.
func TestBaseIsConfigurableAndNormalised(t *testing.T) {
	if got := New("https://eu.store.bambulab.com/").URL("ABS"); got != "https://eu.store.bambulab.com/products/abs-filament" {
		t.Errorf("regional base not honoured, or trailing slash not trimmed: %s", got)
	}
	if got := New("").URL("ABS"); !strings.HasPrefix(got, "https://us.store.bambulab.com/") {
		t.Errorf("empty base should default to the US store, got %s", got)
	}
}

// A Linker that was never wired up must still produce absolute URLs. A
// relative "/products/..." renders as a broken link in Grafana and Slack
// rather than as an obvious misconfiguration.
func TestZeroValueLinkerIsUsable(t *testing.T) {
	var l Linker // deliberately not New()
	if got := l.URL("ABS"); got != DefaultBase+"/products/abs-filament" {
		t.Errorf("zero-value Linker produced %q, want an absolute URL", got)
	}
	if got := l.URL("Unknown"); !strings.HasPrefix(got, "https://") {
		t.Errorf("zero-value fallback produced %q, want an absolute URL", got)
	}
}

func TestNamesAreTrimmed(t *testing.T) {
	if New("").URL("  ABS  ") != New("").URL("ABS") {
		t.Error("surrounding whitespace should not defeat the lookup")
	}
}
