# Tesla integration (Go server)

The Go server talks to one Tesla through the Fleet API. It covers three things:

- **Navigation:** the TeslaHelper phone app sends up to 8 stops (`POST /api/tesla/navigate`).
- **Charging cost:** charging sessions are recorded and priced with the dashboard's cost plan, so home charging shows up alongside the house energy data.
- **Virtual key:** the server hosts the public half of the key at `/.well-known/appspecific/com.tesla.3p.public-key.pem`.

Code: `internal/tesla` (no PocketBase dependency) and `internal/server/tesla.go`.
Secrets (client secret, OAuth tokens, private key, command sessions) are kept in the encrypted `credentials` collection.
Samples and sessions go in `tesla_samples` / `tesla_sessions` (migration `1762646402_tesla.go`).

## How commands are sent

Signed commands use Tesla's `github.com/teslamotors/vehicle-command` library in-process; no separate `tesla-http-proxy` is needed.

Tesla's proxy and library don't implement `navigation_gps_request` (the proxy returns `invalid_command`).
So `internal/tesla/navigation.go` encodes the `carserver.Action` itself and sends it through `vehicle.Send` on the infotainment domain.
The field numbers come from the community `tesla-protocol` definitions, and the tests compare our bytes against those bindings.

`nav_transport: "rest"` sends the plain JSON Fleet API command instead, for comparison.

Real-car testing confirmed GPS inserts a stop before the existing destination. Single GPS commands always use **order 1**; alternate numbering schemes are no longer offered.

The supported batch endpoint is Maps:

- `POST /api/tesla/navigate` sends **places**: each stop is reverse-geocoded to a Google place ID (needs `google_api_key`, Geocoding API) and the batch goes as one `navigation_waypoints_request`, so the car builds one trip in order. Only rooftop-accurate addresses are used, the nearest to the pin wins, and it must be within `place_max_distance_m` (default 30 m; `max_distance_m` overrides it per request); otherwise the send fails naming the nearest address, rather than routing to the wrong house. Nothing is sent unless every stop resolves, and every failing stop is reported. A stop may carry a `place_id` (chosen by the user) to skip the lookup. Each result records its `address` and `distance_m` from the pin.
- GPS batch sending is removed. `POST /api/tesla/navigate/gps` returns **410 Gone** without waking or sending to the car. Use the Maps endpoint for batches.

Maps batches and single GPS sends accept a `transport` override and an optional `request_id` (8–64 of `A-Za-z0-9_-`). A repeated `request_id` never sends again: it returns the first job (202 while running). The app uses this to settle sends whose response was lost. A batch that replaces an earlier one passes `supersedes_request_id`: the server answers 409 (with `previous`) while that batch is still running, and 409 with `"needs_confirm":true` when its outcome is unknown (server restarted mid-send) until the app resends with `"confirm_unknown":true`. A settled or never-received previous batch does not block. Single stops (`/navigate/one`) always use GPS.

## One-time setup

1. **developer.tesla.com:** create an app.
   - Allowed origin: `https://j.cdani.net`
   - Redirect URI: `https://j.cdani.net/api/tesla/oauth/callback`
   - Scopes: vehicle information, location, commands, charging commands
   - Add a payment method and a billing limit.
2. **Save the config** with `POST /api/tesla/config`:
   ```json
   {"client_id":"…","client_secret":"…","vin":"…","domain":"j.cdani.net","region":"na"}
   ```
3. **Generate the key** with `POST /api/tesla/key` (or import `{"private_key_pem": "…"}`). Then check `https://j.cdani.net/.well-known/appspecific/com.tesla.3p.public-key.pem` loads from outside your network.
4. **Register the domain** with Tesla: `POST /api/tesla/partner/register`.
5. **Sign in:** open `/api/tesla/oauth/start?redirect=1` while logged in to the dashboard.
6. **Pair the key** with the car: open the `pairing_url` from `GET /api/tesla/status` on the phone that has the Tesla app.
7. **Phone token:** `POST /api/tesla/app_token` returns a long-lived bearer token for TeslaHelper. Changing the dashboard password revokes it.

