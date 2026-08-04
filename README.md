# bambu-sync

Prometheus exporter for the **Bambu Lab Cloud API** — print history, RFID
filament inventory, and MakerWorld favourites — with Slack alerting for the
conditions worth acting on.

It is a **companion to a LAN MQTT exporter, not a replacement.** Those read the
*device*: temperatures, fans, print progress, live AMS state. This reads the
*account*: what you have printed, which spools exist and how full they are, and
what you have bookmarked. Same printer, different data source.

> **Unofficial API.** `api.bambulab.com` is reverse-engineered and undocumented.
> Bambu can change or break it without notice. This tool is **read-only** — it
> never starts a print and never touches the printer's queue. Field mappings
> here were confirmed against an X2D; other models may differ.

## Metrics

Naming convention: **`bambu_*` is the cloud plane** (this exporter).
**`bambulab_*` is the device plane** (the MQTT exporters). Not self-evident, so
worth stating.

| Metric | Description |
| --- | --- |
| `bambu_sync_up` | 1 if the last full sync succeeded |
| `bambu_sync_last_run_timestamp_seconds` | Unix time of the last full sync |
| `bambu_sync_last_exit_code` | 0 ok, 1 error, 2 token expired |
| `bambu_token_expires_timestamp_seconds` | When the cloud token expires |
| `bambu_print_probe_last_run_timestamp_seconds` | Last fast current-print poll |
| `bambu_spool_remaining_grams` | Remaining filament per registered spool |
| `bambu_spool_remaining_percent` | Same, as a percentage of capacity |
| `bambu_prints_succeeded` / `bambu_prints_failed` | Counts in fetched history |
| `bambu_filter_hours` / `bambu_filter_percent` | Activated-carbon filter wear |
| `bambu_print_info` | Recent prints; value is grams, labels carry model + URL |
| `bambu_queue_item` | MakerWorld favourites; value is that design's print count |
| `bambu_makerworld_favorites` | Count of favourites |

**Only RFID-registered spools appear.** Sealed spares are invisible to the API,
so "nothing running low" means nothing low *among what has been loaded*.
Weights are the AMS's running estimate, not a scale reading — they drift.

### Usage attribution is deliberately ambiguous in one case

Print history reports only the broad material (`PLA`), never the variant
(`PLA Matte` vs `PLA Basic`). Two spools of the same colour and material
therefore share one usage figure, and it belongs to the **group**, not to
either spool. `bambu_spool_remaining_*` is per-spool and exact; consumption is
not split. Attributing it to both would double-count.

## Configuration

All configuration is environment variables; nothing site-specific is compiled
in. Secrets are read from **files**, not values, and re-read every cycle — so a
rotated Kubernetes Secret is picked up without a restart.

| Variable | Default | |
| --- | --- | --- |
| `BAMBU_TOKEN_FILE` | `/etc/bambu/token` | Bearer token |
| `BAMBU_TOKEN_EXPIRY_FILE` | `/etc/bambu/expires` | `YYYY-MM-DD`; see below |
| `BAMBU_API_BASE_URL` | `https://api.bambulab.com/v1` | The `/v1` is mandatory |
| `SLACK_ALERTS_ENABLED` | `false` | Explicit; ships disabled |
| `SLACK_WEBHOOK_FILE` | `/etc/bambu/slack-webhook` | Incoming webhook URL |
| `SLACK_USERNAME` / `SLACK_ICON_EMOJI` | `Bambu Sync` / `:printer:` | |
| `HUMIDITY_METRICS_URL` | *(unset — disabled)* | e.g. `http://bambulab-exporter:9109/metrics` |
| `HUMIDITY_METRIC_NAME` | `bambulab_ams_unit_humidity` | |
| `SYNC_DAILY_AT` | `07:00` | Local time, honours `TZ` across DST |
| `SYNC_FAST_INTERVAL` | `30m` | Current-print refresh |
| `LOW_GRAMS` | `250` | Reorder threshold |
| `HUMIDITY_WARN` / `HUMIDITY_CRIT` | `30` / `45` | % RH |
| `FILTER_INTERVAL_HOURS` | `1440` | |
| `TOKEN_WARN_DAYS` | `14` | Warn this far ahead of expiry |
| `FAVORITES_LIMIT` / `PRINT_INFO_LIMIT` | `25` / `20` | Cardinality caps |
| `LISTEN_ADDR` | `:9110` | |
| `TZ` | `UTC` | |

Humidity thresholds sit deliberately **above** Bambu's stated <20% RH target:
the AMS reads ~21% in normal operation, so alerting at 20% would fire daily for
a condition that is fine.

`FAVORITES_LIMIT` and `PRINT_INFO_LIMIT` are cardinality caps, not display
limits. Every entry is a distinct label set and both lists only grow.

## Authentication

The token is an **opaque bearer string, not a JWT** — its expiry cannot be
decoded from it. That is why the expiry date is configured separately: it comes
from the login response's `expiresIn` (~3 months).

There is **no working refresh endpoint**. Re-authentication is manual and
interactive: email, password, and an emailed 2FA code. When you re-auth,
**update both the token and its expiry date** — updating only the token leaves
the exporter warning against a stale date.

## Running

```sh
bambu-sync              # serve /metrics, run on schedule
bambu-sync -once        # single sync, print exposition text, exit
```

`-once` prints the metrics to stdout without serving, which makes it easy to
diff against another implementation before trusting this one.

Logs are JSON on stdout. Each completed run emits one record tagged
`"event":"run_summary"` for easy isolation in a log aggregator.

## Alerting

Silent by default: alerts fire only when something is actionable — filament
low, AMS humid, a new failed print, the filter due, or the token expiring.

Two behaviours worth knowing:

- **Alerts are evaluated only on the scheduled daily run**, never on startup or
  the fast poll. Most conditions are *standing* — a spool at 200 g is low on
  every run until you swap it — so alerting on every cycle would re-ping on
  every restart.
- **The failed-print baseline is in memory** and is seeded on the first run
  after start *without* alerting, so a restart is silent rather than replaying
  history.

## Licence

MIT. See [LICENSE](LICENSE).

Field mappings were derived from observation and from the community
reverse-engineering notes at
[Doridian/OpenBambuAPI](https://github.com/Doridian/OpenBambuAPI). No code was
taken from other Bambu projects.
