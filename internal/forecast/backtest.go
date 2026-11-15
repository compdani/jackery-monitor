package forecast

import (
	"encoding/json"
	"github.com/compdani/jackery-monitor/internal/automation"
	"sort"
)

type ReplayOptions struct {
	Capacity, MaxChargeW float64
	Cost                 automation.CostPlan
	Offset               int
	TargetOverride       *float64
}

// ReplayDecisions never exposes post-decision energy history to a fitted model.
// Weather observations remain an approximate counterfactual, as in Python.
func ReplayDecisions(decisions, history, weather []Row, o ReplayOptions) []Row {
	decisions = append([]Row(nil), decisions...)
	sort.SliceStable(decisions, func(i, j int) bool { return Number(decisions[i], "decided_at") < Number(decisions[j], "decided_at") })
	out := []Row{}
	for _, d := range decisions {
		ts := int64(Number(d, "decided_at"))
		if ts == 0 || Number(d, "sunrise_ts") == 0 || d["current_soc_pct"] == nil {
			continue
		}
		target := Number(d, "target_sunrise_soc_pct")
		if target == 0 {
			target = 25
		}
		if o.TargetOverride != nil {
			target = *o.TargetOverride
		}
		var h, w []Row
		for _, r := range history {
			if r["ts"] != nil && Number(r, "ts") <= float64(ts) {
				h = append(h, r)
			}
		}
		for _, r := range weather {
			t := Number(r, "ts")
			if r["ts"] != nil && t >= float64(ts-14*86400) && t <= float64(ts+72*3600) {
				w = append(w, r)
			}
		}
		h = sortedHistory(h)
		w = sortedHistory(w)
		soc := Number(d, "current_soc_pct")
		// Pass the historical decision time explicitly; the reference's implicit
		// wall clock would otherwise filter every historical weather row away.
		opts := BuildOptions{Now: ts, Simulation: Options{ACFloor: &target}}
		fc := Build(h, w, soc, o.Capacity, opts)
		opts.Simulation.ACFloor = nil
		base := Build(h, w, soc, o.Capacity, opts)
		decode := func(r Row) automation.ChargeForecast {
			b, _ := json.Marshal(r)
			var f automation.ChargeForecast
			_ = json.Unmarshal(b, &f)
			return f
		}
		baseline := decode(base)
		plan := automation.SmartPlan(automation.SmartConfig{Mode: "active", Target: target, MaxChargeW: o.MaxChargeW}, &soc, decode(fc), &baseline, o.Cost, o.Capacity, o.Offset, ts, "")
		if plan.PlannedHours == nil {
			plan.PlannedHours = []int64{}
		}
		out = append(out, Row{"ts": ts, "old_action": d["action"], "new_action": plan.Action, "would_flip": plan.Action != d["action"], "old_reason": d["reason"], "new_reason": plan.Reason, "soc": soc, "old_predicted_sunrise": d["predicted_sunrise_soc_pct"], "new_predicted_sunrise": plan.Predicted, "new_baseline_sunrise": plan.Baseline, "target": target, "planned_hours": plan.PlannedHours, "extension_active": plan.Extension, "deficit_kwh": plan.DeficitKWH, "sunrise_ts": plan.Sunrise})
	}
	return out
}
func SummarizeReplay(rows []Row) Row {
	old, newActions, pairs := map[string]int{}, map[string]int{}, map[string]int{}
	flips, extensions, errs := 0, 0, 0
	for _, r := range rows {
		a, _ := r["old_action"].(string)
		b, _ := r["new_action"].(string)
		if a == "" {
			a = "?"
		}
		if b == "" {
			b = "?"
		}
		old[a]++
		newActions[b]++
		if r["error"] != nil && r["error"] != "" {
			errs++
			continue
		}
		if r["would_flip"] == true {
			flips++
			pairs[a+"→"+b]++
		}
		if r["extension_active"] == true {
			extensions++
		}
	}
	return Row{"n": len(rows), "n_flipped": flips, "n_extension": extensions, "n_error": errs, "by_action_old": old, "by_action_new": newActions, "flip_pairs": pairs}
}
