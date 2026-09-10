// Package energy stores minute buckets alongside PocketBase's collections.
package energy

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"sync"
	"time"

	catalog "jackery-monitor"

	"github.com/pocketbase/pocketbase/core"

	"jackery-monitor/internal/jackery"

	"github.com/pocketbase/dbx"
)

//go:embed schema.sql
var Schema string

type Reading struct {
	TS, Input, Output, Solar, Grid, Diverted float64
	SOC, SystemSOC                           *float64
}
type Store struct {
	App      core.App
	mu       sync.Mutex
	last     map[string]Reading
	recentMu sync.Mutex
	recent   map[string]historyCache
}

func New(app core.App) *Store { return &Store{App: app, last: map[string]Reading{}} }
func Number(v any) float64 {
	switch v := v.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}
func Nullable(v any) *float64 {
	if v == nil {
		return nil
	}
	n := Number(v)
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return &n
}
func Query(app core.App, query string, params dbx.Params) ([]map[string]any, error) {
	rows, err := app.DB().NewQuery(query).Bind(params).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return ReadRows(rows.Rows)
}
func ReadRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(cols))
		refs := make([]any, len(cols))
		for i := range values {
			refs[i] = &values[i]
		}
		if err := rows.Scan(refs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, key := range cols {
			v := values[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[key] = v
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
func (s *Store) UpsertDevice(d jackery.Device, now time.Time) error {
	_, err := s.App.DB().NewQuery(`INSERT INTO energy_devices(device_sn,name,model_code,model_name,first_seen,last_seen) VALUES ({:sn},{:name},{:code},{:model},{:ts},{:ts}) ON CONFLICT(device_sn) DO UPDATE SET name=excluded.name,model_code=excluded.model_code,model_name=excluded.model_name,last_seen=excluded.last_seen`).Bind(dbx.Params{"sn": d.SN, "name": d.Name, "code": d.ModelCode, "model": d.ModelName, "ts": now.Unix()}).Execute()
	return err
}
func (s *Store) Record(sn string, r Reading) error {
	if sn == "" || r.TS <= 0 {
		return errors.New("device serial and positive timestamp required")
	}
	for _, v := range []float64{r.TS, r.Input, r.Output, r.Solar, r.Grid, r.Diverted} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("non-finite energy reading")
		}
	}
	r.Input = max(0, r.Input)
	r.Output = max(0, r.Output)
	r.Solar = max(0, r.Solar)
	r.Grid = max(0, r.Grid)
	r.Diverted = min(r.Output, max(0, r.Diverted))
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.last[sn]
	dt := r.TS - previous.TS
	if exists && dt <= 0 {
		return nil
	}
	integrate := exists && dt <= 600
	wh := func(a, b float64) float64 {
		if !integrate {
			return 0
		}
		return (a + b) / 2 * dt / 3600
	}
	in, out, solar, grid, div := wh(previous.Input, r.Input), wh(previous.Output, r.Output), wh(previous.Solar, r.Solar), wh(previous.Grid, r.Grid), wh(previous.Diverted, r.Diverted)
	_, err := s.App.DB().NewQuery(`INSERT INTO samples(device_sn,bucket,input_wh,output_wh,solar_wh,ac_input_wh,solar_charge_diverted_wh,last_input_w,last_output_w,last_solar_w,last_ac_input_w,last_battery_pct,last_system_soc,sample_count)
 VALUES ({:sn},{:bucket},{:in},{:out},{:solar},{:grid},{:div},{:iw},{:ow},{:sw},{:gw},{:soc},{:system},1)
 ON CONFLICT(device_sn,bucket) DO UPDATE SET input_wh=input_wh+excluded.input_wh,output_wh=output_wh+excluded.output_wh,solar_wh=solar_wh+excluded.solar_wh,ac_input_wh=ac_input_wh+excluded.ac_input_wh,solar_charge_diverted_wh=solar_charge_diverted_wh+excluded.solar_charge_diverted_wh,last_input_w=excluded.last_input_w,last_output_w=excluded.last_output_w,last_solar_w=excluded.last_solar_w,last_ac_input_w=excluded.last_ac_input_w,last_battery_pct=COALESCE(excluded.last_battery_pct,last_battery_pct),last_system_soc=excluded.last_system_soc,sample_count=sample_count+1`).Bind(dbx.Params{"sn": sn, "bucket": int64(r.TS) / 60 * 60, "in": in, "out": out, "solar": solar, "grid": grid, "div": min(div, out), "iw": r.Input, "ow": r.Output, "sw": r.Solar, "gw": r.Grid, "soc": r.SOC, "system": r.SystemSOC}).Execute()
	if err == nil {
		s.last[sn] = r
	}
	return err
}
func (s *Store) Devices() ([]map[string]any, error) {
	return Query(s.App, `SELECT * FROM energy_devices ORDER BY last_seen DESC,device_sn`, nil)
}
func (s *Store) Totals(sn string, now time.Time) (map[string]any, error) {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	result := map[string]any{"device_sn": sn}
	for name, since := range map[string]int64{"lifetime": 0, "today": midnight, "last_7d": now.Add(-7 * 24 * time.Hour).Unix(), "last_30d": now.Add(-30 * 24 * time.Hour).Unix()} {
		rows, err := Query(s.App, `SELECT COALESCE(SUM(input_wh),0) input_wh,COALESCE(SUM(output_wh),0) output_wh,COALESCE(SUM(solar_wh),0) solar_wh,COALESCE(SUM(ac_input_wh),0) ac_input_wh,COALESCE(SUM(solar_charge_diverted_wh),0) solar_charge_diverted_wh FROM samples WHERE device_sn={:sn} AND bucket>={:since}`, dbx.Params{"sn": sn, "since": since})
		if err != nil {
			return nil, err
		}
		if name != "lifetime" {
			rows[0]["since"] = since
		}
		result[name] = rows[0]
	}
	return result, nil
}
func (s *Store) History(sn string, hours, bucket int, now time.Time) ([]map[string]any, error) {
	bucket = max(60, bucket)
	rows, err := Query(s.App, `SELECT (bucket/{:size})*{:size} ts,SUM(input_wh) input_wh,SUM(output_wh) output_wh,SUM(solar_wh) solar_wh,SUM(ac_input_wh) ac_input_wh,SUM(solar_charge_diverted_wh) solar_charge_diverted_wh,CAST(AVG(last_input_w) AS INTEGER) input_w,CAST(AVG(last_output_w) AS INTEGER) output_w,CAST(AVG(last_solar_w) AS INTEGER) solar_w,CAST(AVG(last_ac_input_w) AS INTEGER) ac_input_w,CAST(AVG(last_battery_pct) AS INTEGER) battery_pct,AVG(last_system_soc) system_soc FROM samples WHERE device_sn={:sn} AND bucket>={:since} GROUP BY ts ORDER BY ts`, dbx.Params{"size": bucket, "sn": sn, "since": now.Unix() - int64(hours)*3600})
	if err != nil {
		return nil, err
	}
	meta, err := Query(s.App, `SELECT model_code FROM energy_devices WHERE device_sn={:sn}`, dbx.Params{"sn": sn})
	if err != nil {
		return nil, err
	}
	code := 0
	if len(meta) > 0 {
		code = int(Number(meta[0]["model_code"]))
	}
	main, pack, _ := catalog.Capacity(code)

	for _, row := range rows {
		if row["system_soc"] != nil || row["battery_pct"] == nil {
			continue
		}
		target := int64(Number(row["ts"]))
		nearest, err := Query(s.App, `SELECT COALESCE(SUM(soc_pct),0) soc_sum,COUNT(*) n FROM battery_packs WHERE parent_sn={:sn} AND soc_pct BETWEEN 0 AND 100 AND ts=(SELECT ts FROM battery_packs WHERE parent_sn={:sn} AND ts>={:lo} AND ts<{:hi} AND soc_pct BETWEEN 0 AND 100 GROUP BY ts ORDER BY ABS(ts-{:target}),ts DESC LIMIT 1)`, dbx.Params{"sn": sn, "lo": target - 1800, "hi": target + 1800, "target": target})
		if err != nil {
			return nil, err
		}
		count := int(Number(nearest[0]["n"]))
		if count > 0 {
			row["system_soc"] = (Number(row["battery_pct"])*float64(main) + Number(nearest[0]["soc_sum"])*float64(pack)) / float64(main+count*pack)
		}
	}

	fractions, err := Query(s.App, `SELECT (decided_at/{:size})*{:size} ts,AVG(CASE WHEN plug_state_before='on' THEN 1.0 ELSE 0.0 END) fraction FROM solar_charge_decisions WHERE device_sn={:sn} AND decided_at>={:since} GROUP BY ts`, dbx.Params{"size": bucket, "sn": sn, "since": now.Unix() - int64(hours)*3600})
	if err != nil {
		return nil, err
	}
	byTS := map[int64]float64{}
	for _, r := range fractions {
		byTS[int64(Number(r["ts"]))] = Number(r["fraction"])
	}
	for _, r := range rows {
		if v, ok := byTS[int64(Number(r["ts"]))]; ok {
			r["solar_charge_plug_on_frac"] = v
		}
	}

	return rows, nil
}
func (s *Store) Daily(sn string, days, offset int, now time.Time) ([]map[string]any, error) {
	return Query(s.App, `SELECT date(bucket+{:offset},'unixepoch') date,ROUND(SUM(solar_wh)/1000,2) solar_kwh,ROUND(SUM(output_wh)/1000,2) consumed_kwh,ROUND(SUM(input_wh)/1000,2) charged_kwh,ROUND(SUM(ac_input_wh)/1000,2) grid_kwh,ROUND(SUM(solar_charge_diverted_wh)/1000,2) diverted_kwh,MAX(last_solar_w) peak_solar_w,MAX(last_output_w) peak_output_w,MIN(last_battery_pct) min_soc,MAX(last_battery_pct) max_soc FROM samples WHERE device_sn={:sn} AND bucket>={:since} GROUP BY date ORDER BY date`, dbx.Params{"offset": offset, "sn": sn, "since": now.Unix() - int64(days)*86400})
}
func (s *Store) SetCapacity(sn string, value *int) error {
	if value != nil && (*value < 500 || *value > 200000) {
		return errors.New("capacity_wh must be 500..200000 or null")
	}
	result, err := s.App.DB().NewQuery(`UPDATE energy_devices SET capacity_wh_override={:value} WHERE device_sn={:sn}`).Bind(dbx.Params{"sn": sn, "value": value}).Execute()
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return errors.New("unknown device serial")
	}
	return nil
}
func (s *Store) RecordPacks(sn string, packs []map[string]any, ts int64) error {
	raw, err := json.Marshal(packs)
	if err != nil {
		return err
	}
	return s.App.RunInTransaction(func(tx core.App) error {
		var latest struct{ TS int64 }
		err := tx.DB().NewQuery(`SELECT ts FROM pack_snapshots WHERE parent_sn={:sn}`).Bind(dbx.Params{"sn": sn}).One(&latest)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if latest.TS >= ts {
			return nil
		}
		for _, p := range packs {
			_, err := tx.DB().NewQuery(`INSERT OR REPLACE INTO battery_packs(ts,parent_sn,pack_sn,device_order,soc_pct,input_w,output_w,internal_temp_c,error_code) VALUES ({:ts},{:parent},{:sn},{:order},{:soc},{:in},{:out},{:temp},{:error})`).Bind(dbx.Params{"ts": ts, "parent": sn, "sn": p["deviceSn"], "order": Number(p["deviceOrder"]), "soc": p["rb"], "in": p["ip"], "out": p["op"], "temp": p["it"], "error": p["ec"]}).Execute()
			if err != nil {
				return err
			}
		}
		_, err = tx.DB().NewQuery(`INSERT INTO pack_snapshots(parent_sn,ts,items) VALUES ({:sn},{:ts},{:items}) ON CONFLICT(parent_sn) DO UPDATE SET ts=excluded.ts,items=excluded.items`).Bind(dbx.Params{"sn": sn, "ts": ts, "items": string(raw)}).Execute()
		return err
	})
}
func (s *Store) Packs(sn string) ([]map[string]any, int64, error) {
	rows, err := Query(s.App, `SELECT ts,items FROM pack_snapshots WHERE parent_sn={:sn}`, dbx.Params{"sn": sn})
	if err != nil {
		return nil, 0, err
	}
	if len(rows) > 0 {
		var packs []map[string]any
		if err = json.Unmarshal([]byte(rows[0]["items"].(string)), &packs); err != nil {
			return nil, 0, err
		}
		return packs, int64(Number(rows[0]["ts"])), nil
	}
	packs, err := Query(s.App, `SELECT pack_sn deviceSn,parent_sn parentDeviceSn,device_order deviceOrder,soc_pct rb,input_w ip,output_w op,internal_temp_c it,error_code ec,ts FROM battery_packs WHERE parent_sn={:sn} AND ts=(SELECT MAX(ts) FROM battery_packs WHERE parent_sn={:sn}) ORDER BY device_order`, dbx.Params{"sn": sn})
	var ts int64
	if len(packs) > 0 {
		ts = int64(Number(packs[0]["ts"]))
	}
	return packs, ts, err
}

type historyCache struct {
	at   time.Time
	rows []map[string]any
}

// RecentHistory amortizes legacy pack/SOC joins for high-frequency WS snapshots.
// Explicit Energy-tab queries still read fresh data via History.
func (s *Store) RecentHistory(sn string, now time.Time) ([]map[string]any, error) {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if s.recent == nil {
		s.recent = map[string]historyCache{}
	}
	if cached, ok := s.recent[sn]; ok && now.Sub(cached.at) < 30*time.Second {
		return cached.rows, nil
	}
	rows, err := s.History(sn, 6, 60, now)
	if err != nil {
		return nil, err
	}
	s.recent[sn] = historyCache{now, rows}
	return rows, nil
}
