
CREATE TABLE IF NOT EXISTS energy_devices (
    device_sn   TEXT PRIMARY KEY,
    name        TEXT,
    model_code  INTEGER,
    model_name  TEXT,
    first_seen  INTEGER,
    last_seen   INTEGER,
    capacity_wh_override INTEGER  -- user-set total capacity (e.g. with extension batteries); NULL = use model default
);

CREATE TABLE IF NOT EXISTS samples (
    device_sn   TEXT NOT NULL,
    bucket      INTEGER NOT NULL,    -- unix epoch (seconds), floored to BUCKET_S
    input_wh    REAL NOT NULL DEFAULT 0,
    output_wh   REAL NOT NULL DEFAULT 0,
    solar_wh    REAL NOT NULL DEFAULT 0,
    ac_input_wh REAL NOT NULL DEFAULT 0,
    last_input_w   INTEGER,
    last_output_w  INTEGER,
    last_solar_w   INTEGER,
    last_ac_input_w INTEGER,
    last_battery_pct INTEGER,
    last_system_soc REAL,
    solar_charge_diverted_wh REAL NOT NULL DEFAULT 0,
    sample_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (device_sn, bucket)
);

CREATE INDEX IF NOT EXISTS idx_samples_bucket ON samples(bucket);
CREATE INDEX IF NOT EXISTS idx_samples_dev_bucket ON samples(device_sn, bucket);

CREATE TABLE IF NOT EXISTS forecast_predictions (
    device_sn     TEXT NOT NULL,
    made_at       INTEGER NOT NULL,   -- hour-aligned unix epoch (when made)
    target        INTEGER NOT NULL,   -- hour-aligned unix epoch (when about)
    predicted_soc REAL NOT NULL,
    PRIMARY KEY (device_sn, made_at, target)
);
CREATE INDEX IF NOT EXISTS idx_pred_target ON forecast_predictions(device_sn, target);

-- Smart-charge controller decision log. Every periodic tick (5 min) writes
-- one row capturing what the controller decided AND why. Used to audit
-- behavior over time and to compute predicted-vs-actual analytics (cf.
-- forecast_predictions joined to samples).
CREATE TABLE IF NOT EXISTS smart_charge_decisions (
    decided_at    INTEGER NOT NULL,
    device_sn     TEXT NOT NULL,
    mode          TEXT NOT NULL,       -- off | test | active
    action        TEXT NOT NULL,       -- on | off | skip
    executed      INTEGER NOT NULL DEFAULT 0,  -- 0 = test/skipped, 1 = Kasa actually toggled
    reason        TEXT,
    current_soc_pct           REAL,
    predicted_sunrise_soc_pct REAL,
    target_sunrise_soc_pct    REAL,
    deficit_kwh               REAL,
    window_start              INTEGER,
    window_end                INTEGER,
    sunrise_ts                INTEGER,
    cheapest_rate             REAL,
    narration                 TEXT,
    baseline_predicted_sunrise_soc_pct REAL,
    PRIMARY KEY (decided_at, device_sn)
);
CREATE INDEX IF NOT EXISTS idx_sc_decided ON smart_charge_decisions(decided_at);
CREATE INDEX IF NOT EXISTS idx_sc_sunrise ON smart_charge_decisions(sunrise_ts);

