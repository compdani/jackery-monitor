package server

import (
	"encoding/json"
	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/store"
	"math"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) energySerial(r *http.Request, b map[string]any) string {
	sn := r.URL.Query().Get("device_sn")
	if sn == "" {
		sn = str(b, "device_sn")
	}
	if sn == "" {
		d, _ := s.status(r)["device"].(map[string]any)
		sn = str(d, "device_sn")
	}
	return sn
}
func (s *Server) energyOffset() int {
	loc, err := s.Store.Secret(s.Key, "location")
	if err != nil {
		return 0
	}
	v, _ := loc["utc_offset_seconds"].(float64)
	return int(v)
}
func (s *Server) energyRead(w http.ResponseWriter, r *http.Request) bool {
	sn := s.energySerial(r, nil)
	path := r.URL.Path
	offset := s.energyOffset()
	now := float64(time.Now().Unix())
	if sn == "" {
		out := map[string]any{"device_sn": nil}
		switch path {
		case "/api/energy/totals":
			for k, v := range store.EnergyTotals(nil, now, offset) {
				out[k] = v
			}
		case "/api/energy/daily":
			out["daily"] = []any{}
			out["days"] = 0
			out["tz_offset_s"] = 0
		default:
			out["history"] = []any{}
		}
		jsonOut(w, 200, out)
		return true
	}
	// Bound history/daily reads before decoding samples; totals alone need lifetime data.
	since := 0.0
	if path == "/api/energy/history" {
		hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
		if hours == 0 {
			hours = 24
		}
		since = now - float64(max(1, min(8760, hours)))*3600
	} else if path == "/api/energy/daily" {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		if days == 0 {
			days = 90
		}
		since = now - float64(max(1, min(365, days)))*86400
	}
	minutes, err := s.Store.EnergyMinutes(sn, since)
	if err != nil {
		storageError(w, err)
		return true
	}
	if path == "/api/energy/totals" {
		out := store.EnergyTotals(minutes, now, offset)
		out["device_sn"] = sn
		var plan automation.CostPlan
		v, err := s.Store.Get("settings", "cost_plan")
		if isMissing(err) {
			v = map[string]any{"type": "flat", "rate_per_kwh": .30, "currency": "USD"}
		}
		if err == nil || isMissing(err) {
			data, _ := json.Marshal(v)
			json.Unmarshal(data, &plan)
			for _, window := range []string{"lifetime", "today", "last_7d", "last_30d"} {
				totals := out[window].(map[string]any)
				since, _ := totals["since_ts"].(float64)
				rows := []automation.Energy{}
				for _, m := range minutes {
					ts, _ := m["ts"].(float64)
					if ts < since {
						continue
					}
					n := func(k string) float64 { v, _ := m[k].(float64); return v }
					ac := n("ac_input_wh")
					rows = append(rows, automation.Energy{TS: int64(ts), InputWH: n("input_wh"), OutputWH: n("output_wh"), SolarWH: n("solar_wh"), ACInputWH: &ac})
				}
				totals["savings"] = automation.Savings(rows, plan, offset)
				out[window+"_savings"] = totals["savings"]
			}
		}
		jsonOut(w, 200, out)
		return true
	}
	if path == "/api/energy/daily" {
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		if days == 0 {
			days = 90
		}
		days = max(1, min(365, days))
		filtered := []map[string]any{}
		for _, m := range minutes {
			ts, _ := m["ts"].(float64)
			if ts >= now-float64(days)*86400 {
				filtered = append(filtered, m)
			}
		}
		jsonOut(w, 200, map[string]any{"device_sn": sn, "days": days, "tz_offset_s": offset, "daily": store.EnergyDaily(filtered, offset)})
		return true
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours == 0 {
		hours = 24
	}
	hours = max(1, min(8760, hours))
	bucket, _ := strconv.Atoi(r.URL.Query().Get("bucket_s"))
	if bucket != 900 && bucket != 1800 && bucket != 3600 {
		bucket = 900
	}
	bucket = max(bucket, hours*3600/1500)
	history := store.EnergyHistory(minutes, now-float64(hours)*3600, bucket)
	device, _ := s.Store.Get("devices", sn)
	d, _ := device["device"].(map[string]any)
	model, _ := d["model_code"].(float64)
	if err := s.Store.DecorateHistorySOC(sn, history, forecast.BatteryCapacity(int(model)), forecast.ExpansionCapacity(int(model))); err != nil {
		storageError(w, err)
		return true
	}
	jsonOut(w, 200, map[string]any{"device_sn": sn, "hours": hours, "bucket_s": bucket, "history": history})
	return true
}
func (s *Server) energyMutation(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	if r.URL.Path == "/api/energy/correct_output" {
		if r.Method != "POST" {
			fail(w, 405, "method not allowed")
			return true
		}
		sn := s.energySerial(r, b)
		start, ok := b["start_ts"].(float64)
		end, ok2 := b["end_ts"].(float64)
		power, ok3 := b["output_w"].(float64)
		if !ok || !ok2 || !ok3 {
			fail(w, 400, "start_ts, end_ts and output_w are required numbers")
			return true
		}
		out, err := s.Store.CorrectOutput(sn, start, end, power)
		if err != nil {
			fail(w, 400, err.Error())
		} else {
			out["ok"] = true
			out["device_sn"] = sn
			out["live_points_patched"] = 0
			jsonOut(w, 200, out)
		}
		return true
	}
	if r.URL.Path != "/api/devices/capacity" {
		return false
	}
	if r.Method == "GET" {
		rows, err := s.Store.List("devices", "", 0, 0)
		if err != nil {
			storageError(w, err)
			return true
		}
		devices := []any{}
		for _, row := range rows {
			d, _ := row["device"].(map[string]any)
			sn := str(d, "device_sn")
			model, _ := d["model_code"].(float64)
			main, pack := forecast.BatteryCapacity(int(model)), forecast.ExpansionCapacity(int(model))
			var auto any
			count := 0
			packs, err := s.Store.List("battery_packs", sn, 0, 0)
			if err != nil {
				storageError(w, err)
				return true
			}
			if len(packs) > 0 {
				latest, _ := packs[len(packs)-1]["ts"].(float64)
				for _, p := range packs {
					if p["ts"] == latest {
						if arr, ok := p["packs"].([]any); ok {
							count += len(arr)
						}
					}
				}
			}
			effective := main
			if count > 0 {
				effective += float64(count) * pack
				auto = effective
			}
			override := row["capacity_wh_override"]
			if override == nil {
				override = d["capacity_wh_override"]
			}
			if n, ok := override.(float64); ok {
				effective = n
			}
			devices = append(devices, map[string]any{"device_sn": sn, "name": d["name"], "model_code": d["model_code"], "default_capacity_wh": main, "capacity_wh_override": override, "auto_capacity_wh": auto, "pack_count": count, "effective_capacity_wh": effective})
		}
		jsonOut(w, 200, map[string]any{"devices": devices})
		return true
	}
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return true
	}
	v := b["capacity_wh"]
	if v == "" || v == float64(0) {
		v = nil
	}
	if v != nil {
		n, ok := v.(float64)
		if !ok || math.IsNaN(n) {
			fail(w, 400, "invalid capacity")
			return true
		}
	}
	if err := s.Store.SetCapacity(str(b, "device_sn"), v); err != nil {
		fail(w, 400, err.Error())
	} else {
		jsonOut(w, 200, map[string]any{"ok": true, "device_sn": str(b, "device_sn"), "capacity_wh_override": v})
	}
	return true
}
