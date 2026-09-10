package forecast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"jackery-monitor/internal/energy"

	"jackery-monitor/internal/site"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

type WeatherHour struct {
	TS    int64   `json:"ts"`
	GHI   float64 `json:"ghi_w_m2"`
	Cloud float64 `json:"cloud_cover_pct"`
}
type Weather struct {
	Hourly    []WeatherHour `json:"hourly"`
	Fetched   int64         `json:"fetched_at"`
	Timezone  string        `json:"timezone"`
	Offset    int           `json:"utc_offset_seconds"`
	Stale     bool          `json:"stale"`
	Synthetic bool          `json:"synthetic"`
	StaleAge  int64         `json:"stale_age_s"`
}
type WeatherClient struct {
	App                     core.App
	HTTP                    *http.Client
	ForecastURL, GeocodeURL string
	mu                      sync.Mutex
	retryAt                 time.Time
}

func NewWeather(app core.App) *WeatherClient {
	return &WeatherClient{App: app, HTTP: &http.Client{Timeout: 15 * time.Second}, ForecastURL: "https://api.open-meteo.com/v1/forecast", GeocodeURL: "https://geocoding-api.open-meteo.com/v1/search"}
}
func (c *WeatherClient) get(ctx context.Context, endpoint string, params url.Values, dest any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("weather service unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("weather service returned HTTP %d", resp.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(dest); err != nil {
		return errors.New("invalid weather response")
	}
	return nil
}

