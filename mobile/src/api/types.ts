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
  [key: string]: unknown;
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
