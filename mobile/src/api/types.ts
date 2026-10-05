export type Json = Record<string, unknown>;

export type Telemetry = {
  battery_percent?: number | null;
  system_soc_pct?: number | null;
  main_soc_pct?: number | null;
  battery_temp_c?: number | null;
  input_power_w?: number | null;
  output_power_w?: number | null;
  ac_input_w?: number | null;
  car_input_w?: number | null;
  solar_input_w?: number | null;
  solar_inputs?: SolarInput[] | null;
  load_inputs?: LoadInput[] | null;
  linked_ecoflow?: LinkedEcoflow[] | null;
  ecoflow_linked?: boolean | null;
  pv_voltage_v?: number | null;
  pv_current_a?: number | null;
  inverter_soc_pct?: number | null;
  ac_output_v?: number | null;
  ac_output_hz?: number | null;
  ac_on?: boolean | number | null;
  dc_on?: boolean | number | null;
  usb_on?: boolean | number | null;
  car_on?: boolean | number | null;
  ups_on?: boolean | number | null;
  super_charge_on?: boolean | number | null;
  error_code?: number | string | null;
  time_to_full_h?: number | null;
  time_remaining_h?: number | null;
  battery_status?: number | null;
  capacity_wh?: number | null;
  main_capacity_wh?: number | null;
  pack_capacity_wh?: number | null;
  [key: string]: unknown;
};

export type DeviceInfo = {
  name?: string | null;
  model_code?: number | string | null;
  model_name?: string | null;
  device_sn?: string | null;
  device_id?: string | null;
  address?: string | null;
  source?: string | null;
  [key: string]: unknown;
};

export type DeviceOverview = {
  device_id: string;
  device_sn?: string;
  name?: string;
  model_name?: string;
  soc_pct?: number | null;
  solar_w?: number | null;
  output_w?: number | null;
  pack_count?: number;
  battery_status?: number | null;
  source?: string;
  feed_in_w?: number | null;
};

export type HistoryPoint = {
  ts?: number;
  battery_percent?: number | null;
  input_power_w?: number | null;
  output_power_w?: number | null;
  [key: string]: unknown;
};

export type EnergyWindow = {
  input_wh?: number;
  output_wh?: number;
  solar_wh?: number;
  ac_input_wh?: number;
  solar_charge_diverted_wh?: number;
  since?: number;
};

export type EnergySavings = {
  solar_savings?: number;
  grid_cost?: number;
  net_savings?: number;
};

export type EnergyTotals = {
  device_sn?: string;
  name?: string;
  today?: EnergyWindow;
  last_7d?: EnergyWindow;
  last_30d?: EnergyWindow;
  lifetime?: EnergyWindow;
  today_savings?: EnergySavings;
  lifetime_savings?: EnergySavings;
  cost_plan?: { type?: string; currency?: string };
};

export type StatusPayload = {
  connection_status?: string;
  connection_error?: string | null;
  device?: DeviceInfo | null;
  last_update_ts?: number | null;
  telemetry?: Telemetry | null;
  battery_packs?: Record<string, unknown>[] | null;
  history?: HistoryPoint[];
  mock_mode?: boolean;
  backend?: string;
  source?: string | null;
  cloud?: {
    selected_device_id?: string | null;
    devices?: DeviceInfo[];
    devices_overview?: DeviceOverview[];
    [key: string]: unknown;
  };
  energy?: EnergyTotals | null;
  inverter_watchdog?: Json | null;
  siseli_controls?: SiseliControl[] | null;
  device_prefs?: DevicePrefs | null;
  [key: string]: unknown;
};

export type SiseliControl = {
  canonical: string;
  kind: "number" | "select" | "switch" | string;
  name?: string;
  unit?: string;
  hint?: string;
  min?: number;
  max?: number;
  step?: number;
  value?: number | boolean | string | null;
  options?: { value: number; label: string }[];
  on_value?: number | string;
  off_value?: number | string;
  dynamic?: boolean;
};

export type DevicePrefs = {
  alias?: string;
  live_controls?: string[];
  ignore_inverter_soc?: boolean;
  calc_grid?: boolean;
  solar_flow_unified?: boolean;
  load_sources?: string[] | null;
};

export type SolarInput = {
  id?: string;
  label?: string;
  source?: string;
  device_sn?: string;
  watts?: number;
};

export type LoadInput = {
  id?: string;
  label?: string;
  source?: string;
  device_sn?: string;
  watts?: number;
};

export type LinkedEcoflow = {
  sn?: string;
  alias?: string;
  device_type?: string;
  roles?: string[];
  fresh?: boolean;
  telemetry?: Telemetry;
  detail?: Record<string, unknown>;
  ts?: number;
};

export type SiseliReadings = {
  solar_w?: number | null;
  load_w?: number | null;
  grid_w?: number | null;
  feed_in_w?: number | null;
  battery_v?: number | null;
  charge_a?: number | null;
  discharge_a?: number | null;
  soc?: number | null;
};

export type SiseliMqttStream = {
  ip?: string;
  port?: number;
  encrypted?: boolean;
  readings?: SiseliReadings | null;
  mqtt_packets?: number;
  saw_mqtt?: boolean;
  payload_bytes?: number;
  readings_ts?: number;
};

export type SiseliCreds = {
  has_credentials?: boolean;
  user_id?: string | null;
  station_id?: string | null;
  device_id?: string | null;
  time_zone?: string | null;
  local_read?: boolean;
  hybrid_pull?: boolean;
  inverter_ip?: string;
  router_ip?: string;
  sniff_iface?: string;
  inverter_mac?: string;
  router_mac?: string;
  mqtt_broker_ip?: string;
  mqtt_streams?: SiseliMqttStream[];
  local_running?: boolean;
  local_error?: string | null;
  local_last_decode_ts?: number | null;
  local_readings?: SiseliReadings | null;
  state?: string | null;
  error?: string | null;
};

export type SiseliLocalBody = {
  local_read: boolean;
  hybrid_pull: boolean;
  inverter_ip: string;
  router_ip: string;
  sniff_iface: string;
  inverter_mac: string;
  router_mac: string;
  mqtt_broker_ip: string;
};

export type BmsPack = {
  mac: string;
  alias?: string;
  capacity_wh?: number | null;
  siseli_device_sn?: string | null;
  error?: string | null;
  reading?: {
    soc_pct?: number | null;
    error?: string | null;
  };
};

export type BmsInverterFlag = { use_as_main?: boolean };

export type BmsSaved = {
  packs?: BmsPack[];
  inverters?: Record<string, BmsInverterFlag>;
  ble?: { available?: boolean; error?: string | null };
};

export type BmsScanDevice = {
  mac: string;
  name?: string;
  rssi?: number | null;
  likely_jbd?: boolean;
};

export type DailyRow = {
  date: string;
  solar_kwh: number;
  consumed_kwh: number;
  charged_kwh: number;
  grid_kwh: number;
  diverted_kwh: number;
  peak_solar_w: number;
  peak_output_w: number;
  min_soc: number;
  max_soc: number;
};

export type SettingSpec = {
  key: string;
  label: string;
  hint: string;
  min: number;
  max: number;
  value: number;
};

export type ProbeResult =
  | { kind: "setup" }
  | { kind: "login" }
  | { kind: "ok"; status: StatusPayload }
  | { kind: "unreachable"; error: string };
