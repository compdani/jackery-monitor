package server

import "time"

func populateFleetOverview(out map[string]any) {
	cloud, _ := out["cloud"].(map[string]any)
	telemetry, _ := cloud["devices_telemetry"].(map[string]any)
	devices := []map[string]any{}
	switch rows := cloud["devices"].(type) {
	case []any:
		for _, row := range rows {
			if d, ok := row.(map[string]any); ok {
				devices = append(devices, d)
			}
		}
	case []map[string]any:
		devices = rows
	}
	overview := []map[string]any{}
	for _, d := range devices {
		row := map[string]any{}
		for _, k := range []string{"device_id", "device_sn", "name", "model_name", "source"} {
			row[k] = d[k]
		}
		entry, _ := telemetry[str(d, "device_sn")].(map[string]any)
		if entry == nil {
			entry, _ = telemetry[str(d, "device_id")].(map[string]any)
		}
		tele, _ := entry["telemetry"].(map[string]any)
		for dst, src := range map[string]string{"soc_pct": "battery_percent", "solar_w": "solar_input_w", "ac_input_w": "ac_input_w", "output_w": "output_power_w", "feed_in_w": "feed_in_w", "battery_status": "battery_status"} {
			row[dst] = tele[src]
		}
		if stamp, ok := entry["ts"].(float64); ok {
			row["age_s"] = max(0, float64(time.Now().Unix())-stamp)
		}
		overview = append(overview, row)
	}
	cloud["devices_overview"] = overview
}
