package ble

import (
	"path/filepath"
	"testing"
)

func sp(s string) *string { return &s }
func intp(i int) *int     { return &i }

func f(v float64) *float64 { return &v }

func newBmsReg(t *testing.T) *BmsRegistry {
	return NewBmsRegistry(BmsFileStore{Path: filepath.Join(t.TempDir(), "bms.json")})
}

func TestNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{
		"aa-bb-cc-dd-ee-ff": "AA:BB:CC:DD:EE:FF",
		"AABBCCDDEEFF":      "AA:BB:CC:DD:EE:FF",
	} {
		got, err := NormalizeMAC(in)
		if err != nil || got != want {
			t.Errorf("NormalizeMAC(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := NormalizeMAC("aa:bb"); err == nil {
		t.Error("expected error")
	}
}

func TestUpsertAndAssign(t *testing.T) {
	r := newBmsReg(t)
	p, err := r.Upsert("aa:bb:cc:dd:ee:ff", BmsUpsertOpts{Alias: "Pack 1", CapacityWh: intp(5120), SiseliDeviceSN: sp("siseli:42")})
	if err != nil || p.MAC != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("%+v %v", p, err)
	}
	if !r.UseAsMain("siseli:42") {
		t.Fatal("use_as_main default true")
	}
	if len(r.ListPacksFor("siseli:42")) != 1 || len(r.ListPacksFor("siseli:99")) != 0 {
		t.Fatal("ListPacksFor")
	}
	r.SetUseAsMain("siseli:42", false)
	if r.UseAsMain("siseli:42") {
		t.Fatal("use_as_main should be false")
	}
	if !r.Delete("AA:BB:CC:DD:EE:FF") {
		t.Fatal("delete")
	}
	if _, ok := r.Get("AA:BB:CC:DD:EE:FF"); ok {
		t.Fatal("still present")
	}
}

func TestRegistryPersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bms.json")
	r := NewBmsRegistry(BmsFileStore{Path: path})
	r.Upsert("AA:BB:CC:DD:EE:01", BmsUpsertOpts{Alias: "A", SiseliDeviceSN: sp("siseli:1")})
	r.SetUseAsMain("siseli:1", false)
	r2 := NewBmsRegistry(BmsFileStore{Path: path})
	if p, ok := r2.Get("AA:BB:CC:DD:EE:01"); !ok || p.Alias != "A" || r2.UseAsMain("siseli:1") {
		t.Fatalf("reload %+v ok=%v", p, ok)
	}
}

func TestWeightedSOCPackOnly(t *testing.T) {
	// Two equal packs at 80% and 20% → 50%. Inverter is not in the mix.
	if v, ok := WeightedSOC([]SocItem{{80, 5000}, {20, 5000}}); !ok || !near(v, 50.0, 1e-9) {
		t.Fatal(v)
	}
	if v, ok := WeightedSOC([]SocItem{{100, 1000}, {50, 3000}}); !ok || !near(v, 62.5, 1e-9) {
		t.Fatal(v)
	}
	if _, ok := WeightedSOC(nil); ok {
		t.Fatal("empty → none")
	}
	if _, ok := WeightedSOC([]SocItem{{80, 0}}); ok {
		t.Fatal("zero capacity → none")
	}
}

const now0 = 1_700_000_000.0

func TestOverlayReplacesHeadlineKeepsPortal(t *testing.T) {
	tele := map[string]any{"battery_percent": 64, "solar_input_w": 100, "source": "siseli"}
	packs := []Pack{{MAC: "AA:BB:CC:DD:EE:01", CapacityWh: 5000, Alias: "A"}, {MAC: "AA:BB:CC:DD:EE:02", CapacityWh: 5000, Alias: "B"}}
	live := map[string]Reading{
		"AA:BB:CC:DD:EE:01": {SocPct: f(80), TS: f(now0), CurrentA: f(10), PowerW: f(500), VoltageV: f(53.0), TempC: f(22)},
		"AA:BB:CC:DD:EE:02": {SocPct: f(20), TS: f(now0), CurrentA: f(5), PowerW: f(250), VoltageV: f(53.1), TempC: f(28)},
	}
	out := OverlayTelemetry(tele, OverlayOpts{Packs: packs, Live: live, UseAsMain: true, Now: now0})
	if tele["battery_percent"] != 64 {
		t.Fatal("original mutated")
	}
	if out["battery_percent"] != 50.0 || out["system_soc_pct"] != 50.0 || out["inverter_soc_pct"] != 64 ||
		out["main_soc_pct"] != 64 || out["bms_source"] != true || out["capacity_wh"] != 10000 ||
		out["battery_temp_c"] != 28.0 || out["battery_status"] != 1 {
		t.Fatalf("out = %v", out)
	}
}

