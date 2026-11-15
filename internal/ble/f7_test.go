package ble

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func f7cfg(mut func(*F7Config)) F7Config {
	c := F7Config{Enabled: true, ErrorCode: 8, CooldownMin: 30, PulseS: 5}
	if mut != nil {
		mut(&c)
	}
	return c
}

func f7tele(mut func(*F7Telemetry)) *F7Telemetry {
	t := &F7Telemetry{ACOn: false, ErrorCode: 8}
	if mut != nil {
		mut(t)
	}
	return t
}

func TestF7EvaluateTable(t *testing.T) {
	now := 1_700_000_000.0
	cases := []struct {
		name     string
		cfg      F7Config
		tele     *F7Telemetry
		now      float64
		daylight bool
		want     F7Action
	}{
		{"disabled", f7cfg(func(c *F7Config) { c.Enabled = false }), f7tele(nil), 0, true, F7SkipDisabled},
		{"skips when ac already on", f7cfg(nil), f7tele(func(x *F7Telemetry) { x.ACOn = true }), 0, true, F7SkipACOn},
		{"idle wrong error code", f7cfg(nil), f7tele(func(x *F7Telemetry) { x.ErrorCode = 0 }), 0, true, F7Idle},
		{"skips night", f7cfg(nil), f7tele(nil), 0, false, F7SkipNight},
		{"skips cooldown", f7cfg(func(c *F7Config) { c.LastCycleTS = now - 60 }), f7tele(nil), now, true, F7SkipCooldown},
		{"cycle when f7 daylight ac off", f7cfg(func(c *F7Config) { c.LastCycleTS = now - 31*60 }), f7tele(nil), now, true, F7Cycle},
		{"nil telemetry is idle", f7cfg(nil), nil, 0, true, F7Idle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateF7(tc.cfg, tc.tele, tc.now, tc.daylight); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestInWeatherDaylightGHI(t *testing.T) {
	now := int64(1_700_000_000)
	hour := now - now%3600
	if !InWeatherDaylight(float64(now), []WeatherHour{{TS: hour, GhiWM2: 12}}) {
		t.Error("ghi 12 → daylight")
	}
	if InWeatherDaylight(float64(now), []WeatherHour{{TS: hour, GhiWM2: 0}}) {
		t.Error("ghi 0 → dark")
	}
	if InWeatherDaylight(float64(now), nil) {
		t.Error("empty → dark")
	}
	if InWeatherDaylight(float64(now), []WeatherHour{{TS: hour + 3*3600, GhiWM2: 500}}) {
		t.Error("far hour → dark")
	}
}

func TestManualDaylightWindow(t *testing.T) {
	noon := float64(time.Date(2024, 7, 1, 12, 0, 0, 0, time.UTC).Unix())
	night := float64(time.Date(2024, 7, 1, 2, 0, 0, 0, time.UTC).Unix())
	if r := InManualDaylight(noon, sp("06:00"), sp("20:00"), 0); r == nil || !*r {
		t.Error("noon should be daylight")
	}
	if r := InManualDaylight(night, sp("06:00"), sp("20:00"), 0); r == nil || *r {
		t.Error("02:00 should be night")
	}
	if InManualDaylight(noon, nil, nil, 0) != nil {
		t.Error("unset → nil")
	}
}

func TestIsDaylightManualOverridesWeather(t *testing.T) {
	ts := float64(time.Date(2024, 7, 1, 2, 0, 0, 0, time.UTC).Unix())
	cfg := f7cfg(func(c *F7Config) { c.DayStart, c.DayEnd = sp("06:00"), sp("20:00") })
	hour := int64(ts) - int64(ts)%3600
	// Weather claims sun; manual night window wins.
	if IsDaylight(ts, cfg, 0, []WeatherHour{{TS: hour, GhiWM2: 800}}) {
		t.Fatal("manual window must win")
	}
}

func TestF7ConfigRoundtripAndEvents(t *testing.T) {
	f := NewF7(F7FileStore{Path: filepath.Join(t.TempDir(), "f7.json")})
	saved, err := f.SetConfig(map[string]any{"enabled": true, "cooldown_min": 15}, "SN1")
	if err != nil || !saved.Enabled || saved.CooldownMin != 15 {
		t.Fatalf("%+v %v", saved, err)
	}
	if got := f.GetConfig("SN1"); !got.Enabled {
		t.Fatal("not persisted")
	}
	f.StampCycle("SN1", 123.0)
	f.RecordEvent("SN1", "warn", "F7 AC reset pulse started", map[string]any{"error_code": 8})
	got := f.GetConfig("SN1")
	if got.LastCycleTS != 123.0 || len(got.Events) == 0 || got.Events[len(got.Events)-1].Category != "f7" {
		t.Fatalf("%+v", got)
	}
	// UI save must not wipe history
	f.SetConfig(map[string]any{"enabled": true, "cooldown_min": 20}, "SN1")
	got = f.GetConfig("SN1")
	if got.LastCycleTS != 123.0 || len(got.Events) == 0 {
		t.Fatalf("history lost: %+v", got)
	}
	listed := f.ListEvents(0, 200)
	found := false
	for _, e := range listed {
		if e.Message == "F7 AC reset pulse started" {
			found = true
		}
	}
	if !found {
		t.Fatalf("listed = %+v", listed)
	}
	// survives a fresh load from disk
	f2 := NewF7(F7FileStore{Path: f.store.(F7FileStore).Path})
	if g := f2.GetConfig("SN1"); g.LastCycleTS != 123.0 || g.CooldownMin != 20 || len(g.Events) == 0 {
		t.Fatalf("reload %+v", g)
	}
	if _, err := f.SetConfig(nil, ""); err == nil {
		t.Fatal("device_sn required")
	}
}

func TestF7ValidationClamps(t *testing.T) {
	c := validateConfig(map[string]any{"error_code": 5000, "cooldown_min": 0, "pulse_s": 61,
		"day_start": "6:00", "day_end": "bad"})
	if c.ErrorCode != 8 || c.CooldownMin != 30 || c.PulseS != 5 || c.DayStart != nil || c.DayEnd != nil {
		t.Fatalf("%+v", c)
	}
	c = validateConfig(map[string]any{"day_start": "6:00", "day_end": "20:5", "pulse_s": 10})
	if c.DayStart == nil || *c.DayStart != "06:00" || *c.DayEnd != "20:05" || c.PulseS != 10 {
		t.Fatalf("%+v", c)
	}
}

func TestF7EventLogCapped(t *testing.T) {
	f := NewF7(nil)
	f.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	for i := 0; i < F7EventsMax+10; i++ {
		f.RecordEvent("SN", "bogus-level", "m", nil)
	}
	got := f.GetConfig("SN")
	if len(got.Events) != F7EventsMax || got.Events[0].Level != "info" {
		t.Fatalf("len=%d level=%s", len(got.Events), got.Events[0].Level)
	}
}
