package jackery

import (
	"encoding/json"
	"math"
	"strconv"
)

func number(v any) float64 {
	var n float64
	switch v := v.(type) {
	case json.Number:
		n, _ = v.Float64()
	case float64:
		n = v
	case int:
		n = float64(v)
	case string:
		n, _ = strconv.ParseFloat(v, 64)
	case bool:
		if v {
			n = 1
		}
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	return n
}

// Telemetry preserves the Python cloud adapter's units and field names.
func Telemetry(p map[string]any) map[string]any {
	i := func(k string) int { return int(number(p[k])) }
	f := func(k string) float64 { return number(p[k]) }
	hours := func(k string) float64 {
		v := i(k)
		if v == 0 || v == 999 {
			return 0
		}
		return float64(v) / 10
	}
	ups := true
	if _, ok := p["ups"]; ok {
		ups = i("ups") != 0
	}
	out := map[string]any{"battery_percent": i("rb"), "battery_temp_c": math.Round(f("bt")) / 10, "input_power_w": i("ip"), "output_power_w": i("op"), "ac_input_w": i("acip"), "car_input_w": i("cip"), "solar_input_w": max(0, i("ip")-i("acip")-i("cip")), "ac_output_v": math.Round(f("acov")) / 10, "ac_output_v_l1": math.Round(f("acov1")) / 10, "ac_output_hz": f("acohz"), "ac_on": i("oac") != 0, "dc_on": i("odc") != 0, "usb_on": i("odcu") != 0, "car_on": i("odcc") != 0, "ups_on": ups, "super_charge_on": i("sfc") != 0, "error_code": i("ec"), "time_to_full_h": hours("it"), "time_remaining_h": hours("ot"), "utc_offset_seconds": nil}
	if _, ok := p["uo"]; ok {
		out["utc_offset_seconds"] = i("uo")
	}
	if _, ok := p["bs"]; ok {
		out["battery_status"] = i("bs")
	}
	return out
}
