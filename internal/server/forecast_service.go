package server

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/compdani/jackery-monitor/internal/weatherstore"
)

// forecastRuntime coordinates the shared weather clients and caches API responses.
type forecastRuntime struct {
	once       sync.Once
	fetch      sync.Mutex
	locationMu sync.Mutex
	cacheMu    sync.Mutex
	cache      map[forecastCacheKey]*forecastCacheEntry
}

type forecastCacheKey struct {
	serial  string
	preview bool
}

type forecastCacheEntry struct {
	ready   chan struct{}
	expires time.Time
	value   map[string]any
}

const forecastCacheTTL = 60 * time.Second

func (s *Server) invalidateForecastCache() {
	s.forecasts.cacheMu.Lock()
	s.forecasts.cache = nil
	s.forecasts.cacheMu.Unlock()
}

// cachedForecast coalesces concurrent /api/forecast reads for the same device
// and reuses recent responses for 60s so tab switches don't recompute or write.
func (s *Server) cachedForecast(ctx context.Context, sn string, preview bool) map[string]any {
	resolvedSN := sn
	if resolvedSN == "" {
		resolvedSN = s.activeSerial()
	}
	if resolvedSN == "" {
		return s.buildForecastWithMode(ctx, sn, time.Now().Unix(), true, preview)
	}
	key := forecastCacheKey{serial: resolvedSN, preview: preview}
	s.forecasts.cacheMu.Lock()
	if s.forecasts.cache == nil {
		s.forecasts.cache = map[forecastCacheKey]*forecastCacheEntry{}
	}
	now := time.Now()
	if preview {
		full := s.forecasts.cache[forecastCacheKey{serial: resolvedSN}]
		if full != nil && now.Before(full.expires) {
			s.forecasts.cacheMu.Unlock()
			return full.value
		}
	}
	entry := s.forecasts.cache[key]
	leader := entry == nil || (!entry.expires.IsZero() && !now.Before(entry.expires))
	if leader {
		entry = &forecastCacheEntry{ready: make(chan struct{})}
		s.forecasts.cache[key] = entry
	}
	s.forecasts.cacheMu.Unlock()
	if leader {
		val := s.buildForecastWithMode(ctx, resolvedSN, now.Unix(), true, preview)
		s.forecasts.cacheMu.Lock()
		entry.value = val
		if val["error"] == nil && val["ready"] == true {
			entry.expires = time.Now().Add(forecastCacheTTL)
		} else if s.forecasts.cache[key] == entry {
			delete(s.forecasts.cache, key)
		}
		s.forecasts.cacheMu.Unlock()
		close(entry.ready)
		return val
	}
	select {
	case <-entry.ready:
		return entry.value
	case <-ctx.Done():
		return map[string]any{"error": "request canceled", "configured": true}
	}
}

var errNoLocation = errors.New("location not set")

func (s *Server) forecastClients() (*forecast.WeatherClient, *forecast.SolarClient) {
	s.forecasts.once.Do(func() {
		if s.Weather == nil {
			s.Weather = &forecast.WeatherClient{}
		}
		if s.Weather.Store == nil {
			s.Weather.Store = weatherstore.PocketWeatherStore{Store: s.Store}
		}
		if s.SolarForecast == nil {
			s.SolarForecast = &forecast.SolarClient{}
		}
	})
	return s.Weather, s.SolarForecast
}
func (s *Server) weatherStore() weatherstore.PocketWeatherStore {
	return weatherstore.PocketWeatherStore{Store: s.Store}
}

