package automation

import (
	"fmt"
	"strings"
)

type SolarInput struct {
	Config        map[string]any `json:"config"`
	SOC           *float64       `json:"current_soc_pct"`
	Solar         *float64       `json:"solar_w"`
	Load          *float64       `json:"load_w"`
	ACInput       *float64       `json:"ac_input_w"`
	Age           float64        `json:"telemetry_age_s"`
	Target        float64        `json:"target_sunrise_soc_pct"`
	Predicted     *float64       `json:"predicted_sunrise_soc_with_diversion"`
	Baseline      *float64       `json:"predicted_sunrise_soc_baseline"`
	Capacity      *float64       `json:"capacity_wh"`
	Hours         *float64       `json:"hours_to_sunrise"`
	Household     *float64       `json:"household_load_w"`
	MinSOC        *float64       `json:"predicted_min_soc_pct"`
	BalanceDue    bool           `json:"balance_due"`
	Horizon       float64        `json:"guard_horizon_h"`
	DaysSinceFull *float64       `json:"days_since_full"`
	Spread        *float64       `json:"pack_spread_pp"`
	Stalled       *float64       `json:"balance_stalled_days"`
	Now           float64        `json:"now_ts"`
}

func configNumber(m map[string]any, k string, fallback float64) float64 {
	if v, ok := m[k].(float64); ok && v != 0 {
		return v
	}
	return fallback
}

type SolarChargePlan struct {
	Action     string   `json:"action"`
	Reason     string   `json:"reason"`
	Mode       string   `json:"mode"`
	DecidedAt  int64    `json:"decided_at"`
	CurrentSOC *float64 `json:"current_soc_pct"`
	Predicted  *float64 `json:"predicted_sunrise_soc_pct"`
	Baseline   *float64 `json:"baseline_predicted_sunrise_soc_pct"`
	Target     float64  `json:"target_sunrise_soc_pct"`
	Solar      *float64 `json:"solar_w"`
	Load       *float64 `json:"load_w"`
	Surplus    *float64 `json:"surplus_w"`
	CarLoad    float64  `json:"car_load_w"`
	Before     *string  `json:"plug_state_before"`
	MinSOC     *float64 `json:"predicted_min_soc_pct"`
}

