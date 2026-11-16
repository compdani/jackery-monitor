package siseli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc, creds Credentials) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	creds.UserID = "alice"
	creds.Password = "secret"
	c, e := NewClient(ClientOptions{Credentials: creds, HTTP: srv.Client(), BaseURL: srv.URL})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func reply(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, s)
}
func TestLoginAndListDevices(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case loginPath:
			var body Values
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["account"] != "alice" || body["password"] == "secret" {
				t.Error(body)
			}
			for _, k := range []string{"IOT-Open-AppID", "IOT-Open-Nonce", "IOT-Open-Body-Hash", "IOT-Open-Sign"} {
				if r.Header.Get(k) == "" {
					t.Errorf("missing %s", k)
				}
			}
			reply(w, `{"code":0,"data":{"accessToken":"tok-1","refreshToken":"ref-1","accessTokenWillExpiredAt":"2099-01-01T00:00:00Z"}}`)
		case "/apis/device/list":
			if r.Header.Get("IOT-Token") != "tok-1" {
				t.Error("token")
			}
			reply(w, `{"code":0,"data":{"total":1,"list":[{"id":"99","name":"Inv"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}, Credentials{})
	if e := c.Login(context.Background()); e != nil {
		t.Fatal(e)
	}
	if c.Credentials().AccessToken != "tok-1" {
		t.Fatal("token")
	}
	d, e := c.ListDevices(context.Background(), "station-1", 50)
	if e != nil || len(d) != 1 || d[0]["id"] != "99" {
		t.Fatal(d, e)
	}
}
func TestLoginRejectsBadCredentials(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, `{"code":7,"message":"bad password"}`) }, Credentials{})
	if !errors.Is(c.Login(context.Background()), ErrAuthentication) {
		t.Fatal("accepted")
	}
}
func TestControllerLatestState(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != latestPath || r.URL.Query().Get("deviceId") != "525039200608948225" || r.URL.Query().Get("dataSource") != "1" {
			t.Error(r.URL)
		}
		reply(w, `{"code":0,"data":{"fields":{"batteryRemainingCapacitySOC":{"value":42,"unit":"%"},"BatteryVoltage":{"value":51.6,"unit":"V"},"generationPower":{"value":0.152,"unit":"kW"},"load_power":{"value":0,"unit":"W"},"ChargingCurrent":{"value":2.9,"unit":"A"},"batteryTemperature1":{"value":39,"unit":"°C"}}}}`)
	}, Credentials{AccessToken: "tok"})
	v, e := c.FetchLatestData(context.Background(), "525039200608948225")
	if e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	for k, n := range map[string]float64{"batterySOC": 42, "pvInputPower": 152, "loadPower": 0, "batteryVoltage": 51.6} {
		if num(v[k]) != n {
			t.Fatal(k, v)
		}
	}
	tele := ToTelemetry(v)
	if num(tele["battery_temp_c"]) != 39 || num(tele["battery_status"]) != 1 {
		t.Fatal(tele)
	}
}
func TestLatestFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name, history string
		flow          bool
		soc           float64
	}{{"empty", `{"code":0,"data":{"payload":{"fields":{}}}}`, true, 55}, {"history-error", `{"code":20101,"message":"Illegal argument"}`, true, 55}, {"partial", `{"code":"0","data":{"payload":{"fields":{"feedInPower":[0]}}}}`, true, 55}, {"soc", `{"code":0,"data":{"payload":{"fields":{"batterySOC":[42]}}}}`, false, 42}} {
		t.Run(tc.name, func(t *testing.T) {
			flow := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case historyPath:
					reply(w, tc.history)
				case flowPath:
					flow++
					reply(w, `{"code":0,"data":{"deviceAttributeState":{"fields":{"pv1Power":300,"bmsSOC":55,"load_power":0.2}}}}`)
				default:
					http.NotFound(w, r)
				}
			}, Credentials{AccessToken: "tok"})
			v, e := c.FetchLatestData(context.Background(), "99")
			if e != nil || num(v["batterySOC"]) != tc.soc {
				t.Fatal(v, e)
			}
			if (flow > 0) != tc.flow {
				t.Fatal(flow)
			}
			if tc.flow && (num(v["pvInputPower"]) != 300 || num(v["acOutputActivePower"]) != 200) {
				t.Fatal(v)
			}
		})
	}
}
func TestSettingWrites(t *testing.T) {
	writes := []Values{}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != writePath {
			t.Error(r.URL)
		}
		var v Values
		_ = json.NewDecoder(r.Body).Decode(&v)
		writes = append(writes, v)
		reply(w, `{"code":0,"data":{}}`)
	}, Credentials{AccessToken: "tok"})
	for _, tc := range []struct {
		key      string
		value    any
		settings Values
		wantKey  string
		want     any
	}{{"outputSourcePrioritySetting", 2, Values{"setOutputSourcePriority": Values{"value": 1}}, "setOutputSourcePriority", 2}, {"LoadSwitchSetting", 1, Values{"LoadSwitchSetting": Values{"value": "0"}}, "LoadSwitchSetting", "1"}, {"ChargeLimitVoltage", 16, Values{"ChargeLimitVoltage": Values{"value": 15}}, "ChargeLimitVoltage", 16}, {"BatteryType", 5, Values{"BatteryType": Values{"value": "4"}}, "BatteryType", "5"}} {
		if e := c.SetDeviceSetting(context.Background(), "99", tc.key, tc.value, tc.settings); e != nil {
			t.Fatal(e)
		}
		equalJSON(t, writes[len(writes)-1], Values{"id": "99", "deviceId": "99", "key": tc.wantKey, "value": tc.want})
	}
}
func TestSigningVector(t *testing.T) {
	h := signedHeaders([]byte(`{"account":"x","password":"y"}`), "0123456789abcdef0123456789abcdef")
	if h.Get("IOT-Open-AppID") != IOTAppID || len(h.Get("IOT-Open-Sign")) != 32 {
		t.Fatal(h)
	}
	if h.Get("IOT-Open-Sign") != signVector {
		t.Fatal(h.Get("IOT-Open-Sign"))
	}
}
func TestRefresh401AndCancellation(t *testing.T) {
	refresh, latest := 0, 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case refreshPath:
			refresh++
			reply(w, `{"code":0,"data":{"accessToken":"new","refreshToken":"refresh","accessTokenWillExpiredAt":"2099-01-01T00:00:00Z"}}`)
		case latestPath:
			latest++
			if latest == 1 {
				w.WriteHeader(401)
				return
			}
			reply(w, `{"code":0,"data":{"fields":{"batterySOC":42}}}`)
		default:
			t.Error(r.URL)
		}
	}, Credentials{AccessToken: "old", RefreshToken: "refresh", AccessTokenExpires: time.Now().Add(time.Hour).Format(time.RFC3339)})
	_, e := c.FetchLatestData(context.Background(), "99")
	if e != nil || refresh != 1 || latest != 2 {
		t.Fatal(refresh, latest, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e = c.FetchLatestData(ctx, "99")
	if e == nil || !strings.Contains(e.Error(), "canceled") {
		t.Fatal(e)
	}
}

func TestDailyProductionIntervals(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/apis/deviceOverView/generatedEnergy/daily" || r.URL.Query().Get("deviceId") != "525039200608948225" {
			t.Error(r.Method, r.URL)
		}
		var b Values
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b["time"] != "2026-11-15" {
			t.Error(b, err)
		}
		reply(w, `{"code":0,"data":[{"time":"2026-11-15 11:00:00","value":0.435,"generatedEnergy":0.435,"isRealValue":true},{"time":"2026-11-15 11:30:00","value":0.295,"generatedEnergy":0.295,"isRealValue":true},{"time":"2026-11-15 12:00:00","value":9,"isRealValue":false}]}`)
	}, Credentials{AccessToken: "tok", TimeZone: "America/Chicago"})
	rows, err := c.FetchDailyProduction(context.Background(), "525039200608948225", "2026-11-15")
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	if num(rows[0]["solar_wh"]) != 435 || num(rows[1]["solar_wh"]) != 295 || num(rows[1]["ts"])-num(rows[0]["ts"]) != 1800 {
		t.Fatal(rows)
	}
	stamp := time.Unix(int64(num(rows[0]["ts"])), 0).UTC().Format(time.RFC3339)
	if stamp != "2026-11-15T17:00:00Z" {
		t.Fatal(stamp)
	}
}

func TestPortalPreservesLargeNumericDeviceID(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"code":0,"data":{"id":525039200608948225,"name":"Controller"}}`)
	}, Credentials{AccessToken: "tok"})
	v, err := c.request(context.Background(), "GET", "/fixture", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := NormalizeDevice(object(v["data"]))
	if d["device_id"] != "siseli:525039200608948225" {
		t.Fatal(d)
	}
}

func TestEmptyTelemetryIsNotSuccessfulObservation(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, `{"code":0,"data":{}}`) }, Credentials{AccessToken: "tok"})
	if _, err := c.FetchLatestData(context.Background(), "99"); err == nil {
		t.Fatal("empty response accepted")
	}
}
