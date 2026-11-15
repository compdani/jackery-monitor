package server

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/compdani/jackery-monitor/internal/forecast"
)

func (s *Server) requestSerial(r *http.Request, b map[string]any) string {
	if sn := str(b, "device_sn"); sn != "" {
		return sn
	}
	if sn := r.URL.Query().Get("device_sn"); sn != "" {
		return sn
	}
	return s.activeSerial()
}
func numberArg(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case string:
		f, e := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, e == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}

// forecastRoutes serves the Forecast tab, Device tab parameter and diagnostic
// endpoints with server.py's paths, status codes and wrappers.
func (s *Server) forecastRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	p, m, q := r.URL.Path, r.Method, r.URL.Query()
	ctx := r.Context()
	switch {
	case p == "/api/forecast" && m == "GET":
		sn := q.Get("device_sn")
		out := s.buildForecast(ctx, sn, time.Now().Unix(), true)
		if q.Get("_diag") != "" && q.Get("_diag") != "0" && sn != "" {
			h, _, _, err := s.energyInputs(sn, time.Now().Unix(), 14*24)
			if err != nil {
				out["idle_window_diag"] = map[string]any{"error": err.Error()}
			} else {
				out["idle_window_diag"] = diagnoseIdleWindows(h)
			}
		}
		jsonOut(w, 200, out)
	case p == "/api/location" && m == "GET":
		loc, ok, err := s.savedLocation()
		if err != nil {
			storageError(w, err)
			return true
		}
		if !ok {
			jsonOut(w, 200, map[string]any{"latitude": nil, "longitude": nil})
			return true
		}
		if str(loc, "label") == "" {
			client, _ := s.forecastClients()
			if label := client.ReverseGeocode(ctx, forecast.Number(loc, "latitude"), forecast.Number(loc, "longitude")); label != "" {
				loc["label"] = string([]rune(label)[:min(200, len([]rune(label)))])
				if err := s.Store.SaveSecret(s.Key, "location", loc); err != nil {
					s.Store.App.Logger().Debug("location label backfill", "error", err)
				}
			}
		}
		jsonOut(w, 200, loc)
	case p == "/api/location" && m == "POST":
		lat, ok1 := numberArg(b["latitude"])
		lon, ok2 := numberArg(b["longitude"])
		if !ok1 || !ok2 || !forecast.ValidLocation(lat, lon) {
			fail(w, 400, "latitude/longitude out of range")
			return true
		}
		rec := map[string]any{"latitude": lat, "longitude": lon, "updated_at": float64(time.Now().UnixNano()) / 1e9}
		if label, ok := b["label"].(string); ok && strings.TrimSpace(label) != "" {
			runes := []rune(strings.TrimSpace(label))
			rec["label"] = string(runes[:min(200, len(runes))])
		}
		if err := s.Store.SaveSecret(s.Key, "location", rec); err != nil {
			storageError(w, err)
			return true
		}
		weather, solar := s.forecastClients()
		weather.ClearCache()
		solar.ClearCache()
		jsonOut(w, 200, rec)
	case p == "/api/location/geocode" && m == "GET":
		count := 5
		if v, e := strconv.Atoi(q.Get("count")); e == nil {
			count = v
		}
		client, _ := s.forecastClients()
		jsonOut(w, 200, client.Geocode(ctx, q.Get("q"), count))
	case p == "/api/forecast/solar_array" && m == "GET":
		sn := s.requestSerial(r, nil)
		if sn == "" {
			fail(w, 400, "device_sn required")
			return true
		}
		plane, ok, err := s.weatherStore().Plane(sn)
		if err != nil {
			storageError(w, err)
			return true
		}
		key, _ := s.weatherStore().SolarKey(s.Key)
		var array any
		if ok {
			array = map[string]any{"declination": plane.Declination, "azimuth": plane.Azimuth, "kwp": plane.KWP}
		}
		jsonOut(w, 200, map[string]any{"device_sn": sn, "array": array, "has_key": key != ""})
	case p == "/api/forecast/solar_array" && m == "POST":
		sn := s.requestSerial(&http.Request{URL: r.URL}, b)
		if str(b, "device_sn") == "" {
			sn = s.activeSerial()
		}
		if sn == "" {
			fail(w, 400, "device_sn required")
			return true
		}
		dec, ok1 := numberArg(b["declination"])
		az, ok2 := numberArg(b["azimuth"])
		kwp, ok3 := numberArg(b["kwp"])
		if _, valid := forecast.ValidatePlane(dec, az, kwp); !ok1 || !ok2 || !ok3 || !valid {
			fail(w, 400, "declination 0-90, azimuth -180-180 (0=south), kwp 0.01-100")
			return true
		}
		plane, err := s.weatherStore().SavePlane(sn, dec, az, kwp)
		if err != nil {
			storageError(w, err)
			return true
		}
		_, solar := s.forecastClients()
		solar.ClearCache()
		jsonOut(w, 200, map[string]any{"device_sn": sn, "declination": plane.Declination, "azimuth": plane.Azimuth, "kwp": plane.KWP})
	case p == "/api/forecast/solar_array/infer" && m == "POST":
		loc, ok, err := s.savedLocation()
		if err != nil || !ok {
			fail(w, 400, "location not set")
			return true
		}
		sn := q.Get("device_sn")
		if sn == "" {
			sn = s.activeSerial()
		}
		if sn == "" {
			fail(w, 400, "device_sn required")
			return true
		}
		now := time.Now().Unix()
		hist, _, _, err := s.energyInputs(sn, now, 14*24)
		if err != nil {
			storageError(w, err)
			return true
		}
		wx := s.fitWeather(ctx, now, 14*24)
		out := forecast.InferPlane(hist, wx, forecast.Number(loc, "latitude"), forecast.Number(loc, "longitude"))
		if out["error"] != nil && out["declination"] == nil {
			peak := 0.0
			for _, h := range hist {
				peak = math.Max(peak, forecast.Number(h, "solar_w"))
			}
			fail(w, 400, fmt.Sprintf("%v (paired hours=%v, weather=%d, energy hours=%d, peak solar=%d W)", out["error"], valueOr(out["n_samples"], 0), len(wx), len(hist), int(math.Round(peak))))
			return true
		}
		jsonOut(w, 200, out)
	case p == "/api/forecast/solar_key":
		ws := s.weatherStore()
		_, solar := s.forecastClients()
		switch m {
		case "GET":
			key, _ := ws.SolarKey(s.Key)
			jsonOut(w, 200, map[string]any{"has_key": key != ""})
		case "POST":
			key, _ := b["api_key"].(string)
			if strings.TrimSpace(key) == "" {
				fail(w, 400, "api_key required")
				return true
			}
			if err := ws.SaveSolarKey(s.Key, key); err != nil {
				fail(w, 500, "failed to save key")
				return true
			}
			solar.ClearCache()
			jsonOut(w, 200, map[string]any{"ok": true})
		case "DELETE":
			if err := ws.ClearSolarKey(); err != nil {
				storageError(w, err)
				return true
			}
			solar.ClearCache()
			jsonOut(w, 200, map[string]any{"ok": true})
		default:
			fail(w, 405, "method not allowed")
		}
	case p == "/api/forecast/load_schedule" && m == "GET":
		sn := s.requestSerial(r, nil)
		if sn == "" {
			fail(w, 400, "device_sn required")
			return true
		}
		sched, err := s.loadSchedule(sn)
		if err != nil {
			storageError(w, err)
			return true
		}
		learned := []map[string]any{}
		now := time.Now().Unix()
		if h, _, _, err := s.energyInputs(sn, now, 14*24); err == nil {
			learned = learnedProfile(forecast.FitLoadProfile(h, now, s.carLoad(sn), 3600))
		}
		out := map[string]any{"device_sn": sn, "learned_profile": learned}
		for k, v := range sched {
			out[k] = v
		}
		jsonOut(w, 200, out)
	case p == "/api/forecast/load_schedule" && m == "POST":
		sn := str(b, "device_sn")
		if sn == "" {
			sn = s.activeSerial()
		}
		if sn == "" {
			fail(w, 400, "device_sn required")
			return true
		}
		payload := map[string]any{}
		for k, v := range b {
			payload[k] = v
		}
		if v := q.Get("copy_learned"); v != "" && v != "0" {
			now := time.Now().Unix()
			h, _, _, err := s.energyInputs(sn, now, 14*24)
			if err != nil {
				fail(w, 400, "could not copy learned profile: "+err.Error())
				return true
			}
			payload["windows"] = windowsFromProfile(forecast.FitLoadProfile(h, now, s.carLoad(sn), 3600))
			payload["mode"] = "scheduled"
		}
		rec := NormalizeLoadSchedule(payload)
		if err := s.Store.Put("settings", "load_schedule:"+sn, sn, rec); err != nil {
			storageError(w, err)
			return true
		}
		out := map[string]any{"device_sn": sn}
		for k, v := range rec {
			out[k] = v
		}
		jsonOut(w, 200, out)
	case p == "/api/devices/params" && m == "GET":
		s.paramsGet(w, r)
	case strings.HasPrefix(p, "/api/devices/params") && m == "POST":
		s.paramsPost(w, r, b)
	case p == "/api/diagnostics/row_soc" && m == "GET":
		s.rowSOCDiagnostics(w)
	case p == "/api/diagnostics/parasitic_fit_windows" && m == "GET":
		s.parasiticWindows(w, r)
	default:
		return false
	}
	return true
}
func valueOr(v any, d any) any {
	if v == nil {
		return d
	}
	return v
}
func (s *Server) tzOffset() int {
	loc, _, _ := s.savedLocation()
	return int(forecast.Number(loc, "utc_offset_seconds"))
}
func (s *Server) paramsGet(w http.ResponseWriter, r *http.Request) {
	sn := s.requestSerial(r, nil)
	if sn == "" {
		fail(w, 400, "no active device")
		return
	}
	rows, err := s.resolveDeviceParams(r.Context(), sn, 14*24)
	if err != nil {
		storageError(w, err)
		return
	}
	out := map[string]any{"device_sn": sn, "params": rows, "tz_offset_seconds": s.tzOffset()}
	if r.URL.Query().Get("debug_key") == "max_charge_w" {
		now := time.Now().Unix()
		h, _, _, err := s.energyInputs(sn, now, 14*24)
		if err != nil {
			out["debug"] = map[string]any{"key": "max_charge_w", "error": err.Error()}
		} else {
			wx := s.fitWeather(r.Context(), now, 14*24)
			v, n := forecast.FitMaxCharge(h, wx, s.tzOffset(), 100, 6, .95)
			var value any
			if v != nil {
				value = *v
			}
			// The Go fit does not expose per-sample candidates; counts and the
			// resulting value are reported instead of Python's sample list.
			out["debug"] = map[string]any{"key": "max_charge_w", "tz_offset_seconds": s.tzOffset(), "weather_observations": len(wx), "n_used_in_fit": n, "value": value, "samples": []any{}}
		}
	}
	jsonOut(w, 200, out)
}
func (s *Server) paramsPost(w http.ResponseWriter, r *http.Request, b map[string]any) {
	p := r.URL.Path
	if p != "/api/devices/params" && p != "/api/devices/params/refit" && p != "/api/devices/params/fit" {
		fail(w, 404, "not found")
		return
	}
	sn, key := str(b, "device_sn"), str(b, "key")
	if sn == "" {
		sn = s.activeSerial()
	}
	if p == "/api/devices/params/fit" {
		out, status, detail := s.fitDeviceParam(r.Context(), sn, key, b["lookback_hours"], b["apply"] == true)
		if status != 200 {
			fail(w, status, detail)
		} else {
			jsonOut(w, 200, out)
		}
		return
	}
	if sn == "" || key == "" {
		fail(w, 400, "device_sn and key required")
		return
	}
	if !knownParam(key) {
		fail(w, 400, fmt.Sprintf("unknown param key: %q", key))
		return
	}
	hours := 14 * 24
	if p == "/api/devices/params" {
		if b["value"] == nil {
			if err := s.setParam(sn, key, nil, nil); err != nil {
				storageError(w, err)
				return
			}
		} else {
			v, ok := numberArg(b["value"])
			if !ok {
				fail(w, 400, "value must be numeric or null")
				return
			}
			if err := s.setParam(sn, key, &v, map[string]any{"note": "set via Device tab"}); err != nil {
				storageError(w, err)
				return
			}
		}
	} else {
		// Refit drops the stored row, then re-resolves over the optional window.
		if err := s.setParam(sn, key, nil, nil); err != nil {
			storageError(w, err)
			return
		}
		if b["lookback_hours"] != nil {
			hours = clampFitLookback(b["lookback_hours"])
		}
	}
	out := map[string]any{"device_sn": sn, "key": key}
	for k, v := range s.resolveParam(r.Context(), sn, key, hours) {
		out[k] = v
	}
	if p == "/api/devices/params/refit" && b["lookback_hours"] != nil {
		out["lookback_hours"] = hours
	}
	jsonOut(w, 200, out)
}

