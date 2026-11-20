package forecast

import (
	"github.com/compdani/jackery-monitor/internal/automation"
	"math"
	"sort"
	"strconv"
	"time"
)

type BuildOptions struct {
	Now                  int64
	Horizon              int
	Packs, Offset        int
	CarLoad              float64
	Lat, Lon             float64
	Plane                *Plane
	SolarHourly          map[int64]float64
	SolarHistory         map[int64]float64
	LoadMode             string
	Windows              []automation.Window
	SleepStart, SleepEnd string
	DisableCeiling       bool
	Simulation           Options
	// Optional user overrides stored via /api/devices/params.
	SolarCoeffOverride *float64
	ParasiticOverride  *float64
	OverheadOverride   *float64
	ChargeEffOverride  *float64
}

func Build(history, weather []Row, start, capacity float64, o BuildOptions) Row {
	readiness := Readiness(history, capacity)
	out := Row{"ready": readiness["ready"], "readiness": readiness, "capacity_wh": capacity, "starting_soc_pct": round(start, 1), "forecast": []Row{}}
	if readiness["ready"] != true {
		return out
	}
	if o.Now == 0 {
		o.Now = time.Now().Unix()
	}
	if o.Horizon <= 0 {
		o.Horizon = 168
	}
	completed := make([]Row, 0, len(history))
	for _, row := range history {
		ts := int64(Number(row, "ts"))
		if ts > 0 && ts/3600*3600+3600 <= o.Now {
			completed = append(completed, row)
		}
	}
	k, n := FitSolarCoefficient(completed, weather)
	if o.SolarCoeffOverride != nil && *o.SolarCoeffOverride >= 0 {
		k = *o.SolarCoeffOverride
	}
	shape := FitDiurnalShape(completed, weather, k, o.Offset)
	fsHist := map[int64]float64{}
	for ts, w := range o.SolarHistory {
		fsHist[ts] = w
	}
	for ts, w := range o.SolarHourly {
		if ts < o.Now {
			if _, exists := fsHist[ts]; !exists {
				fsHist[ts] = w
			}
		}
	}
	shade := FitShadeModel(completed, weather, ShadeOptions{
		Now:        o.Now,
		Offset:     o.Offset,
		Lat:        o.Lat,
		Lon:        o.Lon,
		Plane:      o.Plane,
		FSHistory:  fsHist,
		SolarCoeff: k,
	})
	profile := FitLoadProfileOffset(history, o.Now, o.CarLoad, 3600, o.Offset)
	parasitic, pct, dn := FitDrainModel(history, capacity, o.Packs)
	drainSrc := "default"
	if dn >= 5 {
		drainSrc = "fit"
	}
	if o.ParasiticOverride != nil && *o.ParasiticOverride >= 0 {
		parasitic = *o.ParasiticOverride
		drainSrc = "user"
	}
	if o.OverheadOverride != nil && *o.OverheadOverride >= 0 {
		pct = *o.OverheadOverride
		drainSrc = "user"
	}
	baseline := PerPackBaseline(o.Packs)
	effective := parasitic + baseline
	eff, en := FitChargeEfficiency(history, capacity, .9, 5)
	effSrc := "default"
	if en >= 5 {
		effSrc = "fit"
	}
	if o.ChargeEffOverride != nil && *o.ChargeEffOverride > 0 && *o.ChargeEffOverride <= 1 {
		eff = *o.ChargeEffOverride
		effSrc = "user"
	}
	vals := []float64{}
	peak, total := 0.0, 0.0
	for _, r := range history {
		if r["output_w"] != nil {
			v := Number(r, "output_w")
			vals = append(vals, v)
			total += v
		}
		if Number(r, "ts") >= float64(o.Now-14*86400) {
			peak = math.Max(peak, Number(r, "solar_w"))
		}
	}
	avg, p95 := 0.0, 0.0
	if len(vals) > 0 {
		avg = total / float64(len(vals))
		sort.Float64s(vals)
		p95 = vals[min(len(vals)-1, int(float64(len(vals))*.95))]
	}
	var capW, ceiling *float64
	if peak > 50 {
		v := peak * 2
		capW = &v
	}
	if !o.DisableCeiling {
		ceiling = FitChargeCeiling(history, capacity, o.Offset, 5, .05)
	}
	mode := o.LoadMode
	if mode != "scheduled" {
		mode = "historical"
	}
	hours := []Row{}
	fs, om := 0, 0
	for _, w := range weather {
		ts := int64(Number(w, "ts"))
		if ts <= o.Now {
			continue
		}
		if len(hours) >= o.Horizon {
			break
		}
		begin := ts - 3600
		duration := clamp(float64(ts-max(o.Now, begin))/3600, 0, 1)
		t := time.Unix(begin+int64(o.Offset), 0).UTC()
		ghi := Number(w, "ghi_w_m2")
		uncapped, shadeMult := shade.ApplyOpenMeteo(k, ghi, w, t.Hour(), begin, shape[t.Hour()])
		rawSolar := math.Max(0, k*ghi)
		source := "open_meteo"
		v, ok := o.SolarHourly[begin]
		if !ok {
			v, ok = o.SolarHourly[ts]
		}
		if ok {
			rawSolar = math.Max(0, v)
			uncapped, shadeMult = shade.ApplyForecastSolar(rawSolar, w, t.Hour(), begin)
			source = "forecast_solar"
			fs++
		} else {
			om++
		}
		solar := uncapped
		if capW != nil {
			solar = math.Min(solar, *capW)
		}
		asleep := automation.InSleepWindow(t.Hour()*60+t.Minute(), o.SleepStart, o.SleepEnd)
		load := 0.0
		if mode == "scheduled" {
			base := automation.ScheduledLoad(o.Windows, t.Hour(), (int(t.Weekday())+6)%7, t.Format("2006-01-02"))
			if base > 0 {
				load = base * (1 + pct)
				if !asleep {
					load += effective
				}
			}
		} else if !asleep {
			load = ExpectedLoad(profile, begin, o.Offset, pct, effective)
		}
		hours = append(hours, Row{"ts": ts, "duration_h": round(duration, 6), "solar_w": round(solar, 1), "ghi_w_m2": round(ghi, 1), "solar_w_raw": round(rawSolar, 1), "solar_shade_factor": round(shadeMult, 3), "solar_w_uncapped": round(uncapped, 1), "solar_capped": solar < uncapped, "solar_source": source, "load_w": round(load, 1), "cloud_cover_pct": round(Number(w, "cloud_cover_pct"), 1)})
	}
	sim := o.Simulation
	sim.ChargeEfficiency = &eff
	sim.Ceiling = ceiling
	hours = SimulateSOC(start, capacity, hours, sim)
	source := "open_meteo"
	if fs > 0 {
		source = "forecast_solar"
		if om > 0 {
			source = "mixed"
		}
	}
	shapeOut := map[string]float64{}
	for h, v := range shape {
		shapeOut[strconv.Itoa(h)] = round(v, 3)
	}
	nullable := func(v *float64) any {
		if v == nil {
			return nil
		}
		return round(*v, 1)
	}
	for key, v := range (Row{"solar_source": source, "load_mode": mode, "solar_coefficient": round(k, 4), "fit_samples": n, "solar_recent_peak_w": round(peak, 1), "solar_cap_w": nullable(capW), "diurnal_shape": shapeOut, "shade_profile": shade.Diagnostics(), "charge_ceiling_pct": nullable(ceiling), "overall_load_w": round(avg, 1), "output_w_p95": round(p95, 1), "parasitic_w": round(parasitic, 1), "pack_baseline_w": round(baseline, 1), "pack_count": o.Packs, "effective_parasitic_w": round(effective, 1), "inverter_overhead_pct": round(pct, 4), "inverter_overhead_n_windows": dn, "inverter_overhead_source": drainSrc, "idle_overhead_w": round(effective, 1), "idle_overhead_n_windows": dn, "charge_efficiency": round(eff, 3), "charge_efficiency_n_windows": en, "charge_efficiency_source": effSrc, "solar_calibration": SolarCalibration(completed, weather, k), "forecast": hours, "daily": SummarizeDays(hours, &start, o.Offset)}) {
		out[key] = v
	}
	return out
}

