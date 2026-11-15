package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/compdani/jackery-monitor/internal/forecast"
)

var deviceParamKeys = []string{"battery_capacity_wh", "max_charge_w", "inverter_overhead_pct", "parasitic_w", "charge_efficiency", "solar_coefficient"}

func knownParam(key string) bool {
	for _, k := range deviceParamKeys {
		if k == key {
			return true
		}
	}
	return false
}

// clampFitLookback ports _clamp_fit_lookback_hours: [6h, 45d], default 14d.
func clampFitLookback(v any) int {
	h := 14 * 24
	switch n := v.(type) {
	case nil:
		return h
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return h
		}
		h = int(n)
	case int:
		h = n
	case string:
		i, e := strconv.Atoi(n)
		if e != nil {
			return 14 * 24
		}
		h = i
	default:
		return h
	}
	return max(6, min(45*24, h))
}
func paramRecordKey(sn, key string) string { return "device_param:" + sn + ":" + key }

// storedParam returns the Go-owned user parameter, or an imported legacy user
// row. A Go record with a null value is a tombstone that hides imported rows
// without modifying them.
func (s *Server) storedParam(sn, key string) (map[string]any, error) {
	row, err := s.Store.Get("forecast_models", paramRecordKey(sn, key))
	if err == nil {
		if row["value"] == nil {
			return nil, nil
		}
		return row, nil
	}
	if !isMissing(err) {
		return nil, err
	}
	rows, err := s.Store.List("forecast_models", sn, 0, 0)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r["_legacy_table"] == "device_params" && str(r, "key") == key && r["value"] != nil {
			return r, nil
		}
	}
	return nil, nil
}
func (s *Server) setParam(sn, key string, value *float64, extra map[string]any) error {
	now := float64(time.Now().Unix())
	row := map[string]any{"key": key, "param_key": key, "device_sn": sn, "value": nil, "source": "user", "updated_at": now, "ts": now, "_go_param": true}
	if value != nil {
		row["value"] = *value
	}
	for k, v := range extra {
		row[k] = v
	}
	return s.Store.Put("forecast_models", paramRecordKey(sn, key), sn, row)
}

func confidence(n, high, medium int) string {
	if n >= high {
		return "high"
	}
	if n >= medium {
		return "medium"
	}
	return "low"
}

// fitWeather returns past irradiance for fits: persisted observations plus
// any past hours from the live weather service, deduplicated by timestamp.
func (s *Server) fitWeather(ctx context.Context, now int64, hours int) []forecast.Row {
	since := float64(now - int64(hours+48)*3600)
	byTS := map[int64]forecast.Row{}
	if rows, err := s.Store.List("weather_observations", "", since, 0); err == nil {
		for _, r := range rows {
			byTS[int64(forecast.Number(r, "ts"))] = forecast.Row(r)
		}
	}
	if s.SmartForecast != nil {
		rows, _ := s.SmartForecast.Weather(ctx, "", now)
		for _, r := range rows {
			byTS[int64(forecast.Number(r, "ts"))] = r
		}
	} else if wx, err := s.weatherNow(ctx); err == nil {
		for _, r := range wx.Hourly {
			if ts := forecast.Number(r, "ts"); ts <= float64(now) && ts >= since {
				byTS[int64(ts)] = r
			}
		}
	}
	out := make([]forecast.Row, 0, len(byTS))
	for _, r := range byTS {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return forecast.Number(out[i], "ts") < forecast.Number(out[j], "ts") })
	return out
}

