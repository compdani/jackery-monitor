package forecast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type SolarEstimate struct {
	Hourly      []Row   `json:"hourly"`
	FetchedAt   int64   `json:"fetched_at"`
	Source      string  `json:"source,omitempty"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Declination int     `json:"declination"`
	Azimuth     int     `json:"azimuth"`
	KWP         float64 `json:"kwp"`
	Stale       bool    `json:"stale,omitempty"`
	Error       string  `json:"error,omitempty"`
	StaleError  string  `json:"stale_error,omitempty"`
}
type SolarClient struct {
	HTTP       *http.Client
	BaseURL    string
	mu         sync.Mutex
	cache      map[string]SolarEstimate
	retryUntil time.Time
}

// Fetch serializes site requests so simultaneous recorder/controller calls do
// not consume the public IP quota. No request is retried before retry-at.
func (c *SolarClient) Fetch(ctx context.Context, lat, lon float64, tilt, azimuth int, kwp float64, offset int, key string) SolarEstimate {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	site := fmt.Sprintf("%.4f:%.4f:%d:%d:%g:%d", lat, lon, tilt, azimuth, kwp, offset)
	cached, ok := c.cache[site]
	if ok && now.Unix()-cached.FetchedAt < 3600 {
		return cached
	}
	out := SolarEstimate{Hourly: []Row{}, FetchedAt: now.Unix(), Lat: lat, Lon: lon, Declination: tilt, Azimuth: azimuth, KWP: kwp}
	failure := func(msg string, rate bool) SolarEstimate {
		if ok && len(cached.Hourly) > 0 {
			cached.Stale = true
			if rate {
				cached.Error = msg
			} else {
				cached.StaleError = msg
			}
			return cached
		}
		out.Error = msg
		return out
	}
	if now.Before(c.retryUntil) {
		return failure("rate limited (retry-at honored)", true)
	}
	if !ValidLocation(lat, lon) || tilt < 0 || tilt > 90 || azimuth < -180 || azimuth > 180 || kwp <= 0 {
		return failure("invalid solar plane", false)
	}
	endpoint := EstimateURL(lat, lon, float64(tilt), float64(azimuth), kwp, strings.TrimSpace(key))
	if c.BaseURL != "" {
		endpoint = strings.TrimRight(c.BaseURL, "/") + strings.TrimPrefix(endpoint, "https://api.forecast.solar")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return failure("forecast.solar request failed", false)
	}
	req.Header.Set("Accept", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return failure("forecast.solar transport failed", false)
	}
	defer resp.Body.Close()
	var body struct {
		Result struct {
			Watts map[string]json.RawMessage `json:"watts"`
		} `json:"result"`
		Message struct {
			Rate struct {
				Retry string `json:"retry-at"`
			} `json:"ratelimit"`
		} `json:"message"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body)
	if resp.StatusCode == 429 {
		raw := body.Message.Rate.Retry
		if raw == "" {
			raw = resp.Header.Get("X-Ratelimit-Retry-At")
		}
		until, e := time.Parse(time.RFC3339, raw)
		if e != nil {
			until = now.Add(time.Hour)
		}
		c.retryUntil = until
		return failure("rate limited", true)
	}
	if resp.StatusCode != 200 {
		return failure(fmt.Sprintf("forecast.solar HTTP %d", resp.StatusCode), false)
	}
	if err != nil {
		return failure("invalid forecast.solar JSON", false)
	}
	watts := map[string]float64{}
	for stamp, raw := range body.Result.Watts {
		var w float64
		if json.Unmarshal(raw, &w) == nil && string(raw) != "null" {
			watts[stamp] = w
		}
	}
	for ts, w := range ResampleWatts(watts, offset) {
		out.Hourly = append(out.Hourly, Row{"ts": float64(ts), "watts": w})
	}
	sort.Slice(out.Hourly, func(i, j int) bool { return Number(out.Hourly[i], "ts") < Number(out.Hourly[j], "ts") })
	out.Source = "forecast_solar"
	if c.cache == nil {
		c.cache = map[string]SolarEstimate{}
	}
	c.cache[site] = out
	return out
}

// ClearCache drops site estimates after plane, key or location changes. The
// shared rate-limit retry-at remains honored.
func (c *SolarClient) ClearCache() {
	c.mu.Lock()
	c.cache = nil
	c.mu.Unlock()
}