func SolarCalibration(history, weather []Row, k float64) Row {
	empty := Row{"actual_over_predicted": nil, "n": 0, "pool": nil, "suggested_k": nil}
	if k <= 0 {
		return empty
	}
	solar, soc := map[int64]float64{}, map[int64]float64{}
	ac := map[int64]bool{}
	for _, r := range history {
		ts := int64(Number(r, "ts"))
		if ts <= 0 {
			continue
		}
		h := ts / 3600 * 3600
		solar[h] = math.Max(solar[h], Number(r, "solar_w"))
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
	var head, clear, broad []float64
	for _, w := range weather {
		h := int64(Number(w, "ts"))/3600*3600 - 3600
		ghi := Number(w, "ghi_w_m2")
		if ghi <= 50 || solar[h] <= 0 {
			continue
		}
		ratio := solar[h] / (k * ghi)
		broad = append(broad, ratio)
		if ghi >= 700 && Number(w, "cloud_cover_pct") <= 30 {
			clear = append(clear, ratio)
			if v, ok := soc[h]; ok && v <= 70 && !ac[h] {
				head = append(head, ratio)
			}
		}
	}
	v, pool := broad, "broad"
	if len(head) >= 2 {
		v, pool = head, "headroom"
	} else if len(clear) >= 2 {
		v, pool = clear, "clear_sky"
	}
	if len(v) < 2 {
		return empty
	}
	mean := 0.0
	for _, r := range v {
		mean += r
	}
	mean /= float64(len(v))
	var suggested any
	if s := k * mean; s >= .05 && s <= 15 {
		suggested = round(s, 4)
	}
	return Row{"actual_over_predicted": round(mean, 4), "n": len(v), "pool": pool, "suggested_k": suggested}
}

func SummarizeDays(hours []Row, start *float64, offset int) []Row {
	out := []Row{}
	var day Row
	prev := start
	finalize := func() {
		if day == nil {
			return
		}
		day["surplus_wh"] = round(Number(day, "solar_wh")-Number(day, "load_wh"), 1)
		for _, k := range []string{"solar_wh", "load_wh", "min_soc", "start_soc", "end_soc", "peak_solar_w"} {
			if day[k] != nil {
				day[k] = round(Number(day, k), 1)
			}
		}
		out = append(out, day)
		if day["end_soc"] != nil {
			v := Number(day, "end_soc")
			prev = &v
		}
	}
	for _, h := range hours {
		ts := int64(Number(h, "ts"))
		if ts <= 0 {
			continue
		}
		if h["duration_h"] != nil {
			ts -= 3600
		}
		date := time.Unix(ts+int64(offset), 0).UTC().Format("2006-01-02")
		if day == nil || day["date"] != date {
			finalize()
			var s any
			if prev != nil {
				s = *prev
			}
			day = Row{"date": date, "solar_wh": 0.0, "load_wh": 0.0, "min_soc": nil, "start_soc": s, "end_soc": s, "peak_solar_w": 0.0}
		}
		d := 1.0
		if h["duration_h"] != nil {
			d = clamp(Number(h, "duration_h"), 0, 1)
		}
		day["solar_wh"] = Number(day, "solar_wh") + Number(h, "solar_w")*d
		day["load_wh"] = Number(day, "load_wh") + Number(h, "load_w")*d
		day["peak_solar_w"] = math.Max(Number(day, "peak_solar_w"), Number(h, "solar_w"))
		v := h["predicted_soc"]
		if v == nil {
			v = h["soc_pct"]
		}
		if v != nil {
			s := Number(map[string]any{"v": v}, "v")
			if day["min_soc"] == nil || s < Number(day, "min_soc") {
				day["min_soc"] = s
			}
			day["end_soc"] = s
			if day["start_soc"] == nil {
				day["start_soc"] = s
			}
		}
	}
	finalize()
	return out
}
