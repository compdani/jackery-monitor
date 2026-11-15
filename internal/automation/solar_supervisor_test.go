package automation

import (
	"math"
	"testing"
)

func TestSolarNormalizationAndSpread(t *testing.T) {
	c := NormalizeSolar(map[string]any{"mode": "ACTIVE", "car_load_w": "1600", "comfort_low_pct": 90, "comfort_high_pct": 40, "balance_every_days": 0, "inverter_protect_load_w": 499})
	if c["mode"] != "active" || c["car_load_w"] != float64(1600) || c["comfort_low_pct"] != float64(30) || c["balance_every_days"] != float64(0) || c["inverter_protect_load_w"] != float64(2100) {
		t.Fatal(c)
	}
	for _, p := range [][]map[string]any{nil, {{"rb": 23}}, {{"rb": nil}, {"rb": "junk"}, {"rb": math.NaN()}}} {
		if PackSOCSpread(p) != nil {
			t.Fatal(p)
		}
	}
	v := PackSOCSpread([]map[string]any{{"rb": 20}, {"rb": "70"}, {"junk": 0}, {"rb": nil}})
	if v == nil || *v != 50 {
		t.Fatal(v)
	}
}
func TestDivertedMeasurementPaths(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }
	c := NormalizeSolar(map[string]any{"mode": "active"})
	tests := []struct {
		name         string
		st           SolarState
		output, want float64
	}{
		{"off relay", SolarState{}, 1500, 0},
		{"fresh meter", SolarState{On: true, Power: ptr(1310), PowerTS: 1000}, 9999, 1310},
		{"zero car", SolarState{On: true, Power: ptr(.4), PowerTS: 1000}, 280, 0},
		{"noise", SolarState{On: true, Power: ptr(2.7), PowerTS: 1000}, 80, 0},
		{"stale meter", SolarState{On: true, Power: ptr(1310), PowerTS: 700, Baseline: ptr(80), Learned: ptr(1300)}, 1380, 1300},
		{"unsupported meter", SolarState{On: true, Baseline: ptr(80), Learned: ptr(1300)}, 1380, 1300},
		{"disconnected", SolarState{On: true, Baseline: ptr(80), Learned: ptr(1300)}, 200, 0},
		{"no measurement", SolarState{On: true, Baseline: ptr(80)}, 1380, 0},
		{"output fell", SolarState{On: true, Baseline: ptr(200), Learned: ptr(1300)}, 150, 0},
		{"output clamp", SolarState{On: true, Power: ptr(1310), PowerTS: 1000}, 500, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SolarDivertedW(c, tt.st, tt.output, 1000); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
	c["mode"] = "off"
	if SolarDivertedW(c, SolarState{On: true, Power: ptr(1400), PowerTS: 1000}, 1500, 1000) != 0 {
		t.Fatal("disabled diversion")
	}
}