## Endpoints

All require the app session or bearer token, except the public key and the OAuth callback (which is protected by a one-time `state`).

| Method and path | Purpose |
| --- | --- |
| `GET /api/tesla/status` | Config, key, token expiry, last vehicle state, last charge sample, active session, last navigation |
| `GET`/`POST /api/tesla/config` | Read (the secret is never returned) or save config |
| `POST /api/tesla/key` | Generate, `{"rotate":true}`, or import the virtual key |
| `POST /api/tesla/partner/register` | Register the domain with Tesla (one time per region) |
| `GET /api/tesla/oauth/start` | Authorize URL (`?redirect=1` redirects) |
| `POST /api/tesla/oauth/forget` | Delete owner tokens |
| `POST /api/tesla/wake` | Wake the car and wait until online |
| `POST /api/tesla/navigate/one` | One GPS stop inserted before the existing route: `{"lat","lng","wait","request_id"}`. Always sends **order 1**. Legacy `position` and `order` inputs are ignored; no append/replace promise. Same job record/idempotency as `/navigate`. |
| `POST /api/tesla/navigate` | Places route: `{"stops":[{"lat","lng"}…≤8], "wait":true, "transport"}`. With `"wait":false` it returns 202; poll `GET /api/tesla/navigate/last`. |
| `POST /api/tesla/places/resolve` | `{"stops":[…], "max_distance_m"}` → per stop: `match` (the place a places route would use, if within the limit), up to 6 rooftop `candidates` nearest first (`place_id`, `address`, `lat`, `lng`, `distance_m`), and `error`. Sends nothing. |
| `POST /api/tesla/navigate/gps` | Removed: returns **410 Gone**. No GPS batches are sent. |
| `GET /api/tesla/navigate/job` | `?id=` or `?request_id=` → that job. A job stored as running by an earlier server process reports `"status":"unknown"` (outcome not known: check the car). 404 if never received. |
| `GET /api/tesla/commands` | The last 100 commands sent to the car, newest first (`?limit=`): `command`, `transport`, `endpoint`, `params`, `payload_hex` (signed: the `carserver.Action` before signing/encryption), `response` (REST `response` JSON, or the decoded `carserver.Response`), `ok`, `error`, `duration_ms`. In memory only; resets on restart. |
| `POST /api/tesla/commands/clear` | Empty the command log |
| `GET /api/tesla/charging/sessions` | Sessions with kWh and cost (`?limit=`) |
| `POST /api/tesla/charging/poll` | Poll now |
| `POST /api/tesla/app_token` | `{"days":365}` |

## Charging cost

- **Polling.** The poller checks the car's online/asleep state every `poll_idle_minutes` (default 30). That check never wakes the car. When the car is online, the poller reads `charge_state` and `location_data`; while charging, it reads them every `poll_charging_minutes` (default 5).
- **Billing.** Every check is billable (Tesla bills every response under HTTP 500), so keep the intervals modest. `"polling_disabled": true` turns polling off.
- **What a session is.** A session follows Tesla's `charge_energy_added` counter from plug-in to unplug. Scheduled pauses don't split it.
- **Cost.** Wall energy = added / `charge_efficiency` (default 0.9). Each increase is priced at the cost-plan rate (flat or TOU) at the midpoint between samples, in the server's local time zone.
- **What isn't priced at home rates:**
  - Supercharger sessions, and sessions more than `home_radius_m` (250 m) from the saved dashboard location, are reported as `unpriced_kwh`.
  - Energy already added before the first sample of a session is priced at the first sample's rate and also reported in `unpriced_kwh`.

## Trip routing (`/api/route/*`)

Whole-trip road routing for TeslaHelper's Route tab (`internal/routing`, `internal/server/route.go`). Provider: **Apple Maps Server API**, because the app draws maps with Apple Maps (expo-maps on iOS); Google route data must not be shown on a non-Google map.

