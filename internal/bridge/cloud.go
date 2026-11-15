package bridge

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrSessionContested = errors.New("Jackery session contested")

const loginKey = "1234567890123456"
const publicKey = "MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCVmzgJy/4XolxPnkfu32YtJqYGFLYqf9/rnVgURJED+8J9J3Pccd6+9L97/+7COZE5OkejsgOkqeLNC9C3r5mhpE4zk/HStss7Q8/5DqkGD1annQ+eoICo3oi0dITZ0Qll56Dowb8lXi6WHViVDdih/oeUwVJY89uJNtTWrz7t7QIDAQAB"

type CloudDevice struct {
	ID        string `json:"device_id"`
	Name      string `json:"name"`
	ModelCode int    `json:"model_code"`
	ModelName string `json:"model_name"`
	SN        string `json:"device_sn"`
}

func (d CloudDevice) Info() map[string]any {
	kind := "portable"
	if d.ModelCode == 22 {
		kind = "box"
	}
	return map[string]any{"device_id": d.ID, "device_sn": d.SN, "name": d.Name, "address": d.ID, "rssi": -100, "model_code": d.ModelCode, "device_type": kind}
}

// Cloud serializes account operations, including MQTT lifecycle. It never
// automatically reclaims a contested session: Native enforces a cooldown.
type Cloud struct {
	mu                                 sync.Mutex
	Email, Password, Region, AndroidID string
	BaseURL                            string
	HTTP                               *http.Client
	// TLSConfig allows the operator to supply the Jackery broker CA.
	TLSConfig              *tls.Config
	token, userID, mqttKey string
	mqtt                   mqtt.Client
	macID                  string
}

