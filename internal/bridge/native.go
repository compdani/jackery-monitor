package bridge

import (
	"context"
	"encoding/json"
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
	Configure                        func(*Cloud) error
	PollInterval                     func() time.Duration
	Cooldown                         func() time.Duration
	Now                              func() time.Time
	cache                            map[string]*cachedDevice
	probes                           map[string]map[string]any
	nextHTTP, lastFresh, lastRestart time.Time
	firstPoll                        time.Time
	devicesRefreshed                 time.Time
	contested                        bool
	failures                         int
	lastFailure                      string
	mu                               sync.Mutex
	Cloud                            *Cloud
	Accounts                         AccountStore
	devices                          []CloudDevice
	selected                         string
	pausedUntil                      time.Time
	retryAfter                       time.Time
	initialized                      bool
	events                           []map[string]any
}

func (n *Native) event(kind string) {
	level, category := "info", "cloud"
	switch kind {
	case "watchdog_restart":
		level, category = "error", "watchdog"
	case "session_contested":
		level, category = "warn", "session"
	case "mqtt_subscribe_failed":
		level, category = "warn", "mqtt"
	case "poll_failed":
		level = "warn"
	}
	n.events = append(n.events, map[string]any{"ts": float64(n.clock().UnixNano()) / 1e9, "kind": kind, "level": level, "category": category, "message": kind, "extra": map[string]any{}})
	if len(n.events) > 500 {
		n.events = n.events[len(n.events)-500:]
	}
}
func (n *Native) Call(ctx context.Context, method string, p map[string]any) (map[string]any, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
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
	if n.Cloud != nil && !n.initialized {
		if err := n.configure(n.Cloud); err != nil {
			return nil, err
		}
	}
	n.initialized = true
	switch method {
	case "raw_props", "get_raw_props":
		sn := stringValue(p["device_sn"])
		if sn == "" {
			for _, d := range n.devices {
				if d.ID == n.selected {
					sn = d.SN
				}
			}
		}
		out := n.diagnostics(sn)
		out["device_sn"] = sn
		out["props"] = map[string]any{}
		if entry := n.cache[sn]; entry != nil {
			data, _ := json.Marshal(entry.Raw)
			var props map[string]any
			json.Unmarshal(data, &props)
			out["props"] = props
		}
		return out, nil
	case "probe_results":
		sn := stringValue(p["device_sn"])
		if result := n.probes[sn]; result != nil {
			return map[string]any{"device_sn": sn, "found": true, "in_flight": false, "results": result}, nil
		}
		return map[string]any{"device_sn": sn, "found": false, "in_flight": false}, nil
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
		if err := n.configure(c); err != nil {
			return nil, err
		}
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
		n.cache = nil
		n.probes = nil
		n.lastFresh = time.Time{}
		n.firstPoll = time.Time{}
		n.lastFailure = ""
		n.failures = 0
		n.nextHTTP = time.Time{}
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
		n.cache = nil
		n.probes = nil
		n.lastFresh = time.Time{}
		n.firstPoll = time.Time{}
		n.lastFailure = ""
		n.failures = 0
		n.nextHTTP = time.Time{}
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
		n.pausedUntil = n.clock().Add(time.Duration(seconds) * time.Second)
		return map[string]any{"ok": true, "paused_until": n.pausedUntil.Unix()}, nil
	case "resume_polling", "connect":
		n.pausedUntil = time.Time{}
		n.retryAfter = time.Time{}
		return map[string]any{"ok": true}, nil
	case "disconnect":
		if n.Cloud != nil {
			n.Cloud.Close()
		}
		n.pausedUntil = n.clock().Add(24 * time.Hour)
		return map[string]any{"ok": true}, nil
	}
	if n.Cloud == nil {
		return nil, errors.New("Jackery credentials required")
	}
	if n.clock().Before(n.pausedUntil) || n.clock().Before(n.retryAfter) {
		return nil, errors.New("Jackery polling paused")
	}
	if len(n.devices) == 0 || n.clock().Sub(n.devicesRefreshed) >= 10*time.Minute {
		d, e := n.Cloud.Devices(ctx)
		if e != nil {
			return nil, n.failure(e)
		}
		n.devices = d
		n.devicesRefreshed = n.clock()
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
	if selected == nil && len(n.devices) > 0 {
		selected = &n.devices[0]
	}
	if selected == nil {
		return nil, errors.New("no Jackery devices bound")
	}
	n.selected = selected.ID
	switch method {
	case "cloud_probe":
		target := *selected
		for _, d := range n.devices {
			if d.SN == stringValue(p["device_sn"]) {
				target = d
			}
		}
		results, err := n.Cloud.Probe(ctx, target)
		if err != nil {
			return nil, n.failure(err)
		}
		if n.probes == nil {
			n.probes = map[string]map[string]any{}
		}
		n.probes[target.SN] = results
		return map[string]any{"device_sn": target.SN, "results": results}, nil
	case "get_battery_packs":
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
		packs, err := n.Cloud.Packs(ctx, sn)
		if err != nil {
			return nil, n.failure(err)
		}
		stamp := float64(n.clock().UnixNano()) / 1e9
		if entry := n.cache[sn]; entry != nil {
			entry.Packs = packs
			entry.PackTS = stamp
		}
		return map[string]any{"packs": packs, "fetched_at": stamp, "cached": false}, nil
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
		bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		return n.pollFleet(bounded)
	}
	return nil, errors.New("unsupported native bridge method")
}
func (n *Native) state() string {
	if n.Cloud == nil {
		return "needs_credentials"
	}
	if n.clock().Before(n.pausedUntil) {
		return "paused"
	}
	if n.clock().Before(n.retryAfter) {
		if n.contested {
			return "contested"
		}
		return "retrying"
	}
	if len(n.cache) > 0 {
		return "connected"
	}
	return "configured"
}
func (n *Native) failure(err error) error {
	n.lastFailure = err.Error()
	n.failures = min(n.failures+1, 8)
	delay := min(10*time.Second*time.Duration(1<<n.failures), 5*time.Minute)
	n.contested = errors.Is(err, ErrSessionContested)
	if n.contested {
		delay = n.cooldownDuration()
		n.event("session_contested")
	} else {
		n.event("poll_failed")
	}
	n.retryAfter = n.clock().Add(delay)
	return err
}
