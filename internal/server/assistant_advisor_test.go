package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/compdani/jackery-monitor/internal/assistant"
	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/forecast"
)

// scriptedModel replays recorded-shape provider responses in order and keeps
// every request body for assertions.
type scriptedModel struct {
	mu       sync.Mutex
	replies  []string
	requests []map[string]any
}

func (m *scriptedModel) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.requests = append(m.requests, body)
		if len(m.replies) == 0 {
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Done here."}]}`))
			return
		}
		reply := m.replies[0]
		m.replies = m.replies[1:]
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv
}
func toolUse(id, name string, input map[string]any) string {
	data, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "thinking", "thinking": "…", "signature": "sig"}, map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}})
	return string(data)
}

func TestAssistantToolsApprovalAutopilotAndAttachments(t *testing.T) {
	s, h := testServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	token := contractToken(t, h)
	model := &scriptedModel{}
	s.AnthropicBaseURL = model.server(t).URL
	if err := s.Store.SaveSecret(s.Key, "anthropic", map[string]any{"api_key": "sk-ant-x"}); err != nil {
		t.Fatal(err)
	}
	seedHistory(t, s, "SN", time.Now().Unix())
	conv := contractJSON(t, h, "POST", "/api/assistant/conversations", `{}`, token, 200)["id"].(string)
	contractJSON(t, h, "POST", "/api/assistant/chat", `{"conversation_id":"`+conv+`"}`, token, 400)
	// Read tool runs immediately; a write tool becomes pending and ends the turn.
	model.replies = []string{
		toolUse("t1", "get_charge_status", map[string]any{"device_sn": "SN"}),
		toolUse("t2", "smart_charge_set", map[string]any{"device_sn": "SN", "max_charge_w": 1200}),
	}
	v := contractJSON(t, h, "POST", "/api/assistant/chat", `{"conversation_id":"`+conv+`","message":"Charge faster","tab":"forecast"}`, token, 200)
	pending := v["pending_tools"].([]any)
	if len(pending) != 1 || v["text"] != "I have actions ready — review and Approve or Deny below." {
		t.Fatal(v)
	}
	if len(model.requests) != 2 || !strings.Contains(model.requests[0]["system"].(string), "'forecast' tab") {
		t.Fatal(len(model.requests))
	}
	// The thinking block is echoed back unchanged on the follow-up turn.
	second, _ := json.Marshal(model.requests[1]["messages"])
	if !strings.Contains(string(second), `"signature":"sig"`) || !strings.Contains(string(second), "tool_result") {
		t.Fatal(string(second))
	}
	if c, _ := s.smartConfig("SN"); c.MaxChargeW == 1200 {
		t.Fatal("write executed before approval")
	}
	conversation := contractJSON(t, h, "GET", "/api/assistant/conversations/"+conv, "", token, 200)
	if conversation["title"] != "Charge faster" || len(conversation["tool_runs"].([]any)) != 1 {
		t.Fatal(conversation)
	}
	run := pending[0].(map[string]any)["id"].(string)
	v = contractJSON(t, h, "POST", "/api/assistant/tools/"+run+"/approve", "", token, 200)
	if v["ok"] != true {
		t.Fatal(v)
	}
	if c, _ := s.smartConfig("SN"); c.MaxChargeW != 1200 {
		t.Fatal(c)
	}
	contractJSON(t, h, "POST", "/api/assistant/tools/"+run+"/deny", "", token, 400)
	contractJSON(t, h, "POST", "/api/assistant/tools/nope/approve", "", token, 404)
	// Autopilot executes writes and records an applied tool run.
	model.replies = []string{toolUse("t3", "schedule_upsert", map[string]any{"device_sn": "SN", "start": "18:00", "end": "19:00", "watts": 500, "kind": "weekly", "weekdays": []int{0}}), `{"content":[{"type":"text","text":"Scheduled."}]}`}
	v = contractJSON(t, h, "POST", "/api/assistant/chat", `{"conversation_id":"`+conv+`","message":"add dryer","autopilot":true}`, token, 200)
	if v["text"] != "Scheduled." || len(v["pending_tools"].([]any)) != 0 {
		t.Fatal(v)
	}
	if sched, _ := s.loadSchedule("SN"); len(sched["windows"].([]any)) != 1 || sched["mode"] != "scheduled" {
		t.Fatal(sched)
	}
	// Attachments: type allowlist, image block on the newest user turn.
	upload := func(mime, name string, data []byte) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("conversation_id", conv)
		hdr := map[string][]string{"Content-Disposition": {fmt.Sprintf(`form-data; name="file"; filename=%q`, name)}, "Content-Type": {mime}}
		part, _ := mw.CreatePart(hdr)
		_, _ = part.Write(data)
		_ = mw.Close()
		r := httptest.NewRequest("POST", "/api/assistant/attachments", &buf)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := upload("application/x-msdownload", "x.exe", []byte("MZ")); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := upload("image/png", "big.png", make([]byte, maxAttachmentBytes+1)); w.Code != 400 {
		t.Fatal(w.Code)
	}
	w := upload("image/png", "p.png", []byte("\x89PNG"))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var att map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &att)
	model.requests = nil
	model.replies = []string{`{"content":[{"type":"text","text":"Nice panel."}]}`}
	contractJSON(t, h, "POST", "/api/assistant/chat", `{"conversation_id":"`+conv+`","attachment_ids":["`+att["id"].(string)+`"]}`, token, 200)
	msgs, _ := json.Marshal(model.requests[0]["messages"])
	if !strings.Contains(string(msgs), `"media_type":"image/png"`) {
		t.Fatal(string(msgs))
	}
	if r := request(h, "GET", "/api/assistant/attachments/"+att["id"].(string), "", token); r.Code != 200 || r.Body.String() != "\x89PNG" {
		t.Fatal(r.Code)
	}
	contractJSON(t, h, "DELETE", "/api/assistant/attachments/"+att["id"].(string), "", token, 200)
	contractJSON(t, h, "DELETE", "/api/assistant/attachments/"+att["id"].(string), "", token, 404)
}

