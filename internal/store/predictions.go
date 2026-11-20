package store

import (
	"database/sql"
	"encoding/json"
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
// Unchanged predictions for the hour are skipped to avoid redundant SQLite writes.
func (s *Store) RecordForecast(sn string, made float64, predictions []forecast.Row) (int, error) {
	if sn == "" || len(predictions) == 0 {
		return 0, nil
	}
	hour := math.Floor(made/3600) * 3600
	type item struct {
		key    string
		target float64
		soc    float64
	}
	items := make([]item, 0, len(predictions))
	for _, p := range predictions {
		if p["ts"] == nil || p["predicted_soc"] == nil {
			continue
		}
		target := math.Floor(forecast.Number(p, "ts")/3600) * 3600
		items = append(items, item{
			key:    fmt.Sprintf("forecast:%s:%.0f:%.0f", sn, hour, target),
			target: target,
			soc:    forecast.Number(p, "predicted_soc"),
		})
	}
	if len(items) == 0 {
		return 0, nil
	}
	var existingRows []struct {
		Key     string `db:"key"`
		Payload string `db:"payload"`
	}
	if err := s.App.DB().NewQuery(`SELECT key, payload FROM predictions WHERE device_sn = {:sn} AND ts = {:hour}`).
		Bind(dbx.Params{"sn": sn, "hour": hour}).All(&existingRows); err != nil {
		return 0, err
	}
	existing := make(map[string]float64, len(existingRows))
	for _, r := range existingRows {
		var p map[string]any
		if json.Unmarshal([]byte(r.Payload), &p) == nil && p["predicted_soc"] != nil {
			existing[r.Key] = forecast.Number(p, "predicted_soc")
		}
	}
	changed := make([]item, 0, len(items))
	for _, it := range items {
		if prev, ok := existing[it.key]; !ok || math.Abs(prev-it.soc) > 1e-6 {
			changed = append(changed, it)
		}
	}
	if len(changed) == 0 {
		return len(items), nil
	}
	err := s.App.RunInTransaction(func(app core.App) error {
		c, err := app.FindCollectionByNameOrId("predictions")
		if err != nil {
			return err
		}
		for _, it := range changed {
			r, e := app.FindFirstRecordByData("predictions", "key", it.key)
			if e != nil {
				if !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				r = core.NewRecord(c)
			}
			r.Set("key", it.key)
			r.Set("device_sn", sn)
			r.Set("ts", hour)
			r.Set("payload", map[string]any{"ts": hour, "made_at": hour, "target": it.target, "predicted_soc": it.soc})
			if e := app.Save(r); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(items), nil
}

type AccuracyOptions struct {
	Now                        int64
	MaxAgeDays, Limit          int
	SinceMadeAt                int64
	MainCapacity, PackCapacity float64
}

type socSample struct {
	ts  float64
	soc float64
}
type packPoint struct {
	ts    float64
	sumRB float64
	count int
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
	var predRows []struct {
		Payload string `db:"payload"`
	}
	e := s.App.DB().NewQuery(`SELECT payload FROM predictions
 WHERE device_sn = {:sn}
   AND ts >= {:low}
   AND json_extract(payload, '$.target') IS NOT NULL
   AND CAST(json_extract(payload, '$.target') AS REAL) <= {:now}
   AND CAST(json_extract(payload, '$.made_at') AS REAL) >= {:low}
 ORDER BY CAST(json_extract(payload, '$.target') AS REAL) DESC, id ASC
 LIMIT {:limit}`).Bind(dbx.Params{"sn": sn, "low": float64(low), "now": float64(o.Now), "limit": o.Limit}).All(&predRows)
	if e != nil {
		return nil, e
	}
	if len(predRows) == 0 {
		return []map[string]any{}, nil
	}
	eligible := make([]map[string]any, 0, len(predRows))
	lowTarget, highTarget := math.Inf(1), math.Inf(-1)
	for _, row := range predRows {
		var p map[string]any
		if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
			return nil, err
		}
		v := forecast.Number(p, "target")
		lowTarget = math.Min(lowTarget, v)
		highTarget = math.Max(highTarget, v)
		eligible = append(eligible, p)
	}
	samples, e := s.socSamplesRange(sn, lowTarget-1800, highTarget+1800)
	if e != nil {
		return nil, e
	}
	var packs []packPoint
	if o.MainCapacity > 0 && o.PackCapacity > 0 {
		packs, e = s.packPointsRange(sn, lowTarget-1800, highTarget+1800)
		if e != nil {
			return nil, e
		}
	}
	out := []map[string]any{}
	for _, p := range eligible {
		target := forecast.Number(p, "target")
		actual, ok := socAtIndexed(samples, packs, target, o.MainCapacity, o.PackCapacity)
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

func (s *Store) socSamplesRange(sn string, low, high float64) ([]socSample, error) {
	var rows []struct {
		TS  float64 `db:"eff_ts"`
		SOC float64 `db:"soc"`
	}
	// Extract numeric SOC and effective timestamp directly in SQLite to avoid
	// decoding full telemetry payloads in Go.
	err := s.App.DB().NewQuery(`SELECT
  CAST(COALESCE(json_extract(payload, '$.bucket'), ts) AS REAL) AS eff_ts,
  CAST(COALESCE(json_extract(payload, '$.last_battery_pct'), json_extract(payload, '$.battery_pct'), json_extract(payload, '$.battery_percent')) AS REAL) AS soc
 FROM samples
 WHERE device_sn = {:sn}
   AND ts >= {:low}
   AND ts < {:high}
   AND COALESCE(json_extract(payload, '$.last_battery_pct'), json_extract(payload, '$.battery_pct'), json_extract(payload, '$.battery_percent')) IS NOT NULL
 ORDER BY eff_ts`).Bind(dbx.Params{"sn": sn, "low": low, "high": high}).All(&rows)
	if err != nil {
		return nil, err
	}
	out := make([]socSample, len(rows))
	for i, r := range rows {
		out[i] = socSample{ts: r.TS, soc: r.SOC}
	}
	return out, nil
}

func (s *Store) packPointsRange(sn string, low, high float64) ([]packPoint, error) {
	var rows []struct {
		TS      float64 `db:"ts"`
		Payload string  `db:"payload"`
	}
	err := s.App.DB().NewQuery(`SELECT ts, payload FROM battery_packs
 WHERE device_sn = {:sn} AND ts >= {:low} AND ts < {:high}
 ORDER BY ts`).Bind(dbx.Params{"sn": sn, "low": low, "high": high}).All(&rows)
	if err != nil {
		return nil, err
	}
	out := make([]packPoint, 0, len(rows))
	for _, r := range rows {
		var p map[string]any
		if json.Unmarshal([]byte(r.Payload), &p) != nil || p["packs"] == nil {
			continue
		}
		items, ok := p["packs"].([]any)
		if !ok {
			continue
		}
		sumRB, count := 0.0, 0
		for _, item := range items {
			pm, ok := item.(map[string]any)
			if ok && pm["rb"] != nil {
				sumRB += forecast.Number(pm, "rb")
				count++
			}
		}
		if len(out) > 0 && out[len(out)-1].ts == r.TS {
			out[len(out)-1].sumRB += sumRB
			out[len(out)-1].count += count
		} else {
			out = append(out, packPoint{ts: r.TS, sumRB: sumRB, count: count})
		}
	}
	return out, nil
}

func socAt(samples, packs []map[string]any, target, mainCapacity, packCapacity float64) (float64, bool) {
	ss := make([]socSample, 0, len(samples))
	for _, s := range samples {
		ts := forecast.Number(s, "ts")
		if s["bucket"] != nil {
			ts = forecast.Number(s, "bucket")
		}
		for _, key := range []string{"last_battery_pct", "battery_pct", "battery_percent"} {
			if s[key] != nil {
				ss = append(ss, socSample{ts: ts, soc: forecast.Number(s, key)})
				break
			}
		}
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].ts < ss[j].ts })
	byTS := map[float64]*packPoint{}
	for _, p := range packs {
		if p["packs"] == nil {
			continue
		}
		items, ok := p["packs"].([]any)
		if !ok {
			continue
		}
		ts := forecast.Number(p, "ts")
		pt := byTS[ts]
		if pt == nil {
			pt = &packPoint{ts: ts}
			byTS[ts] = pt
		}
		for _, item := range items {
			pm, ok := item.(map[string]any)
			if ok && pm["rb"] != nil {
				pt.sumRB += forecast.Number(pm, "rb")
				pt.count++
			}
		}
	}
	pp := make([]packPoint, 0, len(byTS))
	for _, pt := range byTS {
		pp = append(pp, *pt)
	}
	sort.Slice(pp, func(i, j int) bool { return pp[i].ts < pp[j].ts })
	return socAtIndexed(ss, pp, target, mainCapacity, packCapacity)
}

func socAtIndexed(samples []socSample, packs []packPoint, target, mainCapacity, packCapacity float64) (float64, bool) {
	start := sort.Search(len(samples), func(i int) bool { return samples[i].ts >= target-1800 })
	total, n := 0.0, 0
	for i := start; i < len(samples) && samples[i].ts < target+1800; i++ {
		total += samples[i].soc
		n++
	}
	if n == 0 {
		return 0, false
	}
	actual := total / float64(n)
	if mainCapacity <= 0 || packCapacity <= 0 || len(packs) == 0 {
		return actual, true
	}
	idx := sort.Search(len(packs), func(i int) bool { return packs[i].ts >= target })
	best := -1
	dist := math.Inf(1)
	for _, i := range []int{idx - 1, idx} {
		if i < 0 || i >= len(packs) {
			continue
		}
		ts := packs[i].ts
		if ts < target-1800 || ts >= target+1800 {
			continue
		}
		d := math.Abs(ts - target)
		if d < dist || (d == dist && (best < 0 || ts > packs[best].ts)) {
			best = i
			dist = d
		}
	}
	if best < 0 {
		return actual, true
	}
	weighted := actual*mainCapacity + packs[best].sumRB*packCapacity
	capacity := mainCapacity + float64(packs[best].count)*packCapacity
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
	samples, e := s.socSamplesRange(sn, target-1800, target+1800)
	if e != nil {
		return nil, e
	}
	var packs []packPoint
	if mainCapacity > 0 && packCapacity > 0 {
		packs, e = s.packPointsRange(sn, target-1800, target+1800)
		if e != nil {
			return nil, e
		}
	}
	v, ok := socAtIndexed(samples, packs, target, mainCapacity, packCapacity)
	if !ok {
		return nil, nil
	}
	return &v, nil
}

func (s *Store) BackfillDailyActuals(sn string, now int64, days int, mainCapacity, packCapacity float64) (int, error) {
	rows, e := s.DailySummary(sn, now, days)
	if e != nil {
		return 0, e
	}
	needsWork := false
	for _, r := range rows {
		for _, event := range []string{"sunset", "sunrise"} {
			ts := forecast.Number(r, event+"_ts")
			if r["actual_"+event+"_soc_pct"] == nil && ts > 0 && ts <= float64(now) {
				needsWork = true
				break
			}
		}
		if needsWork {
			break
		}
	}
	if !needsWork {
		return 0, nil
	}
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
