package store

import (
	"errors"
	"fmt"
	"github.com/pocketbase/pocketbase/core"
	"math"
	"sort"
	"time"
)

// EnergyMinutes keeps imported/corrected accumulated Wh authoritative. Raw
// observations are integrated only with other raw observations, never with an
// aggregate row or across an outage. Aggregate minutes win on overlap.
func (s *Store) EnergyMinutes(sn string, since float64) ([]map[string]any, error) {
	rows, err := s.List("samples", sn, math.Max(0, since-300), 0)
	if err != nil {
		return nil, err
	}
	buckets := map[int64]map[string]any{}
	raw := []map[string]any{}
	for _, r := range rows {
		if r["bucket"] != nil || r["_energy_kind"] == "aggregate" {
			b := int64(num(r["bucket"]))
			if r["bucket"] == nil {
				b = int64(num(r["ts"])) / 60 * 60
			}
			buckets[b] = r
		} else {
			raw = append(raw, r)
		}
	}
	imported := map[int64]bool{}
	for b := range buckets {
		imported[b] = true
	}
	for i, r := range raw {
		b := int64(num(r["ts"])) / 60 * 60
		if imported[b] {
			continue
		}
		row := buckets[b]
		if row == nil {
			row = map[string]any{"ts": float64(b), "bucket": float64(b), "sample_count": 0.0}
			for _, f := range energyFields {
				row[f] = 0.0
			}
			buckets[b] = row
		}
		for dst, src := range map[string]string{"last_input_w": "input_power_w", "last_output_w": "output_power_w", "last_solar_w": "solar_input_w", "last_ac_input_w": "ac_input_w", "last_battery_pct": "battery_percent", "system_soc": "system_soc"} {
			if r[src] != nil {
				row[dst] = r[src]
			}
		}
		row["sample_count"] = num(row["sample_count"]) + 1
		if i == 0 {
			continue
		}
		a := raw[i-1]
		dt := num(r["ts"]) - num(a["ts"])
		if dt <= 0 || dt > 300 {
			continue
		}
		for dst, src := range map[string]string{"input_wh": "input_power_w", "output_wh": "output_power_w", "solar_wh": "solar_input_w", "ac_input_wh": "ac_input_w", "solar_charge_diverted_wh": "solar_charge_diverted_w"} {
			av, zv := num(a[src]), num(r[src])
			if src == "solar_input_w" {
				if a[src] == nil {
					av = num(a["solar_w"])
				}
				if r[src] == nil {
					zv = num(r["solar_w"])
				}
			}
			wh := math.Max(0, (av+zv)/2) * dt / 3600
			row[dst] = num(row[dst]) + wh
		}
		row["solar_charge_diverted_wh"] = math.Min(num(row["solar_charge_diverted_wh"]), num(row["output_wh"]))
	}
	out := []map[string]any{}
	for b, row := range buckets {
		if float64(b) >= since {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return num(out[i]["ts"]) < num(out[j]["ts"]) })
	return out, nil
}
func (s *Store) CorrectOutput(sn string, start, end, power float64) (map[string]any, error) {
	if sn == "" || math.IsNaN(start) || math.IsNaN(end) || math.IsNaN(power) || math.IsInf(start, 0) || math.IsInf(end, 0) || math.IsInf(power, 0) || start < 0 || power < 0 || end <= start || end-start > 86400 {
		return nil, errors.New("device_sn and finite nonnegative power/timestamps required; range must be positive and at most 24 hours")
	}
	first := math.Floor(start/60) * 60
	last := first
	count := 0
	err := s.App.RunInTransaction(func(app core.App) error {
		tx := &Store{App: app}
		minutes, err := tx.EnergyMinutes(sn, first)
		if err != nil {
			return err
		}
		byBucket := map[float64]map[string]any{}
		for _, r := range minutes {
			byBucket[num(r["ts"])] = r
		}
		for b := first; b < end; b += 60 {
			row := byBucket[b]
			if row == nil {
				row = map[string]any{"input_wh": 0.0, "solar_wh": 0.0, "ac_input_wh": 0.0, "sample_count": 1.0}
			}
			row["ts"] = b
			row["bucket"] = b
			row["device_sn"] = sn
			row["output_wh"] = power / 60
			row["last_output_w"] = math.RoundToEven(power)
			row["_corrected_output"] = true
			row["_energy_kind"] = "aggregate"
			if err := tx.Put("samples", SampleKey(sn, b), sn, row); err != nil {
				return err
			}
			last = b
			count++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"buckets": count, "start_bucket": first, "end_bucket": last, "output_w": math.RoundToEven(power)}, nil
}
func EnergyHistory(minutes []map[string]any, since float64, bucket int) []map[string]any {
	groups := map[int64][]map[string]any{}
	for _, r := range minutes {
		ts := num(r["ts"])
		if ts < since {
			continue
		}
		b := int64(ts) / int64(bucket) * int64(bucket)
		groups[b] = append(groups[b], r)
	}
	out := []map[string]any{}
	for b, rows := range groups {
		p := map[string]any{"ts": float64(b)}
		for _, f := range energyFields {
			v := 0.0
			for _, r := range rows {
				v += num(r[f])
			}
			p[f] = v
		}
		for dst, src := range map[string]string{"input_w": "last_input_w", "output_w": "last_output_w", "solar_w": "last_solar_w", "ac_input_w": "last_ac_input_w", "battery_pct": "last_battery_pct", "system_soc": "system_soc"} {
			sum, n := 0.0, 0
			for _, r := range rows {
				if r[src] != nil {
					sum += num(r[src])
					n++
				}
			}
			p[dst] = nil
			if n > 0 {
				p[dst] = sum / float64(n)
			} else if dst != "battery_pct" && dst != "system_soc" {
				p[dst] = 0.0
			}
		}
		p["solar_w"] = math.Max(num(p["solar_w"]), num(p["solar_wh"])*3600/float64(bucket))
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return num(out[i]["ts"]) < num(out[j]["ts"]) })
	return out
}
func EnergyTotals(minutes []map[string]any, now float64, offset int) map[string]any {
	startToday := math.Floor((now+float64(offset))/86400)*86400 - float64(offset)
	out := map[string]any{}
	for label, since := range map[string]float64{"lifetime": 0, "today": startToday, "last_7d": now - 7*86400, "last_30d": now - 30*86400} {
		totals := map[string]any{}
		for _, f := range energyFields {
			v := 0.0
			for _, r := range minutes {
				if num(r["ts"]) >= since {
					v += num(r[f])
				}
			}
			totals[f] = v
		}
		if label != "lifetime" {
			totals["since_ts"] = since
		}
		out[label] = totals
	}
	return out
}
func EnergyDaily(minutes []map[string]any, offset int) []map[string]any {
	groups := map[string][]map[string]any{}
	for _, r := range minutes {
		date := time.Unix(int64(num(r["ts"]))+int64(offset), 0).UTC().Format("2006-01-02")
		groups[date] = append(groups[date], r)
	}
	out := []map[string]any{}
	for date, rows := range groups {
		r := map[string]any{"date": date, "peak_solar_w": 0.0, "peak_output_w": 0.0, "min_soc": nil, "max_soc": nil}
		for dst, src := range map[string]string{"solar_kwh": "solar_wh", "consumed_kwh": "output_wh", "charged_kwh": "input_wh", "grid_kwh": "ac_input_wh", "diverted_kwh": "solar_charge_diverted_wh"} {
			v := 0.0
			for _, p := range rows {
				v += num(p[src])
			}
			r[dst] = math.RoundToEven(v/10) / 100
		}
		for _, p := range rows {
			r["peak_solar_w"] = math.Max(num(r["peak_solar_w"]), num(p["last_solar_w"]))
			r["peak_output_w"] = math.Max(num(r["peak_output_w"]), num(p["last_output_w"]))
			if v := p["last_battery_pct"]; v != nil {
				if r["min_soc"] == nil || num(v) < num(r["min_soc"]) {
					r["min_soc"] = v
				}
				if r["max_soc"] == nil || num(v) > num(r["max_soc"]) {
					r["max_soc"] = v
				}
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return text(out[i]["date"]) < text(out[j]["date"]) })
	return out
}
func (s *Store) SetCapacity(sn string, capacity any) error {
	if sn == "" {
		return errors.New("device_sn required")
	}
	if capacity != nil && (num(capacity) < 500 || num(capacity) > 200000) {
		return errors.New("capacity must be null or 500..200000 Wh")
	}
	row, err := s.Get("devices", sn)
	if err != nil {
		return err
	}
	device, _ := row["device"].(map[string]any)
	if device == nil {
		return fmt.Errorf("device metadata missing")
	}
	device["capacity_wh_override"] = capacity
	row["capacity_wh_override"] = capacity
	row["_capacity_corrected"] = true
	return s.Put("devices", sn, sn, row)
}

// DecorateHistorySOC uses the nearest complete pack snapshot within 30 minutes.
// Separate imported pack rows at the same timestamp form one snapshot.
func (s *Store) DecorateHistorySOC(sn string, history []map[string]any, mainCapacity, packCapacity float64) error {
	if mainCapacity <= 0 || packCapacity <= 0 {
		return nil
	}
	packs, err := s.List("battery_packs", sn, 0, 0)
	if err != nil {
		return err
	}
	grouped := map[float64][]any{}
	times := []float64{}
	for _, row := range packs {
		ts := num(row["ts"])
		if _, ok := grouped[ts]; !ok {
			times = append(times, ts)
		}
		if items, ok := row["packs"].([]any); ok {
			grouped[ts] = append(grouped[ts], items...)
		}
	}
	sort.Float64s(times)
	for _, row := range history {
		if row["battery_pct"] == nil {
			continue
		}
		target := num(row["ts"])
		idx := sort.SearchFloat64s(times, target)
		nearest := -1
		distance := math.Inf(1)
		for _, i := range []int{idx - 1, idx} {
			if i >= 0 && i < len(times) {
				d := math.Abs(times[i] - target)
				if d <= 1800 && d <= distance {
					distance = d
					nearest = i
				}
			}
		}
		if nearest < 0 {
			continue
		}
		sum, capacity := num(row["battery_pct"])*mainCapacity, mainCapacity
		for _, raw := range grouped[times[nearest]] {
			pack, ok := raw.(map[string]any)
			if ok && pack["rb"] != nil {
				sum += num(pack["rb"]) * packCapacity
				capacity += packCapacity
			}
		}
		row["system_soc"] = math.Max(0, math.Min(100, sum/capacity))
	}
	return nil
}
