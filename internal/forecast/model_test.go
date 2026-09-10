package forecast

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

func TestPythonForecastVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/python_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name            string
		Packs           int
		Capacity, Start float64
		Now             int64
		History         []Row
		Weather         []WeatherHour
		Expected        Row
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got := Build(c.History, c.Weather, Options{Now: time.Unix(c.Now, 0), Zone: time.UTC, Capacity: c.Capacity, StartSOC: c.Start, PackCount: c.Packs})
			for _, key := range []string{"solar_coefficient", "fit_samples", "parasitic_w", "inverter_overhead_pct", "charge_efficiency", "overall_load_w", "output_w_p95", "pack_baseline_w"} {
				if math.Abs(num(got, key)-num(c.Expected, key)) > 1e-6 {
					t.Errorf("%s: got %v want %v", key, got[key], c.Expected[key])
				}
			}
			hours := got["forecast"].([]Row)
			expected := c.Expected["forecast"].([]any)
			if len(hours) != len(expected) {
				t.Fatalf("hour count %d != %d", len(hours), len(expected))
			}
			for i, h := range hours {
				want := expected[i].(map[string]any)
				for _, key := range []string{"predicted_soc", "solar_w", "load_w"} {
					if math.Abs(num(h, key)-num(want, key)) > .11 {
						t.Fatalf("hour %d %s: %v != %v", i, key, h[key], want[key])
					}
				}
			}
		})
	}
}
func TestSimulationReserveAndLimits(t *testing.T) {
	floor := 30.0
	hours := []Row{{"solar_w": 0, "load_w": 100}, {"solar_w": 0, "load_w": 100}, {"solar_w": 0, "load_w": 100}}
	got := Simulate(70, 1000, hours, Simulation{Efficiency: .9, ExtraW: 200, ExtraFloor: &floor})
	if num(got[0], "extra_load_w_applied") != 0 || num(got[2], "predicted_soc") < floor {
		t.Fatalf("extra load exhausted reserve: %v", got)
	}
	ceiling := 80.0
	got = Simulate(75, 1000, []Row{{"solar_w": 1000, "load_w": 0}}, Simulation{Efficiency: .9, Ceiling: &ceiling})
	if num(got[0], "predicted_soc") != 80 {
		t.Fatal(got)
	}
	if len(Simulate(50, 0, hours, Simulation{Efficiency: .9})) != 0 {
		t.Fatal("zero capacity must not simulate")
	}
}
func TestSolarHeadroomAndNoPhantomPanels(t *testing.T) {
	rows := []Row{}
	wx := []WeatherHour{}
	for i := range 6 {
		ts := float64(1700000000/3600*3600 + i*3600)
		solar, soc := 1600.0, 60.0
		if i > 1 {
			solar = 400
			soc = 95
		}
		rows = append(rows, Row{"ts": ts, "solar_w": solar, "battery_pct": soc})
		wx = append(wx, WeatherHour{int64(ts), 800, 0})
	}
	k, n, _ := solarFit(rows, wx)
	if k != 2 || n != 2 {
		t.Fatalf("headroom fit %f/%d", k, n)
	}
	for _, r := range rows {
		r["solar_w"] = 20
	}
	k, _, _ = solarFit(rows, wx)
	if k != 0 {
		t.Fatal("sensor noise invented panels")
	}
}
func TestDrainUsesPacksAndExcludesMissingSystemSOC(t *testing.T) {
	rows := []Row{}
	for i := range 12 {
		load := float64(100 + i*80)
		start := float64(1700000000 + i*3*3600)
		drop := (60 + load*1.2 + 120) / 100
		rows = append(rows, Row{"ts": start, "output_wh": load, "system_soc": 96.0, "battery_pct": 96.0}, Row{"ts": start + 3600, "system_soc": 96 - drop, "battery_pct": 50.0})
	}
	a, b, n := drainFit(rows, 10000, 2)
	if math.Abs(a-60) > 1e-6 || math.Abs(b-.2) > 1e-6 || n != 12 {
		t.Fatalf("fit %f/%f/%d", a, b, n)
	}
	rows = append(rows, Row{"ts": 1.0, "battery_pct": 99.0, "output_wh": 1000.0}, Row{"ts": 3601.0, "battery_pct": 50.0})
	a, b, n = drainFit(sorted(rows), 10000, 2)
	if math.Abs(a-60) > 1e-6 || n != 12 {
		t.Fatalf("missing pack SOC biased fit: %f/%f/%d", a, b, n)
	}
}
