package server

import (
	"github.com/compdani/jackery-monitor/internal/forecast"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"
)

func (s *Server) energy(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "GET" {
		return false
	}
	switch r.URL.Path {
	case "/api/devices", "/api/energy/devices":
		rows, e := s.Store.List("devices", "", 0, 1000)
		if e != nil {
			storageError(w, e)
			return true
		}
		devices := []any{}
		for _, row := range rows {
			if d, ok := row["device"].(map[string]any); ok {
				devices = append(devices, s.deviceAlias(d))
			}
		}
		var selected any
		if cloud, ok := s.status(r)["cloud"].(map[string]any); ok {
			selected = cloud["selected_device_id"]
		}
		jsonOut(w, 200, map[string]any{"devices": devices, "selected_device_id": selected})
		return true
	case "/api/energy/history", "/api/energy/totals":
		sn := r.URL.Query().Get("device_sn")
		if sn == "" {
			if d, ok := s.status(r)["device"].(map[string]any); ok {
				sn = str(d, "device_sn")
			}
		}
		if sn == "" {
			if r.URL.Path == "/api/energy/totals" {
				jsonOut(w, 200, map[string]any{"device_sn": nil, "lifetime": map[string]any{"input_wh": 0, "output_wh": 0}})
			} else {
				jsonOut(w, 200, map[string]any{"device_sn": nil, "history": []any{}})
			}
			return true
		}
		hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
		if hours == 0 {
			hours = 24
		}
		hours = max(1, min(8760, hours))
		since := float64(time.Now().Unix() - int64(hours)*3600)
		if r.URL.Path == "/api/energy/totals" {
			since = 0
		}
		rows, e := s.Store.List("samples", sn, since-3600, 100000)
		if e != nil {
			storageError(w, e)
			return true
		}
		bucket, _ := strconv.Atoi(r.URL.Query().Get("bucket_s"))
		if bucket != 900 && bucket != 1800 && bucket != 3600 {
			bucket = 900
		}
		bucket = max(bucket, int(math.Ceil(float64(hours*3600)/1500/900))*900)
		points := map[int64]map[string]any{}
		totals := map[string]any{"input_wh": 0.0, "output_wh": 0.0, "solar_wh": 0.0, "ac_input_wh": 0.0}
		// Integrate adjacent observations with a five-minute gap cap. Never
		// integrate an outage or a single point into invented energy.
		for i := 1; i < len(rows); i++ {
			a, z := rows[i-1], rows[i]
			start, end := forecast.Number(a, "ts"), forecast.Number(z, "ts")
			if end <= start || end-start > 300 {
				continue
			}
			start = math.Max(start, since)
			if end <= start {
				continue
			}
			for start < end {
				ts := int64(start) / int64(bucket) * int64(bucket)
				stop := math.Min(end, float64(ts+int64(bucket)))
				p := points[ts]
				if p == nil {
					p = map[string]any{"ts": ts, "input_wh": 0.0, "output_wh": 0.0, "solar_wh": 0.0, "ac_input_wh": 0.0}
					points[ts] = p
				}
				for _, pair := range [][2]string{{"input_power_w", "input_wh"}, {"output_power_w", "output_wh"}, {"solar_w", "solar_wh"}, {"ac_input_w", "ac_input_wh"}} {
					wh := math.Max(0, forecast.Number(a, pair[0])) * (stop - start) / 3600
					p[pair[1]] = p[pair[1]].(float64) + wh
					totals[pair[1]] = totals[pair[1]].(float64) + wh
				}
				start = stop
			}
		}
		if r.URL.Path == "/api/energy/totals" {
			jsonOut(w, 200, map[string]any{"device_sn": sn, "lifetime": totals})
			return true
		}
		keys := []int64{}
		for k := range points {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		out := []any{}
		for _, k := range keys {
			out = append(out, points[k])
		}
		jsonOut(w, 200, map[string]any{"device_sn": sn, "hours": hours, "bucket_s": bucket, "history": out})
		return true
	}
	return false
}
