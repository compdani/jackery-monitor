package forecast

import (
	"encoding/json"
	"fmt"
	"github.com/compdani/jackery-monitor/internal/automation"
	"testing"
	"time"
)

func TestReplayPythonParity(t *testing.T) {
	old := time.Local
	time.Local = time.UTC
	defer func() { time.Local = old }()
	raw, e := readFixture("testdata/replay.json.gz")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		History, Weather, Decisions []Row
		Capacity                    float64
		Expected                    []Row
		Summary                     Row
	}
	if e = json.Unmarshal(raw, &cases); e != nil {
		t.Fatal(e)
	}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			got := ReplayDecisions(c.Decisions, c.History, c.Weather, ReplayOptions{Capacity: c.Capacity, MaxChargeW: 800, Cost: automation.CostPlan{Type: "flat", Rate: .12}})
			b, _ := json.Marshal(got)
			var norm any
			json.Unmarshal(b, &norm)
			b, _ = json.Marshal(c.Expected)
			var want any
			json.Unmarshal(b, &want)
			if !parityEqual(norm, want) {
				t.Fatalf("got %v want %v", norm, want)
			}
			b, _ = json.Marshal(SummarizeReplay(got))
			json.Unmarshal(b, &norm)
			b, _ = json.Marshal(c.Summary)
			json.Unmarshal(b, &want)
			if !parityEqual(norm, want) {
				t.Fatal(norm, want)
			}
			// Appending future telemetry cannot change a historical replay.
			future := append(append([]Row{}, c.History...), Row{"ts": 1e12, "output_w": 1e9, "battery_pct": 0.0})
			b, _ = json.Marshal(ReplayDecisions(c.Decisions, future, c.Weather, ReplayOptions{Capacity: c.Capacity, MaxChargeW: 800, Cost: automation.CostPlan{Type: "flat", Rate: .12}}))
			json.Unmarshal(b, &norm)
			b, _ = json.Marshal(c.Expected)
			json.Unmarshal(b, &want)
			if !parityEqual(norm, want) {
				t.Fatal("future telemetry leaked into replay")
			}
		})
	}
}
