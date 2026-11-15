// Package server preserves the browser/mobile HTTP seam while PocketBase owns
// auth and persistence. Unported operations fail explicitly with HTTP 501.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/ble"
	"github.com/compdani/jackery-monitor/internal/bridge"
	"github.com/compdani/jackery-monitor/internal/ecoflow"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/kasa"
	"github.com/compdani/jackery-monitor/internal/siseli"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase/core"
)

type Server struct {
	actions       actionArbiter
	watchdog      watchdogRuntime
	f7            f7Runtime
	SmartPlug     SmartPlug
	SmartForecast SmartForecastSource
	RescueSwitch  automation.Switch
	rescue        rescueRuntime
	solarStarted  map[string]bool
	solar         smartRuntime
	smart         smartRuntime
	AIHTTP        *http.Client
	Store         *store.Store
	Bridge        bridge.Client
	PublicDir     string
	Key           []byte
	assistant     assistantRuntime
	controllers   controllerRuntime
	setupMu       sync.Mutex
	mu            sync.RWMutex
	snapshot      map[string]any
	tesla         teslaRuntime
	// Weather and SolarForecast are injectable for fixture HTTP tests.
	Weather       *forecast.WeatherClient
	SolarForecast *forecast.SolarClient
	forecasts     forecastRuntime
	// KasaBackend replaces the native LAN transport in tests.
	KasaBackend kasa.Backend
	fleet       fleetRuntime
	// Fleet transports are injectable so tests never reach real accounts or radios.
	SiseliHTTP    siseli.HTTPDoer
	SiseliBaseURL string
	EcoFlowHTTP   ecoflow.HTTPDoer
	EcoFlowMQTT   ecoflow.MQTTTransport
	BLEAdapter    ble.Adapter
	// AnthropicBaseURL redirects Anthropic calls to a fixture in tests.
	AnthropicBaseURL string
	advisor          advisorRuntime
}

