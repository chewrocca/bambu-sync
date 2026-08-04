# Metrics reference

Every metric this exporter publishes, what it means, and — where it matters —
what will bite you if you read it the obvious way.

**Naming convention.** `bambu_*` is the **cloud plane**: this exporter, reading
your Bambu *account*. `bambulab_*` is the **device plane**: the LAN MQTT
exporters, reading the *printer*. Different data sources, same machine. They do
not overlap and neither replaces the other.

---

## Exporter health

| Metric | Type | Labels | |
| --- | --- | --- | --- |
| `bambu_sync_up` | gauge | | 1 if the last **full** sync succeeded |
| `bambu_sync_last_run_timestamp_seconds` | gauge | | Unix time of the last full sync attempt |
| `bambu_sync_last_exit_code` | gauge | | 0 ok · 1 error · 2 token expired |
| `bambu_sync_duration_seconds` | gauge | | Wall-clock duration of the last full sync |
| `bambu_sync_build_info` | gauge | `version` | Always 1 |
| `bambu_api_errors_total` | **counter** | `endpoint` | Request failures, by endpoint |
| `bambu_print_probe_last_run_timestamp_seconds` | gauge | | Last fast current-print poll |

> **`bambu_sync_up` is written only by the full sync.** The 30-minute
> current-print poll deliberately never touches it. A poll running 48×/day
> would otherwise hold the signal green straight through a total failure of the
> daily sync — the exact silent failure this metric exists to catch.

> **Absence is meaningful.** `bambu_sync_up == 0` means the sync ran and
> failed. The metric being *missing* means the exporter is not running at all.
> Alert on both.

`bambu_api_errors_total` is labelled by endpoint because the API is
undocumented and can break one endpoint at a time. "Tasks is failing but
filament is fine" is the diagnosis that saves an hour.

---

## Authentication

| Metric | Type | |
| --- | --- | --- |
| `bambu_token_expires_timestamp_seconds` | gauge | When the cloud token expires |

Days remaining: `(bambu_token_expires_timestamp_seconds - time()) / 86400`

> The token is an **opaque bearer string, not a JWT** — its expiry cannot be
> decoded from it, so this comes from a configured date, not the token. If you
> rotate the token and forget the date, this counts down against a stale value
> while everything works fine. Update both together.

---

## Filament

| Metric | Type | Labels | |
| --- | --- | --- | --- |
| `bambu_spool_remaining_grams` | gauge | `name` `material` `color` `store` | Remaining weight |
| `bambu_spool_remaining_percent` | gauge | + `grams` `capacity` `loaded` `ams_slot` | Same, as % of capacity |
| `bambu_spool_used_grams` | gauge | `name` `material` `color` `store` **`ambiguous`** | Lifetime consumption — **read the caveat** |
| `bambu_spool_depleted` | gauge | `name` `material` `color` | 1 = a finished roll |

### ⚠️ `bambu_spool_used_grams` is not always summable

Print history reports only the **broad material** (`PLA`), never the variant
(`PLA Matte` vs `PLA Basic`). Two spools sharing a colour and material are
therefore indistinguishable in the usage feed, and the figure belongs to the
**group**:

```
bambu_spool_used_grams{name="PLA Basic",color="#000000",ambiguous="true"} 884.91
bambu_spool_used_grams{name="PLA Matte",color="#000000",ambiguous="true"} 884.91
```

Both report the group's 884.91 g. **Summing without filtering reports 1,770 g
from 885 g of filament.** An earlier implementation did exactly that.

```promql
sum(bambu_spool_used_grams{ambiguous="false"})   # safe
sum(bambu_filament_used_grams_total)             # safer — see below
```

`bambu_spool_remaining_*` is per-spool and exact; only *consumption* is
ambiguous.

> **Only RFID-registered spools appear.** Sealed spares the AMS has never
> scanned are invisible, so "nothing running low" means nothing low *among what
> has been loaded*. Weights are the AMS's running estimate, not a scale
> reading — they drift.

---

## Prints

