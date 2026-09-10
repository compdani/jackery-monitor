// Package forecast ports the history-fitted solar/load model from forecaster.py.
package forecast

import (
	"math"
	"sort"
	"strconv"
	"time"

	"jackery-monitor/internal/energy"
)

const ModelVersion = "go-forecast-1"

type Row = map[string]any

func num(r Row, k string) float64 { return energy.Number(r[k]) }
func soc(r Row) *float64 {
	if r["system_soc"] != nil {
		return energy.Nullable(r["system_soc"])
	}
	return energy.Nullable(r["battery_pct"])
}
func sorted(rows []Row) []Row {
	out := append([]Row(nil), rows...)
	sort.Slice(out, func(i, j int) bool { return num(out[i], "ts") < num(out[j], "ts") })
	return out
}
func median(values []float64) float64 {
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	if len(v) == 0 {
		return 0
	}
	if len(v)%2 == 0 {
		return (v[len(v)/2-1] + v[len(v)/2]) / 2
	}
	return v[len(v)/2]
}
func upperMedian(v []float64) float64    { sort.Float64s(v); return v[len(v)/2] }
func round(v float64, n float64) float64 { return math.Round(v*n) / n }

type pair struct{ x, y float64 }

func solarFit(rows []Row, wx []WeatherHour) (float64, int, map[int64]float64) {
	solar := map[int64]float64{}
	low := map[int64]float64{}
	ac := map[int64]bool{}
	peak := 0.0
	for _, r := range rows {
		ts := int64(num(r, "ts")) / 3600 * 3600
		v := num(r, "solar_w")
		if ts <= 0 {
			continue
		}
		solar[ts] = max(solar[ts], v)
		peak = max(peak, v)
		if s := soc(r); s != nil {
			old, ok := low[ts]
			if !ok || *s < old {
				low[ts] = *s
			}
		}
		if num(r, "ac_input_w") > 50 {
			ac[ts] = true
		}
	}
	if peak <= 50 {
		return 0, 0, solar
	}
	broad, clear, headroom := []pair{}, []pair{}, []pair{}
	for _, w := range wx {
		h := w.TS / 3600 * 3600
		s := solar[h]
		if w.GHI <= 50 || s <= 0 {
			continue
		}
		p := pair{w.GHI, s}
		broad = append(broad, p)
		if w.GHI >= 700 && w.Cloud <= 30 {
			clear = append(clear, p)
			if v, ok := low[h]; ok && v <= 70 && !ac[h] {
				headroom = append(headroom, p)
			}
		}
	}
	pairs := broad
	if len(headroom) >= 2 {
		pairs = headroom
	} else if len(clear) >= 2 {
		pairs = clear
	}
	if len(pairs) < 2 {
		return .32, len(pairs), solar
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
	return k, len(pairs), solar
}
func diurnal(solar map[int64]float64, wx []WeatherHour, k float64, zone *time.Location) [24]float64 {
	var shape [24]float64
	var ratios [24][]float64
	for i := range shape {
		shape[i] = 1
	}
	if k <= 0 {
		return shape
	}
	for _, w := range wx {
		if w.GHI <= 50 || solar[w.TS/3600*3600] <= 0 {
			continue
		}
		r := solar[w.TS/3600*3600] / (k * w.GHI)
		if r < .05 || r > 5 {
			continue
		}
		h := time.Unix(w.TS, 0).In(zone).Hour()
		ratios[h] = append(ratios[h], r)
	}
	for h, rs := range ratios {
		n := float64(len(rs))
		shape[h] = (n*median(rs) + 4) / (n + 4)
	}
	return shape
}

type profileKey struct {
	hour    int
	weekend bool
}

func keyAt(ts int64, zone *time.Location) profileKey {
	t := time.Unix(ts, 0).In(zone)
	return profileKey{t.Hour(), t.Weekday() == time.Saturday || t.Weekday() == time.Sunday}
}
func loadProfile(rows []Row, now int64, zone *time.Location, carLoad float64) map[profileKey]float64 {
	net := func(r Row) float64 {
		out := num(r, "output_w")
		if r["solar_charge_plug_on_frac"] != nil && carLoad > 0 {
			return max(0, out-num(r, "solar_charge_plug_on_frac")*carLoad)
		}
		return max(0, out-num(r, "solar_charge_diverted_wh"))
	}
	vals := []float64{}
	for _, r := range rows {
		if r["output_w"] != nil {
			vals = append(vals, net(r))
		}
	}
	result := map[profileKey]float64{}
	if len(vals) == 0 {
		return result
	}
	sort.Float64s(vals)
	cap := vals[min(len(vals)-1, int(float64(len(vals))*.95))]
	buckets := map[profileKey][]pair{}
	for _, r := range rows {
		if r["output_w"] == nil {
			continue
		}
		ts := int64(num(r, "ts"))
		key := keyAt(ts, zone)
		buckets[key] = append(buckets[key], pair{min(net(r), cap), float64(ts)})
	}
	for k, samples := range buckets {
		all, recent, older := []float64{}, []float64{}, []float64{}
		for _, s := range samples {
			all = append(all, s.x)
			if int64(s.y) >= now-3*86400 {
				recent = append(recent, s.x)
			} else {
				older = append(older, s.x)
			}
		}
		sort.Float64s(all)
		m := upperMedian(all)
		iqr := all[3*len(all)/4] - all[len(all)/4]
		value := m
		if m > 0 && iqr/m >= .5 && len(all) >= 6 && len(recent) > 0 {
			old := upperMedian(recent)
			if len(older) > 0 {
				old = upperMedian(older)
			}
			value = .7*upperMedian(recent) + .3*old
		}
		result[k] = min(value, max(180, cap))
	}
	return result
}
func expected(profile map[profileKey]float64, ts int64, zone *time.Location) float64 {
	key := keyAt(ts, zone)
	if v, ok := profile[key]; ok {
		return v
	}
	if v, ok := profile[profileKey{key.hour, !key.weekend}]; ok {
		return v
	}
	for _, weekend := range []bool{key.weekend, !key.weekend} {
		for _, delta := range []int{1, -1, 2, -2, 3, -3} {
			if v, ok := profile[profileKey{(key.hour + delta + 24) % 24, weekend}]; ok {
				return v
			}
		}
	}
	return 30
}
func drainFit(rows []Row, capacity float64, packs int) (float64, float64, int) {
	requireSystem := false
	for _, r := range rows {
		requireSystem = requireSystem || r["system_soc"] != nil
	}
	baseline := float64(max(0, packs)) * 60
	pairs := []pair{}
	for i := 0; i+1 < len(rows); i++ {
		a, b := rows[i], rows[i+1]
		sa, sb := soc(a), soc(b)
		if sa == nil || sb == nil || *sa < 85 {
			continue
		}
		if requireSystem && (a["system_soc"] == nil || b["system_soc"] == nil) {
			continue
		}
		threshold := 2.0
		if a["system_soc"] != nil && b["system_soc"] != nil {
			threshold = .5
		}
		dt := (num(b, "ts") - num(a, "ts")) / 3600
		if *sa-*sb < threshold || num(a, "solar_wh") > 20 || num(a, "ac_input_wh") > 20 || dt < .5 || dt > 6 {
			continue
		}
		load := num(a, "output_wh") / dt
		if load < 50 {
			continue
		}
		pairs = append(pairs, pair{load, max(0, (*sa-*sb)*capacity/100/dt-baseline)})
	}
	n := len(pairs)
	if n < 5 {
		return 50, .1, n
	}
	loads := []float64{}
	for _, p := range pairs {
		loads = append(loads, p.x)
	}
	sort.Float64s(loads)
	if loads[min(n-1, int(float64(n)*.9))]/loads[int(float64(n)*.1)] < 2 {
		runs := cleanRuns(rows, capacity, requireSystem)
		pool := runs
		long := [][3]float64{}
		for _, r := range runs {
			if r[2] >= 4 {
				long = append(long, r)
			}
		}
		if len(long) >= 2 {
			pool = long
		}
		implied := []float64{}
		if len(pool) >= 2 {
			for _, r := range pool {
				implied = append(implied, max(0, r[1]-baseline)-r[0]*1.1)
			}
			v := max(0, upperMedian(implied))
			if v <= 1000 {
				return v, .1, len(pool)
			}
		}
		implied = nil
		for _, p := range pairs {
			implied = append(implied, p.y-p.x*1.1)
		}
		v := max(0, upperMedian(implied))
		if v <= 1000 {
			return v, .1, n
		}
		return 50, .1, n
	}
	x, y, xy, xx := 0.0, 0.0, 0.0, 0.0
	for _, p := range pairs {
		x += p.x
		y += p.y
		xy += p.x * p.y
		xx += p.x * p.x
	}
	den := float64(n)*xx - x*x
	if den <= 0 {
		return 50, .1, n
	}
	b := (float64(n)*xy - x*y) / den
	a := (y - b*x) / float64(n)
	if a < 0 || a > 1000 || b < 1 || b > 1.5 {
		return 50, .1, n
	}
	return a, b - 1, n
}
func cleanRuns(rows []Row, capacity float64, requireSystem bool) [][3]float64 {
	result := [][3]float64{}
	run := []Row{}
	flush := func() {
		if len(run) < 2 {
			return
		}
		a, b := run[0], run[len(run)-1]
		sa, sb := soc(a), soc(b)
		dt := (num(b, "ts") - num(a, "ts")) / 3600
		pp := 3.0
		if a["system_soc"] != nil && b["system_soc"] != nil {
			pp = .5
		}
		if dt < 2 || dt > 12 || sa == nil || sb == nil || *sa < 85 || *sa-*sb < pp {
			return
		}
		out := 0.0
		for _, r := range run[:len(run)-1] {
			out += num(r, "output_wh")
		}
		if out/dt >= 50 {
			result = append(result, [3]float64{out / dt, (*sa - *sb) * capacity / 100 / dt, dt})
		}
	}
	for _, r := range rows {
		clean := soc(r) != nil && (!requireSystem || r["system_soc"] != nil) && num(r, "solar_wh") <= 20 && num(r, "ac_input_wh") <= 20
		gap := len(run) > 0 && num(r, "ts")-num(run[len(run)-1], "ts") > 5400
		if !clean || gap {
			flush()
			run = nil
		}
		if clean {
			run = append(run, r)
		}
	}
	flush()
	return result
}
func chargeFit(rows []Row, capacity float64) (float64, int) {
	values := []float64{}
	requireSystem := false
	for _, r := range rows {
		requireSystem = requireSystem || r["system_soc"] != nil
	}
	for i := 0; i+1 < len(rows); i++ {
		a, b := rows[i], rows[i+1]
		sa, sb := soc(a), soc(b)
		if sa == nil || sb == nil || *sa > 95 || *sb > 99 || requireSystem && (a["system_soc"] == nil || b["system_soc"] == nil) {
			continue
		}
		pp := 1.0
		if a["system_soc"] != nil && b["system_soc"] != nil {
			pp = .25
		}
		net := num(a, "input_wh") - num(a, "output_wh")
		dt := (num(b, "ts") - num(a, "ts")) / 3600
		if *sb-*sa < pp || net < 100 || dt <= 0 || dt > 6 {
			continue
		}
		values = append(values, (*sb-*sa)*capacity/100/net)
	}
	if len(values) < 5 {
		return .9, len(values)
	}
	v := upperMedian(values)
	if v < .5 || v > .99 {
		return .9, len(values)
	}
	return v, len(values)
}
func ceilingFit(rows []Row, capacity float64, zone *time.Location) *float64 {
	type day struct {
		soc, solar, out float64
		hasSOC          bool
	}
	days := map[string]day{}
	for _, r := range rows {
		key := time.Unix(int64(num(r, "ts")), 0).In(zone).Format("2006-01-02")
		d := days[key]
		if s := soc(r); s != nil {
			d.soc = max(d.soc, *s)
			d.hasSOC = true
		}
		d.solar += num(r, "solar_wh")
		d.out += num(r, "output_wh")
		days[key] = d
	}
	values := []float64{}
	for _, d := range days {
		if d.hasSOC && d.solar-d.out > .05*capacity {
			values = append(values, d.soc)
		}
	}
	if len(values) < 5 {
		return nil
	}
	sort.Float64s(values)
	v := values[min(len(values)-1, int(float64(len(values))*.9))]
	if v >= 97 {
		return nil
	}
	return &v
}

type Simulation struct {
	Efficiency     float64
	Floor, Ceiling *float64
	ExtraW         float64
	ExtraFloor     *float64
}

func Simulate(start, capacity float64, hours []Row, opt Simulation) []Row {
	out := []Row{}
	if capacity <= 0 {
		return out
	}
	soc := min(100, max(0, start))
	cum := make([]float64, len(hours)+1)
	for i, h := range hours {
		net := num(h, "solar_w") - num(h, "load_w")
		if net > 0 {
			net *= opt.Efficiency
		}
		cum[i+1] = cum[i] + net/capacity*100
	}
	low := append([]float64(nil), cum...)
	for i := len(hours) - 1; i >= 0; i-- {
		low[i] = min(cum[i], low[i+1])
	}
	for i, h := range hours {
		extra := 0.0
		if opt.ExtraW > 0 {
			extra = opt.ExtraW
			if opt.ExtraFloor != nil {
				net := num(h, "solar_w") - num(h, "load_w") - extra
				if net > 0 {
					net *= opt.Efficiency
				}
				tentative := max(0, min(100, soc+net/capacity*100))
				if tentative+low[i+1]-cum[i+1] < *opt.ExtraFloor {
					extra = 0
				}
			}
		}
		net := num(h, "solar_w") - num(h, "load_w") - extra
		if net > 0 {
			net *= opt.Efficiency
		}
		soc = max(0, min(100, soc+net/capacity*100))
		if opt.Ceiling != nil {
			soc = min(soc, *opt.Ceiling)
		}
		if opt.Floor != nil {
			soc = max(soc, min(100, max(0, *opt.Floor)))
		}
		row := Row{}
		for k, v := range h {
			row[k] = v
		}
		row["predicted_soc"] = round(soc, 10)
		row["extra_load_w_applied"] = round(extra, 10)
		out = append(out, row)
	}
	return out
}

type Options struct {
	Now                time.Time
	Zone               *time.Location
	Capacity, StartSOC float64
	PackCount          int
	Floor, ExtraFloor  *float64
	ExtraW, CarLoad    float64
}

func Build(history []Row, weather []WeatherHour, o Options) Row {
	if o.Zone == nil {
		o.Zone = time.UTC
	}
	rows := sorted(history)
	if o.PackCount == 0 {
		for i, r := range rows {
			copy := Row{}
			for k, v := range r {
				copy[k] = v
			}
			copy["system_soc"] = nil
			rows[i] = copy
		}
	}
	have := 0.0
	if len(rows) > 1 {
		have = (num(rows[len(rows)-1], "ts") - num(rows[0], "ts")) / 3600
	}
	ready := have >= 24 && o.Capacity > 0
	reason := "calibrating"
	if len(rows) < 2 {
		reason = "no_history"
	}
	if ready {
		reason = "ready"
	}
	parasitic, pct, n := drainFit(rows, o.Capacity, o.PackCount)
	readiness := Row{"ready": ready, "reason": reason, "have_hours": round(have, 10), "needed_hours": 24, "have_idle_windows": n, "needed_idle_windows": 5, "low_confidence_overhead_fit": n < 5}
	result := Row{"ready": ready, "readiness": readiness, "capacity_wh": o.Capacity, "starting_soc_pct": round(o.StartSOC, 10), "forecast": []Row{}, "model_version": ModelVersion}
	if !ready {
		return result
	}
	k, solarN, solar := solarFit(rows, weather)
	shape := diurnal(solar, weather, k, o.Zone)
	profile := loadProfile(rows, o.Now.Unix(), o.Zone, o.CarLoad)
	eff, effN := chargeFit(rows, o.Capacity)
	ceiling := ceilingFit(rows, o.Capacity, o.Zone)
	peak, overall := 0.0, 0.0
	loads := []float64{}
	for _, r := range rows {
		if num(r, "ts") >= float64(o.Now.Unix()-14*86400) {
			peak = max(peak, num(r, "solar_w"))
		}
		if r["output_w"] != nil {
			loads = append(loads, num(r, "output_w"))
			overall += num(r, "output_w")
		}
	}
	p95 := 0.0
	if len(loads) > 0 {
		overall /= float64(len(loads))
		sort.Float64s(loads)
		p95 = loads[min(len(loads)-1, int(float64(len(loads))*.95))]
	}
	hours := []Row{}
	baseline := float64(max(0, o.PackCount)) * 60
	for _, w := range weather {
		if w.TS < o.Now.Unix() {
			continue
		}
		if len(hours) >= 120 {
			break
		}
		raw := max(0, k*w.GHI*shape[time.Unix(w.TS, 0).In(o.Zone).Hour()])
		solar := raw
		if peak > 50 {
			solar = min(solar, peak*2)
		}
		hours = append(hours, Row{"ts": w.TS, "solar_w": round(solar, 10), "ghi_w_m2": w.GHI, "solar_w_uncapped": round(raw, 10), "solar_capped": solar < raw, "load_w": round(expected(profile, w.TS, o.Zone)*(1+pct)+parasitic+baseline, 10), "cloud_cover_pct": w.Cloud})
	}
	if len(hours) == 0 {
		result["ready"] = false
		result["error"] = "Weather forecast has no future hours"
		return result
	}
	result["forecast"] = Simulate(o.StartSOC, o.Capacity, hours, Simulation{eff, o.Floor, ceiling, o.ExtraW, o.ExtraFloor})
	source := func(n int) string {
		if n >= 5 {
			return "fit"
		}
		return "default"
	}
	for key, v := range (Row{"solar_coefficient": round(k, 10000), "fit_samples": solarN, "diurnal_shape": shapeJSON(shape), "solar_recent_peak_w": peak, "solar_cap_w": nil, "charge_ceiling_pct": ceiling, "overall_load_w": round(overall, 10), "output_w_p95": p95, "parasitic_w": round(parasitic, 10), "pack_baseline_w": baseline, "pack_count": o.PackCount, "effective_parasitic_w": round(parasitic+baseline, 10), "inverter_overhead_pct": round(pct, 10000), "inverter_overhead_n_windows": n, "inverter_overhead_source": source(n), "idle_overhead_w": round(parasitic+baseline, 10), "idle_overhead_n_windows": n, "charge_efficiency": round(eff, 1000), "charge_efficiency_n_windows": effN, "charge_efficiency_source": source(effN)}) {
		result[key] = v
	}
	if peak > 50 {
		result["solar_cap_w"] = peak * 2
	}
	return result
}

func shapeJSON(shape [24]float64) map[string]float64 {
	out := map[string]float64{}
	for i, v := range shape {
		out[strconv.Itoa(i)] = round(v, 1000)
	}
	return out
}
