package store

import (
	"database/sql"
	"errors"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"math"
	"time"
)

// EnsureEnergyRollups materializes once, then reconciles only ranges dirtied by
// sample writes. Dirty markers and samples share a transaction, as do rebuilds
// and marker removal, so updates cannot be lost during reconciliation.
func missing(err error) bool { return errors.Is(err, sql.ErrNoRows) }

const EnergyRollupVersion = 4

var ErrEnergyBuilding = errors.New("energy history is being prepared")

func rollupsReady(row map[string]any) bool {
	return row != nil && num(row["version"]) == EnergyRollupVersion && row["ready"] == true
}

// One call performs at most one UTC day of work. Cursor state survives restart;
// callers can expose progress while the lifecycle worker advances remaining days.
func (s *Store) EnsureEnergyRollups(sn string) error {
	readyKey, dirtyKey := "energy_ready:"+sn, "energy_dirty:"+sn
	ready, err := s.Get("settings", readyKey)
	if err != nil && !missing(err) {
		return err
	}
	dirty, err := s.Get("settings", dirtyKey)
	if err != nil && !missing(err) {
		return err
	}
	if rollupsReady(ready) && dirty == nil {
		return nil
	}
	building := false
	err = s.App.RunInTransaction(func(app core.App) error {
		tx := &Store{App: app}
		ready, err := tx.Get("settings", readyKey)
		if err != nil && !missing(err) {
			return err
		}
		dirty, err := tx.Get("settings", dirtyKey)
		if err != nil && !missing(err) {
			return err
		}
		if rollupsReady(ready) && dirty == nil {
			return nil
		}
		if ready == nil || num(ready["version"]) != EnergyRollupVersion {
			var bounds struct {
				First *float64 `db:"first"`
				Last  *float64 `db:"last"`
			}
			err := app.DB().NewQuery(`SELECT min(first) AS first,max(last) AS last FROM (
 SELECT min(ts) AS first,max(ts) AS last FROM samples WHERE device_sn={:sn}
 UNION ALL SELECT min(ts) AS first,max(ts) AS last FROM energy_minutes WHERE device_sn={:sn})`).Bind(dbx.Params{"sn": sn}).One(&bounds)
			if err != nil {
				return err
			}
			first, last := 0.0, 0.0
			if bounds.First != nil {
				first = math.Floor(math.Max(0, *bounds.First)/86400) * 86400
				last = math.Ceil((*bounds.Last+1800)/86400) * 86400
			}
			ready = map[string]any{"version": EnergyRollupVersion, "cursor": first, "until": last, "ready": first >= last}
			// Everything committed before this point is covered by the initial cursor.
			if dirty != nil {
				if err := tx.Delete("settings", dirtyKey); err != nil {
					return err
				}
				dirty = nil
			}
		}
		initial := !rollupsReady(ready)
		since, until := 0.0, 0.0
		if initial {
			since = num(ready["cursor"])
			until = math.Min(num(ready["until"]), since+86400)
		} else if dirty != nil {
			since = math.Floor(num(dirty["since"])/60) * 60
			until = math.Min(math.Ceil(num(dirty["until"])/60)*60, math.Floor(since/86400)*86400+86400)
		}
		if until > since {
			minutes, err := tx.energyMinutesRange(sn, since, until)
			if err != nil {
				return err
			}
			_, err = app.DB().NewQuery("DELETE FROM energy_minutes WHERE device_sn={:sn} AND ts>={:since} AND ts<{:until}").Bind(dbx.Params{"sn": sn, "since": since, "until": until}).Execute()
			if err != nil {
				return err
			}
			for _, row := range minutes {
				params := dbx.Params{
					"id": ID()[:15], "sn": sn, "ts": num(row["ts"]),
					"input": num(row["input_wh"]), "output": num(row["output_wh"]),
					"solar": num(row["solar_wh"]), "ac": num(row["ac_input_wh"]),
					"diverted": num(row["solar_charge_diverted_wh"]), "coverage": num(row["solar_coverage_s"]),
				}
				for column, field := range minuteTelemetry {
					params[column] = num(row[field])
					params["has_"+column] = row[field] != nil
				}
				_, err := app.DB().NewQuery(`INSERT INTO energy_minutes (
    id,device_sn,ts,input_wh,output_wh,solar_wh,ac_input_wh,solar_charge_diverted_wh,solar_coverage_s,
    input_w,output_w,solar_w,ac_input_w,soc,system_soc,has_input_w,has_output_w,has_solar_w,has_ac_input_w,has_soc,has_system_soc)
    VALUES ({:id},{:sn},{:ts},{:input},{:output},{:solar},{:ac},{:diverted},{:coverage},
    {:input_w},{:output_w},{:solar_w},{:ac_input_w},{:soc},{:system_soc},{:has_input_w},{:has_output_w},{:has_solar_w},{:has_ac_input_w},{:has_soc},{:has_system_soc})`).Bind(params).Execute()
				if err != nil {
					return err
				}
			}
		}
		if initial {
			ready["cursor"] = until
			ready["ready"] = until >= num(ready["until"])
		} else if dirty != nil {
			if until < num(dirty["until"]) {
				dirty["since"] = until
				if err := tx.Put("settings", dirtyKey, sn, dirty); err != nil {
					return err
				}
			} else {
				if err := tx.Delete("settings", dirtyKey); err != nil {
					return err
				}
				dirty = nil
			}
		}
		ready["ts"] = float64(time.Now().Unix())
		if err := tx.Put("settings", readyKey, sn, ready); err != nil {
			return err
		}
		building = !rollupsReady(ready) || dirty != nil
		return nil
	})
	if err != nil {
		return err
	}
	if building {
		return ErrEnergyBuilding
	}
	return nil
}

