package ecoflow

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func str(s string) *string { return &s }
func ip(i int) *int        { return &i }

func TestSNHelpers(t *testing.T) {
	if !IsEcoflowSN("ecoflow:R351") || IsEcoflowSN("siseli:42") {
		t.Fatal("IsEcoflowSN")
	}
	if got, _ := MakeSN("R351ABC"); got != "ecoflow:R351ABC" {
		t.Fatal(got)
	}
	if got, _ := MakeSN("ecoflow:R351ABC"); got != "ecoflow:R351ABC" {
		t.Fatal(got)
	}
	if RawSNFromSN("ecoflow:R351ABC") != "R351ABC" {
		t.Fatal("RawSNFromSN")
	}
	if _, err := MakeSN(""); err == nil {
		t.Fatal("expected error for empty")
	}
}

func newReg(t *testing.T) *Registry {
	return NewRegistry(FileStore{Path: filepath.Join(t.TempDir(), "ecoflow.json")})
}

func TestUpsertLinkDelete(t *testing.T) {
	r := newReg(t)
	row, err := r.Upsert("R351TEST", UpsertOpts{DeviceType: "DELTA_3_MAX_PLUS", Alias: "Garage", CapacityWh: ip(2048)})
	if err != nil {
		t.Fatal(err)
	}
	if row.SN != "ecoflow:R351TEST" || !reflect.DeepEqual(row.Roles, []string{"solar", "battery", "output"}) {
		t.Fatalf("row = %+v", row)
	}
	linked, err := r.Link("ecoflow:R351TEST", str("siseli:42"), []string{"solar", "output"})
	if err != nil {
		t.Fatal(err)
	}
	if *linked.SiseliDeviceSN != "siseli:42" || !reflect.DeepEqual(linked.Roles, []string{"solar", "output"}) {
		t.Fatalf("linked = %+v", linked)
	}
	if len(r.ListForSiseli("siseli:42")) != 1 || len(r.ListForSiseli("siseli:99")) != 0 {
		t.Fatal("ListForSiseli")
	}
	if !r.Delete("ecoflow:R351TEST") {
		t.Fatal("delete")
	}
	if _, ok := r.Get("ecoflow:R351TEST"); ok {
		t.Fatal("still present")
	}
	if _, err := r.Upsert("X", UpsertOpts{DeviceType: "BOGUS"}); err == nil {
		t.Fatal("unsupported type must error")
	}
	if _, err := r.Link("ecoflow:NOPE", nil, nil); err != ErrNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestRegistryPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.json")
	r := NewRegistry(FileStore{Path: path})
	r.Upsert("R1", UpsertOpts{Alias: "A"})
	r2 := NewRegistry(FileStore{Path: path})
	d, ok := r2.Get("R1")
	if !ok || d.Alias != "A" || d.CapacityWh != 2048 {
		t.Fatalf("%+v ok=%v", d, ok)
	}
}

func TestFleetMetaOmitsLinkedKeepsUnattached(t *testing.T) {
	r := newReg(t)
	r.Upsert("FREE1", UpsertOpts{Alias: "Solo", DeviceType: "DELTA_3_MAX_PLUS"})
	r.Upsert("LINK1", UpsertOpts{Alias: "Attached", DeviceType: "DELTA_3_MAX_PLUS", SiseliDeviceSN: str("siseli:42")})
	meta := r.FleetMeta()
	if len(meta) != 1 || meta[0].DeviceID != "ecoflow:FREE1" || meta[0].Source != "ecoflow" {
		t.Fatalf("meta = %+v", meta)
	}
}

func liveFor(tele map[string]any, ts float64, detail map[string]any) map[string]Live {
	return map[string]Live{"ecoflow:R351": {Telemetry: tele, Detail: detail, TS: &ts}}
}