func SolarPlan(in SolarInput) (p SolarChargePlan) {
	mode, _ := in.Config["mode"].(string)
	mode = strings.ToLower(mode)
	if mode == "" {
		mode = "off"
	}
	p = SolarChargePlan{Action: "off", Mode: mode, DecidedAt: int64(in.Now), CurrentSOC: in.SOC, Predicted: in.Predicted, Baseline: in.Baseline, Target: in.Target}
	p.Solar, p.Load, p.MinSOC = in.Solar, in.Load, in.MinSOC
	p.CarLoad = configNumber(in.Config, "car_load_w", 1400)
	if in.Solar != nil && in.Load != nil {
		v := *in.Solar - *in.Load
		p.Surplus = &v
	}
	target := configNumber(in.Config, "balance_target_main_pct", 100)
	defer func() {
		if in.Stalled != nil {
			p.Reason += fmt.Sprintf(" | pack-balance STALLED %.0fd without reaching %.0f%% — check the packs", *in.Stalled, target)
		}
	}()
	horizon := in.Horizon
	if horizon == 0 {
		horizon = 36
	}
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
		p.Reason = fmt.Sprintf("grid charging active (%.0fW AC input ≥ 50W); yielding to avoid inverter overdraw", *in.ACInput)
	case in.BalanceDue:
		since := "no full charge on record"
		if in.DaysSinceFull != nil {
			since = fmt.Sprintf("%.0fd since last full charge", *in.DaysSinceFull)
		}
		why := fmt.Sprintf("%s (interval %.0fd)", since, configNumber(in.Config, "balance_every_days", 30))
		if SpreadTriggerFired(in.Config, in.Spread, in.DaysSinceFull) {
			why = fmt.Sprintf("pack spread %.0fpp ≥ %.0fpp trigger (%s)", *in.Spread, configNumber(in.Config, "balance_spread_trigger_pp", 0), since)
		}
		p.Reason = fmt.Sprintf("pack-balance window: %s — diversion held OFF so surplus drives main to %.0f%% and the BMS can balance the packs", why, target)
	case in.Predicted == nil:
		p.Reason = "forecast unavailable"
	case *in.SOC <= low:
		p.Reason = fmt.Sprintf("SOC %.0f%% ≤ comfort_low %.0f%%", *in.SOC, low)
	case in.Capacity != nil && *in.Capacity > 0 && in.Hours != nil && *in.Hours != 0 && in.Household != nil && in.Solar != nil && *in.Solar <= *in.Household && *in.SOC - *in.Household**in.Hours / *in.Capacity * 100 < floor:
		p.Reason = fmt.Sprintf("overnight-reserve guard: no solar surplus and coasting %.0fW house load for %.1fh would reach %.0f%% by sunrise < target%.0f%%+margin = %.0f%%; diverting would eat the overnight reserve", *in.Household, *in.Hours, *in.SOC - *in.Household**in.Hours / *in.Capacity * 100, in.Target, floor)
	case in.MinSOC != nil && *in.MinSOC < low:
		p.Reason = fmt.Sprintf("cloudy-tomorrow guard: predicted %.1f%% trough within %.0fh < comfort_low %.0f%%; reserving for the next day's recharge", *in.MinSOC, horizon, low)
	case in.MinSOC != nil && *in.MinSOC < low+hyst:
		p.Action = "skip"
		p.Reason = fmt.Sprintf("in cloudy-tomorrow hysteresis band: predicted %.1f%% trough within %.0fh in [%.0f%%, %.0f%%); holding state", *in.MinSOC, horizon, low, low+hyst)
	case *in.Predicted < floor:
		p.Reason = fmt.Sprintf("predicted sunrise %.1f%% < target %.0f%%+margin%.0f = %.0f%%; diversion would risk overnight floor", *in.Predicted, in.Target, safety, floor)
	case *in.Predicted >= floor+hyst && *in.SOC >= high:
		p.Action = "on"
		p.Reason = fmt.Sprintf("predicted sunrise %.1f%% ≥ target %.0f%%+margin%.0f+hyst%.0f = %.0f%% AND SOC %.0f%% ≥ comfort_high %.0f%%; battery has headroom to divert", *in.Predicted, in.Target, safety, hyst, floor+hyst, *in.SOC, high)
	case *in.SOC < high:
		p.Action = "skip"
		p.Reason = fmt.Sprintf("SOC %.0f%% < comfort_high %.0f%%; waiting for battery to recover before starting a new session (if already on, holding through forecast band)", *in.SOC, high)
	default:
		p.Action = "skip"
		p.Reason = fmt.Sprintf("in forecast hysteresis band: predicted sunrise %.1f%% in [%.0f%%, %.0f%%); holding current plug state", *in.Predicted, floor, floor+hyst)
	}
	return p
}
func GateMinHold(p SolarChargePlan, last, hold float64, before string, now float64) SolarChargePlan {
	p.Before = &before
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

func SpreadTriggerFired(config map[string]any, spread, days *float64) bool {
	trigger, _ := config["balance_spread_trigger_pp"].(float64)
	return int(trigger) > 0 && spread != nil && *spread >= float64(int(trigger)) && (days == nil || *days >= 1)
}

// BalanceWindowStatus bounds a balancing attempt and grants another attempt
// only after the configured stand-down interval.
func BalanceWindowStatus(config map[string]any, opened *float64, now float64) (float64, float64, bool) {
	start := now
	if opened != nil {
		start = *opened
	}
	days := (now - start) / 86400
	if days < 0 {
		days = 0
	}
	maxDays, _ := config["balance_max_window_days"].(float64)
	maxDays = float64(int(maxDays))
	if maxDays <= 0 {
		return start, days, false
	}
	interval, _ := config["balance_every_days"].(float64)
	interval = float64(int(interval))
	if interval <= 0 {
		interval = maxDays
	}
	if days >= maxDays+interval {
		return now, 0, false
	}
	return start, days, days >= maxDays
}

func GateLoadCeiling(p SolarChargePlan, load *float64, before string, ceiling float64) SolarChargePlan {
	if p.Action != "on" || before == "on" || load == nil || *load < ceiling {
		return p
	}
	p.Action = "skip"
	p.Reason = fmt.Sprintf("system load %.0fW ≥ ceiling %.0fW — engaging diversion (~%.0fW) would risk pushing total toward the inverter-protect threshold; deferring: %s", *load, ceiling, p.CarLoad, p.Reason)
	return p
}

func GateInverterProtect(p SolarChargePlan, last, cooldown, now float64) SolarChargePlan {
	if p.Action != "on" || last == 0 || now-last >= cooldown {
		return p
	}
	p.Action = "skip"
	p.Reason = fmt.Sprintf("inverter-protect cooldown active (%.0f min remaining of %.0f min) — load exceeded threshold %.0f min ago; holding plug OFF: %s", (cooldown-(now-last))/60, cooldown/60, (now-last)/60, p.Reason)
	return p
}
