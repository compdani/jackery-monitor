export type Telemetry = {
  battery_percent: number; system_soc_pct?: number; capacity_wh?: number; battery_temp_c: number; input_power_w: number;
  output_power_w: number; solar_input_w: number; ac_input_w: number;
  car_input_w: number; ac_on: boolean; dc_on: boolean; usb_on: boolean;
  car_on: boolean; time_remaining_h: number; time_to_full_h: number;
};
export type FleetDevice = {
  device_id: string; device_sn: string; name: string; soc_pct: number | null;
  solar_w: number | null; output_w: number | null; age_s: number | null;
};
export type Snapshot = {
  connection_status: string; connection_error: string; mock_mode: boolean;
  last_update_ts: number | null;
  energy_error?: string; battery_packs_error?: string; battery_packs_ts?: number | null;
  device: { name: string; device_sn: string } | null;
  telemetry: Telemetry | null;
  history: (Telemetry & { ts: number })[];
  cloud: {
    devices_overview: FleetDevice[]; selected_device_id: string;
    age_s: number | null; http_age_s: number | null; mqtt_connected: boolean;
    mqtt_error: string; pause_remaining_s: number | null;
    contested_remaining_s: number | null;
  };
};
