package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/devices"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/store"
)

// SmartForecastSource supplies weather rows without coupling the controller to
// an account or transport. The default uses persisted weather only.
type SmartForecastSource interface {
	Weather(context.Context, string, int64) ([]forecast.Row, error)
}
type SmartPlug interface {
	automation.Switch
	Power(context.Context, string) (bool, error)
}
type nativeSmartPlug struct{ devices.KasaSwitch }

func (p nativeSmartPlug) Power(ctx context.Context, host string) (bool, error) {
	info, err := p.Client.Status(ctx, host)
	return info.IsOn, err
}
func (s *Server) smartPlug() SmartPlug {
	if s.SmartPlug != nil {
		return s.SmartPlug
	}
	s.initControllers()
	return nativeSmartPlug{devices.KasaSwitch{Client: s.controllers.client}}
}
func rowsForecast(rows []map[string]any) []forecast.Row {
	out := []forecast.Row{}
	for _, r := range rows {
		out = append(out, forecast.Row(r))
	}
	return out
}
func (s *Server) smartInputs(ctx context.Context, sn string, now int64) ([]forecast.Row, []forecast.Row, float64, *float64, int, error) {
	minutes, err := s.Store.EnergyMinutes(sn, float64(now-14*86400))
	if err != nil {
		return nil, nil, 0, nil, 0, err
	}
	history := store.EnergyHistory(minutes, float64(now-14*86400), 3600)
	cached, err := s.Store.Get("devices", sn)
	if err != nil && !isMissing(err) {
		return nil, nil, 0, nil, 0, err
	}
	dev, _ := cached["device"].(map[string]any)
	model := int(forecast.Number(dev, "model_code"))
	main, pack := forecast.BatteryCapacity(model), forecast.ExpansionCapacity(model)
	capacity := main
	if v := forecast.Number(cached, "capacity_wh_override"); v > 0 {
		capacity = v
	} else if v := forecast.Number(dev, "capacity_wh_override"); v > 0 {
		capacity = v
	}
	if capacity <= 0 {
		capacity = 2048
	}
	if err = s.Store.DecorateHistorySOC(sn, history, main, pack); err != nil {
		return nil, nil, 0, nil, 0, err
	}
	var soc *float64
	fresh := store.EnergyHistory(minutes, float64(now-600), 60)
	if err = s.Store.DecorateHistorySOC(sn, fresh, main, pack); err != nil {
		return nil, nil, 0, nil, 0, err
	}
	for _, r := range fresh {
		if forecast.Number(r, "ts") >= float64(now-600) && forecast.Number(r, "ts") <= float64(now) {
			v := r["system_soc"]
			if v == nil {
				v = r["battery_pct"]
			}
			if v != nil {
				n := forecast.Number(map[string]any{"v": v}, "v")
				soc = &n
			}
		}
	}
	loc, _ := s.Store.Secret(s.Key, "location")
	offset := int(forecast.Number(loc, "utc_offset_seconds"))
	var weather []forecast.Row
	if s.SmartForecast != nil {
		weather, err = s.SmartForecast.Weather(ctx, sn, now)
	} else {
		var rows []map[string]any
		rows, err = s.Store.List("weather_forecasts", "", float64(now), 0)
		weather = rowsForecast(rows)
	}
	sort.Slice(weather, func(i, j int) bool { return forecast.Number(weather[i], "ts") < forecast.Number(weather[j], "ts") })
	return rowsForecast(history), weather, capacity, soc, offset, err
}
func (s *Server) evaluateSmart(ctx context.Context, sn string, now int64, record bool) (*automation.ChargePlan, error) {
	s.smart.evaluate.Lock()
	defer s.smart.evaluate.Unlock()
	cfg, err := s.smartConfig(sn)
	if err != nil {
		return nil, err
	}
	if sn == "" || cfg.Mode == "off" {
		return nil, nil
	}
	h, w, capacity, soc, offset, err := s.smartInputs(ctx, sn, now)
	if err != nil {
		return nil, err
	}
	costRaw, err := s.Store.Get("settings", "cost_plan")
	if err != nil && !isMissing(err) {
		return nil, err
	}
	var cost automation.CostPlan
	_ = fromMap(costRaw, &cost)
	fc, base := automation.ChargeForecast{}, automation.ChargeForecast{}
	reason := "no forecast yet — weather service unavailable"
	if soc == nil {
		reason = "fresh battery telemetry unavailable"
	} else if len(w) > 0 {
		opts := forecast.BuildOptions{Now: now, Offset: offset}
		baseline := forecast.Build(h, w, *soc, capacity, opts)
		opts.Simulation.ACFloor = &cfg.Target
		live := forecast.Build(h, w, *soc, capacity, opts)
		if live["ready"] == true {
			_ = fromMap(live, &fc)
			_ = fromMap(baseline, &base)
			if record {
				predictions, _ := live["forecast"].([]forecast.Row)
				if _, e := s.Store.RecordForecast(sn, float64(now), predictions); e != nil {
					return nil, e
				}
			}
		} else {
			reason = "calibrating: insufficient history for forecast"
		}
	}
	plan := automation.SmartPlan(cfg.SmartConfig, soc, fc, &base, cost, capacity, offset, now, reason)
	if !record {
		return &plan, nil
	}
	if cfg.Mode == "active" && cfg.Host != "" {
		plug := s.smartPlug()
		call, cancel := context.WithTimeout(ctx, 10*time.Second)
		on, e := plug.Power(call, cfg.Host)
		cancel()
		if e != nil {
			return &plan, e
		}
		// Reconcile a durable uncertain intent before issuing any new command.
		s.smart.mu.Lock()
		raw, e := s.Store.Get("settings", "smart_charge_state:"+sn)
		if e != nil && !isMissing(e) {
			s.smart.mu.Unlock()
			return &plan, e
		}
		var state automation.SmartState
		data, _ := json.Marshal(raw["state"])
		_ = json.Unmarshal(data, &state)
		if on {
			if state.OnSince == 0 {
				state.OnSince = now
			}
			state.LastAction = "on"
		} else {
			state.LastAction = "off"
			state.OnSince = 0
		}
		e = s.Store.Put("settings", "smart_charge_state:"+sn, sn, map[string]any{"state": state, "pending": false})
		s.smart.mu.Unlock()
		if e != nil {
			return &plan, e
		}
		if e = s.runSmart(ctx, sn, cfg, plan, plug); e != nil {
			return &plan, e
		}
	} else if err = s.runSmart(ctx, sn, cfg, plan, nil); err != nil {
		return &plan, err
	}
	return &plan, nil
}

