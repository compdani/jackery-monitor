# PocketBase preview

The preview includes the Go/PocketBase foundation, Svelte authentication and
Settings, the Jackery HTTP/MQTT live path, and durable energy tracking. Live has
fleet selection, power flow, output controls, capacity-weighted system SOC, and
six-hour history restored from SQLite. Energy includes totals, comparisons,
history, daily rollups, and electricity savings. Device includes capacity overrides
and battery packs. Forecast includes five-day SOC/solar/load charts, encrypted
location settings, weather caching, and prediction accuracy. Settings supports flat
and seasonal time-of-use electricity plans.

Automation, backup, and the other remaining domain features still run
only in Python. Keep the production stack until those ports and parity checks are
complete. Automated checks use mock devices and protocol fixtures; real Jackery
hardware and browser visual QA have not yet been validated.

## Route namespace

Following the updated implementation request, all custom feature routes use
`/japi/*`. PocketBase keeps its native `/api/*` routes and `/_/` administration.
There is no collision, so the app's runtime tunables remain named **settings**:

| Purpose | Path |
| --- | --- |
| Live snapshot, GET | `/japi/status?view_device_id=...` |
| Device list, GET | `/japi/devices` |
| Output control, POST | `/japi/set_output` |
| Pause/resume, POST | `/japi/pause_polling`, `/japi/resume_polling` |
| Refresh now, POST | `/japi/reconnect` |
| Cloud account, GET/POST | `/japi/auth/status`, `/japi/auth/credentials`, `/japi/auth/forget` |
| Energy, GET | `/japi/energy/totals`, `/japi/energy/history`, `/japi/energy/daily`, `/japi/energy/devices` |
| Capacity, GET/POST | `/japi/devices/capacity` |
| Latest stored packs, GET | `/japi/devices/battery_packs` |
| Verified import report, GET | `/japi/migration/status` |
| Forecast and accuracy, GET | `/japi/forecast`, `/japi/forecast/accuracy` |
| Sunrise/sunset daily SOC, GET | `/japi/daily_summary` |
| Location, GET/POST; city search, GET | `/japi/location`, `/japi/location/geocode` |
| Electricity plan, GET/POST; savings, GET | `/japi/cost/plan`, `/japi/cost/savings` |
| Runtime settings, GET/POST | `/japi/settings` |
| Create first dashboard user | `/api/collections/users/records` |
| Dashboard login | `/api/collections/users/auth-with-password` |
| Refresh dashboard token | `/api/collections/users/auth-refresh` |
| Change own password | `/api/collections/users/records/{id}` (PATCH) |
| PocketBase instance settings | `/api/settings` (superuser only) |
| PocketBase health | `/api/health` |
| Administration | `/_/` |
| Live custom WebSocket | `/ws?token=...` |

Future automation, backup, and advisor
routes must use `/japi/` too. The existing Python/Expo `/api/` contract stays in
place until its coordinated cutover; do not change the SDK base URL to `/japi`.
The SDK calls PocketBase's native `/api/collections` routes itself.

## Run on the host

Requires Go 1.26.5 and Node 22.

```sh
npm ci --prefix ui
npm run build --prefix ui
go run ./cmd/jackery serve --mock --http=127.0.0.1:8124
```

Open <http://127.0.0.1:8124>. `--mock` (or `JACKERY_MOCK=1`) provides simulated
stations without any cloud calls. Omit it for real hardware, then save the Jackery
account in Settings. Local data defaults to `data/pb_data` (ignored by Git).
Use `JACKERY_DATA_DIR=/some/directory` to change the parent or PocketBase's `--dir`
to select an explicit database directory. This does not read the production DB.

For UI development, run the Go command above and, in another terminal:

```sh
npm run dev --prefix ui
```

Vite listens on port 5173 and proxies `/api`, `/japi`, `/ws`, and `/_` to 8124.
The service worker is registered only by production builds and excludes all four
of those paths from caching.

## First account

Open the app to be routed to `/setup`. Choose a username (letters, digits,
underscores, dots, and hyphens) and a password of at least 12 characters.
Setup creates a dashboard `users` record and a matching `_superusers` record in
one transaction. Concurrent setup requests cannot create two owners. Additional
REST signups and owner deletion are disabled.

Dashboard login uses the username; administration uses `username@local.invalid`
and the initial password. Setup via the native collection API can optionally
supply a real email, which is then the administrator's email too. After setup,
the two passwords are managed independently. The dashboard never stores an
administrator token. `/japi/*` accepts only `users` tokens, including when a
caller has a valid superuser token.

If `auth.json` exists in the parent of `pb_data`, setup prefills its old username.
It never imports the old password hash or edits that file. Cloudflare Access and setup restore are pending. The legacy energy/config
import is described below.

## Cloud connection and encrypted credentials

