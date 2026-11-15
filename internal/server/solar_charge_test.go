package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/compdani/jackery-monitor/internal/automation"
	"testing"
)

func TestSolarDryRunExecutionTripAndRestart(t *testing.T) {
	s, _ := testServer(t)
	f := &smartPlugFake{on: true}
	s.SmartPlug = f
	if e := s.Store.Put("settings", "solar_charge", "", map[string]any{"by_device": map[string]any{"sn": map[string]any{"mode": "active", "kasa_device_host": "fake"}}}); e != nil {
		t.Fatal(e)
	}
	now := int64(1700000000)
	p, e := s.evaluateSolar(context.Background(), "sn", now, false)
	if e != nil || p == nil || f.reads != 0 || len(f.hosts) != 0 {
		t.Fatal(p, e, f)
	}
	rows, _ := s.Store.List("automation_decisions", "sn", 0, 0)
	if len(rows) != 0 {
		t.Fatal(rows)
	}
	p, e = s.evaluateSolar(context.Background(), "sn", now, true)
	if e != nil || p.Action != "off" || len(f.hosts) != 1 {
		t.Fatal(p, e, f)
	}
	restarted := &Server{Store: s.Store, SmartPlug: f}
	st, e := restarted.solarState("sn")
	if e != nil || st.On || st.Pending {
		t.Fatal(st, e)
	}
	st.On = true
	st.Pending = false
	if e = s.saveSolar("sn", st); e != nil {
		t.Fatal(e)
	}
	if got := s.solarTelemetry(context.Background(), "sn", map[string]any{"output_power_w": 2200.0}, float64(now+1)); got != 0 {
		t.Fatal(got)
	}
	st, e = restarted.solarState("sn")
	if e != nil || st.Overload != float64(now+1) || st.On {
		t.Fatal(st, e)
	}
	c, _ := s.solarConfig("sn")
	if !s.solarOwnsHost("fake") || automation.NormalizeSolar(c)["mode"] != "active" {
		t.Fatal(c)
	}
}
func TestSolarAPIConfigMergeAndHistory(t *testing.T) {
	_, h := testServer(t)
	if w := request(h, "GET", "/api/solar_charge/status", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(h, "POST", "/api/auth/setup", `{"username":"admin","password":"secure-pass"}`, "")
	var auth map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &auth)
	token := auth["token"].(string)
	for _, body := range []string{`{"mode":"test","car_load_w":1700}`, `{"comfort_high_pct":80}`} {
		w = request(h, "POST", "/api/solar_charge/config?device_sn=sn", body, token)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["config"].(map[string]any)["car_load_w"] != 1700.0 {
		t.Fatal(out)
	}
	for _, path := range []string{"status", "history", "config"} {
		w = request(h, "GET", "/api/solar_charge/"+path+"?device_sn=sn", "", token)
		if w.Code != 200 {
			t.Fatal(path, w.Body.String())
		}
	}
	w = request(h, "POST", "/api/solar_charge/evaluate_now?device_sn=sn", `{}`, token)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

type solarFailPlug struct{ smartPlugFake }

func (f *solarFailPlug) SetPower(context.Context, string, bool) error {
	return fmt.Errorf("synthetic uncertain write")
}
func TestSolarFailedIntentAndNoLoadCooldown(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	cfg := automation.NormalizeSolar(map[string]any{"mode": "active", "kasa_device_host": "fake"})
	f := &solarFailPlug{}
	s.SmartPlug = f
	st := automation.SolarState{}
	if e := s.solarCommand(ctx, "sn", cfg, &st, true, 1700000000, 80); e == nil {
		t.Fatal("expected failure")
	}
	saved, e := s.solarState("sn")
	if e != nil || !saved.Pending || saved.On {
		t.Fatal(saved, e)
	}
	// A fresh relay read reconciles an uncertain command; no-load OFF wins over min hold.
	plug := &smartPlugFake{on: true}
	s.SmartPlug = plug
	now := int64(1700000100)
	baseline := 80.0
	st = automation.SolarState{On: true, Toggle: float64(now - 1), Baseline: &baseline, Verify: float64(now - 1)}
	if e = s.saveSolar("sn", st); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.Put("settings", "solar_charge", "", cfg); e != nil {
		t.Fatal(e)
	}
	packet := map[string]any{"device": map[string]any{"device_sn": "sn"}, "last_update_ts": float64(now), "telemetry": map[string]any{"battery_percent": 90.0, "output_power_w": 100.0, "solar_input_w": 500.0}}
	s.persistObservation(ctx, packet)
	p, e := s.evaluateSolar(ctx, "sn", now, true)
	if e != nil || p.Action != "off" {
		t.Fatal(p, e)
	}
	st, e = s.solarState("sn")
	if e != nil || st.On || st.Cooldown != float64(now+900) || st.Pending {
		t.Fatal(st, e)
	}
}
