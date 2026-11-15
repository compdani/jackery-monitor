package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/compdani/jackery-monitor/internal/ecoflow"
)

// siseliPortal is a recorded-shape solar.siseli.com fixture.
func siseliPortal(t *testing.T, badPassword *atomic.Bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apis/login/account":
			if badPassword.Load() {
				_, _ = w.Write([]byte(`{"code":7,"message":"bad password"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"tok-1","refreshToken":"ref-1","accessTokenWillExpiredAt":"2099-01-01T00:00:00Z"}}`))
		case "/apis/device/list":
			_, _ = w.Write([]byte(`{"code":0,"data":{"total":1,"list":[{"id":"99","name":"Inv"}]}}`))
		case "/apis/deviceState/simple/state/latest/v1":
			_, _ = w.Write([]byte(`{"code":0,"data":{"fields":{"batteryRemainingCapacitySOC":{"value":42,"unit":"%"},"BatteryVoltage":{"value":51.6,"unit":"V"},"generationPower":{"value":0.152,"unit":"kW"},"load_power":{"value":300,"unit":"W"}}}}`))
		case "/apis/remote/device/configs/cache/get":
			_, _ = w.Write([]byte(`{"code":0,"data":{"LoadSwitchSetting":{"value":"0"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSiseliCloudFleet(t *testing.T) {
	s, h := testServer(t)
	bad := &atomic.Bool{}
	portal := siseliPortal(t, bad)
	s.SiseliHTTP, s.SiseliBaseURL = portal.Client(), portal.URL
	token := contractToken(t, h)
	v := contractJSON(t, h, "GET", "/api/siseli/credentials", "", token, 200)
	if v["has_credentials"] != false || v["local_running"] != false || v["local_retired"] != true {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/siseli/credentials", `{"user_id":"alice","password":"pw"}`, token, 400)
	bad.Store(true)
	contractJSON(t, h, "POST", "/api/siseli/credentials", `{"user_id":"alice","password":"pw","station_id":"7"}`, token, 400)
	bad.Store(false)
	v = contractJSON(t, h, "POST", "/api/siseli/credentials", `{"user_id":"alice","password":"pw","station_id":"7"}`, token, 200)
	if v["device_count"] != 1.0 || v["devices"].([]any)[0] != "Inv" {
		t.Fatal(v)
	}
	raw, err := s.Store.Get("credentials", "siseli")
	if err != nil || raw["password"] != nil || raw["ct"] == nil {
		t.Fatal("secret not encrypted", raw, err)
	}
	// Empty password keeps the saved one.
	contractJSON(t, h, "POST", "/api/siseli/credentials", `{"user_id":"alice","password":"","station_id":"7"}`, token, 200)
	if err = s.siseliPollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	v = contractJSON(t, h, "GET", "/api/siseli/status", "", token, 200)
	if v["state"] != "connected" || len(v["devices"].([]any)) != 1 || v["has_credentials"] != true {
		t.Fatal(v)
	}
	samples, err := s.Store.List("samples", "siseli:99", 0, 0)
	if err != nil || len(samples) != 1 || samples[0]["battery_pct"] != 42.0 {
		t.Fatal(samples, err)
	}
	st := contractJSON(t, h, "GET", "/api/status", "", token, 200)
	cloud := st["cloud"].(map[string]any)
	if len(cloud["devices"].([]any)) != 1 || cloud["devices_telemetry"].(map[string]any)["siseli:99"] == nil {
		t.Fatal(cloud)
	}
	contractJSON(t, h, "GET", "/api/siseli/settings", "", token, 400)
	contractJSON(t, h, "GET", "/api/siseli/settings?device_id=nope", "", token, 404)
	if v = contractJSON(t, h, "GET", "/api/siseli/settings?device_id=99", "", token, 200); v["device_id"] != "siseli:99" {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/siseli/settings", `{"device_id":"siseli:99","key":"bogus","value":1}`, token, 400)
	contractJSON(t, h, "POST", "/api/siseli/settings", `{"device_id":"siseli:99","key":"LoadSwitchSetting"}`, token, 400)
	for _, path := range []string{"/api/siseli/local", "/api/siseli/local/test", "/api/siseli/mqtt-capture"} {
		contractJSON(t, h, "POST", path, `{}`, token, 410)
	}
	if v = contractJSON(t, h, "GET", "/api/siseli/mqtt-capture", "", token, 200); v["enabled"] != false {
		t.Fatal(v)
	}
	contractJSON(t, h, "DELETE", "/api/siseli/credentials", "", token, 200)
	if v = contractJSON(t, h, "GET", "/api/siseli/status", "", token, 200); v["state"] != "idle" || v["has_credentials"] != false {
		t.Fatal(v)
	}
}

// ecoflowHTTP answers the private-API login and MQTT certification.
type ecoflowHTTP struct{ reject atomic.Bool }

func (e *ecoflowHTTP) Do(r *http.Request) (*http.Response, error) {
	body := `{"code":"0","message":"Success","data":{"token":"tk","user":{"userId":"1234567"}}}`
	if strings.HasSuffix(r.URL.Path, "/certification") {
		body = `{"code":"0","message":"Success","data":{"url":"mqtt.example","port":"8883","certificateAccount":"u","certificatePassword":"p"}}`
	} else if e.reject.Load() {
		body = `{"code":"1","message":"bad email or password"}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

type fakeSession struct{ subs atomic.Int32 }

func (f *fakeSession) Subscribe(s []ecoflow.Subscription) error { f.subs.Add(int32(len(s))); return nil }
func (f *fakeSession) Publish(string, byte, []byte) error       { return nil }
func (f *fakeSession) Stop()                                    {}

type fakeMQTT struct{ starts atomic.Int32 }

func (m *fakeMQTT) Start(_ ecoflow.MQTTOptions, cb ecoflow.MQTTCallbacks) (ecoflow.MQTTSession, error) {
	m.starts.Add(1)
	s := &fakeSession{}
	go cb.OnConnect(s, ecoflow.ConnackReason{})
	return s, nil
}

func TestEcoFlowFleetAndSiseliOverlay(t *testing.T) {
	s, h := testServer(t)
	doer, mqtt := &ecoflowHTTP{}, &fakeMQTT{}
	s.EcoFlowHTTP, s.EcoFlowMQTT = doer, mqtt
	token := contractToken(t, h)
	v := contractJSON(t, h, "GET", "/api/ecoflow/credentials", "", token, 200)
	if v["has_credentials"] != false || v["reconnect"] == nil {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/ecoflow/reconnect", `{}`, token, 400)
	contractJSON(t, h, "POST", "/api/ecoflow/devices", `{}`, token, 400)
	contractJSON(t, h, "POST", "/api/ecoflow/devices", `{"sn":"R1","device_type":"TOASTER"}`, token, 400)
	v = contractJSON(t, h, "POST", "/api/ecoflow/devices", `{"sn":"R1","alias":"Delta","capacity_wh":2048,"siseli_device_sn":"siseli:99","roles":["battery"]}`, token, 200)
	if v["device"].(map[string]any)["sn"] != "ecoflow:R1" {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "GET", "/api/ecoflow/devices?siseli_sn=siseli:99", "", token, 200); len(v["devices"].([]any)) != 1 {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/ecoflow/devices/ecoflow:NOPE/link", `{"siseli_device_sn":""}`, token, 404)
	if s.ecoflowBatteryWh("siseli:99") != 2048 || s.ecoflowBatteryWh("other") != 0 {
		t.Fatal(s.ecoflowBatteryWh("siseli:99"))
	}
	doer.reject.Store(true)
	contractJSON(t, h, "POST", "/api/ecoflow/credentials", `{"email":"e@x","password":"pw"}`, token, 400)
	doer.reject.Store(false)
	v = contractJSON(t, h, "POST", "/api/ecoflow/credentials", `{"email":"e@x","password":"pw"}`, token, 200)
	if v["email"] != "e@x" || v["api_host"] == nil {
		t.Fatal(v)
	}
	s.ecoflowTick(context.Background(), float64(time.Now().Unix()))
	if v = contractJSON(t, h, "GET", "/api/ecoflow/credentials", "", token, 200); v["state"] != "connected" || v["has_password"] != true || mqtt.starts.Load() != 1 {
		t.Fatal(v, mqtt.starts.Load())
	}
	// Telemetry callback: cached, persisted, and overlaid onto the linked Siseli view.
	s.fleet.siseli.mu.Lock()
	s.fleet.siseli.telemetry = map[string]map[string]any{"siseli:99": {"telemetry": map[string]any{"battery_percent": 40.0, "output_power_w": 100.0}, "ts": float64(time.Now().Unix()), "origin": "http"}}
	s.fleet.siseli.mu.Unlock()
	s.ecoflowUpdate("ecoflow:R1", ecoflow.Telemetry{}, ecoflow.Detail{})
	if live := s.ecoflowLive(); live["ecoflow:R1"].TS == nil {
		t.Fatal(live)
	}
	if rows, err := s.Store.List("devices", "siseli:99", 0, 0); err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	contractJSON(t, h, "POST", "/api/ecoflow/reconnect_settings", `{"reconnect_start":"25:00"}`, token, 400)
	v = contractJSON(t, h, "POST", "/api/ecoflow/reconnect_settings", `{"auto_reconnect":false,"reconnect_start":"01:00","reconnect_end":"02:00"}`, token, 200)
	if v["ok"] != true || v["auto_reconnect"] != false || v["reconnect_start"] != "01:00" {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "GET", "/api/ecoflow/reconnect_settings", "", token, 200); v["reconnect_end"] != "02:00" {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "POST", "/api/ecoflow/reconnect", `{}`, token, 200); v["ok"] != true {
		t.Fatal(v)
	}
	contractJSON(t, h, "DELETE", "/api/ecoflow/devices/ecoflow:NOPE", "", token, 404)
	contractJSON(t, h, "DELETE", "/api/ecoflow/devices/ecoflow:R1", "", token, 200)
	contractJSON(t, h, "DELETE", "/api/ecoflow/credentials", "", token, 200)
	s.ecoflowTick(context.Background(), float64(time.Now().Unix()))
	if v = contractJSON(t, h, "GET", "/api/ecoflow/credentials", "", token, 200); v["state"] != "idle" {
		t.Fatal(v)
	}
}

func TestBMSRoutesAndBLEInspectorMount(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	v := contractJSON(t, h, "POST", "/api/bms/scan", `{"seconds":99}`, token, 200)
	if v["ble_available"] != false || len(v["devices"].([]any)) != 0 {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/bms/saved", `{"mac":"nope"}`, token, 400)
	contractJSON(t, h, "POST", "/api/bms/saved", `{"mac":"AA:BB:CC:DD:EE:FF","capacity_wh":"x"}`, token, 400)
	v = contractJSON(t, h, "POST", "/api/bms/saved", `{"mac":"aa:bb:cc:dd:ee:ff","alias":"Rack","capacity_wh":5120,"siseli_device_sn":"siseli:99"}`, token, 200)
	pack := v["pack"].(map[string]any)
	if pack["mac"] != "AA:BB:CC:DD:EE:FF" || pack["capacity_wh"] != 5120.0 || pack["reading"] == nil {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "GET", "/api/bms/saved?siseli_sn=siseli:99", "", token, 200); len(v["packs"].([]any)) != 1 || v["ble"] == nil {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "POST", "/api/bms/inverter/siseli:99", `{"use_as_main":true}`, token, 200); v["use_as_main"] != true {
		t.Fatal(v)
	}
	if !s.bmsRegistry().UseAsMain("siseli:99") {
		t.Fatal("use_as_main not persisted")
	}
	if v = contractJSON(t, h, "GET", "/api/bms/status", "", token, 200); v["ble_available"] != false || len(v["packs"].([]any)) != 1 {
		t.Fatal(v)
	}
	contractJSON(t, h, "DELETE", "/api/bms/saved/11:22:33:44:55:66", "", token, 404)
	contractJSON(t, h, "DELETE", "/api/bms/saved/AA:BB:CC:DD:EE:FF", "", token, 200)
	// The inspector routes are mounted behind application auth.
	if w := request(h, "GET", "/api/ble/status", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/api/ble/status", "", token); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(h, "POST", "/api/ble/sessions", `{"operation":"bogus"}`, token); w.Code != 422 && w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}
