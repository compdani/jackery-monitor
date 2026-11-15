package server

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/ble"
)

type bmsStore struct{ settingsDoc }

func (b bmsStore) Load() (ble.BmsData, error) {
	var d ble.BmsData
	return d, b.load("", &d)
}
func (b bmsStore) Save(d ble.BmsData) error { return b.save("", d) }

// bmsRuntime owns the BMS registry, BLE poller, live readings and the BLE
// inspector sessions. All of them share the process-wide adapter lock.
type bmsRuntime struct {
	once     sync.Once
	reg      *ble.BmsRegistry
	poller   *ble.Poller
	sessions *ble.Sessions
	mu       sync.Mutex
	live     map[string]ble.Reading
	wake     chan struct{}
}

func (s *Server) bmsInit() *bmsRuntime {
	rt := &s.fleet.bms
	rt.once.Do(func() {
		rt.reg = ble.NewBmsRegistry(bmsStore{settingsDoc{s, "bms_devices"}})
		rt.poller = &ble.Poller{Adapter: s.BLEAdapter}
		rt.sessions = ble.NewSessions(s.BLEAdapter, filepath.Join(s.Store.App.DataDir(), "ble_captures"))
		rt.live, rt.wake = map[string]ble.Reading{}, make(chan struct{}, 1)
	})
	return rt
}
func (s *Server) bmsRegistry() *ble.BmsRegistry { return s.bmsInit().reg }
func (s *Server) bmsLive() map[string]ble.Reading {
	rt := s.bmsInit()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := map[string]ble.Reading{}
	for k, v := range rt.live {
		out[k] = v
	}
	return out
}

// bmsPoll ports one bms_loop pass: errors keep the last good reading, and a
// pack without capacity adopts the inferred value.
func (s *Server) bmsPoll(ctx context.Context) {
	rt := s.bmsInit()
	macs := []string{}
	for _, p := range rt.reg.ListPacks() {
		if p.MAC != "" {
			macs = append(macs, p.MAC)
		}
	}
	if len(macs) == 0 {
		return
	}
	readings := rt.poller.PollAll(ctx, macs, 20*time.Second)
	rt.mu.Lock()
	for mac, reading := range readings {
		if reading.Error != nil {
			prev := rt.live[mac]
			prev.Error = reading.Error
			if prev.SocPct == nil {
				prev.TS = reading.TS
			}
			rt.live[mac] = prev
			continue
		}
		rt.live[mac] = reading
		if pack, ok := rt.reg.Get(mac); ok && pack.CapacityWh == 0 {
			r := reading
			if wh, ok := ble.InferCapacityWh(&r); ok {
				rt.reg.SetCapacityWh(mac, wh)
			}
		}
	}
	rt.mu.Unlock()
}