// liveParamFit ports _compute_live_param_fit; it never reads or writes user
// overrides.
func (s *Server) liveParamFit(ctx context.Context, sn, key string, hours int, now int64) map[string]any {
	if key == "battery_capacity_wh" {
		return map[string]any{"value": nil, "source": "unknown", "n_samples": 0, "error": "battery_capacity_wh is not fit from history", "lookback_hours": hours}
	}
	if !knownParam(key) {
		return map[string]any{"value": nil, "source": "unknown", "n_samples": 0, "error": fmt.Sprintf("unknown param key: %q", key), "lookback_hours": hours}
	}
	history, capacity, _, err := s.energyInputs(sn, now, hours)
	if err != nil {
		return map[string]any{"value": nil, "source": "unknown", "n_samples": 0, "lookback_hours": hours, "error": err.Error()}
	}
	capacity = s.resolvedCapacity(sn, capacity)
	src := func(n, need int) string {
		if n >= need {
			return "fit"
		}
		return "default"
	}
	switch key {
	case "max_charge_w":
		loc, _, _ := s.savedLocation()
		w, n := forecast.FitMaxCharge(history, s.fitWeather(ctx, now, hours), int(forecast.Number(loc, "utc_offset_seconds")), 100, 6, .95)
		if w == nil {
			return map[string]any{"value": 800.0, "source": "default", "n_samples": n, "lookback_hours": hours, "note": "fit found no qualifying samples"}
		}
		return map[string]any{"value": *w, "source": "fit", "n_samples": n, "lookback_hours": hours, "confidence": confidence(n, 30, 12)}
	case "inverter_overhead_pct", "parasitic_w":
		parasitic, pct, n := forecast.FitDrainModel(history, capacity, s.packCount(sn))
		v := pct
		if key == "parasitic_w" {
			v = parasitic
		}
		return map[string]any{"value": v, "source": src(n, 5), "n_samples": n, "lookback_hours": hours, "confidence": confidence(n, 20, 10)}
	case "charge_efficiency":
		v, n := forecast.FitChargeEfficiency(history, capacity, .9, 5)
		return map[string]any{"value": v, "source": src(n, 5), "n_samples": n, "lookback_hours": hours, "confidence": confidence(n, 20, 10)}
	default: // solar_coefficient
		k, n := forecast.FitSolarCoefficient(history, s.fitWeather(ctx, now, hours))
		return map[string]any{"value": math.Round(k*1e4) / 1e4, "source": src(n, 2), "n_samples": n, "lookback_hours": hours, "confidence": confidence(n, 20, 5)}
	}
}

// resolvedCapacity adds cached expansion packs unless a capacity override or
// user parameter applies (system capacity, as the simulator uses).
func (s *Server) resolvedCapacity(sn string, capacity float64) float64 {
	cached, _ := s.Store.Get("devices", sn)
	dev, _ := cached["device"].(map[string]any)
	if forecast.Number(cached, "capacity_wh_override") <= 0 && forecast.Number(dev, "capacity_wh_override") <= 0 {
		capacity += float64(s.packCount(sn)) * forecast.ExpansionCapacity(int(forecast.Number(dev, "model_code")))
	}
	if row, err := s.storedParam(sn, "battery_capacity_wh"); err == nil && row != nil && forecast.Number(row, "value") > 0 {
		capacity = forecast.Number(row, "value")
	}
	return capacity
}

