package forecast

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type weatherMemory struct {
	saved Weather
	obs   []Row
}

func (m *weatherMemory) SaveWeather(_ string, w Weather) error     { m.saved = w; return nil }
func (m *weatherMemory) LoadWeather(string) (Weather, error)       { return m.saved, nil }
func (m *weatherMemory) Observations(string, int64) ([]Row, error) { return m.obs, nil }

type weatherFailure struct{}

func (weatherFailure) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("timeout")
}
func TestWeatherPersistedFallbacks(t *testing.T) {
	now := time.Now().Unix() / 3600 * 3600
	m := &weatherMemory{}
	for d := 1; d <= 3; d++ {
		for h := 0; h < 24; h++ {
			ts := now - int64(d)*86400 + int64(h)*3600
			hour := ts % 86400 / 3600
			g := 0.0
			if hour >= 10 && hour <= 14 {
				g = 800
			}
			m.obs = append(m.obs, Row{"ts": float64(ts), "ghi_w_m2": g, "cloud_cover_pct": 10})
		}
	}
	c := &WeatherClient{HTTP: &http.Client{Transport: weatherFailure{}}, Store: m}
	out, err := c.Fetch(context.Background(), 37, -122)
	if err != nil || !out.Synthetic || !out.Stale || out.Error != "" || out.StaleError == "" {
		t.Fatal(out, err)
	}
	for _, r := range out.Hourly {
		if Number(r, "ts") <= float64(now) {
			continue
		}
		h := int64(Number(r, "ts")) % 86400 / 3600
		if h >= 10 && h <= 14 && Number(r, "ghi_w_m2") != 800 {
			t.Fatal(r)
		}
		if h < 4 && Number(r, "ghi_w_m2") != 0 {
			t.Fatal(r)
		}
	}
	m.saved = Weather{Hourly: []Row{{"ts": float64(now + 7200), "ghi_w_m2": 500}}, FetchedAt: now - 7200}
	out, err = c.Fetch(context.Background(), 37, -122)
	if err != nil || out.Synthetic || !out.Stale || out.StaleAge < 7200 || len(out.Hourly) != len(m.obs)+1 {
		t.Fatal(out, err)
	}
}
func TestGeocodeContracts(t *testing.T) {
	count := ""
	payload := `{"results":[{"name":"Good","latitude":1,"longitude":2},{"name":"Missing","longitude":2},{"latitude":"bad","longitude":2},{"name":"Good 2","latitude":3,"longitude":4}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count = r.URL.Query().Get("count")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	c := &WeatherClient{GeocodeURL: srv.URL, ReverseURL: srv.URL}
	if out := c.Geocode(context.Background(), "  ", 5); len(out.Results) != 0 {
		t.Fatal(out)
	}
	out := c.Geocode(context.Background(), "x", 0)
	if len(out.Results) != 2 || out.Results[0].Name != "Good" || count != "1" {
		t.Fatal(out, count)
	}
	c.Geocode(context.Background(), "x", 999)
	if count != "10" {
		t.Fatal(count)
	}
	for _, test := range []struct{ body, want string }{{`{"locality":"Almaden Valley","city":"San Jose"}`, "Almaden Valley"}, {`{"city":"San Jose","principalSubdivision":"California"}`, "San Jose"}, {`{"principalSubdivision":"California"}`, "California"}, {`{}`, ""}} {
		payload = test.body
		if got := c.ReverseGeocode(context.Background(), 37, -122); got != test.want {
			t.Fatal(got, test.want)
		}
	}
	c.HTTP = &http.Client{Transport: weatherFailure{}}
	if out = c.Geocode(context.Background(), "x", 5); out.Error == "" || len(out.Results) != 0 {
		t.Fatal(out)
	}
	if c.ReverseGeocode(context.Background(), 37, -122) != "" {
		t.Fatal("failure label")
	}
}