// savedLocation returns the decrypted location and whether it holds usable
// coordinates (location.py's _validate rules).
func (s *Server) savedLocation() (map[string]any, bool, error) {
	v, err := s.Store.Secret(s.Key, "location")
	if isMissing(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if v["latitude"] == nil || v["longitude"] == nil {
		return v, false, nil
	}
	return v, forecast.ValidLocation(forecast.Number(v, "latitude"), forecast.Number(v, "longitude")), nil
}

// weatherNow fetches irradiance for the saved location. Successful live
// responses merge Open-Meteo's timezone into the location record, matching
// location.update_timezone.
func (s *Server) weatherNow(ctx context.Context) (forecast.Weather, error) {
	loc, ok, err := s.savedLocation()
	if err != nil {
		return forecast.Weather{}, err
	}
	if !ok {
		return forecast.Weather{}, errNoLocation
	}
	return s.weatherAt(ctx, loc)
}

func (s *Server) weatherAt(ctx context.Context, loc map[string]any) (forecast.Weather, error) {
	client, _ := s.forecastClients()
	s.forecasts.fetch.Lock()
	client.Offset = int(forecast.Number(loc, "utc_offset_seconds"))
	w, err := client.Fetch(ctx, forecast.Number(loc, "latitude"), forecast.Number(loc, "longitude"))
	s.forecasts.fetch.Unlock()
	if err == nil && !w.Synthetic && !w.Stale && w.Timezone != "" && (loc["utc_offset_seconds"] == nil || int(forecast.Number(loc, "utc_offset_seconds")) != w.Offset || str(loc, "timezone") != w.Timezone) {
		if e := s.updateLocationMetadata(loc, map[string]any{"utc_offset_seconds": w.Offset, "timezone": w.Timezone}); e != nil {
			s.Store.App.Logger().Warn("location timezone update", "error", e)
		}
	}
	return w, err
}

func (s *Server) updateLocationMetadata(original, fields map[string]any) error {
	s.forecasts.locationMu.Lock()
	defer s.forecasts.locationMu.Unlock()
	current, ok, err := s.savedLocation()
	if err != nil || !ok {
		return err
	}
	for _, key := range []string{"latitude", "longitude", "updated_at"} {
		if forecast.Number(current, key) != forecast.Number(original, key) {
			return nil
		}
	}
	for key, value := range fields {
		current[key] = value
	}
	return s.Store.SaveSecret(s.Key, "location", current)
}

func (s *Server) activeSerial() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, _ := s.snapshot["device"].(map[string]any)
	return str(d, "device_sn")
}
func (s *Server) packCount(sn string) int {
	n, err := s.Store.LatestPackCount(sn)
	if err != nil {
		return 0
	}
	return n
}

func (s *Server) applyStoredParamOverrides(sn string, opts *forecast.BuildOptions) {
	read := func(key string) *float64 {
		row, err := s.storedParam(sn, key)
		if err != nil || row == nil || row["value"] == nil {
			return nil
		}
		v := forecast.Number(row, "value")
		return &v
	}
	opts.SolarCoeffOverride = read("solar_coefficient")
	opts.ParasiticOverride = read("parasitic_w")
	opts.OverheadOverride = read("inverter_overhead_pct")
	opts.ChargeEffOverride = read("charge_efficiency")
}
func (s *Server) carLoad(sn string) float64 {
	c, err := s.solarConfig(sn)
	if err != nil || forecast.Number(c, "car_load_w") <= 0 {
		return 1400
	}
	return forecast.Number(c, "car_load_w")
}

// buildForecast mirrors server.py's _build_and_record_forecast: one path for
// /api/forecast and the hourly recorder, so both persist identical rows.
func (s *Server) buildForecast(ctx context.Context, sn string, now int64, record bool) map[string]any {
	return s.buildForecastWithMode(ctx, sn, now, record, false)
}

