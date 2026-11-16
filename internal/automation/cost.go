package automation

import (
	"errors"
	"math"
	"time"
)

type Slot struct {
	Start  int     `json:"start_hour"`
	End    int     `json:"end_hour"`
	Rate   float64 `json:"rate"`
	Months []int   `json:"months,omitempty"`
}
type CostPlan struct {
	Type     string  `json:"type"`
	Rate     float64 `json:"rate_per_kwh"`
	Currency string  `json:"currency"`
	Slots    []Slot  `json:"tou_rates"`
}

func HourInSlot(h, a, b int) bool {
	if a <= b {
		return h >= a && h < b
	}
	return h >= a || h < b
}
func (p CostPlan) Validate() error {
	if p.Type != "flat" && p.Type != "tou" {
		return errors.New("type must be flat or tou")
	}
	if p.Rate < 0 || math.IsNaN(p.Rate) || math.IsInf(p.Rate, 0) {
		return errors.New("invalid rate")
	}
	for _, s := range p.Slots {
		if s.Start < 0 || s.Start > 23 || s.End < 0 || s.End > 24 || s.Rate < 0 {
			return errors.New("invalid TOU slot")
		}
	}
	return nil
}
func (p CostPlan) RateAt(ts int64, offset int) float64 {
	if p.Type == "flat" {
		return p.Rate
	}
	t := time.Unix(ts+int64(offset), 0).UTC()
	for _, s := range p.Slots {
		monthOK := len(s.Months) == 0
		for _, m := range s.Months {
			if m == int(t.Month()) {
				monthOK = true
			}
		}
		if monthOK && HourInSlot(t.Hour(), s.Start, s.End) {
			return s.Rate
		}
	}
	return 0
}

type Energy struct {
	TS                         int64
	OutputWH, SolarWH, InputWH float64
	ACInputWH                  *float64
	UTCOffsetSeconds           *int
}

func Savings(rows []Energy, p CostPlan, offset int) map[string]any {
	saved, cost, output, solar, grid := 0.0, 0.0, 0.0, 0.0, 0.0
	for _, r := range rows {
		g := math.Max(0, r.InputWH-r.SolarWH)
		if r.ACInputWH != nil {
			g = math.Max(0, *r.ACInputWH)
		}
		rowOffset := offset
		if r.UTCOffsetSeconds != nil {
			rowOffset = *r.UTCOffsetSeconds
		}
		rate := p.RateAt(r.TS, rowOffset)
		saved += r.OutputWH / 1000 * rate
		cost += g / 1000 * rate
		output += r.OutputWH / 1000
		solar += r.SolarWH / 1000
		grid += g / 1000
	}
	round := func(v float64, n int) float64 { f := math.Pow10(n); return math.RoundToEven(v*f) / f }
	currency := p.Currency
	if currency == "" {
		currency = "USD"
	}
	return map[string]any{"solar_savings": round(saved, 2), "grid_cost": round(cost, 2), "net_savings": round(saved-cost, 2), "output_kwh": round(output, 3), "solar_kwh": round(solar, 3), "grid_kwh": round(grid, 3), "currency": currency}
}
