package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/compdani/jackery-monitor/internal/automation"
)

func TestSmartPersistedExecutionAndUnresolvedIntent(t *testing.T) {
	s, _ := testServer(t)
	f := &rescueSwitchFake{}
	cfg := automation.NormalizeSmart(map[string]any{"mode": "active", "kasa_device_host": "fake"})
	plan := automation.ChargePlan{Action: "on", DecidedAt: 1700000000}
	if err := s.runSmart(context.Background(), "sn", cfg, plan, f); err != nil {
		t.Fatal(err)
	}
	raw, err := s.Store.Get("settings", "smart_charge_state:sn")
	if err != nil || raw["pending"] != false {
		t.Fatal(raw, err)
	}
	rows, err := s.Store.List("automation_decisions", "sn", 0, 0)
	if err != nil || len(rows) != 1 || rows[0]["executed"] != true || rows[0]["controller"] != "smart_charge" {
		t.Fatal(rows, err)
	}
	if err = s.Store.Put("settings", "smart_charge_state:sn", "sn", map[string]any{"pending": true}); err != nil {
		t.Fatal(err)
	}
	restarted := &Server{Store: s.Store}
	if err = restarted.runSmart(context.Background(), "sn", cfg, plan, f); err == nil || len(f.hosts) != 1 {
		t.Fatal(err, f.hosts)
	}
}

func TestSmartConfigImportedDeviceAndLegacy(t *testing.T) {
	s, _ := testServer(t)
	if err := s.Store.Put("settings", "smart_charge", "", map[string]any{"by_device": map[string]any{"one": map[string]any{"mode": "test", "max_charge_w": 1200}}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.smartConfig("one")
	if err != nil || cfg.Mode != "test" || cfg.MaxChargeW != 1200 {
		t.Fatal(cfg, err)
	}
	cfg, err = s.smartConfig("two")
	if err != nil || cfg.Mode != "off" {
		t.Fatal(cfg, err)
	}
}

func TestSmartConfigHTTP(t *testing.T) {
	s, h := testServer(t)
	if w := request(h, "GET", "/api/smart_charge/config", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(h, "POST", "/api/auth/setup", `{"username":"admin","password":"secure-pass"}`, "")
	var auth map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &auth); err != nil {
		t.Fatal(err)
	}
	token := auth["token"].(string)
	rescueRule(t, s, "same", "sn", "plug", "on", true)
	rescueRule(t, s, "other", "elsewhere", "plug", "on", true)
	w = request(h, "POST", "/api/smart_charge/config?device_sn=sn", `{"mode":"test","kasa_device_host":"plug","target_sunrise_soc_pct":30}`, token)
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	cfg := result["config"].(map[string]any)
	if cfg["mode"] != "test" || cfg["target_sunrise_soc_pct"] != 30.0 || len(result["conflicting_rules"].([]any)) != 1 {
		t.Fatal(result)
	}
	w = request(h, "GET", "/api/smart_charge/config?device_sn=other", "", token)
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["config"].(map[string]any)["kasa_device_host"] != nil {
		t.Fatal(result)
	}
}