var minuteTelemetry = map[string]string{
	"input_w": "last_input_w", "output_w": "last_output_w", "solar_w": "last_solar_w",
	"ac_input_w": "last_ac_input_w", "soc": "last_battery_pct", "system_soc": "system_soc",
}

// Small typed SQL rows avoid decoding telemetry JSON during stats requests.
type energySummary struct {
	TS         float64  `db:"ts"`
	Input      float64  `db:"input_wh"`
	Output     float64  `db:"output_wh"`
	Solar      float64  `db:"solar_wh"`
	AC         float64  `db:"ac_input_wh"`
	Diverted   float64  `db:"solar_charge_diverted_wh"`
	PeakSolar  float64  `db:"peak_solar_w"`
	PeakOutput float64  `db:"peak_output_w"`
	MinSOC     *float64 `db:"min_soc"`
	MaxSOC     *float64 `db:"max_soc"`
}

const summaryColumns = `sum(input_wh) AS input_wh,sum(output_wh) AS output_wh,sum(solar_wh) AS solar_wh,sum(ac_input_wh) AS ac_input_wh,sum(solar_charge_diverted_wh) AS solar_charge_diverted_wh`

// EnergySummary returns hourly energy for exact window cutoffs and TOU savings.
// SQL scans indexed numeric rollups; Go receives at most 24 rows per day.
func (s *Store) EnergySummary(sn string, since float64, offset int) ([]map[string]any, error) {
	if err := s.EnsureEnergyRollups(sn); err != nil {
		return nil, err
	}
	var rows []energySummary
	err := s.App.DB().NewQuery("SELECT min(ts) AS ts," + summaryColumns + ` FROM energy_minutes WHERE device_sn={:sn} AND ts>={:since}
 GROUP BY cast((ts+{:offset})/3600 AS INTEGER) ORDER BY ts`).Bind(dbx.Params{"sn": sn, "since": since, "offset": offset}).All(&rows)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range rows {
		out = append(out, map[string]any{"ts": r.TS, "input_wh": r.Input, "output_wh": r.Output, "solar_wh": r.Solar, "ac_input_wh": r.AC, "solar_charge_diverted_wh": r.Diverted})
	}
	return out, nil
}

func (s *Store) EnergyDailySummary(sn string, since float64, offset int) ([]map[string]any, error) {
	if err := s.EnsureEnergyRollups(sn); err != nil {
		return nil, err
	}
	var rows []energySummary
	// The view is the UTC daily projection; non-UTC day boundaries use the same
	// numeric rollup table with the requested offset, preserving local dates.
	query := `SELECT cast((ts+{:offset})/86400 AS INTEGER)*86400-{:offset} AS ts,` + summaryColumns + `,max(solar_w) AS peak_solar_w,max(output_w) AS peak_output_w,min(CASE WHEN has_soc THEN soc END) AS min_soc,max(CASE WHEN has_soc THEN soc END) AS max_soc
 FROM energy_minutes WHERE device_sn={:sn} AND ts>={:since} GROUP BY cast((ts+{:offset})/86400 AS INTEGER) ORDER BY ts`
	if offset == 0 && math.Mod(since, 86400) == 0 {
		query = `SELECT * FROM energy_daily_stats WHERE device_sn={:sn} AND ts>={:since} ORDER BY ts`
	}
	err := s.App.DB().NewQuery(query).Bind(dbx.Params{"sn": sn, "since": since, "offset": offset}).All(&rows)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range rows {
		row := map[string]any{"date": time.Unix(int64(r.TS)+int64(offset), 0).UTC().Format("2006-01-02"), "peak_solar_w": r.PeakSolar, "peak_output_w": r.PeakOutput, "min_soc": nil, "max_soc": nil}
		if r.MinSOC != nil {
			row["min_soc"] = *r.MinSOC
		}
		if r.MaxSOC != nil {
			row["max_soc"] = *r.MaxSOC
		}
		for key, value := range map[string]float64{"charged_kwh": r.Input, "consumed_kwh": r.Output, "solar_kwh": r.Solar, "grid_kwh": r.AC, "diverted_kwh": r.Diverted} {
			row[key] = math.RoundToEven(value/10) / 100
		}
		out = append(out, row)
	}
	return out, nil
}

