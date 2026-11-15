package server

import (
	"context"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"testing"
)

type fleetSnapshotBridge struct{ packet map[string]any }

func (b fleetSnapshotBridge) Call(context.Context, string, map[string]any) (map[string]any, error) {
	return b.packet, nil
}
func TestFleetPersistenceFreshnessAndNormalizedHistory(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	fleet := map[string]any{}
	for sn, soc := range map[string]float64{"A": 40, "B": 50} {
		fleet[sn] = map[string]any{"device": map[string]any{"device_sn": sn, "device_id": sn, "model_code": 22.0}, "ts": 1700000000.0, "pack_ts": 1700000001.0, "telemetry": map[string]any{"battery_percent": soc, "input_power_w": 100.0, "output_power_w": 200.0, "solar_input_w": 10.0}, "battery_packs": []any{map[string]any{"rb": 80.0}}}
	}
	s.Bridge = fleetSnapshotBridge{map[string]any{"cloud": map[string]any{"devices_telemetry": fleet}}}
	s.pollOnce(context.Background())
	s.pollOnce(context.Background())
	for sn, soc := range map[string]float64{"A": 40, "B": 50} {
		rows, err := s.Store.List("samples", sn, 0, 0)
		if err != nil || len(rows) != 1 {
			t.Fatal(sn, rows, err)
		}
		if value := forecast.RowSOC(rows[0]); value == nil || *value != soc {
			t.Fatal("forecast normalized SOC missing", rows)
		}
		packs, err := s.Store.List("battery_packs", sn, 0, 0)
		if err != nil || len(packs) != 1 || packs[0]["ts"] != 1700000001.0 {
			t.Fatal(packs, err)
		}
	}
	v := contractJSON(t, h, "GET", "/api/devices/battery_packs?device_sn=B", "", token, 200)
	if len(v["packs"].([]any)) != 1 || v["fetched_at"] != 1700000001.0 || v["main_soc_pct"] != 50.0 {
		t.Fatal(v)
	}
}
func TestNativeDiagnosticsForwarding(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	backend := &contractBridge{}
	s.Bridge = backend
	contractJSON(t, h, "GET", "/api/debug/raw_props?device_sn=B", "", token, 200)
	if backend.method != "get_raw_props" || backend.params["device_sn"] != "B" {
		t.Fatal(backend)
	}
	contractJSON(t, h, "GET", "/api/devices/probe_results?device_sn=B", "", token, 200)
	if backend.method != "probe_results" || backend.params["device_sn"] != "B" {
		t.Fatal(backend)
	}
	contractJSON(t, h, "POST", "/api/devices/probe_now?device_sn=B", `{}`, token, 200)
	if backend.method != "cloud_probe" || backend.params["device_sn"] != "B" {
		t.Fatal(backend)
	}
}
