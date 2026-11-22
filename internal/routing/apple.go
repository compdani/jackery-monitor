// Package routing evaluates and optimizes whole delivery trips for the
// TeslaHelper app (see TeslaHelper/ROUTE_PLANNING.md).
//
// Road data comes from the Apple Maps Server API, chosen because the app
// draws its maps with Apple Maps (expo-maps on iOS): Apple route geometry
// shown on an Apple map avoids the Google "no use with a non-Google map"
// restriction. Fixed-order evaluation uses /v1/directions per leg; the
// optimizer's travel-time matrix uses /v1/etas.
package routing

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AppleBaseURL is the Apple Maps Server API.
var AppleBaseURL = "https://maps-api.apple.com"

// MaxETADestinations is the most destinations one /v1/etas call accepts.
const MaxETADestinations = 10

// HTTPDoer is satisfied by *http.Client.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Point is a routable location with the caller's stable ID.
type Point struct {
	ID  string  `json:"id"`
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
	// Visits only: time window (unix seconds; 0 = none). Arriving before
	// Earliest waits; arriving after Latest is late.
	Earliest int64 `json:"earliest,omitempty"`
	Latest   int64 `json:"latest,omitempty"`
	// ServiceS overrides the request's per-visit service time.
	ServiceS float64 `json:"service_s,omitempty"`
}

// Cost is a road travel estimate between two points.
type Cost struct {
	DistanceM float64
	DurationS float64
}

// Leg is a road route between two consecutive points.
type Leg struct {
	Cost
	// HasTolls: the route uses a toll road.
	HasTolls bool
	// Path is the road geometry, [lat, lng] pairs.
	Path [][2]float64
}

// Provider supplies road costs and geometry.
type Provider interface {
	// Directions returns the road route from a to b.
	Directions(ctx context.Context, a, b Point) (Leg, error)
	// ETAs returns travel costs from origin to each destination (at most
	// MaxETADestinations), in order.
	ETAs(ctx context.Context, origin Point, dests []Point) ([]Cost, error)
	// Search finds places for a free-text query (forward geocoding).
	Search(ctx context.Context, q string) ([]SearchResult, error)
}