// BMSLoop polls assigned JBD packs every BMS_POLL_INTERVAL_S (15 s default).
func (s *Server) BMSLoop(ctx context.Context) {
	rt := s.bmsInit()
	for {
		s.bmsPoll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-rt.wake:
		case <-time.After(15 * time.Second):
		}
	}
}
func (s *Server) bmsPackUI(p ble.Pack) map[string]any {
	out := toSolarMap(p)
	live, ok := s.bmsLive()[p.MAC]
	reading := map[string]any{"error": nil}
	out["error"], out["last_seen_ts"] = nil, nil
	if ok {
		lm := toSolarMap(live)
		for _, k := range []string{"soc_pct", "voltage_v", "current_a", "power_w", "temp_c", "min_cell_mv", "max_cell_mv", "cycles", "error", "ts"} {
			if v, present := lm[k]; present {
				reading[k] = v
			}
		}
		out["error"], out["last_seen_ts"] = lm["error"], lm["ts"]
	}
	out["reading"] = reading
	return out
}
func (s *Server) bmsRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	p, m := r.URL.Path, r.Method
	if !strings.HasPrefix(p, "/api/bms/") {
		return false
	}
	rt := s.bmsInit()
	status := rt.poller.Status()
	packs := func(list []ble.Pack) []map[string]any {
		out := []map[string]any{}
		for _, p := range list {
			out = append(out, s.bmsPackUI(p))
		}
		return out
	}
	inverters := func() any {
		raw := map[string]any{}
		_ = settingsDoc{s, "bms_devices"}.load("inverters", &raw)
		return raw
	}
	switch {
	case p == "/api/bms/scan" && (m == "GET" || m == "POST"):
		seconds := 8.0
		if v, ok := numberArg(b["seconds"]); ok {
			seconds = v
		} else if v, ok := numberArg(r.URL.Query().Get("seconds")); ok {
			seconds = v
		}
		seconds = max(1, min(seconds, 15))
		if !status.Available {
			jsonOut(w, 200, map[string]any{"devices": []any{}, "error": status.Error, "ble_available": false})
			return true
		}
		rows, err := rt.poller.Scan(r.Context(), time.Duration(seconds*float64(time.Second)))
		if err != nil {
			jsonOut(w, 200, map[string]any{"devices": []any{}, "error": err.Error(), "ble_available": true})
			return true
		}
		likely := 0
		for _, row := range rows {
			if row.LikelyJBD {
				likely++
			}
		}
		if rows == nil {
			rows = []ble.ScanRow{}
		}
		jsonOut(w, 200, map[string]any{"devices": rows, "ble_available": true, "likely_jbd": likely, "error": nil})
	case p == "/api/bms/status" && m == "GET":
		jsonOut(w, 200, map[string]any{"ble_available": status.Available, "error": status.Error, "packs": packs(rt.reg.ListPacks()), "inverters": inverters()})
	case p == "/api/bms/saved" && m == "GET":
		list := rt.reg.ListPacks()
		if sn := r.URL.Query().Get("siseli_sn"); sn != "" {
			list = rt.reg.ListPacksFor(sn)
		}
		jsonOut(w, 200, map[string]any{"packs": packs(list), "inverters": inverters(), "ble": status})
	case p == "/api/bms/saved" && m == "POST":
		opts := ble.BmsUpsertOpts{Alias: strings.TrimSpace(str(b, "alias"))}
		if raw, ok := b["capacity_wh"]; ok && raw != nil && raw != "" {
			v, ok := numberArg(raw)
			if !ok || v != float64(int(v)) {
				fail(w, 400, "capacity_wh must be an integer")
				return true
			}
			n := int(v)
			opts.CapacityWh = &n
		}
		if v, ok := b["siseli_device_sn"]; ok {
			sn, _ := v.(string)
			opts.SiseliDeviceSN = &sn
		}
		pack, err := rt.reg.Upsert(strings.TrimSpace(str(b, "mac")), opts)
		if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		kick(rt.wake)
		jsonOut(w, 200, map[string]any{"ok": true, "pack": s.bmsPackUI(pack)})
	case strings.HasPrefix(p, "/api/bms/saved/") && m == "DELETE":
		mac, _ := url.PathUnescape(strings.TrimPrefix(p, "/api/bms/saved/"))
		if !rt.reg.Delete(mac) {
			fail(w, 404, "unknown BMS MAC")
			return true
		}
		if n, err := ble.NormalizeMAC(mac); err == nil {
			rt.mu.Lock()
			delete(rt.live, n)
			rt.mu.Unlock()
		}
		jsonOut(w, 200, map[string]any{"ok": true})
	case strings.HasPrefix(p, "/api/bms/inverter/") && m == "POST":
		sn, _ := url.PathUnescape(strings.TrimPrefix(p, "/api/bms/inverter/"))
		enabled := true
		if v, ok := b["use_as_main"]; ok {
			enabled = v == true
		}
		row, err := rt.reg.SetUseAsMain(sn, enabled)
		if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		jsonOut(w, 200, map[string]any{"ok": true, "siseli_device_sn": sn, "use_as_main": row.UseAsMain})
	default:
		return false
	}
	return true
}