func TestOverlayLoadIsGreaterOfSiseliAndBMSDischarge(t *testing.T) {
	packs := []Pack{{MAC: "AA:BB:CC:DD:EE:01", CapacityWh: 5000, Alias: "A"}, {MAC: "AA:BB:CC:DD:EE:02", CapacityWh: 5000, Alias: "B"}}
	live := map[string]Reading{
		"AA:BB:CC:DD:EE:01": {SocPct: f(60), TS: f(now0), CurrentA: f(-4), PowerW: f(-200), VoltageV: f(52.0)},
		"AA:BB:CC:DD:EE:02": {SocPct: f(55), TS: f(now0), CurrentA: f(-2), PowerW: f(-100), VoltageV: f(52.0)},
	}
	run := func(load any) map[string]any {
		return OverlayTelemetry(map[string]any{"battery_percent": 55, "output_power_w": load, "source": "siseli"},
			OverlayOpts{Packs: packs, Live: live, UseAsMain: true, Now: now0})
	}
	if run(80)["output_power_w"] != 300 {
		t.Error("higher bms")
	}
	if run(500)["output_power_w"] != 500 {
		t.Error("higher siseli")
	}
	if run(nil)["output_power_w"] != 300 {
		t.Error("missing siseli")
	}
	charging := OverlayTelemetry(map[string]any{"battery_percent": 55, "output_power_w": 90, "source": "siseli"},
		OverlayOpts{Packs: []Pack{{MAC: "AA:BB:CC:DD:EE:01", CapacityWh: 5000}},
			Live:      map[string]Reading{"AA:BB:CC:DD:EE:01": {SocPct: f(60), TS: f(now0), CurrentA: f(8), PowerW: f(400), VoltageV: f(52.0)}},
			UseAsMain: true, Now: now0})
	if charging["output_power_w"] != 90 {
		t.Errorf("charging = %v", charging["output_power_w"])
	}
}

func TestOverlayGridIsOnlyTheShortfall(t *testing.T) {
	packs := []Pack{{MAC: "AA:BB:CC:DD:EE:01", CapacityWh: 5000, Alias: "A"}}
	live := map[string]Reading{"AA:BB:CC:DD:EE:01": {SocPct: f(39), TS: f(now0), CurrentA: f(-24), PowerW: f(-1213), VoltageV: f(51.0)}}
	d := OverlayTelemetry(map[string]any{"battery_percent": 39, "solar_input_w": 16, "output_power_w": nil, "ac_input_w": 1197, "source": "siseli"},
		OverlayOpts{Packs: packs, Live: live, UseAsMain: true, Now: now0, CalcGrid: true})
	if d["output_power_w"] != 1213 || d["ac_input_w"] != 0 {
		t.Errorf("discharging = %v", d)
	}
	s := OverlayTelemetry(map[string]any{"battery_percent": 39, "solar_input_w": 16, "output_power_w": 1500, "source": "siseli"},
		OverlayOpts{Packs: packs, Live: live, UseAsMain: true, Now: now0, CalcGrid: true})
	if s["ac_input_w"] != 1500-16-1213 {
		t.Errorf("short = %v", s["ac_input_w"])
	}
	off := OverlayTelemetry(map[string]any{"battery_percent": 39, "solar_input_w": 16, "output_power_w": 1500, "ac_input_w": 1197, "source": "siseli"},
		OverlayOpts{Packs: packs, Live: live, UseAsMain: true, Now: now0, CalcGrid: false})
	if off["ac_input_w"] != 0 {
		t.Errorf("off = %v", off["ac_input_w"])
	}
}

