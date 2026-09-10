# Changelog

## Unreleased — PocketBase preview

- Port solar/load forecasting, pack-aware drain and charge fits, hourly prediction
  recording, accuracy, and daily sunrise/sunset summaries to Go under `/japi`.
- Add encrypted location settings, Open-Meteo city search and durable weather
  fallback, flat/seasonal TOU electricity plans, and streamed savings calculations.
- Add the Svelte Forecast tab, location controls, electricity-plan editor, and
  Energy savings display. Verify core predictions against Python reference vectors.

- Add durable minute energy integration, totals/history/daily APIs, capacity
  overrides, HTTP/MQTT battery packs, and weighted system SOC. The Svelte Energy
  and Device tabs now display persisted data and the Live chart survives restarts.
- Add transactional read-only legacy SQLite import with row/energy verification,
  WAL support, idempotence, and config/username migration. Original files remain
  untouched; unsupported schemas and nonempty destinations stop the import.

- Add the Go Jackery HTTP/MQTT live runtime, contention backoff, per-device
  snapshots and output commands, pause/resume, and `--mock` / `JACKERY_MOCK=1`.
- Add authenticated `/ws` with account-token revocation and `/japi` live/control
  routes, plus a Svelte Live tab with fleet selection, power flow, output buttons,
  recent in-memory charts, and connection/staleness indicators.
- Add encrypted cloud-account management compatible with Python's AES-GCM files.
  Broker certificate verification is enabled by default, with CA-file and explicit
  legacy TLS compatibility options.

- Add a pinned PocketBase 0.36.0 Go application with single-owner username
  authentication, transactional first-run administrator creation, and owner
  password changes. Custom feature routes use `/japi/*`; built-in PocketBase
  routes remain under `/api/*`.
- Add `/japi/settings`, backed by PocketBase, preserving the tunable schema,
  environment defaults, and clamped partial updates.
- Add the Svelte 5 dashboard shell, SDK auth, setup, Settings, and public-shell
  service worker. Other tabs explicitly show their pending migration state.
- Add standalone preview Docker/Compose packaging and Go/Svelte CI. Production
  Python, Expo auth, publishing, and legacy data remain unchanged until cutover.


All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project tries to follow [Semantic Versioning](https://semver.org/).
Pre-1.0 means breaking changes can land in any minor.

## [Unreleased]

### Added
- Energy history chart **compare chips** when two or more Jackerys are
  on the account: tick extra units to overlay consumed / charged / battery
  as colored lines (solid / dashed / thin). One device still uses the
  existing bar chart. KPI cards, daily table, and records stay on the
  viewed device.
- Live-tab **fleet strip** when two or more Jackerys are on the account:
  compact cards with system SOC, solar/load W, charge state, and pack
  count. Clicking a card focuses the existing Live hero (same per-browser
  view cookie as the header picker). Hidden for single-device accounts.
- `models.json` entries for Explorer 2000 Plus (`model_code` 2, 2042 Wh),
  Explorer 1500 Ultra (17, 1534 Wh), and Explorer 1500 v2 (21, 1512 Wh).
  Optional `pack_capacity_wh` so 2000 Plus expansion packs aren't sized as
  5040 Wh 5000 Plus cells.
- App-level username/password login (`auth.py`) — first-visit setup, PBKDF2
  password hash, HMAC-signed session cookies, sign-out button.
- `LICENSE`, `CONTRIBUTING.md`, `SECURITY.md`, issue + PR templates,
  this `CHANGELOG.md`.
- Unit tests for `auth`, `crypto_util`, `automation`, `settings` (pytest).
- CI workflow (`ci.yml`) that runs ruff lint + pytest on every push and PR
  across Python 3.11 and 3.12.
- `pyproject.toml` with pytest + ruff config; `requirements-dev.txt`.

### Changed
- Packs UI uses server `main_capacity_wh` / `pack_capacity_wh` instead of a
  hardcoded 5040 Wh default, so a 2000 Plus + Battery Pack 2000 Plus
  weights SOC correctly.
- Cloud property `bs` (idle / charging / discharging) is parsed into
  telemetry and shown on fleet cards.
- Static assets now send `Cache-Control: no-cache` so CDNs (Cloudflare in
  front of a Tunnel) revalidate every request instead of serving 4-hour-stale
  CSS after a deploy.
- Service worker drops `style.css` from the precache shell; bumped cache
  version to `v4` so existing PWAs evict stale shells on next nav (fleet
  strip markup lives in the HTML/JS shell).
- `[hidden]` HTML attribute now wins over flex layouts (was being defeated
  by `.field { display: flex }`).

### Fixed
- Watchtower no longer crash-loops on Docker API ≥ 1.40 (`client version
  1.25 is too old`). Prod compose now uses `nickfedor/watchtower`, which
  negotiates the API; the unmaintained `containrrr/watchtower` image
  hardcoded 1.25. Copy the new compose file to the NAS and recreate the
  project — Watchtower cannot heal itself.
- Device tab capacity override, learned parameters, and the unknown-model
  banner now reload when you switch Jackerys while staying on that tab.
  Identity used to update from the live status frame while those shared
  form fields kept the previous unit's values (and Save could write to
  the old serial). Cloud properties fetch now passes the viewed device
  serial instead of defaulting to the bridge-active unit.
- AC output no longer turns itself back on after a UI off or when the
  bridge starts with the port already off. The inverter watchdog used to
  treat any `AC=OFF` as a trip and publish AC-on (`action_id` 4). Port-off
  auto-recovery is gone; the opt-in hardware-trip off/on cycle (Settings →
  inverter trip-recovery floor, default 0 / disabled) is unchanged.

## [0.x — pre-tag history]

The project's first ~50 commits don't have proper semver tags yet. Highlights
of major behaviour the dashboard ships today:

- **Live tab**: smooth-bezier 6h chart with gradient area fills + dual-axis
  battery %; clickable AC/DC/USB/Car cards (commands over Jackery cloud
  MQTT); mood-aware battery glow.
- **Real-time updates**: MQTT subscribe to `hb/app/{userId}/device` so the
  dashboard sees device-pushed deltas at ~500ms instead of HTTP polling rate.
- **Multi-device**: bridge polls every Jackery device on the account each
  cycle. Per-device telemetry exposed through `merged_poll().cloud
  .devices_telemetry`.
- **Energy tab**: per-device kWh totals (today / 7d / 30d / lifetime),
  time-bucketed history chart with range picker.
- **Device tab**: model / serial / cloud state; "Pause polling" button
  with duration picker so the phone app can hold the cloud session.
- **Automation tab**: SOC-driven Kasa smart-plug rules. Each rule targets
  a specific Jackery device, edge-triggered (fires once per crossing),
  retry-on-failure (transient errors don't burn the trigger). Saved-Kasa-
  device registry; encrypted Kasa cloud creds for KLAP/SMART devices.
- **Logs tab**: in-bridge ring buffer of notable events.
- **Settings tab**: runtime-tunable poll cadence + low-battery threshold
  + contested-cooldown.
- **Auto-deploy**: GHA → GHCR → Watchtower on the NAS.
- **PWA**: installable on iPhone home screen; service worker caches the
  shell.