// SearchResult is one forward-geocoding match.
type SearchResult struct {
	Name    string  `json:"name"`
	Address string  `json:"address"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
}

// AppleConfig holds the Maps Server API credentials (a Maps key from the
// Apple Developer account: Certificates, IDs & Profiles → Keys → Maps).
type AppleConfig struct {
	TeamID        string `json:"team_id"`
	KeyID         string `json:"key_id"`
	PrivateKeyPEM string `json:"private_key_pem,omitempty"`
}

// Validate checks the credentials' shape and parses the key.
func (c AppleConfig) Validate() error {
	if len(c.TeamID) != 10 {
		return errors.New("team_id must be the 10-character Apple Team ID")
	}
	if len(c.KeyID) != 10 {
		return errors.New("key_id must be the 10-character Maps key ID")
	}
	_, err := parseECKey(c.PrivateKeyPEM)
	return err
}

func parseECKey(p string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(p)))
	if block == nil {
		return nil, errors.New("private_key_pem is not a PEM key (.p8 file contents)")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if ek, err2 := x509.ParseECPrivateKey(block.Bytes); err2 == nil {
			if ek.Curve != elliptic.P256() {
				return nil, errors.New("private key must be a P-256 (ES256) key")
			}
			return ek, nil
		}
		return nil, fmt.Errorf("private key: %w", err)
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not an EC (ES256) key")
	}
	if ek.Curve != elliptic.P256() {
		// ES256 signatures are 64 bytes; another curve would panic when signing.
		return nil, errors.New("private key must be a P-256 (ES256) key")
	}
	return ek, nil
}

// Apple calls the Apple Maps Server API. Every call is charged to Budget.
type Apple struct {
	Config AppleConfig
	HTTP   HTTPDoer
	Budget *Budget
	Now    func() time.Time

	mu      sync.Mutex
	access  string
	expires time.Time

	// dirs reuses recent Directions results (see DirectionsTTL). Callers
	// treat a Leg's Path as read-only.
	dirs dirCache
}

func (a *Apple) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// authJWT signs the token used to obtain a Maps access token.
func (a *Apple) authJWT() (string, error) {
	key, err := parseECKey(a.Config.PrivateKeyPEM)
	if err != nil {
		return "", err
	}
	now := a.now().Unix()
	head, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": a.Config.KeyID, "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": a.Config.TeamID, "iat": now, "exp": now + 3600, "scope": "server_api"})
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(head) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// verifyJWT is used by tests to check our signatures.
func verifyJWT(tok string, pub *ecdsa.PublicKey) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

func (a *Apple) accessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.access != "" && a.now().Before(a.expires) {
		return a.access, nil
	}
	jwt, err := a.authJWT()
	if err != nil {
		return "", err
	}
	if err := a.Budget.Take(1); err != nil {
		return "", err
	}
	var out struct {
		AccessToken      string `json:"accessToken"`
		ExpiresInSeconds int    `json:"expiresInSeconds"`
	}
	if err := a.do(ctx, "/v1/token", nil, jwt, &out); err != nil {
		return "", fmt.Errorf("apple maps token: %w", err)
	}
	if out.AccessToken == "" {
		return "", errors.New("apple maps token: empty response")
	}
	a.access = out.AccessToken
	// Refresh a minute early.
	a.expires = a.now().Add(time.Duration(max(out.ExpiresInSeconds-60, 60)) * time.Second)
	return a.access, nil
}

// APIError is a non-2xx answer from Apple.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string {
	if e.Status == http.StatusTooManyRequests {
		return "apple maps daily quota reached (429); try again later"
	}
	return fmt.Sprintf("apple maps: http %d: %s", e.Status, e.Msg)
}

func (a *Apple) do(ctx context.Context, path string, q url.Values, bearer string, out any) error {
	u := AppleBaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 160 {
			msg = msg[:160]
		}
		return &APIError{Status: resp.StatusCode, Msg: msg}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unreadable response: %w", err)
	}
	return nil
}

// call charges the budget and makes an authenticated API call, retrying
// once on a stale access token.
func (a *Apple) call(ctx context.Context, path string, q url.Values, out any) error {
	for attempt := 0; ; attempt++ {
		tok, err := a.accessToken(ctx)
		if err != nil {
			return err
		}
		if err := a.Budget.Take(1); err != nil {
			return err
		}
		err = a.do(ctx, path, q, tok, out)
		var ae *APIError
		if attempt == 0 && errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
			a.mu.Lock()
			a.access = ""
			a.mu.Unlock()
			continue
		}
		return err
	}
}

type departureKey struct{}

// WithDeparture asks the provider for travel times at a future departure
// instead of now.
func WithDeparture(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, departureKey{}, t)
}

// DepartureFrom reports the departure set by WithDeparture.
func DepartureFrom(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(departureKey{}).(time.Time)
	return t, ok && !t.IsZero()
}

type avoidTollsKey struct{}

// WithAvoidTolls asks for toll-free directions where a reasonable one
// exists. Apple's ETAs endpoint has no such option, so it affects
// Directions (evaluation and geometry) only.
func WithAvoidTolls(ctx context.Context) context.Context {
	return context.WithValue(ctx, avoidTollsKey{}, true)
}

// AvoidTollsFrom reports whether WithAvoidTolls was set.
func AvoidTollsFrom(ctx context.Context) bool {
	v, _ := ctx.Value(avoidTollsKey{}).(bool)
	return v
}

func withDeparture(ctx context.Context, q url.Values) url.Values {
	if t, ok := DepartureFrom(ctx); ok {
		q.Set("departureDate", t.UTC().Format(time.RFC3339))
	}
	return q
}

func ll(p Point) string {
	return strconv.FormatFloat(p.Lat, 'f', 6, 64) + "," + strconv.FormatFloat(p.Lng, 'f', 6, 64)
}

// Directions implements Provider. Recent identical requests are served
// from memory without an API call.
func (a *Apple) Directions(ctx context.Context, from, to Point) (Leg, error) {
	key := dirKey(ctx, from, to)
	if leg, ok := a.dirs.get(key, a.now()); ok {
		return leg, nil
	}
	leg, err := a.fetchDirections(ctx, from, to)
	if err != nil {
		return Leg{}, err
	}
	a.dirs.put(key, leg, a.now())
	return leg, nil
}

func (a *Apple) fetchDirections(ctx context.Context, from, to Point) (Leg, error) {
	var out struct {
		Routes []struct {
			DistanceMeters  float64 `json:"distanceMeters"`
			DurationSeconds float64 `json:"durationSeconds"`
			HasTolls        bool    `json:"hasTolls"`
			StepIndexes     []int   `json:"stepIndexes"`
		} `json:"routes"`
		Steps []struct {
			StepPathIndex int `json:"stepPathIndex"`
		} `json:"steps"`
		StepPaths [][]struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"stepPaths"`
	}
	q := withDeparture(ctx, url.Values{"origin": {ll(from)}, "destination": {ll(to)}})
	avoid := AvoidTollsFrom(ctx)
	if avoid {
		q.Set("avoid", "Tolls")
	}
	if err := a.call(ctx, "/v1/directions", q, &out); err != nil {
		return Leg{}, err
	}
	if len(out.Routes) == 0 {
		return Leg{}, ErrNoRoute
	}
	// With avoid=Tolls Apple ranks toll-free routes first but may still
	// return only tolled ones; take the first toll-free route if any.
	r := out.Routes[0]
	if avoid {
		for _, c := range out.Routes {
			if !c.HasTolls {
				r = c
				break
			}
		}
	}
	leg := Leg{Cost: Cost{DistanceM: r.DistanceMeters, DurationS: r.DurationSeconds}, HasTolls: r.HasTolls}
	for _, si := range r.StepIndexes {
		if si < 0 || si >= len(out.Steps) {
			continue
		}
		pi := out.Steps[si].StepPathIndex
		if pi < 0 || pi >= len(out.StepPaths) {
			continue
		}
		for _, c := range out.StepPaths[pi] {
			pt := [2]float64{c.Latitude, c.Longitude}
			if n := len(leg.Path); n > 0 && leg.Path[n-1] == pt {
				continue
			}
			leg.Path = append(leg.Path, pt)
		}
	}
	return leg, nil
}

