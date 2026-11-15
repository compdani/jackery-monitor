package server

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/forecast"
)

type f7Runtime struct {
	mu      sync.Mutex
	running map[string]bool
	// Injected by fake tests; production waits with context cancellation.
	wait func(context.Context, time.Duration) error
}

func f7Config(raw map[string]any) map[string]any {
	out := map[string]any{"enabled": raw["enabled"] == true, "error_code": 8, "cooldown_min": 30, "pulse_s": 5, "day_start": nil, "day_end": nil, "last_cycle_ts": float64(0), "events": []any{}}
	for k, max := range map[string]int{"error_code": 999, "cooldown_min": 1440, "pulse_s": 60} {
		n := int(forecast.Number(raw, k))
		if n >= 1 && n <= max {
			out[k] = n
		}
	}
	valid := func(v any) string {
		s, _ := v.(string)
		parts := strings.Split(s, ":")
		if len(parts) != 2 {
			return ""
		}
		h, e := strconv.Atoi(parts[0])
		m, e2 := strconv.Atoi(parts[1])
		if e != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return ""
		}
		return fmt.Sprintf("%02d:%02d", h, m)
	}
	a, b := valid(raw["day_start"]), valid(raw["day_end"])
	if a != "" && b != "" {
		out["day_start"], out["day_end"] = a, b
	}
	if n := forecast.Number(raw, "last_cycle_ts"); !math.IsNaN(n) && !math.IsInf(n, 0) {
		out["last_cycle_ts"] = n
	}
	if events, ok := raw["events"].([]any); ok {
		clean := []any{}
		for _, v := range events[max(0, len(events)-50):] {
			if e, ok := v.(map[string]any); ok && str(e, "message") != "" {
				clean = append(clean, e)
			}
		}
		out["events"] = clean
	}
	return out
}
func (s *Server) f7Config(sn string) (map[string]any, error) {
	raw, err := s.Store.Get("settings", "f7_ac_reset:"+sn)
	if isMissing(err) {
		imported, e := s.Store.Get("settings", "f7_ac_reset")
		if e != nil && !isMissing(e) {
			return nil, e
		}
		by, _ := imported["by_device"].(map[string]any)
		raw, _ = by[sn].(map[string]any)
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return f7Config(raw), nil
}
func (s *Server) f7Save(sn string, cfg map[string]any) error {
	payload := make(map[string]any, len(cfg)+1)
	for k, v := range cfg {
		payload[k] = v
	}
	payload["_f7_device_sn"] = sn
	return s.Store.Put("settings", "f7_ac_reset:"+sn, sn, payload)
}
func (s *Server) f7Daylight(cfg map[string]any, now float64) bool {
	if start, ok := cfg["day_start"].(string); ok {
		end := cfg["day_end"].(string)
		loc, _ := s.Store.Secret(s.Key, "location")
		minute := int64(now+forecast.Number(loc, "utc_offset_seconds")) % 86400 / 60
		hhmm := func(s string) int64 { h, _ := strconv.Atoi(s[:2]); m, _ := strconv.Atoi(s[3:]); return int64(h*60 + m) }
		a, b := hhmm(start), hhmm(end)
		if a <= b {
			return minute >= a && minute < b
		}
		return minute >= a || minute < b
	}
	rows, err := s.Store.List("weather_forecasts", "", now-7200, 0)
	if err != nil {
		return false
	}
	hour := math.Floor(now/3600) * 3600
	distance := math.Inf(1)
	ghi := 0.0
	for _, row := range rows {
		d := math.Abs(forecast.Number(row, "ts") - hour)
		if d < distance {
			distance = d
			ghi = forecast.Number(row, "ghi_w_m2")
		}
	}
	return distance <= 3600 && ghi > 0
}
func f7Evaluate(cfg, tele map[string]any, now float64, day bool) string {
	if cfg["enabled"] != true {
		return "skip_disabled"
	}
	if tele["ac_on"] == true {
		return "skip_ac_on"
	}
	if int(forecast.Number(tele, "error_code")) != int(forecast.Number(cfg, "error_code")) {
		return "idle"
	}
	if !day {
		return "skip_night"
	}
	last := forecast.Number(cfg, "last_cycle_ts")
	if last != 0 && now-last < forecast.Number(cfg, "cooldown_min")*60 {
		return "skip_cooldown"
	}
	return "cycle"
}
func (s *Server) f7Event(sn, level, message string, code, pulse any) error {
	cfg, err := s.f7Config(sn)
	if err != nil {
		return err
	}
	events := cfg["events"].([]any)
	events = append(events, map[string]any{"ts": float64(time.Now().UnixNano()) / 1e9, "level": level, "category": "f7", "message": message, "extra": map[string]any{"device_sn": sn, "error_code": code, "pulse_s": pulse}})
	cfg["events"] = events[max(0, len(events)-50):]
	return s.f7Save(sn, cfg)
}
func (s *Server) f7Tick(ctx context.Context, sn string, tele map[string]any) {
	s.f7.mu.Lock()
	defer s.f7.mu.Unlock()
	if sn == "" || s.Bridge == nil || s.f7.running[sn] {
		return
	}
	cfg, err := s.f7Config(sn)
	if err != nil {
		return
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if f7Evaluate(cfg, tele, now, s.f7Daylight(cfg, now)) != "cycle" {
		return
	}
	cfg["last_cycle_ts"] = now
	if err = s.f7Save(sn, cfg); err != nil {
		return
	}
	code, pulse := cfg["error_code"], cfg["pulse_s"]
	if err = s.f7Event(sn, "warn", "F7 AC reset pulse started", code, pulse); err != nil {
		return
	}
	if s.f7.running == nil {
		s.f7.running = map[string]bool{}
	}
	s.f7.running[sn] = true
	go func() {
		command := func(c context.Context, on bool) error {
			bounded, cancel := context.WithTimeout(c, 10*time.Second)
			defer cancel()
			_, err := s.Bridge.Call(bounded, "set_output", map[string]any{"device_sn": sn, "port": "ac", "on": on})
			return err
		}
		err := command(ctx, true)
		if err == nil {
			duration := time.Duration(forecast.Number(cfg, "pulse_s")) * time.Second
			if s.f7.wait != nil {
				err = s.f7.wait(ctx, duration)
			} else {
				timer := time.NewTimer(duration)
				select {
				case <-ctx.Done():
					err = ctx.Err()
				case <-timer.C:
				}
				timer.Stop()
			}
			// Always attempt the trailing OFF after a successful ON, including shutdown.
			offErr := command(context.WithoutCancel(ctx), false)
			if offErr != nil {
				err = offErr
			}
		}
		s.f7.mu.Lock()
		defer s.f7.mu.Unlock()
		delete(s.f7.running, sn)
		level, message := "info", "F7 AC reset pulse finished"
		if err != nil {
			level = "error"
			message = "F7 AC reset pulse failed: " + err.Error()
		}
		if e := s.f7Event(sn, level, message, code, pulse); e != nil {
			s.Store.App.Logger().Error("persist F7 event", "error", e)
		}
	}()
}
func (s *Server) f7Routes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	if r.URL.Path != "/api/f7_ac_reset/config" {
		return false
	}
	if r.Method != "GET" && r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return true
	}
	sn := r.URL.Query().Get("device_sn")
	if sn == "" {
		sn = str(b, "device_sn")
	}
	if sn == "" {
		s.mu.RLock()
		d, _ := s.snapshot["device"].(map[string]any)
		sn = str(d, "device_sn")
		s.mu.RUnlock()
	}
	s.f7.mu.Lock()
	defer s.f7.mu.Unlock()
	cfg, err := s.f7Config(sn)
	if err != nil {
		storageError(w, err)
		return true
	}
	if r.Method == "POST" {
		if sn == "" {
			fail(w, 400, "no active device — pass device_sn explicitly")
			return true
		}
		for _, k := range []string{"enabled", "error_code", "cooldown_min", "pulse_s", "day_start", "day_end"} {
			if v, ok := b[k]; ok {
				cfg[k] = v
			}
		}
		cfg = f7Config(cfg)
		if err = s.f7Save(sn, cfg); err != nil {
			storageError(w, err)
			return true
		}
		jsonOut(w, 200, map[string]any{"device_sn": sn, "config": cfg})
	} else {
		var device any = sn
		if sn == "" {
			device = nil
		}
		jsonOut(w, 200, map[string]any{"device_sn": device, "config": cfg, "in_daylight": sn != "" && s.f7Daylight(cfg, float64(time.Now().Unix())), "events": cfg["events"]})
	}
	return true
}
func (s *Server) f7Events(since float64) ([]map[string]any, error) {
	rows, err := s.Store.List("settings", "", 0, 0)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	// Dedicated records supersede imported records for each device.
	configs := map[string]map[string]any{}
	imported, e := s.Store.Get("settings", "f7_ac_reset")
	if e != nil && !isMissing(e) {
		return nil, e
	}
	by, _ := imported["by_device"].(map[string]any)
	for sn, v := range by {
		if cfg, ok := v.(map[string]any); ok {
			configs[sn] = cfg
		}
	}
	for _, cfg := range rows {
		if sn, ok := cfg["_f7_device_sn"].(string); ok {
			configs[sn] = cfg
		}
	}
	for _, cfg := range configs {
		for _, v := range f7Config(cfg)["events"].([]any) {
			e := v.(map[string]any)
			if forecast.Number(e, "ts") > since {
				out = append(out, e)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return forecast.Number(out[i], "ts") < forecast.Number(out[j], "ts") })
	return out, nil
}