-- Mirror of smart_charge_decisions for the *solar* charge controller, which
-- toggles a separate Kasa plug to divert surplus solar to an EV/heater/etc.
-- Decision direction is inverted: smart_charge brings grid power IN to the
-- Jackery during cheap TOU windows; solar_charge sends Jackery power OUT to
-- a downstream load when solar is producing more than home demand and the
-- overnight reserve is safe. Schema is intentionally similar so the
-- dashboard analytics can reuse the same query patterns.
CREATE TABLE IF NOT EXISTS solar_charge_decisions (
    decided_at    INTEGER NOT NULL,
    device_sn     TEXT NOT NULL,
    mode          TEXT NOT NULL,       -- off | test | active
    action        TEXT NOT NULL,       -- on | off | skip
    executed      INTEGER NOT NULL DEFAULT 0,
    reason        TEXT,
    current_soc_pct                       REAL,
    -- Predicted sunrise SOC assuming the controller keeps making the same
    -- ON/OFF decisions going forward (the "with diversion" projection).
    predicted_sunrise_soc_pct             REAL,
    -- Counterfactual: predicted sunrise SOC if we leave the plug OFF for
    -- the rest of the day. Used by compute_plan to decide whether
    -- diverting is safe (must satisfy baseline >= target + safety margin).
    baseline_predicted_sunrise_soc_pct    REAL,
    target_sunrise_soc_pct                REAL,
    -- Instantaneous values that drove THIS decision (snapshotted for audit
    -- so a "why did it turn off at 3:47pm" review needs no time-machine).
    solar_w                               REAL,
    load_w                                REAL,
    surplus_w                             REAL,  -- solar_w - load_w
    car_load_w                            REAL,  -- configured assumption
    plug_state_before                     TEXT,  -- "on" | "off"
    PRIMARY KEY (decided_at, device_sn)
);
CREATE INDEX IF NOT EXISTS idx_solar_sc_decided ON solar_charge_decisions(decided_at);

-- Daily denormalized summary: one row per (local-date, device) with the
-- noteworthy moments labelled — sunset SOC + sunrise SOC, predicted vs
-- actual. Filled progressively by the smart-charge tick: predicted values
-- written when the moment is in the future; actual values written once
-- it's in the past and a sample exists. Cheap to query for daily checks
-- and ML-style tuning over time.
-- Hourly weather observations (GHI + cloud cover) from Open-Meteo's
-- past_days response. Persisted so a future offline learning job can
-- correlate weather with our actual solar production WITHOUT re-fetching
-- (Open-Meteo deletes their archive after a while; once we've seen a
-- value we keep it).
CREATE TABLE IF NOT EXISTS weather_observations (
    ts              INTEGER NOT NULL,    -- hour-aligned unix epoch
    ghi_w_m2        REAL NOT NULL,
    cloud_cover_pct REAL,
    PRIMARY KEY (ts)
);

-- Latest full weather forecast (future hours), persisted so the app can
-- serve a real last-good 5-day forecast when the Open-Meteo API is
-- unreachable, and survive restarts (the in-memory cache does not).
-- Replaced wholesale on each successful fetch; refreshed from Open-Meteo
-- whenever it is available.
CREATE TABLE IF NOT EXISTS weather_forecast (
    ts              INTEGER NOT NULL,    -- hour-aligned unix epoch (future)
    ghi_w_m2        REAL NOT NULL,
    cloud_cover_pct REAL,
    fetched_at      INTEGER NOT NULL,    -- when this forecast was pulled
    PRIMARY KEY (ts)
);

CREATE TABLE IF NOT EXISTS daily_solar_summary (
    date           TEXT NOT NULL,         -- YYYY-MM-DD in user's local TZ
    device_sn      TEXT NOT NULL,
    sunset_ts      INTEGER,
    sunrise_ts     INTEGER,                -- the NEXT sunrise (tomorrow's)
    predicted_sunset_soc_pct  REAL,
    actual_sunset_soc_pct     REAL,
    predicted_sunrise_soc_pct REAL,
    actual_sunrise_soc_pct    REAL,
    updated_at     INTEGER NOT NULL,
    predictions_made_at INTEGER,
    PRIMARY KEY (date, device_sn)
);

CREATE TABLE IF NOT EXISTS battery_packs (
    ts              INTEGER NOT NULL,     -- unix epoch when sampled
    parent_sn       TEXT NOT NULL,        -- main device SN
    pack_sn         TEXT NOT NULL,        -- expansion battery SN
    device_order    INTEGER,              -- position in iOS app (0..N)
    soc_pct         REAL,                 -- rb
    input_w         REAL,                 -- ip
    output_w        REAL,                 -- op
    internal_temp_c REAL,                 -- it
    error_code      INTEGER,              -- ec
    PRIMARY KEY (ts, parent_sn, pack_sn)
);

CREATE INDEX IF NOT EXISTS idx_battery_packs_parent_ts
    ON battery_packs(parent_sn, ts);

CREATE TABLE IF NOT EXISTS legacy_algorithm_suggestions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at      INTEGER NOT NULL,
    device_sn       TEXT,                  -- NULL = global (forecaster const)
    kind            TEXT NOT NULL,         -- 'config' | 'anomaly'
    target          TEXT,                  -- e.g. 'smart_charge.max_charge_w'
    current_value   TEXT,                  -- JSON-encoded
    proposed_value  TEXT,                  -- JSON-encoded
    reasoning       TEXT,
    confidence      TEXT,                  -- 'high' | 'medium' | 'low'
    severity        TEXT,                  -- 'info' | 'warn' (anomalies only)
    status          TEXT NOT NULL DEFAULT 'pending',  -- pending|applied|dismissed
    decided_at      INTEGER,
    decided_by      TEXT                   -- 'user' | 'auto-expired'
);

