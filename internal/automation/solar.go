package automation

import "fmt"

type SolarInput struct {
	Config     map[string]any `json:"config"`
	SOC        *float64       `json:"current_soc_pct"`
	Solar      *float64       `json:"solar_w"`
	Load       *float64       `json:"load_w"`
	ACInput    *float64       `json:"ac_input_w"`
	Age        float64        `json:"telemetry_age_s"`
	Target     float64        `json:"target_sunrise_soc_pct"`
	Predicted  *float64       `json:"predicted_sunrise_soc_with_diversion"`
	Baseline   *float64       `json:"predicted_sunrise_soc_baseline"`
	Capacity   *float64       `json:"capacity_wh"`
	Hours      *float64       `json:"hours_to_sunrise"`
	Household  *float64       `json:"household_load_w"`
	MinSOC     *float64       `json:"predicted_min_soc_pct"`
	BalanceDue bool           `json:"balance_due"`
	Now        float64        `json:"now_ts"`
}

func configNumber(m map[string]any, k string, fallback float64) float64 {
	if v, ok := m[k].(float64); ok && v != 0 {
		return v
	}
	return fallback
}
func SolarPlan(in SolarInput) ChargePlan {
	mode, _ := in.Config["mode"].(string)
	if mode == "" {
		mode = "off"
	}
	p := ChargePlan{Action: "off", Mode: mode, DecidedAt: int64(in.Now), CurrentSOC: in.SOC, Predicted: in.Predicted, Baseline: in.Baseline, Target: in.Target}
	low := configNumber(in.Config, "comfort_low_pct", 30)
	high := configNumber(in.Config, "comfort_high_pct", 50)
	safety := configNumber(in.Config, "safety_margin_pp", 5)
	hyst := configNumber(in.Config, "on_hysteresis_pp", 3)
	floor := in.Target + safety
	switch {
	case mode == "off":
		p.Action = "skip"
		p.Reason = "mode=off"
	case in.Age > 90:
		p.Reason = fmt.Sprintf("stale telemetry (%.0fs old)", in.Age)
	case in.SOC == nil:
		p.Reason = "missing telemetry (soc)"
	case in.ACInput != nil && *in.ACInput > 50:
		p.Reason = "grid charging active; yielding to avoid inverter overdraw"
	case in.BalanceDue:
		p.Reason = "pack-balance window; diversion held OFF"
	case in.Predicted == nil:
		p.Reason = "forecast unavailable"
	case *in.SOC <= low:
		p.Reason = "SOC at comfort_low"
	case in.Capacity != nil && *in.Capacity > 0 && in.Hours != nil && *in.Hours != 0 && in.Household != nil && in.Solar != nil && *in.Solar <= *in.Household && *in.SOC - *in.Household**in.Hours / *in.Capacity * 100 < floor:
		p.Reason = "overnight-reserve guard"
	case in.MinSOC != nil && *in.MinSOC < low:
		p.Reason = "cloudy-tomorrow guard"
	case in.MinSOC != nil && *in.MinSOC < low+hyst:
		p.Action = "skip"
		p.Reason = "in cloudy-tomorrow hysteresis band"
	case *in.Predicted < floor:
		p.Reason = "predicted sunrise below target+margin"
	case *in.Predicted >= floor+hyst && *in.SOC >= high:
		p.Action = "on"
		p.Reason = "battery has headroom to divert"
	default:
		p.Action = "skip"
		p.Reason = "holding current plug state"
	}
	return p
}
func GateMinHold(p ChargePlan, last, hold float64, before string, now float64) ChargePlan {
	if p.Action == "skip" {
		return p
	}
	desired := p.Action
	if desired == before {
		p.Action = "skip"
		p.Reason = "already " + before + "; " + p.Reason
	} else if now-last < hold {
		p.Action = "skip"
		p.Reason = fmt.Sprintf("min_hold not elapsed (%.0fs < %.0fs) — would have gone %s: %s", now-last, hold, desired, p.Reason)
	}
	return p
}
