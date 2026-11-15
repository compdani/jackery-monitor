# Go + PocketBase migration status

This is a **partial implementation**, not a completed replacement for the
Python backend. Python source remains intact.
The device libraries remain separate from the core through adapters in
`internal/devices`. This continuation does not modify their implementation.

## Runtime and decisions

- Module: github.com/compdani/jackery-monitor. Go 1.27 is required by
  github.com/pocketbase/pocketbase v0.40.6, resolved with go get @latest.
  This is the latest version returned for the requested GitHub module path.
  PocketBase's current website advertises a newer release under a different
  module host; this implementation uses the requested module.
- cmd/jackery embeds PocketBase, applies compiled Go migrations, and starts
  the compatibility HTTP layer and context-cancelled bridge poller.
- Run: go run ./cmd/jackery serve --http=127.0.0.1:8000 --dir=./pb_data
  PocketBase's --http flag controls listening; old PORT is not consumed.
- Public assets come from JACKERY_UI_DIR, or pb_public, then ui/build,
  then web/next. The current Svelte adapter actually builds web/next;
  neither frontend source nor its API base path was changed.
- Application users are PocketBase auth records. Browser cookies still use
  jackery_session; login/setup still return {ok, username, token}.
  Bearer auth and WebSocket ?token= work. Sessions last 30 days.
  Username login, six-character password minimum, setup lockout after the
  first account, and password change are implemented. Password changes
  invalidate PocketBase tokens. Logout clears the cookie.
- Existing Python HMAC sessions are not imported. `import-config` preserves
  legacy auth.json accounts via the password transition documented below.
  PocketBase superusers remain separate.
  Cloud credential writes now require app authentication.
- /api/settings conflicts with PocketBase's own route. A root middleware
  intercepts legacy API paths before native route middleware. Native
  collection/file/realtime/health/backup/log/cron endpoints retain PocketBase
  authorization. All domain collections have locked API rules.
- AES-256-GCM envelopes use Python's {v,alg,nonce,ct,tag} format. Existing
  32-byte keys are readable; invalid keys fail instead of silently regenerating.
  New key files are created exclusively with mode 0600. The default Go key
  lives inside the PocketBase data directory. Override
  JACKERY_AT_REST_KEY_FILE to use an existing Python key.
- internal/bridge.RPC implements the existing line-delimited TCP bridge
  protocol with deadlines, cancellation and bounded responses. The bridge
  endpoint must remain on a trusted network/loopback: it has no wire auth.
  bridge.Client, bridge.Device, and bridge.Switch are integration
  interfaces; hardware packages are not imported into the core.
- Docker's final target is go-runtime, including the embedded root model catalog
  (no remote-backup transport binaries). Default/build Compose run only
  the native Go server; `BACKEND=native` is also the binary default.
  `BACKEND=bridge` with `BRIDGE_URL` remains an explicit rollback option,
  and the legacy Docker target/development profiles remain available.
  Native mode has not been hardware-tested and still lacks the operational
  features described below. Existing JSON credentials/configuration are not
  automatically imported: do not assume switching images migrates data.
  Compose points at `/data/.jackery-creds.key` to preserve the legacy key;
  set JACKERY_AT_REST_KEY_FILE to the actual existing key location if different.

## Collection schemas

The core migration is migrations/1762646400_core.go;
migrations/1762646401_battery_packs.go adds locked pack-history storage.

users is the PocketBase auth collection, with a required, unique username
field. Email is optional. Public record CRUD is locked.

Each domain collection has:

| Field | Type | Meaning |
| --- | --- | --- |
| id | PocketBase ID | Internal identity |
| key | required unique text | Stable logical key |
| device_sn | text, indexed with ts | Device scope, empty for global data |
| ts | number | Unix seconds |
| payload | JSON, max 10 MiB | Original domain data |

Collections:

| Collections | Contents |
| --- | --- |
| settings, device_prefs | Global configuration and device presentation preferences |
| credentials | Encrypted secret envelopes; location is also encrypted here |
| devices, samples | Cached device snapshots and timestamped raw telemetry |
| predictions, forecast_models | Forecast snapshots, daily summaries, model records |
| battery_packs | Timestamped expansion-pack snapshots for capacity-weighted accuracy |
| automation_rules, automation_decisions | Reserved rules, decisions and imported firings |
| conversations, messages, attachments, tool_runs | Assistant persistence |
| suggestions, changes | Advisor proposals and audit records |
| events | Reserved event log |

Queries bind parameters. Upserts use PocketBase transactions. Collection
rollback removes domain collections, but retains the existing auth collection.
Payload JSON preserves old field names; dedicated typed schemas,
relations, retention policies, and full domain validation remain future work.

## Implemented API behavior

- /api/auth/setup, /login, /logout, /me, /change_password
  under the same /api/auth prefix as Python.
- /api/status and /ws: snapshot/status envelopes, authenticated upgrades,
  cookie/query view selection, periodic updates, bounded writes and revalidation
  of session tokens. Cached device views do not switch the bridge's active unit.
- Bridge-backed cloud auth status/set/forget, reconnect, output switching,
  pause/resume polling, active device selection, and events.
- Device list from persisted snapshots.
- Basic global settings, device prefs, cost plan, load schedule, AI provider,
  and Anthropic preference JSON persistence. These do not yet reproduce all
  Python normalization, defaults, metadata or response wrappers.
- Encrypted location read/write with coordinate validation.
- Assistant conversation list/create/get/delete.
- /api/backup/run creates a PocketBase local backup.
- Energy history and lifetime totals integrate successive raw observations,
  with a five-minute outage cutoff. This is an initial implementation:
  Python rolling totals, dollar decoration, daily rollups, pack weighting and
  corrections are not ported. Imported aggregated samples are archival and
  are not yet included correctly in these integrations.

internal/server/routes.go records the legacy route inventory.
Unimplemented operations return HTTP 501 with a detail string; they do not
silently report success. Inventory entries are not a statement of API parity.

## Numeric and service modules

Implemented and compared with deterministic Python-generated fixtures:

- SOC simulation, including partial hours, charge efficiency, AC floor,
  learned ceiling and future-trough-aware diversion gating.
- Solar coefficient fit, interval alignment, sensor-noise cutoff,
  headroom/clear-sky/broad pool selection and coefficient bounds.
- Diurnal shape fit with median shrinkage.
- Load profile with diversion subtraction, p95 caps, upper trimmed median,
  variability classification and recent/older blending.
- Charge efficiency fit and surplus-day charge ceiling fit.

Also implemented: expected-load fallbacks, load windows/sleep windows,
flat/seasonal TOU rates and energy savings, edge-triggered rule evaluation
with a switch interface, and cheapest-hour selection with a mandatory anchor.
The edge-triggered rule engine now runs against successful bridge telemetry
with a native Kasa switch adapter. Smart/solar planners and the watchdog remain
unwired to live actuation.

