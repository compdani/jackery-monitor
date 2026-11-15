package store

import (
	"math"
	"testing"
)

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
