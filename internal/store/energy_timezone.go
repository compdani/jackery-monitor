package store

import (
	"github.com/pocketbase/dbx"
	"math"
	"sort"
	"time"
)

// Split numeric summaries by UTC quarter-hour, then apply the location's offset
// at each timestamp. This preserves modern hour/half-hour/quarter-hour timezone
// boundaries and DST transitions without reading raw telemetry.
func (s *Store) zonedEnergySummary(sn string, since float64) ([]energySummary, error) {
	if err := s.EnsureEnergyRollups(sn); err != nil {
		return nil, err
	}
	var rows []energySummary
	query := `SELECT min(ts) AS ts,` + summaryColumns + `,max(solar_w) AS peak_solar_w,max(output_w) AS peak_output_w,min(CASE WHEN has_soc THEN soc END) AS min_soc,max(CASE WHEN has_soc THEN soc END) AS max_soc
 FROM energy_minutes WHERE device_sn={:sn} AND ts>={:since} GROUP BY cast(ts/900 AS INTEGER) ORDER BY ts`
	err := s.App.DB().NewQuery(query).Bind(dbx.Params{"sn": sn, "since": since}).All(&rows)
	return rows, err
}
func (s *Store) EnergySummaryAtLocation(sn string, since float64, loc *time.Location) ([]map[string]any, error) {
	rows, err := s.zonedEnergySummary(sn, since)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, r := range rows {
		_, offset := time.Unix(int64(r.TS), 0).In(loc).Zone()
		out = append(out, map[string]any{"ts": r.TS, "input_wh": r.Input, "output_wh": r.Output, "solar_wh": r.Solar, "ac_input_wh": r.AC, "solar_charge_diverted_wh": r.Diverted, "utc_offset_s": float64(offset)})
	}
	return out, nil
}
func (s *Store) EnergyDailyAtLocation(sn string, since float64, loc *time.Location) ([]map[string]any, error) {
	rows, err := s.zonedEnergySummary(sn, since)
	if err != nil {
		return nil, err
	}
	days := map[string]energySummary{}
	for _, r := range rows {
		day := time.Unix(int64(r.TS), 0).In(loc).Format("2006-01-02")
		v := days[day]
		v.Input += r.Input
		v.Output += r.Output
		v.Solar += r.Solar
		v.AC += r.AC
		v.Diverted += r.Diverted
		v.PeakSolar = math.Max(v.PeakSolar, r.PeakSolar)
		v.PeakOutput = math.Max(v.PeakOutput, r.PeakOutput)
		if r.MinSOC != nil && (v.MinSOC == nil || *r.MinSOC < *v.MinSOC) {
			v.MinSOC = r.MinSOC
		}
		if r.MaxSOC != nil && (v.MaxSOC == nil || *r.MaxSOC > *v.MaxSOC) {
			v.MaxSOC = r.MaxSOC
		}
		days[day] = v
	}
	keys := []string{}
	for day := range days {
		keys = append(keys, day)
	}
	sort.Strings(keys)
	out := []map[string]any{}
	for _, day := range keys {
		r := days[day]
		v := map[string]any{"date": day, "peak_solar_w": r.PeakSolar, "peak_output_w": r.PeakOutput, "min_soc": nil, "max_soc": nil}
		if r.MinSOC != nil {
			v["min_soc"] = *r.MinSOC
		}
		if r.MaxSOC != nil {
			v["max_soc"] = *r.MaxSOC
		}
		for key, n := range map[string]float64{"charged_kwh": r.Input, "consumed_kwh": r.Output, "solar_kwh": r.Solar, "grid_kwh": r.AC, "diverted_kwh": r.Diverted} {
			v[key] = math.RoundToEven(n/10) / 100
		}
		out = append(out, v)
	}
	return out, nil
}
