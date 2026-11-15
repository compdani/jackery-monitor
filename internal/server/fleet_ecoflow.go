package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/ecoflow"
)

type ecoflowStore struct{ settingsDoc }

func (e ecoflowStore) Load() ([]ecoflow.Device, error) {
	var out []ecoflow.Device
	return out, e.load("devices", &out)
}
func (e ecoflowStore) Save(d []ecoflow.Device) error { return e.save("devices", d) }

type reconnectStore struct{ settingsDoc }

func (r reconnectStore) Load() (ecoflow.ReconnectConfig, bool, error) {
	var c ecoflow.ReconnectConfig
	raw, err := r.s.Store.Get("settings", r.key)
	if isMissing(err) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	return c, true, fromMap(raw, &c)
}
func (r reconnectStore) Save(c ecoflow.ReconnectConfig) error { return r.save("", c) }

// ecoflowRuntime mirrors server.py's state.ecoflow plus the live client.
type ecoflowRuntime struct {
	once          sync.Once
	reg           *ecoflow.Registry
	reconnect     *ecoflow.Reconnect
	mu            sync.Mutex
	client        *ecoflow.Client
	state         string
	err           any
	lastPoll      *float64
	nextRetry     float64
	rotated       bool
	quotaAttempts int
	lastQuota     float64
	live          map[string]ecoflow.Live
	wake          chan struct{}
}