type energyHourlyRow struct {
	TS         float64  `db:"ts"`
	Input      float64  `db:"input_wh"`
	Output     float64  `db:"output_wh"`
	Solar      float64  `db:"solar_wh"`
	AC         float64  `db:"ac_input_wh"`
	Diverted   float64  `db:"solar_charge_diverted_wh"`
	AvgSolarW  float64  `db:"solar_w"`
	AvgOutputW float64  `db:"output_w"`
	AvgInputW  float64  `db:"input_w"`
	AvgACW     float64  `db:"ac_input_w"`
	AvgSOC     *float64 `db:"battery_pct"`
	SystemSOC  *float64 `db:"system_soc"`
	Coverage   float64  `db:"solar_coverage_s"`
}

// EnergyHourlyFromRollups reads hourly history directly from the indexed
// numeric energy_minutes table, avoiding raw sample JSON decoding. Returns
// ErrEnergyBuilding when initial rollups are not yet materialized.
func (s *Store) EnergyHourlyFromRollups(sn string, since float64) ([]map[string]any, error) {
	if err := s.EnsureEnergyRollups(sn); err != nil {
		return nil, err
	}
	var rows []energyHourlyRow
	query := `SELECT cast(ts/3600 AS INTEGER)*3600 AS ts,` + summaryColumns + `,
	 coalesce(avg(CASE WHEN has_solar_w THEN solar_w END), 0) AS solar_w,
	 coalesce(avg(CASE WHEN has_output_w THEN output_w END), 0) AS output_w,
	 coalesce(avg(CASE WHEN has_input_w THEN input_w END), 0) AS input_w,
	 coalesce(avg(CASE WHEN has_ac_input_w THEN ac_input_w END), 0) AS ac_input_w,
	 avg(CASE WHEN has_soc THEN soc END) AS battery_pct,
	 avg(CASE WHEN has_system_soc THEN system_soc END) AS system_soc,
	 sum(solar_coverage_s) AS solar_coverage_s
 FROM energy_minutes WHERE device_sn={:sn} AND ts>={:since}
 GROUP BY cast(ts/3600 AS INTEGER) ORDER BY ts`
	if err := s.App.DB().NewQuery(query).Bind(dbx.Params{"sn": sn, "since": since}).All(&rows); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		var soc any
		if r.AvgSOC != nil {
			soc = *r.AvgSOC
		}
		var systemSOC any
		if r.SystemSOC != nil {
			systemSOC = *r.SystemSOC
		}
		out = append(out, map[string]any{
			"ts":                       r.TS,
			"input_wh":                 r.Input,
			"output_wh":                r.Output,
			"solar_wh":                 r.Solar,
			"ac_input_wh":              r.AC,
			"solar_charge_diverted_wh": r.Diverted,
			"input_w":                  r.AvgInputW,
			"output_w":                 r.AvgOutputW,
			"solar_w":                  math.Max(r.AvgSolarW, r.Solar),
			"ac_input_w":               r.AvgACW,
			"battery_pct":              soc,
			"system_soc":               systemSOC,
			"solar_coverage_s":         r.Coverage,
		})
	}
	return out, nil
}

// SolarSinceFromRollups sums solar_wh from energy_minutes since a timestamp.
func (s *Store) SolarSinceFromRollups(sn string, since float64) (float64, error) {
	if err := s.EnsureEnergyRollups(sn); err != nil {
		return 0, err
	}
	var row struct {
		Solar float64 `db:"solar_wh"`
	}
	err := s.App.DB().NewQuery(`SELECT COALESCE(sum(solar_wh), 0) AS solar_wh FROM energy_minutes WHERE device_sn={:sn} AND ts>={:since}`).
		Bind(dbx.Params{"sn": sn, "since": since}).One(&row)
	return row.Solar, err
}
