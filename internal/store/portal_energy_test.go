package store

import (
	"errors"
	"fmt"
	"github.com/pocketbase/dbx"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestForecastHourlyRollupPreservesTelemetry(t *testing.T) {
	s := predictionStore(t)
	const sn = "forecast"
	start := math.Floor(1794762000.0/3600) * 3600
	for _, row := range []map[string]any{
		{"ts": start, "bucket": start, "last_ac_input_w": 100.0, "ac_input_wh": 100.0 / 60,
			"last_input_w": 100.0, "input_wh": 100.0 / 60, "last_solar_w": 300.0, "solar_wh": 5.0,
			"last_output_w": 500.0, "output_wh": 500.0 / 60, "last_battery_pct": 50.0, "system_soc": 55.0},
		{"ts": start + 60, "bucket": start + 60},
		{"ts": start + 3600, "solar_input_w": 0.0},
		{"ts": start + 3900, "solar_input_w": 0.0},
		{"ts": start + 4200}, // no solar reading at the end of this interval
	} {
		if err := s.Put("samples", SampleKey(sn, num(row["ts"])), sn, row); err != nil {
			t.Fatal(err)
		}
	}
	minutes, err := s.EnergyMinutes(sn, start)
	if err != nil {
		t.Fatal(err)
	}
	raw := EnergyHistory(minutes, start, 3600)
	rolled, err := s.EnergyHourlyFromRollups(sn, start)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw, rolled) {
		t.Fatalf("raw %v\nrollup %v", raw, rolled)
	}
	if len(rolled) != 2 || rolled[0]["solar_coverage_s"] != 60.0 || rolled[1]["solar_coverage_s"] != 300.0 {
		t.Fatalf("wrong observed solar coverage: %v", rolled)
	}
}

func TestPortalProductionOverridesSolarOnlyAndReconciles(t *testing.T) {
	s := predictionStore(t)
	const start = 1794762000.0
	for i := 0; i <= 30; i++ {
		ts := start + float64(i)*60
		if err := s.Put("samples", SampleKey("siseli:99", ts), "siseli:99", map[string]any{"ts": ts, "bucket": ts, "solar_wh": 99.0, "input_wh": 2.0, "output_wh": 3.0, "last_battery_pct": 49.0}); err != nil {
			t.Fatal(err)
		}
	}
	for _, wh := range []float64{435, 435, 440} {
		if err := s.Put("samples", "production:99", "siseli:99", map[string]any{"ts": start, "interval_s": 1800.0, "solar_wh": wh, "_energy_kind": "portal_solar"}); err != nil {
			t.Fatal(err)
		}
		minutes, err := s.EnergyMinutes("siseli:99", start)
		if err != nil {
			t.Fatal(err)
		}
		totals := EnergyTotals(minutes, start+3600, 0)["lifetime"].(map[string]any)
		if math.Abs(num(totals["solar_wh"])-(wh+99)) > 1e-8 || num(totals["input_wh"]) != 62 || num(totals["output_wh"]) != 93 {
			t.Fatal(totals)
		}
		if minutes[0]["last_battery_pct"] != 49.0 {
			t.Fatal(minutes[0])
		}
		tail, err := s.EnergyMinutes("siseli:99", start+900)
		if err != nil || math.Abs(num(tail[0]["solar_wh"])-wh/30) > 1e-8 {
			t.Fatal(tail, err)
		}
	}
}

func TestCloudCadenceAndMinuteBoundaryIntegration(t *testing.T) {
	s := predictionStore(t)
	// Five minutes plus request overhead must still integrate for cloud cadence.
	start := 1794787080.0 // minute-aligned, two minutes before midnight UTC
	start = math.Floor(start/86400)*86400 + 86280
	for i, ts := range []float64{start, start + 302} {
		if err := s.Put("samples", fmt.Sprintf("cadence:%d", i), "cloud", map[string]any{"ts": ts, "input_power_w": 360.0, "output_power_w": 120.0, "energy_max_gap_s": 480.0}); err != nil {
			t.Fatal(err)
		}
	}
	minutes, err := s.EnergyMinutes("cloud", 0)
	if err != nil {
		t.Fatal(err)
	}
	daily := EnergyDaily(minutes, 0)
	if len(daily) != 2 || num(daily[0]["charged_kwh"]) != 0.01 || num(daily[1]["charged_kwh"]) != 0.02 {
		t.Fatal(daily)
	}
	totals := EnergyTotals(minutes, start+400, 0)["lifetime"].(map[string]any)
	if math.Abs(num(totals["input_wh"])-30.2) > 1e-8 {
		t.Fatal(totals)
	}
}