The weather HTTP client has bounded requests, context support, 30-minute
in-memory caching, stale-cache fallback and nonempty errors. Forecast.Solar URL
and hourly resampling helpers exist. The full weather service, historical
cache, observation fallback, geocoding and solar-array inference are absent.

The OpenAI-compatible text transport and advisor bound/allowlist validation
are implemented as primitives. They are not a complete assistant/advisor.

## Legacy archival import

    go run ./cmd/jackery import-legacy /absolute/path/energy.db --dir=./pb_data

The importer opens the source read-only, skips absent known tables, preserves
all original row fields, and uses deterministic content keys for idempotent
reruns. It copies devices, samples, predictions, smart/solar decisions,
suggestions/changes, firings, device params and assistant tables.
It does NOT convert legacy records into complete live domain models, copy
attachments from disk, import JSON configuration/credentials, or replace the
Python source. Changed source rows produce new content-keyed archival records.
Weather and battery-pack tables are not imported. The import is incremental,
not one atomic operation; a failed run can be rerun.

## Remaining work before a production cutover

1. Finish native Jackery lifecycle parity: MQTT realtime subscriptions and pack
   push cache, all-device background polling, session/backoff policy beyond the
   one-minute contention cooldown, cloud diagnostics and BLE Jackery fallback.
   Native HTTP login/polling and MQTT output commands now exist and are the
   default deployment; lifecycle parity is still pending.
2. Full server contract port: configuration validation/defaults/wrappers,
   fleet metadata, model catalog annotations, raw diagnostics, telemetry
   history, weather forecast route/build/readiness, calibration, rolling energy
   totals, daily summaries, corrections and prediction accuracy.
3. Complete solar-charge helper/plan parity, then wire smart/solar planners and
   the inverter watchdog into live actuation. Finish
   scheduling/execution loops, controller status/history/analytics/backtest,
   persisted edge state and cross-controller conflict handling.
4. Finish Siseli/EcoFlow/BLE fleet lifecycle and hardware HTTP routes.
   Narrow device adapters now exist; Kasa is used by the running rule engine.
   Most hardware routes still return 501.
5. Daily advisor reviews/narration, suggestion application
   transactions, assistant streaming/tool approval/attachments/read tools,
   advanced provider capabilities; model discovery and basic history now work.
6. Local backup scheduling, retention, setup restore and key-inclusive recovery.
   Remote backup operations are retired by the Pi-only owner scope. Python
   directory snapshots are not compatible with Go PocketBase archives.
7. JSON settings/device prefs/credentials/auth import is implemented below;
   weather/battery-pack data, attachment files and domain-aware history remain.
8. Cloudflare Access verification is not implemented in Go.
9. Port the remaining pytest cases. Current Go tests cover selected reference
   behaviors and integration seams, not the complete pytest suite.
10. Revalidate Docker image builds and real browser/mobile/hardware behavior.
    Compose configuration was checked; no image deployment or hardware
    end-to-end test was performed.

## Validation

go build ./..., go vet ./..., and go test ./... passed across all
packages present at the time of validation, including the concurrent
EcoFlow/Kasa packages. Core race tests also passed for store, server, bridge, forecast, automation,
assistant, and advisor. The import-legacy CLI passed a temporary-database smoke
test.

Python fixture regeneration:

    PYTHONPATH=. python3 scripts/forecast-fixtures.py
    go test ./internal/forecast

Fixtures use a fixed seed and UTC for local-hour load fitting: 100 simulation
cases, 30 solar/diurnal cases, and 20 load/charge cases. Go integration tests
exercise actual PocketBase migrations, locked/authenticated routes,
setup/login/password failure, cookie/bearer auth, WebSocket snapshots,
encrypted secret round trips/tamper rejection, parameterized persistence,
and read-only/idempotent legacy import.

## Implementation continuation (2026-11-09)

This continuation is **not a completed core migration**. Python remains the
reference and no commit was created. Remaining items above still block cutover.

Implemented in this continuation:

- `internal/bridge/cloud.go`: native Jackery AES-ECB/RSA-PKCS1v15 login,
  Android MD5 UUID identity, token header, nested login fields, device list,
  properties/pack reads, metadata preservation, cloud telemetry mapping,
  MQTT AES-CBC credential derivation and QoS-1 output commands. HTTP is bounded
  and context-aware. A contested session is surfaced rather than immediately
  reclaimed. Native bridge adds a one-minute cooldown, account changes,
  selected-device controls, pause/resume and a bounded in-memory event log.
- `BACKEND=native` selects `bridge.Native` in the server. Cloud credentials
  are stored with the existing PocketBase encryption envelope. `bridge.Serve`
  also exposes the legacy TCP protocol through an injected listener, with
  bounded connections, response deadlines and context shutdown; it is a library
  entry point, not a new default CLI listener.
- Native MQTT keeps certificate verification enabled. The Cloud library accepts
  a TLS configuration for a supplied Jackery broker CA. Unlike Python it does
  not silently disable verification; CA configuration is not yet exposed in the
  server CLI/environment. Actual Jackery MQTT control is therefore unvalidated
  and may fail against the broker's private CA. Realtime subscription is absent.
- Smart-charge pure planning now matches **all fields, including reason text**
  of the compute-plan calls captured from the Python smart-charge test suite.
  Solar-charge covers mode/staleness/SOC/grid-charge/balance holds,
  overnight reserve, next-day trough and SOC/forecast hysteresis decisions,
  plus min-hold gating. Solar tests compare actions/mode, **not full Plan or
  reason parity**. Balance-window helpers, overload protection, config
  normalization and the richer solar Plan fields remain missing.
- Watchdog collapse detection, distinct-frame debounce, user-off suppression,
  pacing, two-cycle episode cap, error latch and genuine recovery match all
  captured evaluate calls and state fields from the Python watchdog suite.
  It is a pure state machine, not a running AC-cycle supervisor.
- `internal/devices` adapts native Kasa switching, Siseli portal telemetry with
  optional BLE BMS overlay, and EcoFlow snapshots/output commands to core
  interfaces. These adapters do not start/configure fleet lifecycles.
- Successful server polls evaluate persisted SOC rules using the native Kasa
  client and save firing/error audit records. Rules have list/create/update/
  delete endpoints. Rule edge state is still process-local; persisted edges,
  conflict resolution, bulk disable and full Python response parity are absent.
- Anthropic text transport, OpenAI-compatible gateway resolution, encrypted
  provider key endpoints, and `/api/assistant/chat` text turns with stored
  message history and transactional turn persistence. A configured provider,
  key and model are required. Attachments explicitly fail with 501. Tool calls,
  approvals, streaming, daily advisor reviews/narration and
  suggestion application remain absent; this is a text-only workflow.
