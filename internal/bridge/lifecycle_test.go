package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type readyMQTTToken struct{ mqtt.Token }

func (readyMQTTToken) Done() <-chan struct{} { c := make(chan struct{}); close(c); return c }
func (readyMQTTToken) Error() error          { return nil }

type fakeMQTTClient struct {
	mqtt.Client
	opts          *mqtt.ClientOptions
	subscriptions int
	closed        bool
}

func (f *fakeMQTTClient) Connect() mqtt.Token {
	if f.opts.OnConnect != nil {
		f.opts.OnConnect(f)
	}
	return readyMQTTToken{}
}
func (f *fakeMQTTClient) Subscribe(string, byte, mqtt.MessageHandler) mqtt.Token {
	f.subscriptions++
	return readyMQTTToken{}
}
func (f *fakeMQTTClient) Disconnect(uint)                            { f.closed = true }
func (f *fakeMQTTClient) IsConnected() bool                          { return !f.closed }
func (f *fakeMQTTClient) Publish(string, byte, bool, any) mqtt.Token { return readyMQTTToken{} }

type fakeMQTTMessage struct {
	mqtt.Message
	data []byte
}

func (f fakeMQTTMessage) Payload() []byte { return f.data }
func TestNativeFleetPushCacheAndSourceTimestamps(t *testing.T) {
	var requests atomic.Int32
	var fail atomic.Bool
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/v1/device/bind/list":
			ioJSON(w, `{"code":0,"data":[{"devId":1,"devSn":"A","devNickname":"Main","modelCode":22},{"devId":2,"devSn":"B","devNickname":"Other","modelCode":22}]}`)
		case "/v1/device/property":
			if fail.Load() {
				w.WriteHeader(503)
				return
			}
			rb := 10
			if r.URL.Query().Get("deviceId") == "2" {
				rb = 40
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"properties": map[string]any{"rb": rb, "ip": 100}}})
		case "/v1/device/battery/pack/list":
			ioJSON(w, `{"code":0,"data":[{"rb":80,"deviceSn":"pack"}]}`)
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer h.Close()
	var driver *fakeMQTTClient
	c := &Cloud{BaseURL: h.URL, token: "token", userID: "user", macID: "mac", mqttKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), MQTTFactory: func(opts *mqtt.ClientOptions) mqtt.Client { driver = &fakeMQTTClient{opts: opts}; return driver }}
	now := time.Unix(1700000000, 0)
	n := &Native{Cloud: c, Now: func() time.Time { return now }}
	first, err := n.Call(context.Background(), "poll", nil)
	if err != nil {
		t.Fatal(err)
	}
	fleet := object(object(first["cloud"])["devices_telemetry"])
	if len(fleet) != 2 || object(object(fleet["B"])["telemetry"])["battery_percent"] != 40.0 {
		t.Fatal(first)
	}
	if driver.subscriptions != 2 {
		t.Fatal("initial/reconnect subscription absent", driver)
	}
	driver.opts.OnConnect(driver)
	if driver.subscriptions != 3 {
		t.Fatal("reconnect did not subscribe")
	}
	count := requests.Load()
	now = now.Add(2 * time.Second)
	driver.opts.DefaultPublishHandler(driver, fakeMQTTMessage{data: []byte(`{"messageType":"DevicePropertyChange","deviceSn":"B","timestamp":1700000002000,"body":{"rb":42,"op":500}}`)})
	driver.opts.DefaultPublishHandler(driver, fakeMQTTMessage{data: []byte(`{"messageType":"SubDevicePropertyChange","deviceSn":"B","timestamp":1700000002000,"body":{"subDevices":[{"rb":55}]}}`)})
	second, err := n.Call(context.Background(), "poll", nil)
	if err != nil {
		t.Fatal(err)
	}
	fleet = object(object(second["cloud"])["devices_telemetry"])
	entry := object(fleet["B"])
	if requests.Load() != count || entry["ts"] != 1700000002.0 || object(entry["telemetry"])["battery_percent"] != 42.0 || entry["pack_ts"] != 1700000002.0 {
		t.Fatal(second)
	}
	original := object(object(first["cloud"])["devices_telemetry"])
	if object(object(original["B"])["telemetry"])["battery_percent"] != 40.0 {
		t.Fatal("response maps shared with cache")
	}
	driver.opts.DefaultPublishHandler(driver, fakeMQTTMessage{data: []byte(`{"messageType":"DevicePropertyChange","deviceSn":"B","timestamp":1699999999000,"body":{"rb":99}}`)})
	n.Call(context.Background(), "select_device", map[string]any{"device_id": "2"})
	selected, err := n.Call(context.Background(), "poll", nil)
	if err != nil || object(selected["device"])["device_sn"] != "B" || object(selected["telemetry"])["battery_percent"] != 42.0 {
		t.Fatal(selected, err)
	}
	diag, err := n.Call(context.Background(), "raw_props", map[string]any{"device_sn": "B"})
	if err != nil || len(object(diag["devices_raw"])) != 1 {
		t.Fatal(diag, err)
	}
	fail.Store(true)
	now = now.Add(60 * time.Second)
	if _, err = n.Call(context.Background(), "poll", nil); err == nil {
		t.Fatal("failed cloud was treated as fresh")
	}
	if n.cache["B"].TS != 1700000002 {
		t.Fatal("failure advanced source timestamp")
	}
	before := requests.Load()
	if _, err = n.Call(context.Background(), "poll", nil); err == nil || requests.Load() != before {
		t.Fatal("backoff did not suppress HTTP")
	}
}
func TestNativeBackoffWatchdogAndCA(t *testing.T) {
	now := time.Unix(1700000000, 0)
	n := &Native{Now: func() time.Time { return now }, Cooldown: func() time.Duration { return 70 * time.Second }}
	n.failure(errors.New("outage"))
	if n.retryAfter.Sub(now) != 20*time.Second {
		t.Fatal(n.retryAfter)
	}
	for i := 0; i < 1000; i++ {
		n.failure(errors.New("outage"))
	}
	if n.failures != 8 || n.retryAfter.Sub(now) != 300*time.Second {
		t.Fatal(n)
	}
	n.failure(ErrSessionContested)
	if n.retryAfter.Sub(now) != 70*time.Second {
		t.Fatal("contention cooldown ignored")
	}
	cfg, err := TLSFromCA("")
	if err != nil || cfg.InsecureSkipVerify {
		t.Fatal(cfg, err)
	}
	if _, err = TLSFromCA("/nonexistent/synthetic-ca.pem"); err == nil {
		t.Fatal("missing CA accepted")
	}
	for _, data := range []string{`{}`, `{"messageType":"Notice","deviceSn":"A","body":{}}`, `{"messageType":"DevicePropertyChange","deviceSn":"A","body":[]}`} {
		if _, ok := DecodePush([]byte(data), 1); ok {
			t.Fatal("invalid push accepted", data)
		}
	}
}
func TestWatchdogRecyclesStalledSessionWithoutActuation(t *testing.T) {
	n := &Native{Cloud: &Cloud{}, cache: map[string]*cachedDevice{}, devices: []CloudDevice{{ID: "1", SN: "A"}}, lastFresh: time.Unix(1, 0)}
	now := time.Unix(1000, 0)
	n.Now = func() time.Time { return now }
	// Canceled context proves watchdog reset occurs without network I/O.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n.pollFleet(ctx)
	if !n.lastRestart.Equal(now) {
		t.Fatal("watchdog did not recycle session")
	}
	previous := n.lastRestart
	now = now.Add(time.Minute)
	n.pollFleet(ctx)
	if !n.lastRestart.Equal(previous) {
		t.Fatal("watchdog restart throttle ignored")
	}
}

