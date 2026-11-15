package server

import (
	"github.com/compdani/jackery-monitor/internal/forecast"
	"net/http"
	"strconv"
)

func (s *Server) packRoutes(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/devices/battery_packs" {
		return false
	}
	if r.Method != "GET" {
		fail(w, 405, "method not allowed")
		return true
	}
	sn := s.energySerial(r, nil)
	out := map[string]any{"device_sn": sn, "packs": []any{}, "cached": true, "fetched_at": nil, "main_soc_pct": nil, "error": nil}
	if sn == "" {
		out["device_sn"] = nil
		out["error"] = "no device"
		jsonOut(w, 200, out)
		return true
	}
	rows, err := s.Store.List("battery_packs", sn, 0, 0)
	if err != nil {
		storageError(w, err)
		return true
	}
	packs := []any{}
	if len(rows) > 0 {
		latest := rows[len(rows)-1]["ts"]
		out["fetched_at"] = latest
		for _, row := range rows {
			if row["ts"] == latest {
				if items, ok := row["packs"].([]any); ok {
					packs = append(packs, items...)
				}
			}
		}
	}
	device, _ := s.Store.Get("devices", sn)
	d, _ := device["device"].(map[string]any)
	tele, _ := device["telemetry"].(map[string]any)
	out["main_soc_pct"] = tele["battery_percent"]
	model, _ := d["model_code"].(float64)
	out["main_capacity_wh"] = forecast.BatteryCapacity(int(model))
	out["pack_capacity_wh"] = forecast.ExpansionCapacity(int(model))
	out["packs"] = packs
	fresh, _ := strconv.ParseBool(r.URL.Query().Get("fresh"))
	if fresh {
		if s.Bridge == nil {
			out["error"] = "device backend unavailable"
		} else {
			result, err := s.Bridge.Call(r.Context(), "get_battery_packs", map[string]any{"device_sn": sn})
			if err != nil {
				out["error"] = err.Error()
			} else {
				for _, key := range []string{"packs", "fetched_at", "cached"} {
					out[key] = result[key]
				}
			}
		}
	}
	jsonOut(w, 200, out)
	return true
}
