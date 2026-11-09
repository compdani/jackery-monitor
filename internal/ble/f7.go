package ble

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Per-device F7 (ec=8) AC-pulse workaround for Explorer 2000 Plus.
//
// Morning low-PV can latch the solar inverter. A short AC pulse (ON → 5s →
// OFF) clears it. Opt-in per serial number; never interrupts an inverter
// that is already on. Daylight is clock-based (manual window or weather
// GHI), not live solar watts — the fault happens at low/zero PV.

// F7ErrorCode is the F7 fault code (ec=8).
const F7ErrorCode = 8

// F7EventsMax caps the persisted per-device event log.
const F7EventsMax = 50

// F7Action is the outcome of the pure gate.
type F7Action string

const (
	F7Idle         F7Action = "idle"
	F7Cycle        F7Action = "cycle"
	F7SkipDisabled F7Action = "skip_disabled"
	F7SkipACOn     F7Action = "skip_ac_on"
	F7SkipNight    F7Action = "skip_night"
	F7SkipCooldown F7Action = "skip_cooldown"
)

// F7Event is one dashboard-shaped event.
type F7Event struct {
	TS       float64        `json:"ts"`
	Level    string         `json:"level"`
	Category string         `json:"category"`
	Message  string         `json:"message"`
	Extra    map[string]any `json:"extra,omitempty"`
}

// F7Config is the per-device blob: settings plus runtime fields.
type F7Config struct {
	Enabled     bool      `json:"enabled"`
	ErrorCode   int       `json:"error_code"`
	CooldownMin int       `json:"cooldown_min"`
	PulseS      int       `json:"pulse_s"`
	DayStart    *string   `json:"day_start"`
	DayEnd      *string   `json:"day_end"`
	LastCycleTS float64   `json:"last_cycle_ts"`
	Events      []F7Event `json:"events"`
}

// DefaultF7Config are the defaults.
func DefaultF7Config() F7Config {
	return F7Config{ErrorCode: F7ErrorCode, CooldownMin: 30, PulseS: 5, Events: []F7Event{}}
}

// F7Data is the persisted document: {"by_device": {SN: F7Config}}.
type F7Data struct {
	ByDevice map[string]F7Config `json:"by_device"`
}

// F7Store persists the config.
type F7Store interface {
	Load() (F7Data, error)
	Save(F7Data) error
}

// F7FileStore is a JSON file store matching /data/f7_ac_reset.json.
type F7FileStore struct{ Path string }