func (s *Server) buildForecastWithMode(ctx context.Context, sn string, now int64, record, preview bool) map[string]any {
	loc, ok, err := s.savedLocation()
	if err != nil {
		return map[string]any{"error": "location unavailable", "error_detail": err.Error(), "configured": false}
	}
	if !ok {
		return map[string]any{"error": "location not set", "configured": false}
	}
	if sn == "" {
		sn = s.activeSerial()
	}
	if sn == "" {
		return map[string]any{"error": "no active device", "configured": true}
	}
	lat, lon := forecast.Number(loc, "latitude"), forecast.Number(loc, "longitude")
	savedOffset := int(forecast.Number(loc, "utc_offset_seconds"))
	plane, hasPlane, err := s.weatherStore().Plane(sn)
	if err != nil {
		return map[string]any{"error": "solar array unavailable", "error_detail": err.Error(), "configured": true}
	}
	apiKey, keyErr := s.weatherStore().SolarKey(s.Key)

	// Fetch weather, 14-day energy history, and Forecast.Solar in parallel.
	var (
		wx         forecast.Weather
		wxErr      error
		history    []forecast.Row
		capacity   float64
		soc        *float64
		histErr    error
		est        forecast.SolarEstimate
		solarReady bool
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if s.SmartForecast != nil {
			wx.Hourly, wxErr = s.SmartForecast.Weather(ctx, sn, now)
		} else {
			wx, wxErr = s.weatherAt(ctx, loc)
		}
	}()
	go func() {
		defer wg.Done()
		history, capacity, soc, histErr = s.energyInputs(sn, now, 14*24)
	}()
	if !preview && hasPlane && loc["utc_offset_seconds"] != nil {
		solarReady = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, solar := s.forecastClients()
			est = solar.Fetch(ctx, lat, lon, int(plane.Declination), int(plane.Azimuth), plane.KWP, savedOffset, apiKey)
		}()
	}
	wg.Wait()

	if wxErr != nil || len(wx.Hourly) == 0 {
		detail := wx.Error
		if wxErr != nil {
			detail = wxErr.Error()
		}
		return map[string]any{"error": "Weather service unreachable (Open-Meteo)", "error_detail": detail, "configured": true}
	}
	wx.Hourly = slices.Clone(wx.Hourly)
	sort.Slice(wx.Hourly, func(i, j int) bool { return forecast.Number(wx.Hourly[i], "ts") < forecast.Number(wx.Hourly[j], "ts") })
	offset := wx.Offset
	if wx.Timezone == "" && offset == 0 {
		offset = savedOffset
	}
	if histErr != nil {
		return map[string]any{"error": "history unavailable", "error_detail": histErr.Error(), "configured": true}
	}
	capacity = s.resolvedCapacity(sn, capacity)
	mainSOC := 50.0
	if cached, e := s.Store.Get("devices", sn); e == nil {
		if t, ok := cached["telemetry"].(map[string]any); ok && t["battery_percent"] != nil {
			mainSOC = forecast.Number(t, "battery_percent")
		}
	}
	start := mainSOC
	if soc != nil {
		start = *soc
	}
	if seed, seedCap := s.siseliSeed(sn); seed != nil || seedCap > 0 {
		if seed != nil {
			start = *seed
		}
		if cached, _ := s.Store.Get("devices", sn); seedCap > 0 && forecast.Number(cached, "capacity_wh_override") <= 0 {
			capacity = max(capacity, seedCap)
		}
	}
	fsMeta := map[string]any{"configured": false}
	var planeOut any
	var planeOpt *forecast.Plane
	solarHourly := map[int64]float64{}
	var solarHistory map[int64]float64
	if hasPlane {
		pCopy := plane
		planeOpt = &pCopy
		planeOut = map[string]any{"declination": plane.Declination, "azimuth": plane.Azimuth, "kwp": plane.KWP}
		fsMeta["configured"] = true
		if preview {
			_, solar := s.forecastClients()
			var cached bool
			est, cached = solar.Cached(lat, lon, plane.Declination, plane.Azimuth, plane.KWP, offset)
			fsMeta["pending"] = !cached
		} else if !solarReady || offset != savedOffset {
			_, solar := s.forecastClients()
			est = solar.Fetch(ctx, lat, lon, int(plane.Declination), int(plane.Azimuth), plane.KWP, offset, apiKey)
		}
		fsMeta["error"] = nil
		if est.Error != "" {
			fsMeta["error"] = est.Error
		}
		fsMeta["stale"] = est.Stale
		for _, r := range est.Hourly {
			solarHourly[int64(forecast.Number(r, "ts"))] = forecast.Number(r, "watts")
		}
		site := weatherstore.SolarSite{Lat: lat, Lon: lon, Plane: plane}
		if !preview && !est.Stale && len(est.Hourly) > 0 {
			if e := s.weatherStore().SaveSolarHistory(sn, site, est.Hourly, now); e != nil {
				s.Store.App.Logger().Debug("save solar history", "device", sn, "error", e)
			}
		}
		if sh, e := s.weatherStore().LoadSolarHistory(sn, site, now-14*86400); e == nil {
			solarHistory = sh
		}
	}
	sched, err := s.loadSchedule(sn)
	if err != nil {
		return map[string]any{"error": "load schedule unavailable", "error_detail": err.Error(), "configured": true}
	}
	var windows []automation.Window
	data, _ := json.Marshal(sched["windows"])
	_ = json.Unmarshal(data, &windows)
	opts := forecast.BuildOptions{
		Now:            now,
		Offset:         offset,
		Packs:          s.packCount(sn),
		CarLoad:        s.carLoad(sn),
		Lat:            lat,
		Lon:            lon,
		Plane:          planeOpt,
		SolarHistory:   solarHistory,
		LoadMode:       str(sched, "mode"),
		Windows:        windows,
		SleepStart:     str(sched, "sleep_start"),
		SleepEnd:       str(sched, "sleep_end"),
		DisableCeiling: s.ecoflowBatteryWh(sn) > 0,
	}
	s.applyStoredParamOverrides(sn, &opts)
	if len(solarHourly) > 0 {
		opts.SolarHourly = solarHourly
	}
	result := forecast.Build(history, wx.Hourly, start, capacity, opts)
	if result["ready"] == true && record && fsMeta["pending"] != true {
		hours, _ := result["forecast"].([]forecast.Row)
		if _, e := s.Store.RecordForecast(sn, float64(now), hours); e != nil {
			s.Store.App.Logger().Warn("record forecast", "device", sn, "error", e)
		}
		if e := s.updateDailySummary(sn, hours, now, offset); e != nil {
			s.Store.App.Logger().Warn("daily summary", "device", sn, "error", e)
		}
	}
	today := 0.0
	startToday := float64((now+int64(offset))/86400*86400 - int64(offset))
	if v, e := s.Store.SolarSinceFromRollups(sn, startToday); e == nil {
		today = v
	} else if minutes, e := s.Store.EnergyMinutes(sn, startToday); e == nil {
		totals := store.EnergyTotals(minutes, float64(now), offset)
		if t, ok := totals["today"].(map[string]any); ok {
			today = forecast.Number(t, "solar_wh")
		}
	}
	threshold := any(20)
	if values, _, e := s.settingValues(); e == nil {
		threshold = values["low_battery_threshold"]
	}
	out := map[string]any{"device_sn": sn, "low_battery_threshold": threshold, "main_soc_pct": mainSOC, "system_soc_pct": start, "pack_count": opts.Packs, "today_actual_solar_wh": today, "weather_synthetic": wx.Synthetic, "weather_stale": wx.Stale, "forecast_solar": fsMeta, "solar_array": planeOut, "forecast_solar_has_key": keyErr == nil && apiKey != "", "load_schedule": sched}
	for k, v := range result {
		out[k] = v
	}
	out["configured"] = true
	return out
}

