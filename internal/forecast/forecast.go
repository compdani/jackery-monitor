// Package forecast ports the interval-aware Python SOC simulator and solar fit.
package forecast

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

type Row map[string]any

func Number(r map[string]any, key string) float64 {
	switch v := r[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	}
	return 0
}
func clamp(v, a, b float64) float64  { return math.Max(a, math.Min(b, v)) }
func round(v float64, n int) float64 { p := math.Pow10(n); return math.RoundToEven(v*p) / p }
func RowSOC(r Row) *float64 {
	for _, k := range []string{"system_soc", "battery_pct"} {
		if r[k] != nil {
			v := Number(r, k)
			return &v
		}
	}
	return nil
}

type Options struct {
	ACFloor, ChargeEfficiency, ExtraFloor, Ceiling *float64
	ExtraLoadW                                     float64
}

func SimulateSOC(start, capacity float64, hours []Row, o Options) []Row {
	if capacity <= 0 {
		return nil
	}
	eff := 0.9
	if o.ChargeEfficiency != nil {
		eff = *o.ChargeEfficiency
	}
	n := len(hours)
	cum := make([]float64, n+1)
	mins := make([]float64, n+1)
	duration := func(r Row) float64 {
		if r["duration_h"] == nil {
			return 1
		}
		return clamp(Number(r, "duration_h"), 0, 1)
	}
	for i, h := range hours {
		net := Number(h, "solar_w") - Number(h, "load_w")
		if net > 0 {
			net *= eff
		}
		cum[i+1] = cum[i] + net*duration(h)/capacity*100
	}
	mins[n] = cum[n]
	for i := n - 1; i >= 0; i-- {
		mins[i] = math.Min(cum[i], mins[i+1])
	}
	soc := clamp(start, 0, 100)
	out := make([]Row, 0, n)
	for i, h := range hours {
		sol, load, d := Number(h, "solar_w"), Number(h, "load_w"), duration(h)
		extra := 0.0
		if o.ExtraLoadW > 0 {
			if o.ExtraFloor == nil {
				extra = o.ExtraLoadW
			} else {
				net := sol - load - o.ExtraLoadW
				if net > 0 {
					net *= eff
				}
				tent := clamp(soc+net*d/capacity*100, 0, 100)
				if tent+mins[i+1]-cum[i+1] >= *o.ExtraFloor {
					extra = o.ExtraLoadW
				}
			}
		}
		net := sol - load - extra
		if net > 0 {
			net *= eff
		}
		soc = clamp(soc+net*d/capacity*100, 0, 100)
		if o.Ceiling != nil {
			soc = math.Min(soc, *o.Ceiling)
		}
		if o.ACFloor != nil {
			soc = math.Max(soc, clamp(*o.ACFloor, 0, 100))
		}
		r := Row{}
		for k, v := range h {
			r[k] = v
		}
		r["predicted_soc"] = round(soc, 1)
		r["extra_load_w_applied"] = round(extra, 1)
		out = append(out, r)
	}
	return out
}
func FitSolarCoefficient(energy, weather []Row) (float64, int) {
	solar := map[int64]float64{}
	soc := map[int64]float64{}
	ac := map[int64]bool{}
	detected := false
	for _, r := range energy {
		ts := int64(Number(r, "ts"))
		if ts <= 0 {
			continue
		}
		h := ts / 3600 * 3600
		s := Number(r, "solar_w")
		if s > solar[h] {
			solar[h] = s
		}
		if s > 50 {
			detected = true
		}
		if v := RowSOC(r); v != nil {
			old, ok := soc[h]
			if !ok || *v < old {
				soc[h] = *v
			}
		}
		if Number(r, "ac_input_w") > 50 {
			ac[h] = true
		}
	}
	if !detected {
		return 0, 0
	}
	type pair struct{ x, y float64 }
	var head, clear, broad []pair
	for _, w := range weather {
		h := int64(Number(w, "ts"))/3600*3600 - 3600
		ghi := Number(w, "ghi_w_m2")
		sol := solar[h]
		if ghi <= 50 || sol <= 0 {
			continue
		}
		p := pair{ghi, sol}
		broad = append(broad, p)
		if ghi >= 700 && Number(w, "cloud_cover_pct") <= 30 {
			clear = append(clear, p)
			v, ok := soc[h]
			if ok && v <= 70 && !ac[h] {
				head = append(head, p)
			}
		}
	}
	pairs := broad
	if len(head) >= 2 {
		pairs = head
	} else if len(clear) >= 2 {
		pairs = clear
	}
	if len(pairs) < 2 {
		return .32, len(pairs)
	}
	xx, xy := 0.0, 0.0
	for _, p := range pairs {
		xx += p.x * p.x
		xy += p.x * p.y
	}
	k := xy / xx
	if k < .05 || k > 15 {
		k = .32
	}
	return k, len(pairs)
}

type LoadKey struct{ Hour, Weekend int }

func ExpectedLoad(profile map[LoadKey]float64, ts int64, offset int, pct, flat float64) float64 {
	t := time.Unix(ts+int64(offset), 0).UTC()
	h, w := t.Hour(), 0
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		w = 1
	}
	v, ok := profile[LoadKey{h, w}]
	if !ok {
		v, ok = profile[LoadKey{h, 1 - w}]
	}
	for _, flag := range []int{w, 1 - w} {
		for _, d := range []int{1, -1, 2, -2, 3, -3} {
			if ok {
				break
			}
			v, ok = profile[LoadKey{(h + d + 24) % 24, flag}]
		}
	}
	if !ok {
		v = 30
	}
	if v <= 0 {
		return 0
	}
	return v*(1+pct) + flat
}
func median(v []float64) float64 {
	sort.Float64s(v)
	n := len(v)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}
func FitDiurnalShape(energy, weather []Row, k float64, offset int) map[int]float64 {
	out := map[int]float64{}
	for h := 0; h < 24; h++ {
		out[h] = 1
	}
	if k <= 0 {
		return out
	}
	byHour := map[int64]float64{}
	for _, r := range energy {
		h := int64(Number(r, "ts")) / 3600 * 3600
		byHour[h] = math.Max(byHour[h], Number(r, "solar_w"))
	}
	buckets := map[int][]float64{}
	for _, w := range weather {
		ts := int64(Number(w, "ts"))/3600*3600 - 3600
		ghi := Number(w, "ghi_w_m2")
		if ghi <= 50 {
			continue
		}
		ratio := byHour[ts] / (k * ghi)
		if ratio < .05 || ratio > 5 {
			continue
		}
		h := time.Unix(ts+int64(offset), 0).UTC().Hour()
		buckets[h] = append(buckets[h], ratio)
	}
	for h, v := range buckets {
		n := float64(len(v))
		out[h] = (median(v)*n + 4) / (n + 4)
	}
	return out
}