func TestMaterializedEnergyReconcilesPortalRevision(t *testing.T) {
	s := predictionStore(t)
	start := 1794700800.0
	for i, ts := range []float64{start, start + 120, start + 300} {
		if err := s.Put("samples", fmt.Sprintf("raw:%d", i), "siseli:99", map[string]any{"ts": ts, "input_power_w": 360.0, "output_power_w": 120.0, "solar_input_w": 300.0, "battery_percent": 49.0}); err != nil {
			t.Fatal(err)
		}
	}
	for _, wh := range []float64{435, 295} {
		if err := s.Put("samples", "portal:99", "siseli:99", map[string]any{"ts": start, "interval_s": 1800.0, "solar_wh": wh, "_energy_kind": "portal_solar"}); err != nil {
			t.Fatal(err)
		}
		summary, err := s.EnergySummary("siseli:99", 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		totals := EnergyTotals(summary, start+3600, 0)["lifetime"].(map[string]any)
		if math.Abs(num(totals["solar_wh"])-wh) > 1e-8 || math.Abs(num(totals["input_wh"])-30) > 1e-8 {
			t.Fatal(totals)
		}
		daily, err := s.EnergyDailySummary("siseli:99", 0, 0)
		if err != nil || len(daily) != 1 {
			t.Fatal(daily, err)
		}
		if num(daily[0]["solar_kwh"]) != math.RoundToEven(wh/10)/100 {
			t.Fatal(daily)
		}
	}
}

func TestHourlyIncrementalRollupsMatchColdRebuild(t *testing.T) {
	s := predictionStore(t)
	sn := "siseli:hourly"
	start := 1794700800.0
	for i, delta := range []float64{0, 3600, 7200, 1800} {
		power := 360.0
		if delta == 1800 {
			power = 720
		}
		if err := s.Put("samples", fmt.Sprintf("hour:%d", i), sn, map[string]any{"ts": start + delta, "input_power_w": power, "energy_max_gap_s": 5430.0}); err != nil {
			t.Fatal(err)
		}
		incremental, err := s.EnergySummary(sn, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		a := EnergyTotals(incremental, start+10000, 0)["lifetime"].(map[string]any)
		if i == 1 && math.Abs(num(a["input_wh"])-360) > 1e-8 {
			t.Fatal(a)
		}
		if err := s.Delete("settings", "energy_ready:"+sn); err != nil {
			t.Fatal(err)
		}
		cold, err := s.EnergySummary(sn, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		b := EnergyTotals(cold, start+10000, 0)["lifetime"].(map[string]any)
		if math.Abs(num(a["input_wh"])-num(b["input_wh"])) > 1e-8 {
			t.Fatal(i, a, b)
		}
	}
}

func TestHistoricalTimezoneDaysAndDSTOffsets(t *testing.T) {
	s := predictionStore(t)
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]int{"2026-01-15T05:30:00Z": -21600, "2026-07-15T05:30:00Z": -18000, "2026-03-08T07:30:00Z": -21600, "2026-03-08T08:30:00Z": -18000, "2026-11-01T06:30:00Z": -18000, "2026-11-01T07:30:00Z": -21600}
	for stamp := range cases {
		ts, _ := time.Parse(time.RFC3339, stamp)
		if err := s.Put("samples", stamp, "zoned", map[string]any{"ts": float64(ts.Unix()), "bucket": float64(ts.Unix()), "output_wh": 1000.0}); err != nil {
			t.Fatal(err)
		}
	}
	finishEnergyBuild(t, s, "zoned")
	rows, err := s.EnergySummaryAtLocation("zoned", 0, loc)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		stamp := time.Unix(int64(num(r["ts"])), 0).UTC().Format(time.RFC3339)
		if int(num(r["utc_offset_s"])) != cases[stamp] {
			t.Fatal(stamp, r)
		}
	}
	days, err := s.EnergyDailyAtLocation("zoned", 0, loc)
	if err != nil {
		t.Fatal(err)
	}
	dates := map[string]float64{}
	for _, day := range days {
		dates[text(day["date"])] = num(day["consumed_kwh"])
	}
	if dates["2026-01-14"] != 1 || dates["2026-07-15"] != 1 || dates["2026-03-08"] != 2 || dates["2026-11-01"] != 2 {
		t.Fatal(dates)
	}
}

func finishEnergyBuild(t *testing.T, s *Store, sn string) {
	t.Helper()
	for i := 0; i < 10000; i++ {
		err := s.EnsureEnergyRollups(sn)
		if err == nil {
			return
		}
		if !errors.Is(err, ErrEnergyBuilding) {
			t.Fatal(err)
		}
	}
	t.Fatal("energy backfill did not complete")
}

func TestEnergyBackfillResumesAndIdenticalProductionDoesNotDirty(t *testing.T) {
	s := predictionStore(t)
	sn := "resumable"
	start := 1794700800.0
	p := map[string]any{"ts": start, "_energy_kind": "portal_solar", "interval_s": 1800.0, "solar_wh": 100.0}
	if err := s.Put("samples", "first", sn, p); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("samples", "last", sn, map[string]any{"ts": start + 3*86400, "bucket": start + 3*86400, "input_wh": 200.0}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureEnergyRollups(sn); !errors.Is(err, ErrEnergyBuilding) {
		t.Fatal(err)
	}
	progress, err := s.Get("settings", "energy_ready:"+sn)
	if err != nil {
		t.Fatal(err)
	}
	if num(progress["cursor"])-start > 86400 {
		t.Fatal("unbounded chunk", progress)
	}
	// A fresh Store instance resumes the persisted cursor, just like a restart.
	restarted := &Store{App: s.App}
	finishEnergyBuild(t, restarted, sn)
	if err := s.Put("samples", "first", sn, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("settings", "energy_dirty:"+sn); !missing(err) {
		t.Fatal("unchanged production dirtied rollup", err)
	}
	totals, err := s.EnergySummary(sn, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	sum := EnergyTotals(totals, start+4*86400, 0)["lifetime"].(map[string]any)
	if num(sum["solar_wh"]) != 100 || num(sum["input_wh"]) != 200 {
		t.Fatal(sum)
	}
}

func TestImportedBucketBackfillWithLiveRawBoundary(t *testing.T) {
	s := predictionStore(t)
	sn := "siseli:mixed"
	start := 1794700800.0 - 100000*60
	_, err := s.App.DB().NewQuery(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<99999)
 INSERT INTO samples (id,key,device_sn,ts,payload)
 SELECT printf('%015d',x),printf('import:%d',x),{:sn},{:start}+x*60,
 json_object('ts',{:start}+x*60,'bucket',{:start}+x*60,'solar_wh',1,'_energy_kind','aggregate') FROM n`).Bind(dbx.Params{"sn": sn, "start": start}).Execute()
	if err != nil {
		t.Fatal(err)
	}
	liveStart := start + 100000*60
	for i, ts := range []float64{liveStart, liveStart + 3600} {
		if err := s.Put("samples", fmt.Sprintf("live:%d", i), sn, map[string]any{"ts": ts, "input_power_w": 360.0, "output_power_w": 120.0, "energy_max_gap_s": 5430.0}); err != nil {
			t.Fatal(err)
		}
	}
	began := time.Now()
	chunks := 0
	maxChunk := time.Duration(0)
	for ; chunks < 1000; chunks++ {
		beganChunk := time.Now()
		err = s.EnsureEnergyRollups(sn)
		duration := time.Since(beganChunk)
		if duration > maxChunk {
			maxChunk = duration
		}
		if err == nil {
			break
		}
		if !errors.Is(err, ErrEnergyBuilding) {
			t.Fatal(err)
		}
	}
	if chunks == 1000 {
		t.Fatal("backfill did not complete")
	}
	t.Logf("100k imported buckets + live raw: %d chunks, total %s, max chunk %s", chunks+1, time.Since(began), maxChunk)
	rows, err := s.EnergySummary(sn, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	totals := EnergyTotals(rows, liveStart+7200, 0)["lifetime"].(map[string]any)
	if math.Abs(num(totals["solar_wh"])-100000) > 1e-8 || math.Abs(num(totals["input_wh"])-360) > 1e-8 || math.Abs(num(totals["output_wh"])-120) > 1e-8 {
		t.Fatal(totals)
	}
}