// ChangeLocation atomically invalidates data from the previous installation.
func (c *WeatherClient) ChangeLocation(l site.Location) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := l.Validate(); err != nil {
		return err
	}
	l.Updated = float64(time.Now().UnixNano()) / 1e9
	err := c.App.RunInTransaction(func(tx core.App) error {
		if err := site.Save(tx, l); err != nil {
			return err
		}
		_, err := tx.DB().NewQuery(`DELETE FROM weather_observations; DELETE FROM weather_forecast; DELETE FROM weather_cache_meta;`).Execute()
		return err
	})
	if err == nil {
		c.retryAt = time.Time{}
	}
	return err
}
func (c *WeatherClient) Fetch(ctx context.Context, now time.Time) (Weather, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, err := site.Get(c.App)
	if err != nil {
		return Weather{}, err
	}
	if !l.Configured() {
		return Weather{}, errors.New("location not configured")
	}
	cached, err := c.cached(now, l)
	if err != nil {
		return Weather{}, err
	}
	if len(cached.Hourly) > 0 && ((cached.Fetched > now.Unix()-3600) || now.Before(c.retryAt)) {
		cached.Stale = cached.Fetched <= now.Unix()-3600
		return cached, nil
	}
	if now.Before(c.retryAt) {
		synth, e := c.synthetic(now, l)
		if e != nil {
			return Weather{}, e
		}
		if len(synth.Hourly) > 0 {
			return synth, nil
		}
		return Weather{}, errors.New("Weather service unavailable; retrying shortly")
	}
	var payload struct {
		Timezone string `json:"timezone"`
		Offset   int    `json:"utc_offset_seconds"`
		Hourly   struct {
			Times []int64    `json:"time"`
			GHI   []*float64 `json:"shortwave_radiation"`
			Cloud []*float64 `json:"cloud_cover"`
		} `json:"hourly"`
	}
	params := url.Values{"latitude": {strconv.FormatFloat(*l.Latitude, 'f', 6, 64)}, "longitude": {strconv.FormatFloat(*l.Longitude, 'f', 6, 64)}, "hourly": {"shortwave_radiation,cloud_cover"}, "past_days": {"14"}, "forecast_days": {"6"}, "timezone": {"auto"}, "timeformat": {"unixtime"}}
	err = c.get(ctx, c.ForecastURL, params, &payload)
	w := Weather{Fetched: now.Unix(), Timezone: payload.Timezone, Offset: payload.Offset, Hourly: []WeatherHour{}}
	if err == nil {
		h := payload.Hourly
		if len(h.Times) == 0 || len(h.Times) != len(h.GHI) || len(h.Times) != len(h.Cloud) {
			err = errors.New("incomplete weather response")
		} else {
			for i, ts := range h.Times {
				if h.GHI[i] == nil || h.Cloud[i] == nil {
					continue
				}
				if ts <= 0 || *h.GHI[i] < 0 || *h.Cloud[i] < 0 || *h.Cloud[i] > 100 || (i > 0 && ts <= h.Times[i-1]) {
					err = errors.New("invalid weather hourly values")
					break
				}
				w.Hourly = append(w.Hourly, WeatherHour{ts, *h.GHI[i], *h.Cloud[i]})
			}
			future := 0
			for _, r := range w.Hourly {
				if r.TS > now.Unix() {
					future++
				}
			}
			if future < 24 {
				err = errors.New("weather response lacks a complete forecast day")
			}
		}
	}
	if err != nil {
		c.retryAt = now.Add(5 * time.Minute)
		if len(cached.Hourly) > 0 {
			cached.Stale = true
			return cached, nil
		}
		synth, e := c.synthetic(now, l)
		if e != nil {
			return Weather{}, e
		}
		if len(synth.Hourly) > 0 {
			return synth, nil
		}
		return Weather{}, err
	}
	err = c.App.RunInTransaction(func(tx core.App) error {
		if _, e := tx.DB().NewQuery(`DELETE FROM weather_forecast`).Execute(); e != nil {
			return e
		}
		for _, r := range w.Hourly {
			query := `INSERT INTO weather_observations(ts,ghi_w_m2,cloud_cover_pct) VALUES({:ts},{:ghi},{:cloud}) ON CONFLICT(ts) DO UPDATE SET ghi_w_m2=excluded.ghi_w_m2,cloud_cover_pct=excluded.cloud_cover_pct`
			if r.TS > now.Unix() {
				query = `INSERT INTO weather_forecast(ts,ghi_w_m2,cloud_cover_pct,fetched_at) VALUES({:ts},{:ghi},{:cloud},{:now})`
			}
			if _, e := tx.DB().NewQuery(query).Bind(dbx.Params{"ts": r.TS, "ghi": r.GHI, "cloud": r.Cloud, "now": now.Unix()}).Execute(); e != nil {
				return e
			}
		}
		if _, e := tx.DB().NewQuery(`INSERT INTO weather_cache_meta(id,fetched_at) VALUES(1,{:now}) ON CONFLICT(id) DO UPDATE SET fetched_at=excluded.fetched_at`).Bind(dbx.Params{"now": now.Unix()}).Execute(); e != nil {
			return e
		}
		l.Timezone = w.Timezone
		l.Offset = &w.Offset
		return site.Save(tx, l)
	})
	return w, err
}
func (c *WeatherClient) cached(now time.Time, l site.Location) (Weather, error) {
	w := Weather{Hourly: []WeatherHour{}, Timezone: l.Timezone}
	_, w.Offset = now.In(l.Zone()).Zone()
	rows, err := energy.Query(c.App, `SELECT ts,ghi_w_m2,cloud_cover_pct,fetched_at FROM weather_forecast WHERE ts>{:now} ORDER BY ts`, dbx.Params{"now": now.Unix()})
	if err != nil || len(rows) == 0 {
		return w, err
	}
	for _, r := range rows {
		w.Hourly = append(w.Hourly, weatherRow(r))
		w.Fetched = max(w.Fetched, int64(energy.Number(r["fetched_at"])))
	}
	obs, err := c.observations(now)
	if err != nil {
		return w, err
	}
	w.Hourly = append(obs, w.Hourly...)
	w.StaleAge = max(0, now.Unix()-w.Fetched)
	return w, nil
}
func weatherRow(r map[string]any) WeatherHour {
	return WeatherHour{int64(energy.Number(r["ts"])), energy.Number(r["ghi_w_m2"]), energy.Number(r["cloud_cover_pct"])}
}
func (c *WeatherClient) observations(now time.Time) ([]WeatherHour, error) {
	rows, err := energy.Query(c.App, `SELECT ts,ghi_w_m2,cloud_cover_pct FROM weather_observations WHERE ts>={:since} AND ts<={:now} ORDER BY ts`, dbx.Params{"since": now.Unix() - 14*86400, "now": now.Unix()})
	out := []WeatherHour{}
	for _, r := range rows {
		out = append(out, weatherRow(r))
	}
	return out, err
}
func (c *WeatherClient) synthetic(now time.Time, l site.Location) (Weather, error) {
	obs, err := c.observations(now)
	w := Weather{Hourly: []WeatherHour{}, Stale: true, Synthetic: true, Timezone: l.Timezone}
	if err != nil {
		return w, err
	}
	var ghi, cloud [24][]float64
	for _, r := range obs {
		if r.TS < now.Unix()-7*86400 {
			continue
		}
		h := time.Unix(r.TS, 0).In(l.Zone()).Hour()
		ghi[h] = append(ghi[h], r.GHI)
		cloud[h] = append(cloud[h], r.Cloud)
		w.Fetched = max(w.Fetched, r.TS)
	}
	// Require two full diurnal cycles; missing nighttime/daytime hours aren't zeros.
	for h := range 24 {
		if len(ghi[h]) < 2 {
			return w, nil
		}
	}
	w.Hourly = obs
	_, w.Offset = now.In(l.Zone()).Zone()
	w.StaleAge = now.Unix() - w.Fetched
	for ts := (now.Unix()/3600 + 1) * 3600; ts <= now.Unix()+120*3600; ts += 3600 {
		h := time.Unix(ts, 0).In(l.Zone()).Hour()
		w.Hourly = append(w.Hourly, WeatherHour{ts, median(ghi[h]), median(cloud[h])})
	}
	return w, nil
}
func (c *WeatherClient) Geocode(ctx context.Context, q string, count int) ([]map[string]any, error) {
	out := []map[string]any{}
	q = strings.TrimSpace(q)
	if len(q) < 2 {
		return out, nil
	}
	if len(q) > 200 {
		return nil, errors.New("search is too long")
	}
	var payload struct {
		Results []map[string]any `json:"results"`
	}
	err := c.get(ctx, c.GeocodeURL, url.Values{"name": {q}, "count": {strconv.Itoa(min(10, max(1, count)))}, "language": {"en"}, "format": {"json"}}, &payload)
	if err != nil {
		return out, err
	}
	for _, r := range payload.Results {
		item := map[string]any{}
		for _, key := range []string{"name", "admin1", "country", "latitude", "longitude", "timezone"} {
			item[key] = r[key]
		}
		out = append(out, item)
	}
	return out, nil
}
