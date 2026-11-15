package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/compdani/jackery-monitor/internal/kasa"
)

type fakePlug struct {
	mu   *sync.Mutex
	on   *bool
	down bool
}

func (p fakePlug) Update(context.Context) error {
	if p.down {
		return errors.New("host unreachable")
	}
	return nil
}
func (p fakePlug) TurnOn(context.Context) error  { p.mu.Lock(); *p.on = true; p.mu.Unlock(); return nil }
func (p fakePlug) TurnOff(context.Context) error { p.mu.Lock(); *p.on = false; p.mu.Unlock(); return nil }
func (p fakePlug) Alias() string                 { return "Car" }
func (p fakePlug) Model() string                 { return "KP125M" }
func (p fakePlug) DeviceType() string            { return "Plug" }
func (p fakePlug) IsOn() bool                    { p.mu.Lock(); defer p.mu.Unlock(); return *p.on }
func (p fakePlug) PowerW() *float64              { return nil }

type fakeKasaBackend struct {
	mu    sync.Mutex
	on    bool
	down  map[string]bool
	calls int
}

func (b *fakeKasaBackend) plug(host string) fakePlug {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	return fakePlug{mu: &b.mu, on: &b.on, down: b.down[host]}
}
func (b *fakeKasaBackend) Discover(context.Context, time.Duration, *kasa.Credentials) (map[string]kasa.Device, error) {
	return map[string]kasa.Device{"10.0.0.5": b.plug("10.0.0.5")}, nil
}
func (b *fakeKasaBackend) DiscoverSingle(_ context.Context, host string, _ *kasa.Credentials) (kasa.Device, error) {
	p := b.plug(host)
	if p.down {
		return nil, errors.New("host unreachable")
	}
	return p, nil
}

func TestKasaFleetRoutesAndReconciler(t *testing.T) {
	s, h := testServer(t)
	fake := &fakeKasaBackend{down: map[string]bool{"10.0.0.9": true}}
	s.KasaBackend = fake
	token := contractToken(t, h)
	v := contractJSON(t, h, "GET", "/api/kasa/credentials", "", token, 200)
	if v["has_credentials"] != false || v["email"] != nil {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/kasa/credentials", `{"email":"a@b.c"}`, token, 400)
	if v = contractJSON(t, h, "POST", "/api/kasa/credentials", `{"email":" a@b.c ","password":"pw"}`, token, 200); v["email"] != "a@b.c" {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "GET", "/api/kasa/credentials", "", token, 200); v["has_credentials"] != true || v["email"] != "a@b.c" {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "GET", "/api/kasa/devices", "", token, 200); len(v["devices"].([]any)) != 1 {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/kasa/test", `{"on":true}`, token, 400)
	if v = contractJSON(t, h, "POST", "/api/kasa/test", `{"host":"10.0.0.5","on":true}`, token, 200); v["ok"] != true || v["is_on"] != true {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "GET", "/api/kasa/status?host=10.0.0.5", "", token, 200); v["is_on"] != true || v["alias"] != "Car" {
		t.Fatal(v)
	}
	contractJSON(t, h, "GET", "/api/kasa/status?host=10.0.0.9", "", token, 400)
	contractJSON(t, h, "POST", "/api/kasa/saved", `{"host":"10.0.0.9"}`, token, 400)
	v = contractJSON(t, h, "POST", "/api/kasa/saved", `{"host":"10.0.0.5","alias":"","jackery_device_sn":"SN1"}`, token, 200)
	dev := v["device"].(map[string]any)
	if dev["alias"] != "Car" || dev["jackery_device_sn"] != "SN1" || dev["is_on"] != true || dev["last_tested"] == nil {
		t.Fatal(v)
	}
	// Restart: registry is persisted in PocketBase.
	restarted := &Server{Store: s.Store, Key: s.Key, KasaBackend: fake}
	if got := restarted.kasaRegistry().ListAll(); len(got) != 1 || got[0].Host != "10.0.0.5" {
		t.Fatal(got)
	}
	if err := s.Store.Put("automation_rules", "r1", "", map[string]any{"id": "r1", "kasa_host": "10.0.0.5"}); err != nil {
		t.Fatal(err)
	}
	v = contractJSON(t, h, "GET", "/api/kasa/saved?jackery_sn=OTHER", "", token, 200)
	if len(v["devices"].([]any)) != 0 {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/kasa/saved?refresh=true", "", token, 200)
	row := v["devices"].([]any)[0].(map[string]any)
	if row["online"] != true || row["status"] != "online" || row["is_on"] != true {
		t.Fatal(row)
	}
	// An offline plug is backed off after failure and counted by health.
	if _, err := s.kasaRegistry().Upsert("10.0.0.9", kasa.UpsertOpts{Alias: "Dead"}); err != nil {
		t.Fatal(err)
	}
	s.kasaReconcile(context.Background(), time.Now())
	if v = contractJSON(t, h, "GET", "/api/kasa/health", "", token, 200); v["offline_count"] != 1.0 || v["device_count"] != 2.0 {
		t.Fatal(v)
	}
	before := fake.calls
	s.kasaReconcile(context.Background(), time.Now())
	if fake.calls != before+1 { // only the healthy plug is probed again
		t.Fatal(before, fake.calls)
	}
	v = contractJSON(t, h, "DELETE", "/api/kasa/saved/10.0.0.5", "", token, 200)
	if v["deleted"] != true || len(v["rules_referencing"].([]any)) != 1 {
		t.Fatal(v)
	}
	contractJSON(t, h, "DELETE", "/api/kasa/credentials", "", token, 200)
}
