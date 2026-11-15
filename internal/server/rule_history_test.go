package server

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDurableRuleEdges(t *testing.T) {
	s, _ := testServer(t)
	s.initControllers()
	f := &rescueSwitchFake{}
	s.controllers.engine.Switch = f
	row := map[string]any{"id": "r", "enabled": true, "operator": "<", "value": 20.0, "action": "on", "kasa_host": "fake", "jackery_device_sn": "A"}
	if e := s.Store.Put("automation_rules", "r", "A", row); e != nil {
		t.Fatal(e)
	}
	s.evaluateRules(context.Background(), "B", map[string]any{"battery_percent": 10.0})
	if len(f.hosts) != 0 {
		t.Fatal(f.hosts)
	}
	s.evaluateRules(context.Background(), "A", map[string]any{"battery_percent": 10.0})
	restarted := &Server{Store: s.Store}
	restarted.initControllers()
	restarted.controllers.engine.Switch = f
	restarted.evaluateRules(context.Background(), "A", map[string]any{"battery_percent": 10.0})
	if len(f.hosts) != 1 {
		t.Fatal(f.hosts)
	}
	restarted.evaluateRules(context.Background(), "A", map[string]any{"battery_percent": 50.0})
	restarted.evaluateRules(context.Background(), "A", map[string]any{"battery_percent": 10.0})
	if len(f.hosts) != 2 {
		t.Fatal(f.hosts)
	}
}
func TestRuleIntervals(t *testing.T) {
	rows := []map[string]any{{"action": "off", "fired_at": 1.0}, {"action": "on", "fired_at": 2.0, "rule_id": "a"}, {"action": "on", "fired_at": 3.0}, {"action": "off", "fired_at": 5.0, "rule_id": "b"}, {"action": "on", "fired_at": 8.0}}
	got := pairRuleFirings(rows, 10)
	if len(got) != 2 || got[0]["duration_s"] != 3.0 || got[0]["closed_by_rule_id"] != "b" || got[1]["open"] != true || got[1]["duration_s"] != 2.0 {
		t.Fatal(got)
	}
}
func TestRuleHTTPContracts(t *testing.T) {
	_, h := testServer(t)
	w := request(h, "POST", "/api/auth/setup", `{"username":"admin","password":"secure-pass"}`, "")
	var auth map[string]any
	json.Unmarshal(w.Body.Bytes(), &auth)
	token := auth["token"].(string)
	w = request(h, "POST", "/api/automation/rules", `{"id":"r","operator":"<","value":20,"action":"on","kasa_host":"fake"}`, token)
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 200 || body["ok"] != true || body["rule"] == nil {
		t.Fatal(w.Body.String())
	}
	for _, payload := range []string{`{"rule_ids":["r","r","missing"]}`, `{"rule_ids":[]}`} {
		w = request(h, "POST", "/api/automation/rules/disable", payload, token)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	w = request(h, "GET", "/api/automation/rules/r/history?days=999", "", token)
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 200 || body["rule_exists"] != true || body["days"] != 365.0 {
		t.Fatal(w.Body.String())
	}
	w = request(h, "DELETE", "/api/automation/rules/missing", "", token)
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 200 || body["deleted"] != false {
		t.Fatal(w.Body.String())
	}
}
