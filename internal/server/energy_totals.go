package server

import (
	"encoding/json"
	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/store"
	"time"
)

func (s *Server) energyTotals(sn string, now float64, offset int) (map[string]any, error) {
	out := map[string]any{"device_sn": sn}
	planValues, err := s.Store.Get("settings", "cost_plan")
	if isMissing(err) {
		planValues = map[string]any{"type": "flat", "rate_per_kwh": .30, "currency": "USD"}
	} else if err != nil {
		return nil, err
	}
	var plan automation.CostPlan
	data, _ := json.Marshal(planValues)
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, err
	}
	out["cost_plan"] = planValues
	loc := s.energyLocation()
	localNow := time.Unix(int64(now), 0).In(loc)
	today := float64(time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc).Unix())
	for window, since := range map[string]float64{"lifetime": 0, "today": today, "last_7d": now - 7*86400, "last_30d": now - 30*86400} {
		summary, err := s.Store.EnergySummaryAtLocation(sn, since, loc)
		if err != nil {
			return nil, err
		}
		totals := store.EnergyTotals(summary, now, offset)["lifetime"].(map[string]any)
		if window != "lifetime" {
			totals["since_ts"] = since
		}
		rows := []automation.Energy{}
		for _, m := range summary {
			n := func(k string) float64 { v, _ := m[k].(float64); return v }
			ac := n("ac_input_wh")
			rowOffset := int(n("utc_offset_s"))
			rows = append(rows, automation.Energy{TS: int64(n("ts")), InputWH: n("input_wh"), OutputWH: n("output_wh"), SolarWH: n("solar_wh"), ACInputWH: &ac, UTCOffsetSeconds: &rowOffset})
		}
		savings := automation.Savings(rows, plan, offset)
		totals["savings"] = savings
		out[window] = totals
		out[window+"_savings"] = savings
	}
	return out, nil
}
