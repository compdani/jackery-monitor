package bridge

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"net/url"
	"os"
	"time"
)

type Push struct {
	SN, Kind string
	Body     map[string]any
	Packs    []any
	TS       float64
}

func TLSFromCA(path string) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if path == "" {
		return config, nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("broker CA file contains no certificates")
	}
	config.RootCAs = pool
	return config, nil
}

// ensureMQTT is called with c.mu held. Callbacks only enqueue bounded messages;
// they never call Native while its HTTP poll owns the lifecycle lock.
func (c *Cloud) ensureMQTT(ctx context.Context) error {
	if c.mqtt != nil {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(c.mqttKey)
	if err != nil || len(key) != 32 || c.userID == "" {
		return errors.New("Jackery MQTT credentials missing or invalid")
	}
	if c.pushes == nil {
		c.pushes = make(chan Push, 1024)
	}
	queue := c.pushes
	user := c.userID + "@" + c.macID
	b := pad([]byte(user))
	block, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(b, b)
	broker := c.Broker
	if broker == "" {
		broker = "ssl://emqx.jackeryapp.com:8883"
	}
	parsed, err := url.Parse(broker)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "ssl" && parsed.Scheme != "tls" && parsed.Scheme != "wss") {
		return errors.New("MQTT broker must use verified TLS (ssl/tls/wss)")
	}
	topic := "hb/app/" + c.userID + "/device"
	opts := mqtt.NewClientOptions().AddBroker(broker).SetClientID(c.userID + "@APP").SetUsername(user).SetPassword(base64.StdEncoding.EncodeToString(b)).SetConnectTimeout(10 * time.Second).SetTLSConfig(c.TLSConfig).SetAutoReconnect(true).SetKeepAlive(30 * time.Second).SetPingTimeout(10 * time.Second)
	opts.SetOnConnectHandler(func(client mqtt.Client) { client.Subscribe(topic, 1, nil) })
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, message mqtt.Message) {
		if len(message.Payload()) > 2<<20 {
			return
		}
		if push, ok := DecodePush(message.Payload(), float64(time.Now().UnixNano())/1e9); ok {
			select {
			case queue <- push:
			default:
				c.droppedPushes.Add(1)
			}
		}
	})
	factory := c.MQTTFactory
	if factory == nil {
		factory = mqtt.NewClient
	}
	client := factory(opts)
	if err := waitMQTT(ctx, client.Connect()); err != nil {
		client.Disconnect(0)
		return err
	}
	if err := waitMQTT(ctx, client.Subscribe(topic, 1, nil)); err != nil {
		client.Disconnect(0)
		return err
	}
	c.mqtt = client
	return nil
}
func (c *Cloud) Subscribe(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureMQTT(ctx)
}
func (c *Cloud) DrainPushes() []Push {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Push{}
	for {
		select {
		case p := <-c.pushes:
			out = append(out, p)
		default:
			return out
		}
	}
}
func DecodePush(data []byte, received float64) (Push, bool) {
	var p map[string]any
	if json.Unmarshal(data, &p) != nil {
		return Push{}, false
	}
	sn := stringValue(p["deviceSn"])
	if sn == "" {
		return Push{}, false
	}
	kind := stringValue(p["messageType"])
	body, ok := p["body"].(map[string]any)
	if !ok {
		return Push{}, false
	}
	ts := number(p["timestamp"])
	if ts > 1e12 {
		ts /= 1000
	}
	if ts <= 0 {
		ts = received
	}
	push := Push{SN: sn, Kind: kind, TS: ts, Body: body}
	switch kind {
	case "DevicePropertyChange":
		return push, true
	case "SubDevicePropertyChange":
		packs, ok := body["subDevices"].([]any)
		push.Packs = packs
		return push, ok
	default:
		return Push{}, false
	}
}
func (c *Cloud) MQTTStatus() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	connected := c.mqtt != nil && c.mqtt.IsConnected()
	return map[string]any{"connected": connected, "dropped_pushes": c.droppedPushes.Load()}
}
