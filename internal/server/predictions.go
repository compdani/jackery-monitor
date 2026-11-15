package server

import (
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/store"
	"net/http"
	"os"
	"strconv"
	"time"
)

func (s *Server) predictionRoutes(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "GET" || (r.URL.Path != "/api/forecast/accuracy" && r.URL.Path != "/api/daily_summary") {
		return false
	}
	sn := r.URL.Query().Get("device_sn")
	status := s.status(r)
	device, _ := status["device"].(map[string]any)
	if sn == "" {
		sn = str(device, "device_sn")
	}
	cutoff := int64(1790184600)
	if v, e := strconv.ParseInt(os.Getenv("JACKERY_FORECASTER_CUTOFF_TS"), 10, 64); e == nil {
		cutoff = v
	}
	var serial any
	if sn != "" {
		serial = sn
	}
	out := map[string]any{"device_sn": serial, "cutoff_ts": cutoff}
	if r.URL.Path == "/api/daily_summary" {
		days := 7
		if v, e := strconv.Atoi(r.URL.Query().Get("days")); e == nil {
			days = max(1, min(90, v))
		}
		rows := []map[string]any{}
		if sn != "" {
			var e error
			rows, e = s.Store.DailySummary(sn, time.Now().Unix(), days)
			if e != nil {
				storageError(w, e)
				return true
			}
			out["days"] = days
		}
		out["rows"] = rows
		jsonOut(w, 200, out)
		return true
	}
	if v, e := strconv.ParseInt(r.URL.Query().Get("since_ts"), 10, 64); e == nil {
		cutoff = v
		out["cutoff_ts"] = cutoff
	}
	rows := []map[string]any{}
	if sn != "" {
		// Resolve the selected device's catalog hints; an explicitly requested
		// cached device carries its own metadata, independent of bridge selection.
		if str(device, "device_sn") != sn {
			if cached, e := s.Store.Get("devices", sn); e == nil {
				device, _ = cached["device"].(map[string]any)
			}
		}
		model := int(forecast.Number(device, "model_code"))
		main, pack := forecast.BatteryCapacity(model), forecast.ExpansionCapacity(model)
		var e error
		rows, e = s.Store.PredictionAccuracy(sn, store.AccuracyOptions{Now: time.Now().Unix(), MainCapacity: main, PackCapacity: pack})
		if e != nil {
			storageError(w, e)
			return true
		}
	}
	post := []map[string]any{}
	for _, row := range rows {
		if forecast.Number(row, "made_at") >= float64(cutoff) {
			post = append(post, row)
		}
	}
	out["samples"] = rows
	out["summary"] = forecast.BucketAccuracy(rows)
	out["summary_post_fix"] = forecast.BucketAccuracy(post)
	jsonOut(w, 200, out)
	return true
}
