# bambu-sync

**A Prometheus exporter for the Bambu Lab Cloud API** — print history, RFID
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
| `bambu_spool_used_grams` | Lifetime consumption — **read the `ambiguous` label** |
| `bambu_prints_succeeded` / `bambu_prints_failed` | Counts in fetched history |
| `bambu_filter_hours` / `bambu_filter_percent` | Activated-carbon filter wear |
| `bambu_print_info` | Recent prints; value is grams, labels carry model, URL and slicer profile |
| `bambu_print_duration_seconds` | Wall-clock duration, same labels as `bambu_print_info` |
| `bambu_queue_item` | MakerWorld favourites; value is that design's print count |
| `bambu_makerworld_favorites` | Count of favourites |
| `bambu_device_info` | Printer identity — **the only accurate model source**, see below |
| `bambu_device_online` | 1 if Bambu's cloud considers the printer online |
| `bambu_prints_by_material` | Prints per material across the full fetched history |
| `bambu_filament_used_grams` / `_total` | Filament consumed, per material and overall |

### The cloud is the only place the printer is named correctly

`bambu_device_info` carries `product_name` — `"X2D"` on the printer this was
built against. That matters because the **LAN MQTT payload cannot express it**:
it carries no `module` list and a null `print.sn`, so MQTT exporters fall
through to a legacy `device.type` table where the X2D's `type == 1` is already
claimed by the X1C. They report an X2D as an X1C. The cloud API just says X2D.

> **`dev_access_code` is deliberately not carried.** The API returns the
> printer's LAN credential on every device. It is not mapped onto the struct at
> all — not mapped-and-unused — so it is discarded at unmarshal and can never
> reach a metric label. A test enforces this.

**Only RFID-registered spools appear.** Sealed spares are invisible to the API,
so "nothing running low" means nothing low *among what has been loaded*.
Weights are the AMS's running estimate, not a scale reading — they drift.

### Usage attribution is deliberately ambiguous in one case

Print history reports only the broad material (`PLA`), never the variant
(`PLA Matte` vs `PLA Basic`). Two spools of the same colour and material
therefore share one usage figure, and it belongs to the **group**, not to
either spool. `bambu_spool_remaining_*` is per-spool and exact; consumption is
not split. Attributing it to both would double-count.

`bambu_spool_used_grams` carries an **`ambiguous`** label saying exactly this:

```
bambu_spool_used_grams{name="PLA Basic",color="#000000",ambiguous="true"} 884.91
bambu_spool_used_grams{name="PLA Matte",color="#000000",ambiguous="true"} 884.91
```

Both report the group's 884.91 g, because there is no way to tell from the API
which spool consumed what. **Do not `sum()` across spools without filtering on
`ambiguous="false"`** — you would report 1770 g from 885 g of filament.

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

There is no API-key page in Bambu's UI. You log in as yourself and keep the
bearer token it hands back.

```sh
curl -sS -X POST https://api.bambulab.com/v1/user-service/user/login \
  -H 'Content-Type: application/json' \
  -d '{"account":"you@example.com","password":"..."}'
```

Most accounts get `{"loginType":"verifyCode"}` and an **emailed code** rather
than a token. Send it back with the code and no password:

```sh
curl -sS -X POST https://api.bambulab.com/v1/user-service/user/login \
  -H 'Content-Type: application/json' \
  -d '{"account":"you@example.com","code":"123456"}'
```

The response carries `accessToken` and `expiresIn` (seconds, ~3 months). Write
the token to your token file, and **today + `expiresIn`** as `YYYY-MM-DD` to
the expiry file.

> Mind your shell history with a password on the command line — use a heredoc
> or a file, not an inline `-d`, if that matters to you.

The token is an **opaque bearer string, not a JWT** — its expiry cannot be
decoded from it. That is the whole reason the expiry date is configured
separately.

There is **no working refresh endpoint**. Re-authentication is manual and
interactive: email, password, and an emailed 2FA code. When you re-auth,
**update both the token and its expiry date** — updating only the token leaves
the exporter warning against a stale date.

## Running

Secrets are read from **files**, not environment variables, and re-read every
cycle — so rotating a token does not need a restart. Three files, one per
secret; `slack-webhook` is only needed with alerting on.

```sh
mkdir -p ./secrets
printf '%s' 'YOUR_TOKEN'  > ./secrets/token
printf '%s' '2026-10-30'  > ./secrets/expires
```

### From source

Go 1.26+. No cgo, no build tags.

```sh
go build -o bambu-sync ./cmd/bambu-sync
BAMBU_TOKEN_FILE=./secrets/token BAMBU_TOKEN_EXPIRY_FILE=./secrets/expires ./bambu-sync
```

```sh
# Single sync, exposition text to stdout, no server. Good for a first run,
# and for diffing against whatever you are replacing.
BAMBU_TOKEN_FILE=./secrets/token BAMBU_TOKEN_EXPIRY_FILE=./secrets/expires ./bambu-sync -once
```

### Docker

```sh
docker run --rm -p 9110:9110 \
  -v "$PWD/secrets:/etc/bambu:ro" \
  -e TZ=America/Chicago \
  ghcr.io/chewrocca/bambu-sync:latest
```

Images are published for `linux/amd64` and `linux/arm64`, so this runs on a
Raspberry Pi as-is.

### docker-compose

```yaml
services:
  bambu-sync:
    image: ghcr.io/chewrocca/bambu-sync:latest
    restart: unless-stopped
    ports: ["9110:9110"]
    volumes:
      - ./secrets:/etc/bambu:ro
    environment:
      TZ: America/Chicago
      SYNC_DAILY_AT: "07:00"
      # Optional: read AMS humidity from a LAN MQTT exporter, if you run one.
      # HUMIDITY_METRICS_URL: http://bambulab-exporter:9109/metrics
      # SLACK_ALERTS_ENABLED: "true"
```

Then point Prometheus at `bambu-sync:9110`.

### Kubernetes

[`deploy/example.yaml`](deploy/example.yaml) has a Deployment, a Service with
scrape annotations, and a stub Secret:

```sh
kubectl apply -f deploy/example.yaml
```

Adapt the Secret to whatever you already use — External Secrets, Sealed
Secrets, SOPS. The only requirement is that the values arrive as **files**.

### Logs

JSON on stdout. Each completed run emits one record tagged
`"event":"run_summary"`; everything else is the surrounding trace. In Loki:

```
{namespace="bambu"} | json | event="run_summary"
```

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