- `internal/backup` still contains historical remote transport helpers/tests,
  but no running server route invokes them. Pi-only backup routes create/list/
  restore local PocketBase archives; saved remote credentials do not trigger
  transfers. Remote credential/discovery routes return 410. Local scheduling,
  retention, setup restore and encryption-key recovery remain pending.
- Browser views resolve both cached serials and cloud IDs without changing the
  bridge's active selection. Unknown IDs retain the active snapshot.

Siseli energy-counter accumulation and capture supervision were left in the
remaining device list in `internal/DEVICES.md`: the new portal adapter consumes
instantaneous telemetry, and server energy integration uses sample observations;
no server path introduced here depends on raw-capture supervision or Siseli's
local accumulated counters.

Reference validation: `PYTHONPATH=. python3 scripts/controller-fixtures.py`
runs 117 existing Python smart-charge/solar-charge/watchdog tests and captures
actual controller calls to Go testdata. Go uses the smart/watchdog complete
expected values and solar action expectations directly. Existing forecast
fixtures remain in place. This is **not** a port of the full pytest suite:
forecaster/predictions, all route contracts, load schedule, backtest, assistant,
advisor and backup suites still need comprehensive migration. Remote transfer
checks use fake commands/local bytes; no real NAS or hardware was contacted.

Continuation checks completed successfully: `go build ./...`, `go vet ./...`,
`go test ./...`, and `go test -race` for store, server, bridge, forecast,
automation, assistant, advisor, backup and devices. Reference captures contain
18 smart-charge plans, 52 solar decisions and 78 watchdog evaluations; the
117 Python tests cover additional helpers that are not all implemented in Go.
New server tests exercise text-chat history persistence, provider secret
redaction/encryption and browser view fallback. Kasa credential read/write/delete
and status/test routes are also wired to the native client.


## Implementation sub-agent continuation (2026-11-09)

**The requested migration is still incomplete.** This section supersedes the
older descriptions of the forecasting primitives and backup discovery/restore.
Python remains intact and no commit was created.

Completed and verified in this continuation:

- `internal/forecast`: joint drain regression, clean multi-hour discharge runs,
  narrow-load fallback, strict system-SOC filtering, per-pack baselines,
  signed inverter-loss fitting and span-based readiness. The forecast builder
  now composes these fits with the existing solar/load/efficiency/ceiling
  models, partial intervals, hybrid solar sources, recent-peak solar cap,
  scheduled loads/sleep windows, simulator, solar calibration and daily summaries.
  EV-diversion summaries and observed maximum charge-rate fitting are included.
  Battery/expansion capacities use the canonical embedded `models.json`.
- Historic decision replay truncates energy at each decision time and returns
  comparison rows and action/flip/extension/error summaries. It explicitly passes
  historical time to the builder: Python's replay omits `now_ts`, so parity
  fixtures freeze Python's wall clock at each decision. This is an intentional
  correction, not a claim that wall-clock-dependent Python replay is identical.
- Forecast snapshot upserts are transactional and collapse by device, made-at
  hour and target hour. Accuracy joins actual samples in the half-open ±30-minute
  window, selects nearest pack snapshots and capacity-weights actual SOC. Daily
  summaries filter by calendar date, preserve prediction timestamps during actual
  backfill, and backfill eligible past sunset/sunrise actuals idempotently.
- A separate migration adds locked `battery_packs` history. Successful polls
  persist pack arrays. Authenticated `/api/forecast/accuracy` and
  `/api/daily_summary` routes read the new storage, including signed bias and
  unsigned MAE by lead bucket. Capacity overrides and normalization of legacy
  imported archive rows into these live models remain open.
- Backup subnet inference, bounded 64-worker TCP discovery with parallel DNS,
  named-host ordering, result limits and SMB share enumeration. Temporary auth
  files remain private and are removed on failure as well as success. Share
  parsing handles machine-readable output and fallback tables; diagnostics keep
  actionable SMB errors. `/api/backup/discover` and `/api/backup/list-shares`
  are wired behind the existing application authentication.
- `/api/backup/snapshots` lists local PocketBase archives.
  `/api/backup/restore` invokes PocketBase's restore/restart lifecycle. With
  `remote: true`, it first downloads the named archive and its completion
  manifest, verifies name/size/SHA-256, rejects unsafe archive paths/symlinks,
  requires `data.db`, and stages the archive without replacing an existing local
  backup. Download verification tests cover corruption, missing/mismatched
  manifests, unsafe ZIP paths, private output permissions and existing files.
  Native archives remain incompatible with Python directory snapshots. Restore
  requires the corresponding encryption key. Actual process restart and real
  NAS transports were not exercised.

Reference checks added:

    PYTHONPATH=. python3 scripts/build-forecast-fixtures.py
    PYTHONPATH=. python3 scripts/forecaster-test-fixtures.py
    go test ./internal/forecast ./internal/store ./internal/backup ./internal/server

The first script creates 48 complete forecast-output cases and 16 historical
replay cases. The second runs all 94 existing `test_forecaster.py` tests and
captures 123 calls to seven ported functions. Go asserts their captured values,
allowing 1e-9 for floating-point arithmetic on unrounded fit coefficients; full
builder output fields compare exactly. Fixtures are deterministically gzipped.
These captures are **not** a complete direct port of every pytest assertion:
SOC diagnostic instrumentation, diagnostic rejection classification, custom fit
options, catalog override monkeypatching, and remaining server contracts still
need dedicated Go coverage. New prediction/storage tests retain Python's
70/65/5 accuracy example, 84/4 capacity-weighted example, signed MAE/bias 3/+1,
made-at cutoff, daily-date cutoff and idempotent three-actual backfill expectations.

Still required for the user's requested scope:

1. Connect forecast building and recording to an ongoing forecast service and
   `/api/forecast`, with correct rolling telemetry aggregation, pack-weighted
   historical fits, saved weather observations, calibration/debug endpoints and
   configured per-device overrides. The new builder accepts normalized history;
   handing raw poll samples directly to its energy-window fits is incorrect.
   Wire the replay and daily backfill service/API scheduling as well.
2. Complete full solar Plan/helper parity, controller arbitration and persistent
   state, then wire scheduled smart-charge, solar-charge and watchdog actuation
   across Kasa/Jackery/EcoFlow/Siseli. Existing rule actuation is unchanged;
   the pure planners and watchdog are still not a live supervisor.
3. Assistant tool transport/dispatch, approval state, streaming, attachments and
   reference read-tool contracts; daily advisor/narrator reviews, suggestion
   transactions and advisor routes. No assistant/advisor work was completed in
   this continuation.
4. Remote archive enumeration, rsync-module discovery, credential contract
   parity, scheduling/retention, first-run restore and Python-snapshot migration.
5. The other native fleet lifecycle, server contract, import, Cloudflare Access,
   Docker and hardware validation gaps listed above remain open. The remaining
   pytest suites have not all been ported.

