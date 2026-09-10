package jackery

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"os"
	"time"
)

var ports = map[string]struct {
	Action   int
	Property string
}{"ac": {4, "oac"}, "dc": {1, "odc"}, "usb": {2, "odcu"}, "car": {3, "odcc"}}

func ValidPort(port string) bool      { _, ok := ports[port]; return ok }
func PortProperty(port string) string { return ports[port].Property }
func MQTTPassword(userID, macID, encodedKey string) (string, string, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return "", "", errors.New("MQTT credentials missing or invalid")
	}
	username := userID + "@" + macID
	block, _ := aes.NewCipher(key)
	plain := pad([]byte(username))
	ct := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(ct, plain)
	return username, base64.StdEncoding.EncodeToString(ct), nil
}
func Command(sn, port string, on bool, now time.Time) (map[string]any, error) {
	p, ok := ports[port]
	if !ok || sn == "" {
		return nil, errors.New("valid device serial and output port required")
	}
	value := 0
	if on {
		value = 1
	}
	ms := now.UnixMilli()
	return map[string]any{"deviceSn": sn, "id": ms, "version": 0, "messageType": "DevicePropertyChange", "actionId": p.Action, "timestamp": ms, "body": map[string]int{p.Property: value}}, nil
}
func wait(ctx context.Context, token mqtt.Token) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-token.Done():
		return token.Error()
	}
}
func (c *Client) Subscribe(ctx context.Context, onDelta func(string, map[string]any), onState func(bool, string)) error {
	if c.mqtt != nil {
		return nil
	}
	if c.UserID == "" {
		return errors.New("MQTT user ID missing from cloud login")
	}
	c.mqttState = onState
	username, password, err := MQTTPassword(c.UserID, c.MacID, c.MQTTKey)
	if err != nil {
		return err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if file := os.Getenv("JACKERY_MQTT_CA_FILE"); file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return errors.New("cannot read MQTT CA file")
		}
		roots, _ := x509.SystemCertPool()
		if roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(raw) {
			return errors.New("invalid MQTT CA file")
		}
		tlsConfig.RootCAs = roots
	}
	// Legacy Python disabled verification for Jackery's private broker CA. Go
	// requires explicitly opting into that compatibility mode when no CA is available.
	tlsConfig.InsecureSkipVerify = os.Getenv("JACKERY_MQTT_INSECURE") == "1"
	topic := "hb/app/" + c.UserID + "/device"
	opts := mqtt.NewClientOptions().AddBroker("ssl://emqx.jackeryapp.com:8883").SetClientID(c.UserID + "@APP").SetUsername(username).SetPassword(password).SetTLSConfig(tlsConfig).SetConnectTimeout(10 * time.Second).SetKeepAlive(30 * time.Second).SetAutoReconnect(true).SetMaxReconnectInterval(30 * time.Second).SetWriteTimeout(5 * time.Second)
	packHandler := c.packHandler
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, msg mqtt.Message) {
		if msg.Topic() != topic || len(msg.Payload()) > 1<<20 {
			return
		}
		if packHandler != nil {
			if sn, packs, ok := ParsePacks(msg.Payload()); ok {
				packHandler(sn, packs)
				return
			}
		}
		sn, body, ok := ParseDelta(msg.Payload())
		if ok {
			onDelta(sn, body)
		}
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, _ error) { onState(false, "MQTT disconnected; reconnecting") })
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		go func() {
			token := client.Subscribe(topic, 1, nil)
			if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
				onState(false, "MQTT subscription failed")
				return
			}
			onState(true, "")
		}()
	})
	if c.configureMQTT != nil {
		c.configureMQTT(opts)
	}
	client := mqtt.NewClient(opts)
	if err := wait(ctx, client.Connect()); err != nil {
		client.Disconnect(0)
		return errors.New("MQTT connection failed; check broker connectivity and CA configuration")
	}
	c.mqtt = client
	return nil
}
func ParseDelta(raw []byte) (string, map[string]any, bool) {
	var p struct {
		Type string         `json:"messageType"`
		SN   string         `json:"deviceSn"`
		Body map[string]any `json:"body"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Type != "DevicePropertyChange" || p.SN == "" || p.Body == nil {
		return "", nil, false
	}
	return p.SN, p.Body, true
}
func (c *Client) SetOutput(ctx context.Context, sn, port string, on bool) error {
	if c.mqtt == nil || !c.mqtt.IsConnectionOpen() {
		return errors.New("MQTT is not connected")
	}
	command, err := Command(sn, port, on, time.Now())
	if err != nil {
		return err
	}
	raw, err := json.Marshal(command)
	if err != nil {
		return err
	}
	if err = wait(ctx, c.mqtt.Publish("hb/app/"+c.UserID+"/command", 1, false, raw)); err != nil {
		return fmt.Errorf("output command was not acknowledged by the broker")
	}
	return nil
}
