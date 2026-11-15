package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase/core"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"
)

func (s *Server) solarConfig(sn string) (map[string]any, error) {
	raw, e := s.Store.Get("settings", "solar_charge")
	if e != nil && !isMissing(e) {
		return nil, e
	}
	if by, ok := raw["by_device"].(map[string]any); ok {
		raw, _ = by[sn].(map[string]any)
	}
	return automation.NormalizeSolar(raw), nil
}
func (s *Server) solarState(sn string) (automation.SolarState, error) {
	raw, e := s.Store.Get("settings", "solar_charge_state:"+sn)
	var st automation.SolarState
	if e != nil && !isMissing(e) {
		return st, e
	}
	if len(raw) > 0 {
		e = fromMap(raw, &st)
		return st, e
	}
	return st, nil
}
func (s *Server) saveSolar(sn string, st automation.SolarState) error {
	return s.Store.Put("settings", "solar_charge_state:"+sn, sn, toSolarMap(st))
}
func toSolarMap(v any) map[string]any {
	var out map[string]any
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	return out
}

// All solar commands and runtime transitions share one lock, including push trips.
func (s *Server) solarCommand(ctx context.Context, sn string, c map[string]any, st *automation.SolarState, on bool, now float64, load float64) error {
	st.Pending = true
	if e := s.saveSolar(sn, *st); e != nil {
		return e
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	e := s.smartPlug().SetPower(call, str(c, "kasa_device_host"), on)
	if e != nil {
		st.Error = e.Error()
		_ = s.saveSolar(sn, *st)
		return e
	}
	st.Pending = false
	st.Error = ""
	st.On = on
	st.Toggle = now
	st.Power = nil
	st.PowerTS = 0
	st.Learned = nil
	if on {
		st.Baseline = &load
		st.Verify = now + forecast.Number(c, "no_load_verify_delay_s")
	} else {
		st.Baseline = nil
		st.Verify = 0
	}
	return nil
}
func (s *Server) solarTelemetry(ctx context.Context, sn string, t map[string]any, stamp float64) float64 {
	s.solar.mu.Lock()
	defer s.solar.mu.Unlock()
	c, e := s.solarConfig(sn)
	if e != nil {
		return 0
	}
	st, e := s.solarState(sn)
	if e != nil {
		return 0
	}
	load := forecast.Number(t, "output_power_w")
	if c["mode"] == "active" && str(c, "kasa_device_host") != "" && load >= forecast.Number(c, "inverter_protect_load_w") {
		st.Overload = stamp
		// Persist every high-load observation, even when the plug is already off.
		if e = s.saveSolar(sn, st); e != nil {
			return 0
		}
		if st.On || st.Pending {
			_ = s.solarCommand(ctx, sn, c, &st, false, float64(time.Now().Unix()), load)
			_ = s.saveSolar(sn, st)
		}
	}
	return automation.SolarDivertedW(c, st, load, stamp)
}
func (s *Server) evaluateSolar(ctx context.Context, sn string, now int64, record bool) (*automation.SolarChargePlan, error) {
	s.solar.mu.Lock()
	defer s.solar.mu.Unlock()
	c, e := s.solarConfig(sn)
	if e != nil {
		return nil, e
	}
	if sn == "" || c["mode"] == "off" {
		return nil, nil
	}
	st, e := s.solarState(sn)
	if e != nil {
		return nil, e
	}
	h, w, cap, soc, offset, e := s.smartInputs(ctx, sn, now)
	if e != nil {
		return nil, e
	}
	dev, e := s.Store.Get("devices", sn)
	if e != nil && !isMissing(e) {
		return nil, e
	}
	t, _ := dev["telemetry"].(map[string]any)
	load, solar, ac := forecast.Number(t, "output_power_w"), forecast.Number(t, "solar_input_w"), forecast.Number(t, "ac_input_w")
	age := float64(now) - forecast.Number(dev, "last_update_ts")
	if age < 0 {
		age = 1e9
	}
	smart, e := s.smartConfig(sn)
	if e != nil {
		return nil, e
	}
	in := automation.SolarInput{Config: c, SOC: soc, Solar: &solar, Load: &load, ACInput: &ac, Age: age, Target: smart.Target, Capacity: &cap, Now: float64(now), Horizon: 36}
	if record && c["mode"] == "active" && str(c, "kasa_device_host") != "" {
		call, cancel := context.WithTimeout(ctx, 10*time.Second)
		on, err := s.smartPlug().Power(call, str(c, "kasa_device_host"))
		cancel()
		if err != nil {
			return nil, err
		}
		// Reconcile ambiguous writes against the physical relay before another command.
		if on != st.On {
			st.Baseline = nil
			st.Verify = 0
			st.Learned = nil
		}
		st.On = on
		st.Pending = false
		if p, ok := s.smartPlug().(interface {
			Watts(context.Context, string) (*float64, error)
		}); ok {
			call, cancel = context.WithTimeout(ctx, 10*time.Second)
			v, err := p.Watts(call, str(c, "kasa_device_host"))
			cancel()
			if err == nil {
				st.Power = v
				st.PowerTS = float64(now)
			}
		}
	}
	house := load
	if st.On {
		house = math.Max(0, load-forecast.Number(c, "car_load_w"))
	}
	in.Household = &house
	if soc != nil && len(w) > 0 {
		opts := forecast.BuildOptions{Now: now, Offset: offset, CarLoad: forecast.Number(c, "car_load_w")}
		base := forecast.Build(h, w, *soc, cap, opts)
		opts.Simulation.ExtraLoadW = forecast.Number(c, "car_load_w")
		floor := in.Target + forecast.Number(c, "safety_margin_pp") + forecast.Number(c, "on_hysteresis_pp")
		opts.Simulation.ExtraFloor = &floor
		live := forecast.Build(h, w, *soc, cap, opts)
		if base["ready"] == true && live["ready"] == true {
			var b, f automation.ChargeForecast
			_ = fromMap(base, &b)
			_ = fromMap(live, &f)
			sunrise := automation.NextSunrise(b.Hours)
			if sunrise != nil {
				hrs := math.Max(0, float64(*sunrise-now)/3600)
				in.Hours = &hrs
				for _, v := range b.Hours {
					if v.TS >= *sunrise {
						v := v.SOC
						in.Baseline = &v
						break
					}
				}
				for _, v := range f.Hours {
					if v.TS >= *sunrise {
						v := v.SOC
						in.Predicted = &v
						break
					}
				}
			}
			for _, v := range b.Hours {
				if v.TS <= now+36*3600 && (in.MinSOC == nil || v.SOC < *in.MinSOC) {
					x := v.SOC
					in.MinSOC = &x
				}
			}
		}
	}
	// Main-unit full-charge evidence closes the balance window independently of weighted SOC.
	var last *float64
	balanceHistory, balanceErr := s.Store.EnergyMinutes(sn, float64(now)-86400*math.Max(2, forecast.Number(c, "balance_every_days")+1))
	if balanceErr != nil {
		return nil, balanceErr
	}
	for _, r := range balanceHistory {
		if forecast.Number(r, "last_battery_pct") >= forecast.Number(c, "balance_target_main_pct") {
			v := forecast.Number(r, "ts")
			last = &v
		}
	}

	if forecast.Number(t, "battery_percent") >= forecast.Number(c, "balance_target_main_pct") && age <= 90 {
		v := float64(now)
		last = &v
	}
	if last != nil {
		days := (float64(now) - *last) / 86400
		in.DaysSinceFull = &days
	}
	packs, e := s.Store.List("battery_packs", sn, 0, 0)
	if e != nil {
		return nil, e
	}
	if len(packs) > 0 {
		var ps []map[string]any
		b, _ := json.Marshal(packs[len(packs)-1]["packs"])
		_ = json.Unmarshal(b, &ps)
		in.Spread = automation.PackSOCSpread(ps)
	}
	interval := forecast.Number(c, "balance_every_days")
	due := interval > 0 && (in.DaysSinceFull == nil || *in.DaysSinceFull >= interval) || automation.SpreadTriggerFired(c, in.Spread, in.DaysSinceFull)
	if due {
		opened, days, stalled := automation.BalanceWindowStatus(c, st.BalanceOpened, float64(now))
		st.BalanceOpened = &opened
		in.BalanceDue = !stalled
		if stalled {
			in.Stalled = &days
		}
	} else {
		st.BalanceOpened = nil
	}
	p := automation.SolarPlan(in)
	before := "off"
	if st.On {
		before = "on"
	}
	p = automation.GateMinHold(p, st.Toggle, forecast.Number(c, "min_hold_s"), before, float64(now))
	p = automation.GateLoadCeiling(p, &load, before, forecast.Number(c, "max_system_load_w"))
	p = automation.GateInverterProtect(p, st.Overload, forecast.Number(c, "inverter_protect_cooldown_s"), float64(now))
	if p.Action == "on" && st.Cooldown > float64(now) {
		p.Action = "skip"
		p.Reason = "no-load cooldown active"
	}
	if st.On && st.Verify > 0 && float64(now) >= st.Verify && age <= 90 && st.Baseline != nil {
		delta := load - *st.Baseline
		st.Verify = 0
		if delta < forecast.Number(c, "no_load_threshold_w") {
			p.Action = "off"
			p.Reason = "no-load detected"
			st.Cooldown = float64(now) + forecast.Number(c, "no_load_cooldown_s")
		} else {
			st.Learned = &delta
		}
	}
	// Safety OFF bypasses hold time; overload protection is also push-driven.
	if age > 90 || soc == nil || ac > 50 || (soc != nil && *soc <= forecast.Number(c, "comfort_low_pct")) || load >= forecast.Number(c, "inverter_protect_load_w") {
		p.Action = "off"
		p.Reason = "safety OFF: stale/missing telemetry, grid charge, SOC floor or overload"
	}
	if record && c["mode"] == "active" && !s.solarStarted[sn] {
		p.Action = "off"
		p.Reason = "startup fail-safe: force diversion OFF"
	}
	if s.smartOwnsHost(str(c, "kasa_device_host")) {
		p.Action = "skip"
		p.Reason = "plug owned by smart-charge"
	}
	if !record {
		return &p, nil
	}
	executed := false
	if c["mode"] == "active" && str(c, "kasa_device_host") != "" && (p.Action == "on" || p.Action == "off") {
		e = s.solarCommand(ctx, sn, c, &st, p.Action == "on", float64(now), load)
		executed = e == nil
		if executed {
			if s.solarStarted == nil {
				s.solarStarted = map[string]bool{}
			}
			s.solarStarted[sn] = true
		}
	}
	d := toSolarMap(p)
	d["executed"] = executed
	d["device_sn"] = sn
	d["controller"] = "solar_charge"
	d["ts"] = now
	d["error"] = st.Error
	err := s.Store.App.RunInTransaction(func(app core.App) error {
		ss := &store.Store{App: app}
		if err := ss.Put("automation_decisions", fmt.Sprintf("solar_charge:%s:%d", sn, now), sn, d); err != nil {
			return err
		}
		return ss.Put("settings", "solar_charge_state:"+sn, sn, toSolarMap(st))
	})
	if err != nil {
		return &p, err
	}
	return &p, e
}
func (s *Server) SolarChargeLoop(ctx context.Context) {
	bo := automation.LoopBackoff{Max: 10 * time.Minute}
	for {
		if e := s.solarTick(ctx, time.Now().Unix()); e != nil {
			s.Store.App.Logger().Error("solar-charge tick", "error", e)
			bo.RecordFailure()
		} else {
			bo.Reset()
		}
		timer := time.NewTimer(bo.NextSleep(30 * time.Second))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (s *Server) solarTick(ctx context.Context, now int64) error {
	raw, e := s.Store.Get("settings", "solar_charge")
	if e != nil && !isMissing(e) {
		return e
	}
	by, ok := raw["by_device"].(map[string]any)
	if !ok {
		s.mu.RLock()
		d, _ := s.snapshot["device"].(map[string]any)
		sn := str(d, "device_sn")
		s.mu.RUnlock()
		_, e = s.evaluateSolar(ctx, sn, now, true)
		return e
	}
	for sn := range by {
		if sn != "__legacy__" {
			if _, err := s.evaluateSolar(ctx, sn, now, true); err != nil {
				e = err
			}
		}
	}
	return e
}
func (s *Server) solarChargeRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	path := r.URL.Path
	if path != "/api/solar_charge/config" && path != "/api/solar_charge/status" && path != "/api/solar_charge/history" && path != "/api/solar_charge/evaluate_now" {
		return false
	}
	if (path == "/api/solar_charge/evaluate_now" && r.Method != "POST") || (path != "/api/solar_charge/evaluate_now" && r.Method != "GET" && !(path == "/api/solar_charge/config" && r.Method == "POST")) {
		fail(w, 405, "method not allowed")
		return true
	}
	sn := r.URL.Query().Get("device_sn")
	if sn == "" {
		d, _ := s.status(r)["device"].(map[string]any)
		sn = str(d, "device_sn")
	}
	if path == "/api/solar_charge/evaluate_now" {
		p, e := s.evaluateSolar(r.Context(), sn, time.Now().Unix(), false)
		if e != nil {
			storageError(w, e)
		} else {
			jsonOut(w, 200, map[string]any{"device_sn": sn, "plan": p})
		}
		return true
	}
	s.solar.mu.Lock()
	defer s.solar.mu.Unlock()
	if r.Method == "POST" {
		if sn == "" {
			fail(w, 400, "no active device — pass device_sn explicitly")
			return true
		}
		raw, e := s.Store.Get("settings", "solar_charge")
		if e != nil && !isMissing(e) {
			storageError(w, e)
			return true
		}
		by, _ := raw["by_device"].(map[string]any)
		if by == nil {
			by = map[string]any{}
			if len(raw) > 0 {
				by["__legacy__"] = raw
			}
		}
		old, e := s.solarConfig(sn)
		if e != nil {
			storageError(w, e)
			return true
		}
		for k, v := range b {
			old[k] = v
		}
		by[sn] = automation.NormalizeSolar(old)
		if e = s.Store.Put("settings", "solar_charge", "", map[string]any{"by_device": by}); e != nil {
			storageError(w, e)
			return true
		}
	}
	c, e := s.solarConfig(sn)
	if e != nil {
		storageError(w, e)
		return true
	}
	var serial any
	if sn != "" {
		serial = sn
	}
	if path == "/api/solar_charge/config" {
		jsonOut(w, 200, map[string]any{"device_sn": serial, "config": c})
		return true
	}
	rows, e := s.Store.List("automation_decisions", sn, 0, 0)
	if e != nil {
		storageError(w, e)
		return true
	}
	history := []map[string]any{}
	hours, limit := 24, 200
	if n, e := strconv.Atoi(r.URL.Query().Get("hours")); e == nil {
		hours = max(1, min(n, 87600))
	}
	if n, e := strconv.Atoi(r.URL.Query().Get("limit")); e == nil {
		limit = max(1, min(n, 1000))
	}
	for _, d := range rows {
		if sn != "" && (d["controller"] == "solar_charge" || d["_legacy_table"] == "solar_charge_decisions") && (path != "/api/solar_charge/history" || forecast.Number(d, "decided_at") >= float64(time.Now().Unix()-int64(hours)*3600)) {
			history = append(history, d)
		}
	}
	sort.Slice(history, func(i, j int) bool {
		return forecast.Number(history[i], "decided_at") > forecast.Number(history[j], "decided_at")
	})
	if path == "/api/solar_charge/history" {
		jsonOut(w, 200, map[string]any{"device_sn": serial, "decisions": history[:min(limit, len(history))]})
		return true
	}
	st, e := s.solarState(sn)
	if e != nil {
		storageError(w, e)
		return true
	}
	loc, _ := s.Store.Secret(s.Key, "location")
	offset := int64(forecast.Number(loc, "utc_offset_seconds"))
	now := time.Now().Unix()
	start := (now+offset)/86400*86400 - offset
	minutes, e := s.Store.EnergyMinutes(sn, float64(start))
	if e != nil {
		storageError(w, e)
		return true
	}
	wh := 0.0
	for _, m := range minutes {
		wh += forecast.Number(m, "solar_charge_diverted_wh")
	}
	var runtime any
	if sn != "" {
		runtime = st
	}
	jsonOut(w, 200, map[string]any{"device_sn": serial, "config": c, "runtime": runtime, "diverted_wh_today": wh, "recent_decisions": history[:min(20, len(history))]})
	return true
}
func (s *Server) solarOwnsHost(host string) bool {
	if host == "" {
		return false
	}
	raw, e := s.Store.Get("settings", "solar_charge")
	if e != nil {
		return false
	}
	by, ok := raw["by_device"].(map[string]any)
	if !ok {
		return raw["mode"] == "active" && str(raw, "kasa_device_host") == host
	}
	for _, v := range by {
		m, _ := v.(map[string]any)
		if m["mode"] == "active" && str(m, "kasa_device_host") == host {
			return true
		}
	}
	return false
}
