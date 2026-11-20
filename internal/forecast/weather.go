package forecast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

type Weather struct {
	Hourly     []Row   `json:"hourly"`
	FetchedAt  int64   `json:"fetched_at"`
	Stale      bool    `json:"stale,omitempty"`
	StaleError string  `json:"stale_error,omitempty"`
	Error      string  `json:"error,omitempty"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	Offset     int     `json:"utc_offset_seconds"`
	Timezone   string  `json:"timezone"`
	Synthetic  bool    `json:"synthetic,omitempty"`
	StaleAge   int64   `json:"stale_age_s,omitempty"`
}
type WeatherClient struct {
	HTTP       *http.Client
	BaseURL    string
	GeocodeURL string
	ReverseURL string
	Store      WeatherStore
	Offset     int
	mu         sync.Mutex
	cache      map[string]Weather
}

func ValidLocation(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 && (lat != 0 || lon != 0)
}
func (c *WeatherClient) Fetch(ctx context.Context, lat, lon float64) (Weather, error) {
	if !ValidLocation(lat, lon) {
		return Weather{}, fmt.Errorf("invalid location")
	}
	key := fmt.Sprintf("%.4f,%.4f", lat, lon)
	c.mu.Lock()
	cached, ok := c.cache[key]
	c.mu.Unlock()
	if ok && time.Now().Unix()-cached.FetchedAt < 3600 {
		return cached, nil
	}
	endpoint := c.BaseURL
	if endpoint == "" {
		endpoint = "https://api.open-meteo.com/v1/forecast"
	}
	q := url.Values{"latitude": {strconv.FormatFloat(lat, 'f', -1, 64)}, "longitude": {strconv.FormatFloat(lon, 'f', -1, 64)}, "hourly": {"shortwave_radiation,direct_radiation,diffuse_radiation,cloud_cover"}, "past_days": {"14"}, "forecast_days": {"7"}, "timeformat": {"unixtime"}, "timezone": {"auto"}}
	req, e := http.NewRequestWithContext(ctx, "GET", endpoint+"?"+q.Encode(), nil)
	if e != nil {
		return Weather{}, e
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, e := client.Do(req)
	if e == nil {
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			e = fmt.Errorf("weather HTTP %d", resp.StatusCode)
		} else {
			var v struct {
				Offset   int    `json:"utc_offset_seconds"`
				Timezone string `json:"timezone"`
				Hourly   struct {
					Time    []int64    `json:"time"`
					GHI     []*float64 `json:"shortwave_radiation"`
					Direct  []*float64 `json:"direct_radiation"`
					Diffuse []*float64 `json:"diffuse_radiation"`
					Cloud   []*float64 `json:"cloud_cover"`
				} `json:"hourly"`
			}
			e = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&v)
			if e == nil {
				rows := []Row{}
				for i, ts := range v.Hourly.Time {
					if i >= len(v.Hourly.GHI) || i >= len(v.Hourly.Cloud) {
						continue
					}
					g, cloud := 0.0, 0.0
					if v.Hourly.GHI[i] != nil {
						g = *v.Hourly.GHI[i]
					}
					if v.Hourly.Cloud[i] != nil {
						cloud = *v.Hourly.Cloud[i]
					}
					row := Row{"ts": float64(ts), "ghi_w_m2": g, "cloud_cover_pct": cloud}
					if i < len(v.Hourly.Direct) && v.Hourly.Direct[i] != nil {
						row["direct_w_m2"] = *v.Hourly.Direct[i]
					}
					if i < len(v.Hourly.Diffuse) && v.Hourly.Diffuse[i] != nil {
						row["diffuse_w_m2"] = *v.Hourly.Diffuse[i]
					}
					rows = append(rows, row)
				}
				if len(rows) == 0 {
					e = fmt.Errorf("weather returned no usable hours")
				} else {
					out := Weather{Hourly: rows, FetchedAt: time.Now().Unix(), Lat: lat, Lon: lon, Offset: v.Offset, Timezone: v.Timezone}
					if c.Store != nil {
						_ = c.Store.SaveWeather(key, out)
					}
					c.mu.Lock()
					if c.cache == nil {
						c.cache = map[string]Weather{}
					}
					c.cache[key] = out
					c.mu.Unlock()
					return out, nil
				}
			}
		}
	}
	if ok {
		cached.StaleAge = time.Now().Unix() - cached.FetchedAt
		cached.Stale = true
		cached.StaleError = fmt.Sprintf("weather: %v", e)
		return cached, nil
	}
	if c.Store != nil {
		if fallback, found := c.fallback(key, lat, lon); found {
			fallback.StaleError = fmt.Sprintf("weather: %v", e)
			return fallback, nil
		}
	}
	return Weather{Hourly: []Row{}, Lat: lat, Lon: lon, Error: fmt.Sprintf("weather: %v", e)}, e
}
func ResampleWatts(watts map[string]float64, offset int) map[int64]float64 {
	sum := map[int64]float64{}
	count := map[int64]int{}
	for stamp, v := range watts {
		t, e := time.ParseInLocation("2006-01-02 15:04:05", stamp, time.FixedZone("device", offset))
		if e != nil {
			continue
		}
		h := t.Unix() / 3600 * 3600
		sum[h] += v
		count[h]++
	}
	for h, n := range count {
		sum[h] /= float64(n)
	}
	return sum
}
func EstimateURL(lat, lon, tilt, azimuth, kw float64, key string) string {
	base := "https://api.forecast.solar/"
	if key != "" {
		base += url.PathEscape(key) + "/"
	}
	return base + "estimate/" + fmt.Sprintf("%g/%g/%g/%g/%g", lat, lon, tilt, azimuth, kw)
}
