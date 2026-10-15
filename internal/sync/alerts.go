package sync

import (
	"fmt"
	"strings"
	"time"

	"github.com/chewrocca/bambu-sync/internal/config"
	"github.com/chewrocca/bambu-sync/internal/humidity"
	"github.com/chewrocca/bambu-sync/internal/notify"
	"github.com/chewrocca/bambu-sync/internal/stock"
)

// sanitise strips characters that Prometheus label values cannot carry
// unescaped. Model and filament names are arbitrary user text from MakerWorld,
// so they are never trusted into a label as-is. Replacing rather than escaping
// keeps the quoting unambiguous.
func sanitise(s string) string {
	return strings.NewReplacer("\\", " ", "\"", " ", "\n", " ", "\r", " ").Replace(s)
}

// buildAlerts decides what is worth interrupting a human for.
//
// Silence is the normal state. Every condition here is actionable: something
// needs buying, drying, replacing, or re-authenticating.
func (s *Syncer) buildAlerts(low []stock.Spool, hum humidity.Reading, humState string,
	failed int, filterPct float64) []notify.Alert {

	var alerts []notify.Alert

	if len(low) > 0 {
		var b strings.Builder
		for _, sp := range low {
			// Link each spool to ITS product page. The point of the alert is
			// that you can reorder from it without going looking.
			fmt.Fprintf(&b, "• <%s|%s> (%s) — %.0f g left\n",
				s.Store.URL(sp.Name), sp.Name, sp.Material, sp.Left)
		}
		alerts = append(alerts, notify.Alert{
			Title: fmt.Sprintf("*Filament low* (%d at or below %.0f g)", len(low), s.Cfg.LowGrams),
			Body:  b.String(),
		})
	}

	switch humState {
	case "critical":
		alerts = append(alerts, notify.Alert{
			Title: fmt.Sprintf("*AMS humidity critical — %.0f%% RH*", hum.Percent),
			Body: "Filament degrades within hours at this level. " +
				"Run a drying cycle and replace the desiccant.",
		})
	case "warn":
		alerts = append(alerts, notify.Alert{
			Title: fmt.Sprintf("*AMS humidity elevated — %.0f%% RH*", hum.Percent),
			Body:  "The desiccant is loading. Worth replacing before it matters.",
		})
	}

	// Only NEW failures. The baseline lives in memory and is seeded on the
	// first run after start without alerting, so a restart is silent.
	if s.seeded && failed > s.prevFailed {
		alerts = append(alerts, notify.Alert{
			Title: fmt.Sprintf("*Print failed* (%d new)", failed-s.prevFailed),
			Body:  "Check the printer and the recent prints panel.",
		})
	}

	if filterPct >= 90 {
		alerts = append(alerts, notify.Alert{
			Title: fmt.Sprintf("*Air filter at %.0f%% of its %.0f-hour interval*", filterPct, s.Cfg.FilterHours),
			Body:  "Time to replace the activated carbon filter.",
		})
	}

	if a, ok := s.tokenExpiryAlert(); ok {
		alerts = append(alerts, a)
	}
	return alerts
}

// tokenExpiryAlert warns ahead of expiry. This is the PREDICTIVE half of token
// alerting: the stored date can be wrong — a password change revokes the token
// early — so it does not replace alerting on an actual 401, it precedes it.
func (s *Syncer) tokenExpiryAlert() (notify.Alert, bool) {
	raw, err := config.ReadSecret(s.Cfg.TokenExpiryFile)
	if err != nil {
		return notify.Alert{}, false
	}
	expiry, err := time.ParseInLocation("2006-01-02", raw, s.Cfg.Location)
	if err != nil {
		return notify.Alert{}, false
	}

	days := int(time.Until(expiry).Hours() / 24)
	if days > s.Cfg.TokenWarnDays {
		return notify.Alert{}, false
	}

	title := fmt.Sprintf("*Bambu token expires in %d days* (%s)", days, raw)
	if days < 0 {
		title = fmt.Sprintf("*Bambu token expired %d days ago* (%s)", -days, raw)
	}
	return notify.Alert{Title: title, Body: s.tokenRenewalBody()}, true
}

// tokenRenewalBody is the shared explanation under both token alerts: which
// token this is, how to get a new one, and where to put it. The first two are
// the same everywhere; the last is only knowable here as the files the
// exporter reads, so TOKEN_RENEWAL_NOTE lets a deployment name the actual
// source of truth behind them.
func (s *Syncer) tokenRenewalBody() string {
	var b strings.Builder
	b.WriteString("*Which token:* the Bambu Lab *cloud API bearer token* this exporter " +
		"uses to read your account (print history, filament stock, favourites). " +
		"Not the printer's LAN access code.\n")
	b.WriteString("*How to renew:* re-authentication is manual — the refresh endpoint " +
		"does not work. Log in again at api.bambulab.com with your Bambu account " +
		"email and password, then the emailed verification code; the response " +
		"carries `accessToken` and `expiresIn`. See the README's Authentication " +
		"section for the exact requests.\n")
	fmt.Fprintf(&b, "*Where to update it:* the exporter reads the token from `%s` and the "+
		"expiry date from `%s`. Update whatever secret feeds those files. ",
		s.Cfg.TokenFile, s.Cfg.TokenExpiryFile)
	b.WriteString("Update BOTH the token and its expiry date (today + `expiresIn`, as " +
		"YYYY-MM-DD), or this warning will keep firing against a stale date.")
	if note := strings.TrimSpace(s.Cfg.TokenRenewalNote); note != "" {
		b.WriteString("\n*For this deployment:* ")
		b.WriteString(note)
	}
	return b.String()
}
