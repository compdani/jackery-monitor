package server

import (
	"context"
	"encoding/json"
	"github.com/compdani/jackery-monitor/internal/automation"
	"testing"
	"time"
)

func TestWatchdogLivePersistenceAndUserSuppression(t *testing.T) {
	s, _ := testServer(t)
	if err := s.Store.Put("settings", "settings", "", map[string]any{"inverter_trip_recovery_min_w": 100}); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	state := automation.WatchdogState{LastHighOutputTS: now - 10, FirstLowTS: now - 8, LowSamples: 2, LastSampleTS: 1}
	if err := s.saveWatchdog("SN", state); err != nil {
		t.Fatal(err)
	}
	// No bridge: exercise live detection without hardware or a recovery goroutine.
	s.Bridge = nil
	s.watchdogTick(context.Background(), "SN", map[string]any{"ac_on": true, "output_power_w": 10.0}, 13.0, 2)
	got, err := s.watchdogState("SN")
	if err != nil {
		t.Fatal(err)
	}
	if !got.HWTripActive || got.EpisodeCycles != 1 {
		t.Fatalf("%+v", got)
	}
	done := s.watchdogUserOff("SN")
	s.watchdogTick(context.Background(), "SN", map[string]any{"ac_on": true, "output_power_w": 10.0}, 13.0, 3)
	done()
	got, err = s.watchdogState("SN")
	if err != nil {
		t.Fatal(err)
	}
	if got.EpisodeCycles != 1 || got.LowSamples != 0 || got.LastUserOffTS == 0 {
		t.Fatalf("%+v", got)
	}
	// A fresh server sees the same episode and user suppression timestamp.
	restarted := &Server{Store: s.Store}
	restored, err := restarted.watchdogState("SN")
	if err != nil || restored.LastUserOffTS != got.LastUserOffTS {
		t.Fatal(restored, err)
	}
}

type watchdogBridge struct{ calls chan bool }

func (b watchdogBridge) Call(ctx context.Context, method string, p map[string]any) (map[string]any, error) {
	select {
	case b.calls <- p["on"].(bool):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return map[string]any{"ok": true}, nil
}
func TestWatchdogCycleCancelledByUserOff(t *testing.T) {
	s, _ := testServer(t)
	calls := make(chan bool, 4)
	s.Bridge = watchdogBridge{calls}
	if err := s.Store.Put("settings", "settings", "", map[string]any{"inverter_trip_recovery_min_w": 100}); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	if err := s.saveWatchdog("SN", automation.WatchdogState{LastHighOutputTS: now - 10, FirstLowTS: now - 8, LowSamples: 2, LastSampleTS: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.watchdogTick(ctx, "SN", map[string]any{"ac_on": true, "output_power_w": 10.0}, 13.0, 2)
	select {
	case on := <-calls:
		if on {
			t.Fatal("expected OFF first")
		}
	case <-time.After(time.Second):
		t.Fatal("no OFF")
	}
	done := s.watchdogUserOff("SN")
	done()
	select {
	case on := <-calls:
		t.Fatalf("unexpected recovery command %v", on)
	case <-time.After(2100 * time.Millisecond):
	}
}

func TestWatchdogAPIContract(t *testing.T) {
	s, h := testServer(t)
	if err := s.Store.Put("devices", "SN", "SN", map[string]any{"device": map[string]any{"device_sn": "SN", "model_code": 13}}); err != nil {
		t.Fatal(err)
	}
	if w := request(h, "GET", "/api/inverter_watchdog/config?device_sn=SN", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request(h, "POST", "/api/auth/setup", `{"username":"admin","password":"secure-pass"}`, "")
	var auth map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &auth); err != nil {
		t.Fatal(err)
	}
	token := auth["token"].(string)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "config?device_sn=SN", ""},
		{"PUT", "config", `{"device_sn":"SN","enabled":false}`},
		{"GET", "status?device_sn=SN", ""},
		{"POST", "dismiss?device_sn=SN", ""},
	} {
		w = request(h, tc.method, "/api/inverter_watchdog/"+tc.path, tc.body, token)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out["device_sn"] != "SN" {
			t.Fatal(out)
		}
		if tc.method == "PUT" && (out["enabled"] != false || out["source"] != "user") {
			t.Fatal(out)
		}
	}
}

func TestWatchdogImportedOverridePrecedence(t *testing.T) {
	s, _ := testServer(t)
	for _, value := range []float64{0, 1} {
		if err := s.Store.Put("forecast_models", "legacy-param", "SN", map[string]any{"_legacy_table": "device_params", "key": "inverter_watchdog_enabled", "value": value}); err != nil {
			t.Fatal(err)
		}
		enabled, source, err := s.watchdogEnabled("SN", 13.0)
		if err != nil || enabled != (value == 1) || source != "user" {
			t.Fatal(enabled, source, err)
		}
	}
	if err := s.Store.Put("settings", "inverter_watchdog_config:SN", "SN", map[string]any{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	enabled, _, err := s.watchdogEnabled("SN", 13.0)
	if enabled || err != nil {
		t.Fatal(enabled, err)
	}
}

func TestWatchdogPendingIntentsSurviveDismiss(t *testing.T) {
	s, _ := testServer(t)
	first := s.watchdogUserOff("SN")
	second := s.watchdogUserOff("SN")
	state, err := s.watchdogState("SN")
	if err != nil {
		t.Fatal(err)
	}
	state.Dismiss()
	if err := s.saveWatchdog("SN", state); err != nil {
		t.Fatal(err)
	}
	first()
	if s.watchdog.pending["SN"] != 1 {
		t.Fatal(s.watchdog.pending)
	}
	second()
	if s.watchdog.pending["SN"] != 0 {
		t.Fatal(s.watchdog.pending)
	}
}

type queuedWatchdogBridge struct {
	queued    chan struct{}
	cancelled chan struct{}
}

func (b queuedWatchdogBridge) Call(ctx context.Context, method string, p map[string]any) (map[string]any, error) {
	if p["on"] == true {
		close(b.queued)
		<-ctx.Done()
		close(b.cancelled)
		return nil, ctx.Err()
	}
	return map[string]any{"ok": true}, nil
}
func TestWatchdogUserOffCancelsQueuedRecoveryON(t *testing.T) {
	s, _ := testServer(t)
	b := queuedWatchdogBridge{make(chan struct{}), make(chan struct{})}
	s.Bridge = b
	if err := s.Store.Put("settings", "settings", "", map[string]any{"inverter_trip_recovery_min_w": 100}); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	if err := s.saveWatchdog("SN", automation.WatchdogState{LastHighOutputTS: now - 10, FirstLowTS: now - 8, LowSamples: 2, LastSampleTS: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.watchdogTick(ctx, "SN", map[string]any{"ac_on": true, "output_power_w": 10.0}, 13.0, 2)
	select {
	case <-b.queued:
	case <-time.After(3 * time.Second):
		t.Fatal("no queued ON")
	}
	done := make(chan struct{})
	go func() { finish := s.watchdogUserOff("SN"); finish(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("user intent blocked by queued ON")
	}
	select {
	case <-b.cancelled:
	case <-time.After(time.Second):
		t.Fatal("ON not cancelled")
	}
}
