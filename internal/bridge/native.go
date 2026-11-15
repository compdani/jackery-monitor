package bridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

type AccountStore interface {
	Load() (map[string]any, error)
	Save(map[string]any) error
	Clear() error
}

// Native is an in-process implementation of the legacy bridge seam.
// Only one poll logs in; a contested session cools down before reclaiming.
type Native struct {
	mu          sync.Mutex
	Cloud       *Cloud
	Accounts    AccountStore
	devices     []CloudDevice
	selected    string
	pausedUntil time.Time
	retryAfter  time.Time
	initialized bool
	events      []map[string]any
}

func (n *Native) event(kind string) {
	n.events = append(n.events, map[string]any{"ts": float64(time.Now().Unix()), "kind": kind, "level": "info", "category": "cloud", "message": kind, "extra": map[string]any{}})
	if len(n.events) > 500 {
		n.events = n.events[len(n.events)-500:]
	}
}
func (n *Native) Call(ctx context.Context, method string, p map[string]any) (map[string]any, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.initialized {
		if n.Cloud == nil && n.Accounts != nil {
			m, e := n.Accounts.Load()
			if e != nil {
				return nil, e
			}
			if m != nil {
				n.Cloud = &Cloud{Email: stringValue(m["email"]), Password: stringValue(m["password"]), Region: stringValue(m["region"])}
			}
		}
	}
	n.initialized = true
	switch method {
	case "get_events":
		since, _ := strconv.ParseFloat(fmt.Sprint(p["since"]), 64)
		limit, _ := strconv.Atoi(fmt.Sprint(p["limit"]))
		if limit <= 0 {
			limit = 100
		}
		limit = min(limit, 500)
		events := []map[string]any{}
		for _, e := range n.events {
			if e["ts"].(float64) > since {
				events = append(events, e)
			}
		}
		if len(events) > limit {
			events = events[len(events)-limit:]
		}
		return map[string]any{"ok": true, "events": events}, nil
	case "auth_status":
		email := ""
		if n.Cloud != nil {
			email = n.Cloud.Email
		}
		return map[string]any{"has_credentials": n.Cloud != nil, "email": email, "state": n.state()}, nil
	case "set_credentials":
		email, password := stringValue(p["email"]), stringValue(p["password"])
		if email == "" || password == "" {
			return nil, errors.New("email and password required")
		}
		c := &Cloud{Email: email, Password: password, Region: stringValue(p["region"])}
		if e := c.Login(ctx); e != nil {
			return nil, e
		}
		if n.Accounts != nil {
			if e := n.Accounts.Save(map[string]any{"email": email, "password": password, "region": c.Region}); e != nil {
				c.Close()
				return nil, e
			}
		}
		if n.Cloud != nil {
			n.Cloud.Close()
		}
		n.Cloud = c
		n.devices = nil
		n.selected = ""
		n.retryAfter = time.Time{}
		n.pausedUntil = time.Time{}
		n.event("credentials_set")
		return map[string]any{"ok": true}, nil
	case "clear_credentials":
		if n.Accounts != nil {
			if e := n.Accounts.Clear(); e != nil {
				return nil, e
			}
		}
		if n.Cloud != nil {
			n.Cloud.Close()
		}
		n.Cloud = nil
		n.devices = nil
		n.selected = ""
		n.event("credentials_cleared")
		return map[string]any{"ok": true}, nil
	case "pause_polling":
		seconds := number(p["seconds"])
		if seconds <= 0 {
			seconds = 600
		}
		if seconds > 86400 {
			seconds = 86400
		}
		n.pausedUntil = time.Now().Add(time.Duration(seconds) * time.Second)
		return map[string]any{"ok": true, "paused_until": n.pausedUntil.Unix()}, nil
	case "resume_polling", "connect":
		n.pausedUntil = time.Time{}
		n.retryAfter = time.Time{}
		return map[string]any{"ok": true}, nil
	case "disconnect":
		if n.Cloud != nil {
			n.Cloud.Close()
		}
		n.pausedUntil = time.Now().Add(24 * time.Hour)
		return map[string]any{"ok": true}, nil
	}
	if n.Cloud == nil {
		return nil, errors.New("Jackery credentials required")
	}
	if time.Now().Before(n.pausedUntil) || time.Now().Before(n.retryAfter) {
		return nil, errors.New("Jackery polling paused")
	}
	if len(n.devices) == 0 {
		d, e := n.Cloud.Devices(ctx)
		if e != nil {
			return nil, n.failure(e)
		}
		n.devices = d
	}
	if method == "select_device" {
		id := stringValue(p["device_id"])
		for _, d := range n.devices {
			if d.ID == id {
				n.selected = id
				return map[string]any{"ok": true, "device_id": id, "name": d.Name}, nil
			}
		}
		return nil, errors.New("unknown device")
	}
	var selected *CloudDevice
	for i := range n.devices {
		if n.devices[i].ID == n.selected || (n.selected == "" && i == 0) {
			selected = &n.devices[i]
			break
		}
	}
	if selected == nil {
		return nil, errors.New("no Jackery devices bound")
	}
	n.selected = selected.ID
	switch method {
	case "set_output":
		sn := stringValue(p["device_sn"])
		if sn == "" {
			sn = selected.SN
		}
		known := false
		for _, d := range n.devices {
			if d.SN == sn {
				known = true
			}
		}
		if !known {
			return nil, errors.New("unknown device SN")
		}
		on, ok := p["on"].(bool)
		if !ok {
			return nil, errors.New("boolean on required")
		}
		r, e := n.Cloud.Command(ctx, sn, stringValue(p["port"]), on)
		if e != nil {
			return nil, n.failure(e)
		}
		r["ok"] = true
		return r, nil
	case "poll", "status":
		props, e := n.Cloud.Properties(ctx, selected.ID)
		if e != nil {
			return nil, n.failure(e)
		}
		packs, e := n.Cloud.Packs(ctx, selected.SN)
		if e != nil {
			if errors.Is(e, ErrSessionContested) {
				return nil, n.failure(e)
			}
			packs = []any{}
		}
		return map[string]any{"device": selected.Info(), "telemetry": CloudTelemetry(props), "source": "cloud", "battery_packs": packs, "cloud": map[string]any{"state": "connected", "devices": n.devices, "selected_device_id": selected.ID}}, nil
	}
	return nil, errors.New("unsupported native bridge method")
}
func (n *Native) state() string {
	if n.Cloud == nil {
		return "needs_credentials"
	}
	if time.Now().Before(n.pausedUntil) {
		return "paused"
	}
	if time.Now().Before(n.retryAfter) {
		return "contested"
	}
	return "configured"
}
func (n *Native) failure(e error) error {
	if errors.Is(e, ErrSessionContested) {
		n.retryAfter = time.Now().Add(time.Minute)
		n.event("session_contested")
	}
	return e
}
