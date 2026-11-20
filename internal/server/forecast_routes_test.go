package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/compdani/jackery-monitor/internal/forecast"
	"github.com/compdani/jackery-monitor/internal/store"
	"github.com/compdani/jackery-monitor/internal/weatherstore"
)

// forecastFixture serves recorded-shape Open-Meteo, geocoding and
// Forecast.Solar responses; no upstream service is contacted.
type forecastFixture struct {
	srv      *httptest.Server
	weather  atomic.Int32
	solar    atomic.Int32
	failWx   atomic.Bool
	reverse  atomic.Int32
	solarURL string
}

func newForecastFixture(t *testing.T, now int64) *forecastFixture {
	f := &forecastFixture{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/geo"):
			_, _ = w.Write([]byte(`{"results":[{"name":"San Jose","admin1":"California","country":"United States","latitude":37.34,"longitude":-121.89,"timezone":"America/Los_Angeles"},{"name":"broken"}]}`))
		case strings.HasPrefix(r.URL.Path, "/rev"):
			f.reverse.Add(1)
			_, _ = w.Write([]byte(`{"locality":"San Jose"}`))
		case strings.Contains(r.URL.Path, "/estimate/"):
			f.solar.Add(1)
			watts := map[string]float64{}
			for h := int64(1); h <= 48; h++ {
				ts := (now/3600 + h) * 3600
				local := time.Unix(ts, 0).UTC()
				if local.Hour() >= 10 && local.Hour() <= 15 {
					watts[local.Format("2006-01-02 15:04:05")] = 900
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"watts": watts}})
		default:
			f.weather.Add(1)
			if f.failWx.Load() {
				w.WriteHeader(503)
				return
			}
			var times []int64
			var ghi, cloud []float64
			for ts := (now/3600 - 72) * 3600; ts <= now+96*3600; ts += 3600 {
				h := time.Unix(ts, 0).UTC().Hour()
				g := 0.0
				if h >= 9 && h <= 16 {
					g = 700
				}
				times, ghi, cloud = append(times, ts), append(ghi, g), append(cloud, 20)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"utc_offset_seconds": 0, "timezone": "GMT", "hourly": map[string]any{"time": times, "shortwave_radiation": ghi, "cloud_cover": cloud}})
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}
func (f *forecastFixture) install(s *Server) {
	s.Weather = &forecast.WeatherClient{BaseURL: f.srv.URL + "/v1/forecast", GeocodeURL: f.srv.URL + "/geo", ReverseURL: f.srv.URL + "/rev"}
	s.SolarForecast = &forecast.SolarClient{BaseURL: f.srv.URL}
}

// seedHistory writes 72 hourly imported buckets: daytime solar charging and
// steady overnight discharge, enough for forecast readiness.
func seedHistory(t *testing.T, s *Server, sn string, now int64) {
	t.Helper()
	if err := s.Store.Put("devices", sn, sn, map[string]any{"device": map[string]any{"device_sn": sn, "model_code": 13.0}, "telemetry": map[string]any{"battery_percent": 64.0}}); err != nil {
		t.Fatal(err)
	}
	soc := 60.0
	for i := 72; i >= 1; i-- {
		ts := float64((now/3600 - int64(i)) * 3600)
		h := time.Unix(int64(ts), 0).UTC().Hour()
		solar := 0.0
		if h >= 9 && h <= 16 {
			solar = 1400
			soc = math.Min(100, soc+3)
		} else {
			soc = math.Max(5, soc-1.5)
		}
		row := map[string]any{"ts": ts, "bucket": ts, "solar_wh": solar, "output_wh": 300.0, "input_wh": solar, "ac_input_wh": 0.0, "last_battery_pct": soc, "last_solar_w": solar, "last_output_w": 300.0}
		if err := s.Store.Put("samples", store.SampleKey(sn, ts), sn, row); err != nil {
			t.Fatal(err)
		}
	}
}

