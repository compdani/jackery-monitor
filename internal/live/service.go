// Package live owns cloud polling and the immutable REST/WebSocket snapshots.
package live

import (
	"context"
	"encoding/json"
	"errors"
	"jackery-monitor/internal/jackery"
	"jackery-monitor/internal/secrets"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Backend interface {
	Devices(context.Context) ([]jackery.Device, error)
	Properties(context.Context, string) (map[string]any, error)
	Subscribe(context.Context, func(string, map[string]any), func(bool, string)) error
	SetOutput(context.Context, string, string, bool) error
	Close()
}
type Options struct {
	Mock     bool
	Disabled bool
}
type entry struct {
	Raw                map[string]any
	TS, HTTPAt, MQTTAt time.Time
	History            []map[string]any
	LastHistory        time.Time
	DeltaAt            map[string]time.Time
	Packs              []map[string]any
	PacksAt            time.Time
	PackError          string
}
type Service struct {
	op                                    sync.Mutex   // serializes backend lifecycle, polling and commands
	mu                                    sync.RWMutex // protects snapshots and subscribers; never held during I/O
	opts                                  Options
	store                                 *secrets.Store
	backend                               Backend
	factory                               func(secrets.Credentials) Backend
	settings                              func(string) int
	devices                               []jackery.Device
	entries                               map[string]*entry
	state, lastError, email, mqttError    string
	mqttConnected, pinned, hasCredentials bool
	pauseUntil, contestedUntil, nextPoll  time.Time
	generation                            int
	subscribers                           map[chan struct{}]struct{}
	contestedConsecutive                  int
}

func New(root string, opts Options, settings func(string) int) *Service {
	s := &Service{opts: opts, settings: settings, entries: map[string]*entry{}, devices: []jackery.Device{}, state: "needs-credentials", subscribers: map[chan struct{}]struct{}{}, factory: func(c secrets.Credentials) Backend { return jackery.New(c) }}
	path := os.Getenv("JACKERY_CREDS_FILE")
	if path == "" {
		path = filepath.Join(root, "jackery-creds.json")
	}
	key := secrets.KeyFile(root)
	s.store = &secrets.Store{Path: path, KeyPath: key}
	if opts.Mock {
		s.hasCredentials = true
		s.state = "connected"
		s.mockPoll(time.Now())
		return s
	}
	creds := secrets.Credentials{Email: os.Getenv("JACKERY_EMAIL"), Password: os.Getenv("JACKERY_PASSWORD"), Region: "US"}
	if creds.Email != "" && creds.Password != "" {
		s.pinned = true
	} else {
		var err error
		creds, err = s.store.Load()
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				s.lastError = err.Error()
			}
			return s
		}
	}
	s.hasCredentials = true
	s.email = creds.Email
	s.backend = s.factory(creds)
	s.state = "connecting"
	return s
}
func (s *Service) notifyLocked() {
	for ch := range s.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func (s *Service) Subscribe() (chan struct{}, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{}, 1)
	s.subscribers[ch] = struct{}{}
	return ch, func() { s.mu.Lock(); delete(s.subscribers, ch); s.mu.Unlock() }
}
func (s *Service) Run(ctx context.Context) {
	defer func() {
		s.op.Lock()
		defer s.op.Unlock()
		s.mu.Lock()
		s.generation++
		s.mu.Unlock()
		if s.backend != nil {
			s.backend.Close()
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		s.Poll(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) Poll(ctx context.Context, now time.Time) {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.RLock()
	skip := now.Before(s.nextPoll) || now.Before(s.pauseUntil) || now.Before(s.contestedUntil)
	s.mu.RUnlock()
	if skip || ctx.Err() != nil {
		return
	}
	if s.opts.Mock {
		s.mockPoll(now)
		s.mu.Lock()
		s.nextPoll = now.Add(time.Duration(s.settings("poll_interval_s")) * time.Second)
		s.mu.Unlock()
		return
	}
	if s.backend == nil {
		return
	}
	s.mu.Lock()
	s.nextPoll = now.Add(time.Duration(s.settings("cloud_poll_interval_s")) * time.Second)
	generation := s.generation
	s.mu.Unlock()
	devices, err := s.backend.Devices(ctx)
	if err != nil {
		s.failed(err, now)
		return
	}
	s.mu.Lock()
	s.devices = devices
	for sn := range s.entries {
		found := false
		for _, d := range devices {
			if d.SN == sn {
				found = true
				break
			}
		}
		if !found {
			delete(s.entries, sn)
		}
	}
	s.mu.Unlock()
	var pollError error
	for _, d := range devices {
		start := time.Now()
		props, err := s.backend.Properties(ctx, d.ID)
		if err != nil {
			if errors.Is(err, jackery.ErrContested) {
				s.failed(err, now)
				return
			}
			pollError = err
			continue
		}
		s.mu.Lock()
		existing := s.entries[d.SN]
		// Don't overwrite a delta that arrived while the HTTP request was in flight.
		if existing != nil && existing.MQTTAt.After(start) {
			for key, updated := range existing.DeltaAt {
				if updated.After(start) {
					props[key] = existing.Raw[key]
				}
			}
		}
		s.updateLocked(d.SN, props, time.Now(), true)
		s.mu.Unlock()
		if fetcher, ok := s.backend.(interface {
			Packs(context.Context, string) ([]map[string]any, error)
		}); ok {
			packStart := time.Now()
			packs, err := fetcher.Packs(ctx, d.SN)
			if errors.Is(err, jackery.ErrContested) {
				s.failed(err, now)
				return
			}
			s.mu.Lock()
			current := s.entries[d.SN]
			if err != nil {
				current.PackError = err.Error()
			} else if !current.PacksAt.After(packStart) {
				current.Packs = packs
				current.PacksAt = time.Now()
				current.PackError = ""
			}
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	s.state = "connected"
	s.lastError = ""
	s.contestedConsecutive = 0
	if pollError != nil {
		s.state = "degraded"
		s.lastError = pollError.Error()
	}
	s.contestedUntil = time.Time{}
	s.notifyLocked()
	s.mu.Unlock()
	if handler, ok := s.backend.(interface {
		SetPackHandler(func(string, []map[string]any))
	}); ok {
		handler.SetPackHandler(func(sn string, packs []map[string]any) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if generation != s.generation {
				return
			}
			if e := s.entries[sn]; e != nil {
				e.Packs = packs
				e.PacksAt = time.Now()
				e.PackError = ""
				s.notifyLocked()
			}
		})
	}
	mqttCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	err = s.backend.Subscribe(mqttCtx, func(sn string, delta map[string]any) { s.applyDelta(generation, sn, delta) }, func(connected bool, message string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if generation != s.generation {
			return
		}
		s.mqttConnected = connected
		s.mqttError = message
		s.notifyLocked()
	})
	if err != nil {
		s.mu.Lock()
		s.mqttConnected = false
		s.mqttError = err.Error()
		s.notifyLocked()
		s.mu.Unlock()
	}
}
func (s *Service) failed(err error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = err.Error()
	s.state = "error"
	if errors.Is(err, jackery.ErrContested) {
		s.state = "contested"
		s.contestedConsecutive++
		multiplier := 1 << min(s.contestedConsecutive-1, 6)
		cooldown := min(3600, s.settings("session_contested_cooldown_s")*multiplier)
		s.contestedUntil = now.Add(time.Duration(cooldown) * time.Second)
		s.nextPoll = s.contestedUntil
	}
	if errors.Is(err, jackery.ErrCredentials) {
		s.state = "auth-error"
		s.nextPoll = now.Add(24 * time.Hour)
	}
	s.notifyLocked()
}
func (s *Service) updateLocked(sn string, props map[string]any, now time.Time, http bool) {
	e := s.entries[sn]
	if e == nil {
		e = &entry{Raw: map[string]any{}, History: []map[string]any{}, DeltaAt: map[string]time.Time{}}
		s.entries[sn] = e
	}
	for k, v := range props {
		e.Raw[k] = v
		if !http {
			if e.DeltaAt == nil {
				e.DeltaAt = map[string]time.Time{}
			}
			e.DeltaAt[k] = now
		}
	}
	e.TS = now
	if http {
		e.HTTPAt = now
	} else {
		e.MQTTAt = now
	}
	if now.Sub(e.LastHistory) >= 30*time.Second {
		t := jackery.Telemetry(e.Raw)
		t["ts"] = stamp(now)
		e.History = append(e.History, t)
		if len(e.History) > 720 {
			e.History = append([]map[string]any(nil), e.History[len(e.History)-720:]...)
		}
		e.LastHistory = now
	}
	s.notifyLocked()
}
func (s *Service) applyDelta(generation int, sn string, delta map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.generation {
		return
	}
	if _, ok := s.entries[sn]; !ok {
		return
	}
	s.updateLocked(sn, delta, time.Now(), false)
}
func (s *Service) mockPoll(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.devices) == 0 {
		s.devices = []jackery.Device{{ID: "mock-5000", SN: "MOCK-5000", Name: "Explorer 5000 Plus", ModelCode: 13, ModelName: "Explorer 5000 Plus"}, {ID: "mock-3000", SN: "MOCK-3000", Name: "HomePower 3000", ModelCode: 19, ModelName: "HomePower 3000"}}
	}
	for i, d := range s.devices {
		solar := int(450 + 250*math.Sin(float64(now.Unix())/120+float64(i)))
		raw := map[string]any{"rb": 76 - i*13, "bt": 256, "ip": solar, "op": 320 + i*90, "acip": 0, "cip": 0, "acov": 1200, "acohz": 60, "oac": 1, "odc": 0, "odcu": 1, "odcc": 0, "ups": 1, "it": 22, "ot": 999, "bs": 1}
		if e := s.entries[d.SN]; e != nil {
			for _, port := range []string{"ac", "dc", "usb", "car"} {
				key := jackery.PortProperty(port)
				raw[key] = e.Raw[key]
			}
			if raw["oac"] == 0 {
				raw["op"] = 0
			}
		}
		s.updateLocked(d.SN, raw, now, true)
		e := s.entries[d.SN]
		e.Packs = []map[string]any{}
		e.PacksAt = now
		if i == 0 {
			e.Packs = []map[string]any{{"deviceSn": "MOCK-PACK-1", "parentDeviceSn": d.SN, "deviceOrder": 1, "rb": 80, "ip": 100, "op": 20, "it": 25, "ec": 0}, {"deviceSn": "MOCK-PACK-2", "parentDeviceSn": d.SN, "deviceOrder": 2, "rb": 84, "ip": 120, "op": 20, "it": 26, "ec": 0}}
		}
	}
	s.state = "connected"
	s.lastError = ""
}
func (s *Service) SetOutput(ctx context.Context, sn, port string, on bool) error {
	if !jackery.ValidPort(port) {
		return errors.New("port must be one of: ac, dc, usb, car")
	}
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.RLock()
	found := false
	for _, d := range s.devices {
		if sn == "" {
			sn = d.SN
		}
		if d.SN == sn {
			found = true
			break
		}
	}
	paused := time.Now().Before(s.pauseUntil)
	s.mu.RUnlock()
	if !found {
		return errors.New("unknown device serial")
	}
	if paused {
		return errors.New("resume polling before sending commands")
	}
	if s.opts.Mock {
		s.mu.Lock()
		v := 0
		if on {
			v = 1
		}
		s.updateLocked(sn, map[string]any{jackery.PortProperty(port): v}, time.Now(), false)
		s.mu.Unlock()
		return nil
	}
	if s.backend == nil {
		return errors.New("cloud is not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.backend.SetOutput(ctx, sn, port, on); err != nil {
		return err
	}
	// PUBACK is broker acceptance, not device confirmation. Keep reported port
	// state unchanged until telemetry confirms it; trigger a fresh HTTP read.
	s.mu.Lock()
	s.nextPoll = time.Time{}
	s.mu.Unlock()
	return nil
}
func (s *Service) Pause(seconds int) map[string]any {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if seconds == 0 {
		seconds = 600
	}
	seconds = min(3600, max(1, seconds))
	s.pauseUntil = time.Now().Add(time.Duration(seconds) * time.Second)
	s.notifyLocked()
	return map[string]any{"ok": true, "seconds": seconds, "pause_until": stamp(s.pauseUntil)}
}
func (s *Service) Resume() map[string]any {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	wasPaused := time.Now().Before(s.pauseUntil)
	s.pauseUntil = time.Time{}
	s.contestedUntil = time.Time{}
	s.nextPoll = time.Time{}
	s.notifyLocked()
	s.mu.Unlock()
	return map[string]any{"ok": true, "was_paused": wasPaused}
}
func (s *Service) SetCredentials(ctx context.Context, creds secrets.Credentials) error {
	s.op.Lock()
	defer s.op.Unlock()
	if s.opts.Mock {
		return errors.New("credentials are not used in mock mode")
	}
	if s.pinned {
		return errors.New("credentials are provided by environment variables")
	}
	// Validate HTTP login and device access before replacing the saved account.
	candidate := s.factory(creds)
	if _, err := candidate.Devices(ctx); err != nil {
		candidate.Close()
		return err
	}
	if err := s.store.Save(creds); err != nil {
		candidate.Close()
		return err
	}
	s.replace(candidate, creds.Email)
	return nil
}
func (s *Service) replace(backend Backend, email string) {
	s.mu.Lock()
	s.generation++
	s.contestedConsecutive = 0
	s.mqttConnected = false
	s.mqttError = ""
	s.devices = []jackery.Device{}
	s.entries = map[string]*entry{}
	s.email = email
	s.hasCredentials = backend != nil
	s.lastError = ""
	s.state = "needs-credentials"
	if backend != nil {
		s.state = "connecting"
	}
	s.nextPoll = time.Time{}
	s.pauseUntil = time.Time{}
	s.contestedUntil = time.Time{}
	s.notifyLocked()
	s.mu.Unlock()
	if s.backend != nil {
		s.backend.Close()
	}
	s.backend = backend
}
func (s *Service) Forget() error {
	s.op.Lock()
	defer s.op.Unlock()
	if s.pinned {
		return errors.New("credentials are provided by environment variables")
	}
	if s.opts.Mock {
		return errors.New("credentials are not used in mock mode")
	}
	if err := s.store.Forget(); err != nil {
		return err
	}
	s.replace(nil, "")
	return nil
}
func (s *Service) AuthStatus() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{"has_credentials": s.hasCredentials, "email": s.email, "cloud_state": s.state, "cloud_error": s.lastError, "region": "US", "error": s.lastError, "backend": s.backendName(), "source": map[bool]string{true: "env", false: "file"}[s.pinned], "editable": !s.pinned && !s.opts.Mock}
}
func (s *Service) backendName() string {
	if s.opts.Mock {
		return "mock"
	}
	return "cloud"
}
func stamp(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return float64(t.UnixMilli()) / 1000
}
func remaining(t time.Time, now time.Time) any {
	if !t.After(now) {
		return nil
	}
	return t.Sub(now).Seconds()
}
func (s *Service) Snapshot(view string) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	state := s.state
	if now.Before(s.pauseUntil) {
		state = "paused"
	}
	var selected *jackery.Device
	for i := range s.devices {
		d := &s.devices[i]
		if selected == nil || d.ID == view {
			selected = d
		}
		if d.ID == view {
			break
		}
	}
	teleBySN := map[string]any{}
	overview := []map[string]any{}
	for _, d := range s.devices {
		e := s.entries[d.SN]
		var tele map[string]any
		var ts, age, httpAge any
		if e != nil {
			tele = decoratedTelemetry(e, d.ModelCode)
			ts = stamp(e.TS)
			age = now.Sub(e.TS).Seconds()
			if !e.HTTPAt.IsZero() {
				httpAge = now.Sub(e.HTTPAt).Seconds()
			}
		}
		teleBySN[d.SN] = map[string]any{"telemetry": tele, "ts": ts}
		overview = append(overview, map[string]any{"device_id": d.ID, "device_sn": d.SN, "name": d.Name, "model_code": d.ModelCode, "model_name": d.ModelName, "soc_pct": tele["system_soc_pct"], "pack_count": packCount(e), "solar_w": tele["solar_input_w"], "ac_input_w": tele["ac_input_w"], "output_w": tele["output_power_w"], "ac_on": tele["ac_on"], "battery_status": tele["battery_status"], "age_s": age, "http_age_s": httpAge})
	}
	var device, tele any
	packs := []map[string]any{}
	var packsTS any
	packError := ""
	var ts, age, httpAge any
	selectedID := ""
	history := []map[string]any{}
	if selected != nil {
		d := selected
		selectedID = d.ID
		kind := "portable"
		if d.ModelCode == 22 {
			kind = "box"
		}
		device = map[string]any{"name": d.Name, "address": "cloud", "rssi": 0, "model_code": d.ModelCode, "device_sn": d.SN, "device_type": kind}
		if e := s.entries[d.SN]; e != nil {
			tele = decoratedTelemetry(e, d.ModelCode)
			if e.Packs != nil {
				packs = e.Packs
			}
			packsTS = stamp(e.PacksAt)
			packError = e.PackError
			ts = stamp(e.TS)
			age = now.Sub(e.TS).Seconds()
			if !e.HTTPAt.IsZero() {
				httpAge = now.Sub(e.HTTPAt).Seconds()
			}
			history = e.History
		}
	}
	cloud := map[string]any{"state": state, "error": s.lastError, "ts": ts, "age_s": age, "http_age_s": httpAge, "devices": s.devices, "device": device, "selected_device_id": selectedID, "devices_telemetry": teleBySN, "devices_overview": overview, "pause_until": stamp(s.pauseUntil), "pause_remaining_s": remaining(s.pauseUntil, now), "contested_until": stamp(s.contestedUntil), "contested_consecutive": s.contestedConsecutive, "contested_remaining_s": remaining(s.contestedUntil, now), "mqtt_connected": s.mqttConnected, "mqtt_error": s.mqttError}
	result := map[string]any{"connection_status": state, "connection_error": s.lastError, "device": device, "last_update_ts": ts, "telemetry": tele, "battery_packs": packs, "battery_packs_ts": packsTS, "battery_packs_error": packError, "history": history, "mock_mode": s.opts.Mock, "backend": s.backendName(), "low_battery_threshold": s.settings("low_battery_threshold"), "source": s.backendName(), "cloud": cloud, "energy": nil, "inverter_watchdog": nil}
	// Detached JSON trees keep readers from sharing mutable cache maps/slices.
	raw, _ := json.Marshal(result)
	var detached map[string]any
	_ = json.Unmarshal(raw, &detached)
	return detached
}

func packCount(e *entry) int {
	if e == nil {
		return 0
	}
	return len(e.Packs)
}
func decoratedTelemetry(e *entry, code int) map[string]any {
	t := jackery.Telemetry(e.Raw)
	main, pack, _ := catalog.Capacity(code)
	mainSOC := t["battery_percent"].(int)
	sum := float64(mainSOC * main)
	valid := 0
	for _, p := range e.Packs {
		if p["rb"] == nil {
			continue
		}
		raw, _ := json.Marshal(p["rb"])
		var soc float64
		if json.Unmarshal(raw, &soc) != nil || soc < 0 || soc > 100 {
			continue
		}
		sum += soc * float64(pack)
		valid++
	}
	t["main_soc_pct"] = mainSOC
	t["system_soc_pct"] = sum / float64(main+valid*pack)
	t["main_capacity_wh"] = main
	t["pack_capacity_wh"] = pack
	t["capacity_wh"] = main + len(e.Packs)*pack
	return t
}

// HydratePacks seeds last-known pack data before the first cloud pack response.
func (s *Service) HydratePacks(sn string, packs []map[string]any, ts time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.entries[sn]; e != nil && e.PacksAt.IsZero() {
		e.Packs = packs
		e.PacksAt = ts
	}
}
