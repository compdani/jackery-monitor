package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type bufferedRealtimeClient struct {
	*subscriptions.DefaultClient
	messages chan subscriptions.Message
}

func (c *bufferedRealtimeClient) Channel() chan subscriptions.Message { return c.messages }

func TestCustomRealtimeCookieAuthAndDeviceNotifications(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	client := &bufferedRealtimeClient{subscriptions.NewDefaultClient(), make(chan subscriptions.Message, 10)}
	s.Store.App.SubscriptionsBroker().Register(client)
	defer s.Store.App.SubscriptionsBroker().Unregister(client.Id())
	body, _ := json.Marshal(map[string]any{"clientId": client.Id(), "subscriptions": []string{deviceTopic("siseli:A")}})
	subscribe := func(auth bool) int {
		req := httptest.NewRequest("POST", "/api/realtime", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		if auth {
			req.AddCookie(&http.Cookie{Name: "jackery_session", Value: token})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if code := subscribe(false); code != 401 {
		t.Fatal("anonymous topic accepted", code)
	}
	// Repeat subscription proves cookie auth is installed before PocketBase's
	// previous-client-auth comparison, not just in the subscription hook.
	if code := subscribe(true); code != 204 {
		t.Fatal(code)
	}
	if code := subscribe(true); code != 204 {
		t.Fatal(code)
	}
	bearerReq := httptest.NewRequest("POST", "/api/realtime", strings.NewReader(string(body)))
	bearerReq.Header.Set("Content-Type", "application/json")
	bearerReq.Header.Set("Authorization", "Bearer "+token)
	bearerResponse := httptest.NewRecorder()
	h.ServeHTTP(bearerResponse, bearerReq)
	if bearerResponse.Code != 204 {
		t.Fatal("mobile bearer subscription", bearerResponse.Code, bearerResponse.Body.String())
	}
	if client.Get(apis.RealtimeClientAuthKey) == nil {
		t.Fatal("missing broker auth")
	}
	for _, sn := range []string{"siseli:A", "siseli:B"} {
		if err := s.Store.Put("devices", sn, sn, map[string]any{"device": map[string]any{"device_sn": sn}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(client.messages) != 1 {
		t.Fatal("device filtering", len(client.messages))
	}
	msg := <-client.messages
	if msg.Name != deviceTopic("siseli:A") {
		t.Fatal(msg)
	}
	rollback := errors.New("rollback")
	err := s.Store.App.RunInTransaction(func(app core.App) error {
		tx := &store.Store{App: app}
		if err := tx.Put("devices", "siseli:A", "siseli:A", map[string]any{"device": map[string]any{"device_sn": "siseli:A", "name": "uncommitted"}}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) || len(client.messages) != 0 {
		t.Fatal("uncommitted notification", err, len(client.messages))
	}
	s.publishEnergy("siseli:A", []byte(`{"today":1}`))
	s.publishEnergy("siseli:A", []byte(`{"today":1}`))
	if len(client.messages) != 1 {
		t.Fatal("duplicate energy notification")
	}
	client.Unset(apis.RealtimeClientAuthKey)
	s.publishRealtime(deviceTopic("siseli:A"), map[string]any{"kind": "telemetry"})
	if len(client.messages) != 1 {
		t.Fatal("anonymous subscriber received data")
	}
}

func TestRealtimeSlowSubscriberDoesNotBlock(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	client := subscriptions.NewDefaultClient()
	client.Subscribe(fleetTopic)
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client.Set(apis.RealtimeClientAuthKey, s.user(req))
	s.Store.App.SubscriptionsBroker().Register(client)
	defer s.Store.App.SubscriptionsBroker().Unregister(client.Id())
	// DefaultClient has an unbuffered channel and no receiver here.
	s.publishRealtime(fleetTopic, map[string]any{"kind": "fleet"})
}

func TestCustomAutomationAndKasaEvents(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	client := &bufferedRealtimeClient{subscriptions.NewDefaultClient(), make(chan subscriptions.Message, 10)}
	client.Subscribe("monitor.events")
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client.Set(apis.RealtimeClientAuthKey, s.user(req))
	s.Store.App.SubscriptionsBroker().Register(client)
	defer s.Store.App.SubscriptionsBroker().Unregister(client.Id())
	for _, item := range []struct {
		collection, key string
		payload         map[string]any
	}{
		{"automation_decisions", "firing", map[string]any{"rule_id": "rule-1", "rule_name": "Charge", "action": "on", "fired_at": 100.0}},
		{"settings", "kasa_devices", map[string]any{"devices": []any{}}},
		{"events", "alert", map[string]any{"type": "alert", "message": "Battery low"}},
	} {
		if err := s.Store.Put(item.collection, item.key, "A", item.payload); err != nil {
			t.Fatal(err)
		}
	}
	if len(client.messages) != 3 {
		t.Fatal("missing events", len(client.messages))
	}
	for _, kind := range []string{"automation_fired", "kasa_updated", "alert"} {
		msg := <-client.messages
		var body map[string]any
		if err := json.Unmarshal(msg.Data, &body); err != nil || body["kind"] != kind {
			t.Fatal(body, err)
		}
	}
}

func TestRealtimePayloadOverHTTP(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	sn := "siseli:payload"
	if err := s.Store.Put("devices", sn, sn, map[string]any{"device": map[string]any{"device_id": sn, "device_sn": sn, "name": "Payload device"}, "telemetry": map[string]any{"battery_percent": 49.0}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/realtime", nil)
	stream, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if !strings.Contains(stream.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(stream.Header)
	}
	reader := bufio.NewReader(stream.Body)
	readEvent := func(wanted string) map[string]any {
		t.Helper()
		event := ""
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event:") {
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if event == wanted && strings.HasPrefix(line, "data:") {
				var data map[string]any
				if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &data); err != nil {
					t.Fatal(err)
				}
				return data
			}
		}
	}
	hello := readEvent("PB_CONNECT")
	path := "/api/energy/devices"
	options, _ := json.Marshal(map[string]any{"query": map[string]string{"view": sn}})
	topics := []string{"monitor.data?options=" + url.QueryEscape(string(options)), "monitor.query." + url.PathEscape(path)}
	payload, _ := json.Marshal(map[string]any{"clientId": hello["clientId"], "subscriptions": topics})
	sub, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/realtime", bytes.NewReader(payload))
	sub.Header.Set("Content-Type", "application/json")
	sub.AddCookie(&http.Cookie{Name: "jackery_session", Value: token})
	response, err := http.DefaultClient.Do(sub)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal(response.StatusCode)
	}
	// Exercise the native HTTP SSE writer, not a fake buffered broker client.
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.pushRealtimeData(ctx)
			}
		}
	}()
	frame := readEvent("monitor.data")
	status := frame["status"].(map[string]any)
	if status["device"].(map[string]any)["device_sn"] != sn {
		t.Fatal(status)
	}
	queries := frame["queries"].(map[string]any)
	if len(queries[path].(map[string]any)["devices"].([]any)) != 1 {
		t.Fatal(queries)
	}
	// A subsequent frame carries the changed field; unchanged history isn't resent.
	if err := s.Store.Put("devices", sn, sn, map[string]any{"device": map[string]any{"device_id": sn, "device_sn": sn, "name": "Changed name"}}); err != nil {
		t.Fatal(err)
	}
	for {
		frame = readEvent("monitor.data")
		status = frame["status"].(map[string]any)
		device, _ := status["device"].(map[string]any)
		if device["name"] == "Changed name" {
			break
		}
	}
	if _, exists := status["history"]; exists {
		t.Fatal("unchanged history resent")
	}
}

func TestRealtimeQueryAllowlist(t *testing.T) {
	for _, path := range []string{"/api/secrets", "/api/energy/history?hours=999999", "/api/energy/totals?token=secret"} {
		if _, ok := realtimeQueryPath("monitor.query." + url.PathEscape(path)); ok {
			t.Fatal("accepted", path)
		}
	}
}

func TestRealtimeDroppedPayloadRetainsBaseline(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	client := &bufferedRealtimeClient{subscriptions.NewDefaultClient(), make(chan subscriptions.Message, 1)}
	client.Subscribe("monitor.data")
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client.Set(apis.RealtimeClientAuthKey, s.user(req))
	s.Store.App.SubscriptionsBroker().Register(client)
	defer s.Store.App.SubscriptionsBroker().Unregister(client.Id())
	client.messages <- subscriptions.Message{Name: "busy"}
	s.pushRealtimeData(context.Background())
	if client.Get("monitor.baseline") != nil {
		t.Fatal("dropped payload advanced baseline")
	}
	<-client.messages
	s.pushRealtimeData(context.Background())
	if client.Get("monitor.baseline") == nil {
		t.Fatal("successful payload did not advance baseline")
	}
	message := <-client.messages
	var body map[string]any
	if err := json.Unmarshal(message.Data, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["status"].(map[string]any)["connection_status"]; !ok {
		t.Fatal("retry omitted initial snapshot", body)
	}
	client.Subscribe("monitor.query." + url.PathEscape("/api/energy/devices"))
	client.Set("monitor.generation", &realtimeSubscription{})
	s.pushRealtimeData(context.Background())
	message = <-client.messages
	if err := json.Unmarshal(message.Data, &body); err != nil {
		t.Fatal(err)
	}
	if body["snapshot"] != false || len(body["status"].(map[string]any)) != 0 {
		t.Fatal("query update resent full status", body)
	}
	if _, ok := body["queries"].(map[string]any)["/api/energy/devices"]; !ok {
		t.Fatal("new query has no initial result", body)
	}
}

func TestRealtimeCookieViewResolution(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	for _, sn := range []string{"device-A", "device-B"} {
		if err := s.Store.Put("devices", sn, sn, map[string]any{"device": map[string]any{"device_id": sn, "device_sn": sn}}); err != nil {
			t.Fatal(err)
		}
	}
	client := &bufferedRealtimeClient{subscriptions.NewDefaultClient(), make(chan subscriptions.Message, 10)}
	s.Store.App.SubscriptionsBroker().Register(client)
	defer s.Store.App.SubscriptionsBroker().Unregister(client.Id())
	for _, test := range []struct{ option, want string }{{"", "device-B"}, {"device-A", "device-A"}} {
		options, _ := json.Marshal(map[string]any{"query": map[string]string{"view": test.option}})
		topics := []string{"monitor.data?options=" + url.QueryEscape(string(options)), "monitor.query." + url.PathEscape("/api/energy/devices")}
		payload, _ := json.Marshal(map[string]any{"clientId": client.Id(), "subscriptions": topics})
		req := httptest.NewRequest("POST", "/api/realtime", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "jackery_session", Value: token})
		req.AddCookie(&http.Cookie{Name: "view_device_id", Value: "device-B"})
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != 204 {
			t.Fatal(response.Code, response.Body.String())
		}
		s.pushRealtimeData(context.Background())
		select {
		case message := <-client.messages:
			var body map[string]any
			if err := json.Unmarshal(message.Data, &body); err != nil {
				t.Fatal(err)
			}
			if body["snapshot"] != true {
				t.Fatal("view change was not a full snapshot", body)
			}
			status := body["status"].(map[string]any)
			if status["device"].(map[string]any)["device_sn"] != test.want {
				t.Fatal("wrong status view", test, status)
			}
			devices := body["queries"].(map[string]any)["/api/energy/devices"].(map[string]any)
			if devices["selected_device_id"] != test.want {
				t.Fatal("mixed energy view", test, devices)
			}
		default:
			t.Fatal("missing payload")
		}
	}
}
