package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

// RealtimeLoop builds one snapshot per view per tick, shared across subscribers.
// Only successfully delivered fields advance a client's baseline. A dropped
// message is therefore retried, rather than leaving a permanently stale client.
func (s *Server) RealtimeLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.realtime.wake:
		}
		// Coalesce bursts and impose an idle floor even under sustained writes.
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		select {
		case <-s.realtime.wake:
		default:
		}
		s.pushRealtimeData(ctx)
	}
}

type realtimeSubscription struct{ cookieView string }

type realtimeBaseline struct {
	view         string
	subscription string
	generation   any
	at           time.Time
	fields       map[string]json.RawMessage
	queries      map[string]json.RawMessage
}

func realtimeQueryPath(topic string) (string, bool) {
	path, err := url.PathUnescape(strings.TrimPrefix(topic, "monitor.query."))
	if err != nil || len(path) > 600 {
		return "", false
	}
	u, err := url.ParseRequestURI(path)
	if err != nil {
		return "", false
	}
	switch u.Path {
	case "/api/energy/totals", "/api/energy/history", "/api/energy/daily", "/api/energy/devices":
	default:
		return "", false
	}
	for key := range u.Query() {
		switch key {
		case "device_sn", "hours", "bucket_s", "days":
		default:
			return "", false
		}
	}
	for key, limit := range map[string]int{"hours": 8760, "days": 365} {
		if raw := u.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > limit {
				return "", false
			}
		}
	}
	return path, true
}

func (s *Server) pushRealtimeData(ctx context.Context) {
	snapshots := map[string]map[string]json.RawMessage{}
	results := map[string]json.RawMessage{}
	for _, client := range s.Store.App.SubscriptionsBroker().Clients() {
		auth, _ := client.Get(apis.RealtimeClientAuthKey).(*core.Record)
		if auth == nil || auth.Collection().Name != "users" {
			continue
		}
		for subscription, options := range client.Subscriptions("monitor.data") {
			if subscription != "monitor.data" && !strings.HasPrefix(subscription, "monitor.data?") {
				continue
			}
			generation := client.Get("monitor.generation")
			view := options.Query["view"]
			if view == "" {
				if sub, ok := generation.(*realtimeSubscription); ok {
					view = sub.cookieView
				}
			}
			fields, ok := snapshots[view]
			if !ok {
				req, _ := http.NewRequestWithContext(ctx, "GET", "/api/status?"+url.Values{"view_device_id": {view}}.Encode(), nil)
				raw, err := json.Marshal(s.status(req))
				if err != nil {
					continue
				}
				if json.Unmarshal(raw, &fields) != nil {
					continue
				}
				snapshots[view] = fields
			}
			old, _ := client.Get("monitor.baseline").(*realtimeBaseline)
			full := old == nil || old.subscription != subscription || old.view != view
			if full {
				old = &realtimeBaseline{subscription: subscription}
			} else if old.generation != generation {
				// New query subscriptions need their initial results, while the
				// unchanged view keeps its status baseline and six-hour history.
				copy := *old
				copy.queries = nil
				old = &copy
			}
			patch := map[string]json.RawMessage{}
			for key, value := range fields {
				if !bytes.Equal(old.fields[key], value) {
					patch[key] = value
				}
			}
			for key := range old.fields {
				if _, ok := fields[key]; !ok {
					patch[key] = json.RawMessage("null")
				}
			}
			queryPatch := map[string]json.RawMessage{}
			nextQueries := map[string]json.RawMessage{}
			for topic := range client.Subscriptions("monitor.query.") {
				path, valid := realtimeQueryPath(topic)
				if !valid {
					continue
				}
				resultKey := view + "|" + path
				value, exists := results[resultKey]
				if !exists {
					req, _ := http.NewRequestWithContext(ctx, "GET", path, nil)
					req.AddCookie(&http.Cookie{Name: "view_device_id", Value: view})
					response := &energyResponse{header: make(http.Header)}
					s.energy(response, req)
					if response.status != 200 {
						continue
					} // retain last good while preparing
					value = append(json.RawMessage(nil), response.Bytes()...)
					results[resultKey] = value
				}
				nextQueries[path] = value
				if !bytes.Equal(old.queries[path], value) {
					queryPatch[path] = value
				}
			}
			if len(patch) == 0 && len(queryPatch) == 0 && time.Since(old.at) < 10*time.Second {
				continue
			}
			raw, err := json.Marshal(map[string]any{"kind": "data", "snapshot": full, "status": patch, "queries": queryPatch})
			if err != nil {
				continue
			}
			if client.Get(apis.RealtimeClientAuthKey) == auth && client.Get("monitor.generation") == generation && client.HasSubscription(subscription) && sendRealtime(client, subscriptions.Message{Name: "monitor.data", Data: raw}) {
				client.Set("monitor.baseline", &realtimeBaseline{subscription: subscription, view: view, generation: generation, at: time.Now(), fields: fields, queries: nextQueries})
			}
		}
	}
}
