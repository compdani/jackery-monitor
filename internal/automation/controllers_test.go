package automation

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestSmartReferencePlans(t *testing.T) {
	b, e := os.ReadFile("testdata/smart_charge.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Input struct {
			Config      SmartConfig     `json:"config"`
			SOC         *float64        `json:"current_soc_pct"`
			Forecast    ChargeForecast  `json:"forecast"`
			Baseline    *ChargeForecast `json:"baseline_forecast"`
			Cost        CostPlan        `json:"cost_plan"`
			Capacity    float64         `json:"capacity_wh"`
			Offset      int             `json:"tz_offset_seconds"`
			Now         float64         `json:"now_ts"`
			Unavailable string          `json:"forecast_unavailable_reason"`
		}
		Expected ChargePlan
	}
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	for i, c := range cases {
		p := SmartPlan(c.Input.Config, c.Input.SOC, c.Input.Forecast, c.Input.Baseline, c.Input.Cost, c.Input.Capacity, c.Input.Offset, int64(c.Input.Now), c.Input.Unavailable)
		if !reflect.DeepEqual(p, c.Expected) {
			t.Errorf("case %d got %+v want %+v", i, p, c.Expected)
		}
	}
}
func TestSolarReferencePlans(t *testing.T) {
	b, e := os.ReadFile("testdata/solar_charge.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Input    SolarInput
		Expected SolarChargePlan
	}
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	for i, c := range cases {
		p := SolarPlan(c.Input)
		if !reflect.DeepEqual(p, c.Expected) {
			t.Errorf("case %d got %+v want %+v", i, p, c.Expected)
		}
	}
}
func TestWatchdogReferenceReplay(t *testing.T) {
	s := WatchdogState{}
	for _, c := range []struct {
		TS, Output float64
		Want       string
	}{{1000, 1795, "idle"}, {1002, 0, "idle"}, {1004, 0, "idle"}, {1008, 0, "cycle"}, {1010, 0, "waiting"}, {1019, 0, "cycle"}, {1030, 0, "error"}, {2200, 0, "error"}, {2300, 850, "idle"}} {
		if got := s.Evaluate(true, &c.Output, &c.TS, 20, c.TS, 10, 60); got != c.Want {
			t.Fatalf("%+v: %s", c, got)
		}
	}
	if s.HWTripActive || s.EpisodeCycles != 0 || s.ErrorMessage != nil {
		t.Fatal(s)
	}
}
func TestWatchdogCachedAndUserOff(t *testing.T) {
	s := WatchdogState{}
	out, ts := 900., 1000.
	s.Evaluate(true, &out, &ts, 20, ts, 10, 60)
	out = 0
	ts = 1002
	for now := 1002.; now <= 1020; now += 2 {
		if got := s.Evaluate(true, &out, &ts, 20, now, 10, 60); got != "idle" {
			t.Fatal(got)
		}
	}
	if s.LowSamples != 1 {
		t.Fatal(s)
	}
	s.HWTripActive = true
	s.PendingOff = map[string]bool{"request": true}
	if got := s.Evaluate(true, &out, &ts, 20, 1030, 10, 60); got != "user_grace" {
		t.Fatal(got)
	}
	if got := s.Evaluate(false, &out, &ts, 20, 1031, 10, 60); got != "idle" || s.HWTripActive {
		t.Fatal(s, got)
	}
}

func TestWatchdogCapturedReferenceSuite(t *testing.T) {
	b, e := os.ReadFile("testdata/inverter_watchdog.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Input struct {
			State  WatchdogState `json:"state"`
			AC     bool          `json:"ac_on"`
			Output *float64      `json:"output_w"`
			Sample *float64      `json:"sample_ts"`
			Floor  float64       `json:"collapse_floor_w"`
			Now    float64       `json:"now_ts"`
			Retry  float64       `json:"retry_interval_s"`
			Grace  float64       `json:"user_grace_s"`
		}
		Action   string
		Expected WatchdogState
	}
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	var raw []struct {
		Input struct {
			State struct {
				Pending int `json:"pending_count"`
			}
		}
	}
	json.Unmarshal(b, &raw)
	for i, c := range cases {
		state := c.Input.State
		if raw[i].Input.State.Pending > 0 {
			state.PendingOff = map[string]bool{"pending": true}
		}
		got := state.Evaluate(c.Input.AC, c.Input.Output, c.Input.Sample, c.Input.Floor, c.Input.Now, c.Input.Retry, c.Input.Grace)
		state.PendingOff = nil
		if got != c.Action || !reflect.DeepEqual(state, c.Expected) {
			t.Errorf("case %d: action %s want %s; state %+v want %+v", i, got, c.Action, state, c.Expected)
		}
	}
}
