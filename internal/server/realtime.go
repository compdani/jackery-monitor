package server

import (
	"crypto/sha256"
	"encoding/json"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
	"strings"
	"sync"
)

const fleetTopic = "monitor.fleet"

func deviceTopic(sn string) string { return "monitor.device." + sn }

type realtimeState struct {
	mu     sync.Mutex
	energy map[string][32]byte
}

// Custom topics use the same authenticated-user boundary as snapshot APIs.
// Messages are invalidations only: no credentials or device payload is broadcast.
func (s *Server) registerRealtime() {
	app := s.Store.App
	app.OnRealtimeSubscribeRequest().BindFunc(func(e *core.RealtimeSubscribeRequestEvent) error {
		for _, topic := range e.Subscriptions {
			if strings.HasPrefix(topic, "monitor.") && (e.Auth == nil || e.Auth.Collection().Name != "users") {
				return e.UnauthorizedError("auth_required", nil)
			}
		}
		return e.Next()
	})
	notify := func(e *core.RecordEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		if e.Record.Collection().Name == "devices" {
			sn := e.Record.GetString("device_sn")
			s.publishRealtime(deviceTopic(sn), map[string]any{"kind": "telemetry", "device_sn": sn})
		}
		s.publishRealtime(fleetTopic, map[string]any{"kind": "fleet"})
		return nil
	}
	app.OnRecordAfterCreateSuccess("devices", "device_prefs").BindFunc(notify)
	app.OnRecordAfterUpdateSuccess("devices", "device_prefs").BindFunc(notify)
	app.OnRecordAfterDeleteSuccess("devices", "device_prefs").BindFunc(notify)
	events := func(e *core.RecordEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		if e.Record.Collection().Name == "settings" {
			if e.Record.GetString("key") == "kasa_devices" {
				s.publishRealtime("monitor.events", map[string]any{"kind": "kasa_updated"})
			}
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(e.Record.GetString("payload")), &payload); err != nil {
			return nil
		}
		switch e.Record.Collection().Name {
		case "automation_decisions":
			if str(payload, "rule_id") != "" {
				s.publishRealtime("monitor.events", map[string]any{"kind": "automation_fired", "id": payload["rule_id"], "name": payload["rule_name"], "action": payload["action"], "last_fired": payload["fired_at"]})
			}
		case "events":
			if str(payload, "type") == "alert" {
				s.publishRealtime("monitor.events", map[string]any{"kind": "alert", "message": str(payload, "message")})
			}
		}
		return nil
	}
	app.OnRecordAfterCreateSuccess("automation_decisions", "events", "settings").BindFunc(events)
	app.OnRecordAfterUpdateSuccess("settings").BindFunc(events)

}
func (s *Server) publishRealtime(topic string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	message := subscriptions.Message{Name: topic, Data: raw}
	for _, client := range s.Store.App.SubscriptionsBroker().Clients() {
		auth, _ := client.Get(apis.RealtimeClientAuthKey).(*core.Record)
		if auth != nil && auth.Collection().Name == "users" && client.HasSubscription(topic) {
			sendRealtime(client, message)
		}
	}
}
func (s *Server) publishEnergy(sn string, body []byte) {
	hash := sha256.Sum256(body)
	state := &s.realtime
	state.mu.Lock()
	if state.energy == nil {
		state.energy = map[string][32]byte{}
	}
	old, ok := state.energy[sn]
	state.energy[sn] = hash
	state.mu.Unlock()
	if !ok || old != hash {
		s.publishRealtime(deviceTopic(sn), map[string]any{"kind": "energy", "device_sn": sn})
		// Fleet comparisons also need to refresh when a non-viewed device changes.
		s.publishRealtime(fleetTopic, map[string]any{"kind": "energy", "device_sn": sn})
	}
}

// A slow SSE connection must never stall telemetry persistence. Notifications
// are coalescible invalidations; clients reconcile on reconnect and fallback polls.
func sendRealtime(client subscriptions.Client, message subscriptions.Message) {
	defer func() { _ = recover() }() // broker may discard the channel concurrently
	select {
	case client.Channel() <- message:
	default:
	}
}