type cancelCloudTransport struct{}

func (cancelCloudTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, context.Canceled
}
func TestRealtimeDeliversWithoutAnHTTPPoll(t *testing.T) {
	c := &Cloud{pushes: make(chan Push, 1)}
	now := time.Unix(1700000000, 0)
	n := &Native{Cloud: c, Now: func() time.Time { return now }, selected: "1", cache: map[string]*cachedDevice{"A": {Device: CloudDevice{ID: "1", SN: "A"}, Raw: map[string]any{"rb": 40.0}, TS: 1700000000.0}}}
	c.pushes <- Push{SN: "A", Kind: "DevicePropertyChange", Body: map[string]any{"rb": 41.0}, TS: 1700000001.0}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	delivered := make(chan map[string]any, 1)
	done := make(chan struct{})
	go func() { n.Realtime(ctx, func(p map[string]any) { delivered <- p }); close(done) }()
	select {
	case p := <-delivered:
		if object(p["telemetry"])["battery_percent"] != 41.0 || p["last_update_ts"] != 1700000001.0 {
			t.Error(p)
		}
	case <-time.After(4 * time.Second):
		t.Error("push waited for HTTP polling")
	}
	cancel()
	<-done
}
func TestProbeUsesOnlyLegacyDiagnosticRequestShapes(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.URL.Path != "/v1/device/version" && r.URL.Path != "/v1/device/battery/pack/list" {
			t.Error("unexpected probe path", r.URL.Path)
		}
		if r.URL.Path == "/v1/device/battery/pack/list" && r.Method != "GET" {
			t.Error("pack probe must be read-only GET")
		}
		ioJSON(w, `{"code":0,"data":{"version":"synthetic"}}`)
	}))
	defer server.Close()
	c := &Cloud{BaseURL: server.URL, token: "synthetic"}
	results, err := c.Probe(context.Background(), CloudDevice{ID: "1", SN: "A", ModelCode: 22})
	if err != nil || count.Load() != 11 || len(results) != 11 {
		t.Fatal(len(results), count.Load(), err)
	}
}