func TestOverlaySumsSolarAndOutput(t *testing.T) {
	now := float64(time.Now().Unix())
	ef := ParamsToTelemetry(Params{"cms_batt_soc": 80.0, "pow_get_pv": 400.0, "pow_get_pv2": 300.0, "pow_out_sum_w": 150.0},
		TelemetryOpts{Alias: "Garage", CapacityWh: 2048})
	live := liveFor(ef.Map(), now, ef.EcoflowDetail.DetailMap())
	linked := []LinkedRow{{SN: "ecoflow:R351", Alias: "Garage", Roles: []string{"solar", "battery", "output"}, CapacityWh: 2048}}
	base := map[string]any{
		"battery_percent": 55, "solar_input_w": 1000,
		"solar_inputs": []map[string]any{
			{"id": "siseli:pv1", "label": "Siseli PV1", "source": "siseli", "watts": 400},
			{"id": "siseli:pv2", "label": "Siseli PV2", "source": "siseli", "watts": 600},
		},
		"output_power_w": 200, "ac_input_w": 0, "source": "siseli",
	}
	out := OverlayTelemetry(base, OverlayOpts{Linked: linked, LiveBySN: live, Now: now})
	if base["solar_input_w"] != 1000 {
		t.Fatal("original mutated")
	}
	eq := func(name string, got any, want float64) {
		t.Helper()
		if asF(got) != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	eq("solar_input_w", out["solar_input_w"], 1700)
	eq("siseli_solar_w", out["siseli_solar_w"], 1000)
	eq("additional_solar_w", out["additional_solar_w"], 700)
	eq("output_power_w", out["output_power_w"], 350)
	eq("additional_output_w", out["additional_output_w"], 150)
	if out["ecoflow_linked"] != true {
		t.Error("ecoflow_linked")
	}
	le := out["linked_ecoflow"].([]map[string]any)
	if len(le) != 1 || le[0]["fresh"] != true {
		t.Errorf("linked_ecoflow = %v", le)
	}
	wantLoads := []map[string]any{
		{"id": "siseli", "label": "Siseli load", "source": "siseli", "watts": 200},
		{"id": "ecoflow:R351", "label": "Garage load", "source": "ecoflow", "device_sn": "ecoflow:R351", "watts": 150},
	}
	if !reflect.DeepEqual(out["load_inputs"], wantLoads) {
		t.Errorf("load_inputs = %v", out["load_inputs"])
	}
	// Battery role: EcoFlow only (no BMS yet) → EcoFlow SOC + capacity
	eq("battery_percent", out["battery_percent"], 80.0)
	if out["ecoflow_source"] != true {
		t.Error("ecoflow_source")
	}
	eq("capacity_wh", out["capacity_wh"], 2048)
	sources := map[string]bool{}
	for _, in := range out["solar_inputs"].([]any) {
		sources[in.(map[string]any)["source"].(string)] = true
	}
	if !sources["siseli"] || !sources["ecoflow"] || len(sources) != 2 {
		t.Errorf("sources = %v", sources)
	}
}

func TestOverlayLoadSourcesFilter(t *testing.T) {
	now := float64(time.Now().Unix())
	ef := ParamsToTelemetry(Params{"cms_batt_soc": 80.0, "pow_get_pv": 0.0, "pow_out_sum_w": 150.0},
		TelemetryOpts{Alias: "Garage", CapacityWh: 2048})
	live := liveFor(ef.Map(), now, map[string]any{})
	linked := []LinkedRow{{SN: "ecoflow:R351", Alias: "Garage", Roles: []string{"output"}, CapacityWh: 2048}}
	base := map[string]any{"battery_percent": 55, "solar_input_w": 0, "output_power_w": 200, "source": "siseli"}
	only := OverlayTelemetry(base, OverlayOpts{Linked: linked, LiveBySN: live, Now: now, LoadSources: []string{"ecoflow:R351"}})
	if asF(only["output_power_w"]) != 150 || asF(only["siseli_output_w"]) != 200 {
		t.Fatalf("only eco = %v", only)
	}
	onlySiseli := OverlayTelemetry(base, OverlayOpts{Linked: linked, LiveBySN: live, Now: now, LoadSources: []string{"siseli"}})
	if asF(onlySiseli["output_power_w"]) != 200 {
		t.Fatalf("only siseli = %v", onlySiseli["output_power_w"])
	}
	none := OverlayTelemetry(base, OverlayOpts{Linked: linked, LiveBySN: live, Now: now, LoadSources: []string{}})
	if asF(none["output_power_w"]) != 0 {
		t.Fatalf("none = %v", none["output_power_w"])
	}
	// load_inputs always lists every contributor for the gear UI
	if len(none["load_inputs"].([]map[string]any)) != 2 {
		t.Fatal("load_inputs")
	}
}

func TestOverlayWeightedSOCWithBMS(t *testing.T) {
	now := float64(time.Now().Unix())
	ef := ParamsToTelemetry(Params{"cms_batt_soc": 100.0, "pow_get_pv": 0.0, "pow_out_sum_w": 0.0}, TelemetryOpts{CapacityWh: 2048})
	live := liveFor(ef.Map(), now, map[string]any{})
	linked := []LinkedRow{{SN: "ecoflow:R351", Alias: "Garage", Roles: []string{"battery"}, CapacityWh: 2048}}
	// Pretend BMS already set a 5000 Wh pack at 50%.
	base := map[string]any{"battery_percent": 50.0, "capacity_wh": 5000, "bms_source": true,
		"solar_input_w": 0, "output_power_w": 0, "source": "siseli"}
	out := OverlayTelemetry(base, OverlayOpts{Linked: linked, LiveBySN: live, Now: now, BMSAlready: true})
	// (50*5000 + 100*2048) / (5000+2048) ≈ 64.5
	if !approx(asF(out["battery_percent"]), 64.5, 0.2) || out["ecoflow_source"] != true || asF(out["capacity_wh"]) != 5000+2048 {
		t.Fatalf("out = %v", out)
	}
}

func TestOverlaySkipsStale(t *testing.T) {
	now := float64(time.Now().Unix())
	ef := ParamsToTelemetry(Params{"cms_batt_soc": 80.0, "pow_get_pv": 500.0, "pow_out_sum_w": 100.0}, TelemetryOpts{})
	live := liveFor(ef.Map(), now-500, map[string]any{})
	linked := []LinkedRow{{SN: "ecoflow:R351", Alias: "Garage", Roles: []string{"solar", "output", "battery"}, CapacityWh: 2048}}
	out := OverlayTelemetry(map[string]any{"battery_percent": 40, "solar_input_w": 10, "output_power_w": 5, "source": "siseli"},
		OverlayOpts{Linked: linked, LiveBySN: live, Now: now})
	if asF(out["solar_input_w"]) != 10 || asF(out["output_power_w"]) != 5 {
		t.Fatalf("out = %v", out)
	}
	// Stale: keep Siseli SOC, but still count EcoFlow registry capacity.
	if asF(out["battery_percent"]) != 40 || out["ecoflow_source"] == true ||
		asF(out["capacity_wh"]) != 2048 || asF(out["ecoflow_capacity_wh"]) != 2048 {
		t.Fatalf("out = %v", out)
	}
	if out["linked_ecoflow"].([]map[string]any)[0]["fresh"] != false {
		t.Fatal("expected fresh=false")
	}
}

func TestOverlayStaleEcoflowAddsToBMSCapacity(t *testing.T) {
	now := float64(time.Now().Unix())
	ts := now - 500
	live := map[string]Live{"ecoflow:R351": {Telemetry: map[string]any{}, Detail: map[string]any{}, TS: &ts}}
	linked := []LinkedRow{{SN: "ecoflow:R351", Alias: "Garage", Roles: []string{"battery"}, CapacityWh: 2048}}
	out := OverlayTelemetry(map[string]any{"battery_percent": 50.0, "capacity_wh": 5000, "bms_source": true,
		"solar_input_w": 0, "output_power_w": 0, "source": "siseli"},
		OverlayOpts{Linked: linked, LiveBySN: live, Now: now, BMSAlready: true})
	if asF(out["capacity_wh"]) != 5000+2048 || asF(out["battery_percent"]) != 50.0 {
		t.Fatalf("out = %v", out)
	}
}

func TestOverlayNilTelemetry(t *testing.T) {
	if OverlayTelemetry(nil, OverlayOpts{}) != nil {
		t.Fatal("nil in → nil out")
	}
}

func TestNormalizeRowDefaults(t *testing.T) {
	row, ok := NormalizeRow(map[string]any{"raw_sn": "R1", "device_type": "weird", "roles": []any{"Solar", "bogus", "solar"}}, 1)
	if !ok || row.SN != "ecoflow:R1" || row.DeviceType != DefaultType || row.CapacityWh != 2048 ||
		!reflect.DeepEqual(row.Roles, []string{"solar"}) || row.Alias != "Delta 3 Max Plus" {
		t.Fatalf("%+v ok=%v", row, ok)
	}
	if _, ok := NormalizeRow(map[string]any{}, 1); ok {
		t.Fatal("empty row is invalid")
	}
}
