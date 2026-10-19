export type SiseliControlOption = { value: number | string; label: string };

/** Normalized inverter setting from `status.siseli_controls` (server `normalize_controls`). */
export type SiseliControl = {
  canonical: string;
  name?: string;
  unit?: string;
  hint?: string;
  kind?: 'number' | 'select' | 'switch' | string;
  value?: number | string | boolean | null;
  min?: number;
  max?: number;
  step?: number;
  options?: SiseliControlOption[];
  on_value?: number;
  off_value?: number;
  dynamic?: boolean;
};

export type LinkedEcoflow = {
  sn?: string;
  alias?: string;
  roles?: string[];
  fresh?: boolean;
  telemetry?: {
    battery_percent?: number | null;
    battery_temp_c?: number | null;
    battery_status?: number | null;
    solar_input_w?: number | null;
    output_power_w?: number | null;
    input_power_w?: number | null;
    ac_input_w?: number | null;
    ac_on?: boolean | null;
    dc_on?: boolean | null;
    [key: string]: unknown;
  };
  detail?: { pow_get_pv?: number | null; pow_get_pv2?: number | null; [key: string]: unknown };
};

/** One row of `status.battery_packs` (Jackery expansion or Siseli BMS pack). */
export type BatteryPack = {
  sn?: string;
  deviceSn?: string;
  alias?: string;
  deviceOrder?: number;
  /** State of charge, percent. */
  rb?: number | null;
  /** Input / output watts. */
  ip?: number;
  op?: number;
  /** Pack temperature, °C (999 = unknown). */
  it?: number | null;
  temp?: number;
  voltage_v?: number | null;
  source?: string;
  error?: string;
  needUpgrade?: boolean;
};

export type Telemetry = {
  battery_percent?: number | null;
  system_soc_pct?: number | null;
  main_soc_pct?: number | null;
  capacity_wh?: number | null;
  main_capacity_wh?: number | null;
  pack_capacity_wh?: number | null;
  battery_temp_c?: number | null;
  input_power_w?: number | null;
  output_power_w?: number | null;
  solar_input_w?: number | null;
  ac_input_w?: number | null;
  car_input_w?: number | null;
  ac_on?: boolean | null;
  dc_on?: boolean | null;
  usb_on?: boolean | null;
  car_on?: boolean | null;
  time_remaining_h?: number | null;
  time_to_full_h?: number | null;
  ac_output_v?: number | null;
  ac_output_v_l1?: number | null;
  ac_output_hz?: number | null;
  ups_on?: boolean | null;
  super_charge_on?: boolean | null;
  error_code?: unknown;
  /** Pre-merge Siseli inverter PV watts (solar_input_w may include linked EcoFlow). */
  siseli_solar_w?: number | null;
  pv_voltage_v?: number | null;
  pv_current_a?: number | null;
  inverter_soc_pct?: number | null;
  bms_source?: boolean | string | null;
  ignore_inverter_soc?: boolean | null;
  linked_ecoflow?: LinkedEcoflow[];
  solar_inputs?: { source?: string; label?: string; watts?: number }[];
  load_inputs?: { id?: string; label?: string; watts?: number }[];
  history?: (Telemetry & { ts: number })[];
  [key: string]: unknown;
};

export type FleetDevice = {
  device_id: string;
  device_sn?: string;
  name?: string;
  model_name?: string;
  soc_pct?: number | null;
  solar_w?: number | null;
  ac_input_w?: number | null;
  feed_in_w?: number | null;
  output_w?: number | null;
  pack_count?: number;
  battery_status?: number;
  source?: string;
  age_s?: number | null;
};

export type CloudDevice = {
  device_id: string;
  device_sn?: string;
  name?: string;
  model_name?: string;
  model_code?: number | string;
  source?: string;
};

export type StatusSnapshot = {
  connection_status?: string;
  connection_error?: string;
  source?: string;
  last_update_ts?: number | null;
  mock_mode?: boolean;
  device?: {
    name?: string;
    device_id?: string;
    source?: string;
    device_sn?: string;
    portal_name?: string;
    model_code?: number | string;
  } | null;
  telemetry?: Telemetry | null;
  battery_packs?: BatteryPack[];
  siseli_controls?: SiseliControl[];
  energy?: {
    today?: {
      output_wh?: number;
      input_wh?: number;
      solar_wh?: number;
      ac_input_wh?: number;
      solar_charge_diverted_wh?: number;
    };
    today_savings?: unknown;
    cost_plan?: { currency?: string };
    [key: string]: unknown;
  };
  cloud?: {
    devices?: CloudDevice[];
    devices_overview?: FleetDevice[];
    selected_device_id?: string;
    pause_until?: number | null;
    contested_until?: number | null;
    pause_remaining_s?: number | null;
    contested_remaining_s?: number | null;
    mqtt_connected?: boolean;
    mqtt_error?: string;
    age_s?: number | null;
    http_age_s?: number | null;
  };
  device_prefs?: {
    alias?: string;
    solar_flow_unified?: boolean;
    load_sources?: string[];
    /** Canonical Siseli control keys pinned to the Live tab. */
    live_controls?: string[];
    ignore_inverter_soc?: boolean;
    calc_grid?: boolean;
    [key: string]: unknown;
  };
  inverter_watchdog?: {
    consecutive_attempts?: number;
    error_message?: string;
  };
  [key: string]: unknown;
};

export type TabName =
  | 'live'
  | 'energy'
  | 'forecast'
  | 'device'
  | 'automation'
  | 'logs'
  | 'settings'
  | 'ble';

export const VALID_TABS: ReadonlySet<TabName> = new Set([
  'live',
  'energy',
  'forecast',
  'device',
  'automation',
  'logs',
  'settings',
  'ble',
]);
