package automation

import (
	"fmt"
	"math"
	"time"
)

type ForecastHour struct {
	TS       int64    `json:"ts"`
	SolarW   float64  `json:"solar_w"`
	LoadW    float64  `json:"load_w"`
	SOC      float64  `json:"predicted_soc"`
	Duration *float64 `json:"duration_h"`
}
type ChargeForecast struct {
	Hours      []ForecastHour `json:"forecast"`
	Efficiency float64        `json:"charge_efficiency"`
}
type SmartConfig struct {
	Mode       string  `json:"mode"`
	Target     float64 `json:"target_sunrise_soc_pct"`
	MaxChargeW float64 `json:"max_charge_w"`
}
type ChargePlan struct {
	Action       string   `json:"action"`
	Reason       string   `json:"reason"`
	Mode         string   `json:"mode"`
	DecidedAt    int64    `json:"decided_at"`
	CurrentSOC   *float64 `json:"current_soc_pct"`
	Predicted    *float64 `json:"predicted_sunrise_soc_pct"`
	Baseline     *float64 `json:"baseline_predicted_sunrise_soc_pct"`
	Target       float64  `json:"target_sunrise_soc_pct"`
	DeficitKWH   float64  `json:"deficit_kwh"`
	WindowStart  *int64   `json:"window_start"`
	WindowEnd    *int64   `json:"window_end"`
	Sunrise      *int64   `json:"sunrise_ts"`
	CheapestRate *float64 `json:"cheapest_rate"`
	PlannedHours []int64  `json:"planned_hours"`
	Extension    bool     `json:"extension_active"`
}

func NextSunrise(hours []ForecastHour) *int64 {
	dark := false
	for _, h := range hours {
		if h.SolarW > 0 && dark {
			ts := h.TS
			if h.Duration != nil {
				ts -= 3600
			}
			return &ts
		}
		if h.SolarW <= 0 {
			dark = true
		}
	}
	return nil
}
func sunriseSOC(hours []ForecastHour, sunrise int64) *float64 {
	for i, h := range hours {
		ts := h.TS
		if h.Duration != nil {
			ts -= 3600
		}
		if ts == sunrise && i > 0 {
			v := hours[i-1].SOC
			return &v
		}
	}
	return nil
}
func SmartPlan(cfg SmartConfig, soc *float64, fc ChargeForecast, baseline *ChargeForecast, cost CostPlan, capacity float64, offset int, now int64, unavailable string) ChargePlan {
	if cfg.Mode == "" {
		cfg.Mode = "off"
	}
	if cfg.Target == 0 {
		cfg.Target = 25
	}
	if cfg.MaxChargeW == 0 {
		cfg.MaxChargeW = 800
	}
	p := ChargePlan{Action: "off", Mode: cfg.Mode, DecidedAt: now, CurrentSOC: soc, Target: cfg.Target}
	if cfg.Mode == "off" {
		p.Action = "skip"
		p.Reason = "smart-charge disabled (mode=off)"
		return p
	}
	if len(fc.Hours) == 0 {
		p.Reason = unavailable
		if p.Reason == "" {
			p.Reason = "no forecast yet — set location first"
		}
		return p
	}
	sun := NextSunrise(fc.Hours)
	if sun == nil {
		p.Reason = "no upcoming sunrise in forecast"
		return p
	}
	pred := sunriseSOC(fc.Hours, *sun)
	if pred == nil {
		p.Reason = "predicted sunrise SOC unavailable"
		p.Sunrise = sun
		return p
	}
	bfc := fc
	if baseline != nil {
		bfc = *baseline
	}
	bpred := sunriseSOC(bfc.Hours, *sun)
	if bpred == nil {
		bpred = pred
	}
	p.Predicted = pred
	p.Baseline = bpred
	p.Sunrise = sun
	if *bpred >= cfg.Target {
		p.Reason = fmt.Sprintf("baseline sunrise SOC %.0f%% ≥ target %.0f%%; AC not needed", *bpred, cfg.Target)
		return p
	}
	deficit := cfg.Target - *bpred
	p.DeficitKWH = math.RoundToEven(deficit/100*capacity) / 1000
	margin := *sun - 3600
	loads, n := 0.0, 0.0
	for _, h := range bfc.Hours {
		if h.TS >= now && h.TS < margin {
			loads += h.LoadW
			n++
		}
	}
	avg := 300.0
	if n > 0 {
		avg = loads / n
	}
	eff := bfc.Efficiency
	if eff == 0 {
		eff = .9
	}
	gain := cfg.MaxChargeW
	if gain > avg {
		gain = (gain-avg)*eff + avg
	}
	gain = math.Max(100, gain)
	needed := int(math.Max(1, math.Ceil(p.DeficitKWH*1000/gain)))
	candidates := []Candidate{}
	for ts := now / 3600 * 3600; ts < margin; ts += 3600 {
		candidates = append(candidates, Candidate{ts, cost.RateAt(ts, offset)})
	}
	current := 0.0
	if soc != nil {
		current = *soc
	}
	p.CurrentSOC = &current
	p.Extension = now >= margin && (now < *sun || current < cfg.Target)
	if len(candidates) == 0 && !p.Extension {
		p.Reason = "not enough time before sunrise"
		p.PlannedHours = []int64{}
		return p
	}
	var mandatory *int64
	if margin > now {
		v := margin - 3600
		mandatory = &v
	}
	p.PlannedHours = PickPlannedHours(candidates, needed, mandatory)
	inPlanned := false
	for _, h := range p.PlannedHours {
		r := cost.RateAt(h, offset)
		if p.CheapestRate == nil || r > *p.CheapestRate {
			v := r
			p.CheapestRate = &v
		}
		if h == now/3600*3600 {
			inPlanned = true
		}
	}
	if len(p.PlannedHours) > 0 {
		a, b := p.PlannedHours[0], p.PlannedHours[len(p.PlannedHours)-1]+3600
		p.WindowStart = &a
		p.WindowEnd = &b
	}
	if p.Extension {
		p.Action = "on"
		if now < *sun {
			p.Reason = fmt.Sprintf("extension: baseline sunrise SOC %.1f%% < target %.0f%% — topping up before sunrise", *bpred, cfg.Target)
		} else {
			p.Reason = fmt.Sprintf("extension: SOC %.1f%% < target %.0f%% — charging until target hit", current, cfg.Target)
		}
	} else if inPlanned {
		p.Action = "on"
		p.Reason = fmt.Sprintf("charging now: deficit %.0fpp (%.1f kWh), in cheap window @ $%.2f/kWh", deficit, p.DeficitKWH, *p.CheapestRate)
	} else {
		if current >= cfg.Target {
			if len(p.PlannedHours) > 0 {
				why := "near sunrise to minimize drain"
				if cost.RateAt(now/3600*3600, offset) > *p.CheapestRate+1e-6 {
					why = fmt.Sprintf("cheaper window @ $%.2f/kWh", *p.CheapestRate)
				}
				at := time.Unix(p.PlannedHours[0], 0).In(time.FixedZone("device", offset)).Format("15:04")
				p.Reason = fmt.Sprintf("deferred: SOC %.0f%% ≥ target now, sunrise %.0f%% < target %.0f%% — charging at %s (%s)", current, *bpred, cfg.Target, at, why)
			} else {
				p.Reason = fmt.Sprintf("SOC %.0f%% ≥ target %.0f%%; coasting", current, cfg.Target)
			}
		} else {
			p.Reason = fmt.Sprintf("waiting for next planned hour (deficit %.0fpp, %d planned hour(s))", deficit, len(p.PlannedHours))
		}
	}
	return p
}
