package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/automation"
)

type watchdogRuntime struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	pending map[string]int
}

func (s *Server) watchdogState(sn string) (automation.WatchdogState, error) {
	var state automation.WatchdogState
	raw, err := s.Store.Get("settings", "inverter_watchdog_state:"+sn)
	if isMissing(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	b, err := json.Marshal(raw)
	if err == nil {
		err = json.Unmarshal(b, &state)
	}
	return state, err
}
func (s *Server) saveWatchdog(sn string, state automation.WatchdogState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err = json.Unmarshal(b, &raw); err != nil {
		return err
	}
	return s.Store.Put("settings", "inverter_watchdog_state:"+sn, sn, raw)
}
func (s *Server) watchdogEnabled(sn string, model any) (bool, string, error) {
	raw, err := s.Store.Get("settings", "inverter_watchdog_config:"+sn)
	if err != nil && !isMissing(err) {
		return false, "", err
	}
	if enabled, ok := raw["enabled"].(bool); ok {
		return enabled, "user", nil
	}
	if strings.HasPrefix(sn, "siseli") || strings.HasPrefix(sn, "ecoflow") {
		return false, "default", nil
	}
	n, _ := model.(float64)
	return n == 13 || n == 22, "default", nil
}
func (s *Server) cancelWatchdog(sn string) {
	if cancel := s.watchdog.cancels[sn]; cancel != nil {
		cancel()
		delete(s.watchdog.cancels, sn)
	}
}

// Register the OFF intent before waiting on the bridge, including failed requests.
func (s *Server) watchdogUserOff(sn string) func() {
	s.watchdog.mu.Lock()
	defer s.watchdog.mu.Unlock()
	s.cancelWatchdog(sn)
	if s.watchdog.pending == nil {
		s.watchdog.pending = map[string]int{}
	}
	s.watchdog.pending[sn]++
	state, err := s.watchdogState(sn)
	if err == nil {
		state.LastUserOffTS = float64(time.Now().Unix())
		err = s.saveWatchdog(sn, state)
	}
	if err != nil {
		s.Store.App.Logger().Error("persist user OFF suppression", "error", err)
	}
	return func() { s.watchdog.mu.Lock(); defer s.watchdog.mu.Unlock(); s.watchdog.pending[sn]-- }
}
func (s *Server) watchdogTick(ctx context.Context, sn string, tele map[string]any, model any, stamp float64) {
	s.watchdog.mu.Lock()
	defer s.watchdog.mu.Unlock()
	enabled, _, err := s.watchdogEnabled(sn, model)
	if err != nil || !enabled {
		return
	}
	state, err := s.watchdogState(sn)
	if err != nil {
		return
	}
	if s.watchdog.pending[sn] > 0 {
		state.PendingOff = map[string]bool{"user": true}
	}
	cfg, _, err := s.settingValues()
	if err != nil && !isMissing(err) {
		return
	}
	floor := 0.0
	switch v := cfg["inverter_trip_recovery_min_w"].(type) {
	case int:
		floor = float64(v)
	case float64:
		floor = v
	}
	now := float64(time.Now().UnixNano()) / 1e9
	ac, _ := tele["ac_on"].(bool)
	action := state.Evaluate(ac, rescueNumber(tele["output_power_w"]), &stamp, floor, now, 10, 60)
	if err = s.saveWatchdog(sn, state); err != nil {
		return
	}
	if action != "cycle" || s.Bridge == nil {
		return
	}
	if s.watchdog.cancels == nil {
		s.watchdog.cancels = map[string]context.CancelFunc{}
	}
	s.cancelWatchdog(sn)
	cycleCtx, cancel := context.WithCancel(ctx)
	s.watchdog.cancels[sn] = cancel
	go func() {
		defer cancel()
		command := func(on bool) error {
			c, stop := context.WithTimeout(cycleCtx, 10*time.Second)
			defer stop()
			_, e := s.Bridge.Call(c, "set_output", map[string]any{"device_sn": sn, "port": "ac", "on": on})
			return e
		}
		if err := command(false); err != nil {
			return
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-cycleCtx.Done():
			return
		case <-timer.C:
		}
		// Cancellation and the final eligibility check serialize with user intent.
		s.watchdog.mu.Lock()
		defer s.watchdog.mu.Unlock()
		if cycleCtx.Err() != nil || s.watchdog.pending[sn] > 0 {
			return
		}
		current, e := s.watchdogState(sn)
		if e != nil || !current.HWTripActive || now-current.LastUserOffTS < 60 {
			return
		}
		enabled, _, e := s.watchdogEnabled(sn, model)
		if e != nil || !enabled {
			return
		}
		if err := command(true); err != nil {
			s.Store.App.Logger().Warn("watchdog AC recovery failed", "error", err)
		}
	}()
}
func (s *Server) watchdogRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	const base = "/api/inverter_watchdog/"
	if !strings.HasPrefix(r.URL.Path, base) {
		return false
	}
	sn := r.URL.Query().Get("device_sn")
	if str(b, "device_sn") != "" {
		sn = str(b, "device_sn")
	}
	s.mu.RLock()
	device, _ := s.snapshot["device"].(map[string]any)
	if sn == "" {
		sn = str(device, "device_sn")
	}
	s.mu.RUnlock()
	if sn == "" {
		if strings.HasSuffix(r.URL.Path, "status") {
			jsonOut(w, 200, map[string]any{"device_sn": nil, "state": nil})
		} else {
			fail(w, 400, "no active device — pass device_sn explicitly")
		}
		return true
	}
	if strings.HasPrefix(sn, "siseli:") || strings.HasPrefix(sn, "ecoflow:") {
		fail(w, 400, "AC auto recovery applies to Jackery devices only")
		return true
	}
	snap, err := s.Store.Get("devices", sn)
	if err != nil {
		fail(w, 404, "Jackery device not found")
		return true
	}
	d, _ := snap["device"].(map[string]any)
	if d == nil {
		d = device
	}
	s.watchdog.mu.Lock()
	defer s.watchdog.mu.Unlock()
	enabled, source, err := s.watchdogEnabled(sn, d["model_code"])
	if err != nil {
		storageError(w, err)
		return true
	}
	switch strings.TrimPrefix(r.URL.Path, base) {
	case "config":
		if r.Method == "PUT" {
			v, ok := b["enabled"].(bool)
			if !ok {
				fail(w, 400, "enabled must be true or false")
				return true
			}
			if err = s.Store.Put("settings", "inverter_watchdog_config:"+sn, sn, map[string]any{"enabled": v}); err != nil {
				storageError(w, err)
				return true
			}
			enabled, source = v, "user"
			if !v {
				s.cancelWatchdog(sn)
				err = s.saveWatchdog(sn, automation.WatchdogState{})
			}
		} else if r.Method != "GET" {
			fail(w, 405, "method not allowed")
			return true
		}
		if err != nil {
			storageError(w, err)
			return true
		}
		jsonOut(w, 200, map[string]any{"device_sn": sn, "model_code": d["model_code"], "enabled": enabled, "source": source})
	case "status":
		if r.Method != "GET" {
			fail(w, 405, "method not allowed")
			return true
		}
		var out any
		if enabled {
			state, e := s.watchdogState(sn)
			if e != nil {
				storageError(w, e)
				return true
			}
			out = map[string]any{"consecutive_attempts": state.ConsecutiveAttempts, "last_attempt_ts": state.LastAttemptTS, "last_user_off_ts": state.LastUserOffTS, "error_message": state.ErrorMessage, "hw_trip_active": state.HWTripActive, "episode_cycles": state.EpisodeCycles, "low_samples": state.LowSamples, "last_high_output_ts": state.LastHighOutputTS}
		}
		jsonOut(w, 200, map[string]any{"device_sn": sn, "state": out})
	case "dismiss":
		if r.Method != "POST" {
			fail(w, 405, "method not allowed")
			return true
		}
		state, e := s.watchdogState(sn)
		if e != nil {
			storageError(w, e)
			return true
		}
		s.cancelWatchdog(sn)
		state.Dismiss()
		if e = s.saveWatchdog(sn, state); e != nil {
			storageError(w, e)
		} else {
			jsonOut(w, 200, map[string]any{"ok": true, "device_sn": sn})
		}
	default:
		fail(w, 404, "not found")
	}
	return true
}
