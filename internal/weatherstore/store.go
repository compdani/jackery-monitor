package weatherstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	. "github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

// PocketWeatherStore stores only Go-owned records; no legacy files are opened.
type PocketWeatherStore struct{ Store *store.Store }

// SaveWeather writes past observations and the cached forecast in a single
// transaction, skipping observation rows whose payload is already identical.
func (p PocketWeatherStore) SaveWeather(key string, w Weather) error {
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	var cachePayload map[string]any
	if err = json.Unmarshal(b, &cachePayload); err != nil {
		return err
	}
	return p.Store.App.RunInTransaction(func(app core.App) error {
		tx := store.Store{App: app}
		obsCol, err := app.FindCollectionByNameOrId("weather_observations")
		if err != nil {
			return err
		}
		minTS := float64(w.FetchedAt - 16*86400)
		var existingRows []struct {
			Key     string `db:"key"`
			Payload string `db:"payload"`
		}
		if err := app.DB().NewQuery(`SELECT key, payload FROM weather_observations WHERE device_sn = {:key} AND ts >= {:min}`).
			Bind(dbx.Params{"key": key, "min": minTS}).All(&existingRows); err != nil {
			return err
		}
		existing := make(map[string]map[string]any, len(existingRows))
		for _, row := range existingRows {
			var m map[string]any
			if json.Unmarshal([]byte(row.Payload), &m) == nil {
				existing[row.Key] = m
			}
		}
		for _, r := range w.Hourly {
			ts := Number(r, "ts")
			if ts > float64(w.FetchedAt) {
				continue
			}
			obsKey := fmt.Sprintf("weather:%s:%d", key, int64(ts))
			payload := map[string]any{}
			for k, v := range r {
				payload[k] = v
			}
			if prev, ok := existing[obsKey]; ok && reflect.DeepEqual(prev, payload) {
				continue
			}
			rec, e := app.FindFirstRecordByData("weather_observations", "key", obsKey)
			if e != nil {
				if !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				rec = core.NewRecord(obsCol)
			}
			rec.Set("key", obsKey)
			rec.Set("device_sn", key)
			rec.Set("ts", ts)
			rec.Set("payload", payload)
			if e := app.Save(rec); e != nil {
				return e
			}
		}
		return tx.Put("settings", "weather_cache:"+key, "", cachePayload)
	})
}

func (p PocketWeatherStore) LoadWeather(key string) (Weather, error) {
	var w Weather
	payload, err := p.Store.Get("settings", "weather_cache:"+key)
	if err != nil {
		return w, err
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return w, err
	}
	err = json.Unmarshal(b, &w)
	return w, err
}

func (p PocketWeatherStore) Observations(key string, since int64) ([]Row, error) {
	rows, err := p.Store.List("weather_observations", key, float64(since), 0)
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, Row(r))
	}
	return out, err
}

const solarHistoryRetentionDays = 14

type SolarSite struct {
	Lat, Lon float64
	Plane
}

func (site SolarSite) historyKey(sn string) string {
	config := fmt.Sprintf("%g:%g:%d:%d:%g", site.Lat, site.Lon, site.Declination, site.Azimuth, site.KWP)
	return fmt.Sprintf("fs_history:%s:%x", sn, sha256.Sum256([]byte(config)))
}

// SaveSolarHistory merges Forecast.Solar hourly watts into a compact 14-day
// rolling map scoped to the device, coordinates and array.
func (p PocketWeatherStore) SaveSolarHistory(sn string, site SolarSite, hourly []Row, now int64) error {
	if sn == "" || len(hourly) == 0 {
		return nil
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	cutoff := now - solarHistoryRetentionDays*86400
	key := site.historyKey(sn)
	return p.Store.App.RunInTransaction(func(app core.App) error {
		tx := store.Store{App: app}
		existing, err := tx.Get("forecast_models", key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		merged := map[string]any{}
		changed := false
		if raw, ok := existing["hours"].(map[string]any); ok {
			for k, v := range raw {
				if ts, e := strconv.ParseInt(k, 10, 64); e == nil && ts >= cutoff {
					merged[k] = v
				} else {
					changed = true
				}
			}
		}
		for _, r := range hourly {
			ts := int64(Number(r, "ts"))
			if ts < cutoff {
				continue
			}
			w := Number(r, "watts")
			k := strconv.FormatInt(ts, 10)
			if prev, ok := merged[k]; !ok || Number(map[string]any{"v": prev}, "v") != w {
				merged[k] = w
				changed = true
			}
		}
		if !changed && existing != nil {
			return nil
		}
		return tx.Put("forecast_models", key, sn, map[string]any{"ts": float64(now), "hours": merged})
	})
}

// LoadSolarHistory returns persisted Forecast.Solar hourly estimates since `since`.
func (p PocketWeatherStore) LoadSolarHistory(sn string, site SolarSite, since int64) (map[int64]float64, error) {
	out := map[int64]float64{}
	if sn == "" {
		return out, nil
	}
	row, err := p.Store.Get("forecast_models", site.historyKey(sn))
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	raw, _ := row["hours"].(map[string]any)
	for k, v := range raw {
		if ts, e := strconv.ParseInt(k, 10, 64); e == nil && ts >= since {
			out[ts] = Number(map[string]any{"v": v}, "v")
		}
	}
	return out, nil
}
