package weatherstore

import (
	"database/sql"
	"errors"
	"github.com/compdani/jackery-monitor/internal/forecast"
)

func plane(raw map[string]any) (forecast.Plane, bool) {
	for _, k := range []string{"declination", "azimuth", "kwp"} {
		if raw[k] == nil {
			return forecast.Plane{}, false
		}
	}
	return forecast.ValidatePlane(forecast.Number(raw, "declination"), forecast.Number(raw, "azimuth"), forecast.Number(raw, "kwp"))
}
func (p PocketWeatherStore) Plane(sn string) (forecast.Plane, bool, error) {
	raw, err := p.Store.Get("settings", "solar_plane:"+sn)
	if err == nil {
		v, ok := plane(raw)
		return v, ok, nil
	}
	if !missing(err) {
		return forecast.Plane{}, false, err
	}
	raw, err = p.Store.Get("settings", "solar_array")
	if missing(err) {
		return forecast.Plane{}, false, nil
	}
	if err != nil {
		return forecast.Plane{}, false, err
	}
	if v, ok := plane(raw); ok {
		return v, true, nil
	}
	if row, ok := raw[sn].(map[string]any); ok {
		if v, valid := plane(row); valid {
			return v, true, nil
		}
	}
	row, _ := raw["_default"].(map[string]any)
	v, ok := plane(row)
	return v, ok, nil
}
func (p PocketWeatherStore) SavePlane(sn string, dec, az, kwp float64) (forecast.Plane, error) {
	v, ok := forecast.ValidatePlane(dec, az, kwp)
	if !ok || sn == "" {
		return forecast.Plane{}, errors.New("invalid device or solar plane")
	}
	err := p.Store.Put("settings", "solar_plane:"+sn, sn, map[string]any{"declination": v.Declination, "azimuth": v.Azimuth, "kwp": v.KWP})
	return v, err
}

func missing(err error) bool { return errors.Is(err, sql.ErrNoRows) }
