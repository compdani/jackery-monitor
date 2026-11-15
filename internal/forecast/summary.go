package forecast

import (
	"math"
	"sort"
	"time"
)

func FitMaxCharge(history, weather []Row, offset int, minInput float64, minSamples int, percentile float64) (*float64, int) {
	ghi := map[int64]float64{}
	for _, w := range weather {
		ts := int64(Number(w, "ts"))
		ghi[ts/3600*3600] = Number(w, "ghi_w_m2")
	}
	var candidates []float64
	for _, r := range history {
		input, ac, solar := Number(r, "input_w"), Number(r, "ac_input_w"), Number(r, "solar_w")
		ts := int64(Number(r, "ts"))
		if ac >= minInput && solar < minInput {
			candidates = append(candidates, ac)
		} else if input >= minInput && ts > 0 {
			g, ok := ghi[ts/3600*3600]
			if ok {
				if g < 50 {
					candidates = append(candidates, input)
				}
			} else if offset != 0 {
				h := time.Unix(ts+int64(offset), 0).UTC().Hour()
				if (h >= 21 || h < 6) && solar < minInput {
					candidates = append(candidates, input)
				}
			}
		}
	}
	n := len(candidates)
	if n < minSamples || n == 0 {
		return nil, n
	}
	sort.Float64s(candidates)
	v := candidates[max(0, min(n-1, int(float64(n)*percentile)))]
	return &v, n
}

func SummarizeEVDivert(hours []Row, car, floor float64, offset int) Row {
	car = math.Max(0, car)
	out := Row{"car_load_w": car, "comfort_floor_pct": floor, "total_hours": 0.0, "total_sessions": 0, "divertable_wh": 0.0, "days": []Row{}}
	if car <= 0 || len(hours) == 0 {
		return out
	}
	days := map[string]Row{}
	inSession := false
	totalHours, totalWH := 0.0, 0.0
	sessions := 0
	for _, h := range hours {
		ts := int64(Number(h, "ts"))
		if ts <= 0 {
			continue
		}
		start := ts
		duration := 1.0
		if h["duration_h"] != nil {
			start -= 3600
			duration = clamp(Number(h, "duration_h"), 0, 1)
		}
		date := time.Unix(start+int64(offset), 0).UTC().Format("2006-01-02")
		day := days[date]
		if day == nil {
			day = Row{"date": date, "hours": 0.0, "sessions": 0, "divertable_wh": 0.0}
			days[date] = day
		}
		soc := h["predicted_soc"]
		if soc == nil {
			soc = h["soc_pct"]
		}
		ok := Number(h, "solar_w")-Number(h, "load_w") >= car && (soc == nil || Number(map[string]any{"soc": soc}, "soc") >= floor)
		if ok {
			if !inSession {
				inSession = true
				sessions++
				day["sessions"] = int(Number(day, "sessions")) + 1
			}
			totalHours += duration
			day["hours"] = Number(day, "hours") + duration
			wh := car * duration
			totalWH += wh
			day["divertable_wh"] = Number(day, "divertable_wh") + wh
		} else {
			inSession = false
		}
	}
	keys := []string{}
	for k := range days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := []Row{}
	for _, k := range keys {
		day := days[k]
		day["hours"] = round(Number(day, "hours"), 2)
		day["divertable_wh"] = round(Number(day, "divertable_wh"), 1)
		rows = append(rows, day)
	}
	out["car_load_w"] = round(car, 1)
	out["total_hours"] = round(totalHours, 2)
	out["total_sessions"] = sessions
	out["divertable_wh"] = round(totalWH, 1)
	out["days"] = rows
	return out
}

func BucketAccuracy(samples []map[string]any) map[string]Row {
	out := map[string]Row{}
	for _, s := range samples {
		h := Number(s, "lead_time_h")
		bucket := ">72h"
		if h <= 6 {
			bucket = "≤6h"
		} else if h <= 24 {
			bucket = "≤24h"
		} else if h <= 72 {
			bucket = "≤72h"
		}
		b := out[bucket]
		if b == nil {
			b = Row{"n": 0, "sum_err": 0.0, "sum_signed": 0.0}
			out[bucket] = b
		}
		b["n"] = int(Number(b, "n")) + 1
		b["sum_err"] = Number(b, "sum_err") + Number(s, "error")
		b["sum_signed"] = Number(b, "sum_signed") + Number(s, "predicted_soc") - Number(s, "actual_soc")
	}
	for _, b := range out {
		n := Number(b, "n")
		b["mae"] = round(Number(b, "sum_err")/n, 2)
		b["bias_pp"] = round(Number(b, "sum_signed")/n, 2)
		delete(b, "sum_err")
		delete(b, "sum_signed")
	}
	return out
}