func (s *Server) Register(e *core.ServeEvent) {
	// Intercept legacy API paths before PocketBase's collection-specific
	// middleware. /api/settings is shared with PocketBase and must not be
	// registered twice in Go's ServeMux.
	e.Router.BindFunc(func(ev *core.RequestEvent) error {
		p := ev.Request.URL.Path
		native := false
		for _, prefix := range []string{"/api/collections/", "/api/files/", "/api/realtime", "/api/batch", "/api/health", "/api/backups", "/api/logs", "/api/crons"} {
			if strings.HasPrefix(p, prefix) {
				native = true
				break
			}
		}
		if strings.HasPrefix(p, "/api/") && !native {
			s.ServeHTTP(ev.Response, ev.Request)
			return nil
		}
		return ev.Next()
	})
	e.Router.GET("/ws", func(ev *core.RequestEvent) error { s.ServeHTTP(ev.Response, ev.Request); return nil })
	e.Router.GET("/{path...}", func(ev *core.RequestEvent) error { s.ServeHTTP(ev.Response, ev.Request); return nil })
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, detail string) {
	jsonOut(w, status, map[string]any{"detail": detail})
}
func token(r *http.Request) string {
	if v := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); v != "" && v != r.Header.Get("Authorization") {
		return v
	}
	if v := r.URL.Query().Get("token"); v != "" {
		return v
	}
	if c, e := r.Cookie("jackery_session"); e == nil {
		return c.Value
	}
	return ""
}
func (s *Server) user(r *http.Request) *core.Record {
	u, e := s.Store.App.FindAuthRecordByToken(token(r), core.TokenTypeAuth)
	if e != nil || u.Collection().Name != "users" {
		return nil
	}
	return u
}
func (s *Server) hasUser() (bool, error) { n, e := s.Store.App.CountRecords("users"); return n > 0, e }
func body(r *http.Request) (map[string]any, error) {
	var b map[string]any
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if e := d.Decode(&b); e != nil {
		if e == io.EOF {
			return map[string]any{}, nil
		}
		return nil, e
	}
	if b == nil {
		return nil, errors.New("JSON object required")
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return nil, errors.New("one JSON object required")
	}
	return b, nil
}
func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func (s *Server) session(w http.ResponseWriter, u *core.Record) {
	t, e := u.NewAuthToken()
	if e != nil {
		fail(w, 500, "could not issue session")
		return
	}
	secure := strings.ToLower(os.Getenv("JACKERY_COOKIE_SECURE"))
	http.SetCookie(w, &http.Cookie{Name: "jackery_session", Value: t, Path: "/", MaxAge: 30 * 86400, HttpOnly: true, Secure: secure == "1" || secure == "true" || secure == "yes" || secure == "on", SameSite: http.SameSiteLaxMode})
	jsonOut(w, 200, map[string]any{"ok": true, "username": u.GetString("username"), "token": t})
}
func (s *Server) auth(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	switch p {
	case "/api/auth/setup", "/api/auth/login":
		if r.Method != "POST" {
			fail(w, 405, "method not allowed")
			return true
		}
		s.setupMu.Lock()
		defer s.setupMu.Unlock()
		has, e := s.hasUser()
		if e != nil {
			fail(w, 500, "auth storage unavailable")
			return true
		}
		setup := p == "/api/auth/setup"
		if setup && has {
			fail(w, 403, "already set up")
			return true
		}
		if !setup && !has {
			fail(w, 403, "setup required")
			return true
		}
		b, e := body(r)
		if e != nil {
			fail(w, 400, "invalid JSON object")
			return true
		}
		name, pass := strings.TrimSpace(str(b, "username")), str(b, "password")
		if setup {
			if name == "" || len(pass) < 6 {
				fail(w, 400, "username and password (>=6 chars) required")
				return true
			}
			c, e := s.Store.App.FindCollectionByNameOrId("users")
			if e != nil {
				fail(w, 500, "auth storage unavailable")
				return true
			}
			u := core.NewRecord(c)
			u.Set("username", name)
			u.SetPassword(pass)
			u.Set("verified", true)
			if e = s.Store.App.Save(u); e != nil {
				s.Store.App.Logger().Error("create user", "error", e)
				fail(w, 400, "could not create user")
				return true
			}
			s.session(w, u)
		} else {
			u, e := s.Store.App.FindFirstRecordByData("users", "username", name)
			valid := e == nil && u.ValidatePassword(pass)
			if e == nil && !valid {
				legacy, err := s.Store.Secret(s.Key, "legacy_user:"+u.Id)
				if err == nil && u.ValidatePassword(str(legacy, "_transition_password")) && store.VerifyLegacyPassword(pass, str(legacy, "password_hash")) {
					err = s.Store.App.RunInTransaction(func(app core.App) error {
						user, err := app.FindRecordById("users", u.Id)
						if err != nil {
							return err
						}
						user.SetPassword(pass)
						if err = app.Save(user); err != nil {
							return err
						}
						return (&store.Store{App: app}).Delete("credentials", "legacy_user:"+u.Id)
					})
					if err != nil {
						fail(w, 500, "password migration failed; retry login")
						return true
					}
					u, e = s.Store.App.FindRecordById("users", u.Id)
					valid = e == nil
				}
			}
			if !valid {
				fail(w, 401, "invalid credentials")
				return true
			}
			s.session(w, u)
		}
		return true
	case "/api/auth/me":
		u := s.user(r)
		if u == nil {
			fail(w, 401, "auth_required")
		} else {
			jsonOut(w, 200, map[string]any{"username": u.GetString("username")})
		}
		return true
	case "/api/auth/logout":
		if r.Method != "POST" {
			fail(w, 405, "method not allowed")
			return true
		}
		http.SetCookie(w, &http.Cookie{Name: "jackery_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		jsonOut(w, 200, map[string]any{"ok": true})
		return true
	case "/api/auth/change_password":
		if r.Method != "POST" {
			fail(w, 405, "method not allowed")
			return true
		}
		u := s.user(r)
		if u == nil {
			fail(w, 401, "auth_required")
			return true
		}
		b, e := body(r)
		if e != nil {
			fail(w, 400, "invalid JSON")
			return true
		}
		if !u.ValidatePassword(str(b, "current")) {
			fail(w, 401, "current password is wrong")
			return true
		}
		if len(str(b, "new")) < 6 {
			fail(w, 400, "new password too short (>=6 chars)")
			return true
		}
		u.SetPassword(str(b, "new"))
		if e = s.Store.App.Save(u); e != nil {
			fail(w, 500, "failed to save")
		} else {
			jsonOut(w, 200, map[string]any{"ok": true})
		}
		return true
	}
	return false
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.teslaPublic(w, r) {
		return
	}
	if s.auth(w, r) {
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/ws" {
		s.static(w, r)
		return
	}
	if s.user(r) == nil {
		has, e := s.hasUser()
		if e != nil {
			fail(w, 500, "auth storage unavailable")
		} else if has {
			fail(w, 401, "auth_required")
		} else {
			fail(w, 401, "setup_required")
		}
		return
	}
	if r.URL.Path == "/ws" {
		s.websocket(w, r)
		return
	}
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/ble/") {
		// The inspector handler owns its request bodies and status codes.
		s.bmsInit().sessions.Handler().ServeHTTP(w, r)
		return
	}
	if p == "/api/assistant/attachments" && r.Method == "POST" {
		s.uploadAttachment(w, r)
		return
	}
	var b map[string]any
	if r.Method == "POST" || r.Method == "PUT" {
		var e error
		b, e = body(r)
		if e != nil {
			fail(w, 400, "invalid JSON object")
			return
		}
	}
	if p == "/api/status" && r.Method == "GET" {
		jsonOut(w, 200, s.status(r))
		return
	}
	if p == "/api/view/select_device" && r.Method == "POST" {
		id := str(b, "device_id")
		if id == "" {
			fail(w, 400, "device_id required")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "view_device_id", Value: id, Path: "/", MaxAge: 365 * 86400, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		jsonOut(w, 200, map[string]any{"ok": true, "view_device_id": id})
		return
	}
	rpc := map[string]string{"/api/reconnect": "connect", "/api/set_output": "set_output", "/api/pause_polling": "pause_polling", "/api/resume_polling": "resume_polling", "/api/select_device": "select_device", "/api/auth/status": "auth_status", "/api/auth/credentials": "set_credentials", "/api/auth/forget": "clear_credentials", "/api/events": "get_events", "/api/debug/raw_props": "get_raw_props", "/api/devices/probe_results": "probe_results", "/api/devices/probe_now": "cloud_probe", "/api/debug/cloud_probe": "cloud_probe"}
	if method, ok := rpc[p]; ok {
		expected := "POST"
		if p == "/api/auth/status" || p == "/api/events" || strings.HasPrefix(p, "/api/debug/") || p == "/api/devices/probe_results" {
			expected = "GET"
		}
		if r.Method != expected {
			fail(w, 405, "method not allowed")
			return
		}
		if s.Bridge == nil {
			fail(w, 503, "device bridge unavailable")
			return
		}
		if p == "/api/set_output" {
			port := str(b, "port")
			on, ok := b["on"].(bool)
			_ = on
			if !ok || (port != "ac" && port != "dc" && port != "usb" && port != "car") {
				fail(w, 400, "valid port and boolean on required")
				return
			}
		}
		if b == nil {
			b = map[string]any{}
		}
		{
			for k, values := range r.URL.Query() {
				if _, exists := b[k]; !exists && len(values) > 0 {
					b[k] = values[len(values)-1]
				}
			}
		}
		if p == "/api/set_output" && str(b, "port") == "ac" && b["on"] == false {
			sn := str(b, "device_sn")
			if sn == "" {
				s.mu.RLock()
				d, _ := s.snapshot["device"].(map[string]any)
				sn = str(d, "device_sn")
				s.mu.RUnlock()
			}
			done := s.watchdogUserOff(sn)
			defer done()
		}
		out, e := s.Bridge.Call(r.Context(), method, b)
		if e != nil {
			fail(w, 502, "bridge: "+e.Error())
			return
		}
		if out == nil {
			out = map[string]any{"ok": true}
		}
		if p == "/api/events" {
			since, _ := strconv.ParseFloat(str(b, "since"), 64)
			events, err := s.f7Events(since)
			if err != nil {
				storageError(w, err)
				return
			}
			switch rows := out["events"].(type) {
			case []map[string]any:
				events = append(events, rows...)
			case []any:
				for _, v := range rows {
					if e, ok := v.(map[string]any); ok {
						events = append(events, e)
					}
				}
			}
			sort.SliceStable(events, func(i, j int) bool { return forecast.Number(events[i], "ts") < forecast.Number(events[j], "ts") })
			limit, _ := strconv.Atoi(str(b, "limit"))
			if limit <= 0 {
				limit = 100
			}
			limit = min(limit, 500)
			out["events"] = events[max(0, len(events)-limit):]
		}

		jsonOut(w, 200, out)
		return
	}
	if s.teslaRoutes(w, r, b) {
		return
	}
	if s.solarChargeRoutes(w, r, b) {
		return
	}
	if s.smartChargeRoutes(w, r, b) {
		return
	}
	if s.rescueRoutes(w, r, b) {
		return
	}
	if s.f7Routes(w, r, b) {
		return
	}
	if s.watchdogRoutes(w, r, b) {
		return
	}
	if s.siseliRoutes(w, r, b) || s.ecoflowRoutes(w, r, b) || s.bmsRoutes(w, r, b) {
		return
	}
	if s.kasaRoutes(w, r, b) {
		return
	}
	if s.automationRoutes(w, r, b) {
		return
	}
	if s.forecastRoutes(w, r, b) {
		return
	}
	if s.predictionRoutes(w, r) {
		return
	}
	if s.resources(w, r, b) {
		return
	}
	if s.packRoutes(w, r) {
		return
	}
	if s.energyMutation(w, r, b) {
		return
	}
	if s.energy(w, r) {
		return
	}
	if s.advisorRoutes(w, r) {
		return
	}
	if s.assistantRoutes(w, r, b) {
		return
	}
	if s.aiRoutes(w, r, b) {
		return
	}
	if s.conversations(w, r, b) {
		return
	}
	if s.backupRoutes(w, r, b) {
		return
	}
	fail(w, 501, "Go migration: operation not yet implemented: "+r.Method+" "+p)
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	root := s.PublicDir
	if root == "" {
		root = "pb_public"
	}
	fs := os.DirFS(root)
	path := strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+r.URL.Path)), "/")
	if path == "" {
		path = "index.html"
	}
	if f, e := fs.Open(path); e == nil {
		info, err := f.Stat()
		_ = f.Close()
		if err == nil && !info.IsDir() {
			http.FileServer(http.FS(fs)).ServeHTTP(w, r)
			return
		}
	}
	if strings.Contains(path, ".") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Join(root, "index.html"))
}
func (s *Server) status(r *http.Request) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]any{"connection_status": "disconnected", "connection_error": nil, "device": nil, "telemetry": nil, "last_update_ts": nil, "history": []any{}, "battery_packs": []any{}, "backend": "bridge", "mock_mode": false, "cloud": map[string]any{}, "energy": nil, "low_battery_threshold": 20}
	if _, native := s.Bridge.(*bridge.Native); native {
		out["backend"] = "native"
	}
	for k, v := range s.snapshot {
		out[k] = v
	}
	view := r.URL.Query().Get("view_device_id")
	if view == "" {
		if c, e := r.Cookie("view_device_id"); e == nil {
			view = c.Value
		}
	}
	if view != "" {
		// Resolve both cloud IDs and serials. Unknown views retain the active
		// snapshot, matching Python's per-browser fallback contract.
		var cached map[string]any
		if v, e := s.Store.Get("devices", view); e == nil {
			cached = v
		}
		if cached == nil {
			if rows, e := s.Store.List("devices", "", 0, 1000); e == nil {
				for _, row := range rows {
					if d, ok := row["device"].(map[string]any); ok && (str(d, "device_id") == view || str(d, "device_sn") == view) {
						cached = row
						break
					}
				}
			}
		}
		if cached != nil {
			cloud := map[string]any{}
			if old, ok := out["cloud"].(map[string]any); ok {
				for k, v := range old {
					cloud[k] = v
				}
			}
			cloud["selected_device_id"] = view
			if device, ok := cached["device"].(map[string]any); ok && str(device, "device_id") != "" {
				cloud["selected_device_id"] = device["device_id"]
			}
			out["cloud"] = cloud
			for _, k := range []string{"device", "telemetry", "last_update_ts", "battery_packs"} {
				if v, ok := cached[k]; ok {
					out[k] = v
				}
			}
		}
	}
	if values, _, err := s.settingValues(); err == nil {
		out["low_battery_threshold"] = values["low_battery_threshold"]
	}
	if d, ok := out["device"].(map[string]any); ok {
		out["device"] = s.deviceAlias(d)
	}
	s.mergeFleetStatus(out)
	return out
}
func (s *Server) Poll(ctx context.Context, interval time.Duration) {
	if s.Bridge == nil {
		return
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		s.pollOnce(ctx)
		timer.Reset(s.pollInterval(interval))
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (s *Server) pollOnce(ctx context.Context) {
	var p map[string]any
	defer func() { s.rescueTick(ctx, float64(time.Now().UnixNano())/1e9, p) }()
	if s.Bridge == nil {
		return
	}
	p, e := s.Bridge.Call(ctx, "poll", nil)
	if e != nil {
		s.mu.Lock()
		s.snapshot = map[string]any{"connection_status": "disconnected", "connection_error": e.Error()}
		s.mu.Unlock()
		return
	}
	s.acceptSnapshot(ctx, p)
}
func (s *Server) acceptSnapshot(ctx context.Context, p map[string]any) {
	snap := map[string]any{"connection_status": "connected", "connection_error": nil, "last_update_ts": p["last_update_ts"]}
	for _, k := range []string{"device", "telemetry", "cloud", "source", "battery_packs", "connection_status", "connection_error"} {
		if v, ok := p[k]; ok {
			snap[k] = v
		}
	}
	s.mu.Lock()
	s.snapshot = snap
	s.mu.Unlock()
	fleet, _ := p["cloud"].(map[string]any)
	entries, _ := fleet["devices_telemetry"].(map[string]any)
	if len(entries) > 0 {
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			snapshot := map[string]any{"device": entry["device"], "telemetry": entry["telemetry"], "last_update_ts": entry["ts"], "battery_packs": entry["battery_packs"], "pack_ts": entry["pack_ts"]}
			s.persistObservation(ctx, snapshot)
		}
	} else {
		s.persistObservation(ctx, p)
	}
}
func (s *Server) persistObservation(ctx context.Context, p map[string]any) {
	stamp, _ := p["last_update_ts"].(float64)
	if stamp <= 0 {
		return
	}
	snap := map[string]any{"device": p["device"], "telemetry": p["telemetry"], "last_update_ts": stamp, "battery_packs": p["battery_packs"]}
	if d, ok := p["device"].(map[string]any); ok {
		sn := str(d, "device_sn")
		if sn != "" {
			if previous, err := s.Store.Get("devices", sn); err == nil {
				oldDevice, _ := previous["device"].(map[string]any)
				merged := map[string]any{}
				for key, value := range oldDevice {
					merged[key] = value
				}
				for key, value := range d {
					merged[key] = value
				}
				merged["last_seen"] = stamp
				snap["device"] = merged

				for _, key := range []string{"capacity_wh_override", "_capacity_corrected"} {
					if value, ok := previous[key]; ok {
						snap[key] = value
					}
				}
			}
			if e := s.Store.Put("devices", sn, sn, snap); e != nil {
				s.Store.App.Logger().Error("persist device", "error", e)
			}
			if t, ok := p["telemetry"].(map[string]any); ok {
				if packs, ok := p["battery_packs"].([]any); ok {
					packStamp, _ := p["pack_ts"].(float64)
					if packStamp <= 0 {
						packStamp = stamp
					}
					if e := s.Store.Put("battery_packs", fmt.Sprintf("packs:%s:%.6f", sn, packStamp), sn, map[string]any{"ts": packStamp, "packs": packs}); e != nil {
						s.Store.App.Logger().Error("persist battery packs", "error", e)
					}
				}
				s.watchdogTick(ctx, sn, t, d["model_code"], stamp)
				s.f7Tick(ctx, sn, t)
				s.evaluateRules(ctx, sn, t)
				row := map[string]any{"ts": stamp, "device_sn": sn}
				for k, v := range t {
					row[k] = v
				}
				row["solar_charge_diverted_w"] = s.solarTelemetry(ctx, sn, t, stamp)
				row["battery_pct"] = t["battery_percent"]
				for dst, src := range map[string]string{"input_w": "input_power_w", "output_w": "output_power_w", "solar_w": "solar_input_w"} {
					row[dst] = t[src]
				}
				if e := s.Store.Put("samples", fmt.Sprintf("observation:%s:%.6f", sn, stamp), sn, row); e != nil {
					s.Store.App.Logger().Error("persist telemetry", "error", e)
				}
			}
		}
	}
}
func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	c, e := websocket.Accept(w, r, nil)
	if e != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	ctx = c.CloseRead(ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	kind := "snapshot"
	for {
		if s.user(r) == nil {
			_ = c.Close(websocket.StatusPolicyViolation, "auth_required")
			return
		}
		data, e := json.Marshal(map[string]any{"type": kind, "data": s.status(r)})
		if e != nil {
			return
		}
		writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		e = c.Write(writeCtx, websocket.MessageText, data)
		stop()
		if e != nil {
			return
		}
		kind = "status"
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func storageError(w http.ResponseWriter, e error) {
	if errors.Is(e, sql.ErrNoRows) {
		fail(w, 404, "not found")
	} else {
		fail(w, 500, fmt.Sprintf("storage failure: %v", e))
	}
}
