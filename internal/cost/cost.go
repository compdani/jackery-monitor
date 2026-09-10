// Package cost values displaced grid power using the configured flat or TOU plan.
package cost

import (
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"jackery-monitor/internal/energy"

	"github.com/pocketbase/pocketbase/core"

	"jackery-monitor/internal/site"
)

type Slot struct {
	Start  int     `json:"start_hour"`
	End    int     `json:"end_hour"`
	Rate   float64 `json:"rate"`
	Label  string  `json:"label"`
	Months []int   `json:"months,omitempty"`
}
type Plan struct {
	Type     string  `json:"type"`
	Rate     float64 `json:"rate_per_kwh"`
	Currency string  `json:"currency"`
	Slots    []Slot  `json:"tou_rates,omitempty"`
}

func Default() Plan { return Plan{Type: "flat", Rate: 0.30, Currency: "USD"} }
func (p *Plan) Validate() error {
	validRate := func(r float64) bool { return !math.IsNaN(r) && r >= 0 && r <= 5 }
	p.Currency = strings.TrimSpace(p.Currency)
	if p.Currency == "" {
		p.Currency = "USD"
	}
	if len(p.Currency) > 8 {
		return errors.New("currency must be at most 8 characters")
	}
	if p.Type == "flat" && validRate(p.Rate) {
		p.Slots = nil
		return nil
	}
	if p.Type != "tou" || len(p.Slots) == 0 || len(p.Slots) > 100 {
		return errors.New("invalid plan shape")
	}
	for i := range p.Slots {
		s := &p.Slots[i]
		if s.Start < 0 || s.Start > 24 || s.End < 0 || s.End > 24 || !validRate(s.Rate) {
			return errors.New("invalid time-of-use slot")
		}
		for _, m := range s.Months {
			if m < 1 || m > 12 {
				return errors.New("invalid month")
			}
		}
		slices.Sort(s.Months)
		s.Months = slices.Compact(s.Months)
		s.Label = string([]rune(s.Label)[:min(48, len([]rune(s.Label)))])
	}
	return nil
}
func Get(app core.App) (Plan, error) {
	p := Default()
	_, err := site.Read(app, "cost_plan", &p)
	if err != nil {
		return p, err
	}
	err = p.Validate()
	return p, err
}
func RateAt(p Plan, t time.Time) float64 {
	if p.Type == "flat" {
		return p.Rate
	}
	for _, s := range p.Slots {
		if len(s.Months) > 0 && !slices.Contains(s.Months, int(t.Month())) {
			continue
		}
		h := t.Hour()
		if s.Start <= s.End && h >= s.Start && h < s.End || s.Start > s.End && (h >= s.Start || h < s.End) {
			return s.Rate
		}
	}
	return 0
}

// Accumulator lets callers value arbitrarily long histories without loading them into memory.
type Accumulator struct{ Saved, GridCost, Output, Solar, Grid float64 }

func (a *Accumulator) Add(outputWH, solarWH, gridWH, rate float64) {
	o, s, g := outputWH/1000, solarWH/1000, max(0, gridWH)/1000
	a.Saved += o * rate
	a.GridCost += g * rate
	a.Output += o
	a.Solar += s
	a.Grid += g
}
func (a Accumulator) Result(currency string) map[string]any {
	round := func(v, n float64) float64 { return math.Round(v*n) / n }
	return map[string]any{"solar_savings": round(a.Saved, 100), "grid_cost": round(a.GridCost, 100), "net_savings": round(a.Saved-a.GridCost, 100), "output_kwh": round(a.Output, 1000), "solar_kwh": round(a.Solar, 1000), "grid_kwh": round(a.Grid, 1000), "currency": currency}
}
func Savings(rows []map[string]any, p Plan, zone *time.Location) map[string]any {
	var a Accumulator
	for _, r := range rows {
		grid := max(0, energy.Number(r["input_wh"])-energy.Number(r["solar_wh"]))
		if r["ac_input_wh"] != nil {
			grid = energy.Number(r["ac_input_wh"])
		}
		a.Add(energy.Number(r["output_wh"]), energy.Number(r["solar_wh"]), grid, RateAt(p, time.Unix(int64(energy.Number(r["ts"])), 0).In(zone)))
	}
	return a.Result(p.Currency)
}

// Presets are the legacy April 2026 examples, not live utility tariffs.
//
//go:embed presets.json
var Presets json.RawMessage

func (s *Slot) UnmarshalJSON(raw []byte) error {
	type plain Slot
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, key := range []string{"start_hour", "end_hour", "rate"} {
		if len(fields[key]) == 0 || string(fields[key]) == "null" {
			return errors.New("time-of-use slots require start_hour, end_hour and rate")
		}
	}
	return json.Unmarshal(raw, (*plain)(s))
}