// diagnoseIdleWindows ports forecaster.diagnose_idle_windows.
func diagnoseIdleWindows(history []forecast.Row) map[string]any {
	rows := []forecast.Row{}
	for _, r := range history {
		if r["ts"] != nil {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return forecast.Number(rows[i], "ts") < forecast.Number(rows[j], "ts") })
	rejected := map[string]int{"missing_soc": 0, "soc_drop_below_min_pp": 0, "solar_above_noise": 0, "ac_input_above_noise": 0, "dt_out_of_range": 0, "out_w_under_50": 0}
	qualifying := 0
	for i := 0; i+1 < len(rows); i++ {
		a, b := rows[i], rows[i+1]
		sa, sb := forecast.RowSOC(a), forecast.RowSOC(b)
		if sa == nil || sb == nil {
			rejected["missing_soc"]++
			continue
		}
		minPP := 2.0
		if a["system_soc"] != nil && b["system_soc"] != nil {
			minPP = .5
		}
		dt := (forecast.Number(b, "ts") - forecast.Number(a, "ts")) / 3600
		switch {
		case *sa-*sb < minPP:
			rejected["soc_drop_below_min_pp"]++
		case forecast.Number(a, "solar_wh") > 50:
			rejected["solar_above_noise"]++
		case forecast.Number(a, "ac_input_wh") > 50:
			rejected["ac_input_above_noise"]++
		case dt < .5 || dt > 6:
			rejected["dt_out_of_range"]++
		case forecast.Number(a, "output_wh")/dt < 50:
			rejected["out_w_under_50"]++
		default:
			qualifying++
		}
	}
	return map[string]any{"total_pairs": max(0, len(rows)-1), "qualifying_windows": qualifying, "needed_windows": 5, "rejected": rejected}
}

