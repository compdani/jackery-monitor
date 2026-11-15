package store

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"math"
	"sort"
	"time"
)

// RecordForecast replaces targets within the same made-at hour atomically.
func (s *Store) RecordForecast(sn string, made float64, predictions []forecast.Row) (int, error) {
	if sn == "" || len(predictions) == 0 {
		return 0, nil
	}
	hour := math.Floor(made/3600) * 3600
	n := 0
	err := s.App.RunInTransaction(func(app core.App) error {
		tx := Store{App: app}
		for _, p := range predictions {
			if p["ts"] == nil || p["predicted_soc"] == nil {
				continue
			}
			target := math.Floor(forecast.Number(p, "ts")/3600) * 3600
			key := fmt.Sprintf("forecast:%s:%.0f:%.0f", sn, hour, target)
			if e := tx.Put("predictions", key, sn, map[string]any{"ts": hour, "made_at": hour, "target": target, "predicted_soc": forecast.Number(p, "predicted_soc")}); e != nil {
				return e
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

type AccuracyOptions struct {
	Now                        int64
	MaxAgeDays, Limit          int
	SinceMadeAt                int64
	MainCapacity, PackCapacity float64
}

// PredictionAccuracy averages actual SOC inside [target-1800,target+1800).
// Samples can be raw observations or imported Python buckets. Pack snapshots
// carry `packs` arrays in battery_packs records with a timestamp.
func (s *Store) PredictionAccuracy(sn string, o AccuracyOptions) ([]map[string]any, error) {
	if o.Now == 0 {
		o.Now = time.Now().Unix()
	}
	if o.MaxAgeDays <= 0 {
		o.MaxAgeDays = 14
	}
	if o.Limit <= 0 {
		o.Limit = 500
	}
	low := max(o.Now-int64(o.MaxAgeDays)*86400, o.SinceMadeAt)
	predictions, e := s.List("predictions", sn, float64(low), 0)
	if e != nil {
		return nil, e
	}
	eligible := []map[string]any{}
	for _, p := range predictions {
		if p["target"] != nil && forecast.Number(p, "target") <= float64(o.Now) && forecast.Number(p, "made_at") >= float64(low) {
			eligible = append(eligible, p)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return forecast.Number(eligible[i], "target") > forecast.Number(eligible[j], "target")
	})
	if len(eligible) > o.Limit {
		eligible = eligible[:o.Limit]
	}
	lowTarget, highTarget := math.Inf(1), math.Inf(-1)
	for _, p := range eligible {
		v := forecast.Number(p, "target")
		lowTarget = math.Min(lowTarget, v)
		highTarget = math.Max(highTarget, v)
	}
	if len(eligible) == 0 {
		return []map[string]any{}, nil
	}
	samples, e := s.rangeRows("samples", sn, lowTarget-1800, highTarget+1800)
	if e != nil {
		return nil, e
	}
	packs, e := s.rangeRows("battery_packs", sn, lowTarget-1800, highTarget+1800)
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for _, p := range eligible {
		target := forecast.Number(p, "target")
		actual, ok := socAt(samples, packs, target, o.MainCapacity, o.PackCapacity)
		if !ok {
			continue
		}
		pred := forecast.Number(p, "predicted_soc")
		out = append(out, map[string]any{"made_at": p["made_at"], "target": p["target"], "predicted_soc": pred, "actual_soc": decimal(actual), "lead_time_h": decimal((target - forecast.Number(p, "made_at")) / 3600), "error": decimal(math.Abs(actual - pred))})
	}
	return out, nil
}
func fmtDate(ts int64) string   { return time.Unix(ts, 0).UTC().Format("2006-01-02") }
func decimal(v float64) float64 { return math.RoundToEven(v*10) / 10 }
func socAt(samples, packs []map[string]any, target, mainCapacity, packCapacity float64) (float64, bool) {
	total, n := 0.0, 0
	for _, s := range samples {
		ts := forecast.Number(s, "ts")
		if s["bucket"] != nil {
			ts = forecast.Number(s, "bucket")
		}
		if ts < target-1800 || ts >= target+1800 {
			continue
		}
		for _, key := range []string{"last_battery_pct", "battery_pct", "battery_percent"} {
			if s[key] != nil {
				total += forecast.Number(s, key)
				n++
				break
			}
		}
	}
	if n == 0 {
		return 0, false
	}
	actual := total / float64(n)
	if mainCapacity <= 0 || packCapacity <= 0 {
		return actual, true
	}
	var closest map[string]any
	dist := math.Inf(1)
	for _, p := range packs {
		d := math.Abs(forecast.Number(p, "ts") - target)
		if forecast.Number(p, "ts") >= target-1800 && forecast.Number(p, "ts") < target+1800 && (d < dist || (d == dist && forecast.Number(p, "ts") > forecast.Number(closest, "ts"))) && p["packs"] != nil {
			closest = p
			dist = d
		}
	}
	if closest == nil {
		return actual, true
	}
	arr := []any{}
	for _, snapshot := range packs {
		if forecast.Number(snapshot, "ts") == forecast.Number(closest, "ts") {
			if items, ok := snapshot["packs"].([]any); ok {
				arr = append(arr, items...)
			}
		}
	}
	weighted, capacity := actual*mainCapacity, mainCapacity
	for _, item := range arr {
		p, ok := item.(map[string]any)
		if ok && p["rb"] != nil {
			weighted += forecast.Number(p, "rb") * packCapacity
			capacity += packCapacity
		}
	}
	return math.Max(0, math.Min(100, weighted/capacity)), true
}

// UpsertDailySummary preserves predictions_made_at when only actuals change.
func (s *Store) UpsertDailySummary(sn, date string, now float64, fields map[string]any) error {
	return s.App.RunInTransaction(func(app core.App) error {
		tx := Store{App: app}
		key := "daily:" + sn + ":" + date
		row, e := tx.Get("forecast_models", key)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if row == nil {
			row = map[string]any{"date": date, "predictions_made_at": nil}
		}
		for k, v := range fields {
			if v != nil {
				row[k] = v
				if k == "predicted_sunset_soc_pct" || k == "predicted_sunrise_soc_pct" {
					row["predictions_made_at"] = now
				}
			}
		}
		row["updated_at"] = now
		row["ts"] = now
		return tx.Put("forecast_models", key, sn, row)
	})
}
func (s *Store) DailySummary(sn string, now int64, days int) ([]map[string]any, error) {
	rows, e := s.List("forecast_models", sn, 0, 0)
	if e != nil {
		return nil, e
	}
	cutoff := fmtDate(now - int64(days)*86400)
	out := []map[string]any{}
	for _, r := range rows {
		date, _ := r["date"].(string)
		if date != "" && date >= cutoff {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["date"].(string) > out[j]["date"].(string) })
	return out, nil
}

func (s *Store) SystemSOCAt(sn string, target, mainCapacity, packCapacity float64) (*float64, error) {
	samples, e := s.rangeRows("samples", sn, target-1800, target+1800)
	if e != nil {
		return nil, e
	}
	packs, e := s.rangeRows("battery_packs", sn, target-1800, target+1800)
	if e != nil {
		return nil, e
	}
	v, ok := socAt(samples, packs, target, mainCapacity, packCapacity)
	if !ok {
		return nil, nil
	}
	return &v, nil
}

func (s *Store) BackfillDailyActuals(sn string, now int64, days int, mainCapacity, packCapacity float64) (int, error) {
	filled := 0
	err := s.App.RunInTransaction(func(app core.App) error {
		tx := Store{App: app}
		rows, e := tx.DailySummary(sn, now, days)
		if e != nil {
			return e
		}
		for _, r := range rows {
			fields := map[string]any{}
			for _, event := range []string{"sunset", "sunrise"} {
				key := "actual_" + event + "_soc_pct"
				ts := forecast.Number(r, event+"_ts")
				if r[key] != nil || ts <= 0 || ts > float64(now) {
					continue
				}
				v, e := tx.SystemSOCAt(sn, ts, mainCapacity, packCapacity)
				if e != nil {
					return e
				}
				if v != nil {
					fields[key] = *v
					filled++
				}
			}
			if len(fields) > 0 {
				if e := tx.UpsertDailySummary(sn, r["date"].(string), float64(now), fields); e != nil {
					return e
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return filled, nil
}

func (s *Store) rangeRows(collection, sn string, low, high float64) ([]map[string]any, error) {
	records, e := s.App.FindRecordsByFilter(collection, "device_sn = {:sn} && ts >= {:low} && ts < {:high}", "ts", 0, 0, dbx.Params{"sn": sn, "low": low, "high": high})
	if e != nil {
		return nil, e
	}
	out := make([]map[string]any, 0, len(records))
	for _, r := range records {
		p, e := payload(r)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, nil
}