Validation completed: `go build ./...`, `go vet ./...`, `go test ./...`, and
`go test -race` for store, server, bridge, forecast, automation, assistant,
advisor, backup and devices. No real device or NAS was contacted by the tests.

## Pi-only deployment scope (owner update)

The deployment target is Raspberry Pi, linux/arm64; there is no NAS.
Remote SMB/rsync/SSH transfers, discovery, share listing and remote retention
are retired by owner decision. Their Go HTTP entry points return 410, and
backup creation ignores saved legacy remote credentials. Remote restore is
rejected. Existing credential records and legacy files are retained untouched.
The settings backup panel now calls only local create/list/restore routes.
Python backup_discover.py, backup_creds.py and remote portions of backup.py
may be removed at final Python cleanup; they are no longer port requirements.
Local scheduling, retention, setup restore and encryption-key recovery remain
required. The old Synology deploy script is retired; see docs/RASPBERRY_PI.md.
BlueZ adapter builds are checked for linux/arm64 without cgo, but server BLE
fleet/inspector wiring and owner hardware validation remain outstanding.

### Verification of this deployment increment

- `go build ./...`, `go vet ./...`, `go test ./...`: pass.
- `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...`: pass,
  including the BlueZ adapter package.
- Web UI `npm run check` and `npm run build`: pass.
- Mobile `npm run typecheck`: pass.
- Offline HTTP tests verify retired remote routes return 410, saved remote
  secrets are preserved, and local create/list works even with remote
  credentials present.
- Container execution is not verified locally: Docker reports
  “apiserver is not running and not registered with launchd”. CI now builds
  the runtime image and checks its CLI and public assets.

This increment completes deployment source changes only. API contract parity
(step 2), import/reconciliation, lifecycle/supervisors, forecasting, remaining
fleets, assistant/advisor and local backup completion still block Python
removal. No Python source or tests were deleted. No hardware, cloud accounts,
or remote storage were contacted.

## Step 2 — compatibility API increment

HTTP tests now cover settings schema/defaults/clamping and runtime poll interval,
`device_id` preference writes and `prefs` listing, aliases, cost wrappers and
presets, conversation children/cascades, chat message objects/`pending_tools`,
model-list live/fallback responses using a fake provider, compat catalogs,
provider defaults, GET query forwarding, per-browser views/explicit output
routing, null energy selection and the 900-second history bucket. Native events
include ts/level/category/message/extra and honor since/limit. Account import
and login-upgrade tests are synthetic; no live accounts were used.

Settings metadata for not-yet-wired features remains available for compatibility.
Poll interval and low-battery threshold apply now. Cloud refresh/contested pacing
belongs to step 5; Siseli intervals to step 8; watchdog/advisor/backup scheduler
settings take effect only after their owning supervisors are wired. Model lists
use bounded HTTP reads/timeouts and static fallback aliases preserved from
Python; shared model caching remains part of step 9. Chat is still text-only.
Conversation deletion cascades PocketBase child records; attachment upload and
managed file cleanup remain step 9. Imported legacy attachment files are never
deleted by this increment.

The API audit checked Python route/view tests, Svelte stores/components and
mobile API calls against actual handlers (routes.go alone is not route coverage).
The following routes still lack operational data/services and remain 501; they
are explicitly assigned below rather than represented by successful empty
responses. Paths ending in `/*` include their CRUD and action subroutes.

| Owning plan step | Remaining UI/mobile API families |
| --- | --- |
| 4 — history conversion/read models | `/api/energy/daily`, `/api/energy/correct_output`; lifetime/rolling totals still need legacy aggregate conversion and monetary decoration |
| 5 — native Jackery lifecycle | `/api/devices/probe_results`, `/api/devices/probe_now`, `/api/debug/cloud_probe`, `/api/debug/raw_props`, `/api/devices/battery_packs` (cache/fresh RPC/capacity response), Cloudflare Access login |
| 6 — supervisors | `/api/smart_charge/{config,status,history,analytics,decision_details,evaluate_now,backtest}`, `/api/solar_charge/{config,status,history,evaluate_now}`, `/api/inverter_watchdog/{config,status,dismiss}`, `/api/f7_ac_reset/config`, `/api/automation/rules/disable`, `/api/automation/rules/{id}/history` (paired plug intervals) |
| 7 — forecasting | `/api/forecast`, `/api/forecast/solar_array` and `/infer`, `/api/forecast/solar_key`, `/api/location/geocode`, `/api/devices/params` and `/fit`/`/refit`, `/api/devices/capacity`, `/api/diagnostics/{row_soc,parasitic_fit_windows}`; learned-profile copying for `/api/forecast/load_schedule?copy_learned=1` |
| 8 — fleets | `/api/siseli/{credentials,local,local/test,status,settings,mqtt-capture}`, `/api/ecoflow/{credentials,reconnect_settings,reconnect,devices}` including link/delete subroutes, `/api/bms/{scan,status,saved,inverter}` including per-MAC/SN subroutes, `/api/kasa/{devices,saved,health}`, `/api/ble/{status,sessions,captures}` including session stop, capture events/download/delete |
| 9 — assistant/advisor | `/api/assistant/attachments/*`, `/api/assistant/tools/{id}/{approve,deny}`, `/api/algorithm/{suggestions,changes,preview,review_now,review_status}` and suggestion apply/dismiss; key validation and provider capability parity |
| 10 — local backups | `/api/backup/setup_restore/{test,snapshots,restore}`, local scheduling/retention, encryption-key-inclusive recovery |

`/api/forecast/accuracy` and `/api/daily_summary` read existing prediction data;
they do not generate forecasts. Generic config persistence is not a supervisor.
Registry imports below preserve native JSON structure for the fleet step, not a
claim of working fleet routes. BLE has standalone Go handlers but no server
mount/lifecycle yet. Remote backup transport/discovery remains retired by owner
scope, not pending work. The old Svelte setup remote restore wizard is removed;
local setup restore remains visibly pending.

## Step 3 — read-only legacy JSON import

Use an **existing** 32-byte key for both import and subsequent server startup:

```sh
export JACKERY_AT_REST_KEY_FILE=/path/to/existing/.jackery-creds.key
go run ./cmd/jackery import-config /path/to/legacy-copy --dry-run
go run ./cmd/jackery import-config /path/to/legacy-copy --dir=/path/to/pb_data
go run ./cmd/jackery serve --dir=/path/to/pb_data
```

Dry-run never bootstraps/opens the destination. It validates/decrypts sources and
prints per-source candidates, imported (zero), skipped/missing and errors as JSON.
It cannot check destination conflicts without opening it; apply checks these
inside transactions. Missing optional sources are reported, not errors. Invalid
sources return a failing exit status; other valid sources may commit independently.
Each source is atomic. Reruns insert only missing stable keys; existing native
records/users win, even if source contents changed. Import does not synchronize
or overwrite destination configuration. Reports never include secrets/hashes.