func TestForecastBuildRecordsAndPersistsTimezone(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	now := time.Now().Unix()
	f := newForecastFixture(t, now)
	f.install(s)
	v := contractJSON(t, h, "GET", "/api/forecast?device_sn=SN", "", token, 200)
	if v["error"] != "location not set" || v["configured"] != false {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/location", `{"latitude":37.3,"longitude":-121.9}`, token, 200)
	v = contractJSON(t, h, "GET", "/api/forecast", "", token, 200)
	if v["error"] != "no active device" || v["configured"] != true {
		t.Fatal(v)
	}
	seedHistory(t, s, "SN", now)
	contractJSON(t, h, "POST", "/api/forecast/solar_array", `{"device_sn":"SN","declination":30,"azimuth":0,"kwp":2}`, token, 200)
	contractJSON(t, h, "POST", "/api/forecast/load_schedule", `{"device_sn":"SN","mode":"historical"}`, token, 200)
	v = contractJSON(t, h, "GET", "/api/forecast?device_sn=SN&_diag=1", "", token, 200)
	if v["configured"] != true || v["ready"] != true || v["device_sn"] != "SN" || v["main_soc_pct"] != 64.0 {
		t.Fatal(v["configured"], v["ready"], v["error"], v["readiness"])
	}
	if hours := v["forecast"].([]any); len(hours) == 0 {
		t.Fatal("empty forecast")
	}
	fs := v["forecast_solar"].(map[string]any)
	if fs["configured"] != true || v["solar_array"] == nil || v["forecast_solar_has_key"] != false || f.solar.Load() != 1 {
		t.Fatal(fs, v["solar_array"], f.solar.Load())
	}
	if src := v["solar_source"]; src != "mixed" && src != "forecast_solar" {
		t.Fatal(src)
	}
	if v["load_schedule"].(map[string]any)["mode"] != "historical" || v["idle_window_diag"] == nil || v["weather_synthetic"] != false {
		t.Fatal(v["load_schedule"], v["idle_window_diag"])
	}
	predictions, err := s.Store.List("predictions", "SN", 0, 0)
	if err != nil || len(predictions) == 0 {
		t.Fatal(predictions, err)
	}
	daily, err := s.Store.DailySummary("SN", now, 2)
	if err != nil || len(daily) != 1 || daily[0]["predicted_sunset_soc_pct"] == nil && daily[0]["predicted_sunrise_soc_pct"] == nil {
		t.Fatal(daily, err)
	}
	loc := contractJSON(t, h, "GET", "/api/location", "", token, 200)
	if loc["timezone"] != "GMT" || loc["utc_offset_seconds"] != 0.0 || loc["label"] != "San Jose" {
		t.Fatal(loc)
	}
	// The label is persisted, so the reverse lookup is not repeated.
	contractJSON(t, h, "GET", "/api/location", "", token, 200)
	if f.reverse.Load() != 1 {
		t.Fatal(f.reverse.Load())
	}
	// A cached forecast is reused within the hour.
	contractJSON(t, h, "GET", "/api/forecast?device_sn=SN", "", token, 200)
	if f.weather.Load() != 1 {
		t.Fatal(f.weather.Load())
	}
	// Saving a location busts the cache; a weather failure then serves the
	// persisted forecast rather than an error.
	f.failWx.Store(true)
	contractJSON(t, h, "POST", "/api/location", `{"latitude":37.3,"longitude":-121.9,"label":" Home "}`, token, 200)
	v = contractJSON(t, h, "GET", "/api/forecast?device_sn=SN", "", token, 200)
	if v["ready"] != true || v["weather_stale"] != true {
		t.Fatal(v["ready"], v["weather_stale"], v["error"])
	}
	contractJSON(t, h, "POST", "/api/location", `{"latitude":0,"longitude":0}`, token, 400)
	contractJSON(t, h, "POST", "/api/location", `{"latitude":91,"longitude":1}`, token, 400)
}

func TestForecastRecorderAndSmartConsumeLiveWeather(t *testing.T) {
	s, _ := testServer(t)
	now := time.Now().Unix()
	f := newForecastFixture(t, now)
	f.install(s)
	seedHistory(t, s, "SN", now)
	if r, k, err := s.recordAllForecasts(context.Background(), now); err != nil || r != 0 || k != 0 {
		t.Fatal(r, k, err) // no location: skip quietly, like Python
	}
	if err := s.Store.SaveSecret(s.Key, "location", map[string]any{"latitude": 37.3, "longitude": -121.9}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put("devices", "OTHER", "OTHER", map[string]any{"device": map[string]any{"device_sn": "OTHER"}}); err != nil {
		t.Fatal(err)
	}
	r, k, err := s.recordAllForecasts(context.Background(), now)
	if err != nil || r != 1 || k != 1 {
		t.Fatal(r, k, err)
	}
	if err := s.Store.Put("settings", "smart_charge", "", map[string]any{"by_device": map[string]any{"SN": map[string]any{"mode": "test"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put("samples", fmt.Sprintf("observation:SN:%d", now-30), "SN", map[string]any{"ts": float64(now - 30), "battery_pct": 70.0, "battery_percent": 70.0, "output_w": 300.0}); err != nil {
		t.Fatal(err)
	}
	p, err := s.evaluateSmart(context.Background(), "SN", now, false)
	if err != nil || p == nil || strings.Contains(p.Reason, "weather service unavailable") || strings.Contains(p.Reason, "calibrating") {
		t.Fatal(p, err)
	}
	// Loop cancellation is prompt even before the initial delay elapses.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.forecastRecorderLoop(ctx, time.Hour, time.Hour); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recorder did not stop")
	}
}

func TestLocationArraySolarKeyAndGeocodeContracts(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	f := newForecastFixture(t, time.Now().Unix())
	f.install(s)
	v := contractJSON(t, h, "GET", "/api/location", "", token, 200)
	if v["latitude"] != nil || len(v) != 2 {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/location/geocode?q=San%20Jose&count=6", "", token, 200)
	if res := v["results"].([]any); len(res) != 1 || res[0].(map[string]any)["timezone"] != "America/Los_Angeles" {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/location/geocode?q=", "", token, 200)
	if len(v["results"].([]any)) != 0 {
		t.Fatal(v)
	}
	contractJSON(t, h, "GET", "/api/forecast/solar_array", "", token, 400)
	contractJSON(t, h, "POST", "/api/forecast/solar_array", `{"device_sn":"A","declination":95,"azimuth":0,"kwp":1}`, token, 400)
	contractJSON(t, h, "POST", "/api/forecast/solar_array", `{"device_sn":"A","declination":20,"azimuth":200,"kwp":1}`, token, 400)
	v = contractJSON(t, h, "POST", "/api/forecast/solar_array", `{"device_sn":"A","declination":20,"azimuth":-90,"kwp":2.5}`, token, 200)
	if v["device_sn"] != "A" || v["azimuth"] != -90.0 || v["kwp"] != 2.5 {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "GET", "/api/forecast/solar_array?device_sn=A", "", token, 200)
	if v["array"].(map[string]any)["declination"] != 20.0 || v["has_key"] != false {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "GET", "/api/forecast/solar_array?device_sn=B", "", token, 200); v["array"] != nil {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/forecast/solar_key", `{"api_key":"  "}`, token, 400)
	contractJSON(t, h, "POST", "/api/forecast/solar_key", `{"api_key":"secret"}`, token, 200)
	if v = contractJSON(t, h, "GET", "/api/forecast/solar_key", "", token, 200); v["has_key"] != true {
		t.Fatal(v)
	}
	raw, _ := s.Store.Get("credentials", "forecast_solar")
	if raw["api_key"] != nil {
		t.Fatal("plaintext key stored")
	}
	contractJSON(t, h, "DELETE", "/api/forecast/solar_key", "", token, 200)
	if v = contractJSON(t, h, "GET", "/api/forecast/solar_key", "", token, 200); v["has_key"] != false {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/forecast/solar_array/infer?device_sn=A", "", token, 400)
	contractJSON(t, h, "POST", "/api/location", `{"latitude":37.3,"longitude":-121.9}`, token, 200)
	w := request(h, "POST", "/api/forecast/solar_array/infer?device_sn=A", "", token)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "paired hours=") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLoadScheduleNormalizationAndLearnedCopy(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	now := time.Now().Unix()
	seedHistory(t, s, "SN", now)
	contractJSON(t, h, "GET", "/api/forecast/load_schedule", "", token, 400)
	v := contractJSON(t, h, "GET", "/api/forecast/load_schedule?device_sn=SN", "", token, 200)
	if v["mode"] != "scheduled" || v["sleep_start"] != nil || len(v["learned_profile"].([]any)) == 0 {
		t.Fatal(v)
	}
	body := `{"device_sn":"SN","mode":"bogus","sleep_start":"7:5","sleep_end":"25:00","windows":[
		{"id":"a","start":"18:00","end":"22:00","watts":"250.04","days":"weekend"},
		{"id":"a","start":"01:00","end":"02:00","watts":10,"kind":"once","date":"2026-02-30"},
		{"id":"b","start":"01:00","end":"02:00","watts":10,"kind":"once","date":"2026-02-28"},
		{"start":"x","end":"02:00"},{"start":"01:00","end":"02:00","watts":-1},
		{"id":"a","start":"03:00","end":"04:00","watts":5,"kind":"weekly","weekdays":[9,"2",2,0]}]}`
	v = contractJSON(t, h, "POST", "/api/forecast/load_schedule", body, token, 200)
	windows := v["windows"].([]any)
	if v["mode"] != "scheduled" || v["sleep_start"] != "07:05" || v["sleep_end"] != nil || len(windows) != 3 {
		t.Fatal(v)
	}
	first, third := windows[0].(map[string]any), windows[2].(map[string]any)
	if first["kind"] != "weekly" || fmt.Sprint(first["weekdays"]) != "[5 6]" || first["watts"] != 250.0 {
		t.Fatal(first)
	}
	if third["id"] == "a" || fmt.Sprint(third["weekdays"]) != "[0 2]" {
		t.Fatal(third)
	}
	v = contractJSON(t, h, "POST", "/api/forecast/load_schedule?copy_learned=1", `{"device_sn":"SN","mode":"historical","sleep_start":"23:00"}`, token, 200)
	if v["mode"] != "scheduled" || v["sleep_start"] != "23:00" || len(v["windows"].([]any)) == 0 {
		t.Fatal(v)
	}
	w := v["windows"].([]any)[0].(map[string]any)
	if w["kind"] != "weekly" || !strings.HasSuffix(w["label"].(string), "day") && !strings.HasSuffix(w["label"].(string), "weekend") {
		t.Fatal(w)
	}
}

// Assertions ported from tests/test_device_params_fit.py.
func TestDeviceParamFitContracts(t *testing.T) {
	for in, want := range map[any]int{nil: 336, 24.0: 24, 1.0: 6, 99999.0: 1080, "nope": 336} {
		if got := clampFitLookback(in); got != want {
			t.Fatal(in, got, want)
		}
	}
	s, h := testServer(t)
	token := contractToken(t, h)
	contractJSON(t, h, "POST", "/api/devices/params/fit", `{"device_sn":"FIT","key":"not_a_real_param"}`, token, 400)
	contractJSON(t, h, "POST", "/api/devices/params/fit", `{"device_sn":"FIT","key":"battery_capacity_wh","lookback_hours":24,"apply":true}`, token, 400)
	v := contractJSON(t, h, "POST", "/api/devices/params/fit", `{"device_sn":"FIT","key":"solar_coefficient","lookback_hours":24,"apply":false}`, token, 200)
	if v["lookback_hours"] != 24.0 || v["applied"] != false || v["value"] == nil {
		t.Fatal(v)
	}
	if v = contractJSON(t, h, "POST", "/api/devices/params/fit", `{"device_sn":"FIT","key":"parasitic_w","lookback_hours":2}`, token, 200); v["lookback_hours"] != 6.0 {
		t.Fatal(v)
	}
	// Solar coefficient: recent 24h k=2.0, older k=0.5 (clear-sky GHI 800).
	now := time.Now().Unix() / 3600 * 3600
	for i := 0; i < 14*24; i++ {
		ts := float64(now - int64(i+1)*3600)
		k := .5
		if i < 24 {
			k = 2
		}
		if err := s.Store.Put("samples", store.SampleKey("FIT", ts), "FIT", map[string]any{"ts": ts, "bucket": ts, "solar_wh": k * 800, "output_wh": 100.0, "input_wh": 0.0, "ac_input_wh": 0.0, "last_solar_w": k * 800, "last_output_w": 100.0, "last_battery_pct": 50.0}); err != nil {
			t.Fatal(err)
		}
		if err := s.Store.Put("weather_observations", fmt.Sprintf("wx:%.0f", ts+3600), "", map[string]any{"ts": ts + 3600, "ghi_w_m2": 800.0, "cloud_cover_pct": 10.0}); err != nil {
			t.Fatal(err)
		}
	}
	j24, _, _ := s.fitDeviceParam(context.Background(), "FIT", "solar_coefficient", 24.0, false)
	j14, _, _ := s.fitDeviceParam(context.Background(), "FIT", "solar_coefficient", nil, false)
	if j14["lookback_hours"] != 336 || math.Abs(forecast.Number(j24, "value")-forecast.Number(j14, "value")) <= .2 {
		t.Fatal(j24, j14)
	}
	ja, status, _ := s.fitDeviceParam(context.Background(), "FIT", "solar_coefficient", 24.0, true)
	if status != 200 || ja["applied"] != true || ja["source"] != "user" || !strings.Contains(str(ja, "note"), "fit over last 24h") {
		t.Fatal(ja)
	}
	stored, err := s.storedParam("FIT", "solar_coefficient")
	if err != nil || stored["source"] != "user" || stored["note"] != "fit over last 24h" || math.Abs(forecast.Number(stored, "value")-forecast.Number(ja, "value")) > 1e-6 {
		t.Fatal(stored, err)
	}
	if r := s.resolveParam(context.Background(), "FIT", "solar_coefficient", 336); r["source"] != "user" {
		t.Fatal(r)
	}
	// Refit clears the stored row and re-resolves over the requested window.
	v = contractJSON(t, h, "POST", "/api/devices/params/refit", `{"device_sn":"FIT","key":"solar_coefficient","lookback_hours":24}`, token, 200)
	if v["value"] == nil || (v["source"] != "fit" && v["source"] != "default") || v["lookback_hours"] != 24.0 {
		t.Fatal(v)
	}
}

func TestDeviceParamOverridesAndLegacyTombstone(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	contractJSON(t, h, "GET", "/api/devices/params", "", token, 400)
	if err := s.Store.Put("devices", "SN", "SN", map[string]any{"device": map[string]any{"device_sn": "SN", "model_code": 13.0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put("forecast_models", "legacy:SN:parasitic_w", "SN", map[string]any{"_legacy_table": "device_params", "key": "parasitic_w", "value": 77.0, "source": "user"}); err != nil {
		t.Fatal(err)
	}
	v := contractJSON(t, h, "GET", "/api/devices/params?device_sn=SN&debug_key=max_charge_w", "", token, 200)
	params := map[string]map[string]any{}
	for _, raw := range v["params"].([]any) {
		p := raw.(map[string]any)
		params[p["key"].(string)] = p
	}
	if len(params) != 6 || params["parasitic_w"]["value"] != 77.0 || params["parasitic_w"]["source"] != "user" || params["battery_capacity_wh"]["source"] != "catalog" || params["parasitic_w"]["label"] == nil || v["debug"] == nil {
		t.Fatal(v)
	}
	contractJSON(t, h, "POST", "/api/devices/params", `{"device_sn":"SN","key":"bogus","value":1}`, token, 400)
	contractJSON(t, h, "POST", "/api/devices/params", `{"device_sn":"SN","key":"parasitic_w","value":"x"}`, token, 400)
	v = contractJSON(t, h, "POST", "/api/devices/params", `{"device_sn":"SN","key":"parasitic_w","value":120}`, token, 200)
	if v["value"] != 120.0 || v["source"] != "user" || v["note"] != "set via Device tab" {
		t.Fatal(v)
	}
	v = contractJSON(t, h, "POST", "/api/devices/params", `{"device_sn":"SN","key":"parasitic_w","value":null}`, token, 200)
	if v["source"] == "user" {
		t.Fatal(v)
	}
	// The imported legacy row is hidden, never rewritten.
	legacy, err := s.Store.Get("forecast_models", "legacy:SN:parasitic_w")
	if err != nil || legacy["value"] != 77.0 {
		t.Fatal(legacy, err)
	}
	contractJSON(t, h, "POST", "/api/devices/params", `{"device_sn":"SN","key":"battery_capacity_wh","value":9000}`, token, 200)
	if p := s.resolveParam(context.Background(), "SN", "battery_capacity_wh", 336); p["value"] != 9000.0 {
		t.Fatal(p)
	}
	for _, path := range []string{"/api/diagnostics/row_soc", "/api/diagnostics/parasitic_fit_windows?device_sn=SN&hours=99999"} {
		v = contractJSON(t, h, "GET", path, "", token, 200)
		if strings.Contains(path, "parasitic") && v["hours"] != 720.0 {
			t.Fatal(v)
		}
		if strings.Contains(path, "row_soc") && v["interpretation"] == nil {
			t.Fatal(v)
		}
	}
}

func TestIdleWindowDiagnosis(t *testing.T) {
	rows := []forecast.Row{
		{"ts": 0.0, "system_soc": 50.0, "output_wh": 200.0},
		{"ts": 3600.0, "system_soc": 49.0, "output_wh": 200.0, "solar_wh": 100.0},
		{"ts": 7200.0, "system_soc": 48.0},
		{"ts": 10800.0},
	}
	d := diagnoseIdleWindows(rows)
	rej := d["rejected"].(map[string]int)
	if d["total_pairs"] != 3 || d["qualifying_windows"] != 1 || rej["solar_above_noise"] != 1 || rej["missing_soc"] != 1 {
		t.Fatal(d)
	}
}

// Ported from tests/test_predictions.py::test_smart_charge_analytics_includes_mode_and_reason.
func TestSmartAnalyticsIncludesModeReasonAndBaseline(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	now := time.Now().Unix()
	sunrise := float64(now - 86400)
	if err := s.Store.Put("automation_decisions", "decision:SN-D", "SN-D", map[string]any{"controller": "smart_charge", "decided_at": sunrise - 3600, "ts": sunrise - 3600, "mode": "test", "action": "off", "reason": "predicted SOC 60% >= target 35%; coasting", "current_soc_pct": 65.0, "predicted_sunrise_soc_pct": 60.0, "target_sunrise_soc_pct": 35.0, "sunrise_ts": sunrise, "baseline_predicted_sunrise_soc_pct": 42.5, "executed": false}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []float64{sunrise - 60, sunrise + 60} {
		if err := s.Store.Put("samples", fmt.Sprintf("observation:SN-D:%.0f", at), "SN-D", map[string]any{"ts": at, "battery_pct": 58.0, "battery_percent": 58.0}); err != nil {
			t.Fatal(err)
		}
	}
	v := contractJSON(t, h, "GET", "/api/smart_charge/analytics?device_sn=SN-D&days=2", "", token, 200)
	rows := v["samples"].([]any)
	if len(rows) != 1 {
		t.Fatal(v)
	}
	row := rows[0].(map[string]any)
	if row["mode"] != "test" || row["action"] != "off" || !strings.Contains(row["reason"].(string), "coasting") || row["baseline_predicted_sunrise_soc_pct"] != 42.5 {
		t.Fatal(row)
	}
	listed, err := s.smartHistory("SN-D")
	if err != nil || len(listed) != 1 || listed[0]["baseline_predicted_sunrise_soc_pct"] != 42.5 {
		t.Fatal(listed, err)
	}
}

// Ported from tests/test_forecaster.py diagnose_idle_windows cases.
func TestIdleWindowReferenceClassification(t *testing.T) {
	base := 1_700_000_000.0
	row := func(i int, soc any, out, solar, ac float64) forecast.Row {
		r := forecast.Row{"ts": base + float64(i)*3600, "output_wh": out, "solar_wh": solar, "ac_input_wh": ac}
		if soc != nil {
			r["battery_pct"] = soc
		}
		return r
	}
	history := []forecast.Row{row(0, 80.0, 545, 0, 0), row(1, 78.0, 545, 0, 0), row(2, 76.0, 545, 0, 0), row(3, nil, 545, 0, 0), row(4, 75.0, 545, 0, 0), row(5, 74.0, 545, 1000, 0), row(6, 72.0, 545, 0, 2000), row(7, 70.0, 30, 0, 0), row(8, 68.0, 30, 0, 0)}
	d := diagnoseIdleWindows(history)
	want := map[string]int{"missing_soc": 2, "soc_drop_below_min_pp": 1, "solar_above_noise": 1, "ac_input_above_noise": 1, "dt_out_of_range": 0, "out_w_under_50": 1}
	if d["total_pairs"] != 8 || d["qualifying_windows"] != 2 || d["needed_windows"] != 5 || fmt.Sprint(d["rejected"]) != fmt.Sprint(want) {
		t.Fatal(d)
	}
	d = diagnoseIdleWindows(nil)
	if d["total_pairs"] != 0 || d["qualifying_windows"] != 0 {
		t.Fatal(d)
	}
}

func TestForecastCacheMultiPackCapacityAndShadePersistence(t *testing.T) {
	s, h := testServer(t)
	token := contractToken(t, h)
	now := time.Now().Unix()
	f := newForecastFixture(t, now)
	f.install(s)
	seedHistory(t, s, "SN-PACK", now)
	// Add 2 expansion packs (model 13 = 5040 Wh main + 2 * 5040 Wh packs = 15120 Wh).
	if err := s.Store.Put("battery_packs", "packs:SN-PACK:1", "SN-PACK", map[string]any{
		"ts":    float64(now - 60),
		"packs": []any{map[string]any{"rb": 60.0}, map[string]any{"rb": 62.0}},
	}); err != nil {
		t.Fatal(err)
	}
	contractJSON(t, h, "POST", "/api/location", `{"latitude":37.3,"longitude":-121.9}`, token, 200)
	contractJSON(t, h, "POST", "/api/forecast/solar_array", `{"device_sn":"SN-PACK","declination":30,"azimuth":0,"kwp":2}`, token, 200)
	contractJSON(t, h, "POST", "/api/forecast/load_schedule", `{"device_sn":"SN-PACK","mode":"historical"}`, token, 200)

	v := contractJSON(t, h, "GET", "/api/forecast?device_sn=SN-PACK", "", token, 200)
	if v["capacity_wh"] != 15120.0 {
		t.Fatalf("expected multi-pack capacity 15120, got %v", v["capacity_wh"])
	}
	if v["shade_profile"] == nil {
		t.Fatalf("expected shade_profile in forecast payload")
	}
	// Forecast.Solar history should have been persisted for future calibration.
	site := weatherstore.SolarSite{Lat: 37.3, Lon: -121.9, Plane: forecast.Plane{Declination: 30, KWP: 2}}
	sh, err := s.weatherStore().LoadSolarHistory("SN-PACK", site, now-86400)
	if err != nil || len(sh) == 0 {
		t.Fatalf("expected persisted solar history, got len=%d err=%v", len(sh), err)
	}
	// Stored solar_coefficient override invalidates cache and takes effect.
	contractJSON(t, h, "POST", "/api/devices/params", `{"device_sn":"SN-PACK","key":"solar_coefficient","value":1.75}`, token, 200)
	v2 := contractJSON(t, h, "GET", "/api/forecast?device_sn=SN-PACK", "", token, 200)
	if v2["solar_coefficient"] != 1.75 {
		t.Fatalf("expected solar_coefficient override 1.75, got %v", v2["solar_coefficient"])
	}
	contractJSON(t, h, "POST", "/api/location", `{"latitude":40,"longitude":-121.9}`, token, 200)
	site.Lat = 40
	sh, err = s.weatherStore().LoadSolarHistory("SN-PACK", site, now-86400)
	if err != nil || len(sh) != 0 {
		t.Fatalf("new location inherited old solar history: %v, %v", sh, err)
	}
}

type testWeatherFunc func(context.Context, string, int64) ([]forecast.Row, error)

func (f testWeatherFunc) Weather(ctx context.Context, sn string, now int64) ([]forecast.Row, error) {
	return f(ctx, sn, now)
}

func TestInvalidatedForecastCannotEvictReplacement(t *testing.T) {
	s, _ := testServer(t)
	if err := s.Store.SaveSecret(s.Key, "location", map[string]any{"latitude": 35.0, "longitude": -95.0}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	oldCtx, cancelOld := context.WithCancel(ctx)
	defer cancelOld()
	started := make(chan struct{}, 2)
	s.SmartForecast = testWeatherFunc(func(ctx context.Context, _ string, _ int64) ([]forecast.Row, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	oldDone, newDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(oldDone)
		s.cachedForecast(oldCtx, "SN", false)
	}()
	defer func() { cancelOld(); <-oldDone }()
	wait(started)
	s.invalidateForecastCache()
	go func() {
		defer close(newDone)
		s.cachedForecast(ctx, "SN", false)
	}()
	defer func() { cancel(); <-newDone }()
	wait(started)
	cancelOld()
	wait(oldDone)
	s.forecasts.cacheMu.Lock()
	replacement := s.forecasts.cache[forecastCacheKey{serial: "SN"}]
	s.forecasts.cacheMu.Unlock()
	if replacement == nil {
		t.Fatal("invalidated request evicted the replacement")
	}
}

func TestForecastPreviewReturnsWhileSolarProviderIsBlocked(t *testing.T) {
	s, _ := testServer(t)
	now := time.Now().Unix()
	fixture := newForecastFixture(t, now)
	fixture.install(s)
	seedHistory(t, s, "SN", now)
	if err := s.Store.SaveSecret(s.Key, "location", map[string]any{
		"latitude": 35.0, "longitude": -95.0, "utc_offset_seconds": 0, "timezone": "GMT",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.weatherStore().SavePlane("SN", 30, 0, 2); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}, 1), make(chan struct{})
	solarServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
			fixture.srv.Config.Handler.ServeHTTP(w, r)
		case <-r.Context().Done():
		}
	}))
	defer solarServer.Close()
	s.SolarForecast.BaseURL = solarServer.URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	full := make(chan map[string]any, 1)
	go func() { full <- s.cachedForecast(ctx, "SN", false) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("full forecast never requested Forecast.Solar")
	}
	preview := s.cachedForecast(ctx, "SN", true)
	meta, _ := preview["forecast_solar"].(map[string]any)
	if preview["ready"] != true || preview["solar_source"] != "open_meteo" || meta["pending"] != true {
		t.Fatalf("preview did not return an Open-Meteo forecast while solar was pending: %v, %v", preview["error"], meta)
	}
	close(release)
	select {
	case result := <-full:
		if source := result["solar_source"]; source != "mixed" && source != "forecast_solar" {
			t.Fatalf("full forecast did not pick up the provider response: %v", source)
		}
	case <-ctx.Done():
		t.Fatal("full forecast never completed")
	}
}
