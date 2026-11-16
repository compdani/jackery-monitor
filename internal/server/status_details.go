package server

import (
	"encoding/json"
	"github.com/compdani/jackery-monitor/internal/siseli"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

type liveHistoryCache struct {
	mu   sync.Mutex
	rows map[string]liveHistoryEntry
}
type liveHistoryEntry struct {
	at   time.Time
	rows []map[string]any
}

func (s *Server) liveHistory(sn string) []map[string]any {
	c := &s.liveHistories
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rows == nil {
		c.rows = map[string]liveHistoryEntry{}
	}
	old := c.rows[sn]
	if time.Since(old.at) < 30*time.Second {
		return old.rows
	}
	minutes, err := s.Store.EnergyMinutes(sn, float64(time.Now().Unix()-6*3600))
	if err != nil {
		s.Store.App.Logger().Error("live history", "device", sn, "error", err)
		old.at = time.Now().Add(-25 * time.Second)
		c.rows[sn] = old
		return old.rows
	}
	rows := []map[string]any{}
	for _, m := range minutes {
		if m["last_input_w"] == nil && m["last_output_w"] == nil {
			continue
		}
		rows = append(rows, map[string]any{"ts": m["ts"], "input_power_w": m["last_input_w"], "output_power_w": m["last_output_w"], "battery_percent": m["last_battery_pct"]})
	}
	c.rows[sn] = liveHistoryEntry{time.Now(), rows}
	return rows
}

// Status never starts a lifetime reconstruction synchronously. It shares the
// same coalesced response cache as REST; EnergyLoop warms stats in the background.
func (s *Server) statusEnergy(sn string) map[string]any {
	q := url.Values{"device_sn": {sn}}
	path := "/api/energy/totals?" + q.Encode()
	key := path + "|" + sn
	c := &s.energyReads
	c.mu.Lock()
	entry := c.entries[key]
	body := c.lastTotals[sn]
	if entry != nil && len(entry.body) > 0 {
		body = entry.body
	}
	c.mu.Unlock()

	var out map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &out)
	}
	return out
}

func (s *Server) statusDetails(out map[string]any, sn string) {
	device, _ := out["device"].(map[string]any)
	prefs := map[string]any{}
	for _, id := range []string{str(device, "device_id"), sn} {
		if id == "" {
			continue
		}
		if v, err := s.Store.Get("device_prefs", id); err == nil {
			prefs = v
			break
		}
	}
	controls := []siseli.Values{}
	rt := &s.fleet.siseli
	rt.mu.Lock()
	controls = append(controls, rt.settings[sn]...)
	out["siseli"] = map[string]any{"state": rt.state, "error": rt.err, "last_poll_ts": rt.lastPoll}
	rt.mu.Unlock()
	if _, ok := prefs["live_controls"]; !ok {
		copy := map[string]any{}
		for k, v := range prefs {
			copy[k] = v
		}
		pins := []string{}
		for _, c := range controls {
			pins = append(pins, str(c, "canonical"))
		}
		copy["live_controls"] = pins
		prefs = copy
	}
	out["device_prefs"], out["siseli_controls"] = prefs, controls
	eco := &s.fleet.ecoflow
	eco.mu.Lock()
	out["ecoflow"] = map[string]any{"state": eco.state, "error": eco.err, "last_poll_ts": eco.lastPoll}
	eco.mu.Unlock()
	if state, err := s.watchdogState(sn); err == nil {
		out["inverter_watchdog"] = map[string]any{"consecutive_attempts": state.ConsecutiveAttempts, "error_message": state.ErrorMessage, "last_attempt_ts": state.LastAttemptTS, "hw_trip_active": state.HWTripActive}
	}
	out["history"] = s.liveHistory(sn)
	out["energy"] = s.statusEnergy(sn)
}

// Resolve selection without constructing history, overlays or stats.
func (s *Server) selectedSerial(r *http.Request) string {
	id := r.URL.Query().Get("view_device_id")
	if id == "" {
		if c, err := r.Cookie("view_device_id"); err == nil {
			id = c.Value
		}
	}
	s.mu.RLock()
	d, _ := s.snapshot["device"].(map[string]any)
	cloud, _ := s.snapshot["cloud"].(map[string]any)
	active := str(d, "device_sn")
	if id == "" {
		id = str(cloud, "selected_device_id")
	}
	s.mu.RUnlock()
	if id == "" && active != "" {
		return active
	}
	if id != "" {
		if row, err := s.Store.Get("devices", id); err == nil {
			d, _ := row["device"].(map[string]any)
			return str(d, "device_sn")
		}
	}
	rows, err := s.Store.List("devices", "", 0, 1000)
	if err != nil {
		return active
	}
	serials := []string{}
	for _, row := range rows {
		d, _ := row["device"].(map[string]any)
		if id != "" && (id == str(d, "device_id") || id == str(d, "device_sn")) {
			return str(d, "device_sn")
		}
		if sn := str(d, "device_sn"); sn != "" {
			serials = append(serials, sn)
		}
	}
	if active != "" {
		return active
	}
	sort.Strings(serials)
	if len(serials) > 0 {
		return serials[0]
	}
	return ""
}