Sources: settings, device_prefs, automation rules, kasa/ecoflow/bms/siseli
registries, smart_charge/solar_charge/F7 configs, solar overload state, EcoFlow
reconnect policy, load schedules, location (including timezone/label), solar
array, cost, provider preferences, Jackery/Anthropic/OpenAI/Kasa/Siseli/EcoFlow/
Forecast.Solar credentials, and auth.json. Shared watchdog knobs are in settings;
Python has no separate watchdog config file. Siseli's portal-discovered fleet
usually has no separate registry file; optional siseli_devices.json is supported.
Registry/controller/solar-array objects retain every field under matching
settings keys until their owning services are wired. Per-device load schedules
use `load_schedule:<serial>`, device prefs use native identifiers, and provider
selection is derived from imported provider prefs. Existing remote-backup
credentials are deliberately not imported.

Default filenames match Python's `/data` basenames. Existing `JACKERY_*_FILE`
environment overrides are honored. `--manifest paths.json` can map allowlisted
source names to explicit paths and overrides those variables; unknown names
fail. `ConfigSources` in internal/store/config_import.go is the full mapping.
No directory scanning, DB rewriting, file moves, attachment cleanup, key creation
or source writes occur. Keep original data and rollback copies. Use one instance
of the importer at a time while the destination server is stopped.

Account policy: preserve the legacy PBKDF2-SHA256 hash encrypted under the
existing at-rest key. Create a PocketBase account with an unguessable transition
password to reserve the username and prevent public setup takeover. The first
successful old-password login verifies PBKDF2 in constant time, writes a native
PocketBase password, and deletes the encrypted legacy hash in one transaction.
A subsequent native password reset disables the legacy-password fallback.
Existing destination accounts are never replaced. Unsupported/malformed hashes
fail import with a password-reset-required report; do not start a fresh public
setup flow in that case. Legacy sessions do not carry across; log in again.

History/assistant SQLite conversion remains the separate step 4 importer;
`import-legacy` is still archival and is not a domain-aware replacement.

Step 2/3 verification: `go build ./...`, `go vet ./...`, `go test ./...`
and web UI check/build pass. All importer and provider fixtures are synthetic.
Reports count destination records (a registry/config object is one record,
while device preferences and rules count individual entries).

## Step 6a — dead-man rescue (completed with synthetic verification)

`internal/automation/rescue.go` ports rescue.py's decision state machine.
All eight tests/test_rescue.py cases are ported, with extra frozen/older timestamp
regressions. Only MAIN battery_percent arms the watchdog: <=5% fires immediately
while fresh, >=60 seconds of silence after a low observation fires dead-man,
and a successful toggle latches until a fresh >=20% observation. Missing SOC
retains the last known value; missing/invalid source timestamps never become a
new observation. Failed toggles retry; missing plugs do not latch.

The running server ingests timestamped entries from cloud.devices_telemetry
and the selected-device last_update_ts. It retains observations across poll
failures and persists per-device state/recovery latches in PocketBase settings.
The native client now emits an observation timestamp only after a successful
properties request, matching Python's HTTP-observation timestamp semantics.
Frozen supplied timestamps are preserved, never replaced by server poll time.
Missing/malformed native SOC now remains null instead of converting to zero,
so incomplete properties cannot spuriously arm rescue. Explicit 0% is retained.
Native all-device MQTT/source freshness remains step 5; HTTP success alone
cannot establish that a cloud service's stored reading is current at the unit.

A separate one-second RescueLoop runs beside polling, so cloud failures,
blocked polls, and long configured intervals do not suppress the silence check.
The runner borrows the first enabled ON rule scoped to that device (or an
unscoped rule), matching Python; OFF/disabled/other-device rules are excluded.
Rules are ordered by saved timestamp and stable key for deterministic ties.
There is no separate plug-host configuration to drift from automation rules.
Kasa calls have a ten-second context timeout. Rescue only switches ON.

Authenticated endpoints:

- GET `/api/rescue/config`: enabled plus fixed 5%/60s/20% thresholds and
  rule-based plug-selection metadata.
- POST `/api/rescue/config`: exactly `{"enabled":true|false}`. Thresholds and
  plug selection are deliberately fixed to the ported behavior.
- GET `/api/rescue/status`: enabled, thresholds, `by_device` state, last attempt,
  selected plug, last error and persistence error. No evaluate/actuation endpoint.

Successful actions write a `rescue-watchdog` automation firing audit with device,
host, MAIN SOC, reason, threshold and timestamp in the same transaction as state.
Failed audit/state persistence retains the in-memory latch and pending audit and
retries persistence without toggling again. A process crash between physical ON
and durable commit can repeat ON after restart; ON is idempotent. Exactly-once
physical actuation cannot be guaranteed across process/storage failure.

Tests use fake switches and fake/blocked cloud polls only. They cover plug
selection, multi-device routing, retry/latch/recovery, restart persistence,
firing audit fields, transactional persistence failure/retry, authenticated API
validation and the independent watchdog while polling is blocked. No hardware,
cloud account, legacy data or key was accessed or modified.

This run stops at the completed 6a boundary. Step 4 is next in the requested
order, followed by 5, 6b, 7, 8, 9 and 10; those steps are not completed by this
increment. Rescue implementation/assertions are ported, but rescue.py still has
Python server callers and remains in the tree until the step 11 cutover. No
Python files are deleted or unconditionally safe to remove from that retained
Python runtime yet. No new owner decision is required for rescue.

Step 6a validation passed: `go build ./...`, `go vet ./...`, `go test ./...`,
`go test -race ./internal/automation ./internal/server ./internal/bridge`, and
`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...`. Physical rescue behavior
has not been tested on a device; all actuation tests use injected fakes.

## Step 4 — domain history conversion

`import-legacy` now converts the checkpointed SQLite source rather than archiving
content hashes. It uses immutable/read-only SQLite access, rejects nonempty WAL
files, and commits each table atomically. Use offline checkpointed **copies**;
the importer never checkpoints or modifies a source. Source table/primary-key
identities are independent of file path and mutable row contents. Re-imports
update the same destination records. Previously generated archival duplicates
of those logical rows are removed only from PocketBase.

```sh
go run ./cmd/jackery import-legacy /copies/energy.db --dry-run
go run ./cmd/jackery import-legacy /copies/energy.db --dir=/new/pb_data
go run ./cmd/jackery import-legacy /copies/assistant.db --attachments=/copies/uploads --dir=/new/pb_data
```

The command prints JSON source/destination counts and Wh totals per source table.
Dry-run validates rows, totals Wh, and verifies attachment sizes without opening
the destination. Destination counts remain zero in dry-run. Apply reconciles
records after writing; differences caused by later Go output corrections are
reported rather than silently undoing those corrections. The destination server
should be stopped while converting; do not interleave mutable source snapshots
or overlapping independent device-history sources.