func (s *Server) ecoflowInit() *ecoflowRuntime {
	rt := &s.fleet.ecoflow
	rt.once.Do(func() {
		rt.reg = ecoflow.NewRegistry(ecoflowStore{settingsDoc{s, "ecoflow_devices"}})
		rt.reconnect = ecoflow.NewReconnect(reconnectStore{settingsDoc{s, "ecoflow_reconnect"}})
		rt.state, rt.live, rt.wake = "idle", map[string]ecoflow.Live{}, make(chan struct{}, 1)
	})
	return rt
}
func (s *Server) ecoflowRegistry() *ecoflow.Registry { return s.ecoflowInit().reg }
func (s *Server) ecoflowLive() map[string]ecoflow.Live {
	rt := s.ecoflowInit()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := map[string]ecoflow.Live{}
	for k, v := range rt.live {
		out[k] = v
	}
	return out
}
func (s *Server) ecoflowCreds() (*ecoflow.Credentials, error) {
	raw, err := s.Store.Secret(s.Key, "ecoflow")
	if isMissing(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c ecoflow.Credentials
	if err = fromMap(raw, &c); err != nil {
		return nil, err
	}
	c = c.Normalize()
	if !c.Valid() {
		return nil, nil
	}
	return &c, nil
}
func (s *Server) newEcoflowClient(c ecoflow.Credentials) *ecoflow.Client {
	mqtt := s.EcoFlowMQTT
	if mqtt == nil {
		mqtt = &ecoflow.PahoTransport{}
	}
	return ecoflow.NewClient(ecoflow.ClientOptions{Email: c.Email, Password: c.Password, APIHost: c.APIHost, MQTTClientID: c.MQTTClientID, HTTP: s.EcoFlowHTTP, MQTT: mqtt, OnUpdate: s.ecoflowUpdate})
}

// ecoflowUpdate ports _ecoflow_on_update: cache, persist and re-record the
// linked Siseli parent with the fresh overlay.
func (s *Server) ecoflowUpdate(sn string, tele ecoflow.Telemetry, detail ecoflow.Detail) {
	rt := s.ecoflowInit()
	ts := float64(time.Now().UnixNano()) / 1e9
	t, d := toSolarMap(tele), toSolarMap(detail)
	rt.mu.Lock()
	rt.live[sn] = ecoflow.Live{Telemetry: t, Detail: d, TS: &ts}
	rt.lastPoll, rt.state, rt.err = &ts, "connected", nil
	rt.mu.Unlock()
	row, _ := rt.reg.Get(sn)
	name := row.Alias
	if name == "" {
		name = sn
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.recordFleet(ctx, map[string]any{"device_sn": sn, "device_id": sn, "name": name, "source": "ecoflow", "device_type": row.DeviceType}, t, ts)
	if parent := row.SiseliDeviceSN; parent != nil && *parent != "" {
		s.fleet.siseli.mu.Lock()
		entry := s.fleet.siseli.telemetry[*parent]
		s.fleet.siseli.mu.Unlock()
		if pt, ok := entry["telemetry"].(map[string]any); ok {
			s.recordSiseli(ctx, *parent, pt, ts)
		}
	}
}
func (s *Server) ecoflowStop() {
	rt := s.ecoflowInit()
	rt.mu.Lock()
	c := rt.client
	rt.client = nil
	rt.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// ecoflowStart ports _ecoflow_start_client, including one client-id
// rotation after MQTT auth failure and the 5/30/300 s retry schedule.
func (s *Server) ecoflowStart(ctx context.Context) {
	rt := s.ecoflowInit()
	creds, err := s.ecoflowCreds()
	devices := rt.reg.ListDevices()
	s.ecoflowStop()
	now := float64(time.Now().UnixNano()) / 1e9
	if err != nil || creds == nil || len(devices) == 0 {
		rt.mu.Lock()
		rt.state, rt.err, rt.nextRetry, rt.rotated = "idle", nil, 0, false
		if err != nil {
			rt.err = err.Error()
		} else if creds != nil {
			rt.err = "no devices configured"
		}
		rt.mu.Unlock()
		return
	}
	client := s.newEcoflowClient(*creds)
	setErr := func(msg string, retry float64) {
		rt.mu.Lock()
		rt.state, rt.err, rt.nextRetry = "error", msg, now+retry
		rt.mu.Unlock()
	}
	if err = client.Login(ctx); err != nil {
		client.Close()
		var auth *ecoflow.ErrAuth
		if errors.As(err, &auth) {
			setErr(err.Error(), 300)
		} else {
			setErr(err.Error(), 30)
		}
		return
	}
	id := client.MQTTClientID()
	_ = s.Store.SaveSecret(s.Key, "ecoflow", toSolarMap(creds.WithSession(client.Token(), client.UserID(), &id)))
	meta := []ecoflow.DeviceMeta{}
	for _, d := range devices {
		meta = append(meta, ecoflow.DeviceMeta{RawSN: d.RawSN, SN: d.SN, Alias: d.Alias, DeviceType: d.DeviceType, CapacityWh: d.CapacityWh})
	}
	client.ConfigureDevices(meta)
	ok, _ := client.StartMQTT(ctx, ecoflow.MQTTConnectWait)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.client = client
	if ok && client.Connected() {
		rt.state, rt.err, rt.nextRetry, rt.rotated, rt.quotaAttempts, rt.lastQuota = "connected", nil, 0, false, 0, now
		return
	}
	msg := client.MQTTError()
	if msg == "" {
		msg = "MQTT connect failed"
	}
	rt.state, rt.err = "error", msg
	if client.AuthFailed() && !rt.rotated {
		if fresh, err := ecoflow.ResolveClientID(client.UserID(), ""); err == nil {
			_ = s.Store.SaveSecret(s.Key, "ecoflow", toSolarMap(creds.WithSession(client.Token(), client.UserID(), &fresh)))
		}
		rt.rotated, rt.nextRetry = true, now+5
	} else if client.AuthFailed() {
		rt.nextRetry = now + 300
	} else {
		rt.nextRetry = now + 30
	}
}
func (s *Server) ecoflowNeedsResubscribe(c *ecoflow.Client) bool {
	want := map[string]bool{}
	for _, d := range s.ecoflowRegistry().ListDevices() {
		raw := d.RawSN
		if raw == "" {
			raw = ecoflow.RawSNFromSN(d.SN)
		}
		if raw != "" {
			want[raw] = true
		}
	}
	if len(want) == 0 {
		return false
	}
	if c == nil {
		return true
	}
	have := c.ConfiguredRawSNs()
	if len(have) != len(want) {
		return true
	}
	for k := range want {
		if !have[k] {
			return true
		}
	}
	return false
}

// ecoflowTick ports one ecoflow_loop iteration: (re)start, three quota
// retries 20 s apart, then an auto-reconnect inside the allowed window.
func (s *Server) ecoflowTick(ctx context.Context, now float64) {
	rt := s.ecoflowInit()
	creds, _ := s.ecoflowCreds()
	if creds == nil || len(rt.reg.ListDevices()) == 0 {
		s.ecoflowStop()
		rt.mu.Lock()
		rt.state, rt.err, rt.nextRetry, rt.quotaAttempts = "idle", nil, 0, 0
		rt.mu.Unlock()
		return
	}
	rt.mu.Lock()
	client, next := rt.client, rt.nextRetry
	rt.mu.Unlock()
	if (client == nil || !client.Connected() || s.ecoflowNeedsResubscribe(client)) && now >= next {
		s.ecoflowStart(ctx)
		rt.mu.Lock()
		client = rt.client
		rt.mu.Unlock()
	}
	if client == nil || !client.Connected() {
		return
	}
	rt.mu.Lock()
	stale := rt.lastPoll == nil || now-*rt.lastPoll > 60
	attempts, last := rt.quotaAttempts, rt.lastQuota
	silent := rt.lastPoll == nil || now-*rt.lastPoll > 90
	rt.mu.Unlock()
	if stale && attempts < 3 && now-last >= 20 {
		client.RequestQuotaAll()
		rt.mu.Lock()
		rt.quotaAttempts, rt.lastQuota = attempts+1, now
		if rt.lastPoll == nil && attempts+1 >= 3 && rt.err == nil {
			rt.err = "MQTT up but no telemetry yet (check SN / device online)"
		}
		rt.mu.Unlock()
	} else if stale && attempts >= 3 && silent && rt.reconnect.AutoReconnectAllowed(now, s.tzOffset()) {
		rt.mu.Lock()
		rt.nextRetry, rt.quotaAttempts = 0, 0
		rt.mu.Unlock()
		s.ecoflowStop()
		kick(rt.wake)
	}
}

// EcoFlowLoop keeps the MQTT session alive; wakes on registry/credential edits.
func (s *Server) EcoFlowLoop(ctx context.Context) {
	rt := s.ecoflowInit()
	defer s.ecoflowStop()
	for {
		s.ecoflowTick(ctx, float64(time.Now().UnixNano())/1e9)
		wait := 60 * time.Second
		rt.mu.Lock()
		if d := rt.nextRetry - float64(time.Now().Unix()); d > 0 {
			wait = min(wait, max(time.Second, time.Duration(d*float64(time.Second))))
		} else if rt.client != nil && rt.client.Connected() && rt.quotaAttempts < 3 && (rt.lastPoll == nil || float64(time.Now().Unix())-*rt.lastPoll > 60) {
			wait = 20 * time.Second
		}
		rt.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-rt.wake:
		case <-time.After(wait):
		}
	}
}
func (s *Server) ecoflowStatus() map[string]any {
	rt := s.ecoflowInit()
	c, _ := s.ecoflowCreds()
	out := map[string]any{"has_credentials": c != nil, "email": nil, "api_host": ecoflow.DefaultAPIHost, "has_password": false}
	if c != nil {
		out["email"], out["api_host"], out["has_password"] = c.Email, c.APIHost, c.Password != ""
	}
	rt.mu.Lock()
	out["state"], out["error"], out["last_poll_ts"] = rt.state, rt.err, rt.lastPoll
	stats := ecoflow.MQTTStats{}
	if rt.client != nil {
		stats = rt.client.Stats()
	}
	rt.mu.Unlock()
	out["mqtt_msg_count"], out["mqtt_decode_empty"], out["mqtt_update_count"], out["last_mqtt_topic"] = stats.MsgCount, stats.DecodeEmpty, stats.UpdateCount, stats.LastTopic
	out["devices"] = rt.reg.ListDevices()
	out["reconnect"] = rt.reconnect.Get()
	return out
}
func (s *Server) ecoflowRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	p, m := r.URL.Path, r.Method
	if !strings.HasPrefix(p, "/api/ecoflow/") {
		return false
	}
	rt := s.ecoflowInit()
	switch {
	case p == "/api/ecoflow/credentials" && m == "GET":
		jsonOut(w, 200, s.ecoflowStatus())
	case p == "/api/ecoflow/reconnect_settings" && m == "GET":
		out := toSolarMap(rt.reconnect.Get())
		out["ok"] = true
		jsonOut(w, 200, out)
	case p == "/api/ecoflow/reconnect_settings" && m == "POST":
		var patch ecoflow.ReconnectPatch
		if v, ok := b["auto_reconnect"].(bool); ok {
			patch.AutoReconnect = &v
		}
		for key, set := range map[string]func(*string){"reconnect_start": func(v *string) { patch.SetStart, patch.ReconnectStart = true, v }, "reconnect_end": func(v *string) { patch.SetEnd, patch.ReconnectEnd = true, v }} {
			if raw, ok := b[key]; ok {
				if raw == nil {
					set(nil)
				} else if v, ok := raw.(string); ok {
					set(&v)
				} else {
					fail(w, 400, key+" must be HH:MM or null")
					return true
				}
			}
		}
		cfg, err := rt.reconnect.Set(patch)
		if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		out := toSolarMap(cfg)
		out["ok"] = true
		jsonOut(w, 200, out)
	case p == "/api/ecoflow/reconnect" && m == "POST":
		if c, _ := s.ecoflowCreds(); c == nil {
			fail(w, 400, "EcoFlow credentials not configured")
			return true
		}
		if len(rt.reg.ListDevices()) == 0 {
			fail(w, 400, "No EcoFlow devices registered")
			return true
		}
		rt.mu.Lock()
		rt.nextRetry, rt.quotaAttempts = 0, 0
		rt.mu.Unlock()
		s.ecoflowStop()
		kick(rt.wake)
		out := s.ecoflowStatus()
		out["ok"] = true
		jsonOut(w, 200, out)
	case p == "/api/ecoflow/credentials" && m == "POST":
		existing, err := s.ecoflowCreds()
		if err != nil {
			storageError(w, err)
			return true
		}
		in := ecoflow.Credentials{Email: str(b, "email"), Password: str(b, "password"), APIHost: str(b, "api_host")}.Normalize()
		if in.Password == "" && existing != nil {
			in.Password = existing.Password
		}
		if !in.Valid() {
			fail(w, 400, "email and password are required")
			return true
		}
		if existing != nil {
			in.MQTTClientID = existing.MQTTClientID
		}
		probe := s.newEcoflowClient(in)
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		err = probe.Login(ctx)
		probe.Close()
		var auth *ecoflow.ErrAuth
		if errors.As(err, &auth) {
			fail(w, 400, err.Error())
			return true
		} else if err != nil {
			fail(w, 502, "Could not reach EcoFlow API: "+err.Error())
			return true
		}
		id := probe.MQTTClientID()
		if err = s.Store.SaveSecret(s.Key, "ecoflow", toSolarMap(in.WithSession(probe.Token(), probe.UserID(), &id))); err != nil {
			fail(w, 500, "failed to save credentials")
			return true
		}
		kick(rt.wake)
		jsonOut(w, 200, map[string]any{"ok": true, "email": in.Email, "api_host": in.APIHost, "devices": rt.reg.ListDevices()})
	case p == "/api/ecoflow/credentials" && m == "DELETE":
		if err := s.Store.Delete("credentials", "ecoflow"); err != nil && !isMissing(err) {
			storageError(w, err)
			return true
		}
		s.ecoflowStop()
		rt.mu.Lock()
		rt.state, rt.err, rt.lastPoll, rt.nextRetry, rt.rotated, rt.quotaAttempts, rt.lastQuota, rt.live = "idle", nil, nil, 0, false, 0, 0, map[string]ecoflow.Live{}
		rt.mu.Unlock()
		kick(rt.wake)
		jsonOut(w, 200, map[string]any{"ok": true})
	case p == "/api/ecoflow/devices" && m == "GET":
		devices := rt.reg.ListDevices()
		if sn := r.URL.Query().Get("siseli_sn"); sn != "" {
			devices = rt.reg.ListForSiseli(sn)
		}
		jsonOut(w, 200, map[string]any{"devices": devices})
	case p == "/api/ecoflow/devices" && m == "POST":
		raw := strings.TrimSpace(str(b, "raw_sn"))
		if raw == "" {
			raw = strings.TrimSpace(str(b, "sn"))
		}
		if raw == "" {
			fail(w, 400, "sn / raw_sn is required")
			return true
		}
		opts := ecoflow.UpsertOpts{DeviceType: str(b, "device_type"), Alias: str(b, "alias")}
		if v, ok := numberArg(b["capacity_wh"]); ok {
			n := int(v)
			opts.CapacityWh = &n
		}
		if v, ok := b["siseli_device_sn"]; ok {
			sn, _ := v.(string)
			opts.SiseliDeviceSN = &sn
		}
		if v, ok := b["roles"]; ok {
			opts.Roles = stringList(v)
		}
		row, err := rt.reg.Upsert(raw, opts)
		if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		kick(rt.wake)
		jsonOut(w, 200, map[string]any{"ok": true, "device": row})
	case strings.HasSuffix(p, "/link") && strings.HasPrefix(p, "/api/ecoflow/devices/") && m == "POST":
		sn, _ := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(p, "/api/ecoflow/devices/"), "/link"))
		var parent *string
		if v, ok := b["siseli_device_sn"].(string); ok {
			parent = &v
		}
		var roles []string
		if v, ok := b["roles"]; ok {
			roles = stringList(v)
		}
		row, err := rt.reg.Link(sn, parent, roles)
		if errors.Is(err, ecoflow.ErrNotFound) {
			fail(w, 404, "device not found")
			return true
		} else if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		jsonOut(w, 200, map[string]any{"ok": true, "device": row})
	case strings.HasPrefix(p, "/api/ecoflow/devices/") && m == "DELETE":
		sn, _ := url.PathUnescape(strings.TrimPrefix(p, "/api/ecoflow/devices/"))
		if !rt.reg.Delete(sn) {
			fail(w, 404, "device not found")
			return true
		}
		if key, err := ecoflow.MakeSN(sn); err == nil {
			rt.mu.Lock()
			delete(rt.live, key)
			rt.mu.Unlock()
		}
		kick(rt.wake)
		jsonOut(w, 200, map[string]any{"ok": true})
	default:
		return false
	}
	return true
}
func stringList(v any) []string {
	out := []string{}
	list, _ := v.([]any)
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ecoflowBatteryWh ports _ecoflow_battery_capacity_wh for a Siseli serial.
func (s *Server) ecoflowBatteryWh(sn string) float64 {
	if !strings.HasPrefix(sn, "siseli:") {
		return 0
	}
	total := 0.0
	for _, d := range s.ecoflowRegistry().ListForSiseli(sn) {
		battery := len(d.Roles) == 0
		for _, r := range d.Roles {
			battery = battery || r == "battery"
		}
		if battery && d.CapacityWh > 0 {
			total += float64(d.CapacityWh)
		}
	}
	return total
}

// siseliSeed ports _siseli_forecast_seed: SOC and capacity from the overlaid
// live Siseli view (BMS + EcoFlow).
func (s *Server) siseliSeed(sn string) (*float64, float64) {
	if !strings.HasPrefix(sn, "siseli:") {
		return nil, 0
	}
	s.fleet.siseli.mu.Lock()
	entry := s.fleet.siseli.telemetry[sn]
	s.fleet.siseli.mu.Unlock()
	raw, _ := entry["telemetry"].(map[string]any)
	if raw == nil {
		return nil, 0
	}
	t := s.siseliOverlay(sn, raw)
	var soc *float64
	if v, ok := numberArg(t["battery_percent"]); ok {
		soc = &v
	}
	capacity, _ := numberArg(t["capacity_wh"])
	return soc, capacity
}
