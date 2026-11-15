package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestF7ReferenceGates(t *testing.T) {
	now := 1700000000.0
	for _, tc := range []struct {
		name        string
		patch, tele map[string]any
		day         bool
		want        string
	}{
		{"disabled", map[string]any{"enabled": false}, nil, true, "skip_disabled"},
		{"on", nil, map[string]any{"ac_on": true, "error_code": 8}, true, "skip_ac_on"},
		{"wrong_error", nil, map[string]any{"error_code": 0}, true, "idle"},
		{"night", nil, nil, false, "skip_night"},
		{"cooldown", map[string]any{"last_cycle_ts": now - 60}, nil, true, "skip_cooldown"},
		{"cycle", map[string]any{"last_cycle_ts": now - 31*60}, nil, true, "cycle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := f7Config(map[string]any{"enabled": true})
			for k, v := range tc.patch {
				cfg[k] = v
			}
			tele := tc.tele
			if tele == nil {
				tele = map[string]any{"ac_on": false, "error_code": 8}
			}
			if got := f7Evaluate(cfg, tele, now, tc.day); got != tc.want {
				t.Fatal(got, tc.want)
			}
		})
	}
}
func TestF7DaylightReference(t *testing.T) {
	s, _ := testServer(t)
	now := 1700000000.0
	hour := float64(int64(now) / 3600 * 3600)
	cfg := f7Config(nil)
	if s.f7Daylight(cfg, now) {
		t.Fatal("unknown weather must fail closed")
	}
	for _, ghi := range []float64{12, 0} {
		if err := s.Store.Put("weather_forecasts", "hour", "", map[string]any{"ts": hour, "ghi_w_m2": ghi}); err != nil {
			t.Fatal(err)
		}
		if s.f7Daylight(cfg, now) != (ghi > 0) {
			t.Fatal(ghi)
		}
	}
	noon := float64(time.Date(2024, 7, 1, 12, 0, 0, 0, time.UTC).Unix())
	cfg = f7Config(map[string]any{"day_start": "06:00", "day_end": "20:00"})
	if !s.f7Daylight(cfg, noon) || s.f7Daylight(cfg, noon-10*3600) {
		t.Fatal("manual window")
	}
	night := noon - 10*3600
	if err := s.Store.Put("weather_forecasts", "night", "", map[string]any{"ts": night, "ghi_w_m2": 800}); err != nil {
		t.Fatal(err)
	}
	if s.f7Daylight(cfg, night) {
		t.Fatal("weather overrode manual night")
	}
	cfg = f7Config(map[string]any{"day_start": "20:00", "day_end": "06:00"})
	if !s.f7Daylight(cfg, night) || s.f7Daylight(cfg, noon) {
		t.Fatal("overnight window")
	}
	cfg = f7Config(map[string]any{"pulse_s": 61, "cooldown_min": 0, "error_code": 1000, "day_start": "25:00", "day_end": "06:00"})
	if cfg["pulse_s"] != 5 || cfg["cooldown_min"] != 30 || cfg["error_code"] != 8 || cfg["day_end"] != nil {
		t.Fatal(cfg)
	}
}
func TestF7ConfigEventsContract(t *testing.T) {
	s, h := testServer(t)
	s.Bridge = f7Bridge{}
	if w := request(h, "GET", "/api/f7_ac_reset/config", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(h, "POST", "/api/auth/setup", `{"username":"admin","password":"secure-pass"}`, "")
	var auth map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &auth); err != nil {
		t.Fatal(err)
	}
	token := auth["token"].(string)
	w = request(h, "POST", "/api/f7_ac_reset/config?device_sn=SN", `{"enabled":true,"cooldown_min":15}`, token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cfg, err := s.f7Config("SN")
	if err != nil {
		t.Fatal(err)
	}
	cfg["last_cycle_ts"] = 123.0
	if err = s.f7Save("SN", cfg); err != nil {
		t.Fatal(err)
	}
	if err = s.f7Event("SN", "warn", "F7 AC reset pulse started", 8, 5); err != nil {
		t.Fatal(err)
	}
	w = request(h, "POST", "/api/f7_ac_reset/config?device_sn=SN", `{"cooldown_min":20,"last_cycle_ts":0,"events":[]}`, token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(h, "GET", "/api/f7_ac_reset/config?device_sn=SN", "", token)
	var out map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	cfg = out["config"].(map[string]any)
	if cfg["enabled"] != true || cfg["cooldown_min"] != 20.0 || cfg["last_cycle_ts"] != 123.0 || len(out["events"].([]any)) != 1 || out["in_daylight"] != false {
		t.Fatal(out)
	}
	events, err := s.f7Events(0)
	if err != nil || len(events) != 1 || events[0]["category"] != "f7" {
		t.Fatal(events, err)
	}
	w = request(h, "GET", "/api/events?since=0&limit=1", "", token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out["events"].([]any)) != 1 {
		t.Fatal(out)
	}
	// Restart and event cap.
	restarted := &Server{Store: s.Store}
	cfg, err = restarted.f7Config("SN")
	if err != nil || cfg["last_cycle_ts"] != 123.0 {
		t.Fatal(cfg, err)
	}
	for i := 0; i < 55; i++ {
		if err = s.f7Event("SN", "info", "event", 8, 5); err != nil {
			t.Fatal(err)
		}
	}
	events, err = s.f7Events(0)
	if err != nil || len(events) != 50 {
		t.Fatal(len(events), err)
	}
	if events, err = s.f7Events(float64(time.Now().Unix() + 10)); err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
}

type f7Bridge struct {
	calls chan bool
	fail  bool
}

func (b f7Bridge) Call(ctx context.Context, method string, p map[string]any) (map[string]any, error) {
	if method == "get_events" {
		return map[string]any{"ok": true, "events": []any{}}, nil
	}
	if b.fail {
		return nil, errors.New("fake failure")
	}
	select {
	case b.calls <- p["on"].(bool):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return map[string]any{"ok": true}, nil
}
func TestF7LivePulseAndInflightSuppression(t *testing.T) {
	s, _ := testServer(t)
	calls := make(chan bool, 10)
	s.Bridge = f7Bridge{calls: calls}
	release := make(chan struct{})
	waiting := make(chan struct{})
	s.f7.wait = func(ctx context.Context, d time.Duration) error {
		if d != 5*time.Second {
			t.Errorf("duration %v", d)
		}
		close(waiting)
		<-release
		return nil
	}
	// A window encompassing the current clock, independent of weather.
	now := time.Now().UTC()
	start := now.Add(-time.Hour).Format("15:04")
	end := now.Add(time.Hour).Format("15:04")
	if err := s.f7Save("SN", f7Config(map[string]any{"enabled": true, "day_start": start, "day_end": end})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tele := map[string]any{"ac_on": false, "error_code": 8}
	s.f7Tick(ctx, "SN", tele)
	select {
	case on := <-calls:
		if !on {
			t.Fatal("expected ON")
		}
	case <-time.After(time.Second):
		t.Fatal("no ON")
	}
	<-waiting
	// Remove cooldown to exercise in-flight guard itself.
	s.f7.mu.Lock()
	cfg, err := s.f7Config("SN")
	if err != nil {
		t.Fatal(err)
	}
	cfg["last_cycle_ts"] = 0.0
	if err = s.f7Save("SN", cfg); err != nil {
		t.Fatal(err)
	}
	s.f7.mu.Unlock()
	s.f7Tick(ctx, "SN", tele)
	if len(calls) != 0 {
		t.Fatal("duplicate pulse")
	}
	close(release)
	select {
	case on := <-calls:
		if on {
			t.Fatal("expected OFF")
		}
	case <-time.After(time.Second):
		t.Fatal("no OFF")
	}
	// Wait for terminal persistence without leaving a goroutine using the test store.
	deadline := time.After(time.Second)
	for {
		s.f7.mu.Lock()
		running := s.f7.running["SN"]
		s.f7.mu.Unlock()
		if !running {
			break
		}
		select {
		case <-deadline:
			t.Fatal("cycle did not finish")
		case <-time.After(time.Millisecond):
		}
	}
	events, err := s.f7Events(0)
	if err != nil || len(events) != 2 || events[1]["message"] != "F7 AC reset pulse finished" {
		t.Fatal(events, err)
	}
}

func TestF7ImportedConfigAndEvents(t *testing.T) {
	s, _ := testServer(t)
	raw := map[string]any{"by_device": map[string]any{"SN": map[string]any{"enabled": true, "pulse_s": 3, "last_cycle_ts": 123.0, "events": []any{map[string]any{"ts": 10.0, "message": "imported", "category": "f7"}}}}}
	if err := s.Store.Put("settings", "f7_ac_reset", "", raw); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.f7Config("SN")
	if err != nil || cfg["pulse_s"] != 3 || cfg["last_cycle_ts"] != 123.0 {
		t.Fatal(cfg, err)
	}
	events, err := s.f7Events(0)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	cfg["pulse_s"] = 4
	if err = s.f7Save("SN", cfg); err != nil {
		t.Fatal(err)
	}
	events, err = s.f7Events(0)
	if err != nil || len(events) != 1 {
		t.Fatal("duplicate imported events", events, err)
	}
}

func TestF7FailureAndShutdown(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "shutdown", true: "command_failure"}[fail], func(t *testing.T) {
			s, _ := testServer(t)
			calls := make(chan bool, 4)
			s.Bridge = f7Bridge{calls: calls, fail: fail}
			waiting := make(chan struct{})
			s.f7.wait = func(ctx context.Context, d time.Duration) error { close(waiting); <-ctx.Done(); return ctx.Err() }
			now := time.Now().UTC()
			cfg := f7Config(map[string]any{"enabled": true, "day_start": now.Add(-time.Hour).Format("15:04"), "day_end": now.Add(time.Hour).Format("15:04")})
			if err := s.f7Save("SN", cfg); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.f7Tick(ctx, "SN", map[string]any{"error_code": 8, "ac_on": false})
			if !fail {
				select {
				case <-waiting:
				case <-time.After(time.Second):
					t.Fatal("no pulse")
				}
				cancel()
				select {
				case on := <-calls:
					if !on {
						t.Fatal("no ON")
					}
				case <-time.After(time.Second):
					t.Fatal("no command")
				}
				select {
				case on := <-calls:
					if on {
						t.Fatal("no cleanup OFF")
					}
				case <-time.After(time.Second):
					t.Fatal("no cleanup")
				}
			}
			deadline := time.After(time.Second)
			for {
				s.f7.mu.Lock()
				running := s.f7.running["SN"]
				s.f7.mu.Unlock()
				if !running {
					break
				}
				select {
				case <-deadline:
					t.Fatal("not finished")
				case <-time.After(time.Millisecond):
				}
			}
			events, err := s.f7Events(0)
			if err != nil || len(events) != 2 || events[1]["level"] != "error" {
				t.Fatal(events, err)
			}
			saved, err := s.f7Config("SN")
			if err != nil || saved["last_cycle_ts"] == float64(0) {
				t.Fatal(saved, err)
			}
			s.f7Tick(context.Background(), "SN", map[string]any{"error_code": 8, "ac_on": false})
			if len(calls) != 0 {
				t.Fatal("failed pulse bypassed cooldown")
			}
		})
	}
}
