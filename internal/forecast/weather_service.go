package forecast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WeatherStore keeps location-scoped forecasts and real observations separately.
// Implementations must never treat future predictions as observations.
type WeatherStore interface {
	SaveWeather(string, Weather) error
	LoadWeather(string) (Weather, error)
	Observations(string, int64) ([]Row, error)
}

func (c *WeatherClient) fallback(key string, lat, lon float64) (Weather, bool) {
	now := time.Now().Unix()
	obs, err := c.Store.Observations(key, now-14*86400)
	if err != nil {
		return Weather{}, false
	}
	past := []Row{}
	for _, r := range obs {
		if Number(r, "ts") <= float64(now) {
			past = append(past, r)
		}
	}
	saved, err := c.Store.LoadWeather(key)
	if err == nil {
		future := []Row{}
		for _, r := range saved.Hourly {
			if Number(r, "ts") > float64(now) {
				future = append(future, r)
			}
		}
		if len(future) > 0 {
			saved.Hourly = append(past, future...)
			saved.Stale = true
			saved.StaleAge = now - saved.FetchedAt
			return saved, true
		}
	}
	if len(past) == 0 {
		return Weather{}, false
	}
	var ghi, cloud [24][]float64
	for _, r := range past {
		h := int(((int64(Number(r, "ts"))+int64(c.Offset))%86400 + 86400) % 86400 / 3600)
		ghi[h] = append(ghi[h], Number(r, "ghi_w_m2"))
		cloud[h] = append(cloud[h], Number(r, "cloud_cover_pct"))
	}
	upper := func(v []float64) float64 {
		if len(v) == 0 {
			return 0
		}
		sort.Float64s(v)
		return v[len(v)/2]
	}
	for ts := (now/3600 + 1) * 3600; ts <= now+7*86400; ts += 3600 {
		h := int(((ts+int64(c.Offset))%86400 + 86400) % 86400 / 3600)
		past = append(past, Row{"ts": float64(ts), "ghi_w_m2": upper(ghi[h]), "cloud_cover_pct": upper(cloud[h])})
	}
	return Weather{Hourly: past, FetchedAt: now, Lat: lat, Lon: lon, Offset: c.Offset, Stale: true, Synthetic: true}, true
}
func (c *WeatherClient) getJSON(ctx context.Context, endpoint string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("geocoding HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out)
}

type GeocodeResult struct {
	Name      string  `json:"name"`
	Admin1    string  `json:"admin1"`
	Country   string  `json:"country"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
}
type GeocodeResponse struct {
	Results []GeocodeResult `json:"results"`
	Error   string          `json:"error,omitempty"`
}

func (c *WeatherClient) Geocode(ctx context.Context, query string, count int) GeocodeResponse {
	out := GeocodeResponse{Results: []GeocodeResult{}}
	if strings.TrimSpace(query) == "" {
		return out
	}
	endpoint := c.GeocodeURL
	if endpoint == "" {
		endpoint = "https://geocoding-api.open-meteo.com/v1/search"
	}
	var raw struct {
		Results []json.RawMessage `json:"results"`
	}
	err := c.getJSON(ctx, endpoint, url.Values{"name": {strings.TrimSpace(query)}, "count": {strconv.Itoa(max(1, min(10, count)))}, "language": {"en"}, "format": {"json"}}, &raw)
	if err != nil {
		out.Error = fmt.Sprintf("geocode: %v", err)
		return out
	}
	for _, r := range raw.Results {
		var coords struct {
			Lat *float64 `json:"latitude"`
			Lon *float64 `json:"longitude"`
		}
		var row GeocodeResult
		if json.Unmarshal(r, &coords) != nil || coords.Lat == nil || coords.Lon == nil || json.Unmarshal(r, &row) != nil {
			continue
		}
		out.Results = append(out.Results, row)
	}
	return out
}
func (c *WeatherClient) ReverseGeocode(ctx context.Context, lat, lon float64) string {
	endpoint := c.ReverseURL
	if endpoint == "" {
		endpoint = "https://api.bigdatacloud.net/data/reverse-geocode-client"
	}
	var raw map[string]any
	if c.getJSON(ctx, endpoint, url.Values{"latitude": {strconv.FormatFloat(lat, 'f', -1, 64)}, "longitude": {strconv.FormatFloat(lon, 'f', -1, 64)}, "localityLanguage": {"en"}}, &raw) != nil {
		return ""
	}
	for _, key := range []string{"locality", "city", "principalSubdivision"} {
		if s, ok := raw[key].(string); ok && s != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