The US cloud protocol matches the Python client, including encrypted login and
MQTT broker credentials. The runtime polls every account device, merges addressed
MQTT deltas with HTTP fields, preserves newer MQTT values during in-flight HTTP
requests, and continues with healthy devices when another is offline. Contested
sessions back off from the configured cooldown up to an hour; Resume explicitly
reclaims the session. Pause affects HTTP polling while MQTT updates may continue.

A command's success means the broker acknowledged it. The dashboard waits for
reported device state before changing a real output indicator. Commands are
rejected during an explicit pause. The WebSocket uses native `users` tokens and
rechecks them during the stream, closing sessions after password changes or expiry.

Saved credentials use the existing `jackery-creds.json` AES-256-GCM envelope and
`.jackery-creds.key`, both under the data root. Existing encrypted files can be
read in place; legacy plaintext files require re-entry through Settings. Passwords,
cloud tokens, and MQTT keys are never returned in snapshots or stored in collections.
Forgetting credentials removes the ciphertext but retains the shared key. Missing
or damaged keys are reported, never silently regenerated during a read.

Supported host environment overrides:

- `JACKERY_EMAIL` + `JACKERY_PASSWORD`: read-only account override; Settings cannot
  replace or forget an environment-pinned account.
- `JACKERY_CREDS_FILE`, `JACKERY_CREDS_KEY_FILE`: explicit encrypted file/key paths.
- `JACKERY_MQTT_CA_FILE`: PEM CA bundle for the Jackery broker, using a path visible
  inside the container (for example `/data/jackery-ca.pem`).
- `JACKERY_MQTT_INSECURE=1`: explicit compatibility with the Python client's
  unverified broker TLS. Prefer a trusted CA bundle. Certificate verification is
  enabled by default; a broker using an untrusted private CA will otherwise leave
  MQTT unavailable, with HTTP monitoring continuing and the error shown in Live.

Do not run the Python and Go runtimes against the same cloud account simultaneously:
both HTTP sessions and the account's MQTT client ID can contend. Use mock mode for
parallel preview work. No real account or physical hardware was used for automated
validation.

## Energy storage and legacy migration

Minute samples, pack history, forecast/decision archives, and weather tables live
in PocketBase's SQLite alongside the collections. Integration uses trapezoids,
ignores duplicate or out-of-order readings, and starts a new baseline after a
restart or a gap longer than ten minutes. Stale unchanged telemetry is not counted
again. Pack snapshots include explicit empty reports so disconnected packs do not
reappear from old cached rows. The shared `models.json` supplies main and pack
capacities; Device allows a persistent total-capacity override.

On non-mock startup, `energy.db` in the parent of `pb_data` is imported before
polling starts. Use a **fresh PocketBase data directory** for cutover, and stop
Python before pointing Go at the production data volume. Test with a copy first.
Do not reuse a mock directory for migration into real history.

- The source is opened read-only with a consistent transaction, including WAL
  data. It is never renamed, deleted, or used as the destination.
- Destination inserts and the verified-import marker commit atomically. Row
  counts and input/output/solar/grid/diversion totals are checked before commit.
- Imports are idempotent. A nonempty destination or unsupported source table/column
  stops startup with an error rather than merge or silently discard data.
- Old databases lacking newer energy columns receive destination defaults.
- The old `devices` SQL table maps to `energy_devices` to avoid PocketBase's
  collection table. Original advisor rows are retained in private
  `legacy_algorithm_suggestions` and `legacy_algorithm_changes` tables until the
  advisor collection/API port; their references and IDs are preserved.
- Existing `settings.json`, `automation.json`, `smart_charge.json`,
  `solar_charge.json`, `kasa_devices.json`, `location.json`, and `cost.json` are
  copied into empty configuration collections without overwriting current records.
  Controller configuration remains a lossless JSON record until its domain port.
- `auth.json` contributes only the setup username. A new dashboard password is
  required. Encrypted credential files remain separate and readable in place.

Read `/japi/migration/status` with a dashboard token for the import report. Confirm
the Energy lifetime totals after login; retain the originals as rollback copies.
Imported forecast and weather rows are available to the new forecast APIs.
Automation and parameter records remain available for the later controller/advisor ports.

## Forecast, location, and cost

Set the installation location in Forecast using city search, browser geolocation,
or manual coordinates. The location collection stores the Python-compatible
AES-GCM envelope; legacy plaintext locations are encrypted at startup. Keep the
existing `.jackery-creds.key`, or set `JACKERY_CREDS_KEY_FILE` (preview) /
`JACKERY_AT_REST_KEY_FILE` (legacy). Missing or invalid keys stop the upgrade
rather than replace encrypted coordinates. The original location file remains intact.

