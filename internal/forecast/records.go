package forecast

import (
	"sort"
	"time"

	"jackery-monitor/internal/energy"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// Record keeps the first prediction in each hour; viewing the tab cannot rewrite history.
func Record(app core.App, sn string, now time.Time, hours []Row, zone *time.Location) error {
	return app.RunInTransaction(func(tx core.App) error {
		made := now.Unix() / 3600 * 3600
		previous, err := energy.Query(tx, `SELECT COUNT(*) n FROM forecast_predictions WHERE device_sn={:sn} AND made_at={:made}`, dbx.Params{"sn": sn, "made": made})
		if err != nil {
			return err
		}
		if num(previous[0], "n") == 0 {
			for _, h := range hours {
				if _, err := tx.DB().NewQuery(`INSERT INTO forecast_predictions(device_sn,made_at,target,predicted_soc) VALUES({:sn},{:made},{:target},{:soc}) ON CONFLICT DO NOTHING`).Bind(dbx.Params{"sn": sn, "made": made, "target": int64(num(h, "ts")), "soc": num(h, "predicted_soc")}).Execute(); err != nil {
					return err
				}
			}
			if _, err := tx.DB().NewQuery(`INSERT INTO forecast_model_runs(device_sn,made_at,version) VALUES({:sn},{:made},{:version}) ON CONFLICT DO NOTHING`).Bind(dbx.Params{"sn": sn, "made": made, "version": ModelVersion}).Execute(); err != nil {
				return err
			}
		}
		// Daylight transitions use GHI so a device without panels still has sunrise/sunset.
		var sunset *Row
		for i := 1; i < len(hours); i++ {
			prev, h := hours[i-1], hours[i]
			if num(prev, "ghi_w_m2") > 0 && num(h, "ghi_w_m2") == 0 {
				p := prev
				sunset = &p
			}
			if sunset != nil && num(prev, "ghi_w_m2") == 0 && num(h, "ghi_w_m2") > 0 {
				date := time.Unix(int64(num(*sunset, "ts")), 0).In(zone).Format("2006-01-02")
				_, err := tx.DB().NewQuery(`INSERT INTO daily_solar_summary(date,device_sn,sunset_ts,sunrise_ts,predicted_sunset_soc_pct,predicted_sunrise_soc_pct,updated_at,predictions_made_at) VALUES({:date},{:sn},{:sunset},{:sunrise},{:pset},{:prise},{:now},{:now}) ON CONFLICT(date,device_sn) DO NOTHING`).Bind(dbx.Params{"date": date, "sn": sn, "sunset": int64(num(*sunset, "ts")), "sunrise": int64(num(h, "ts")), "pset": num(*sunset, "predicted_soc"), "prise": num(prev, "predicted_soc"), "now": now.Unix()}).Execute()
				if err != nil {
					return err
				}
				sunset = nil
			}
		}
		return nil
	})
}
func actual(rows []Row, target int64) *float64 {
	sum, n := 0.0, 0
	start := sort.Search(len(rows), func(i int) bool { return int64(num(rows[i], "ts")) >= target-1800 })
	for _, r := range rows[start:] {
		if int64(num(r, "ts")) >= target+1800 {
			break
		}
		if s := soc(r); s != nil {
			sum += *s
			n++
		}
	}

	if n == 0 {
		return nil
	}
	v := sum / float64(n)
	return &v
}
func Accuracy(store *energy.Store, sn string, now time.Time) (Row, error) {
	predictions, err := energy.Query(store.App, `SELECT p.*,m.version FROM forecast_predictions p LEFT JOIN forecast_model_runs m ON p.device_sn=m.device_sn AND p.made_at=m.made_at WHERE p.device_sn={:sn} AND p.target>={:since} AND p.target<={:now} ORDER BY p.target,p.made_at`, dbx.Params{"sn": sn, "since": now.Unix() - 14*86400, "now": now.Unix() - 1800})
	if err != nil {
		return nil, err
	}
	history, err := store.History(sn, 14*24+1, 300, now)
	if err != nil {
		return nil, err
	}
	samples := []Row{}
	summary, current := map[string]Row{}, map[string]Row{}
	add := func(dst map[string]Row, key string, err float64) {
		b := dst[key]
		if b == nil {
			b = Row{"n": 0, "sum": 0.0, "signed": 0.0}
			dst[key] = b
		}
		b["n"] = int(num(b, "n")) + 1
		b["sum"] = num(b, "sum") + abs(err)
		b["signed"] = num(b, "signed") + err
	}
	for _, p := range predictions {
		target := int64(num(p, "target"))
		s := actual(history, target)
		if s == nil {
			continue
		}
		lead := (num(p, "target") - num(p, "made_at")) / 3600
		e := num(p, "predicted_soc") - *s
		key := "≤6h"
		if lead > 72 {
			key = ">72h"
		} else if lead > 24 {
			key = "≤72h"
		} else if lead > 6 {
			key = "≤24h"
		}
		add(summary, key, e)
		if p["version"] == ModelVersion {
			add(current, key, e)
		}
		samples = append(samples, Row{"made_at": p["made_at"], "target": target, "predicted_soc": p["predicted_soc"], "actual_soc": round(*s, 10), "lead_time_h": lead, "error": round(abs(e), 100)})
	}
	for _, s := range []map[string]Row{summary, current} {
		for _, b := range s {
			b["mae"] = round(num(b, "sum")/num(b, "n"), 100)
			b["bias_pp"] = round(num(b, "signed")/num(b, "n"), 100)
			delete(b, "sum")
			delete(b, "signed")
		}
	}
	return Row{"device_sn": sn, "samples": samples, "summary": summary, "summary_post_fix": current, "model_version": ModelVersion}, nil
}
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
func Daily(store *energy.Store, sn string, days int, now time.Time) ([]Row, error) {
	rows, err := energy.Query(store.App, `SELECT * FROM daily_solar_summary WHERE device_sn={:sn} AND date>={:date} ORDER BY date DESC`, dbx.Params{"sn": sn, "date": now.AddDate(0, 0, -days).Format("2006-01-02")})
	if err != nil {
		return nil, err
	}
	history, err := store.History(sn, (days+2)*24, 300, now)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		for _, phase := range []string{"sunset", "sunrise"} {
			key := "actual_" + phase + "_soc_pct"
			target := int64(num(r, phase+"_ts"))
			if r[key] != nil || target <= 0 || target > now.Unix()-1800 {
				continue
			}
			value := actual(history, target)
			if value == nil {
				continue
			}
			if _, err := store.App.DB().NewQuery(`UPDATE daily_solar_summary SET ` + key + `={:value},updated_at={:now} WHERE date={:date} AND device_sn={:sn}`).Bind(dbx.Params{"value": *value, "now": now.Unix(), "date": r["date"], "sn": sn}).Execute(); err != nil {
				return nil, err
			}
			r[key] = *value
		}
	}
	return rows, nil
}