// sunPhases ports server.py's _find_sun_phases.
func sunPhases(hours []forecast.Row) (sunset, sunrise forecast.Row) {
	if len(hours) == 0 {
		return nil, nil
	}
	inDay := forecast.Number(hours[0], "solar_w") > 0
	for i, h := range hours {
		sun := forecast.Number(h, "solar_w") > 0
		if inDay && !sun && i > 0 {
			sunset = hours[i-1]
		}
		if !inDay && sun && i > 0 && sunrise == nil {
			sunrise = hours[i-1]
			break
		}
		inDay = sun
	}
	return sunset, sunrise
}

// updateDailySummary ports _update_daily_summary: today's predicted/actual
// sunset/sunrise row followed by a 14-day actual backfill.
func (s *Server) updateDailySummary(sn string, hours []forecast.Row, now int64, offset int) error {
	if len(hours) == 0 {
		return nil
	}
	set, rise := sunPhases(hours)
	main, pack, _, err := s.capacityHints(sn)
	if err != nil {
		return err
	}
	fields := map[string]any{}
	for name, e := range map[string]forecast.Row{"sunset": set, "sunrise": rise} {
		if e == nil {
			continue
		}
		ts := forecast.Number(e, "ts")
		fields[name+"_ts"] = ts
		fields["predicted_"+name+"_soc_pct"] = e["predicted_soc"]
		if ts <= float64(now) {
			v, err := s.Store.SystemSOCAt(sn, ts, main, pack)
			if err != nil {
				return err
			}
			if v != nil {
				fields["actual_"+name+"_soc_pct"] = *v
			}
		}
	}
	date := time.Unix(now+int64(offset), 0).UTC().Format("2006-01-02")
	if err = s.Store.UpsertDailySummary(sn, date, float64(now), fields); err != nil {
		return err
	}
	_, err = s.Store.BackfillDailyActuals(sn, now, 14, main, pack)
	return err
}

// recordAllForecasts ports _record_forecasts_for_all_devices; one failing
// device never prevents the others from recording.
func (s *Server) recordAllForecasts(ctx context.Context, now int64) (recorded, skipped int, err error) {
	if _, ok, e := s.savedLocation(); e != nil || !ok {
		return 0, 0, e
	}
	rows, err := s.Store.List("devices", "", 0, 0)
	if err != nil {
		return 0, 0, err
	}
	for _, row := range rows {
		d, _ := row["device"].(map[string]any)
		sn := str(d, "device_sn")
		if sn == "" {
			continue
		}
		res := s.buildForecast(ctx, sn, now, true)
		if hours, _ := res["forecast"].([]forecast.Row); res["ready"] == true && len(hours) > 0 {
			recorded++
		} else {
			skipped++
		}
	}
	return recorded, skipped, nil
}

// ForecastRecorderLoop records predictions hourly (90s after start) so
// accuracy analytics accumulate even when nobody opens the Forecast tab.
func (s *Server) ForecastRecorderLoop(ctx context.Context) {
	s.forecastRecorderLoop(ctx, 90*time.Second, time.Hour)
}
func (s *Server) forecastRecorderLoop(ctx context.Context, initial, interval time.Duration) {
	timer := time.NewTimer(initial)
	defer timer.Stop()
	bo := automation.LoopBackoff{Max: 4 * time.Hour}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		recorded, skipped, err := s.recordAllForecasts(ctx, time.Now().Unix())
		if err != nil {
			bo.RecordFailure()
			s.Store.App.Logger().Warn("forecast recorder", "error", err)
		} else {
			bo.Reset()
			s.Store.App.Logger().Debug("forecast recorder", "recorded", recorded, "skipped", skipped)
		}
		timer.Reset(bo.NextSleep(interval))
	}
}