| Metric | Type | Labels | |
| --- | --- | --- | --- |
| `bambu_prints_succeeded` / `bambu_prints_failed` | gauge | | Counts in the fetched history |
| `bambu_print_info` | gauge | `id` `name` `url` `date` `status` `material` `profile` | Value is **grams used** |
| `bambu_print_duration_seconds` | gauge | *(same as above)* | Wall-clock duration |
| `bambu_prints_by_material` | gauge | `material` | Prints using this material |
| `bambu_filament_used_grams` | gauge | `material` | Consumption per material |
| `bambu_filament_used_grams_total` | gauge | | Total consumption |

`bambu_print_info` and `bambu_print_duration_seconds` share a label set
deliberately, so they join cleanly. **`id` is a uniqueness key**, not something
to display: without it, two prints of the same model on the same day with the
same filament collapse into one row.

`profile` is the slicer plate description (`0.2mm layer, 3 walls, 25% infill`)
— distinct from the model name, and usually what you want when comparing two
runs of the same thing.

**Per-material aggregates are safe to sum.** They aggregate *prints*, so the
colour+material ambiguity above never arises — each gram is counted once, by
the job that consumed it. A multi-material print counts once against *each*
material, so `bambu_prints_by_material` answers "how often do I reach for
PETG", not "partition my prints".

> **Capped at 20 by default** (`PRINT_INFO_LIMIT`). Every print is a distinct
> label set and history only grows, so an uncapped version leaks cardinality
> forever. The aggregates run over the **full** fetched history (100 by
> default), which is how the detail the cap discards is recovered for free.

---

## Consumables

| Metric | Type | |
| --- | --- | --- |
| `bambu_filter_hours` | gauge | Cumulative print-hours on the activated-carbon filter |
| `bambu_filter_percent` | gauge | The same, as % of the replacement interval |

Interval defaults to 1,440 print-hours (`FILTER_INTERVAL_HOURS`).

---

## Printer identity

| Metric | Type | Labels | |
| --- | --- | --- | --- |
| `bambu_device_info` | gauge | `name` `serial` `product_name` `model_name` `structure` | Always 1 |
| `bambu_device_online` | gauge | `name` `serial` | Cloud-side reachability |

**`product_name` is the only accurate model identification available.** The LAN
MQTT payload carries no `module` list and a null `print.sn`, so MQTT exporters
fall through to a legacy `device.type` table where the X2D's `type == 1` is
already claimed by the X1C — they report an X2D as an X1C. The cloud API just
says `X2D`.

`bambu_device_online` answers a **different question** from the MQTT exporter's
`bambulab_printer_up`. That one means "can I reach it on the LAN"; this means
"does Bambu's cloud think it is up". Together they separate *the printer is
off* from *my network is broken*.

> **`dev_access_code` is never exported.** The API returns the printer's LAN
> credential on every device. It is not mapped onto the struct at all —
> discarded at unmarshal — so it cannot reach a label and land in your metrics
> backend in plaintext, indexed, indefinitely. A test enforces this.

---

## MakerWorld

| Metric | Type | Labels | |
| --- | --- | --- | --- |
| `bambu_queue_item` | gauge | `name` `url` `creator` | Value is that design's **public** print count |
| `bambu_makerworld_favorites` | gauge | | Number of favourites |

`bambu_queue_item` is named after a note that no longer exists — it is your
favourites list, treated as a print queue. The name is kept for series
continuity rather than accuracy. Capped at 25 (`FAVORITES_LIMIT`), same
cardinality reasoning as prints.

---

## Useful queries

```promql
# Not running at all, versus running and failing
absent(bambu_sync_up) or bambu_sync_up == 0

# Days until the token dies
(bambu_token_expires_timestamp_seconds - time()) / 86400

# Spools to reorder
bambu_spool_remaining_grams <= 250

# Success rate over the fetched history
bambu_prints_succeeded / (bambu_prints_succeeded + bambu_prints_failed)

# Average print duration
avg(bambu_print_duration_seconds)

# Which endpoint is failing
rate(bambu_api_errors_total[1h]) > 0
```

## Alerting

The exporter sends its own Slack alerts (low filament, humidity, new failed
prints, filter wear, token expiry) — see the README. If you would rather alert
in Grafana or Alertmanager, everything above is expressible as a rule, and you
can run with `SLACK_ALERTS_ENABLED=false`.

Humidity comes from the **MQTT exporter**, not this one: the cloud API has no
humidity data at all. This exporter reads it over the network to make alerting
decisions, but does not republish it.
