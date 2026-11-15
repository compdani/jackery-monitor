package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

type cachedDevice struct {
	Device     CloudDevice
	Raw        map[string]any
	TS, PackTS float64
	Packs      []any
}

func (n *Native) clock() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}
func (n *Native) pollDuration() time.Duration {
	if n.PollInterval != nil {
		return n.PollInterval()
	}
	return time.Minute
}
func (n *Native) cooldownDuration() time.Duration {
	if n.Cooldown != nil {
		return n.Cooldown()
	}
	return time.Minute
}
func (n *Native) configure(c *Cloud) error {
	if n.Configure != nil {
		return n.Configure(c)
	}
	return nil
}
func (n *Native) consumePushes() bool {
	changed := false
	if n.cache == nil {
		n.cache = map[string]*cachedDevice{}
	}
	for _, push := range n.Cloud.DrainPushes() {
		entry := n.cache[push.SN]
		if entry == nil || push.TS > float64(n.clock().UnixNano())/1e9+5 {
			continue
		}
		if push.Kind == "SubDevicePropertyChange" {
			if push.TS >= entry.PackTS {
				entry.Packs = validPushPacks(push.Packs)
				entry.PackTS = push.TS
				changed = true
			}
			continue
		}
		if push.TS < entry.TS {
			continue
		}
		if entry.Raw == nil {
			entry.Raw = map[string]any{}
		}
		for k, v := range push.Body {
			entry.Raw[k] = v
		}
		entry.TS = push.TS
		changed = true
		n.lastFresh = n.clock()
	}
	return changed
}
func (n *Native) pollFleet(ctx context.Context) (map[string]any, error) {
	n.consumePushes()
	now := n.clock()
	if n.firstPoll.IsZero() {
		n.firstPoll = now
	}
	if now.Before(n.nextHTTP) && len(n.cache) > 0 {
		return n.fleetSnapshot(), nil
	}
	// A bounded poll context and HTTP timeout prevent indefinite hangs. If no
	// observation arrives for ten minutes, recycle auth/MQTT at most every 5m.
	baseline := n.lastFresh
	if baseline.IsZero() {
		baseline = n.firstPoll
	}
	if now.Sub(baseline) >= 10*time.Minute && now.Sub(n.lastRestart) >= 5*time.Minute {
		n.Cloud.Close()
		n.lastRestart = now
		n.event("watchdog_restart")
	}
	ordered := append([]CloudDevice{}, n.devices...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID == n.selected && ordered[j].ID != n.selected })
	success := false
	var lastErr error
	for _, d := range ordered {
		if d.SN == "" {
			continue
		}
		entry := n.cache[d.SN]
		if entry == nil {
			entry = &cachedDevice{Device: d, Raw: map[string]any{}, Packs: []any{}}
			n.cache[d.SN] = entry
		}
		entry.Device = d
		props, err := n.Cloud.Properties(ctx, d.ID)
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrSessionContested) {
				return nil, n.failure(err)
			}
			continue
		}
		if len(props) == 0 {
			lastErr = errors.New("empty Jackery properties")
			continue
		}
		stamp := float64(n.clock().UnixNano()) / 1e9
		for k, v := range props {
			entry.Raw[k] = v
		}
		entry.TS = stamp
		n.lastFresh = n.clock()
		success = true
		packs, err := n.Cloud.Packs(ctx, d.SN)
		if err == nil {
			entry.Packs = packs
			entry.PackTS = stamp
		} else if errors.Is(err, ErrSessionContested) {
			return nil, n.failure(err)
		} else {
			lastErr = err
		}
	}
	if !success {
		if lastErr == nil {
			lastErr = errors.New("no device observations")
		}
		return nil, n.failure(lastErr)
	}
	n.failures = 0
	n.retryAfter = time.Time{}
	n.nextHTTP = now.Add(n.pollDuration())
	n.lastFailure = ""
	if lastErr != nil {
		n.lastFailure = lastErr.Error()
	}
	if err := n.Cloud.Subscribe(ctx); err != nil {
		n.lastFailure = "MQTT subscribe: " + err.Error()
		n.event("mqtt_subscribe_failed")
	}
	n.consumePushes()
	return n.fleetSnapshot(), nil
}
func (n *Native) fleetSnapshot() map[string]any {
	fleet := map[string]any{}
	var active *cachedDevice
	for sn, entry := range n.cache {
		if entry.TS <= 0 {
			continue
		}
		fleet[sn] = map[string]any{"device": entry.Device.Info(), "telemetry": CloudTelemetry(entry.Raw), "ts": entry.TS, "battery_packs": entry.Packs, "pack_ts": entry.PackTS}
		if entry.Device.ID == n.selected {
			active = entry
		}
	}
	connection := "connected"
	var connectionError any
	if n.lastFailure != "" {
		connection = "degraded"
		connectionError = n.lastFailure
	}
	out := map[string]any{"connection_status": connection, "connection_error": connectionError, "source": "cloud", "cloud": map[string]any{"state": "connected", "devices": n.devices, "selected_device_id": n.selected, "devices_telemetry": fleet, "last_error": n.lastFailure, "mqtt": n.Cloud.MQTTStatus(), "retry_after": n.retryAfter.Unix()}}
	if active != nil {
		out["device"] = active.Device.Info()
		out["telemetry"] = CloudTelemetry(active.Raw)
		out["last_update_ts"] = active.TS
		out["battery_packs"] = active.Packs
		out["pack_ts"] = active.PackTS
	}
	// Never share maps with subsequent push updates or HTTP callers.
	data, _ := json.Marshal(out)
	var clone map[string]any
	json.Unmarshal(data, &clone)
	return clone
}
func (n *Native) diagnostics(sn string) map[string]any {
	out := map[string]any{"state": n.state(), "selected_device_id": n.selected, "devices": n.devices, "last_error": n.lastFailure, "failures": n.failures, "retry_after": n.retryAfter.Unix(), "watchdog_last_restart": n.lastRestart.Unix()}
	raw := map[string]any{}
	for serial, entry := range n.cache {
		if sn == "" || serial == sn {
			raw[serial] = map[string]any{"properties": entry.Raw, "source_ts": entry.TS, "pack_ts": entry.PackTS, "packs": entry.Packs}
		}
	}
	out["devices_raw"] = raw
	data, _ := json.Marshal(out)
	var cloned map[string]any
	json.Unmarshal(data, &cloned)
	return cloned
}