CREATE INDEX IF NOT EXISTS idx_alg_sugg_device_status
    ON legacy_algorithm_suggestions(device_sn, status, created_at);

CREATE TABLE IF NOT EXISTS legacy_algorithm_changes (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    suggestion_id   INTEGER REFERENCES legacy_algorithm_suggestions(id),
    applied_at      INTEGER NOT NULL,
    device_sn       TEXT,
    target          TEXT NOT NULL,
    old_value       TEXT,
    new_value       TEXT,
    reasoning       TEXT
);

CREATE INDEX IF NOT EXISTS idx_alg_changes_device_ts
    ON legacy_algorithm_changes(device_sn, applied_at);

-- Automation rule firing history. Append-only — one row per successful
-- edge-trigger firing. Replaces the in-memory `last_fired` overwrite
-- with a real audit log: which rule fired, when, what action, what
-- the SOC was, and which Kasa plug it acted on. Used by the Automation
-- tab's "View history" view + by duration calculations that pair
-- consecutive ON/OFF firings on the same plug.
CREATE TABLE IF NOT EXISTS automation_firings (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    fired_at        INTEGER NOT NULL,        -- unix epoch
    rule_id         TEXT NOT NULL,
    rule_name       TEXT,                    -- snapshot at fire time so renames don't blank history
    action          TEXT NOT NULL,           -- 'on' | 'off'
    kasa_host       TEXT NOT NULL,           -- target plug (used for duration pairing)
    jackery_sn      TEXT,                    -- which device's SOC drove the firing
    soc_at_fire     REAL,                    -- battery_pct at the moment we fired
    trigger         TEXT,                    -- 'soc' | future expansion
    operator        TEXT,                    -- '<' | '<=' | '=' | '>=' | '>'
    threshold       REAL                     -- the rule's value field
);

CREATE INDEX IF NOT EXISTS idx_automation_firings_rule_ts
    ON automation_firings(rule_id, fired_at);
CREATE INDEX IF NOT EXISTS idx_automation_firings_host_ts
    ON automation_firings(kasa_host, fired_at);

-- Generic per-device parameter store. Keyed by (device_sn, key) with
-- exactly one row per param; the resolution ladder (user override >
-- fitted value > catalog/probe > default) collapses into a single
-- write that records the source. UI / forecaster reads via
-- resolve_device_param(...) so all per-device parameters share the
-- same resolution policy: DB → live fit → catalog/probe → default →
-- ask user. See DEVICE_PARAM_KEYS below for the canonical list.
CREATE TABLE IF NOT EXISTS device_params (
    device_sn   TEXT NOT NULL,
    key         TEXT NOT NULL,
    value       REAL,
    source      TEXT NOT NULL,    -- 'user' | 'fit' | 'probe' | 'catalog' | 'default'
    n_samples   INTEGER,          -- fit-source: how many windows; null otherwise
    confidence  TEXT,             -- 'low' | 'medium' | 'high' | NULL
    note        TEXT,             -- optional free-text (e.g. cloud probe key path)
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (device_sn, key)
);

CREATE TABLE IF NOT EXISTS pack_snapshots (parent_sn TEXT PRIMARY KEY, ts INTEGER NOT NULL, items TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS legacy_imports (source TEXT PRIMARY KEY, imported_at INTEGER NOT NULL, report TEXT NOT NULL);
