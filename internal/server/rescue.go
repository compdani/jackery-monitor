package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase/core"
)

type rescueDevice struct {
	automation.RescueState
	LastError     *string `json:"last_error"`
	LastAttemptTS float64 `json:"last_attempt_ts"`
	Host          string  `json:"kasa_host"`
}
type rescueRuntime struct {
	mu               sync.Mutex
	loaded           bool
	states           map[string]*rescueDevice
	persistenceError string
	pendingAudits    []map[string]any
}
type rescueObservation struct{ ts, main *float64 }

func rescueNumber(v any) *float64 {
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return &n
}
func (s *Server) loadRescue() error {
	if s.rescue.loaded {
		return nil
	}
	value, err := s.Store.Get("settings", "rescue_state")
	if err != nil && !isMissing(err) {
		return err
	}
	states := map[string]*rescueDevice{}
	if err == nil {
		data, err := json.Marshal(value["by_device"])
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &states); err != nil {
			return err
		}
	}
	if states == nil {
		states = map[string]*rescueDevice{}
	}
	s.rescue.states = states
	s.rescue.loaded = true
	return nil
}
func (s *Server) rescueEnabled() (bool, error) {
	cfg, err := s.Store.Get("settings", "rescue_config")
	if isMissing(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if v, ok := cfg["enabled"].(bool); ok {
		return v, nil
	}
	return true, nil
}
func (s *Server) rescueHosts() (map[string]string, error) {
	rows, err := s.Store.App.FindRecordsByFilter("automation_rules", "", "ts,key", 0, 0)
	if err != nil {
		return nil, err
	}
	// Empty scope matches all devices, just as Python's enabled ON rule lookup.
	hosts := map[string]string{}
	for _, record := range rows {
		row, err := s.Store.Get("automation_rules", record.GetString("key"))
		if err != nil {
			return nil, err
		}
		if enabled, ok := row["enabled"].(bool); ok && !enabled {
			continue
		}
		if str(row, "action") != "on" || str(row, "kasa_host") == "" {
			continue
		}
		scope := str(row, "jackery_device_sn")
		for sn := range s.rescue.states {
			if hosts[sn] == "" && (scope == "" || scope == sn) {
				hosts[sn] = str(row, "kasa_host")
			}
		}
	}
	return hosts, nil
}

// rescueTick is also called without observations by the independent watchdog
// and after failed polls. It NEVER replaces a source timestamp with poll time.
func (s *Server) rescueTick(ctx context.Context, now float64, p map[string]any) {
	s.actions.plugs.Lock()
	defer s.actions.plugs.Unlock()
	s.rescue.mu.Lock()
	defer s.rescue.mu.Unlock()
	if err := s.loadRescue(); err != nil {
		s.rescue.persistenceError = err.Error()
		return
	}
	enabled, err := s.rescueEnabled()
	if err != nil {
		s.rescue.persistenceError = err.Error()
		return
	}
	observations := map[string]rescueObservation{}
	add := func(sn string, ts any, tele map[string]any) {
		stamp := rescueNumber(ts)
		if sn == "" || stamp == nil || *stamp <= 0 || *stamp > now+5 {
			return
		}
		main := rescueNumber(tele["battery_percent"])
		if main != nil && (*main < 0 || *main > 100) {
			main = nil
		}
		observations[sn] = rescueObservation{stamp, main}
		if s.rescue.states[sn] == nil {
			s.rescue.states[sn] = &rescueDevice{}
		}
	}
	if cloud, ok := p["cloud"].(map[string]any); ok {
		if fleet, ok := cloud["devices_telemetry"].(map[string]any); ok {
			for sn, raw := range fleet {
				entry, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				tele, _ := entry["telemetry"].(map[string]any)
				add(sn, entry["ts"], tele)
			}
		}
	}
	if device, ok := p["device"].(map[string]any); ok {
		sn := str(device, "device_sn")
		if _, exists := observations[sn]; !exists {
			tele, _ := p["telemetry"].(map[string]any)
			add(sn, p["last_update_ts"], tele)
		}
	}
	hosts, err := s.rescueHosts()
	if err != nil {
		s.rescue.persistenceError = err.Error()
		return
	}
	audits := s.rescue.pendingAudits
	for sn, st := range s.rescue.states {
		if st == nil {
			continue
		}
		observation, ok := observations[sn]
		if !ok && st.LastFreshTS > 0 {
			stamp := st.LastFreshTS
			observation.ts = &stamp
		}
		reason := st.Decide(now, observation.ts, observation.main)
		st.Host = hosts[sn]
		if !enabled || reason == "" {
			continue
		}
		if st.Host == "" {
			message := "no enabled grid-ON rule provides a plug host"
			st.LastError = &message
			continue
		}
		actuator := s.RescueSwitch
		if actuator == nil {
			s.initControllers()
			actuator = s.controllers.engine.Switch
		}
		st.LastAttemptTS = now
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := actuator.SetPower(callCtx, st.Host, true)
		cancel()
		if err != nil {
			message := err.Error()
			st.LastError = &message
			continue
		}
		st.MarkFired(reason, now)
		st.LastError = nil
		audits = append(audits, map[string]any{"rule_id": "rescue-watchdog", "rule_name": "Rescue watchdog (" + reason + ")", "reason": reason, "action": "on", "kasa_host": st.Host, "jackery_sn": sn, "device_sn": sn, "soc_at_fire": st.LastFreshMain, "operator": "<=", "threshold": automation.RescueArmSOC, "fired": true, "ts": now})
	}
	// Keep the in-memory latch even if persistence fails after successful ON.
	// Repeating ON after a crash is idempotent; never pretend a failed toggle fired.
	s.rescue.pendingAudits = audits
	err = s.Store.App.RunInTransaction(func(app core.App) error {
		st := &store.Store{App: app}
		for _, audit := range audits {
			if err := st.Put("automation_decisions", store.ID(), str(audit, "device_sn"), audit); err != nil {
				return err
			}
		}
		return st.Put("settings", "rescue_state", "", map[string]any{"by_device": s.rescue.states})
	})
	if err != nil {
		s.rescue.persistenceError = err.Error()
		s.Store.App.Logger().Error("persist rescue state/audit", "error", err)
	} else {
		s.rescue.persistenceError = ""
		s.rescue.pendingAudits = nil
	}
}

// RescueLoop is independent of cloud polling, including its timeouts/backoff.
func (s *Server) RescueLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.rescueTick(ctx, float64(time.Now().UnixNano())/1e9, nil)
		}
	}
}
func (s *Server) rescueRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	path := r.URL.Path
	if path != "/api/rescue/config" && path != "/api/rescue/status" {
		return false
	}
	if r.Method != "GET" && (r.Method != "POST" || path != "/api/rescue/config") {
		fail(w, 405, "method not allowed")
		return true
	}
	s.rescue.mu.Lock()
	defer s.rescue.mu.Unlock()
	if r.Method == "POST" {
		enabled, ok := b["enabled"].(bool)
		if !ok || len(b) != 1 {
			fail(w, 400, "only boolean enabled may be changed; thresholds and rule-based plug selection are fixed")
			return true
		}
		if err := s.Store.Put("settings", "rescue_config", "", map[string]any{"enabled": enabled}); err != nil {
			storageError(w, err)
			return true
		}
	}
	enabled, err := s.rescueEnabled()
	if err != nil {
		storageError(w, err)
		return true
	}
	out := map[string]any{"enabled": enabled, "arm_main_soc_pct": automation.RescueArmSOC, "stale_fire_s": automation.RescueStaleSeconds, "clear_soc_pct": automation.RescueClearSOC, "plug_selection": "enabled_grid_on_rule"}
	if path == "/api/rescue/status" {
		if err := s.loadRescue(); err != nil {
			storageError(w, err)
			return true
		}
		out["by_device"] = s.rescue.states
		out["persistence_error"] = nil
		if s.rescue.persistenceError != "" {
			out["persistence_error"] = s.rescue.persistenceError
		}
	}
	jsonOut(w, 200, out)
	return true
}
