package automation

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func NormalizeSolar(raw map[string]any) map[string]any {
	out := map[string]any{"mode": "off", "kasa_device_host": nil}
	mode := strings.ToLower(fmt.Sprint(raw["mode"]))
	if mode == "off" || mode == "test" || mode == "active" {
		out["mode"] = mode
	}
	if host, ok := raw["kasa_device_host"].(string); ok && host != "" {
		out["kasa_device_host"] = string([]rune(host)[:min(128, len([]rune(host)))])
	}
	for _, v := range []struct {
		k         string
		d, lo, hi float64
	}{
		{"car_load_w", 1400, 50, 7000},
		{"comfort_high_pct", 70, 30, 99},
		{"comfort_low_pct", 30, 5, 90},
		{"min_hold_s", 30, 10, 3600},
		{"surplus_buffer_w", 100, 0, 1000},
		{"on_hysteresis_pp", 3, 0, 30},
		{"safety_margin_pp", 5, 0, 50},
		{"no_load_verify_delay_s", 90, 30, 600},
		{"no_load_threshold_w", 500, 50, 5000},
		{"no_load_cooldown_s", 900, 60, 7200},
		{"inverter_protect_load_w", 2100, 500, 4500},
		{"inverter_protect_cooldown_s", 1800, 60, 86400},
		{"max_system_load_w", 800, 100, 4500},
		{"balance_every_days", 30, 0, 365},
		{"balance_target_main_pct", 100, 80, 100},
		{"balance_spread_trigger_pp", 25, 0, 100},
		{"balance_max_window_days", 7, 0, 365},
	} {
		n, e := strconv.ParseFloat(fmt.Sprint(raw[v.k]), 64)
		n = math.Trunc(n)
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < v.lo || n > v.hi {
			n = v.d
		}
		out[v.k] = n
	}
	if out["comfort_low_pct"].(float64) >= out["comfort_high_pct"].(float64) {
		out["comfort_low_pct"] = float64(30)
		out["comfort_high_pct"] = float64(70)
	}
	return out
}

type SolarState struct {
	On            bool     `json:"plug_is_on"`
	Toggle        float64  `json:"last_toggle_ts"`
	Overload      float64  `json:"last_overload_ts"`
	Baseline      *float64 `json:"verify_pre_output_w"`
	Verify        float64  `json:"verify_deadline_ts"`
	Learned       *float64 `json:"learned_load_w"`
	Power         *float64 `json:"plug_power_w"`
	PowerTS       float64  `json:"plug_power_ts"`
	Cooldown      float64  `json:"no_load_cooldown_until"`
	BalanceOpened *float64 `json:"balance_opened_ts"`
	Pending       bool     `json:"pending"`
	Error         string   `json:"last_error"`
}

func SolarDivertedW(cfg map[string]any, s SolarState, output, now float64) float64 {
	if cfg["mode"] != "active" || !s.On {
		return 0
	}
	w := 0.0
	if s.Power != nil && s.PowerTS > 0 && now >= s.PowerTS && now-s.PowerTS <= 90 {
		if *s.Power >= 5 {
			w = *s.Power
		}
	} else if s.Learned != nil && s.Baseline != nil && *s.Learned > 0 && output-*s.Baseline >= *s.Learned*.5 {
		w = *s.Learned
	}
	return math.Max(0, math.Min(w, output))
}
func PackSOCSpread(packs []map[string]any) *float64 {
	lo, hi, n := math.Inf(1), math.Inf(-1), 0
	for _, p := range packs {
		v, e := strconv.ParseFloat(fmt.Sprint(p["rb"]), 64)
		if e == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			lo = math.Min(lo, v)
			hi = math.Max(hi, v)
			n++
		}
	}
	if n < 2 {
		return nil
	}
	v := hi - lo
	return &v
}
