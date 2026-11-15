package store

import (
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/pocketbase/pocketbase"
	"testing"
)

func predictionStore(t *testing.T) *Store {
	t.Helper()
	a := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()})
	if e := a.Bootstrap(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.ResetBootstrapState() })
	if e := a.RunAllMigrations(); e != nil {
		t.Fatal(e)
	}
	return &Store{App: a}
}
func TestForecastPredictionReference(t *testing.T) {
	s := predictionStore(t)
	now := float64(1736895600)
	target := now - 3600
	made := target - 3600
	predictions := []forecast.Row{{"ts": target, "predicted_soc": 70.0}, {"ts": nil, "predicted_soc": 50.0}, {"ts": target + 3600, "predicted_soc": nil}}
	for _, at := range []float64{made, made + 60} {
		n, e := s.RecordForecast("SN", at, predictions)
		if e != nil || n != 1 {
			t.Fatal(n, e)
		}
	}
	rows, e := s.List("predictions", "SN", 0, 100)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
	put := func(c, k string, r map[string]any) {
		t.Helper()
		if e := s.Put(c, k, "SN", r); e != nil {
			t.Fatal(e)
		}
	}
	for i, ts := range []float64{target - 60, target + 60} {
		put("samples", string(rune('a'+i)), map[string]any{"ts": ts, "battery_percent": 65.0})
	}
	opts := AccuracyOptions{Now: int64(now)}
	got, e := s.PredictionAccuracy("SN", opts)
	if e != nil || len(got) != 1 {
		t.Fatal(got, e)
	}
	for k, want := range map[string]any{"predicted_soc": 70.0, "actual_soc": 65.0, "error": 5.0, "lead_time_h": 1.0} {
		if got[0][k] != want {
			t.Errorf("%s=%v want %v", k, got[0][k], want)
		}
	}
	// Future targets and missing actuals are excluded.
	if _, e := s.RecordForecast("SN", made, []forecast.Row{{"ts": now + 86400, "predicted_soc": 60.0}, {"ts": target - 86400, "predicted_soc": 60.0}}); e != nil {
		t.Fatal(e)
	}
	got, e = s.PredictionAccuracy("SN", opts)
	if e != nil || len(got) != 1 {
		t.Fatal(got, e)
	}
	opts.SinceMadeAt = int64(made + 3600)
	got, e = s.PredictionAccuracy("SN", opts)
	if e != nil || len(got) != 0 {
		t.Fatal(got, e)
	}
}
func TestPredictionCapacityWeightingReference(t *testing.T) {
	s := predictionStore(t)
	target := 1736892000.0
	for _, ts := range []float64{target - 60, target + 60} {
		if e := s.Put("samples", ID(), "SN", map[string]any{"ts": ts, "last_battery_pct": 100.0}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.RecordForecast("SN", target-3600, []forecast.Row{{"ts": target, "predicted_soc": 88.0}}); e != nil {
		t.Fatal(e)
	}
	if e := s.Put("battery_packs", "packs", "SN", map[string]any{"ts": target, "packs": []any{map[string]any{"rb": 76.0}, map[string]any{"rb": 76.0}}}); e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct{ capacity, actual, err float64 }{{0, 100, 12}, {5040, 84, 4}} {
		got, e := s.PredictionAccuracy("SN", AccuracyOptions{Now: int64(target + 3600), MainCapacity: c.capacity, PackCapacity: c.capacity})
		if e != nil || len(got) != 1 {
			t.Fatal(got, e)
		}
		if got[0]["actual_soc"] != c.actual || got[0]["error"] != c.err {
			t.Fatal(got)
		}
	}
}
func TestDailySummaryDateAndPredictionTimestamp(t *testing.T) {
	s := predictionStore(t)
	now := 1736895600.0
	for d := 0; d < 10; d++ {
		date := fmtDate(int64(now) - int64(d)*86400)
		if e := s.UpsertDailySummary("SN", date, now, map[string]any{"predicted_sunset_soc_pct": 80.0 - float64(d)}); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := s.DailySummary("SN", int64(now), 3)
	if e != nil || len(rows) != 4 {
		t.Fatal(rows, e)
	}
	date := fmtDate(int64(now))
	if e = s.UpsertDailySummary("SN", date, now+60, map[string]any{"predicted_sunset_soc_pct": nil, "actual_sunset_soc_pct": 78.0}); e != nil {
		t.Fatal(e)
	}
	rows, e = s.DailySummary("SN", int64(now), 30)
	if e != nil || len(rows) != 10 {
		t.Fatal(rows, e)
	}
	if rows[0]["predictions_made_at"] != now || rows[0]["updated_at"] != now+60 || rows[0]["actual_sunset_soc_pct"] != 78.0 {
		t.Fatal(rows[0])
	}
}

func TestBackfillDailyActualsReference(t *testing.T) {
	s := predictionStore(t)
	now := int64(1736895600)
	sunset, sunrise := float64(now-3*3600), float64(now-3600)
	for i, at := range []float64{sunset - 60, sunset + 60, sunrise - 60, sunrise + 60} {
		soc := 76.0
		if i >= 2 {
			soc = 49
		}
		if e := s.Put("samples", ID(), "SN", map[string]any{"ts": at, "battery_percent": soc}); e != nil {
			t.Fatal(e)
		}
	}
	for d := 0; d < 2; d++ {
		rise := sunrise
		if d == 0 {
			rise = float64(now + 6*3600)
		}
		if e := s.UpsertDailySummary("SN", fmtDate(now-int64(d)*86400), float64(now-60), map[string]any{"sunset_ts": sunset, "sunrise_ts": rise, "predicted_sunset_soc_pct": 80.0, "predicted_sunrise_soc_pct": 45.0}); e != nil {
			t.Fatal(e)
		}
	}
	n, e := s.BackfillDailyActuals("SN", now, 14, 0, 0)
	if e != nil || n != 3 {
		t.Fatal(n, e)
	}
	n, e = s.BackfillDailyActuals("SN", now, 14, 0, 0)
	if e != nil || n != 0 {
		t.Fatal(n, e)
	}
	rows, e := s.DailySummary("SN", now, 14)
	if e != nil || rows[0]["actual_sunset_soc_pct"] != 76.0 || rows[1]["actual_sunrise_soc_pct"] != 49.0 || rows[0]["actual_sunrise_soc_pct"] != nil || rows[0]["predictions_made_at"] != float64(now-60) {
		t.Fatal(rows, e)
	}
}
