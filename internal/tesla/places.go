package tesla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// GeocodeURL is Google's Geocoding API (reverse geocoding by latlng).
var GeocodeURL = "https://maps.googleapis.com/maps/api/geocode/json"

// Place is a stop resolved to a Google place.
type Place struct {
	ID      string `json:"place_id"`
	Address string `json:"address,omitempty"`
}

// PlaceResolver turns coordinates into Google place IDs, which is what the
// car's multi-stop navigation_waypoints_request accepts. Lookups are cached
// by coordinate (about 1 m), so resending a batch costs nothing.
type PlaceResolver struct {
	HTTP   HTTPDoer
	APIKey string

	mu    sync.Mutex
	cache map[string]Place
}

func coordKey(s Stop) string {
	return strconv.FormatFloat(s.Lat, 'f', 5, 64) + "," + strconv.FormatFloat(s.Lng, 'f', 5, 64)
}

// precise result types, best first: an address beats a street or area.
var preciseTypes = "street_address|premise|subpremise"

// Resolve returns the place for one stop.
func (p *PlaceResolver) Resolve(ctx context.Context, s Stop) (Place, error) {
	if p.APIKey == "" {
		return Place{}, errors.New("google maps api key not configured")
	}
	key := coordKey(s)
	p.mu.Lock()
	if pl, ok := p.cache[key]; ok {
		p.mu.Unlock()
		return pl, nil
	}
	p.mu.Unlock()

	pl, err := p.lookup(ctx, key, preciseTypes)
	if errors.Is(err, errNoResults) {
		// Rural drop points may have no address-level match.
		pl, err = p.lookup(ctx, key, "")
	}
	if err != nil {
		return Place{}, err
	}
	p.mu.Lock()
	if p.cache == nil {
		p.cache = map[string]Place{}
	}
	p.cache[key] = pl
	p.mu.Unlock()
	return pl, nil
}

var errNoResults = errors.New("no place found at these coordinates")

func (p *PlaceResolver) lookup(ctx context.Context, latlng, types string) (Place, error) {
	q := url.Values{"latlng": {latlng}, "key": {p.APIKey}}
	if types != "" {
		q.Set("result_type", types)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", GeocodeURL+"?"+q.Encode(), nil)
	if err != nil {
		return Place{}, err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return Place{}, fmt.Errorf("geocode: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Place{}, err
	}
	var out struct {
		Status  string `json:"status"`
		Message string `json:"error_message"`
		Results []struct {
			PlaceID string `json:"place_id"`
			Address string `json:"formatted_address"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Place{}, fmt.Errorf("geocode: http %d: unreadable response", resp.StatusCode)
	}
	switch out.Status {
	case "OK":
	case "ZERO_RESULTS":
		return Place{}, errNoResults
	default:
		msg := out.Status
		if out.Message != "" {
			msg += ": " + out.Message
		}
		return Place{}, fmt.Errorf("geocode: %s", msg)
	}
	for _, r := range out.Results {
		if r.PlaceID != "" {
			return Place{ID: r.PlaceID, Address: r.Address}, nil
		}
	}
	return Place{}, errNoResults
}

// WaypointsString formats place IDs for navigation_waypoints_request: in
// travel order, the last one being the final destination.
func WaypointsString(places []Place) string {
	parts := make([]string, len(places))
	for i, pl := range places {
		parts[i] = "refId:" + pl.ID
	}
	return strings.Join(parts, ",")
}
