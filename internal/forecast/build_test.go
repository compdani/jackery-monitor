package forecast

import (
	"encoding/json"
	"fmt"
	"github.com/compdani/jackery-monitor/internal/automation"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestBuildPythonParity(t *testing.T) {
	// Python's historical profile uses the process timezone independently of
	// the explicit device offset, as does FitLoadProfile.
	old := time.Local
	time.Local = time.UTC
	defer func() { time.Local = old }()
	raw, e := readFixture("testdata/build.json.gz")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		History, Weather []Row
		Capacity, Start  float64
		Expected         Row
		Drain, Overhead  []float64
		Options          struct {
			Now        int64               `json:"now_ts"`
			Packs      int                 `json:"pack_count"`
			Offset     int                 `json:"utc_offset_seconds"`
			Floor      *float64            `json:"ac_charge_floor_pct"`
			Extra      float64             `json:"extra_load_w"`
			ExtraFloor *float64            `json:"extra_load_floor_pct"`
			SleepStart string              `json:"sleep_start"`
			SleepEnd   string              `json:"sleep_end"`
			Mode       string              `json:"load_mode"`
			Windows    []automation.Window `json:"load_windows"`
			Solar      map[int64]float64   `json:"fs_hourly"`
		}
	}
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			o := c.Options
			got := Build(c.History, c.Weather, c.Start, c.Capacity, BuildOptions{Now: o.Now, Packs: o.Packs, Offset: o.Offset, SleepStart: o.SleepStart, SleepEnd: o.SleepEnd, LoadMode: o.Mode, Windows: o.Windows, SolarHourly: o.Solar, Simulation: Options{ACFloor: o.Floor, ExtraLoadW: o.Extra, ExtraFloor: o.ExtraFloor}})
			b, _ := json.Marshal(got)
			var normalized Row
			_ = json.Unmarshal(b, &normalized)
			// Verify parity on unchanged core fields; solar coefficient, diurnal
			// shading, and resulting hourly/daily solar values intentionally use
			// the improved winter clear-sky + recency-weighted shading model.
			for _, k := range []string{"ready", "readiness", "capacity_wh", "starting_soc_pct", "solar_source", "load_mode", "solar_recent_peak_w", "solar_cap_w", "charge_ceiling_pct", "overall_load_w", "output_w_p95", "parasitic_w", "pack_baseline_w", "pack_count", "effective_parasitic_w", "inverter_overhead_pct", "inverter_overhead_n_windows", "inverter_overhead_source", "idle_overhead_w", "idle_overhead_n_windows", "charge_efficiency", "charge_efficiency_n_windows", "charge_efficiency_source"} {
				if want, ok := c.Expected[k]; ok && !reflect.DeepEqual(normalized[k], want) {
					t.Errorf("%s: got %v want %v", k, normalized[k], want)
				}
			}
			if wantHours, ok := c.Expected["forecast"].([]any); ok {
				gotHours, _ := normalized["forecast"].([]any)
				if len(gotHours) != len(wantHours) {
					t.Errorf("forecast len: got %d want %d", len(gotHours), len(wantHours))
				}
			}
			a, bp, n := FitDrainModel(c.History, c.Capacity, o.Packs)
			if math.Abs(a-c.Drain[0]) > 1e-9 || math.Abs(bp-c.Drain[1]) > 1e-9 || n != int(c.Drain[2]) {
				t.Errorf("drain got %v/%v/%v want %v", a, bp, n, c.Drain)
			}
			p, n := FitInverterOverhead(c.History, c.Capacity)
			if math.Abs(p-c.Overhead[0]) > 1e-9 || n != int(c.Overhead[1]) {
				t.Errorf("overhead %v/%v want %v", p, n, c.Overhead)
			}
		})
	}
}

// Ported from tests/test_forecaster.py capacity catalog and diagnostic-source cases.
func TestCatalogCapacitiesAndDiagnosticSources(t *testing.T) {
	for model, want := range map[int]float64{13: 5040, 22: 5040, 2: 2042, 17: 1534, 21: 1512} {
		if BatteryCapacity(model) != want {
			t.Fatal(model, BatteryCapacity(model))
		}
	}
	for model, want := range map[int]float64{2: 2042, 13: 5040, 22: 5040} {
		if ExpansionCapacity(model) != want {
			t.Fatal(model, ExpansionCapacity(model))
		}
	}
	if ExpansionCapacity(99) != BatteryCapacity(99) {
		t.Fatal(ExpansionCapacity(99), BatteryCapacity(99))
	}
	now := int64(1_800_000_000)
	history := []Row{}
	for i := 0; i < 15; i++ {
		ts := float64(now - 60*3600 + int64(i)*3*3600)
		for j, soc := range []float64{95, 93} {
			history = append(history, Row{"ts": ts + float64(j)*3600, "battery_pct": soc, "output_w": 545.0, "output_wh": 545.0, "solar_w": 0.0, "solar_wh": 0.0, "ac_input_wh": 0.0, "ac_input_w": 0.0, "input_wh": 0.0, "input_w": 0.0})
		}
	}
	weather := []Row{}
	for i := 0; i < 48; i++ {
		weather = append(weather, Row{"ts": float64(now + int64(i)*3600), "ghi_w_m2": 0.0, "cloud_cover_pct": 100.0})
	}
	res := Build(history, weather, 50, 30000, BuildOptions{Now: now, Horizon: 24})
	if res["ready"] != true || Number(res, "output_w_p95") <= 0 || res["inverter_overhead_source"] != "fit" || res["charge_efficiency_source"] != "default" {
		t.Fatal(res["ready"], res["output_w_p95"], res["inverter_overhead_source"], res["charge_efficiency_source"])
	}
}
