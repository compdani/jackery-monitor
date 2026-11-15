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
			for k, want := range c.Expected {
				if !reflect.DeepEqual(normalized[k], want) {
					t.Errorf("%s: got %v want %v", k, normalized[k], want)
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