Setup: in the Apple Developer account (Certificates, IDs & Profiles → Keys) create a key with **Maps** (MapKit JS / Maps Server API) enabled, download the `.p8`, then `POST /api/route/config` with `{"team_id","key_id","private_key_pem","daily_call_limit"}`. The key is stored encrypted and never returned. Apple's free allowance is 25,000 calls/day per team (shared with MapKit JS; over it Apple answers 429). The server caps itself at `daily_call_limit` (default 5,000 per UTC day, saved in `events` as `route_budget:<day>` after each route request so a restart keeps the count) and 1,500 calls per request.

| Method and path | Purpose |
| --- | --- |
| `GET`/`POST /api/route/config` | Read (no key) / save the Apple Maps key and daily cap. GET also returns `used_today`. |
| `POST /api/route/evaluate` | Fixed order: `{"v":1,"start":{id,lat,lng},"finish":{id,lat,lng}\|null,"visits":[{id,lat,lng}],"order":[ids],"departure":unix,"avoid":["tolls"]}` → `evaluation` with per-leg `from`/`to` IDs, `distance_m`, `duration_s`, `has_tolls`, `geometry` (`polyline5`, `lat,lng`, simplified for display), totals. One `/v1/directions` call per leg; identical consecutive points cost nothing. `departure` more than 5 min ahead (up to 7 days) is sent to Apple as `departureDate` for every call; 0 or a past time means now, and the response echoes the departure actually used (0 = now). Optional planning fields: `lock_first` (keep the first N of `order` in place: "visit next"), `service_s` (seconds at each visit; a visit's own `service_s` overrides it), and per visit `earliest`/`latest` (unix s: arriving earlier waits, later is late). Every leg reports `arrive_s` (seconds after departure) and, when relevant, `wait_s`/`late_s`; the evaluation reports `total_service_s`, `total_wait_s`, `finish_s` (when the trip ends: driving + stops + waiting), `late_s` and the `late` visit IDs. `avoid:["tolls"]` (the only option Apple supports) asks Directions for toll-free routes; Apple may still use a toll road when no reasonable toll-free route exists, so `has_tolls` and `toll_legs` report it. |
| `POST /api/route/optimize` | Same body; `order` is the baseline. Prices travel times with `/v1/etas` (all pairs up to 30 visits; above that each point's 20 nearest plus its baseline successor and the finish), ranks orders by lateness first (`late_s`, weighted 100×), then by `finish_s`; offers a proposal that is less late, or equally late and ≥ 60 s sooner. It solves the order exactly (Held-Karp, deadlines as hard limits) up to 12 visits, otherwise runs nearest-neighbour, relocate/2-opt and seeded iterated local search, then evaluates baseline and candidate the same way. Co-located points are priced at 0 without a call. Apple's ETAs have no avoid option, so with `avoid:["tolls"]` the search ranks on normal ETAs while both evaluated orders use toll-free directions. Returns `comparison` with `baseline`, `proposed` (only when ≥ 60 s better), `improved`, `saving_s`, `message`. |
| `GET /api/route/search?q=` | Forward address search (Apple `/v1/search`), up to 10 results. |

Every order must list each visit exactly once (400 otherwise). 422 lists `unreachable` IDs; 429 means the cap or Apple's quota; 504 after 55 s. Travel times are Apple estimates for the departure used (now, or a future `departure`), one per leg (not recomputed for each leg's own arrival time); the schedule adds per-stop service time and waiting for windows; orders are exact up to 12 visits and heuristic above. Nothing is cached server-side.

### Confirmed GPS behavior

Real-car testing by the user confirmed GPS commands insert a stop before the existing destination, regardless of the attempted numbering scheme. Only explicit single-stop GPS sends are supported, always order 1. Historical `nav_order_mode` and stop-delay configuration fields are retained for storage compatibility and have no effect on sending. The dashboard no longer exposes those controls. GPS insertions do not mark a delivery complete or reorder the app trip; the app records that its full car route is no longer known and requires explicit Maps replacement to resynchronize.

### Apple Maps setup in the dashboard

Open the server dashboard → Tesla → **Apple Maps route planning**. Enter Team ID and Key ID, choose the Apple .p8 file (or paste its contents), choose the daily call limit, and save. The private key is stored encrypted and never returned by the configuration endpoint. Leave the key blank when editing other fields to retain the saved key. The file/key is cleared from the form after saving; it is not stored in browser persistence.

**Test saved connection** calls authenticated `POST /api/route/test`, exchanging the saved credentials as needed and performing an Apple search for a fixed sample location. It consumes provider quota and makes no Tesla command. A saved-key badge does not assert the credentials have been verified. Unsupported older server versions show an update-needed message.

## TeslaHelper app updates (over the air, `internal/server/ota.go`)

JavaScript-only changes reach the phone without a rebuild. Adapted from DREIA Members' OTA server (Expo Updates protocol v0/v1). Updates are stored unpacked in `<pb_data>/ota/<id>/` with `ota-info.json`; no collection or migration.

| Endpoint | Auth | |
|---|---|---|
| `GET /api/expo-updates/manifest` | public (expo-updates cannot send the app token) | Active update for the `expo-runtime-version` (= app version): multipart `manifest`, a `rollBackToEmbedded` / `noUpdateAvailable` directive, or 204. |
| `GET /api/expo-updates/assets?updateId=&asset=` | public | Bundle and assets of an uploaded update (path-checked). |
| `GET /api/ota/updates` | token | List, newest first. |
| `POST /api/ota/updates` | token | Multipart `bundle_zip` (from `npm run ota`), `runtime_version`, `notes`, `publish`. Max 30 MB. Refuses zips without `metadata.json`/`expoConfig.json` (with a scheme) or with escaping paths. |
| `POST /api/ota/updates/{id}/publish` · `/rollback` | token | One active update per runtime version; each activation mints a new update ID and time so apps see it as newer. Rollback sends apps back to the bundle built into the binary. |
| `DELETE /api/ota/updates/{id}` | token | Removes it; apps already running it keep it until another publish or rollback. |

App side: `app.json` `updates.url` points here with `checkAutomatically: NEVER` and no launch wait. The app only checks (on launch and on return, at most every 30 min) and shows "App update available" on Stops ("A newer version is available" in Settings → App update); the driver taps **Download and restart** (Stops banner or Settings → App update). Build the zip in `app/` with `npm run ota` (writes `app/ota/TeslaHelper-<version>-….zip` and shows it in Finder), then upload it in the dashboard: **Tesla → TeslaHelper app updates** (version read from the file name, publish right away or as a draft; the list has Publish, Roll back to installed app, and Delete). `npm run ota -- --upload` uploads from the command line instead (token from `$TESLAHELPER_TOKEN` or `~/.teslahelper-token`). An update only reaches release builds whose version matches; native changes need a new build and version.

## Address confidence and saved picks (place corrections)

Flex pins are often the parking spot, so the nearest rooftop address can be the house across the street or next door.

- `POST /api/tesla/places/resolve` now also returns, per stop: `confident` (nearest address within 12 m and the next one at least 1.5× and 5 m farther), `nearby` (addresses within the limit), and `source`: `"nearest"` or `"correction"` with `correction_id`. Send `"corrections": false` to ignore saved picks.
- Saved picks live in the `place_corrections` collection (`pin_lat`, `pin_lng`, `place_id`, `address`, `lat`, `lng`, `uses`, `last_used`). A pin within 6 m of a saved pin uses its pick (one pick per spot; kept tight so a neighbour's parking pin is not affected).
- `POST /api/tesla/places/corrections` `{"pin":{lat,lng},"place":{place_id,address,lat,lng}}` saves (replaces) the pick for that spot; `GET /api/tesla/places/corrections` lists them; `DELETE /api/tesla/places/corrections/{id}` forgets one.

The app saves a pick whenever the driver chooses an address for a Flex stop, marks stops using one with "◆ Saved pick", and marks unsure ones "Check address · N houses nearby" (Stops banner and the address picker walk through them). Stop details offer "It's the right house" (saves it) and "Forget saved pick".
