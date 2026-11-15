package weatherstore

import (
	"encoding/json"
	"fmt"
	. "github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/store"
)

// PocketWeatherStore stores only Go-owned records; no legacy files are opened.
type PocketWeatherStore struct{ Store *store.Store }

func (p PocketWeatherStore) SaveWeather(key string, w Weather) error {
	for _, r := range w.Hourly {
		if Number(r, "ts") <= float64(w.FetchedAt) {
			payload := map[string]any{}
			for k, v := range r {
				payload[k] = v
			}
			if err := p.Store.Put("weather_observations", fmt.Sprintf("weather:%s:%d", key, int64(Number(r, "ts"))), key, payload); err != nil {
				return err
			}
		}
	}
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err = json.Unmarshal(b, &payload); err != nil {
		return err
	}
	return p.Store.Put("settings", "weather_cache:"+key, "", payload)
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
