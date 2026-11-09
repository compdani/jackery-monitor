# Go + PocketBase migration status

This is a **partial implementation**, not a completed replacement for the
Python backend. Python source remains intact. No commits were created.
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
- Existing Python HMAC sessions and auth.json are NOT imported. Create a new
  app account for the Go database. PocketBase superusers remain separate.
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
- Docker's final target is go-runtime; legacy retains Python for the
  bridge and rollback. Default/build Compose files build separate Go and
  Python images. Development profiles explicitly use legacy.
  The default bridge deployment still requires Python. `BACKEND=native` selects
  the new in-process Jackery client; it is opt-in and has not been hardware-tested.

## Collection schemas

The compiled migration is migrations/1762646400_core.go.

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
| predictions, forecast_models | Reserved forecast history/model records |
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
   Native HTTP login/polling and MQTT output commands now exist; the default
   deployment still uses the reference Python bridge.
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
   provider discovery, API-key endpoints and richer conversation history.
6. SMB/rsync discovery, scheduling, retention, restore and first-run restore.
   Remote archive upload and checksum verification are now implemented, but
   Python directory snapshots are not compatible with Go PocketBase archives.
   Preserve the encryption key separately when taking external snapshots.
7. Complete migration of legacy settings/device prefs/credentials/auth,
   weather/battery-pack data, attachment files and domain-aware sample import.
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
  approvals, streaming, model discovery, daily advisor reviews/narration and
  suggestion application remain absent; this is a text-only workflow.
- `internal/backup` transfers native archives through SMB, rsync over SSH,
  rsync daemon, or daemon over SSH. Keys/auth files use exclusive 0600 tempfiles
  and are removed after commands. Commands use context cancellation, argv
  rather than a shell, and validated remote paths. SSH requires known-host
  verification. Uploads download the remote archive and compare SHA-256/size
  before publishing a completion manifest. Server backup credentials are
  encrypted; `/api/backup/run` exports a PocketBase backup and uploads it when
  remote credentials exist, and `/api/backup/status` exposes the last remote
  result. This requires installed `rsync`/`ssh`/`smbclient` binaries. Discovery,
  remote listing, retention, schedules and restore are still missing. The
  encryption key must still be preserved separately.
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
