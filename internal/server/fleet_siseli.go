package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/ble"
	"github.com/compdani/jackery-monitor/internal/ecoflow"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/siseli"
)

// siseliRuntime mirrors server.py's state.siseli (cloud/portal only; local
// packet capture is retired on the Pi).
type siseliRuntime struct {
	mu        sync.Mutex
	state     string
	err       any
	devices   []map[string]any
	telemetry map[string]map[string]any
	settings  map[string][]siseli.Values
	raw       map[string]siseli.Values
	lastPoll  any
	wake      chan struct{}
	wakeOnce  sync.Once
}

func (r *siseliRuntime) reset() {
	r.state, r.err, r.devices, r.lastPoll = "idle", nil, []map[string]any{}, nil
	r.telemetry, r.settings, r.raw = map[string]map[string]any{}, map[string][]siseli.Values{}, map[string]siseli.Values{}
}
func (s *Server) siseliWake() chan struct{} {
	s.fleet.siseli.wakeOnce.Do(func() { s.fleet.siseli.wake = make(chan struct{}, 1) })
	return s.fleet.siseli.wake
}
func kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
func (s *Server) siseliCreds() (*siseli.Credentials, error) {
	raw, err := s.Store.Secret(s.Key, "siseli")
	if isMissing(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c siseli.Credentials
	if err = fromMap(raw, &c); err != nil {
		return nil, err
	}
	c = c.Normalize()
	if !c.Valid() {
		return nil, nil
	}
	return &c, nil
}
func (s *Server) saveSiseliCreds(c siseli.Credentials) error {
	return s.Store.SaveSecret(s.Key, "siseli", toSolarMap(c.Normalize()))
}
func (s *Server) siseliTimezone(c *siseli.Credentials) string {
	if loc, _, _ := s.savedLocation(); str(loc, "timezone") != "" {
		return str(loc, "timezone")
	}
	if c != nil && c.TimeZone != "" {
		return c.TimeZone
	}
	return "UTC"
}
func (s *Server) siseliClient(c siseli.Credentials) (*siseli.Client, error) {
	c.TimeZone = s.siseliTimezone(&c)
	return siseli.NewClient(siseli.ClientOptions{Credentials: c, HTTP: s.SiseliHTTP, BaseURL: s.SiseliBaseURL, OnTokenRefreshed: func(fresh siseli.Credentials) {
		saved, err := s.siseliCreds()
		if err != nil || saved == nil {
			return
		}
		_ = s.saveSiseliCreds(saved.WithTokens(fresh.AccessToken, fresh.RefreshToken, fresh.AccessTokenExpires, fresh.RefreshTokenExpires))
	}})
}

// siseliOverlay ports _apply_siseli_overlays: BMS then linked EcoFlow.
func (s *Server) siseliOverlay(sn string, tele map[string]any) map[string]any {
	if tele == nil || sn == "" {
		return tele
	}
	pref, _ := s.Store.Get("device_prefs", sn)
	override := 0
	if cached, err := s.Store.Get("devices", sn); err == nil {
		override = int(forecast.Number(cached, "capacity_wh_override"))
	}
	reg := s.bmsRegistry()
	out := ble.OverlayTelemetry(tele, ble.OverlayOpts{Packs: reg.ListPacksFor(sn), Live: s.bmsLive(), UseAsMain: reg.UseAsMain(sn), CapacityOverrideWh: override, IgnoreInverterSOC: pref["ignore_inverter_soc"] == true, CalcGrid: pref["calc_grid"] == true})
	linked := s.ecoflowRegistry().ListForSiseli(sn)
	if len(linked) == 0 {
		return out
	}
	var sources []string
	if raw, ok := pref["load_sources"]; ok {
		sources = []string{}
		list, _ := raw.([]any)
		for _, v := range list {
			if id, ok := v.(string); ok {
				sources = append(sources, id)
			}
		}
	}
	return ecoflow.OverlayTelemetry(out, ecoflow.OverlayOpts{Linked: ecoflow.LinkedFromDevices(linked), LiveBySN: s.ecoflowLive(), BMSAlready: out["bms_source"] != nil, LoadSources: sources})
}

// recordFleet persists one fleet observation through the shared device,
// sample, controller and rule path.
func (s *Server) recordFleet(ctx context.Context, device map[string]any, tele map[string]any, ts float64) {
	s.persistObservation(ctx, map[string]any{"device": device, "telemetry": tele, "last_update_ts": ts})
}
func (s *Server) siseliDevice(sn string) map[string]any {
	s.fleet.siseli.mu.Lock()
	defer s.fleet.siseli.mu.Unlock()
	for _, d := range s.fleet.siseli.devices {
		if str(d, "device_sn") == sn {
			return d
		}
	}
	return map[string]any{"device_sn": sn, "device_id": sn, "source": "siseli"}
}
func (s *Server) recordSiseli(ctx context.Context, sn string, tele map[string]any, ts float64) {
	dev := map[string]any{}
	for k, v := range s.siseliDevice(sn) {
		dev[k] = v
	}
	dev["device_sn"], dev["source"] = sn, "siseli"
	s.recordFleet(ctx, dev, s.siseliOverlay(sn, tele), ts)
}

// siseliPollOnce ports siseli_poll_once for the cloud portal.
func (s *Server) siseliPollOnce(ctx context.Context) error {
	rt := &s.fleet.siseli
	creds, err := s.siseliCreds()
	if err != nil {
		return err
	}
	if creds == nil {
		rt.mu.Lock()
		rt.reset()
		rt.mu.Unlock()
		return nil
	}
	rt.mu.Lock()
	rt.state = "connecting"
	rt.mu.Unlock()
	fail := func(e error) error {
		rt.mu.Lock()
		rt.state, rt.err = "error", e.Error()
		rt.mu.Unlock()
		return e
	}
	client, err := s.siseliClient(*creds)
	if err != nil {
		return fail(err)
	}
	if creds.AccessToken == "" {
		if err = client.Login(ctx); err != nil {
			return fail(err)
		}
	}
	raw, err := client.ListDevices(ctx, creds.StationID, 0)
	if err != nil {
		return fail(err)
	}
	devices := []map[string]any{}
	for _, r := range raw {
		n := siseli.NormalizeDevice(r)
		if n == nil {
			continue
		}
		if w := creds.DeviceID; w != "" && w != str(n, "portal_device_id") && w != str(n, "device_id") && w != str(n, "device_sn") {
			continue
		}
		devices = append(devices, map[string]any(n))
	}
	rt.mu.Lock()
	rt.devices = devices
	previous := rt.telemetry
	rt.mu.Unlock()
	ts := float64(time.Now().UnixNano()) / 1e9
	telemetry := map[string]map[string]any{}
	settings, rawSettings := map[string][]siseli.Values{}, map[string]siseli.Values{}
	for _, d := range devices {
		pid, sn := str(d, "portal_device_id"), str(d, "device_sn")
		// Reconcile reported production independently of the live power sample.
		for _, date := range client.ProductionDates() {
			rows, err := client.FetchDailyProduction(ctx, pid, date)
			if err != nil {
				s.Store.App.Logger().Warn("siseli production", "device", pid, "error", err)
				continue
			}
			for _, row := range rows {
				key := fmt.Sprintf("siseli-production:%s:%.0f", sn, row["ts"])
				if err := s.Store.Put("samples", key, sn, row); err != nil {
					s.Store.App.Logger().Error("persist siseli production", "error", err)
				}
			}
		}
		if v, e := client.GetDeviceSettings(ctx, pid); e == nil && v != nil {
			rawSettings[sn], settings[sn] = v, siseli.NormalizeControls(v)
		}
		values, e := client.FetchLatestData(ctx, pid)
		if e != nil {
			s.Store.App.Logger().Warn("siseli fetch", "device", pid, "error", e)
			if prev := previous[sn]; prev != nil {
				telemetry[sn] = prev
			}
			continue
		}
		tele := map[string]any(siseli.ToTelemetry(values))
		telemetry[sn] = map[string]any{"telemetry": tele, "ts": ts, "origin": "http"}
		rt.mu.Lock()
		rt.telemetry = mergeEntries(rt.telemetry, sn, telemetry[sn])
		rt.mu.Unlock()
		s.recordSiseli(ctx, sn, tele, ts)
	}
	rt.mu.Lock()
	rt.state, rt.err, rt.devices, rt.telemetry, rt.settings, rt.raw, rt.lastPoll = "connected", nil, devices, telemetry, settings, rawSettings, ts
	rt.mu.Unlock()
	return nil
}
func mergeEntries(m map[string]map[string]any, k string, v map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for a, b := range m {
		out[a] = b
	}
	out[k] = v
	return out
}

// SiseliLoop polls the portal every max(30, siseli_poll_interval_s) seconds
// or immediately after credential changes.
func (s *Server) SiseliLoop(ctx context.Context) {
	wake := s.siseliWake()
	for {
		if err := s.siseliPollOnce(ctx); err != nil {
			s.Store.App.Logger().Warn("siseli poll", "error", err)
		}
		wait := 300.0
		if values, _, err := s.settingValues(); err == nil && forecast.Number(values, "siseli_poll_interval_s") > 0 {
			wait = forecast.Number(values, "siseli_poll_interval_s")
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-time.After(time.Duration(max(30, wait)) * time.Second):
		}
	}
}

const siseliLocalRetired = "Siseli local packet capture is retired on the Raspberry Pi deployment; the Siseli cloud portal remains supported"

func (s *Server) siseliRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	p, m := r.URL.Path, r.Method
	if !strings.HasPrefix(p, "/api/siseli/") {
		return false
	}
	rt := &s.fleet.siseli
	switch {
	case p == "/api/siseli/credentials" && m == "GET":
		c, err := s.siseliCreds()
		if err != nil {
			storageError(w, err)
			return true
		}
		var pub siseli.Credentials
		if c != nil {
			pub = *c
		}
		rt.mu.Lock()
		devices := rt.devices
		if devices == nil {
			devices = []map[string]any{}
		}
		out := map[string]any{"has_credentials": c != nil, "user_id": nilEmpty(pub.UserID), "station_id": nilEmpty(pub.StationID), "device_id": nilEmpty(pub.DeviceID), "time_zone": nilEmpty(pub.TimeZone), "local_read": false, "hybrid_pull": false, "inverter_ip": "", "router_ip": "", "sniff_iface": "", "inverter_mac": "", "router_mac": "", "mqtt_broker_ip": "", "mqtt_streams": []any{}, "local_running": false, "local_error": nil, "local_last_decode_ts": nil, "local_readings": nil, "local_retired": true, "state": rt.state, "error": rt.err, "last_poll_ts": rt.lastPoll, "devices": devices}
		rt.mu.Unlock()
		jsonOut(w, 200, out)
	case p == "/api/siseli/credentials" && m == "POST":
		existing, err := s.siseliCreds()
		if err != nil {
			storageError(w, err)
			return true
		}
		c := siseli.Credentials{UserID: str(b, "user_id"), Password: str(b, "password"), StationID: str(b, "station_id"), DeviceID: str(b, "device_id"), TimeZone: str(b, "time_zone")}.Normalize()
		if c.Password == "" && existing != nil {
			c.Password = existing.Password
		}
		if !c.Valid() {
			fail(w, 400, "user_id, password, and station_id are required")
			return true
		}
		probe, err := s.siseliClient(c)
		if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		var devices []siseli.Values
		if err = probe.Login(ctx); err == nil {
			devices, err = probe.ListDevices(ctx, c.StationID, 0)
		}
		var portal *siseli.PortalError
		switch {
		case errors.Is(err, siseli.ErrAuthentication) || errors.Is(err, siseli.ErrTokenExpired):
			fail(w, 400, err.Error())
			return true
		case errors.As(err, &portal):
			detail := "Siseli rejected that Station ID. Use the numeric stationId from the solar.siseli.com Network tab, not the inverter serial."
			if portal.Message != "" {
				detail += " Portal said: " + portal.Message
			}
			fail(w, 400, detail)
			return true
		case err != nil:
			fail(w, 502, "Could not reach solar.siseli.com")
			return true
		}
		fresh := probe.Credentials()
		c = c.WithTokens(fresh.AccessToken, fresh.RefreshToken, fresh.AccessTokenExpires, fresh.RefreshTokenExpires)
		if err = s.saveSiseliCreds(c); err != nil {
			fail(w, 500, "failed to save credentials")
			return true
		}
		kick(s.siseliWake())
		names := []any{}
		for _, d := range devices {
			if n := siseli.NormalizeDevice(d); n != nil {
				names = append(names, n["name"])
			}
		}
		jsonOut(w, 200, map[string]any{"ok": true, "user_id": c.UserID, "station_id": c.StationID, "device_count": len(names), "devices": names})
	case p == "/api/siseli/credentials" && m == "DELETE":
		if err := s.Store.Delete("credentials", "siseli"); err != nil && !isMissing(err) {
			storageError(w, err)
			return true
		}
		rt.mu.Lock()
		rt.reset()
		rt.mu.Unlock()
		kick(s.siseliWake())
		jsonOut(w, 200, map[string]any{"ok": true})
	case p == "/api/siseli/status" && m == "GET":
		c, _ := s.siseliCreds()
		rt.mu.Lock()
		devices := rt.devices
		if devices == nil {
			devices = []map[string]any{}
		}
		jsonOut(w, 200, map[string]any{"state": rt.state, "error": rt.err, "last_poll_ts": rt.lastPoll, "devices": devices, "has_credentials": c != nil})
		rt.mu.Unlock()
	case p == "/api/siseli/settings" && m == "GET":
		id := r.URL.Query().Get("device_id")
		if id == "" {
			fail(w, 400, "device_id required")
			return true
		}
		sn, _ := siseli.MakeSN(siseli.PortalIDFromSN(id))
		rt.mu.Lock()
		controls, ok := rt.settings[sn]
		rt.mu.Unlock()
		if !ok {
			fail(w, 404, "no settings cached for this device")
			return true
		}
		jsonOut(w, 200, map[string]any{"device_id": sn, "controls": controls})
	case p == "/api/siseli/settings" && m == "POST":
		id, key := strings.TrimSpace(str(b, "device_id")), strings.TrimSpace(str(b, "key"))
		if id == "" || key == "" {
			fail(w, 400, "device_id and key are required")
			return true
		}
		value, ok := b["value"]
		if !ok {
			fail(w, 400, "value is required")
			return true
		}
		sn, _ := siseli.MakeSN(siseli.PortalIDFromSN(id))
		rt.mu.Lock()
		raw := rt.raw[sn]
		rt.mu.Unlock()
		if _, cached := raw[key]; !siseli.IsKnownControl(key) && !cached {
			fail(w, 400, "unknown control key: "+key)
			return true
		}
		c, err := s.siseliCreds()
		if err != nil || c == nil {
			fail(w, 400, "Siseli credentials not configured")
			return true
		}
		client, err := s.siseliClient(*c)
		if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		pid := siseli.PortalIDFromSN(sn)
		if err = client.SetDeviceSetting(r.Context(), pid, key, value, raw); err == nil {
			var fresh siseli.Values
			if fresh, err = client.GetDeviceSettings(r.Context(), pid); err == nil && fresh != nil {
				rt.mu.Lock()
				rt.raw[sn], rt.settings[sn] = fresh, siseli.NormalizeControls(fresh)
				rt.mu.Unlock()
			}
		}
		if errors.Is(err, siseli.ErrAuthentication) {
			fail(w, 401, err.Error())
			return true
		} else if err != nil {
			fail(w, 502, err.Error())
			return true
		}
		rt.mu.Lock()
		controls := rt.settings[sn]
		rt.mu.Unlock()
		if controls == nil {
			controls = []siseli.Values{}
		}
		jsonOut(w, 200, map[string]any{"ok": true, "device_id": sn, "controls": controls})
	case p == "/api/siseli/mqtt-capture" && m == "GET":
		jsonOut(w, 200, map[string]any{"enabled": false, "captures": []any{}, "retired": true})
	case p == "/api/siseli/local" || p == "/api/siseli/local/test" || p == "/api/siseli/mqtt-capture":
		fail(w, 410, siseliLocalRetired)
	default:
		return false
	}
	return true
}
func nilEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// mergeFleetStatus ports _merged_cloud_meta: Siseli and unattached EcoFlow
// devices join the Jackery fleet list with their (overlaid) live telemetry.
func (s *Server) mergeFleetStatus(out map[string]any) {
	rt := &s.fleet.siseli
	rt.mu.Lock()
	devices := append([]map[string]any(nil), rt.devices...)
	entries := map[string]map[string]any{}
	for k, v := range rt.telemetry {
		entries[k] = v
	}
	rt.mu.Unlock()
	eco := s.ecoflowRegistry().FleetMeta()
	live := s.ecoflowLive()
	if len(devices) == 0 && len(eco) == 0 && len(live) == 0 {
		return
	}
	cloud := map[string]any{}
	if old, ok := out["cloud"].(map[string]any); ok {
		for k, v := range old {
			cloud[k] = v
		}
	}
	list := []any{}
	switch v := cloud["devices"].(type) {
	case []any:
		list = append(list, v...)
	case []map[string]any:
		for _, d := range v {
			list = append(list, d)
		}
	}
	tele := map[string]any{}
	if old, ok := cloud["devices_telemetry"].(map[string]any); ok {
		for k, v := range old {
			tele[k] = v
		}
	}
	for _, d := range devices {
		list = append(list, d)
	}
	for _, d := range eco {
		list = append(list, toSolarMap(d))
	}
	for sn, e := range entries {
		t, _ := e["telemetry"].(map[string]any)
		tele[sn] = map[string]any{"telemetry": s.siseliOverlay(sn, t), "ts": e["ts"], "origin": e["origin"]}
	}
	for sn, l := range live {
		tele[sn] = map[string]any{"telemetry": l.Telemetry, "detail": l.Detail, "ts": l.TS}
	}
	cloud["devices"], cloud["devices_telemetry"] = list, tele
	out["cloud"] = cloud
}
