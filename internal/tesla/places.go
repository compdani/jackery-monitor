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
	"sort"
	"strconv"
	"strings"
	"sync"
)

// GeocodeURL is Google's Geocoding API (reverse geocoding by latlng).
var GeocodeURL = "https://maps.googleapis.com/maps/api/geocode/json"

// Place is a Google place near a stop.
type Place struct {
	ID      string  `json:"place_id"`
	Address string  `json:"address,omitempty"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
	// DistanceM is how far the place is from the stop's pin.
	DistanceM float64 `json:"distance_m"`
}

// DefaultPlaceMaxDistanceM is how far a matched address may be from the pin.
const DefaultPlaceMaxDistanceM = 30

// MaxCandidates is how many nearby addresses Candidates returns.
const MaxCandidates = 6

// PlaceResolver turns coordinates into Google place IDs, which is what the
// car's multi-stop navigation_waypoints_request accepts.
//
// Only rooftop-accurate addresses are used (not ones estimated along the
// street), and of those the one nearest the pin, within a distance limit.
// Google ranks results by its own relevance, not distance, and Flex pins
// often sit at the curb, so taking the first result picked houses across
// the street. Lookups are cached by coordinate (about 1 m); the limit is
// applied per call, so changing it needs no new lookup.
type PlaceResolver struct {
	HTTP   HTTPDoer
	APIKey string

	mu    sync.Mutex
	cache map[string][]Place
}

func coordKey(s Stop) string {
	return strconv.FormatFloat(s.Lat, 'f', 5, 64) + "," + strconv.FormatFloat(s.Lng, 'f', 5, 64)
}

// LimitOrDefault returns m, or the default limit when m is not positive.
func LimitOrDefault(m float64) float64 {
	if m > 0 {
		return m
	}
	return DefaultPlaceMaxDistanceM
}

// NoMatchError means no rooftop address is close enough to the pin.
type NoMatchError struct {
	MaxDistanceM float64
	Nearest      *Place // nearest rooftop address found, if any
}

func (e *NoMatchError) Error() string {
	msg := fmt.Sprintf("no exact address within %.0f m of the pin", e.MaxDistanceM)
	if e.Nearest != nil {
		msg += fmt.Sprintf(" (nearest: %s, %.1f m away)", e.Nearest.Address, e.Nearest.DistanceM)
	}
	return msg
}

// Match returns the nearest rooftop address within maxDistanceM (0: the
// default) of the stop, or a *NoMatchError.
func (p *PlaceResolver) Match(ctx context.Context, s Stop, maxDistanceM float64) (Place, error) {
	cands, err := p.Candidates(ctx, s)
	if err != nil {
		return Place{}, err
	}
	limit := LimitOrDefault(maxDistanceM)
	if len(cands) == 0 {
		return Place{}, &NoMatchError{MaxDistanceM: limit}
	}
	if cands[0].DistanceM > limit {
		return Place{}, &NoMatchError{MaxDistanceM: limit, Nearest: &cands[0]}
	}
	return cands[0], nil
}

// Candidates returns up to MaxCandidates rooftop addresses near the stop,
// nearest first, with no distance limit.
func (p *PlaceResolver) Candidates(ctx context.Context, s Stop) ([]Place, error) {
	if p.APIKey == "" {
		return nil, errors.New("google maps api key not configured")
	}
	key := coordKey(s)
	p.mu.Lock()
	if c, ok := p.cache[key]; ok {
		p.mu.Unlock()
		return c, nil
	}
	p.mu.Unlock()

	c, err := p.lookup(ctx, s)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.cache == nil {
		p.cache = map[string][]Place{}
	}
	p.cache[key] = c
	p.mu.Unlock()
	return c, nil
}

func (p *PlaceResolver) lookup(ctx context.Context, s Stop) ([]Place, error) {
	q := url.Values{
		"latlng":        {coordKey(s)},
		"key":           {p.APIKey},
		"result_type":   {"street_address|premise|subpremise"},
		"location_type": {"ROOFTOP"},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", GeocodeURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("geocode: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("geocode: http %d: unreadable response", resp.StatusCode)
	}
	switch out.Status {
	case "OK":
	case "ZERO_RESULTS":
		return []Place{}, nil
	default:
		msg := out.Status
		if out.Message != "" {
			msg += ": " + out.Message
		}
		return nil, fmt.Errorf("geocode: %s", msg)
	}
	seen := map[string]bool{}
	var cands []Place
	for _, r := range out.Results {
		// The filter is a request; check it anyway.
		if r.PlaceID == "" || seen[r.PlaceID] || (r.Geometry.LocationType != "" && r.Geometry.LocationType != "ROOFTOP") {
			continue
		}
		seen[r.PlaceID] = true
		d := DistanceM(s.Lat, s.Lng, r.Geometry.Location.Lat, r.Geometry.Location.Lng)
		cands = append(cands, Place{
			ID: r.PlaceID, Address: r.Address,
			Lat: r.Geometry.Location.Lat, Lng: r.Geometry.Location.Lng,
			DistanceM: math.Round(d*10) / 10,
		})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].DistanceM < cands[j].DistanceM })
	if len(cands) > MaxCandidates {
		cands = cands[:MaxCandidates]
	}
	return cands, nil
}

// A match is confident when it is close to the pin and clearly nearer than
// the next address. Flex pins are often the parking spot, so with houses on
// both sides of the street (or next door at a similar distance) the nearest
// address is a guess the driver should check.
const (
	ConfidentWithinM = 12
	ClearLeadRatio   = 1.5
	ClearLeadM       = 5
)

// Confidence reports whether the nearest candidate is a safe match and how
// many addresses lie within limitM of the pin.
func Confidence(cands []Place, limitM float64) (confident bool, nearby int) {
	for _, c := range cands {
		if c.DistanceM <= limitM {
			nearby++
		}
	}
	if len(cands) == 0 || cands[0].DistanceM > ConfidentWithinM {
		return false, nearby
	}
	if len(cands) == 1 {
		return true, nearby
	}
	first, second := cands[0].DistanceM, cands[1].DistanceM
	return second >= first*ClearLeadRatio && second-first >= ClearLeadM, nearby
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
