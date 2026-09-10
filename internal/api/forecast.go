package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"jackery-monitor/internal/cost"
	"jackery-monitor/internal/energy"
	"jackery-monitor/internal/forecast"
	"jackery-monitor/internal/jackery"
	"jackery-monitor/internal/live"

	"github.com/pocketbase/pocketbase/core"

	"jackery-monitor/internal/site"
	"jackery-monitor/internal/tunables"

	"github.com/pocketbase/dbx"
)

type forecaster struct {
	store   *energy.Store
	live    *live.Service
	weather *forecast.WeatherClient
}

func (f *forecaster) devices() []jackery.Device {
	snap := f.live.Snapshot("")
	cloud := snap["cloud"].(map[string]any)
	raw, _ := json.Marshal(cloud["devices"])
	var devices []jackery.Device
	_ = json.Unmarshal(raw, &devices)
	return devices
}
func (f *forecaster) selected(r *core.RequestEvent) string {
	sn := r.Request.URL.Query().Get("device_sn")
	if sn != "" {
		return sn
	}
	devices := f.devices()
	if len(devices) > 0 {
		return devices[0].SN
	}
	return ""
}
func (f *forecaster) build(ctx context.Context, sn string) (map[string]any, error) {
	l, err := site.Get(f.store.App)
	if err != nil {
		return nil, err
	}
	if !l.Configured() {
		return map[string]any{"configured": false, "ready": false, "forecast": []any{}}, nil
	}
	var target *jackery.Device
	for _, d := range f.devices() {
		if d.SN == sn {
			copy := d
			target = &copy
			break
		}
	}
	if target == nil {
		return map[string]any{"configured": true, "ready": false, "error": "No current telemetry for this device", "forecast": []any{}}, nil
	}
	snap := decorateEnergy(f.store, f.live.Snapshot(target.ID))
	tele, _ := snap["telemetry"].(map[string]any)
	if tele == nil || energy.Nullable(tele["system_soc_pct"]) == nil || time.Now().Unix()-int64(energy.Number(snap["last_update_ts"])) > 600 {
		return map[string]any{"configured": true, "ready": false, "error": "Waiting for fresh battery telemetry", "forecast": []any{}}, nil
	}
	now := time.Now()
	history, err := f.store.History(sn, 14*24, 3600, now)
	if err != nil {
		return nil, err
	}
	weather, err := f.weather.Fetch(ctx, now)
	if err != nil {
		return map[string]any{"configured": true, "ready": false, "error": err.Error(), "forecast": []any{}}, nil
	}
	zone := l.Zone()
	if weather.Timezone != "" {
		if z, e := time.LoadLocation(weather.Timezone); e == nil {
			zone = z
		}
	}
	packs, _, err := f.store.Packs(sn)
	if err != nil {
		return nil, err
	}
	result := forecast.Build(history, weather.Hourly, forecast.Options{Now: now, Zone: zone, Capacity: energy.Number(tele["capacity_wh"]), StartSOC: energy.Number(tele["system_soc_pct"]), PackCount: len(packs), CarLoad: historicalCarLoad(f.store.App, sn)})
	result["configured"] = true
	result["device_sn"] = sn
	result["main_soc_pct"] = tele["battery_percent"]
	result["system_soc_pct"] = tele["system_soc_pct"]
	result["weather_stale"] = weather.Stale
	result["weather_synthetic"] = weather.Synthetic
	result["weather_fetched_at"] = weather.Fetched
	result["weather_stale_age_s"] = weather.StaleAge
	zoneName := zone.String()
	if zoneName == "Local" || zoneName == "device" {
		zoneName = ""
	}
	result["timezone"] = zoneName
	_, offset := now.In(zone).Zone()
	result["utc_offset_seconds"] = offset
	result["low_battery_threshold"] = tunables.Value(f.store.App, "low_battery_threshold")
	totals, err := f.store.Totals(sn, now.In(zone))
	if err != nil {
		return nil, err
	}
	if today, ok := totals["today"].(map[string]any); ok {
		result["today_actual_solar_wh"] = today["solar_wh"]
	}
	if result["ready"] == true {
		if err = forecast.Record(f.store.App, sn, now, result["forecast"].([]forecast.Row), zone); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func registerForecast(f *forecaster, route func(string, string, func(*core.RequestEvent) error)) {
	route("GET", "/location", func(r *core.RequestEvent) error {
		l, err := site.Get(r.App)
		if err != nil {
			return err
		}
		return r.JSON(200, l)
	})
	route("POST", "/location", func(r *core.RequestEvent) error {
		var l site.Location
		if err := r.BindBody(&l); err != nil {
			return r.BadRequestError("Invalid location", err)
		}
		if err := l.Validate(); err != nil {
			return r.BadRequestError(err.Error(), nil)
		}
		if err := f.weather.ChangeLocation(l); err != nil {
			return err
		}
		saved, err := site.Get(r.App)
		if err != nil {
			return err
		}
		return r.JSON(200, saved)
	})
	route("GET", "/location/geocode", func(r *core.RequestEvent) error {
		count := 5
		if raw := r.Request.URL.Query().Get("count"); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil {
				return r.BadRequestError("count must be an integer", nil)
			}
			count = v
		}
		results, err := f.weather.Geocode(r.Request.Context(), r.Request.URL.Query().Get("q"), count)
		body := map[string]any{"results": results}
		if err != nil {
			body["error"] = err.Error()
		}
		return r.JSON(200, body)
	})
	route("GET", "/forecast", func(r *core.RequestEvent) error {
		result, err := f.build(r.Request.Context(), f.selected(r))
		if err != nil {
			return err
		}
		return r.JSON(200, result)
	})
	route("GET", "/forecast/accuracy", func(r *core.RequestEvent) error {
		result, err := forecast.Accuracy(f.store, f.selected(r), time.Now())
		if err != nil {
			return err
		}
		return r.JSON(200, result)
	})
	route("GET", "/daily_summary", func(r *core.RequestEvent) error {
		days := 7
		if raw := r.Request.URL.Query().Get("days"); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil {
				return r.BadRequestError("days must be an integer", nil)
			}
			days = min(90, max(1, v))
		}
		sn := f.selected(r)
		rows, err := forecast.Daily(f.store, sn, days, localTime(r.App))
		if err != nil {
			return err
		}
		return r.JSON(200, map[string]any{"device_sn": sn, "days": days, "rows": rows, "model_version": forecast.ModelVersion})
	})
	route("GET", "/cost/plan", func(r *core.RequestEvent) error {
		p, err := cost.Get(r.App)
		if err != nil {
			return err
		}
		return r.JSON(200, map[string]any{"plan": p, "presets": cost.Presets})
	})
	route("POST", "/cost/plan", func(r *core.RequestEvent) error {
		var p cost.Plan
		if err := r.BindBody(&p); err != nil {
			return r.BadRequestError("Invalid plan", err)
		}
		if err := p.Validate(); err != nil {
			return r.BadRequestError(err.Error(), nil)
		}
		if err := site.Write(r.App, "cost_plan", p); err != nil {
			return err
		}
		return r.JSON(200, map[string]any{"plan": p})
	})
	route("GET", "/cost/savings", func(r *core.RequestEvent) error {
		result, err := costSavings(f.store, f.selected(r))
		if err != nil {
			return err
		}
		return r.JSON(200, result)
	})
}
func costSavings(store *energy.Store, sn string) (map[string]any, error) {
	p, err := cost.Get(store.App)
	if err != nil {
		return nil, err
	}
	l, err := site.Get(store.App)
	if err != nil {
		return nil, err
	}
	now := time.Now().In(l.Zone())
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, l.Zone()).Unix()
	// Keep minute timestamps so fractional-hour zones and DST select the correct tariff.

	rows, err := store.App.DB().NewQuery(`SELECT bucket,output_wh,solar_wh,ac_input_wh FROM samples WHERE device_sn={:sn} ORDER BY bucket`).Bind(dbx.Params{"sn": sn}).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var today, lifetime cost.Accumulator
	for rows.Next() {
		var ts int64
		var output, solar, grid float64
		if err = rows.Rows.Scan(&ts, &output, &solar, &grid); err != nil {
			return nil, err
		}
		rate := cost.RateAt(p, time.Unix(ts, 0).In(l.Zone()))
		lifetime.Add(output, solar, grid, rate)
		if ts >= midnight {
			today.Add(output, solar, grid, rate)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"today_savings": today.Result(p.Currency), "lifetime_savings": lifetime.Result(p.Currency), "cost_plan": map[string]any{"type": p.Type, "currency": p.Currency}}, nil

}
func (f *forecaster) Run(ctx context.Context) {
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		for _, d := range f.devices() {
			if ctx.Err() != nil {
				return
			}
			_, err := f.build(ctx, d.SN)
			if err != nil && !errors.Is(err, context.Canceled) {
				f.store.App.Logger().Warn("forecast recorder failed", "error", err)
			}
			if _, err := forecast.Daily(f.store, d.SN, 7, localTime(f.store.App)); err != nil {
				f.store.App.Logger().Warn("daily forecast backfill failed", "error", err)
			}
		}
		timer.Reset(time.Hour)
	}
}

// Imported controller settings describe past loads even before Go can run the controller.
func historicalCarLoad(app core.App, sn string) float64 {
	r, err := app.FindFirstRecordByData("solar_charge_config", "key", "legacy")
	if err != nil {
		return 1400
	}
	var cfg map[string]any
	if r.UnmarshalJSONField("data", &cfg) != nil {
		return 1400
	}
	if byDevice, ok := cfg["by_device"].(map[string]any); ok {
		cfg, _ = byDevice[sn].(map[string]any)
	}
	if v := energy.Number(cfg["car_load_w"]); v >= 50 && v <= 7000 {
		return v
	}
	return 1400
}
