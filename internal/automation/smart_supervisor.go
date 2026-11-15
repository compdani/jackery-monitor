package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// SmartSettings is the persisted, per-device Python configuration contract.
type SmartSettings struct {
	SmartConfig
	Host         string `json:"kasa_device_host"`
	MaxOnMinutes int    `json:"max_on_duration_minutes"`
	Narrate      bool   `json:"claude_enabled"`
}

func NormalizeSmart(raw map[string]any) SmartSettings {
	c := SmartSettings{SmartConfig: SmartConfig{Mode: "off", Target: 25, MaxChargeW: 800}, MaxOnMinutes: 480}
	mode := strings.ToLower(fmt.Sprint(raw["mode"]))
	if mode == "off" || mode == "test" || mode == "active" {
		c.Mode = mode
	}
	if host, ok := raw["kasa_device_host"].(string); ok {
		c.Host = string([]rune(host)[:min(128, len([]rune(host)))])
	}
	integer := func(key string, fallback, lo, hi int) int {
		n, err := strconv.ParseFloat(fmt.Sprint(raw[key]), 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < float64(lo) || n > float64(hi)+.999999 {
			return fallback
		}
		return int(n)
	}
	c.Target = float64(integer("target_sunrise_soc_pct", 25, 5, 95))
	c.MaxChargeW = float64(integer("max_charge_w", 800, 50, 5000))
	c.MaxOnMinutes = integer("max_on_duration_minutes", 480, 30, 1440)
	c.Narrate, _ = raw["claude_enabled"].(bool)
	return c
}

// SmartState must be durably saved with each decision. LastAction describes
// successful commands only; a failed switch never advances the execution state.
type SmartState struct {
	LastAction string      `json:"last_action"`
	OnSince    int64       `json:"on_since"`
	LastError  string      `json:"last_error"`
	LastPlan   *ChargePlan `json:"last_plan"`
}

type SmartDecision struct {
	ChargePlan
	Device    string `json:"device_sn"`
	Executed  bool   `json:"executed"`
	Narration string `json:"narration"`
	Error     string `json:"error,omitempty"`
}

// RunSmart executes only in active mode. Callers serialize controller ownership
// and persist the returned state/decision together before permitting another tick.
// dry-run evaluation must call SmartPlan directly instead.
func RunSmart(ctx context.Context, sw Switch, cfg SmartSettings, state SmartState, device string, plan ChargePlan) (SmartState, SmartDecision) {
	d := SmartDecision{ChargePlan: plan, Device: device}
	if cfg.Mode == "active" && plan.Action == "on" && state.OnSince > 0 && plan.DecidedAt-state.OnSince >= int64(cfg.MaxOnMinutes)*60 {
		d.Action = "off"
		d.Reason = "maximum continuous charging duration reached"
	}
	if cfg.Mode == "active" && cfg.Host != "" && (d.Action == "on" || d.Action == "off") {
		var err error
		if sw == nil {
			err = fmt.Errorf("switch adapter unavailable")
		} else {
			callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = sw.SetPower(callCtx, cfg.Host, d.Action == "on")
			cancel()
		}
		if err != nil {
			d.Error = err.Error()
			state.LastError = d.Error
		} else {
			d.Executed = true
			state.LastError = ""
			if d.Action == "on" && state.LastAction != "on" {
				state.OnSince = d.DecidedAt
			}
			if d.Action == "off" {
				state.OnSince = 0
			}
			state.LastAction = d.Action
		}
	}
	state.LastPlan = &d.ChargePlan
	return state, d
}

func (d SmartDecision) Payload() map[string]any {
	b, _ := json.Marshal(d)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	out["controller"] = "smart_charge"
	out["ts"] = float64(d.DecidedAt)
	return out
}

// MarshalJSON preserves Python's null default for an unselected plug.
func (c SmartSettings) MarshalJSON() ([]byte, error) {
	type plain SmartSettings
	b, err := json.Marshal(plain(c))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err = json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if c.Host == "" {
		out["kasa_device_host"] = nil
	}
	return json.Marshal(out)
}
