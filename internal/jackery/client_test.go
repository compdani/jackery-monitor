package jackery

import (
	"context"
	"crypto/aes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"jackery-monitor/internal/secrets"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}
func TestCloudLoginPollAndContention(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	client := New(secrets.Credentials{Email: "fixture@example.com", Password: "secret", Region: "US"})
	client.PublicKey = base64.StdEncoding.EncodeToString(der)
	logins := 0
	contend := false
	client.HTTP = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/auth/login" {
			logins++
			query := r.URL.Query()
			encrypted, _ := base64.StdEncoding.DecodeString(query.Get("rsaForAesKey"))
			aesRaw, err := rsa.DecryptPKCS1v15(rand.Reader, key, encrypted)
			if err != nil {
				t.Fatal(err)
			}
			ct, _ := base64.StdEncoding.DecodeString(query.Get("aesEncryptData"))
			block, _ := aes.NewCipher(aesRaw)
			plain := make([]byte, len(ct))
			for i := 0; i < len(ct); i += 16 {
				block.Decrypt(plain[i:i+16], ct[i:i+16])
			}
			plain = plain[:len(plain)-int(plain[len(plain)-1])]
			var bean map[string]any
			json.Unmarshal(plain, &bean)
			if bean["account"] != "fixture@example.com" || bean["password"] != "secret" || bean["macId"] != client.MacID {
				t.Fatalf("bad login bean: %v", bean)
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				t.Fatal("expected multipart login")
			}
			return response(`{"code":0,"data":{"Token":"cloud-token","user_id":42,"mqttPassword":"abc"}}`), nil
		}
		if r.Header.Get("token") != "cloud-token" {
			t.Fatal("missing cloud token")
		}
		if contend {
			return response(`{"code":10402,"msg":"Token expires"}`), nil
		}
		switch r.URL.Path {
		case "/v1/device/bind/list":
			return response(`{"code":0,"data":[{"devId":123,"devSn":"SN1","devNickname":"Station","modelCode":13}]}`), nil
		case "/v1/device/property":
			if r.URL.Query().Get("deviceId") != "123" {
				t.Fatal("wrong selected device")
			}
			return response(`{"code":0,"data":{"properties":{"rb":87,"bt":256,"ip":900,"acip":100,"cip":50,"op":412,"acov":1199,"acohz":60,"it":999,"ot":64,"oac":1}}}`), nil
		}
		t.Fatalf("unexpected path %s", r.URL.Path)
		return nil, nil
	})}
	devices, err := client.Devices(context.Background())
	if err != nil || len(devices) != 1 || devices[0].ID != "123" {
		t.Fatalf("devices %v %v", devices, err)
	}
	if client.UserID != "42" || client.MQTTKey != "abc" {
		t.Fatal("nested auth aliases not parsed")
	}
	props, err := client.Properties(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	tele := Telemetry(props)
	for k, want := range map[string]any{"battery_percent": 87, "battery_temp_c": 25.6, "solar_input_w": 750, "ac_output_v": 119.9, "ac_output_hz": float64(60), "time_to_full_h": float64(0), "time_remaining_h": 6.4, "ac_on": true} {
		if tele[k] != want {
			t.Errorf("%s=%v want %v", k, tele[k], want)
		}
	}
	if _, ok := tele["battery_status"]; ok {
		t.Fatal("missing battery status falsely reported as idle")
	}
	contend = true
	_, err = client.Devices(context.Background())
	if !errors.Is(err, ErrContested) || logins != 1 || client.Token != "" {
		t.Fatalf("contention must not auto-login: %v %d", err, logins)
	}
}
func TestMQTTProtocolVectors(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	username, password, err := MQTTPassword("42", "2a3f", base64.StdEncoding.EncodeToString(key))
	if err != nil || username != "42@2a3f" || password != "eu820sx+djWMZ+9Q0hLyxg==" {
		t.Fatalf("Python CBC vector mismatch: %s %v", password, err)
	}
	for port, id := range map[string]int{"ac": 4, "dc": 1, "usb": 2, "car": 3} {
		cmd, err := Command("SN2", port, true, time.UnixMilli(1234))
		if err != nil || cmd["actionId"] != id || cmd["deviceSn"] != "SN2" || cmd["id"] != int64(1234) {
			t.Fatalf("invalid command %v", cmd)
		}
	}
	if _, err := Command("SN", "wrong", true, time.Now()); err == nil {
		t.Fatal("unknown port accepted")
	}
	if _, _, ok := ParseDelta([]byte(`{"messageType":"DevicePropertyChange","body":{"ip":400}}`)); ok {
		t.Fatal("unaddressed delta accepted")
	}
	sn, body, ok := ParseDelta([]byte(`{"messageType":"DevicePropertyChange","deviceSn":"SN2","body":{"ip":400}}`))
	if !ok || sn != "SN2" || body["ip"] != float64(400) {
		t.Fatal("delta parse failed")
	}
}
func TestCloudErrorsDoNotLeakEncryptedLoginURL(t *testing.T) {
	c := New(secrets.Credentials{Email: "secret@local", Password: "secret"})
	c.HTTP = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { return nil, errors.New(r.URL.String()) })}
	err := c.Login(context.Background())
	if err == nil || strings.Contains(err.Error(), "aesEncryptData") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestPackHTTPAndMQTT(t *testing.T) {
	c := New(secrets.Credentials{})
	c.Token = "token"
	c.HTTP = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/device/battery/pack/list" || r.URL.Query().Get("deviceSn") != "MAIN" {
			t.Fatal("pack request addressed incorrectly")
		}
		return response(`{"code":0,"data":[{"deviceSn":"P2","deviceOrder":2,"rb":80},{"deviceSn":"P1","deviceOrder":1,"rb":90},{"deviceSn":"removed","isDelete":true}]}`), nil
	})}
	packs, err := c.Packs(context.Background(), "MAIN")
	if err != nil || len(packs) != 2 || packs[0]["deviceSn"] != "P1" {
		t.Fatalf("bad pack list %v %v", packs, err)
	}
	sn, packs, ok := ParsePacks([]byte(`{"messageType":"SubDevicePropertyChange","deviceSn":"MAIN","body":{"subDevices":[]}}`))
	if !ok || sn != "MAIN" || len(packs) != 0 {
		t.Fatal("explicit empty MQTT pack snapshot ignored")
	}
}
