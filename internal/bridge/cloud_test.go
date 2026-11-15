package bridge

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestCloudPropertiesReference(t *testing.T) {
	p := CloudTelemetry(map[string]any{"rb": 26, "bt": 313, "ip": 47, "op": 0, "oac": 1, "it": 999, "ot": 999})
	for k, want := range map[string]any{"battery_percent": 26., "battery_temp_c": 31.3, "input_power_w": 47., "solar_input_w": 47., "output_power_w": 0., "ac_on": true, "time_to_full_h": 0.} {
		if !reflect.DeepEqual(p[k], want) {
			t.Errorf("%s: %v want %v", k, p[k], want)
		}
	}
	if _, ok := p["battery_status"]; ok {
		t.Fatal("absent status must remain absent")
	}
	for i := 0; i < 3; i++ {
		if got := CloudTelemetry(map[string]any{"bs": i})["battery_status"]; got != float64(i) {
			t.Fatal(got)
		}
	}
	p = CloudTelemetry(map[string]any{"ip": 500, "acip": 200, "cip": 50})
	if p["solar_input_w"] != 250. || p["ac_input_w"] != 200. || p["car_input_w"] != 50. {
		t.Fatal(p)
	}
}
func TestCloudLoginPollingAndContested(t *testing.T) {
	logins := 0
	contested := false
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/login" {
			logins++
			b, e := base64.StdEncoding.DecodeString(r.URL.Query().Get("aesEncryptData"))
			if e != nil {
				t.Error(e)
			}
			block, _ := aes.NewCipher([]byte(loginKey))
			for i := 0; i < len(b); i += 16 {
				block.Decrypt(b[i:i+16], b[i:i+16])
			}
			var login map[string]any
			if e = json.Unmarshal(b[:len(b)-int(b[len(b)-1])], &login); e != nil {
				t.Error(e)
			}
			if login["account"] != "test@example.com" || login["password"] != "secret" {
				t.Error(login)
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"token": "token", "userId": 123, "mqttPassword": "unused"}})
			return
		}
		if r.Header.Get("token") != "token" {
			t.Error("missing token")
		}
		if contested {
			json.NewEncoder(w).Encode(map[string]any{"code": 10402, "msg": "Token expires"})
			return
		}
		switch r.URL.Path {
		case "/v1/device/bind/list":
			ioJSON(w, `{"code":0,"data":[{"devId":1,"devSn":"SN","devNickname":"Home","modelCode":22}]}`)
		case "/v1/device/property":
			if r.URL.Query().Get("deviceId") != "1" {
				t.Error("device id")
			}
			ioJSON(w, `{"code":0,"data":{"properties":{"rb":26},"device":{"packs":[1]}}}`)
		case "/v1/device/battery/pack/list":
			ioJSON(w, `{"code":0,"data":[{"deviceOrder":2},{"deviceOrder":1},{"isDelete":true}]}`)
		}
	}))
	defer h.Close()
	c := &Cloud{Email: "test@example.com", Password: "secret", BaseURL: h.URL}
	n := &Native{Cloud: c}
	got, e := n.Call(context.Background(), "poll", nil)
	if e != nil {
		t.Fatal(e)
	}
	if object(got["device"])["device_type"] != "box" || object(got["telemetry"])["battery_percent"] != 26. {
		t.Fatal(got)
	}
	if ts, ok := got["last_update_ts"].(float64); !ok || ts <= 0 {
		t.Fatal("native observation has no timestamp", got)
	}
	contested = true
	if _, e = n.Call(context.Background(), "poll", nil); !errors.Is(e, ErrSessionContested) {
		t.Fatal(e)
	}
	if _, e = n.Call(context.Background(), "poll", nil); e == nil || logins != 1 {
		t.Fatal("must back off without a token war", e, logins)
	}
	if !time.Now().Before(n.retryAfter) {
		t.Fatal("missing cooldown")
	}
}
func ioJSON(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(s))
}

func TestCloudMissingSOCDoesNotBecomeCriticalZero(t *testing.T) {
	for _, raw := range []any{nil, "invalid", true, -1, 101} {
		if got := CloudTelemetry(map[string]any{"rb": raw})["battery_percent"]; got != nil {
			t.Fatalf("%v became %v", raw, got)
		}
	}
	if got := CloudTelemetry(map[string]any{"rb": 0})["battery_percent"]; got != float64(0) {
		t.Fatal("real zero lost", got)
	}
}