func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}
func stringValue(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func pick(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := stringValue(m[k]); s != "" {
			return s
		}
	}
	return ""
}
func pad(b []byte) []byte {
	n := aes.BlockSize - len(b)%aes.BlockSize
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}
func (c *Cloud) request(ctx context.Context, method, path string, q url.Values, body io.Reader, content string) (map[string]any, error) {
	base := c.BaseURL
	if base == "" {
		base = "https://iot.jackeryapp.com"
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path+"?"+q.Encode(), body)
	if err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"accept": "*/*", "app_version": "2.0.2", "sys_version": "26.4.2", "platform": "1", "accept-language": "en-US", "user-agent": "DxPowerProject/2.0.2 (com.hb.jackery; build:3; iOS 26.4.2) Alamofire/5.11.2", "model": "iPhone18,4", "token": c.token, "Content-Type": content} {
		req.Header.Set(k, v)
	}
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := h.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Jackery HTTP %d", resp.StatusCode)
	}
	var m map[string]any
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&m); err != nil {
		return nil, err
	}
	code := int(number(m["code"]))
	msg := strings.ToLower(stringValue(m["msg"]))
	if code == 10402 || code == 401 || code == 1001 || code == 1002 || strings.Contains(msg, "logged in elsewhere") || (strings.Contains(msg, "token") && (strings.Contains(msg, "expir") || strings.Contains(msg, "invalid") || strings.Contains(msg, "auth"))) {
		c.token = ""
		return nil, ErrSessionContested
	}
	if code != 0 {
		return nil, fmt.Errorf("Jackery API code %d", code)
	}
	return m, nil
}
func (c *Cloud) login(ctx context.Context) error {
	if c.mqtt != nil {
		c.mqtt.Disconnect(0)
		c.mqtt = nil
	}
	if c.macID == "" {
		id := c.AndroidID
		if id == "" {
			id = "abcd1234567890ef"
		}
		hash := md5.Sum([]byte(id))
		hash[6] = (hash[6] & 15) | 48
		hash[8] = (hash[8] & 63) | 128
		c.macID = "2" + hex.EncodeToString(hash[:])
	}
	b, _ := json.Marshal(map[string]any{"account": c.Email, "loginType": 2, "macId": c.macID, "password": c.Password, "phone": "", "registerAppId": "com.hbxn.jackery", "verificationCode": ""})
	block, _ := aes.NewCipher([]byte(loginKey))
	b = pad(b)
	for i := 0; i < len(b); i += 16 {
		block.Encrypt(b[i:i+16], b[i:i+16])
	}
	der, _ := base64.StdEncoding.DecodeString(publicKey)
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return err
	}
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, key.(*rsa.PublicKey), []byte(loginKey))
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_, err = w.CreateFormFile("file", "")
	if err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	m, err := c.request(ctx, "POST", "/v1/auth/login", url.Values{"aesEncryptData": {base64.StdEncoding.EncodeToString(b)}, "rsaForAesKey": {base64.StdEncoding.EncodeToString(encrypted)}}, &buf, w.FormDataContentType())
	if err != nil {
		return err
	}
	nested := object(m["data"])
	find := func(keys ...string) string {
		if s := pick(m, keys...); s != "" {
			return s
		}
		return pick(nested, keys...)
	}
	c.token = find("token", "Token")
	c.userID = find("userId", "userid", "user_id")
	c.mqttKey = find("mqttPassWord", "mqttPassword", "mqtt_password")
	if c.token == "" {
		return errors.New("Jackery login returned no token")
	}
	return nil
}
func (c *Cloud) Login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.login(ctx)
}
func (c *Cloud) get(ctx context.Context, path string, q url.Values) (map[string]any, error) {
	if c.token == "" {
		if err := c.login(ctx); err != nil {
			return nil, err
		}
	}
	return c.request(ctx, "GET", path, q, nil, "application/json")
}
func (c *Cloud) Devices(ctx context.Context) ([]CloudDevice, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, err := c.get(ctx, "/v1/device/bind/list", nil)
	if err != nil {
		return nil, err
	}
	raw := m["data"]
	if obj, ok := raw.(map[string]any); ok {
		raw = obj["list"]
	}
	list, _ := raw.([]any)
	out := []CloudDevice{}
	for _, v := range list {
		d := object(v)
		id := pick(d, "devId", "id")
		if id == "" {
			continue
		}
		out = append(out, CloudDevice{id, pick(d, "devNickname", "devName", "deviceName", "modelName"), int(number(d["modelCode"])), pick(d, "devModel", "modelName"), pick(d, "devSn", "deviceCode", "sn")})
	}
	return out, nil
}
func (c *Cloud) Properties(ctx context.Context, id string) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, err := c.get(ctx, "/v1/device/property", url.Values{"deviceId": {id}})
	if err != nil {
		return nil, err
	}
	d := object(m["data"])
	p := object(d["properties"])
	for _, k := range []string{"packs", "subDevices", "expansionPacks", "extPacks", "battery_packs", "modules", "subBatteries", "batteryList", "battList"} {
		if v := object(d["device"])[k]; v != nil {
			p["_dev_"+k] = v
		}
	}
	return p, nil
}
func (c *Cloud) Packs(ctx context.Context, sn string) ([]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, err := c.get(ctx, "/v1/device/battery/pack/list", url.Values{"deviceSn": {sn}})
	if err != nil {
		return nil, err
	}
	raw, _ := m["data"].([]any)
	out := []any{}
	for _, v := range raw {
		d, ok := v.(map[string]any)
		if ok && d["isDelete"] != true && number(d["isDelete"]) == 0 {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return number(object(out[i])["deviceOrder"]) < number(object(out[j])["deviceOrder"])
	})
	return out, nil
}
func waitMQTT(ctx context.Context, t mqtt.Token) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.Done():
		return t.Error()
	}
}
func (c *Cloud) Command(ctx context.Context, sn, port string, on bool) (map[string]any, error) {
	actions := map[string]int{"ac": 4, "dc": 1, "usb": 2, "car": 3}
	props := map[string]string{"ac": "oac", "dc": "odc", "usb": "odcu", "car": "odcc"}
	action, ok := actions[port]
	if !ok || sn == "" {
		return nil, errors.New("valid port and device SN required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" {
		if err := c.login(ctx); err != nil {
			return nil, err
		}
	}
	if c.mqtt == nil {
		key, err := base64.StdEncoding.DecodeString(c.mqttKey)
		if err != nil || len(key) != 32 || c.userID == "" {
			return nil, errors.New("Jackery MQTT credentials missing or invalid")
		}
		user := c.userID + "@" + c.macID
		b := pad([]byte(user))
		block, _ := aes.NewCipher(key)
		cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(b, b)
		opts := mqtt.NewClientOptions().AddBroker("ssl://emqx.jackeryapp.com:8883").SetClientID(c.userID + "@APP").SetUsername(user).SetPassword(base64.StdEncoding.EncodeToString(b)).SetConnectTimeout(10 * time.Second).SetTLSConfig(c.TLSConfig)
		client := mqtt.NewClient(opts)
		if err = waitMQTT(ctx, client.Connect()); err != nil {
			client.Disconnect(0)
			return nil, err
		}
		c.mqtt = client
	}
	ts := time.Now().UnixMilli()
	v := 0
	if on {
		v = 1
	}
	b, _ := json.Marshal(map[string]any{"deviceSn": sn, "id": ts, "version": 0, "messageType": "DevicePropertyChange", "actionId": action, "timestamp": ts, "body": map[string]int{props[port]: v}})
	topic := "hb/app/" + c.userID + "/command"
	if err := waitMQTT(ctx, c.mqtt.Publish(topic, 1, false, b)); err != nil {
		return nil, err
	}
	return map[string]any{"port": port, "on": on, "action_id": action, "topic": topic}, nil
}
func (c *Cloud) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mqtt != nil {
		c.mqtt.Disconnect(0)
		c.mqtt = nil
	}
	c.token = ""
}
func CloudTelemetry(p map[string]any) map[string]any {
	f := func(k string) float64 { return number(p[k]) }
	i := func(k string) float64 { return math.Trunc(f(k)) }
	round := func(v float64) float64 { return math.RoundToEven(v*10) / 10 }
	out := map[string]any{"battery_percent": i("rb"), "battery_temp_c": round(f("bt") / 10), "input_power_w": i("ip"), "output_power_w": i("op"), "ac_input_w": i("acip"), "car_input_w": i("cip"), "solar_input_w": math.Max(0, i("ip")-i("acip")-i("cip")), "ac_output_v": round(f("acov") / 10), "ac_output_v_l1": round(f("acov1") / 10), "ac_output_hz": round(f("acohz")), "ac_on": i("oac") != 0, "dc_on": i("odc") != 0, "usb_on": i("odcu") != 0, "car_on": i("odcc") != 0, "ups_on": true, "super_charge_on": i("sfc") != 0, "error_code": i("ec"), "utc_offset_seconds": nil}
	// A missing/malformed SOC is unknown, never a zero-percent observation:
	// the rescue watchdog must not arm on numeric conversion defaults.
	soc, err := strconv.ParseFloat(fmt.Sprint(p["rb"]), 64)
	if err != nil || math.IsNaN(soc) || math.IsInf(soc, 0) || soc < 0 || soc > 100 {
		out["battery_percent"] = nil
	}
	if p["ups"] != nil {
		out["ups_on"] = i("ups") != 0
	}
	if p["uo"] != nil {
		out["utc_offset_seconds"] = i("uo")
	}
	if p["bs"] != nil {
		out["battery_status"] = i("bs")
	}
	for k, dst := range map[string]string{"it": "time_to_full_h", "ot": "time_remaining_h"} {
		v := i(k)
		if v == 999 {
			v = 0
		}
		out[dst] = v / 10
	}
	return out
}