// rowSOCDiagnostics reports system_soc coverage. Python keeps process-global
// counters inside _row_soc; Go computes the same counters on demand from each
// device's 14-day fit window, so lifetime and last-fit windows coincide.
func (s *Server) rowSOCDiagnostics(w http.ResponseWriter) {
	now := time.Now().Unix()
	rows, err := s.Store.List("devices", "", 0, 0)
	if err != nil {
		storageError(w, err)
		return
	}
	hits, fallbacks, none := 0, 0, 0
	devices := []map[string]any{}
	for _, row := range rows {
		d, _ := row["device"].(map[string]any)
		sn := str(d, "device_sn")
		if sn == "" {
			continue
		}
		main, pack, _, _ := s.capacityHints(sn)
		if h, _, _, err := s.energyInputs(sn, now, 14*24); err == nil {
			for _, r := range h {
				switch {
				case r["system_soc"] != nil:
					hits++
				case r["battery_pct"] != nil:
					fallbacks++
				default:
					none++
				}
			}
		}
		packs, _ := s.Store.List("battery_packs", sn, float64(now-14*86400), 0)
		distinct, orders := map[string]bool{}, map[string]bool{}
		var latest any
		for _, p := range packs {
			latest = p["ts"]
			items, _ := p["packs"].([]any)
			for _, it := range items {
				pm, _ := it.(map[string]any)
				for _, k := range []string{"device_sn", "sn", "id"} {
					if v := fmt.Sprint(pm[k]); pm[k] != nil && v != "" {
						distinct[v] = true
						break
					}
				}
				if pm["order"] != nil {
					orders[fmt.Sprint(pm["order"])] = true
				}
			}
		}
		devices = append(devices, map[string]any{"device_sn": sn, "model_code": d["model_code"], "model_name": d["model_name"], "main_capacity_wh": nilZero(main), "pack_capacity_wh": nilZero(pack), "n_pack_snapshots_14d": len(packs), "distinct_packs_seen": len(distinct), "distinct_orders_seen": len(orders), "latest_pack_snapshot_ts": latest})
	}
	total := hits + fallbacks + none
	window := map[string]any{"system_soc_hits": hits, "battery_pct_fallbacks": fallbacks, "none_returns": none}
	stats := map[string]any{"system_soc_hits": hits, "battery_pct_fallbacks": fallbacks, "none_returns": none, "last_fit_at": now, "last_fit_caller": "diagnostics_scan", "last_fit_window": window, "since_ts": now - 14*86400, "last_fit_at_iso": time.Unix(now, 0).UTC().Format("2006-01-02T15:04:05Z")}
	var ratio any
	verdict := "no_data"
	if total > 0 {
		ratio = math.Round(float64(fallbacks)/float64(total)*1e4) / 1e4
		switch {
		case float64(hits)/float64(total) >= .9:
			verdict = "system_soc_dominant"
		case float64(fallbacks)/float64(total) >= .9:
			verdict = "fallback_dominant"
		default:
			verdict = "mixed"
		}
	}
	jsonOut(w, 200, map[string]any{"stats": stats, "interpretation": map[string]any{"fallback_ratio_lifetime": ratio, "fallback_ratio_last_fit": ratio, "verdict": verdict}, "devices": devices})
}
func nilZero(v float64) any {
	if v <= 0 {
		return nil
	}
	return v
}

