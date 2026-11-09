package forecast

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWeatherStale(t *testing.T) {
	offline := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"hourly":{"time":[1],"shortwave_radiation":[500],"cloud_cover":[0]}}`))
	}))
	defer srv.Close()
	c := &WeatherClient{BaseURL: srv.URL}
	out, e := c.Fetch(context.Background(), 37, -122)
	if e != nil || len(out.Hourly) != 1 {
		t.Fatal(out, e)
	}
	cached := c.cache["37.0000,-122.0000"]
	cached.FetchedAt = time.Now().Unix() - 9999
	c.cache["37.0000,-122.0000"] = cached
	offline = true
	out, e = c.Fetch(context.Background(), 37, -122)
	if e != nil || !out.Stale || out.StaleError == "" || out.Error != "" {
		t.Fatal(out, e)
	}
	c.cache = nil
	out, e = c.Fetch(context.Background(), 37, -122)
	if e == nil || out.Error == "" {
		t.Fatal(out, e)
	}
}
func TestForecastSolarHelpers(t *testing.T) {
	if v := EstimateURL(37.3382, -121.8863, 20, 0, 2.4, ""); v != "https://api.forecast.solar/estimate/37.3382/-121.8863/20/0/2.4" {
		t.Fatal(v)
	}
	hours := ResampleWatts(map[string]float64{"2024-07-01 08:15:00": 100, "2024-07-01 08:45:00": 300}, 0)
	ts := time.Date(2024, 7, 1, 8, 0, 0, 0, time.UTC).Unix()
	if hours[ts] != 200 {
		t.Fatal(hours)
	}
}