// ErrNoRoute means the provider found no drivable route.
var ErrNoRoute = errors.New("no driving route found")

// ETAs implements Provider.
func (a *Apple) ETAs(ctx context.Context, origin Point, dests []Point) ([]Cost, error) {
	if len(dests) == 0 || len(dests) > MaxETADestinations {
		return nil, fmt.Errorf("etas: 1 to %d destinations", MaxETADestinations)
	}
	parts := make([]string, len(dests))
	for i, d := range dests {
		parts[i] = ll(d)
	}
	var out struct {
		ETAs []struct {
			Destination struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"destination"`
			DistanceMeters            float64 `json:"distanceMeters"`
			ExpectedTravelTimeSeconds float64 `json:"expectedTravelTimeSeconds"`
		} `json:"etas"`
	}
	q := withDeparture(ctx, url.Values{"origin": {ll(origin)}, "destinations": {strings.Join(parts, "|")}})
	if err := a.call(ctx, "/v1/etas", q, &out); err != nil {
		return nil, err
	}
	echoed := make([]Point, len(out.ETAs))
	costs := make([]Cost, len(out.ETAs))
	for j, e := range out.ETAs {
		echoed[j] = Point{Lat: e.Destination.Latitude, Lng: e.Destination.Longitude}
		costs[j] = Cost{DistanceM: e.DistanceMeters, DurationS: e.ExpectedTravelTimeSeconds}
	}
	return matchETAs(dests, echoed, costs), nil
}

// Search implements Provider (Apple /v1/search).
func (a *Apple) Search(ctx context.Context, q string) ([]SearchResult, error) {
	var out struct {
		Results []struct {
			Name                  string   `json:"name"`
			FormattedAddressLines []string `json:"formattedAddressLines"`
			Coordinate            struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"coordinate"`
		} `json:"results"`
	}
	if err := a.call(ctx, "/v1/search", url.Values{"q": {q}}, &out); err != nil {
		return nil, err
	}
	res := make([]SearchResult, 0, len(out.Results))
	for _, r := range out.Results {
		res = append(res, SearchResult{
			Name:    r.Name,
			Address: strings.Join(r.FormattedAddressLines, ", "),
			Lat:     r.Coordinate.Latitude,
			Lng:     r.Coordinate.Longitude,
		})
	}
	if len(res) > 10 {
		res = res[:10]
	}
	return res, nil
}

// etaMatchDeg is how far (~11 m) an echoed destination may be from the one
// sent and still count as the same point.
const etaMatchDeg = 1e-4

// matchETAs assigns answers to destinations. It matches by the echoed
// coordinate first, without trusting the order; when Apple answered every
// destination, any left unmatched (e.g. a snapped coordinate) take the
// answer in the same position. Anything still unanswered is unreachable
// (+Inf).
func matchETAs(dests, echoed []Point, answers []Cost) []Cost {
	costs := make([]Cost, len(dests))
	got := make([]bool, len(dests))
	used := make([]bool, len(echoed))
	for i, d := range dests {
		costs[i] = Cost{DurationS: Inf, DistanceM: Inf}
		for j, e := range echoed {
			if !used[j] && abs(e.Lat-d.Lat) < etaMatchDeg && abs(e.Lng-d.Lng) < etaMatchDeg {
				costs[i], got[i], used[j] = answers[j], true, true
				break
			}
		}
	}
	if len(echoed) == len(dests) {
		for i := range dests {
			if !got[i] && !used[i] {
				costs[i], got[i], used[i] = answers[i], true, true
			}
		}
	}
	return costs
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