// parasiticWindows ports /api/diagnostics/parasitic_fit_windows.
func (s *Server) parasiticWindows(w http.ResponseWriter, r *http.Request) {
	sn := s.requestSerial(r, nil)
	if sn == "" {
		fail(w, 400, "no active device")
		return
	}
	hours := 14 * 24
	if v, e := strconv.Atoi(r.URL.Query().Get("hours")); e == nil {
		hours = v
	}
	hours = max(1, min(hours, 30*24))
	h, capacity, _, err := s.energyInputs(sn, time.Now().Unix(), hours)
	if err != nil {
		storageError(w, err)
		return
	}
	capacity = s.resolvedCapacity(sn, capacity)
	sort.Slice(h, func(i, j int) bool { return forecast.Number(h[i], "ts") < forecast.Number(h[j], "ts") })
	overhead := .10
	windows := []map[string]any{}
	opt := func(ok bool, v float64) any {
		if !ok {
			return nil
		}
		return math.Round(v*10) / 10
	}
	for i := 0; i+1 < len(h); i++ {
		a, b := h[i], h[i+1]
		dt := (forecast.Number(b, "ts") - forecast.Number(a, "ts")) / 3600
		out := forecast.Number(a, "output_wh")
		if forecast.Number(a, "solar_wh") > 50 || forecast.Number(a, "ac_input_wh") > 50 || dt < .5 || dt > 6 || out/math.Max(dt, 1e-6) < 50 || a["battery_pct"] == nil || b["battery_pct"] == nil {
			continue
		}
		mainDrop := forecast.Number(a, "battery_pct") - forecast.Number(b, "battery_pct")
		load := out / dt
		drainMain := mainDrop * capacity / 100 / dt
		hasSys := a["system_soc"] != nil && b["system_soc"] != nil
		sysDrop := forecast.Number(a, "system_soc") - forecast.Number(b, "system_soc")
		drainSys := sysDrop * capacity / 100 / dt
		r2 := func(v any) any {
			if v == nil {
				return nil
			}
			return math.Round(forecast.Number(map[string]any{"v": v}, "v")*100) / 100
		}
		var sysDropOut any
		if hasSys {
			sysDropOut = math.Round(sysDrop*100) / 100
		}
		windows = append(windows, map[string]any{"ts_iso": time.Unix(int64(forecast.Number(a, "ts")), 0).UTC().Format("2006-01-02T15:04:05Z"), "dt_h": math.Round(dt*100) / 100, "main_soc_a": a["battery_pct"], "main_soc_b": b["battery_pct"], "main_drop_pp": math.Round(mainDrop*100) / 100, "system_soc_a": r2(a["system_soc"]), "system_soc_b": r2(b["system_soc"]), "system_drop_pp": sysDropOut, "load_w": math.Round(load*10) / 10, "observed_drain_w_main": opt(mainDrop > 0, drainMain), "observed_drain_w_system": opt(hasSys && sysDrop > 0, drainSys), "implied_parasitic_w_main": opt(mainDrop > 0, drainMain-load*(1+overhead)), "implied_parasitic_w_system": opt(hasSys && sysDrop > 0, drainSys-load*(1+overhead))})
	}
	median := func(key string, rows []map[string]any) any {
		vals := []float64{}
		for _, r := range rows {
			if r[key] != nil {
				vals = append(vals, forecast.Number(r, key))
			}
		}
		if len(vals) == 0 {
			return nil
		}
		sort.Float64s(vals)
		return math.Round(vals[len(vals)/2]*10) / 10
	}
	requireSystem := false
	for _, r := range h {
		requireSystem = requireSystem || r["system_soc"] != nil
	}
	runs := []map[string]any{}
	values, weights := []float64{}, []float64{}
	for _, run := range forecast.CleanDischargeRuns(h, capacity, requireSystem) {
		implied := run.DrainW - run.LoadW*(1+overhead)
		runs = append(runs, map[string]any{"dt_h": math.Round(run.Hours*100) / 100, "load_w": math.Round(run.LoadW*10) / 10, "drain_w": math.Round(run.DrainW*10) / 10, "implied_parasitic_w": math.Round(implied*10) / 10})
		values, weights = append(values, implied), append(weights, run.Hours*run.Hours)
	}
	var weighted any
	if v := forecast.LengthWeightedMedian(values, weights); v != nil {
		weighted = math.Round(*v*10) / 10
	}
	jsonOut(w, 200, map[string]any{"device_sn": sn, "hours": hours, "capacity_wh": capacity, "overhead_pct_assumed": overhead, "n_windows": len(windows), "median_implied_parasitic_w_main": median("implied_parasitic_w_main", windows), "median_implied_parasitic_w_system": median("implied_parasitic_w_system", windows), "n_runs": len(runs), "median_implied_parasitic_w_runs": median("implied_parasitic_w", runs), "weighted_median_implied_parasitic_w_runs": weighted, "runs": runs, "windows": windows})
}