// resolveDeviceParams walks resolve_device_param's ladder for every key:
// stored user values, explicit configuration, live fits, catalog, default.
func (s *Server) resolveDeviceParams(ctx context.Context, sn string, hours int) ([]map[string]any, error) {
	metadata := map[string]map[string]any{}
	_ = json.Unmarshal(deviceParamMetadata, &metadata)
	now := time.Now().Unix()
	_, _, base, err := s.capacityHints(sn)
	if err != nil {
		return nil, err
	}
	cached, _ := s.Store.Get("devices", sn)
	dev, _ := cached["device"].(map[string]any)
	packs := s.packCount(sn)
	note := "main only"
	if packs > 0 {
		note = fmt.Sprintf("main + %d pack(s)", packs)
	}
	capacity := map[string]any{"value": s.resolvedCapacity(sn, base), "source": "catalog", "note": note}
	if forecast.Number(cached, "capacity_wh_override") > 0 || forecast.Number(dev, "capacity_wh_override") > 0 {
		capacity = map[string]any{"value": base, "source": "user", "note": "capacity_wh_override"}
	} else if !forecast.InCatalog(int(forecast.Number(dev, "model_code"))) {
		capacity["source"] = "default"
	}
	values := map[string]map[string]any{"battery_capacity_wh": capacity}
	for _, key := range deviceParamKeys[1:] {
		v := s.liveParamFit(ctx, sn, key, hours, now)
		delete(v, "lookback_hours")
		values[key] = v
	}
	if raw, _ := s.smartConfigRaw(sn); forecast.Number(raw, "max_charge_w") > 0 {
		values["max_charge_w"] = map[string]any{"value": raw["max_charge_w"], "source": "user", "note": "from smart_charge config (Automation tab)"}
	}
	out := []map[string]any{}
	for _, key := range deviceParamKeys {
		row := map[string]any{"key": key}
		for k, v := range metadata[key] {
			row[k] = v
		}
		resolved := values[key]
		if stored, err := s.storedParam(sn, key); err != nil {
			return nil, err
		} else if stored != nil {
			resolved = map[string]any{"value": stored["value"], "source": "user"}
			for _, k := range []string{"note", "updated_at", "n_samples", "confidence"} {
				if stored[k] != nil {
					resolved[k] = stored[k]
				}
			}
		}
		for k, v := range resolved {
			row[k] = v
		}
		out = append(out, row)
	}
	return out, nil
}
func (s *Server) smartConfigRaw(sn string) (map[string]any, error) {
	raw, err := s.Store.Get("settings", "smart_charge")
	if err != nil && !isMissing(err) {
		return nil, err
	}
	if by, ok := raw["by_device"].(map[string]any); ok {
		raw, _ = by[sn].(map[string]any)
	}
	return raw, nil
}
func (s *Server) resolveParam(ctx context.Context, sn, key string, hours int) map[string]any {
	rows, err := s.resolveDeviceParams(ctx, sn, hours)
	if err != nil {
		return map[string]any{"value": nil, "source": "unknown", "error": err.Error()}
	}
	for _, r := range rows {
		if r["key"] == key {
			out := map[string]any{}
			for k, v := range r {
				if k != "key" && k != "label" && k != "unit" && k != "description" && k != "decimals" {
					out[k] = v
				}
			}
			return out
		}
	}
	return map[string]any{"value": nil, "source": "unknown"}
}

// fitDeviceParam ports /api/devices/params/fit and the assistant helper.
func (s *Server) fitDeviceParam(ctx context.Context, sn, key string, lookback any, apply bool) (map[string]any, int, string) {
	if sn == "" || key == "" {
		return nil, 400, "device_sn and key required"
	}
	if !knownParam(key) {
		return nil, 400, fmt.Sprintf("unknown param key: %q", key)
	}
	if key == "battery_capacity_wh" {
		return nil, 400, "battery_capacity_wh is not fit from history"
	}
	hours := clampFitLookback(lookback)
	previous, err := s.storedParam(sn, key)
	if err != nil {
		return nil, 500, err.Error()
	}
	computed := s.liveParamFit(ctx, sn, key, hours, time.Now().Unix())
	if computed["error"] != nil && computed["value"] == nil {
		return nil, 400, str(computed, "error")
	}
	var prev any
	if previous != nil {
		prev = previous
	}
	out := map[string]any{"device_sn": sn, "key": key, "lookback_hours": hours, "applied": false, "previous": prev, "value": computed["value"], "n_samples": computed["n_samples"], "fit_source": computed["source"], "confidence": computed["confidence"], "note": computed["note"]}
	if apply && computed["value"] != nil {
		note := fmt.Sprintf("fit over last %dh", hours)
		v := forecast.Number(computed, "value")
		if err := s.setParam(sn, key, &v, map[string]any{"note": note, "n_samples": computed["n_samples"], "confidence": computed["confidence"]}); err != nil {
			return nil, 500, err.Error()
		}
		out["applied"], out["source"], out["note"] = true, "user", note
		if stored, _ := s.storedParam(sn, key); stored != nil {
			out["updated_at"] = stored["updated_at"]
		}
	}
	return out, 200, ""
}