// Load reads the file; missing or unreadable-as-object is empty.
func (s F7FileStore) Load() (F7Data, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return F7Data{}, nil
	}
	if err != nil {
		return F7Data{}, err
	}
	// Tolerate loosely-typed rows by round-tripping through the validator.
	var raw struct {
		ByDevice map[string]map[string]any `json:"by_device"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return F7Data{}, err
	}
	d := F7Data{ByDevice: map[string]F7Config{}}
	for sn, row := range raw.ByDevice {
		d.ByDevice[sn] = deviceBlob(row)
	}
	return d, nil
}

// Save writes atomically.
func (s F7FileStore) Save(d F7Data) error {
	if d.ByDevice == nil {
		d.ByDevice = map[string]F7Config{}
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// ---- time helpers (duplicated from load_schedule on purpose: this package
// has no dependency on other internal packages) ----

func parseHHMM(v any) (int, bool) {
	s := strings.TrimSpace(fmt.Sprint(v))
	if v == nil || s == "" || s == "<nil>" {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, false
	}
	hh, e1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	mm, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if e1 != nil || e2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

func formatHHMM(min int) string {
	min = ((min % 1440) + 1440) % 1440
	return fmt.Sprintf("%02d:%02d", min/60, min%60)
}

// inWindow is true if minute is inside [start, end), wrapping midnight.
func inWindow(minute int, start, end string) bool {
	a, okA := parseHHMM(start)
	b, okB := parseHHMM(end)
	if !okA || !okB || a == b {
		return false
	}
	m := ((minute % 1440) + 1440) % 1440
	if a < b {
		return a <= m && m < b
	}
	return m >= a || m < b
}

func hhmmOrNil(v any) *string {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok && s == "" {
		return nil
	}
	if p, ok := v.(*string); ok {
		if p == nil {
			return nil
		}
		v = *p
	}
	m, ok := parseHHMM(v)
	if !ok {
		return nil
	}
	s := formatHHMM(m)
	return &s
}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case bool:
		return 0, false
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(x))
		return i, err == nil
	}
	return 0, false
}

func truthyAny(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return true
}

// validateConfig sanitises the settings fields of a loosely-typed dict.
func validateConfig(cfg map[string]any) F7Config {
	out := DefaultF7Config()
	if cfg == nil {
		return out
	}
	out.Enabled = truthyAny(cfg["enabled"])
	if v, ok := cfg["error_code"]; ok && v != nil {
		if ec, ok := asInt(v); ok && ec >= 1 && ec <= 999 {
			out.ErrorCode = ec
		}
	}
	if v, ok := cfg["cooldown_min"]; ok && v != nil {
		if cd, ok := asInt(v); ok && cd >= 1 && cd <= 24*60 {
			out.CooldownMin = cd
		}
	}
	if v, ok := cfg["pulse_s"]; ok && v != nil {
		if p, ok := asInt(v); ok && p >= 1 && p <= 60 {
			out.PulseS = p
		}
	}
	out.DayStart = hhmmOrNil(cfg["day_start"])
	out.DayEnd = hhmmOrNil(cfg["day_end"])
	if out.DayStart == nil || out.DayEnd == nil {
		out.DayStart, out.DayEnd = nil, nil
	}
	return out
}

func runtimeFields(raw map[string]any) (float64, []F7Event) {
	var last float64
	switch x := raw["last_cycle_ts"].(type) {
	case float64:
		last = x
	case int:
		last = float64(x)
	}
	var events []F7Event
	if evs, ok := raw["events"].([]any); ok {
		start := 0
		if len(evs) > F7EventsMax {
			start = len(evs) - F7EventsMax
		}
		for _, e := range evs[start:] {
			m, ok := e.(map[string]any)
			if !ok || !truthyAny(m["message"]) {
				continue
			}
			b, _ := json.Marshal(m)
			var ev F7Event
			if json.Unmarshal(b, &ev) == nil {
				events = append(events, ev)
			}
		}
	}
	if events == nil {
		events = []F7Event{}
	}
	return last, events
}

func deviceBlob(raw map[string]any) F7Config {
	cfg := validateConfig(raw)
	if raw != nil {
		cfg.LastCycleTS, cfg.Events = runtimeFields(raw)
	}
	return cfg
}

func configToMap(c F7Config) map[string]any {
	b, _ := json.Marshal(c)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

// F7 holds the per-device config behind a lock.
type F7 struct {
	mu    sync.Mutex
	store F7Store
	mem   F7Data
	Now   func() time.Time
	Log   *slog.Logger
}

// NewF7 wraps a store (nil = memory only).
func NewF7(store F7Store) *F7 { return &F7{store: store, Now: time.Now} }

func (f *F7) logger() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.Default()
}

func (f *F7) load() F7Data {
	var d F7Data
	if f.store != nil {
		d, _ = f.store.Load()
	} else {
		d = f.mem
	}
	if d.ByDevice == nil {
		d.ByDevice = map[string]F7Config{}
	}
	// copy to avoid aliasing memory store
	cp := F7Data{ByDevice: make(map[string]F7Config, len(d.ByDevice))}
	for k, v := range d.ByDevice {
		cp.ByDevice[k] = v
	}
	return cp
}

func (f *F7) save(d F7Data) error {
	if f.store != nil {
		return f.store.Save(d)
	}
	f.mem = d
	return nil
}

// GetConfig returns the device's blob (defaults for unknown/empty SN).
func (f *F7) GetConfig(deviceSN string) F7Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	if deviceSN == "" {
		return DefaultF7Config()
	}
	d := f.load()
	if c, ok := d.ByDevice[deviceSN]; ok {
		return c
	}
	return DefaultF7Config()
}

// SetConfig merges UI fields onto the stored record, keeping last_cycle_ts
// and events. Unknown/absent keys keep their previous value.
func (f *F7) SetConfig(patch map[string]any, deviceSN string) (F7Config, error) {
	if deviceSN == "" {
		return F7Config{}, errors.New("device_sn required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.load()
	prior := d.ByDevice[deviceSN]
	if _, ok := d.ByDevice[deviceSN]; !ok {
		prior = DefaultF7Config()
	}
	merged := configToMap(prior)
	for k, v := range patch {
		merged[k] = v
	}
	validated := validateConfig(merged)
	validated.LastCycleTS, validated.Events = prior.LastCycleTS, prior.Events
	if validated.Events == nil {
		validated.Events = []F7Event{}
	}
	d.ByDevice[deviceSN] = validated
	if err := f.save(d); err != nil {
		return validated, err
	}
	f.logger().Info("f7_ac_reset config saved", "device", deviceSN, "enabled", validated.Enabled, "cooldown_min", validated.CooldownMin)
	return validated, nil
}

// StampCycle records the last pulse time (zero ts = now).
func (f *F7) StampCycle(deviceSN string, ts float64) error {
	if ts == 0 {
		ts = float64(f.Now().UnixNano()) / 1e9
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.load()
	rec, ok := d.ByDevice[deviceSN]
	if !ok {
		rec = DefaultF7Config()
	}
	rec.LastCycleTS = ts
	d.ByDevice[deviceSN] = rec
	return f.save(d)
}

// RecordEvent appends a dashboard-shaped event and mirrors it to the logger.
func (f *F7) RecordEvent(deviceSN, level, message string, extra map[string]any) (F7Event, error) {
	if level != "info" && level != "warn" && level != "error" {
		level = "info"
	}
	payload := map[string]any{"device_sn": deviceSN}
	for k, v := range extra {
		payload[k] = v
	}
	e := F7Event{
		TS: float64(f.Now().UnixNano()) / 1e9, Level: level, Category: "f7",
		Message: message, Extra: payload,
	}
	f.mu.Lock()
	d := f.load()
	rec, ok := d.ByDevice[deviceSN]
	if !ok {
		rec = DefaultF7Config()
	}
	events := append(append([]F7Event(nil), rec.Events...), e)
	if len(events) > F7EventsMax {
		events = events[len(events)-F7EventsMax:]
	}
	rec.Events = events
	d.ByDevice[deviceSN] = rec
	err := f.save(d)
	f.mu.Unlock()
	switch level {
	case "error":
		f.logger().Error(message, "category", e.Category, "extra", payload)
	case "warn":
		f.logger().Warn(message, "category", e.Category, "extra", payload)
	default:
		f.logger().Info(message, "category", e.Category, "extra", payload)
	}
	return e, err
}

// ListEvents returns persisted events across devices, oldest first, trimmed
// to the last limit (0 = unlimited). since filters ts <= since out.
func (f *F7) ListEvents(since float64, limit int) []F7Event {
	f.mu.Lock()
	d := f.load()
	f.mu.Unlock()
	var out []F7Event
	for _, rec := range d.ByDevice {
		for _, e := range rec.Events {
			if since != 0 && e.TS <= since {
				continue
			}
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	if out == nil {
		out = []F7Event{}
	}
	return out
}

// InManualDaylight is a tri-state: true/false when both HH:MM are set, nil
// means fall through to weather.
func InManualDaylight(nowTS float64, dayStart, dayEnd *string, tzOffsetS int) *bool {
	if dayStart == nil || dayEnd == nil || *dayStart == "" || *dayEnd == "" {
		return nil
	}
	local := int64(nowTS) + int64(tzOffsetS)
	minute := int(((local % 86400) + 86400) % 86400 / 60)
	r := inWindow(minute, *dayStart, *dayEnd)
	return &r
}

// WeatherHour is one hourly weather row.
type WeatherHour struct {
	TS     int64   `json:"ts"`
	GhiWM2 float64 `json:"ghi_w_m2"`
}

// InWeatherDaylight is true if the current UTC hour has GHI > 0. It fails
// closed if unknown.
func InWeatherDaylight(nowTS float64, hourly []WeatherHour) bool {
	if len(hourly) == 0 {
		return false
	}
	hourTS := int64(nowTS) - int64(nowTS)%3600
	var best *WeatherHour
	var bestDT int64 = -1
	for i := range hourly {
		dt := hourly[i].TS - hourTS
		if dt < 0 {
			dt = -dt
		}
		if bestDT < 0 || dt < bestDT {
			bestDT, best = dt, &hourly[i]
		}
	}
	if best == nil || bestDT > 3600 {
		return false
	}
	return best.GhiWM2 > 0
}

// IsDaylight prefers the manual window, else weather GHI.
func IsDaylight(nowTS float64, cfg F7Config, tzOffsetS int, hourly []WeatherHour) bool {
	if m := InManualDaylight(nowTS, cfg.DayStart, cfg.DayEnd, tzOffsetS); m != nil {
		return *m
	}
	return InWeatherDaylight(nowTS, hourly)
}

// F7Telemetry is the subset of telemetry the gate reads.
type F7Telemetry struct {
	ACOn      bool
	ErrorCode int
}

// EvaluateF7 is the pure gate. It does not mutate cfg or issue commands.
// nowTS zero means time.Now().
func EvaluateF7(cfg F7Config, tele *F7Telemetry, nowTS float64, inDaylight bool) F7Action {
	if nowTS == 0 {
		nowTS = float64(time.Now().UnixNano()) / 1e9
	}
	if !cfg.Enabled {
		return F7SkipDisabled
	}
	var t F7Telemetry
	if tele != nil {
		t = *tele
	}
	if t.ACOn {
		return F7SkipACOn
	}
	want := cfg.ErrorCode
	if want == 0 {
		want = F7ErrorCode
	}
	if t.ErrorCode != want {
		return F7Idle
	}
	if !inDaylight {
		return F7SkipNight
	}
	cooldown := cfg.CooldownMin
	if cooldown == 0 {
		cooldown = 30
	}
	if cfg.LastCycleTS != 0 && (nowTS-cfg.LastCycleTS) < float64(cooldown*60) {
		return F7SkipCooldown
	}
	return F7Cycle
}