// SmartChargeLoop is independent of polling and cancels all plug operations on shutdown.
func (s *Server) SmartChargeLoop(ctx context.Context) {
	bo := automation.LoopBackoff{Max: 30 * time.Minute}
	for {
		if err := s.smartTick(ctx, time.Now().Unix()); err != nil {
			s.Store.App.Logger().Error("smart-charge tick", "error", err)
			bo.RecordFailure()
		} else {
			bo.Reset()
		}
		timer := time.NewTimer(bo.NextSleep(5 * time.Minute))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (s *Server) smartTick(ctx context.Context, now int64) error {
	raw, err := s.Store.Get("settings", "smart_charge")
	if err != nil && !isMissing(err) {
		return err
	}
	by, _ := raw["by_device"].(map[string]any)
	if by == nil {
		s.mu.RLock()
		dev, _ := s.snapshot["device"].(map[string]any)
		sn := str(dev, "device_sn")
		s.mu.RUnlock()
		if sn != "" {
			_, err = s.evaluateSmart(ctx, sn, now, true)
		}
		return err
	}
	for sn := range by {
		if sn == "__legacy__" {
			continue
		}
		if _, e := s.evaluateSmart(ctx, sn, now, true); e != nil {
			err = e
		}
	}
	return err
}
func (s *Server) smartHistory(sn string) ([]map[string]any, error) {
	rows, err := s.Store.List("automation_decisions", sn, 0, 0)
	out := []map[string]any{}
	for _, r := range rows {
		if r["controller"] == "smart_charge" || r["_legacy_table"] == "smart_charge_decisions" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return forecast.Number(out[i], "decided_at") > forecast.Number(out[j], "decided_at")
	})
	return out, err
}
func (s *Server) smartLiveRoutes(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if path != "/api/smart_charge/status" && path != "/api/smart_charge/evaluate_now" && path != "/api/smart_charge/decision_details" && path != "/api/smart_charge/backtest" && path != "/api/smart_charge/analytics" {
		return false
	}
	if (path == "/api/smart_charge/evaluate_now" && r.Method != "POST") || (path != "/api/smart_charge/evaluate_now" && r.Method != "GET") {
		fail(w, 405, "method not allowed")
		return true
	}
	sn := r.URL.Query().Get("device_sn")
	if sn == "" {
		dev, _ := s.status(r)["device"].(map[string]any)
		sn = str(dev, "device_sn")
	}
	if path == "/api/smart_charge/evaluate_now" {
		p, e := s.evaluateSmart(r.Context(), sn, time.Now().Unix(), false)
		if e != nil {
			storageError(w, e)
		} else {
			jsonOut(w, 200, map[string]any{"plan": p, "narration": nil})
		}
		return true
	}
	cfg, e := s.smartConfig(sn)
	if e != nil {
		storageError(w, e)
		return true
	}
	history, e := s.smartHistory(sn)
	if e != nil {
		storageError(w, e)
		return true
	}
	if path == "/api/smart_charge/status" {
		h, wx, _, _, offset, e := s.smartInputs(r.Context(), sn, time.Now().Unix())
		if e != nil {
			storageError(w, e)
			return true
		}
		observed, n := forecast.FitMaxCharge(h, wx, offset, 100, 5, .95)
		jsonOut(w, 200, map[string]any{"device_sn": sn, "config": cfg, "history": history[:min(50, len(history))], "observed_max_charge_w": observed, "observed_max_charge_n": n})
		return true
	}
	if path == "/api/smart_charge/decision_details" {
		ts, e := strconv.ParseInt(r.URL.Query().Get("decided_at"), 10, 64)
		if e != nil {
			fail(w, 422, "decided_at required")
			return true
		}
		for _, d := range history {
			if int64(forecast.Number(d, "decided_at")) == ts {
				predictions, e := s.Store.List("predictions", sn, 0, 0)
				if e != nil {
					storageError(w, e)
					return true
				}
				made := 0.0
				trace := []map[string]any{}
				for _, p := range predictions {
					v := forecast.Number(p, "made_at")
					if v <= float64(ts) && v > made {
						made = v
					}
				}
				for _, p := range predictions {
					if forecast.Number(p, "made_at") == made && forecast.Number(p, "target") >= float64(ts) {
						trace = append(trace, p)
					}
				}
				sort.Slice(trace, func(i, j int) bool { return forecast.Number(trace[i], "target") < forecast.Number(trace[j], "target") })
				weather, e := s.Store.List("weather_observations", "", float64(ts-3600), 0)
				if e != nil {
					storageError(w, e)
					return true
				}
				wx := []map[string]any{}
				for _, p := range weather {
					if forecast.Number(p, "ts") <= float64(ts+86400) {
						wx = append(wx, p)
					}
				}
				minutes, e := s.Store.EnergyMinutes(sn, float64(ts))
				if e != nil {
					storageError(w, e)
					return true
				}
				samples := store.EnergyHistory(minutes, float64(ts), 600)
				for _, p := range samples {
					p["soc"] = p["battery_pct"]
				}
				jsonOut(w, 200, map[string]any{"device_sn": sn, "decision": d, "forecast_trace": trace, "forecast_made_at": made, "weather": wx, "samples_trace": samples, "resolved_params": []any{}})
				return true
			}
		}
		fail(w, 404, "decision not found")
		return true
	}
	days := 14
	if path == "/api/smart_charge/backtest" {
		days = 7
	}
	if n, e := strconv.Atoi(r.URL.Query().Get("days")); e == nil {
		days = max(1, min(90, n))
	}
	h, wx, capacity, _, offset, e := s.smartInputs(r.Context(), sn, time.Now().Unix())
	if e != nil {
		storageError(w, e)
		return true
	}
	decisions := []forecast.Row{}
	for _, d := range history {
		if forecast.Number(d, "decided_at") >= float64(time.Now().Unix()-int64(days)*86400) {
			decisions = append(decisions, d)
		}
	}
	if path == "/api/smart_charge/backtest" {
		var target *float64
		if q := r.URL.Query().Get("target_override"); q != "" {
			v, e := strconv.ParseFloat(q, 64)
			if e != nil {
				fail(w, 422, "invalid target")
				return true
			}
			target = &v
		}
		raw, _ := s.Store.Get("settings", "cost_plan")
		var cost automation.CostPlan
		_ = fromMap(raw, &cost)
		results := forecast.ReplayDecisions(decisions, h, wx, forecast.ReplayOptions{Capacity: capacity, MaxChargeW: cfg.MaxChargeW, Cost: cost, Offset: offset, TargetOverride: target})
		jsonOut(w, 200, map[string]any{"device_sn": sn, "days": days, "target_override": target, "capacity_wh": capacity, "max_charge_w": cfg.MaxChargeW, "tz_offset_seconds": offset, "summary": forecast.SummarizeReplay(results), "results": results})
		return true
	}
	samples := []map[string]any{}
	hits, errors := 0, 0
	mae := 0.0
	for _, d := range decisions {
		sun := forecast.Number(d, "sunrise_ts")
		if sun <= 0 || sun > float64(time.Now().Unix()) {
			continue
		}
		actual, e := s.Store.SystemSOCAt(sn, sun, 0, 0)
		if e != nil {
			storageError(w, e)
			return true
		}
		if actual == nil {
			continue
		}
		hit := *actual >= forecast.Number(d, "target_sunrise_soc_pct")
		if hit {
			hits++
		}
		var delta any
		if d["predicted_sunrise_soc_pct"] != nil {
			v := *actual - forecast.Number(d, "predicted_sunrise_soc_pct")
			delta = v
			if v < 0 {
				v = -v
			}
			mae += v
			errors++
		}
		samples = append(samples, map[string]any{"sunrise_ts": sun, "decided_at": d["decided_at"], "actual_sunrise_soc_pct": *actual, "predicted_sunrise_soc_pct": d["predicted_sunrise_soc_pct"], "baseline_predicted_sunrise_soc_pct": d["baseline_predicted_sunrise_soc_pct"], "target_sunrise_soc_pct": d["target_sunrise_soc_pct"], "action": d["action"], "mode": d["mode"], "reason": d["reason"], "executed": d["executed"], "target_hit": hit, "prediction_error_pp": delta})
	}
	summary := map[string]any{"n": len(samples), "target_hit_rate": nil, "mae_pp": nil}
	if len(samples) > 0 {
		summary["target_hit_rate"] = float64(hits) / float64(len(samples))
	}
	if errors > 0 {
		summary["mae_pp"] = mae / float64(errors)
	}
	jsonOut(w, 200, map[string]any{"device_sn": sn, "days": days, "summary": summary, "samples": samples})
	return true
}

// An active smart-charge config owns its plug; rescue remains an emergency ON override.
func (s *Server) smartOwnsHost(host string) bool {
	if host == "" {
		return false
	}
	raw, e := s.Store.Get("settings", "smart_charge")
	if e != nil {
		return false
	}
	by, ok := raw["by_device"].(map[string]any)
	if !ok {
		c := automation.NormalizeSmart(raw)
		return c.Mode == "active" && c.Host == host
	}
	for _, v := range by {
		m, _ := v.(map[string]any)
		c := automation.NormalizeSmart(m)
		if c.Mode == "active" && c.Host == host {
			return true
		}
	}
	return false
}

func (p nativeSmartPlug) Watts(ctx context.Context, host string) (*float64, error) {
	info, e := p.Client.Status(ctx, host)
	return info.PowerW, e
}
