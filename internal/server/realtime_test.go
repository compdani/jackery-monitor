package server

import (
	"encoding/json"
	"errors"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