func validPushPacks(raw []any) []any {
	out := []any{}
	for _, p := range raw {
		pack, ok := p.(map[string]any)
		if ok && pack["isDelete"] != true && number(pack["isDelete"]) == 0 {
			out = append(out, pack)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return number(object(out[i])["deviceOrder"]) < number(object(out[j])["deviceOrder"])
	})
	return out
}
func (n *Native) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Cloud != nil {
		n.Cloud.Close()
	}
}

// Realtime delivers accepted property/pack pushes independently of UI polling.
// Never wait behind a cloud poll here: network calls have a 45-second bound.
func (n *Native) Realtime(ctx context.Context, deliver func(map[string]any)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !n.mu.TryLock() {
				continue
			}
			var snapshot map[string]any
			if n.Cloud != nil && len(n.cache) > 0 && n.consumePushes() {
				snapshot = n.fleetSnapshot()
			}
			n.mu.Unlock()
			if snapshot != nil {
				deliver(snapshot)
			}
		}
	}
}

// Probe ports the legacy read-only version/pack diagnostic request shapes.
func (c *Cloud) Probe(ctx context.Context, d CloudDevice) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" {
		if err := c.login(ctx); err != nil {
			return nil, err
		}
	}
	attempts := []map[string]any{{"deviceSn": d.SN}, {"deviceSn": d.SN}, {"deviceSn": d.SN, "modelCode": d.ModelCode}, {"deviceId": d.ID, "deviceSn": d.SN, "modelCode": d.ModelCode}, {"deviceCode": d.SN}, {"deviceCode": d.SN, "modelCode": d.ModelCode}, {"sn": d.SN}, {"deviceId": d.ID}, {}, {"deviceSnList": []string{d.SN}}, {"deviceSn": d.SN}}
	results := map[string]any{}
	for i, params := range attempts {
		method, path := "POST", "/v1/device/version"
		if i == 0 {
			method, path = "GET", "/v1/device/battery/pack/list"
		}
		if i == 10 {
			method = "GET"
		}
		q := url.Values{}
		var body io.Reader
		data, _ := json.Marshal(params)
		if method == "GET" {
			for k, v := range params {
				q.Set(k, fmt.Sprint(v))
			}
		} else {
			body = bytes.NewReader(data)
		}
		keys := []string{}
		for key := range params {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		shape := strings.Join(keys, ",")
		if shape == "" {
			shape = "-"
		}
		key := fmt.Sprintf("%02d %s %s?%s", i, method, path, shape)
		full, err := c.request(ctx, method, path, q, body, "application/json")
		if err != nil {
			results[key] = map[string]any{"error": err.Error()}
			if errors.Is(err, ErrSessionContested) {
				return nil, err
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		} else {
			results[key] = map[string]any{"sent": params, "full": full}
		}
	}
	return results, nil
}