// Ported from tests/test_assistant_read_tools.py and test_assistant_store.py.
func TestAssistantReadToolsRedactAndGate(t *testing.T) {
	s, _ := testServer(t)
	seedHistory(t, s, "SN", time.Now().Unix())
	if err := s.Store.SaveSecret(s.Key, "location", map[string]any{"latitude": 37.0, "longitude": -122.0}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"get_tab_snapshot", "get_energy_totals", "list_suggestions", "get_charge_status", "query_samples", "query_weather", "query_predictions", "query_decisions", "query_battery_packs", "get_device_params", "get_energy_daily", "get_daily_summary", "get_cost_plan", "get_location", "get_solar_array", "list_automation_rules", "get_events", "get_load_schedule"} {
		out, err := s.assistantTool(t.Context(), name, map[string]any{"device_sn": "SN", "start_iso": "2020-01-01T00:00:00", "end_iso": "2030-01-01T00:00:00"})
		if err != nil {
			t.Fatal(name, err)
		}
		if _, ok := out.(map[string]any); !ok {
			t.Fatalf("%s: %T", name, out)
		}
	}
	red := redactSecrets(map[string]any{"api_token": "x", "has_credentials": true, "nested": []any{map[string]any{"password": "p", "ok": 1}}, "session_id": false}, 0).(map[string]any)
	if red["api_token"] != "[redacted]" || red["has_credentials"] != true || red["session_id"] != false || red["nested"].([]any)[0].(map[string]any)["password"] != "[redacted]" {
		t.Fatal(red)
	}
	if !assistant.NeedsApproval("smart_charge_set", nil) || assistant.NeedsApproval("fit_device_param", map[string]any{"apply": false}) || !assistant.NeedsApproval("fit_device_param", map[string]any{"apply": true}) || assistant.NeedsApproval("get_forecast", nil) {
		t.Fatal("approval gating")
	}
	names := map[string]bool{}
	for _, tool := range assistant.Tools() {
		names[tool.Name] = true
	}
	for _, n := range []string{"get_device_params", "get_energy_daily", "get_daily_summary", "get_cost_plan", "get_location", "get_solar_array", "list_automation_rules", "get_events", "fit_device_param"} {
		if !names[n] {
			t.Fatal("missing tool", n)
		}
	}
	if !assistant.AllowedMIME("application/pdf") || assistant.AllowedMIME("application/zip") {
		t.Fatal("mime allowlist")
	}
	// fit_device_param preview runs without approval and reports the window.
	out, _ := s.assistantTool(t.Context(), "fit_device_param", map[string]any{"device_sn": "SN", "key": "solar_coefficient", "lookback_hours": 24.0})
	if out.(map[string]any)["applied"] != false || out.(map[string]any)["lookback_hours"] != 24 {
		t.Fatal(out)
	}
	// query_samples validation (tests/test_advisor_query_samples.py).
	if v := s.advisorQuery("SN", "query_samples", map[string]any{}); v["error"] == nil {
		t.Fatal(v)
	}
	if v := s.advisorQuery("SN", "query_samples", map[string]any{"start_iso": "2026-01-02T00:00:00", "end_iso": "2026-01-01T00:00:00"}); v["error"] == nil {
		t.Fatal(v)
	}
	start := time.Now().Add(-6 * time.Hour).UTC().Format("2006-01-02T15:04:05")
	v := s.advisorQuery("SN", "query_samples", map[string]any{"start_iso": start, "end_iso": time.Now().UTC().Format("2006-01-02T15:04:05"), "bucket_s": 30.0})
	if v["bucket_s"] != 60 || v["requested_bucket_s"] != 30 || v["row_count"] == 0 {
		t.Fatal(v)
	}
}

