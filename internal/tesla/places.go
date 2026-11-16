package tesla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	// DistanceM is how far the place is from the stop's coordinates.
	DistanceM float64 `json:"distance_m"`
}

// DefaultPlaceMaxDistanceM is how far a matched address may be from the pin.
const DefaultPlaceMaxDistanceM = 25

// PlaceResolver turns coordinates into Google place IDs, which is what the
// car's multi-stop navigation_waypoints_request accepts.
//
// Only rooftop-accurate addresses are used (not ones estimated along the
// street), and of those the one nearest the pin, within MaxDistanceM.
// Google ranks results by its own relevance, not distance, and Flex pins
// often sit at the curb, so taking the first result picked houses across
// the street. Lookups are cached by coordinate (about 1 m).
type PlaceResolver struct {
	HTTP         HTTPDoer
	APIKey       string
	MaxDistanceM float64

	mu    sync.Mutex
	cache map[string]Place
}

func coordKey(s Stop) string {
	return strconv.FormatFloat(s.Lat, 'f', 5, 64) + "," + strconv.FormatFloat(s.Lng, 'f', 5, 64)
}

// NoMatchError means no rooftop address is close enough to the pin.
type NoMatchError struct {
	MaxDistanceM float64
	Nearest      *Place // nearest rooftop address found, if any
}

func (e *NoMatchError) Error() string {
	msg := fmt.Sprintf("no exact address within %.0f m of the pin", e.MaxDistanceM)
	if e.Nearest != nil {
		msg += fmt.Sprintf(" (nearest: %s, %.0f m away)", e.Nearest.Address, e.Nearest.DistanceM)
	}
	return msg + "; send this batch with GPS instead"
}

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

	pl, err := p.lookup(ctx, s)
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

func (p *PlaceResolver) maxDistance() float64 {
	if p.MaxDistanceM > 0 {
		return p.MaxDistanceM
	}
	return DefaultPlaceMaxDistanceM
}

func (p *PlaceResolver) lookup(ctx context.Context, s Stop) (Place, error) {
	q := url.Values{
		"latlng":        {coordKey(s)},
		"key":           {p.APIKey},
		"result_type":   {"street_address|premise|subpremise"},
		"location_type": {"ROOFTOP"},
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
			PlaceID  string `json:"place_id"`
			Address  string `json:"formatted_address"`
			Geometry struct {
				Location struct {
					Lat float64 `json:"lat"`
					Lng float64 `json:"lng"`
				} `json:"location"`
				LocationType string `json:"location_type"`
			} `json:"geometry"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Place{}, fmt.Errorf("geocode: http %d: unreadable response", resp.StatusCode)
	}
	limit := p.maxDistance()
	switch out.Status {
	case "OK":
	case "ZERO_RESULTS":
		return Place{}, &NoMatchError{MaxDistanceM: limit}
	default:
		msg := out.Status
		if out.Message != "" {
			msg += ": " + out.Message
		}
		return Place{}, fmt.Errorf("geocode: %s", msg)
	}
	var best *Place
	for _, r := range out.Results {
		// The filter is a request; check it anyway.
		if r.PlaceID == "" || (r.Geometry.LocationType != "" && r.Geometry.LocationType != "ROOFTOP") {
			continue
		}
		d := DistanceM(s.Lat, s.Lng, r.Geometry.Location.Lat, r.Geometry.Location.Lng)
		if best == nil || d < best.DistanceM {
			best = &Place{ID: r.PlaceID, Address: r.Address, DistanceM: math.Round(d*10) / 10}
		}
	}
	if best == nil || best.DistanceM > limit {
		return Place{}, &NoMatchError{MaxDistanceM: limit, Nearest: best}
	}
	return *best, nil
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
