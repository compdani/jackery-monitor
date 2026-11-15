package forecast

import (
	"math"
	"sort"
)

func sortedHistory(rows []Row) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if r["ts"] != nil {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return Number(out[i], "ts") < Number(out[j], "ts") })
	return out
}
func upperMedian(v []float64) float64   { sort.Float64s(v); return v[len(v)/2] }
func PerPackBaseline(count int) float64 { return float64(max(0, count)) * 60 }

// FitInverterOverhead retains signed samples until the final plausibility gate.
func FitInverterOverhead(history []Row, capacity float64) (float64, int) {
	rows := sortedHistory(history)
	var values []float64
	for i := 1; i < len(rows); i++ {
		a, b := rows[i-1], rows[i]
		sa, sb := RowSOC(a), RowSOC(b)
		if sa == nil || sb == nil {
			continue
		}
		minDrop := 2.0
		if a["system_soc"] != nil && b["system_soc"] != nil {
			minDrop = .5
		}
		dt := (Number(b, "ts") - Number(a, "ts")) / 3600
		if *sa-*sb < minDrop || Number(a, "solar_wh") > 50 || Number(a, "ac_input_wh") > 50 || dt < .5 || dt > 6 {
			continue
		}
		load := Number(a, "output_wh") / dt
		if load < 50 {
			continue
		}
		values = append(values, ((*sa-*sb)*capacity/100/dt-load)/load)
	}
	n := len(values)
	if n < 5 {
		return .1, n
	}
	v := upperMedian(values)
	if v < 0 || v > .5 {
		v = .1
	}
	return v, n
}

type dischargeRun struct{ load, drain, hours float64 }

func cleanDischargeRuns(rows []Row, capacity float64, requireSystem bool) []dischargeRun {
	var runs [][]Row
	var current []Row
	for _, r := range rows {
		clean := Number(r, "solar_wh") <= 20 && Number(r, "ac_input_wh") <= 20 && RowSOC(r) != nil && (!requireSystem || r["system_soc"] != nil)
		gap := len(current) > 0 && (Number(r, "ts")-Number(current[len(current)-1], "ts"))/3600 > 1.5
		if !clean || gap {
			if len(current) > 0 {
				runs = append(runs, current)
			}
			current = nil
		}
		if clean {
			current = append(current, r)
		}
	}
	if len(current) > 0 {
		runs = append(runs, current)
	}
	var out []dischargeRun
	for _, run := range runs {
		if len(run) < 2 {
			continue
		}
		a, b := run[0], run[len(run)-1]
		dt := (Number(b, "ts") - Number(a, "ts")) / 3600
		sa, sb := RowSOC(a), RowSOC(b)
		minDrop := 3.0
		if a["system_soc"] != nil && b["system_soc"] != nil {
			minDrop = .5
		}
		if dt < 2 || dt > 12 || *sa < 85 || *sa-*sb < minDrop {
			continue
		}
		load := 0.0
		for _, r := range run[:len(run)-1] {
			load += Number(r, "output_wh")
		}
		load /= dt
		if load >= 50 {
			out = append(out, dischargeRun{load, (*sa - *sb) * capacity / 100 / dt, dt})
		}
	}
	return out
}

// FitDrainModel fits main-unit parasitic draw and throughput losses separately
// from the fixed expansion-pack baseline. It never fits main-only SOC against
// system capacity when any system SOC history is available.
func FitDrainModel(history []Row, capacity float64, packs int) (float64, float64, int) {
	rows := sortedHistory(history)
	requireSystem := false
	for _, r := range rows {
		requireSystem = requireSystem || r["system_soc"] != nil
	}
	baseline := PerPackBaseline(packs)
	type pair struct{ x, y float64 }
	var pairs []pair
	var loads []float64
	for i := 1; i < len(rows); i++ {
		a, b := rows[i-1], rows[i]
		if requireSystem && (a["system_soc"] == nil || b["system_soc"] == nil) {
			continue
		}
		sa, sb := RowSOC(a), RowSOC(b)
		if sa == nil || sb == nil || *sa < 85 {
			continue
		}
		minDrop := 2.0
		if a["system_soc"] != nil && b["system_soc"] != nil {
			minDrop = .5
		}
		dt := (Number(b, "ts") - Number(a, "ts")) / 3600
		if *sa-*sb < minDrop || Number(a, "solar_wh") > 20 || Number(a, "ac_input_wh") > 20 || dt < .5 || dt > 6 {
			continue
		}
		load := Number(a, "output_wh") / dt
		if load < 50 {
			continue
		}
		pairs = append(pairs, pair{load, math.Max(0, (*sa-*sb)*capacity/100/dt-baseline)})
		loads = append(loads, load)
	}
	n := len(pairs)
	if n < 5 {
		return 50, .1, n
	}
	sort.Float64s(loads)
	if loads[min(n-1, int(float64(n)*.9))]/loads[int(float64(n)*.1)] < 2 {
		runs := cleanDischargeRuns(rows, capacity, requireSystem)
		var long []dischargeRun
		for _, r := range runs {
			if r.hours >= 4 {
				long = append(long, r)
			}
		}
		if len(long) >= 2 {
			runs = long
		}
		if len(runs) >= 2 {
			var implied []float64
			for _, r := range runs {
				implied = append(implied, math.Max(0, r.drain-baseline)-r.load*1.1)
			}
			p := upperMedian(implied)
			if p <= 1000 {
				return math.Max(0, p), .1, len(runs)
			}
		}
		var implied []float64
		for _, p := range pairs {
			implied = append(implied, p.y-p.x*1.1)
		}
		p := upperMedian(implied)
		if p > 1000 {
			return 50, .1, n
		}
		return math.Max(0, p), .1, n
	}
	sx, sy, sxy, sxx := 0.0, 0.0, 0.0, 0.0
	for _, p := range pairs {
		sx += p.x
		sy += p.y
		sxy += p.x * p.y
		sxx += p.x * p.x
	}
	den := float64(n)*sxx - sx*sx
	if den <= 0 {
		return 50, .1, n
	}
	b := (float64(n)*sxy - sx*sy) / den
	a := (sy - b*sx) / float64(n)
	if a < 0 || a > 1000 || b < 1 || b > 1.5 {
		return 50, .1, n
	}
	return a, b - 1, n
}

func Readiness(history []Row, capacity float64) Row {
	rows := sortedHistory(history)
	n := 0
	span := 0.0
	reason := "no_history"
	ready := false
	if len(rows) >= 2 {
		span = (Number(rows[len(rows)-1], "ts") - Number(rows[0], "ts")) / 3600
		_, n = FitInverterOverhead(history, capacity)
		ready = span >= 24
		reason = "calibrating"
		if ready {
			reason = "ready"
		}
	}
	return Row{"ready": ready, "reason": reason, "have_hours": round(span, 1), "needed_hours": 24, "have_idle_windows": n, "needed_idle_windows": 5, "low_confidence_overhead_fit": n < 5}
}