func TestAdvisorReviewApplyDismissAndSchedule(t *testing.T) {
	s, h := testServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	token := contractToken(t, h)
	model := &scriptedModel{}
	s.AnthropicBaseURL = model.server(t).URL
	seedHistory(t, s, "SN", time.Now().Unix())
	if v, _ := s.runReview(t.Context(), "SN"); v["reason"] != "no_api_key" {
		t.Fatal(v)
	}
	if err := s.Store.SaveSecret(s.Key, "anthropic", map[string]any{"api_key": "sk-ant-x"}); err != nil {
		t.Fatal(err)
	}
	submit := map[string]any{"summary": "Load model is fine.", "config_suggestions": []any{
		map[string]any{"target": "smart_charge.max_charge_w", "current_value": 800, "proposed_value": 1500, "reasoning": "more headroom", "confidence": "medium"},
		map[string]any{"target": "smart_charge.max_charge_w", "current_value": 800, "proposed_value": 99999, "reasoning": "out of range", "confidence": "low"},
		map[string]any{"target": "forecaster.k", "current_value": 1, "proposed_value": 2, "reasoning": "not whitelisted", "confidence": "low"},
	}, "anomalies": []any{map[string]any{"description": "Pack 2 drifting", "severity": "warn"}}}
	model.replies = []string{toolUse("q1", "query_samples", map[string]any{"start_iso": "2020-01-01T00:00:00", "end_iso": "2030-01-01T00:00:00"}), toolUse("s1", "submit_algorithm_review", submit)}
	res, err := s.runReview(t.Context(), "SN")
	if err != nil || res["ok"] != true || len(res["new_suggestion_ids"].([]int64)) != 2 || res["tool_calls"] != 2 {
		t.Fatal(res, err)
	}
	first := model.requests[0]
	if first["thinking"] == nil || len(first["tools"].([]any)) != 6 {
		t.Fatal(first["thinking"], first["tools"])
	}
	v := contractJSON(t, h, "GET", "/api/algorithm/suggestions?device_sn=SN", "", token, 200)
	if len(v["suggestions"].([]any)) != 2 {
		t.Fatal(v)
	}
	var cfgID, anomalyID int
	for _, raw := range v["suggestions"].([]any) {
		sg := raw.(map[string]any)
		if sg["kind"] == "config" {
			cfgID = int(sg["id"].(float64))
		} else {
			anomalyID = int(sg["id"].(float64))
		}
	}
	contractJSON(t, h, "POST", fmt.Sprintf("/api/algorithm/suggestions/%d/apply", anomalyID), "", token, 400)
	v = contractJSON(t, h, "POST", fmt.Sprintf("/api/algorithm/suggestions/%d/apply", cfgID), "", token, 200)
	if v["applied"].(map[string]any)["smart_charge.max_charge_w"] != 1500.0 || v["previous"] != 800.0 {
		t.Fatal(v)
	}
	if c, _ := s.smartConfig("SN"); c.MaxChargeW != 1500 {
		t.Fatal(c)
	}
	contractJSON(t, h, "POST", fmt.Sprintf("/api/algorithm/suggestions/%d/apply", cfgID), "", token, 400)
	changes := contractJSON(t, h, "GET", "/api/algorithm/changes?device_sn=SN", "", token, 200)["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["new_value"] != 1500.0 || changes[0].(map[string]any)["suggestion_id"] != float64(cfgID) {
		t.Fatal(changes)
	}
	if v = contractJSON(t, h, "POST", fmt.Sprintf("/api/algorithm/suggestions/%d/dismiss", anomalyID), "", token, 200); v["ok"] != true {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "POST", fmt.Sprintf("/api/algorithm/suggestions/%d/dismiss", anomalyID), "", token, 200); v["already"] != "dismissed" {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/algorithm/suggestions/999/apply", "", token, 404)
	// A suggestion whose value left the whitelist range is rejected at apply time
	// and the transaction leaves config and audit untouched.
	if err := s.Store.Put("suggestions", "suggestion:50", "SN", map[string]any{"id": 50, "device_sn": "SN", "kind": "config", "target": "smart_charge.max_charge_w", "proposed_value": 5000.0, "status": "pending", "created_at": float64(time.Now().Unix())}); err != nil {
		t.Fatal(err)
	}
	contractJSON(t, h, "POST", "/api/algorithm/suggestions/50/apply", "", token, 400)
	if c, _ := s.smartConfig("SN"); c.MaxChargeW != 1500 {
		t.Fatal(c)
	}
	if v = contractJSON(t, h, "GET", "/api/algorithm/suggestions?device_sn=SN&status=all", "", token, 200); len(v["suggestions"].([]any)) != 3 {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "GET", "/api/algorithm/preview?device_sn=SN", "", token, 200); !strings.Contains(v["rendered"].(string), "submit_algorithm_review") || v["provider"] != "anthropic" {
		t.Fatal(v)
	}
	// Background job + polling.
	model.replies = []string{toolUse("s2", "submit_algorithm_review", map[string]any{"summary": "ok", "config_suggestions": []any{}, "anomalies": []any{}})}
	if v = contractJSON(t, h, "POST", "/api/algorithm/review_now?device_sn=SN", "", token, 202); v["status"] != "running" {
		t.Fatal(v)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		v = contractJSON(t, h, "GET", "/api/algorithm/review_status?device_sn=SN", "", token, 200)
		if v["status"] == "done" || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v["status"] != "done" || v["result"].(map[string]any)["summary"] != "ok" {
		t.Fatal(v)
	}
	// Scheduled reviews run once per device at the configured local hour.
	model.replies = []string{toolUse("s3", "submit_algorithm_review", map[string]any{"summary": "x", "config_suggestions": []any{}, "anomalies": []any{}})}
	values, _, _ := s.settingValues()
	at := time.Unix(time.Now().Unix()/86400*86400+int64(forecast.Number(values, "advisor_trigger_hour"))*3600+60, 0)
	if n := s.advisorTick(t.Context(), at); n != 1 {
		t.Fatal(n)
	}
	if n := s.advisorTick(t.Context(), at.Add(time.Hour)); n != 0 {
		t.Fatal(n)
	}
	if n := s.advisorTick(t.Context(), at.Add(24*time.Hour)); n != 1 {
		t.Fatal(n)
	}
}

func TestNarratorPromptPort(t *testing.T) {
	soc, pred := 41.26, 30.04
	rate := .1234
	p := &automation.ChargePlan{Action: "on", Mode: "active", Reason: "deficit", CurrentSOC: &soc, Predicted: &pred, Target: 35, DeficitKWH: 1.234, CheapestRate: &rate}
	want := "Action chosen: ON\nMode: active\nEngine reason: deficit\nCurrent SOC: 41.3%\nPredicted SOC at next sunrise: 30%\nTarget SOC at sunrise: 35%\nEnergy deficit: 1.23 kWh\nCheapest rate in charge window: $0.123/kWh"
	if got := planPrompt(p); got != want {
		t.Fatalf("%q", got)
	}
}