Converted tables: devices/capacity overrides, accumulated samples (all original
Wh and last_* columns), weather observations/forecasts, daily solar summaries,
pack history, forecast predictions, device parameters, smart/solar decisions,
automation firings, suggestions/changes, and assistant conversations/messages/
attachments/tool runs. JSON-encoded parts/args/results and proposal values are
decoded while original fields remain. Assistant relationships retain original
IDs. Attachment bytes are SHA-256 verified and copied into managed destination
storage with exclusive publication; source bytes/paths are never rewritten.
An interrupted transaction can leave an unreferenced content-addressed copy;
reruns reuse verified bytes. No legacy attachment is removed.

Energy history/totals now sum authoritative accumulated Wh without reintegrating
last_* watts. Raw Go observations integrate trapezoidally only against other raw
observations across gaps <=300s. An imported/corrected minute takes precedence
over raw records in that same minute, preventing overlap double-counting. The
history endpoint returns Wh and snapshot means with battery_pct; pack snapshots
at a shared timestamp are combined for weighted system_soc. Daily rollups,
rolling/lifetime totals and cost savings are exposed. POST
`/api/energy/correct_output` atomically updates/creates minute buckets (24-hour
maximum), preserving other energy columns and marking Go corrections so reruns
do not erase them. GET/POST `/api/devices/capacity` preserve overrides through
polls and import reruns. Live samples now include normalized battery_pct/input_w/
output_w/solar_w keys for forecast consumers.

Store.List uses stable (ts,id) cursor pagination in 1000-record pages; limit=0
means all rows. Lifetime/history callers no longer impose a 100k cap. A synthetic
100,005-row equal-timestamp test verifies traversal and explicit limits above
100k. Synthetic database/HTTP tests verify Wh reconciliation, mutable-source
idempotence, correction/re-import precedence, multi-pack SOC, preserved capacity,
weather/model/advisor/assistant relationships, attachment bytes, daily/history/
correction contracts and unchanged source database/attachment bytes.

This replaces the history/import algorithms, not all of energy_db.py's forecast,
controller/advisor helpers or assistant_store.py's future attachment/tool APIs.
Those Python modules still have callers and are retained for step 11.

## Step 5 — native Jackery lifecycle

Native now polls all bound devices (selected unit first), caches per-device raw
properties, main telemetry and packs, and exposes cloud.devices_telemetry with
source-observation timestamps. The server records every fleet member, preserves
capacity/metadata, uses observation identities rather than generating duplicate
samples on cached reads, and records packs against their own timestamps.

MQTT connects using the existing encrypted username/password derivation,
subscribes to the per-user device topic, and re-subscribes on reconnect. Property
deltas merge only into their matching serial; sub-device pushes update the pack
cache. Unknown devices and older/future-dated messages do not overwrite current
state. The bounded queue reports dropped messages in diagnostics. A one-second
realtime dispatcher delivers accepted pushes to the server independently of the
UI poll interval, including rescue ingestion. It avoids waiting behind a poll;
HTTP operations have a 45-second total context bound and 15-second transport
timeout. Cached reads and failed HTTP fetches never advance observation time.

Cloud refresh interval and session-contention cooldown now consume runtime
settings. Consecutive failures back off exponentially up to five minutes with a
capped counter; contested/auth-invalid responses retain the configured cooldown.
After ten minutes without observations, a poll recycles the auth/MQTT session
at most once per five minutes; bounded request contexts prevent an indefinite
network stall. Pause/cooldown suppress intentional polling, while independent
rescue timing remains active. Shutdown cancels loops and closes MQTT.

`JACKERY_MQTT_CA_FILE` optionally names a PEM CA bundle; invalid/unreadable files
fail configuration. TLS verification is always enabled (minimum TLS 1.2), using
system roots plus that bundle. Mount a custom CA read-only at the configured
container path via a Compose override. `JACKERY_MQTT_BROKER` defaults to the
standard Jackery SSL broker. Compose forwards both variables.

Implemented diagnostics: GET `/api/debug/raw_props`, GET
`/api/debug/cloud_probe`, GET `/api/devices/probe_results`, POST
`/api/devices/probe_now`, and GET `/api/devices/battery_packs` (cached or
`fresh=true`). Raw props retain their original property keys. Cloud probing uses
the same eleven legacy read-only pack/version request shapes (including POST
version query bodies); it does not issue output or firmware-update commands.
Auto-capacity interpretation and fitted-parameter application remain step 7.

The explicit CLI replaces the two Python login/device diagnostic scripts:

```sh
JACKERY_AT_REST_KEY_FILE=/path/to/existing/key go run ./cmd/jackery diagnose --dir=/path/to/pb_data
JACKERY_AT_REST_KEY_FILE=/path/to/existing/key go run ./cmd/jackery diagnose --devices --dir=/path/to/pb_data
```

Those commands intentionally contact the configured Jackery account when an
operator runs them; this run tested only injected local fake HTTP/MQTT transports.
CLI output excludes passwords/tokens. Do not run diagnostics against a real
account as part of migration verification without separate owner authorization.

Tests verify multiple devices, MQTT reconnect subscriptions, property and pack
deltas, independent realtime delivery, old-message rejection, frozen timestamps
on HTTP failure, cached-read identity, normalized fleet history, metadata/capacity
retention, retry/cooldown/watchdog pacing, diagnostic routing/request shapes,
redacted CLI output, and CA failure behavior. Hardware/cloud behavior is not
validated. Device-side freshness cannot be inferred from HTTP success alone;
HTTP observation timestamps match the legacy bridge semantics.

Step 6b is next: operational controller supervisors, persisted edges/conflicts,
action sequencing/history and full solar plan parity. Steps 7–10 also remain.
The generic backoff.py helper must still be ported for those other loops.
Cloudflare Access is still an outstanding cross-cutting auth port (kept, not
retired). diag_login.py and diag_devices.py now have wired/tested Go command
replacements and are step-11 deletion candidates. Broader bridge.py/cloud_client.py
removal still requires final runtime cutover and the remaining feature audit.

Verification for steps 4/5 passed: `go build ./...`, `go vet ./...`,
`go test ./...`, targeted race checks for store/server/bridge/CLI, and the
CGO-free linux/arm64 cross-build. The 100,005-row traversal test also passed
under the race detector. No real device/account/broker was used. This run stops
at the completed step-5 boundary due to remaining context; step 6b is next.

## Step 6b preparation — solar plan parity and loop pacing (partial)

Step 6b remains **incomplete**. No supervisor or new controller route is wired
by this preparation. Steps 7–10 have not been started in this continuation.