The Open-Meteo client requests hourly GHI and cloud cover with Unix timestamps;
see [forecast API documentation](https://open-meteo.com/en/docs). City lookup uses
[its geocoding API](https://open-meteo.com/en/docs/geocoding-api). Weather requests
have a 15-second timeout, a one-hour cache, and five-minute failure backoff.
Saved future weather survives restarts. If it expires, a clearly labeled synthetic
forecast can use recent per-hour medians, requiring at least two samples for every
hour of day. Changing location clears the previous site's weather and observations.
Mock Jackery devices make no cloud calls; configuring a location still enables
Open-Meteo requests.

The forecast needs a 24-hour history span. Its solar regression prefers clear
hours with battery headroom, learns site shading, and caps solar against recent
production. Load profiles use the installation timezone (including DST), net out
historical diversion, and use neighboring hours for gaps. Drain fits use weighted
system SOC, skip missing pack snapshots, and include per-pack baseline draw.
Charge efficiency and the BMS ceiling are fitted from history. These are model
estimates, not hardware guarantees. Grid charging/diversion controllers are not
yet active in Go; the dashboard forecast shows natural solar and demand.

A background worker starts after 90 seconds and records forecasts hourly for each
live device. The first prediction in each hour is retained; opening the tab cannot
rewrite it. Accuracy compares saved predictions with observed SOC and separates
Go model runs from imported Python predictions. Daily sunset/sunrise predictions
are retained and actuals backfilled as samples arrive. These first-snapshot rules
are intentional changes from Python's within-hour overwrite behavior.

Electricity plans support flat rates and seasonal, midnight-wrapping TOU slots,
with first-match ordering. Savings credit output and subtract measured AC input,
using the installation's timezone at each sample. Changing the plan recalculates
historical savings. The API retains the legacy April 2026 preset examples; the
Settings editor uses rates entered from the user's bill.

## Standalone preview container

```sh
JACKERY_MOCK=1 docker compose -f docker-compose.pocketbase.yml up --build -d
```

This is a separate Compose project (`jackery-pocketbase-preview`) with one service,
host networking, port 8124, a dedicated preview volume, and no Watchtower or Docker
socket mount. Set `JACKERY_PREVIEW_PORT` to change the port. The runtime is non-root
and includes `smbclient`, `rsync`, and SSH for later job ports.

The required image name is `jackerymonitor:latest` with `pull_policy: never`.
**Building the preview retags that local image.** Run it on a development host;
do not restart a production project using the same tag after building a preview.
The default Dockerfile, Compose files, and GHCR publish workflow still build and
run Python. They will switch together once the remaining Go domain ports are ready.

At final cutover, the NAS update flow will be Container Manager → Image → Get
latest version, retag the downloaded GHCR image to `jackerymonitor:latest`, then
restart/recreate the project. A host Task Scheduler task can pull the approved
image, retag it, and run `docker compose up -d --force-recreate`. Compose must not
pull (`pull_policy: never` is intentional); no updater sidecar or `docker.sock`
mount is needed. Do not use `pocketbase update` for this custom binary.

## Validation

```sh
go test -race ./...
go build ./cmd/jackery
npm run check --prefix ui
npm run build --prefix ui
```

The Go API tests cover failed-setup rollback, simultaneous signup, native login,
admin/user token separation, password changes and token invalidation, settings
clamping and persistence, and SPA fallback. Live tests also cover Python crypto
vectors, a fake cloud HTTP transport, an in-memory MQTT broker with reconnection
and PUBACKs, fleet isolation, HTTP/MQTT races, session cooldowns, and actual WebSocket
selection and token revocation. Energy tests cover duplicate/out-of-order samples,
gaps and restarts, pack weighting, capacity bounds, local dates, WAL-aware legacy
imports, rollback on unknown columns, and idempotent config import. A smoke test
with a database produced by Python verified matching energy totals and pack SOC.
Forecast tests compare Go output with fixed vectors produced by the original
Python forecaster (solar, no-panel, and multi-pack cases), and cover drain fits,
reserve simulation, encrypted location upgrades, seasonal TOU rates, weather
outages/restarts/site changes, and immutable prediction snapshots. A built-binary smoke test with seeded
history/weather verified 120-hour forecasts, pack capacity, encrypted coordinates,
daily summaries, and savings without contacting weather services.
The separate PocketBase workflow runs
these checks alongside the existing Python CI.

## Remaining milestones

1. Smart/solar charge and inverter watchdog.
2. Kasa discovery, KLAP, automation, and reconciliation.
3. Backup/restore and AI jobs; Cloudflare Access authentication.
4. Complete dashboard parity, migrate Expo tokens/routes, run parity tests, switch
   production Compose/publishing, and only then remove Python and `web/`.

The remaining controller/advisor configuration collections provide private
`key` + JSON `data` storage until their feature APIs and validation are ported. Existing Watchtower removal and default Compose consolidation
remain part of production cutover, not this preview.
