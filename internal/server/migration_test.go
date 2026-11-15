package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestViewReferenceFallbackAndIsolation(t *testing.T) {
	s, _ := testServer(t)
	s.snapshot = map[string]any{"device": map[string]any{"device_id": "id-A", "device_sn": "SN-A"}, "telemetry": map[string]any{"battery_percent": 80.}, "cloud": map[string]any{"selected_device_id": "id-A"}}
	if e := s.Store.Put("devices", "SN-B", "SN-B", map[string]any{"device": map[string]any{"device_id": "id-B", "device_sn": "SN-B", "model_code": 22.}, "telemetry": map[string]any{"battery_percent": 42., "output_power_w": 75.}, "last_update_ts": 1700000000.}); e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct {
		View, SN string
		SOC      float64
	}{{"", "SN-A", 80}, {"unknown", "SN-A", 80}, {"id-A", "SN-A", 80}, {"id-B", "SN-B", 42}, {"SN-B", "SN-B", 42}} {
		out := s.status(httptest.NewRequest("GET", "/api/status?view_device_id="+c.View, nil))
		if out["device"].(map[string]any)["device_sn"] != c.SN || out["telemetry"].(map[string]any)["battery_percent"] != c.SOC {
			t.Fatal(c, out)
		}
	}
	if s.snapshot["cloud"].(map[string]any)["selected_device_id"] != "id-A" {
		t.Fatal("view changed active device")
	}
}
func TestMigrationSecretEndpoints(t *testing.T) {
	s, h := testServer(t)
	w := request(h, "POST", "/api/auth/setup", `{"username":"admin","password":"secret-pass"}`, "")
	var setup map[string]any
	json.Unmarshal(w.Body.Bytes(), &setup)
	token := setup["token"].(string)
	status := 200
	s.AIHTTP = &http.Client{Transport: narrationTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"content":[{"type":"text","text":"o"}],"choices":[{"message":{"content":"o"}}]}`))}, nil
	})}
	for _, provider := range []string{"openai", "anthropic"} {
		path := "/api/" + provider + "/key"
		prefix := map[string]string{"openai": "sk-", "anthropic": "sk-ant-"}[provider]
		// Keys are validated (prefix, then a one-token call) before saving.
		if w = request(h, "POST", path, `{"api_key":"SENSITIVE"}`, token); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
		status = 401
		if w = request(h, "POST", path, `{"api_key":"`+prefix+`SENSITIVE"}`, token); w.Code != 400 || !strings.Contains(w.Body.String(), "key validation failed") {
			t.Fatal(w.Code, w.Body.String())
		}
		status = 200
		if w = request(h, "POST", path, `{"api_key":"`+prefix+`SENSITIVE"}`, token); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		w = request(h, "GET", path, "", token)
		if w.Code != 200 || strings.Contains(w.Body.String(), "SENSITIVE") {
			t.Fatal(w.Body.String())
		}
		m, e := s.Store.Get("credentials", provider)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(m)
		if strings.Contains(string(b), "SENSITIVE") {
			t.Fatal("unencrypted key")
		}
		if w = request(h, "DELETE", path, "", token); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
}

func TestAssistantTurnPersistsHistory(t *testing.T) {
	s, h := testServer(t)
	w := request(h, "POST", "/api/auth/setup", `{"username":"admin","password":"secret-pass"}`, "")
	var setup map[string]any
	json.Unmarshal(w.Body.Bytes(), &setup)
	token := setup["token"].(string)
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		want := 2
		if calls == 2 {
			want = 4
		}
		if len(body["messages"].([]any)) != want {
			t.Error(body)
		}
		if r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("missing key")
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Battery is at 42%."}}]}`))
	}))
	defer provider.Close()
	if e := s.Store.SaveSecret(s.Key, "openai", map[string]any{"api_key": "fixture-key"}); e != nil {
		t.Fatal(e)
	}
	s.Store.Put("settings", "ai_provider", "", map[string]any{"provider": "openai"})
	s.Store.Put("settings", "anthropic_prefs", "", map[string]any{"openai_base_url": provider.URL, "openai_model": "fixture"})
	s.Store.Put("conversations", "chat", "", map[string]any{"id": "chat"})
	for i := 0; i < 2; i++ {
		w = request(h, "POST", "/api/assistant/chat", `{"conversation_id":"chat","message":"SOC?"}`, token)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "42%") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	rows, e := s.Store.List("messages", "chat", 0, 100)
	if e != nil || len(rows) != 4 {
		t.Fatal(rows, e)
	}
}
