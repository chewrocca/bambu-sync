// Package store maps filament names to Bambu Lab store product pages, so a
// spool running low links to the thing you would actually reorder rather than
// to a shop front you then have to search.
package store

import "strings"

// catalog maps the filament name AS THE BAMBU API REPORTS IT to a verified
// store product slug.
//
// Every slug here was resolved against the store's own sitemap and confirmed
// to return HTTP 200 with a matching page title. Do NOT add a slug by
// guessing it — several are not what you would predict:
//
//	PLA Silk+  is "pla-silk-upgrade", NOT "pla-silk-multi-color" (a different product)
//	PLA Basic  is "pla-basic-filament", not "pla-basic"
//	ABS        is "abs-filament", not "abs"
//
// Anything not listed falls back to the filament collection rather than
// inventing a slug and 404ing.
var catalog = map[string]string{
	"PLA Basic":  "pla-basic-filament",
	"PLA Matte":  "pla-matte",
	"PLA Silk+":  "pla-silk-upgrade",
	"PLA Pure":   "pla-pure",
	"PLA Wood":   "pla-wood",
	"PETG Basic": "petg-basic",
	"ABS":        "abs-filament",
}

// FallbackPath is where unknown filaments point: the filament collection, not
// the store root. A shopper who lands on the collection is one click from what
// they wanted; one who lands on the store front is lost.
const FallbackPath = "/collections/3d-printer-filament"

// DefaultBase is the US storefront, used when none is configured.
const DefaultBase = "https://us.store.bambulab.com"

// Linker builds product URLs against a regional store.
//
// Base is configurable because the slugs are global but the storefront is not
// — eu.store.bambulab.com serves the same products.
//
// The zero value is usable and yields DefaultBase: a Linker that had not been
// wired up would otherwise emit RELATIVE urls like "/products/abs-filament",
// which render as broken links rather than as an obvious misconfiguration.
type Linker struct{ Base string }

func (l Linker) base() string {
	if b := strings.TrimRight(strings.TrimSpace(l.Base), "/"); b != "" {
		return b
	}
	return DefaultBase
}

// New returns a Linker, defaulting to the US store.
func New(base string) Linker {
	return Linker{Base: strings.TrimRight(strings.TrimSpace(base), "/")}
}

// URL returns the product page for a filament name, or the filament collection
// if the name is not in the verified catalog.
//
// Colour is deliberately NOT encoded. The store selects colour as an on-page
// variant, and the query parameter that would preselect it has not been
// verified — the same standard the slugs are held to. A link to the right
// product in the wrong colour is useful; a link to a 404 is not.
func (l Linker) URL(filamentName string) string {
	if slug, ok := catalog[strings.TrimSpace(filamentName)]; ok {
		return l.base() + "/products/" + slug
	}
	return l.base() + FallbackPath
}

// Known reports whether a filament name resolves to a specific product. Useful
// for telling "we link precisely" from "we link to the collection".
func (l Linker) Known(filamentName string) bool {
	_, ok := catalog[strings.TrimSpace(filamentName)]
	return ok
}