Solar now uses its own full plan schema rather than the smart-charge plan:
solar/load/surplus/car load, plug state, and baseline minimum SOC survive in the
returned audit snapshot. All 52 existing reference calls compare the entire
plan, including reason text, instead of just action/mode. Grid-charge yield,
balance trigger evidence, stalled balance diagnosis, reserve and cloudy-day
reason strings now match the captured Python plans. Min-hold fills the prior
plug state even on skip decisions. Pure balance-window aging/stand-down,
pre-engagement load ceiling and inverter cooldown gates are implemented with
synthetic boundary tests. These helpers still require supervisor integration.

`internal/automation/backoff.go` ports backoff.py's capped eight-failure counter,
reset, exponential delay, maximum and per-call base interval. All five Python
backoff test groups have Go equivalents, with an additional fractional-cap
check. The helper is not yet consumed by the operational controller loops.

Remaining 6b work: persisted controller configs/runtime and rule edges;
smart/solar/watchdog/F7 supervisors, conflict arbitration and safe action
sequencing; config/status/evaluate/dismiss/history/analytics routes; bulk rule
disable and rule history; solar config normalization, pack-spread extraction,
overload persistence and remaining Python assertions. Existing pure planners
alone are insufficient for cutover. No additional Python module is declared
fully replaced or safe to delete by this partial work, including backoff.py
whose runtime consumers still need migration.

Validation: go build ./..., go vet ./..., go test ./..., and CGO_ENABLED=0
GOOS=linux GOARCH=arm64 go build ./... passed. All test inputs are fixtures or
synthetic values. Python, legacy data and encryption keys remain untouched.

## Step 6b — smart-charge execution seam (partial)

This increment does **not** finish smart-charge or step 6b. It adds:

- Normalized per-device smart-charge settings, including imported legacy and
  by_device configs. Authenticated GET/POST `/api/smart_charge/config` return
  device_sn, config and conflicting_rules, preserving null for an unset plug.
- A fake-tested execution primitive using the existing Switch interface with
  ten-second command deadlines. Off/test modes never actuate. Successful
  commands alone advance ON tracking; failed commands retain the prior state.
  A conservative continuous-ON timeout forces OFF at the configured duration;
  a cumulative dusk/dawn budget is not implemented.
- A server persistence seam records intent before actuation, then atomically
  saves runtime state and an idempotently keyed smart-charge decision. An
  unresolved intent blocks subsequent commands, including after restart.
  This avoids blindly repeating an ambiguous command but still needs explicit
  plug-state reconciliation before it can form a running supervisor.
- Synthetic tests for normalization, authenticated HTTP config round trips,
  device isolation/conflict reporting, restart state, duration cutoff, failed
  actuation, test/off suppression, history and unresolved intent suppression.

The execution seam is deliberately not scheduled: weather/forecast input
assembly, readiness and freshness gates, reconciliation and controller/rule
arbitration still need to be connected before live execution. Smart status,
read-only evaluate, decision details, analytics/backtest and narration remain
unported. Solar, inverter watchdog, F7 and rule gaps remain as listed above;
no controller is newly complete. No Python file becomes safe to delete.

Validation passed: `go build ./...`, `go vet ./...`, `go test ./...`, and
`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`. Only synthetic temporary
PocketBase data and fake switches were used. No legacy data, keys, accounts or
hardware were accessed. No Python was removed.

## Owner decision — Siseli packet capture retired

Siseli local packet capture is retired on the Raspberry Pi. `siseli_local/core.py`
(Scapy/ARP interception), its capture runner, capture restart supervision and
server capture controls are **retired, delete in step 11, no Go port**. No NAS
is part of this deployment. Siseli cloud/portal integration remains in scope.
Keep pure payload/register decoders used by that cloud path; retirement does
not authorize removing shared decoding code. No Python is deleted here.

## Step 6b — running smart-charge controller

Smart-charge now has an independent cancellable five-minute server loop over
saved per-device configs, with legacy selected-device fallback. It builds both
AC-floor and counterfactual forecasts through `internal/forecast`, reads hourly
energy history and capacity overrides, and accepts an injectable weather source.
The default source reads persisted weather forecasts: until the weather service
is wired, missing weather/history/fresh SOC yields an explicit OFF fallback.
No invented weather or SOC is used to enable charging.

Active execution reads the configured plug before reconciling durable pending
intent and executing through the existing persisted intent/audit seam. A failed
read leaves pending intent intact. Reconciliation preserves continuous-ON time;
failed commands do not advance command state. Active smart configs reserve their
plug against ordinary SOC rules; emergency rescue retains its ON override.
Test/off modes never read or command a plug. Read-only evaluate never writes
history or actuates. Existing continuous-ON duration protection remains.

Authenticated status, evaluate_now, decision_details, analytics and backtest
routes now use the UI paths. Status returns persisted history and the existing
observed-charge-rate fit; replay uses the existing time-truncated Go replay.
Decision detail returns persisted forecast/weather/sample traces. Forecast
snapshots are recorded on scheduled ready evaluations. The smart planner's
captured Python assertions remain covered by complete-plan fixture comparisons;
normalization/execution and route tests add synthetic supervisor coverage.

Limits still requiring migration follow-up: optional AI narration returns null;
resolved_params in decision details remains empty pending step 7's parameter
resolution service. Daily summary refresh remains a separate forecasting task.
Weather is a clean provider seam with persisted-data fallback, not a newly wired
network weather service. Physical plug/cloud behavior has not been validated.

Solar-charge supervisor was not started. Remaining 6b: solar, inverter watchdog
and F7 supervisors, solar normalization/pack-spread/overload persistence and
remaining assertions, persisted rule edges, bulk disable/history, and broader
cross-controller ownership/sequence handling. Smart-charge operational scheduling,
forecast fallback, reconciliation, actuation and history are wired; these limits
mean full Python smart-charge server feature parity is not claimed.

Step-11 deletion candidates now include smart_charge.py's configuration/planner
logic after runtime cutover, alongside previously listed diagnostic and rescue
replacements. No Python file is safe to remove from the still-retained Python
server today. Retired capture pieces can be removed at step 11; shared pure
Siseli decoders must stay if the portal path imports them.

Validation for this increment passed: `go build ./...`, `go vet ./...`,
`go test ./...`, and `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`.
New tests use temporary PocketBase stores and fake plugs only. No real hardware,
accounts, legacy data or encryption keys were accessed. No Python was deleted.

## Step 6b — running solar-charge supervisor

Solar-charge now runs independently every 30 seconds with bounded exponential
backoff and cancellation. It shares smart-charge's injected weather/history
assembly, builds baseline and diversion forecasts, uses the smart sunrise target,
and checks device observation freshness rather than poll success. Missing
forecast/telemetry cannot enable diversion. Balance windows, last main-unit full
charge evidence, pack spread, load ceiling, grid-charge yield, min-hold and
inverter cooldown gates are connected to the full-plan planner.

