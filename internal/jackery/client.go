// Package jackery implements the protocol already used by cloud_client.py.
package jackery

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"jackery-monitor/internal/secrets"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const baseURL = "https://iot.jackeryapp.com"
const publicKey = "MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCVmzgJy/4XolxPnkfu32YtJqYGFLYqf9/rnVgURJED+8J9J3Pccd6+9L97/+7COZE5OkejsgOkqeLNC9C3r5mhpE4zk/HStss7Q8/5DqkGD1annQ+eoICo3oi0dITZ0Qll56Dowb8lXi6WHViVDdih/oeUwVJY89uJNtTWrz7t7QIDAQAB"

var aesKey = []byte("1234567890123456")
var ErrContested = errors.New("cloud session was claimed by another client")
var ErrCredentials = errors.New("Jackery rejected the account credentials")

type Device struct {
	ID        string `json:"device_id"`
	Name      string `json:"name"`
	ModelCode int    `json:"model_code"`
	ModelName string `json:"model_name"`
	SN        string `json:"device_sn"`
}

// Client's HTTP and lifecycle methods are serialized by the live runtime.
// MQTT callbacks never access mutable client fields.
type Client struct {
	Creds                     secrets.Credentials
	HTTP                      *http.Client
	BaseURL, PublicKey, MacID string
	Token, UserID, MQTTKey    string
	mqtt                      mqtt.Client
	configureMQTT             func(*mqtt.ClientOptions)
	mqttState                 func(bool, string)
	packHandler               func(string, []map[string]any)
}

func New(creds secrets.Credentials) *Client {
	hash := md5.Sum([]byte("abcd1234567890ef"))
	hash[6] = (hash[6] & 0x0f) | 0x30
	hash[8] = (hash[8] & 0x3f) | 0x80
	return &Client{Creds: creds, HTTP: &http.Client{Timeout: 15 * time.Second}, BaseURL: baseURL, PublicKey: publicKey, MacID: "2" + hex.EncodeToString(hash[:])}
}
func pad(raw []byte) []byte {
	n := aes.BlockSize - len(raw)%aes.BlockSize
	return append(raw, bytes.Repeat([]byte{byte(n)}, n)...)
}
func loginQuery(creds secrets.Credentials, macID, key string) (url.Values, error) {
	raw, _ := json.Marshal(map[string]any{"account": creds.Email, "loginType": 2, "macId": macID, "password": creds.Password, "phone": "", "registerAppId": "com.hbxn.jackery", "verificationCode": ""})
	block, _ := aes.NewCipher(aesKey)
	plain := pad(raw)
	ct := make([]byte, len(plain))
	for i := 0; i < len(plain); i += aes.BlockSize {
		block.Encrypt(ct[i:i+aes.BlockSize], plain[i:i+aes.BlockSize])
	}
	der, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("invalid login public key")
	}
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, pub, aesKey)
	if err != nil {
		return nil, err
	}
	return url.Values{"aesEncryptData": {base64.StdEncoding.EncodeToString(ct)}, "rsaForAesKey": {base64.StdEncoding.EncodeToString(encrypted)}}, nil
}
func (c *Client) request(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path+"?"+query.Encode(), body)
	if err != nil {
		return nil, errors.New("invalid cloud request")
	}
	for k, v := range map[string]string{"accept": "*/*", "app_version": "2.0.2", "sys_version": "26.4.2", "platform": "1", "accept-language": "en-US", "user-agent": "DxPowerProject/2.0.2 (com.hb.jackery; build:3; iOS 26.4.2) Alamofire/5.11.2", "model": "iPhone18,4"} {
		req.Header.Set(k, v)
	}
	if c.Token != "" {
		req.Header.Set("token", c.Token)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Jackery cloud request failed; check connectivity")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 && path != "/v1/auth/login" {
		c.Token = ""
		return nil, ErrContested
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Jackery cloud HTTP %d", resp.StatusCode)
	}
	var result map[string]any
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	decoder.UseNumber()
	if err = decoder.Decode(&result); err != nil {
		return nil, errors.New("invalid cloud response")
	}
	if path != "/v1/auth/login" && contested(result) {
		c.Token = ""
		return nil, ErrContested
	}
	code, codeErr := strconv.Atoi(str(result["code"]))
	if codeErr != nil {
		return nil, errors.New("cloud response missing status code")
	}
	if code != 0 {
		if path == "/v1/auth/login" {
			return nil, ErrCredentials
		}
		return nil, fmt.Errorf("Jackery cloud error code %d", code)
	}
	return result, nil
}
func contested(data map[string]any) bool {
	code := int(number(data["code"]))
	msg := strings.ToLower(str(data["msg"]))
	return code == 10402 || code == 401 || code == 1001 || code == 1002 || (strings.Contains(msg, "token") && (strings.Contains(msg, "expir") || strings.Contains(msg, "invalid") || strings.Contains(msg, "auth")))
}
func (c *Client) Login(ctx context.Context) error {
	c.Close()
	c.Token = ""
	c.UserID = ""
	c.MQTTKey = ""
	query, err := loginQuery(c.Creds, c.MacID, c.PublicKey)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if _, err = writer.CreateFormFile("file", ""); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	data, err := c.request(ctx, "POST", "/v1/auth/login", query, &body, writer.FormDataContentType())
	if err != nil {
		return err
	}
	nested, _ := data["data"].(map[string]any)
	pick := func(keys ...string) string {
		for _, d := range []map[string]any{data, nested} {
			for _, k := range keys {
				if s := str(d[k]); s != "" {
					return s
				}
			}
		}
		return ""
	}
	c.Token = pick("token", "Token")
	c.UserID = pick("userId", "userid", "user_id")
	c.MQTTKey = pick("mqttPassWord", "mqttPassword", "mqtt_password")
	if c.Token == "" {
		return errors.New("cloud login returned no token")
	}
	return nil
}
func (c *Client) get(ctx context.Context, path string, q url.Values) (map[string]any, error) {
	if c.Token == "" {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
	}
	return c.request(ctx, "GET", path, q, nil, "application/json")
}
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	data, err := c.get(ctx, "/v1/device/bind/list", nil)
	if err != nil {
		return nil, err
	}
	raw := data["data"]
	if m, ok := raw.(map[string]any); ok {
		raw = m["list"]
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, errors.New("invalid cloud device list")
	}
	out := []Device{}
	for _, row := range rows {
		d, ok := row.(map[string]any)
		if !ok {
			continue
		}
		pick := func(keys ...string) string {
			for _, k := range keys {
				if s := str(d[k]); s != "" {
					return s
				}
			}
			return ""
		}
		device := Device{pick("devId", "id"), pick("devNickname", "devName", "deviceName", "modelName"), int(number(d["modelCode"])), pick("devModel", "modelName"), pick("devSn", "deviceCode", "sn")}
		if device.ID != "" && device.SN != "" {
			out = append(out, device)
		}
	}
	return out, nil
}
func (c *Client) Properties(ctx context.Context, id string) (map[string]any, error) {
	data, err := c.get(ctx, "/v1/device/property", url.Values{"deviceId": {id}})
	if err != nil {
		return nil, err
	}
	d, _ := data["data"].(map[string]any)
	props, ok := d["properties"].(map[string]any)
	if !ok {
		return nil, errors.New("cloud response missing properties")
	}
	return props, nil
}
func (c *Client) Close() {
	if c.mqtt != nil {
		c.mqtt.Disconnect(100)
		c.mqtt = nil
		if c.mqttState != nil {
			c.mqttState(false, "")
			c.mqttState = nil
		}
	}
}
func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
