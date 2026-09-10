package cost

import (
	"math"
	"testing"
	"time"
)

func TestSeasonalMidnightRatesAndSavings(t *testing.T) {
	p := Plan{Type: "tou", Currency: "USD", Slots: []Slot{{Start: 23, End: 7, Rate: .1, Months: []int{6, 7, 8}}, {Start: 0, End: 24, Rate: .5}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	zone, _ := time.LoadLocation("America/Chicago")
	at := time.Date(2026, 7, 1, 23, 30, 0, 0, zone)
	if RateAt(p, at) != .1 || RateAt(p, at.Add(8*time.Hour)) != .5 || RateAt(p, at.AddDate(0, 3, 0)) != .5 {
		t.Fatal("TOU boundary or season failed")
	}
	rows := []map[string]any{{"ts": float64(at.Unix()), "output_wh": 2000.0, "input_wh": 1000.0, "solar_wh": 800.0, "ac_input_wh": 500.0}}
	got := Savings(rows, p, zone)
	if got["solar_savings"] != .2 || got["grid_cost"] != .05 || got["net_savings"] != .15 {
		t.Fatal(got)
	}
	delete(rows[0], "ac_input_wh")
	got = Savings(rows, p, zone)
	if got["grid_cost"] != .02 {
		t.Fatal("legacy grid fallback", got)
	}
	p = Default()
	p.Rate = math.NaN()
	if p.Validate() == nil {
		t.Fatal("accepted NaN")
	}
}