Per-device runtime (including overload timestamp, verification baseline, learned
load, balance-window opening and uncertain command intent) is persisted in
PocketBase. Active startup forces diversion OFF; relay reads reconcile uncertain
commands. Commands have ten-second deadlines; failed commands retain uncertain
intent and do not advance successful relay state. No-load verification forces
OFF and persists a retry cooldown. High-load telemetry ingestion trips the plug
OFF between scheduled evaluations and stamps every high-load observation so the
cooldown starts at the last overload. Ordinary rules yield solar-owned plugs;
solar yields plugs owned by active smart-charge. General rescue/controller/rule
arbitration remains a wider step-6b task.

Authenticated config (including partial updates), status, read-only evaluate_now
and bounded history endpoints use the UI's solar_charge paths and wrappers.
Status includes local-day diverted Wh. Telemetry records measured diversion:
fresh Kasa watts with a 5W noise floor, otherwise verified learned load only while
the output delta remains at least half that load, otherwise zero. Instantaneous
W and integrated Wh are clamped to output. No configured-load guess is counted
as diverted energy. Native plugs expose their meter via an optional fakeable seam.

Synthetic tests cover normalization, robust pack spread, all diverted-W reference
paths plus the output clamp, authenticated config merge/status/history/evaluation,
dry-run isolation, startup OFF, durable failed intent, relay reconciliation,
no-load cooldown and telemetry overload persistence. The existing 52 complete
solar-plan fixtures and balance/load/cooldown helper boundary tests remain green.
All verification uses fakes, fixtures and temporary stores; no hardware, accounts,
legacy data or keys were touched. No Python was removed.

Operational controllers now include rescue, smart-charge and solar-charge.
Remaining 6b: inverter watchdog and F7 supervisors, persisted ordinary-rule edges,
bulk disable/history and broader action ownership/sequencing. Smart optional AI
narration and resolved-parameter decision details still require the narration and
parameter-resolution services; they were not expanded in this solar-only change.
Persisted weather remains the fallback until the weather service is wired.
Steps 7–10 and final cutover validation remain as previously documented.

Solar_charge.py joins smart_charge.py and the rescue/diagnostic replacements as
step-11 deletion candidates after runtime cutover. No Python file is safe to
delete from the retained Python runtime today. Retired Siseli capture/core runner
and capture supervision need no Go port; preserve shared cloud payload decoders.

Validation: go build ./..., go vet ./..., go test ./..., and
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./... passed for this increment.

## Step 6b — inverter watchdog live integration (partial continuation)

The native telemetry ingestion path now evaluates the existing collapse detector
for each device/frame. The collapse floor uses normalized global settings.
Per-device detector state, episode cycle budget, error latch and user-OFF
timestamp survive restart. Config/status/dismiss use the UI paths and status
shape; model codes 13/22 default enabled, with persisted per-device overrides.
Siseli and EcoFlow are rejected by these control routes.

Recovery uses the existing bridge set_output path: OFF, a cancellable two-second
pause, then ON after checking current configuration, trip state and user intent.
User AC OFF registers suppression before waiting on the bridge, cancels queued
recovery and records suppression even when the user's command fails. Disable
and dismiss also retract pending recovery. Commands have ten-second deadlines.
Fake tests cover live collapse detection, durable episode/user state, cancelling
ON during the pause, authenticated config/status/dismiss and response fields.
The prior complete detector fixtures remain in use.

This continuation does **not** finish step 6b or claim complete watchdog Python
server parity. Remaining: F7 live supervisor/config/events; restart-safe rule
edges, bulk disable/history/firings and shared controller arbitration; remaining
Python supervisor/concurrency assertions (including bridge-lock queue races),
legacy imported watchdog-override resolution, and optional smart narration /
resolved parameters. Watchdog configuration currently uses dedicated settings
records; imported historical device-parameter overrides are not yet consumed.
No Python is newly declared safe to delete.

Validation: go build ./..., go vet ./..., go test ./..., and CGO_ENABLED=0
GOOS=linux GOARCH=arm64 go build ./... passed. Tests use fakes and temporary
PocketBase stores. No real hardware, legacy data or keys were touched; no Python
was deleted. No push, PR or merge was performed.

## Step 6b — watchdog completion

Imported device_params inverter_watchdog_enabled values now supply the legacy
override unless a dedicated Go config supersedes them. Recovery releases the
state lock before waiting for bridge ON so user OFF can cancel queued recovery;
the native bridge checks cancellation after acquiring its command lock.
Fake tests cover queued ON cancellation, overlapping pending OFF intents through
dismiss, imported overrides and dedicated-config precedence. Existing detector
fixtures retain the Python evaluation assertions. Controller arbitration remains
for the next run. Validation passed: go build ./..., go vet ./..., go test ./...,
and CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./....

## Step 6b — F7 AC reset supervisor

Telemetry ingestion now starts the opt-in F7 AC pulse (ON, configured wait,
OFF) without blocking polling. One in-flight pulse per serial is allowed.
The supervisor preserves the Python disabled/AC-on/error/daylight/cooldown
gate order, stamps cooldown before actuation and persists bounded per-device
events. Commands have ten-second deadlines; cancellation after successful ON
still attempts bounded cleanup OFF. Failures produce error events and retain
the cooldown. General controller arbitration remains deliberately deferred.

Authenticated GET/POST /api/f7_ac_reset/config use the UI wrapper and merge
configuration without permitting runtime/history replacement. Imported
f7_ac_reset.by_device configs and events are fallback records until first save;
dedicated records then supersede them without duplicating imported events.
GET /api/events merges the persisted F7 events with bridge events by timestamp,
with since filtering and bounded results. Manual daylight uses saved location
offset and half-open clock windows; otherwise persisted weather GHI determines
daylight, failing closed when unavailable. This reuses the existing persisted
weather fallback; it does not wire a new weather fetch service.

Go tests port the gate, daylight, config-roundtrip and event assertions from
tests/test_f7_ac_reset.py and add fake pulse execution, concurrent-tick
suppression, command failure, shutdown cleanup, imported state, event cap,
restart persistence and authenticated UI route coverage.
Validation passed: go build ./..., go vet ./..., go test ./..., and
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./....
Only temporary stores, fakes and synthetic telemetry/weather were used.
No Python, legacy data or keys were modified; no push, PR or merge occurred.

Remaining 6b: ordinary automation restart-safe edges, bulk disable and rule
history/firings (including the remaining tests/test_automation*.py assertions);
shared controller arbitration/action ownership and sequencing for the next run.
Smart optional narration and resolved-parameter details still await their
respective services. Watchdog and F7 live integration are now implemented,
alongside rescue, smart-charge and solar-charge. Full cutover and steps 7–10
remain outstanding. F7 joins the controller step-11 deletion candidates only
after runtime cutover; no Python is safe to delete from the retained runtime.
Additional go test -race ./internal/server ./internal/bridge passed for the
watchdog/F7 supervisor increment.