func TestOverlayFallsBackWhenStaleOrDisabled(t *testing.T) {
	tele := map[string]any{"battery_percent": 64}
	packs := []Pack{{MAC: "AA:BB:CC:DD:EE:01", CapacityWh: 5000}}
	out := OverlayTelemetry(tele, OverlayOpts{Packs: packs, UseAsMain: true, Now: now0,
		Live: map[string]Reading{"AA:BB:CC:DD:EE:01": {SocPct: f(10), TS: f(now0 - 500)}}})
	if out["battery_percent"] != 64 {
		t.Fatalf("out = %v", out)
	}
	if _, has := out["bms_source"]; has {
		t.Fatal("bms_source must be absent")
	}
	out2 := OverlayTelemetry(tele, OverlayOpts{Packs: packs, UseAsMain: false, Now: now0,
		Live: map[string]Reading{"AA:BB:CC:DD:EE:01": {SocPct: f(10), TS: f(now0)}}})
	if out2["battery_percent"] != 64 {
		t.Fatalf("out2 = %v", out2)
	}
}

func TestOverlayIgnoreInverterSOCHidesPortal(t *testing.T) {
	tele := map[string]any{"battery_percent": 55, "solar_input_w": 100}
	packs := []Pack{{MAC: "AA:BB:CC:DD:EE:01", CapacityWh: 5000, Alias: "A"}}
	live := map[string]Reading{"AA:BB:CC:DD:EE:01": {SocPct: f(29), TS: f(now0), CurrentA: f(1), PowerW: f(50), VoltageV: f(53)}}
	out := OverlayTelemetry(tele, OverlayOpts{Packs: packs, Live: live, UseAsMain: true, Now: now0, IgnoreInverterSOC: true})
	if out["battery_percent"] != 29.0 || out["bms_source"] != true || out["ignore_inverter_soc"] != true {
		t.Fatalf("out = %v", out)
	}
	if _, has := out["inverter_soc_pct"]; has {
		t.Fatal("inverter_soc_pct must be dropped")
	}
	if tele["battery_percent"] != 55 {
		t.Fatal("original mutated")
	}
}

func TestOverlayIgnoreInverterSOCStaleBMSDropsPortal(t *testing.T) {
	out := OverlayTelemetry(map[string]any{"battery_percent": 55},
		OverlayOpts{Packs: []Pack{{MAC: "AA:BB:CC:DD:EE:01", CapacityWh: 5000}}, Live: map[string]Reading{},
			UseAsMain: true, Now: now0, IgnoreInverterSOC: true})
	if v, has := out["battery_percent"]; !has || v != nil {
		t.Fatalf("out = %v", out)
	}
	if b, _ := out["bms_source"].(bool); b {
		t.Fatal("no bms_source")
	}
	if out["ignore_inverter_soc"] != true {
		t.Fatal("ignore flag")
	}
}

func TestUIPackRowsKeepStaleWithError(t *testing.T) {
	packs := []Pack{{MAC: "AA:BB:CC:DD:EE:01", CapacityWh: 1000, Alias: "P1"}}
	rows := UIPackRows(packs, nil, now0, 0)
	if rows[0]["source"] != "bms" || rows[0]["error"] != "no reading" {
		t.Fatalf("%v", rows[0])
	}
	live := map[string]Reading{"AA:BB:CC:DD:EE:01": {SocPct: f(40), TS: f(now0 - 400), PowerW: f(-100), TempC: f(19)}}
	rows = UIPackRows(packs, live, now0, 0)
	if rows[0]["rb"] != 40.0 || rows[0]["op"] != 100 || rows[0]["error"] != "stale" {
		t.Fatalf("%v", rows[0])
	}
}

func TestInferCapacityWh(t *testing.T) {
	if wh, ok := InferCapacityWh(&Reading{FullAh: f(100), VoltageV: f(51.2)}); !ok || wh != 5120 {
		t.Fatal(wh)
	}
	if _, ok := InferCapacityWh(&Reading{}); ok {
		t.Fatal("empty → none")
	}
	if _, ok := InferCapacityWh(nil); ok {
		t.Fatal("nil → none")
	}
}

func TestLatestLiveTS(t *testing.T) {
	packs := []Pack{{MAC: "A"}, {MAC: "B"}, {MAC: "C"}}
	live := map[string]Reading{"A": {TS: f(5)}, "B": {TS: f(9)}}
	if ts, ok := LatestLiveTS(packs, live); !ok || ts != 9 {
		t.Fatal(ts)
	}
	if _, ok := LatestLiveTS(packs, nil); ok {
		t.Fatal("none")
	}
}
