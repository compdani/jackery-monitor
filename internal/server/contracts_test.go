package server

import (
	"encoding/json"
	"net/http"
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
