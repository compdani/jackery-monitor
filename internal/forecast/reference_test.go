package forecast

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"testing"
)

func TestCapturedForecasterTests(t *testing.T) {
	raw, e := readFixture("testdata/forecaster-tests.json.gz")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name  string
		Input struct {
			History    []Row    `json:"energy_history"`
			Weather    []Row    `json:"weather_hourly"`
			Capacity   float64  `json:"capacity_wh"`
			Packs      int      `json:"pack_count"`
			Offset     int      `json:"utc_offset_seconds"`
			TZ         int      `json:"tz_offset_seconds"`
			K          float64  `json:"k"`
			Hours      []Row    `json:"forecast_hours"`
			Start      *float64 `json:"starting_soc_pct"`
			Car        float64  `json:"car_load_w"`
			Floor      float64  `json:"comfort_floor_pct"`
			MinInput   float64  `json:"min_input_w"`
			MinSamples int      `json:"min_samples"`
			Percentile float64  `json:"percentile"`
		}
		Expected any
	}
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("%s/%d", c.Name, i), func(t *testing.T) {
			in := c.Input
			var got any
			switch c.Name {
			case "fit_drain_model":
				a, b, n := FitDrainModel(in.History, in.Capacity, in.Packs)
				got = []any{a, b, n}
			case "fit_inverter_overhead_pct":
				p, n := FitInverterOverhead(in.History, in.Capacity)
				got = []any{p, n}
			case "forecast_readiness":
				got = Readiness(in.History, in.Capacity)
			case "solar_calibration_stats":
				got = SolarCalibration(in.History, in.Weather, in.K)
			case "summarize_forecast_days":
				got = SummarizeDays(in.Hours, in.Start, in.Offset)
			case "summarize_ev_divert":
				got = SummarizeEVDivert(in.Hours, in.Car, in.Floor, in.Offset)
			case "fit_max_charge_w":
				v, n := FitMaxCharge(in.History, in.Weather, in.TZ, in.MinInput, in.MinSamples, in.Percentile)
				got = []any{v, n}
			default:
				t.Fatal(c.Name)
			}
			b, e := json.Marshal(got)
			if e != nil {
				t.Fatal(e)
			}
			var normalized any
			_ = json.Unmarshal(b, &normalized)
			if !parityEqual(normalized, c.Expected) {
				t.Fatalf("got %v want %v", normalized, c.Expected)
			}
		})
	}
}

func parityEqual(a, b any) bool {
	if x, ok := a.(float64); ok {
		y, ok := b.(float64)
		return ok && math.Abs(x-y) <= 1e-9
	}
	if x, ok := a.([]any); ok {
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !parityEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	if x, ok := a.(map[string]any); ok {
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !parityEqual(v, w) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

func TestAccuracySignedBiasReference(t *testing.T) {
	samples := []map[string]any{{"lead_time_h": 2.0, "predicted_soc": 70.0, "actual_soc": 66.0, "error": 4.0}, {"lead_time_h": 5.0, "predicted_soc": 50.0, "actual_soc": 52.0, "error": 2.0}}
	b := BucketAccuracy(samples)["≤6h"]
	if b["n"] != 2 || b["mae"] != 3.0 || b["bias_pp"] != 1.0 {
		t.Fatal(b)
	}
}
func TestCapacityCatalog(t *testing.T) {
	if BatteryCapacity(13) != 5040 || BatteryCapacity(19) != 3024 || BatteryCapacity(-1) != 3024 || ExpansionCapacity(2) != 2042 || ExpansionCapacity(-1) != BatteryCapacity(-1) {
		t.Fatal("catalog values differ from Python")
	}
}

func readFixture(name string) ([]byte, error) {
	f, e := os.Open(name)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	z, e := gzip.NewReader(f)
	if e != nil {
		return nil, e
	}
	defer z.Close()
	return io.ReadAll(z)
}
