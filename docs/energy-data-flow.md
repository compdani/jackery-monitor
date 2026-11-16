# Dashboard energy data flow

Raw observations and imported minute aggregates remain in `samples`. Siseli
reported production is stored there with a stable device/interval key and
`_energy_kind=portal_solar`. Repeating a request replaces that interval; a downward
revision does not add energy a second time. Reported solar overrides estimated
solar only. Charging, consumption and grid energy remain independent.

`EnergyMinutes` reconstructs a requested window. It integrates raw observations
across minute boundaries, respects imported authoritative minutes, and uses the
expected source cadence to avoid treating an ordinary Siseli poll as an outage.
Siseli production has half-hour precision; its uniform minute distribution is an
allocation for charting, not a claim of measured minute-level production.

`energy_minutes` is a rebuildable, numeric materialization used for statistics.
Sample writes atomically mark their affected range dirty in `settings`, including
neighboring raw observations even when polls are more than half an hour apart. The next
reconciliation rebuilds that range and clears its marker in the same transaction.
The first reconciliation advances a persisted cursor by at most one UTC day per
transaction. Reads return HTTP 202 while history is being prepared, and the
lifecycle worker advances chunks without holding the writer for the full build.
Subsequent dirty-range work is also limited to a day per transaction. Sample
writes and corrections keep the original observations available for rebuilding.
Direct administrative SQL edits to samples bypass these markers and require a
rebuild (remove the device's `energy_ready:<serial>` settings record).

`energy_daily_stats` is a locked PocketBase view collection over those numeric
minutes. It exposes UTC-day sums, peaks and SOC bounds. Local-day API queries
aggregate the same numeric table into quarter-hour summaries and apply the
configured timezone at each timestamp, including historical DST rules. Totals and
TOU savings use indexed SQL aggregation with exact window cutoffs; Go receives
small numeric summaries rather than full telemetry payloads. The view is not itself materialized.

`EnergyLoop` runs after the server starts and warms device-specific totals.
REST requests share its response cache and coalesce identical concurrent requests.
Responses refresh at most every 30 seconds; corrections invalidate them explicitly.
The SSE-driven snapshot reads these cached totals and a separate 30-second six-hour history
cache. A snapshot refresh never triggers a lifetime reconstruction. Energy-tab
refreshes have request-generation guards so late responses cannot replace a newly
selected device or range.

The rollup migration creates empty collections and their indexes. It does not
reindex the historical samples table or build rollups during migration. Initial
materialization happens in resumable daily chunks in the background or on stats
requests, so its
cost depends on existing history and storage speed. Benchmarks in
`internal/server/energy_latency_test.go` exercise 100,000 observations; deployment
hardware latency still needs measurement.


## Realtime transport

PocketBase's native SSE broker sends dashboard payloads to authenticated users.
Updated Svelte web, legacy web and React Native clients subscribe to `monitor.data`
with a per-connection `view` in PocketBase subscription options. The server sends
an initial snapshot, then only changed top-level status fields (telemetry, fleet,
energy totals, preferences or history). Clients merge the patch into local state.
Committed changes and subscription updates wake the publisher immediately; a two-second
server tick retries missed sends and catches time-dependent changes. It shares snapshot construction
between clients viewing the same device. This replaces the former WebSocket
snapshot delivery without adding an HTTP fetch for each notification.

Energy screens register the exact totals, history, daily and comparison queries
that they read as `monitor.query.<encoded-path>` subscriptions. Results are bundled
into `monitor.data` only when changed. The shared API adapters serve pushed results
locally; existing screen loaders do not issue another GET after a payload arrives.
Range changes replace old queries for the same endpoint/device, and leaving the
energy screen clears its queries. At most 16 allowlisted energy queries and one
view are accepted per connection. The server uses the same coalesced HTTP response
cache internally, so energy freshness still follows its 30-second refresh bound.

`monitor.events` carries sanitized automation-firing, alert and Kasa-update events.
Older invalidation clients can still use `monitor.fleet` and
`monitor.device.<serial>`; older installed WebSocket clients retain `/ws`.

The dependency-free protocol in `web/realtime.js` is shared across the frontends;
mobile supplies a React Native EventSource implementation. Subscription POSTs use
the existing bearer token or web session cookie, never credentials in stream URLs.
Guest monitor subscriptions are rejected, and every payload send rechecks broker
auth and the active view subscription. Queries cannot access arbitrary API routes.

Only a successfully queued payload advances that connection's baseline. A slow
subscriber cannot block polling or persistence: a missed send is retried on the
next server tick. A small heartbeat after ten idle seconds tracks stream health;
status recovery polling runs every 30 seconds only while that stream is unhealthy.
Initial reads, user actions, newly selected energy queries and recovery can still
use HTTP. Reconnects clear cached query values and establish a fresh snapshot;
old streams and late HTTP responses cannot overwrite a newer view.

Tests exercise the real PocketBase HTTP SSE writer (GET, cookie-authenticated
subscription POST and streamed payload), query allowlisting, auth, device filtering,
rollback, transport lifecycle and consuming pushed energy without follow-up GETs.
Browser and native-device runtime validation remains separate from these tests.
