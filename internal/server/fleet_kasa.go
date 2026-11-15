package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/kasa"
)

// settingsDoc persists a legacy JSON registry document in one settings record,
// preserving unrelated top-level fields from imported files.
type settingsDoc struct {
	s   *Server
	key string
}

func (d settingsDoc) load(field string, out any) error {
	raw, err := d.s.Store.Get("settings", d.key)
	if isMissing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var v any = raw
	if field != "" {
		v = raw[field]
	}
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
func (d settingsDoc) save(field string, v any) error {
	raw, err := d.s.Store.Get("settings", d.key)
	if err != nil && !isMissing(err) {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var value any
	if err = json.Unmarshal(data, &value); err != nil {
		return err
	}
	if field == "" {
		m, _ := value.(map[string]any)
		return d.s.Store.Put("settings", d.key, "", m)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	raw[field] = value
	return d.s.Store.Put("settings", d.key, "", raw)
}

type kasaStore struct{ settingsDoc }

func (k kasaStore) Load() ([]kasa.SavedDevice, error) {
	var out []kasa.SavedDevice
	return out, k.load("devices", &out)
}
func (k kasaStore) Save(d []kasa.SavedDevice) error { return k.save("devices", d) }

type fleetRuntime struct {
	kasaOnce sync.Once
	kasa     *kasa.Registry
	siseli   siseliRuntime
	ecoflow  ecoflowRuntime
	bms      bmsRuntime
}

func (s *Server) kasaRegistry() *kasa.Registry {
	s.fleet.kasaOnce.Do(func() { s.fleet.kasa = kasa.NewRegistry(kasaStore{settingsDoc{s, "kasa_devices"}}) })
	return s.fleet.kasa
}
func (s *Server) kasaClient() *kasa.Client {
	s.initControllers()
	return s.controllers.client
}
func kasaUI(r *kasa.Registry, d kasa.SavedDevice) map[string]any {
	out := map[string]any{}
	data, _ := json.Marshal(d)
	_ = json.Unmarshal(data, &out)
	out["is_on"] = d.LastKnownIsOn
	out["online"] = r.IsOnline(d)
	out["status"] = r.StatusOf(d)
	out["error"] = d.LastError
	out["last_seen_ts"] = d.LastSeenTS
	return out
}
func infoMap(info kasa.Info) map[string]any {
	out := map[string]any{"ok": true}
	data, _ := json.Marshal(info)
	_ = json.Unmarshal(data, &out)
	return out
}
func (s *Server) probeKasa(ctx context.Context, host string) {
	call, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	info, err := s.kasaClient().Status(call, host)
	if err != nil {
		s.kasaRegistry().UpdateProbe(host, kasa.ProbeResult{Error: err.Error()})
		return
	}
	on := info.IsOn
	model := ""
	if info.Model != nil {
		model = *info.Model
	}
	s.kasaRegistry().UpdateProbe(host, kasa.ProbeResult{Success: true, IsOn: &on, Model: model, Alias: info.Alias})
}

// kasaReconcile ports kasa_reconciler_loop's per-device backoff:
// min(5 min * 2^min(fails,5), 30 min) after a failure.
func (s *Server) kasaReconcile(ctx context.Context, now time.Time) {
	for _, d := range s.kasaRegistry().ListAll() {
		if d.Host == "" || ctx.Err() != nil {
			continue
		}
		if d.ConsecutiveFails > 0 && d.LastFailedTS != nil {
			wait := math.Min(300*math.Pow(2, float64(min(d.ConsecutiveFails, 5))), 1800)
			if float64(now.UnixNano())/1e9-*d.LastFailedTS < wait {
				continue
			}
		}
		s.probeKasa(ctx, d.Host)
	}
}

// KasaReconcileLoop probes saved plugs every five minutes.
func (s *Server) KasaReconcileLoop(ctx context.Context) {
	for {
		s.kasaReconcile(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

func (s *Server) kasaRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	p, m := r.URL.Path, r.Method
	ctx := r.Context()
	switch {
	case p == "/api/kasa/credentials" && m == "GET":
		c, err := s.Store.Secret(s.Key, "kasa")
		if err != nil && !isMissing(err) {
			storageError(w, err)
			return true
		}
		var email any
		if c != nil {
			email = str(c, "email")
		}
		jsonOut(w, 200, map[string]any{"has_credentials": c != nil, "email": email, "has_password": str(c, "password") != ""})
	case p == "/api/kasa/credentials" && m == "POST":
		email, pass := strings.TrimSpace(str(b, "email")), str(b, "password")
		if email == "" || pass == "" {
			fail(w, 400, "email and password are required")
			return true
		}
		if err := s.Store.SaveSecret(s.Key, "kasa", map[string]any{"email": email, "password": pass}); err != nil {
			fail(w, 500, "failed to save credentials")
			return true
		}
		jsonOut(w, 200, map[string]any{"ok": true, "email": email})
	case p == "/api/kasa/credentials" && m == "DELETE":
		if err := s.Store.Delete("credentials", "kasa"); err != nil && !isMissing(err) {
			storageError(w, err)
			return true
		}
		jsonOut(w, 200, map[string]any{"ok": true})
	case p == "/api/kasa/devices" && m == "GET":
		found, err := s.kasaClient().Discover(ctx, 3*time.Second)
		if err != nil {
			fail(w, 500, err.Error())
			return true
		}
		jsonOut(w, 200, map[string]any{"devices": found})
	case p == "/api/kasa/test" && m == "POST":
		host := strings.TrimSpace(str(b, "host"))
		if host == "" {
			fail(w, 400, "host required")
			return true
		}
		call, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		info, err := s.kasaClient().SetState(call, host, b["on"] == true)
		if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		jsonOut(w, 200, infoMap(info))
	case p == "/api/kasa/status" && m == "GET":
		host := r.URL.Query().Get("host")
		if host == "" {
			fail(w, 422, "host required")
			return true
		}
		info, err := s.kasaClient().Status(ctx, host)
		if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		jsonOut(w, 200, infoMap(info))
	case p == "/api/kasa/health" && m == "GET":
		reg := s.kasaRegistry()
		jsonOut(w, 200, map[string]any{"offline_count": reg.OfflineCount(), "device_count": len(reg.ListAll())})
	case p == "/api/kasa/saved" && m == "GET":
		reg := s.kasaRegistry()
		devices := reg.ListAll()
		if sn := r.URL.Query().Get("jackery_sn"); sn != "" {
			devices = reg.ListForJackery(sn)
		}
		if v := r.URL.Query().Get("refresh"); v == "true" || v == "1" {
			var wg sync.WaitGroup
			for _, d := range devices {
				wg.Add(1)
				go func(host string) { defer wg.Done(); s.probeKasa(ctx, host) }(d.Host)
			}
			wg.Wait()
		}
		out := []map[string]any{}
		for _, d := range devices {
			if fresh, ok := reg.Get(d.Host); ok {
				d = fresh
			}
			out = append(out, kasaUI(reg, d))
		}
		jsonOut(w, 200, map[string]any{"devices": out})
	case p == "/api/kasa/saved" && m == "POST":
		host := strings.TrimSpace(str(b, "host"))
		if host == "" {
			fail(w, 400, "host required")
			return true
		}
		info, err := s.kasaClient().Status(ctx, host)
		if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		alias := strings.TrimSpace(str(b, "alias"))
		if alias == "" {
			alias = info.Alias
		}
		typ := info.Type
		opts := kasa.UpsertOpts{Alias: alias, Model: info.Model, Type: &typ, MarkTested: true}
		if v, ok := b["jackery_device_sn"]; ok {
			sn, _ := v.(string)
			opts.JackerySN = &sn
		}
		saved, err := s.kasaRegistry().Upsert(host, opts)
		if err != nil {
			fail(w, 400, err.Error())
			return true
		}
		dev := map[string]any{}
		data, _ := json.Marshal(saved)
		_ = json.Unmarshal(data, &dev)
		dev["is_on"] = info.IsOn
		jsonOut(w, 200, map[string]any{"ok": true, "device": dev})
	case strings.HasPrefix(p, "/api/kasa/saved/") && m == "DELETE":
		host, _ := url.PathUnescape(strings.TrimPrefix(p, "/api/kasa/saved/"))
		deleted := s.kasaRegistry().Delete(host)
		rules, _ := s.Store.List("automation_rules", "", 0, 0)
		refs := []any{}
		for _, rule := range rules {
			if str(rule, "kasa_host") == host {
				refs = append(refs, rule["id"])
			}
		}
		jsonOut(w, 200, map[string]any{"ok": true, "deleted": deleted, "rules_referencing": refs})
	default:
		return false
	}
	return true
}
