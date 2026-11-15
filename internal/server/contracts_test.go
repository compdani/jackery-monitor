package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func contractToken(t *testing.T, h http.Handler) string {
	t.Helper()
	v := contractJSON(t, h, "POST", "/api/auth/setup", `{"username":"admin","password":"secure-pass"}`, "", 200)
	return v["token"].(string)
}
func contractJSON(t *testing.T, h http.Handler, method, path, body, token string, status int) map[string]any {
	t.Helper()
	w := request(h, method, path, body, token)
	if w.Code != status {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	v := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// Assertions ported from test_server_routes.py and the SettingsTab consumer.
func TestSettingsSchemaAndRuntime(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	t.Setenv("POLL_INTERVAL_S", "4")
	v := contractJSON(t, h, "GET", "/api/settings", "", token, 200)
	specs := v["settings"].([]any)
	if len(specs) != 9 {
		t.Fatal(specs)
	}
	for _, raw := range specs {
		spec := raw.(map[string]any)
		for _, key := range []string{"key", "label", "hint", "min", "max", "default", "value", "type"} {
			if _, ok := spec[key]; !ok {
				t.Fatalf("missing %s: %v", key, spec)
			}
		}
	}
	if s.pollInterval(time.Minute) != 4*time.Second {
		t.Fatal("environment default ignored")
	}
	v = contractJSON(t, h, "POST", "/api/settings", `{"poll_interval_s":999,"low_battery_threshold":-1,"unknown":5}`, token, 200)
	settings := v["settings"].(map[string]any)
	if settings["poll_interval_s"] != float64(300) || settings["low_battery_threshold"] != float64(1) || settings["unknown"] != nil {
		t.Fatal(settings)
	}
	if s.pollInterval(time.Minute) != 300*time.Second {
		t.Fatal("runtime override ignored")
	}
	contractJSON(t, h, "POST", "/api/settings", `{"poll_interval_s":"invalid"}`, token, 200)
	if s.pollInterval(time.Minute) != 300*time.Second {
		t.Fatal("invalid override replaced value")
	}
}
func TestPrefsAndCostContracts(t *testing.T) {
	_, h := testServer(t)
	token := contractToken(t, h)
	v := contractJSON(t, h, "GET", "/api/device_prefs", "", token, 200)
	if len(v["prefs"].(map[string]any)) != 0 {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/device_prefs", `{"alias":"House"}`, token, 400)
	v = contractJSON(t, h, "POST", "/api/device_prefs", `{"device_id":"siseli:42","alias":" House ","live_controls":["ac","ac"],"calc_grid":true}`, token, 200)
	if v["device_id"] != "siseli:42" || v["prefs"].(map[string]any)["alias"] != "House" {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "POST", "/api/device_prefs", `{"device_id":"siseli:42","alias":null,"live_controls":[],"calc_grid":false}`, token, 200)
	prefs := v["prefs"].(map[string]any)
	if prefs["alias"] != nil || prefs["calc_grid"] != nil || len(prefs["live_controls"].([]any)) != 0 {
		t.Fatal(prefs)
	}
	v = contractJSON(t, h, "GET", "/api/device_prefs", "", token, 200)
	if v["prefs"].(map[string]any)["siseli:42"] == nil {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/cost/plan", "", token, 200)
	if v["plan"].(map[string]any)["rate_per_kwh"] != .30 || len(v["presets"].([]any)) == 0 {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "POST", "/api/cost/plan", `{"type":"flat","rate_per_kwh":0.2,"currency":"USD"}`, token, 200)
	if v["plan"].(map[string]any)["rate_per_kwh"] != .2 {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/cost/plan", `{"type":"invalid"}`, token, 400)
}

func TestConversationChatAndCascade(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			jsonOut(w, 200, map[string]any{"data": []any{map[string]any{"id": "test-chat"}}})
			return
		}
		jsonOut(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "answer"}}}})
	}))
	defer fake.Close()
	s.AIHTTP = fake.Client()
	for key, v := range map[string]map[string]any{"ai_provider": {"provider": "openai"}, "anthropic_prefs": {"openai_compat_preset": "custom", "openai_base_url": fake.URL}} {
		if err := s.Store.Put("settings", key, "", v); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Store.SaveSecret(s.Key, "openai", map[string]any{"api_key": "fake"}); err != nil {
		t.Fatal(err)
	}
	models := contractJSON(t, h, "GET", "/api/openai/models?refresh=true", "", token, 200)
	if models["source"] != "live" || models["models"].([]any)[0].(map[string]any)["id"] != "test-chat" {
		t.Fatal(models)
	}
	v := contractJSON(t, h, "POST", "/api/assistant/conversations", `{}`, token, 200)
	id := v["id"].(string)
	v = contractJSON(t, h, "GET", "/api/assistant/conversations/"+id, "", token, 200)
	if len(v["messages"].([]any)) != 0 || len(v["tool_runs"].([]any)) != 0 {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "POST", "/api/assistant/chat", `{"conversation_id":"`+id+`","message":"question","model":"test-chat"}`, token, 200)
	msg := v["message"].(map[string]any)
	if msg["role"] != "assistant" || msg["content"] != "answer" || msg["id"] == nil || len(v["pending_tools"].([]any)) != 0 {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/assistant/conversations/"+id, "", token, 200)
	if len(v["messages"].([]any)) != 2 {
		t.Fatal(v)
	}
	for _, col := range []string{"attachments", "tool_runs"} {
		if err := s.Store.Put(col, "child", id, map[string]any{"id": "child", "conversation_id": id}); err != nil {
			t.Fatal(err)
		}
	}
	contractJSON(t, h, "DELETE", "/api/assistant/conversations/"+id, "", token, 200)
	contractJSON(t, h, "GET", "/api/assistant/conversations/"+id, "", token, 404)
	for _, col := range []string{"messages", "attachments", "tool_runs"} {
		rows, err := s.Store.List(col, id, 0, 100)
		if err != nil || len(rows) != 0 {
			t.Fatal(col, rows, err)
		}
	}
}

type contractBridge struct {
	method string
	params map[string]any
}

func (b *contractBridge) Call(_ context.Context, m string, p map[string]any) (map[string]any, error) {
	b.method = m
	b.params = p
	return map[string]any{"events": []any{}}, nil
}
func TestQueryForwardingAndDeviceView(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	b := &contractBridge{}
	s.Bridge = b
	contractJSON(t, h, "GET", "/api/events?since=123&limit=2", "", token, 200)
	if b.method != "get_events" || b.params["since"] != "123" || b.params["limit"] != "2" {
		t.Fatal(b)
	}
	contractJSON(t, h, "POST", "/api/set_output", `{"port":"ac","on":true,"device_sn":"SN-B"}`, token, 200)
	if b.params["device_sn"] != "SN-B" {
		t.Fatal(b)
	}
	s.snapshot = map[string]any{"device": map[string]any{"device_sn": "SN-A"}, "telemetry": map[string]any{"battery_percent": 80.0}}
	if err := s.Store.Put("devices", "SN-B", "SN-B", map[string]any{"device": map[string]any{"device_id": "id-B", "device_sn": "SN-B"}, "telemetry": map[string]any{"battery_percent": 42.0}, "last_update_ts": 123.0}); err != nil {
		t.Fatal(err)
	}
	v := contractJSON(t, h, "GET", "/api/status?view_device_id=id-B", "", token, 200)
	if v["device"].(map[string]any)["device_sn"] != "SN-B" || v["telemetry"].(map[string]any)["battery_percent"] != 42.0 {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/status?view_device_id=unknown", "", token, 200)
	if v["device"].(map[string]any)["device_sn"] != "SN-A" {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/openai/compat", "", token, 200)
	if len(v["presets"].([]any)) != 5 {
		t.Fatal(v)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	v = contractJSON(t, h, "GET", "/api/anthropic/models", "", token, 200)
	if v["source"] != "fallback_no_key" || len(v["models"].([]any)) == 0 {
		t.Fatal(v)
	}
}
func TestEnergyDefaultBucketAndNulls(t *testing.T) {
	_, h := testServer(t)
	token := contractToken(t, h)
	v := contractJSON(t, h, "GET", "/api/energy/history", "", token, 200)
	if v["device_sn"] != nil || len(v["history"].([]any)) != 0 {
		t.Fatal(v)
	}
	for _, query := range []string{"device_sn=SN", "device_sn=SN&bucket_s=123"} {
		v = contractJSON(t, h, "GET", "/api/energy/history?"+query, "", token, 200)
		if v["bucket_s"] != float64(900) {
			t.Fatal(v)
		}
	}
}
func TestAIProviderDefaultsAndValidation(t *testing.T) {
	_, h := testServer(t)
	token := contractToken(t, h)
	v := contractJSON(t, h, "GET", "/api/ai/provider", "", token, 200)
	if v["provider"] != "anthropic" || v["openai_compat_preset"] != "openai" {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/anthropic/prefs", "", token, 200)
	if v["advisor_model"] == nil || v["openai_advisor_model"] == nil {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/ai/provider", `{"provider":"invalid"}`, token, 400)
	contractJSON(t, h, "POST", "/api/ai/provider", `{"provider":"openai"}`, token, 200)
	v = contractJSON(t, h, "POST", "/api/anthropic/prefs", `{"narrator_model":"custom"}`, token, 200)
	if v["provider"] != "openai" || v["advisor_model"] == nil {
		t.Fatal(v)
	}
}
