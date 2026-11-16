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

`order` is unconfirmed:

| Mode | Values sent |
| --- | --- |
| `enum` (default) | 0 replace, then 2 append |
| `position` | 1, 2, 3, … |
| `one_three` | 1, then 3, 3, … |

There are two batch endpoints:

- `POST /api/tesla/navigate` sends **places**: each stop is reverse-geocoded to a Google place ID (needs `google_api_key`, Geocoding API) and the batch goes as one `navigation_waypoints_request`, so the car builds one trip in order. Only rooftop-accurate addresses are used, the nearest to the pin wins, and it must be within `place_max_distance_m` (default 25 m); otherwise the send fails naming the nearest address, rather than routing to the wrong house. Nothing is sent unless every stop resolves. Each result records its `address` and `distance_m` from the pin.
- `POST /api/tesla/navigate/gps` sends one `navigation_gps_request` per stop, numbered by `order_mode` and `stop_delay_seconds` apart (defaults: the saved `nav_order_mode` and `nav_stop_delay_seconds`, 10; delay 0–60).

Both accept a `transport` override. Single stops (`/navigate/one`) always use GPS.

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
| `POST /api/tesla/navigate/one` | One stop added to the car's current trip: `{"lat","lng","position":"append"\|"prepend", "wait"}` (append = order 2, prepend = order 1; `"order":n` sends a raw value). Same job record as `/navigate`. |
| `POST /api/tesla/navigate` | Places route: `{"stops":[{"lat","lng"}…≤8], "wait":true, "transport"}`. With `"wait":false` it returns 202; poll `GET /api/tesla/navigate/last`. |
| `POST /api/tesla/navigate/gps` | GPS per stop: same body plus `"order_mode"`, `"stop_delay_seconds"`. Stops at the first failed stop. |
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
