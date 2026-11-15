package forecast

import (
	"math"
	"sort"
	"time"
)

func trimmedMedian(v []float64) float64 {
	sort.Float64s(v)
	n := len(v)
	if n == 0 {
		return 0
	}
	if n < 5 {
		return v[n/2]
	}
	trim := max(1, int(float64(n)*.1))
	if n-2*trim >= 1 {
		v = v[trim : n-trim]
	}
	return v[len(v)/2]
}
func FitLoadProfile(rows []Row, now int64, carLoad float64, bucket int) map[LoadKey]float64 {
	net := func(r Row) float64 {
		out := Number(r, "output_w")
		if r["solar_charge_plug_on_frac"] != nil && carLoad != 0 {
			return math.Max(0, out-Number(r, "solar_charge_plug_on_frac")*carLoad)
		}
		if d := Number(r, "solar_charge_diverted_wh"); d > 0 {
			return math.Max(0, out-d/(float64(max(1, bucket))/3600))
		}
		return out
	}
	vals := []float64{}
	for _, r := range rows {
		if r["output_w"] != nil {
			vals = append(vals, net(r))
		}
	}
	profile := map[LoadKey]float64{}
	if len(vals) == 0 {
		return profile
	}
	sort.Float64s(vals)
	cap := vals[min(len(vals)-1, int(float64(len(vals))*.95))]
	ceiling := math.Max(180, cap)
	type sample struct {
		v float64
		t int64
	}
	buckets := map[LoadKey][]sample{}
	for _, r := range rows {
		if r["ts"] == nil || r["output_w"] == nil {
			continue
		}
		ts := int64(Number(r, "ts"))
		t := time.Unix(ts, 0).In(time.Local)
		w := 0
		if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
			w = 1
		}
		key := LoadKey{t.Hour(), w}
		buckets[key] = append(buckets[key], sample{math.Min(net(r), cap), ts})
	}
	for k, samples := range buckets {
		vals := []float64{}
		for _, s := range samples {
			vals = append(vals, s.v)
		}
		sort.Float64s(vals)
		n := len(vals)
		med := trimmedMedian(append([]float64(nil), vals...))
		relative := 0.0
		if med > 0 {
			relative = (vals[3*n/4] - vals[n/4]) / med
		}
		if relative < .5 || n < 6 {
			profile[k] = math.Min(med, ceiling)
			continue
		}
		recent, old := []float64{}, []float64{}
		for _, s := range samples {
			if s.t >= now-3*86400 {
				recent = append(recent, s.v)
			} else {
				old = append(old, s.v)
			}
		}
		if len(recent) == 0 {
			profile[k] = math.Min(med, ceiling)
			continue
		}
		rm := trimmedMedian(recent)
		om := rm
		if len(old) > 0 {
			om = trimmedMedian(old)
		}
		profile[k] = math.Min(.7*rm+.3*om, ceiling)
	}
	return profile
}
func FitChargeEfficiency(rows []Row, capacity, defaultValue float64, minWindows int) (float64, int) {
	rows = append([]Row(nil), rows...)
	sort.SliceStable(rows, func(i, j int) bool { return Number(rows[i], "ts") < Number(rows[j], "ts") })
	effs := []float64{}
	for i := 1; i < len(rows); i++ {
		a, b := rows[i-1], rows[i]
		sa, sb := RowSOC(a), RowSOC(b)
		if sa == nil || sb == nil {
			continue
		}
		minGain := 1.0
		if a["system_soc"] != nil && b["system_soc"] != nil {
			minGain = .25
		}
		gain := *sb - *sa
		net := math.Max(0, Number(a, "input_wh")-Number(a, "output_wh"))
		dt := (Number(b, "ts") - Number(a, "ts")) / 3600
		if gain < minGain || *sa > 95 || *sb > 99 || net < 100 || dt <= 0 || dt > 6 {
			continue
		}
		effs = append(effs, gain*capacity/100/net)
	}
	if len(effs) < minWindows {
		return defaultValue, len(effs)
	}
	sort.Float64s(effs)
	v := effs[len(effs)/2]
	if v < .5 || v > .99 {
		return defaultValue, len(effs)
	}
	return v, len(effs)
}
func FitChargeCeiling(rows []Row, capacity float64, offset, minDays int, minSurplus float64) *float64 {
	if capacity <= 0 {
		return nil
	}
	maxSOC, solar, output := map[int64]float64{}, map[int64]float64{}, map[int64]float64{}
	for _, r := range rows {
		ts := int64(Number(r, "ts"))
		if ts <= 0 {
			continue
		}
		day := (ts + int64(offset)) / 86400
		if soc := RowSOC(r); soc != nil {
			old, ok := maxSOC[day]
			if !ok || *soc > old {
				maxSOC[day] = *soc
			}
		}
		solar[day] += Number(r, "solar_wh")
		output[day] += Number(r, "output_wh")
	}
	vals := []float64{}
	for d, v := range maxSOC {
		if solar[d]-output[d] > minSurplus*capacity {
			vals = append(vals, v)
		}
	}
	if len(vals) < minDays || len(vals) == 0 {
		return nil
	}
	sort.Float64s(vals)
	v := vals[min(len(vals)-1, int(float64(len(vals))*.9))]
	if v >= 97 {
		return nil
	}
	return &v
}
