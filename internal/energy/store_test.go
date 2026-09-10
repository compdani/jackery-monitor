package energy_test

import (
	"jackery-monitor/internal/energy"
	"math"
	"testing"
	"time"

	"jackery-monitor/internal/jackery"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

func testStore(t *testing.T) (core.App, *energy.Store) {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.ResetBootstrapState() })
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	return app, energy.New(app)
}
func total(t *testing.T, s *energy.Store, sn, key string) float64 {
	t.Helper()
	r, err := s.Totals(sn, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return energy.Number(r["lifetime"].(map[string]any)[key])
}
func TestIntegrationDuplicatesGapsAndRestart(t *testing.T) {
	app, s := testStore(t)
	sn := "S1"
	now := time.Now()
	if err := s.UpsertDevice(jackery.Device{SN: sn, Name: "Station", ModelCode: 13}, now); err != nil {
		t.Fatal(err)
	}
	soc := 80.0
	t0 := float64(now.Unix() - 600)
	r := energy.Reading{TS: t0, Input: 600, Output: 200, Solar: 500, Grid: 100, Diverted: 2000, SOC: &soc}
	if err := s.Record(sn, r); err != nil {
		t.Fatal(err)
	}
	r.TS += 300
	if err := s.Record(sn, r); err != nil {
		t.Fatal(err)
	}
	if got := total(t, s, sn, "input_wh"); math.Abs(got-50) > 1e-8 {
		t.Fatalf("input Wh=%v", got)
	}
	if got := total(t, s, sn, "solar_charge_diverted_wh"); math.Abs(got-200*300.0/3600) > 1e-8 {
		t.Fatal("diversion exceeds output")
	}
	// Duplicate/out-of-order readings cannot move the integration baseline back.
	s.Record(sn, r)
	earlier := r
	earlier.TS -= 100
	s.Record(sn, earlier)
	if total(t, s, sn, "input_wh") != 50 {
		t.Fatal("duplicate was integrated")
	}
	r.TS += 601
	s.Record(sn, r)
	if total(t, s, sn, "input_wh") != 50 {
		t.Fatal("long gap fabricated energy")
	}
	restarted := energy.New(app)
	r.TS += 30
	restarted.Record(sn, r)
	if total(t, restarted, sn, "input_wh") != 50 {
		t.Fatal("restart extrapolated a gap")
	}
	r.TS += 60
	restarted.Record(sn, r)
	if math.Abs(total(t, restarted, sn, "input_wh")-60) > 1e-8 {
		t.Fatal("restart did not resume integration")
	}
	r.TS += 60
	r.Input = math.NaN()
	if err := restarted.Record(sn, r); err == nil {
		t.Fatal("NaN reading accepted")
	}
}
func TestPacksCapacityHistoryAndLocalDaily(t *testing.T) {
	_, s := testStore(t)
	now := time.Now()
	sn := "PACKED"
	s.UpsertDevice(jackery.Device{SN: sn, ModelCode: 2}, now)
	ts := now.Add(-time.Minute).Unix()
	soc := 20.0
	s.Record(sn, energy.Reading{TS: float64(ts), SOC: &soc})
	s.Record(sn, energy.Reading{TS: float64(ts + 30), SOC: &soc, Input: 1200, Solar: 1200})
	packs := []map[string]any{{"deviceSn": "P1", "deviceOrder": 1, "rb": 80.0, "ip": 100.0}}
	if err := s.RecordPacks(sn, packs, ts); err != nil {
		t.Fatal(err)
	}
	rows, err := s.History(sn, 24, 60, now)
	if err != nil || len(rows) == 0 {
		t.Fatalf("history: %v", err)
	}
	if energy.Number(rows[0]["system_soc"]) != 50 {
		t.Fatalf("expected weighted SOC 50, got %v", rows[0])
	}
	override := 10080
	if err := s.SetCapacity(sn, &override); err != nil {
		t.Fatal(err)
	}
	devices, _ := s.Devices()
	if energy.Number(devices[0]["capacity_wh_override"]) != 10080 {
		t.Fatal("capacity not persisted")
	}
	if err := s.SetCapacity(sn, nil); err != nil {
		t.Fatal(err)
	}
	invalid := 400
	if s.SetCapacity(sn, &invalid) == nil {
		t.Fatal("invalid capacity accepted")
	}
	if err := s.RecordPacks(sn, []map[string]any{}, ts+1); err != nil {
		t.Fatal(err)
	}
	latest, _, err := s.Packs(sn)
	if err != nil || len(latest) != 0 {
		t.Fatal("explicit empty pack report resurrected old packs")
	}
	daily, err := s.Daily(sn, 1, -6*3600, now)
	if err != nil || len(daily) == 0 {
		t.Fatalf("daily: %v", err)
	}
	expected := time.Unix(ts, 0).In(time.FixedZone("test", -6*3600)).Format("2006-01-02")
	if daily[0]["date"] != expected {
		t.Fatalf("wrong local date: %v", daily)
	}
}
