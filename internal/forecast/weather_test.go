package forecast

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jackery-monitor/internal/energy"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"jackery-monitor/internal/site"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func appForTest(t *testing.T) core.App {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: filepath.Join(t.TempDir(), "pb_data")})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := app.RunAllMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.ResetBootstrapState() })
	return app
}
func TestWeatherCacheRestartOutageAndLocationChange(t *testing.T) {
	app := appForTest(t)
	c := NewWeather(app)
	lat, lon := 35.0, -120.0
	l := site.Location{Latitude: &lat, Longitude: &lon}
	if err := c.ChangeLocation(l); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1789041600, 0)
	calls := 0
	fail := false
	c.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if fail {
			return nil, errors.New("offline")
		}
		if r.URL.Query().Get("timeformat") != "unixtime" {
			t.Fatal("missing epoch format")
		}
		times := []int64{}
		ghi, cloud := []float64{}, []float64{}
		for i := -72; i < 121; i++ {
			times = append(times, now.Unix()+int64(i)*3600)
			ghi = append(ghi, float64((i+72)%24)*10)
			cloud = append(cloud, 20)
		}
		raw, _ := json.Marshal(map[string]any{"timezone": "America/Los_Angeles", "utc_offset_seconds": -25200, "hourly": map[string]any{"time": times, "shortwave_radiation": ghi, "cloud_cover": cloud}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	w, err := c.Fetch(context.Background(), now)
	if err != nil || len(w.Hourly) != 193 || w.Stale {
		t.Fatalf("fresh: %v %+v", err, w)
	}
	_, err = c.Fetch(context.Background(), now.Add(time.Minute))
	if err != nil || calls != 1 {
		t.Fatalf("cache missed %d %v", calls, err)
	}
	restarted := NewWeather(app)
	restarted.HTTP = c.HTTP
	fail = true
	w, err = restarted.Fetch(context.Background(), now.Add(2*time.Hour))
	if err != nil || !w.Stale || w.Synthetic || len(w.Hourly) == 0 {
		t.Fatalf("restart fallback: %v %+v", err, w)
	}
	// Encrypted coordinates survive a weather timezone update.
	var raw map[string]any
	_, err = site.Read(app, "location", &raw)
	if err != nil || raw["ct"] == nil || raw["latitude"] != nil {
		t.Fatalf("location not encrypted: %v", err)
	}
	saved, err := site.Get(app)
	if err != nil || saved.Timezone != "America/Los_Angeles" || *saved.Latitude != 35 {
		t.Fatal(saved, err)
	}
	// After the saved horizon expires, enough observed days produce an explicit synthetic fallback.
	w, err = restarted.Fetch(context.Background(), now.Add(120*time.Hour))
	if err != nil || !w.Synthetic || len(w.Hourly) == 0 {
		t.Fatal("expired real forecast served without synthetic label")
	}
	lat = 45
	if err = restarted.ChangeLocation(l); err != nil {
		t.Fatal(err)
	}
	w, err = restarted.Fetch(context.Background(), now.Add(3*time.Hour))
	if err == nil || len(w.Hourly) > 0 {
		t.Fatal("previous site's weather leaked into new site")
	}
	rows, err := energy.Query(app, "SELECT * FROM weather_observations", nil)
	if err != nil || len(rows) > 0 {
		t.Fatal("old observations retained")
	}
}
func TestPredictionsImmutableAndAccuracy(t *testing.T) {
	app := appForTest(t)
	store := energy.New(app)
	now := time.Unix(1789041600, 0)
	target := now.Add(2 * time.Hour).Unix()
	hours := []Row{{"ts": target, "predicted_soc": 80.0}}
	if err := Record(app, "S1", now, hours, time.UTC); err != nil {
		t.Fatal(err)
	}
	hours[0]["predicted_soc"] = 10.0
	if err := Record(app, "S1", now.Add(time.Minute), hours, time.UTC); err != nil {
		t.Fatal(err)
	}
	_, err := app.DB().NewQuery(`INSERT INTO samples(device_sn,bucket,last_battery_pct,last_system_soc) VALUES('S1',1789048800,20,70)`).Execute()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Accuracy(store, "S1", now.Add(4*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	samples := got["samples"].([]Row)
	if len(samples) != 1 || num(samples[0], "predicted_soc") != 80 || num(samples[0], "actual_soc") != 70 {
		t.Fatal(got)
	}
}
