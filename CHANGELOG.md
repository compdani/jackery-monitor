# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project tries to follow [Semantic Versioning](https://semver.org/).
Pre-1.0 means breaking changes can land in any minor.

## [Unreleased]

### Fixed
- Treat Jackery `"logged in elsewhere"` replies as session contention and
  reclaim that session after a fixed 60s (instead of exponential backoff up
  to 1h). Keep the cloud watchdog alive when awaiting a cancelled poller.
- Align Open-Meteo hourly GHI with telemetry interval starts; forecast
  hours carry `duration_h` so the partial first hour is simulated correctly.
- Smart-charge keeps topping up in the final pre-sunrise hour when baseline
  sunrise SOC is still below target, even if current SOC already meets it.
- Cancel in-flight inverter recovery cycles when the user turns AC off or
  disables recovery; pending OFF intents suppress cycling until ack.
- Smart-charge conflict banner in the Svelte Automation tab refreshes rules
  after "Disable them" (and the same fix for `/legacy`).

### Added
- Per-device **Keep AC output on** toggle (Device tab) for Jackery AC
  hardware-trip recovery; defaults on for Explorer 5000 Plus.
- **Calendar expected load** on the Forecast tab: one-off dated events and
  weekly-recurring windows (Mon–Sun chips), with Historical mode retained.
  Calendar (`scheduled`) is the default for new devices; uncovered hours
  stay 0 W. Old `days: all|weekday|weekend` windows migrate to `weekdays`.
- **AI Assistant** right-side panel (Directus-shaped): server-persisted
  chats in SQLite, model picker, history, file/vision attachments (images +
  PDF/text under `/data/assistant_uploads`), tab-aware context, pending
  tool cards (Approve / Deny / Always allow), and Autopilot for write tools
  (calendar load CRUD, smart/solar charge, Live port toggles, apply
  suggestion). Pending algorithm suggestions surface in the empty state.
- **OpenAI-compatible endpoints** in Settings: preset selector (OpenAI,
  OpenRouter, Groq, Ollama, Custom) + Base URL; used by advisor, narrator,
  Assistant, and model list.

### Changed
- **SvelteKit file routing** for the dashboard (`ui/`): `adapter-static` SPA
  mode (`ssr=false`, `fallback: index.html`) with routes at `/`, `/energy`,
  `/forecast`, `/device`, `/automation`, `/ble`, `/logs`, `/settings`,
  `/login`, and `/setup` instead of hash tabs (`#energy`). FastAPI serves
  the SPA shell for those paths; hashed client bundles mount at `/_app/`.
  One-shot redirect maps old `#tab` bookmarks to `/tab`.
- Dashboard chrome uses **shadcn-svelte** + Tailwind v4, with **Light / Dark /
  System** theming (`mode-watcher`). Theme control is in the Topbar and under
  Settings → Display; domain widgets (power-flow, packs, charts) follow CSS
  theme tokens.
- Energy tab **Last 7 days / Last 30 days** KPI cards show the same
  solar / grid / net dollar rows as Today and Lifetime
  (`last_7d_savings` / `last_30d_savings` from the API).
- Energy history chart supports **hover crosshair + tooltip**, and a
  **Bars / Lines** style toggle (persisted in localStorage).

### Added
- **Svelte 5 dashboard** (`ui/`) replaces the vanilla JS PWA as the primary
  web UI. SvelteKit builds into `web/next/`; FastAPI serves the SPA at `/`
  and the tab paths above. The previous shell remains at `/legacy` for
  one-release rollback. Dashboard login/setup now run inside the SPA
  (`/api/auth/*` cookie auth unchanged). Feature calls stay on `/api/*` +
  `/ws` behind `api.ts` / `auth.ts` seams so a later Go/PocketBase cutover
  can swap the contract. Tab parity now covers Live (EOD, power-flow,
  packs, charge toggles), Forecast (canvas chart, day strip, budget, load
  schedule, accuracy), Automation (smart/solar forms, Kasa, rules, AI
  apply/dismiss), Device (Siseli/EcoFlow/F7/params/BMS/LAN), Settings (AI
  keys, cost TOU, backup run/restore), Energy compare/records, and Logs
  debug panel.
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
- Primary web UI is now the SvelteKit SPA at `/` and file routes such as
  `/energy` (was vanilla `web/index.html` + `web/app.js`, then a Vite
  hash-tab SPA). Open `/legacy` for the previous dashboard during the
  cutover window. CI builds `ui/` on every push; the Docker image has a
  Node 22 stage that runs `npm ci && npm run build`.
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
