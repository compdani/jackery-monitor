package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/compdani/jackery-monitor/internal/routing"
)

// lineProvider: travel time is 100 s per 0.001° of latitude difference.
type lineProvider struct{}

func (lineProvider) Directions(_ context.Context, a, b routing.Point) (routing.Leg, error) {
	d := (b.Lat - a.Lat) * 1e5
	if d < 0 {
		d = -d
	}
	return routing.Leg{Cost: routing.Cost{DistanceM: d * 10, DurationS: d}, Path: [][2]float64{{a.Lat, a.Lng}, {b.Lat, b.Lng}}}, nil
}

func (p lineProvider) ETAs(ctx context.Context, o routing.Point, ds []routing.Point) ([]routing.Cost, error) {
	out := make([]routing.Cost, len(ds))
	for i, d := range ds {
		l, _ := p.Directions(ctx, o, d)
		out[i] = l.Cost
	}
	return out, nil
}

func (lineProvider) Search(context.Context, string) ([]routing.SearchResult, error) {
	return []routing.SearchResult{{Name: "Warehouse", Address: "1 Dock Rd", Lat: 40, Lng: -90}}, nil
}

type rejectedSearchProvider struct{ lineProvider }

func (rejectedSearchProvider) Search(context.Context, string) ([]routing.SearchResult, error) {
	return nil, &routing.APIError{Status: 401, Msg: "invalid credentials"}
}

func TestRouteConnectionTestReportsProviderFailure(t *testing.T) {
	s, h, token, _ := teslaSetup(t)
	s.route.Provider = rejectedSearchProvider{}
	w := request(h, "POST", "/api/route/test", `{}`, token)
	if w.Code != 502 || strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("provider rejection reported as success: %d %s", w.Code, w.Body)
	}
}

const zigzag = `{"v":1,"start":{"id":"__start","lat":40,"lng":-90},"finish":{"id":"__finish","lat":40.006,"lng":-90},
 "visits":[{"id":"c","lat":40.003,"lng":-90},{"id":"a","lat":40.001,"lng":-90},{"id":"e","lat":40.005,"lng":-90},{"id":"b","lat":40.002,"lng":-90},{"id":"d","lat":40.004,"lng":-90}],
 "order":["c","a","e","b","d"],"departure":1795000000}`

func TestRouteEndpoints(t *testing.T) {
	s, h, token, _ := teslaSetup(t)
	if w := request(h, "POST", "/api/route/evaluate", zigzag, token); w.Code != 409 || !strings.Contains(w.Body.String(), "not set up") {
		t.Fatalf("unconfigured evaluate %d %s", w.Code, w.Body)
	}
	if w := request(h, "GET", "/api/route/config", "", token); !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatalf("config %s", w.Body)
	}
	if w := request(h, "POST", "/api/route/test", `{}`, token); w.Code != 409 {
		t.Fatalf("unconfigured test %d %s", w.Code, w.Body)
	}
	if w := request(h, "POST", "/api/route/config", `{"team_id":"ABCDE12345","key_id":"short","private_key_pem":"x"}`, token); w.Code != 400 {
		t.Fatalf("bad config accepted %d", w.Code)
	}
	s.route.Provider = lineProvider{}
	if w := request(h, "POST", "/api/route/test", `{}`, token); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("test connection %d %s", w.Code, w.Body)
	}
	if w := request(h, "POST", "/api/route/test", `{}`, ""); w.Code != 401 {
		t.Fatalf("unauthenticated test %d", w.Code)
	}

	w := request(h, "POST", "/api/route/evaluate", zigzag, token)
	if w.Code != 200 {
		t.Fatalf("evaluate %d %s", w.Code, w.Body)
	}
	var ev struct {
		V          int
		Evaluation routing.Evaluation
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ev)
	if ev.V != 1 || len(ev.Evaluation.Legs) != 6 || ev.Evaluation.Legs[5].To != "__finish" || ev.Evaluation.Legs[0].Geometry.Encoding != "polyline5" {
		t.Fatalf("evaluation %s", w.Body)
	}

	w = request(h, "POST", "/api/route/optimize", zigzag, token)
	var opt struct{ Comparison routing.Comparison }
	_ = json.Unmarshal(w.Body.Bytes(), &opt)
	if w.Code != 200 || !opt.Comparison.Improved || strings.Join(opt.Comparison.Proposed.Order, "") != "abcde" {
		t.Fatalf("optimize %d %s", w.Code, w.Body)
	}
	if opt.Comparison.Baseline.TotalDurationS != ev.Evaluation.TotalDurationS {
		t.Fatal("optimize baseline differs from evaluate of the same order")
	}

	bad := strings.Replace(zigzag, `"order":["c","a","e","b","d"]`, `"order":["c","a","e","b"]`, 1)
	if w := request(h, "POST", "/api/route/optimize", bad, token); w.Code != 400 {
		t.Fatalf("incomplete order accepted %d", w.Code)
	}
	if w := request(h, "GET", "/api/route/search?q=warehouse", "", token); w.Code != 200 || !strings.Contains(w.Body.String(), "1 Dock Rd") {
		t.Fatalf("search %d %s", w.Code, w.Body)
	}
	if w := request(h, "POST", "/api/route/evaluate", zigzag, ""); w.Code != 401 {
		t.Fatalf("unauthenticated %d", w.Code)
	}
}

func TestRouteConfigKeepsPrivateKeyAndNeverReturnsIt(t *testing.T) {
	s, h, token, _ := teslaSetup(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	secret := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	body, _ := json.Marshal(map[string]any{"team_id": "ABCDE12345", "key_id": "FGHIJ12345", "private_key_pem": secret, "daily_call_limit": 1234})
	if w := request(h, "POST", "/api/route/config", string(body), token); w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	if w := request(h, "GET", "/api/route/config", "", token); w.Code != 200 || strings.Contains(w.Body.String(), "BEGIN PRIVATE KEY") || strings.Contains(w.Body.String(), "private_key_pem") || !strings.Contains(w.Body.String(), `"has_private_key":true`) {
		t.Fatalf("config leaked or missing status: %d %s", w.Code, w.Body)
	}
	if w := request(h, "POST", "/api/route/config", `{"team_id":"ABCDE12345","key_id":"FGHIJ12345","daily_call_limit":2000}`, token); w.Code != 200 {
		t.Fatalf("retain key %d %s", w.Code, w.Body)
	}
	c, err := s.routeConfig()
	if err != nil || c.PrivateKeyPEM != secret || c.DailyCallLimit != 2000 {
		t.Fatal("saved key was not retained")
	}
}

func TestTeslaNavigateRequestIDIsIdempotent(t *testing.T) {
	s, h, token, fake := teslaSetup(t)
	connectTesla(t, h, token)
	request(h, "POST", "/api/tesla/config", `{"client_id":"cid","vin":"5YJ3E1EA7KF000000","domain":"j.cdani.net","nav_transport":"rest","google_api_key":"gk"}`, token)
	body := `{"request_id":"req-0001-abc","stops":[{"lat":30.1,"lng":-97.1}]}`
	fake.commands = nil
	w := request(h, "POST", "/api/tesla/navigate", body, token)
	var first teslaJob
	_ = json.Unmarshal(w.Body.Bytes(), &first)
	if w.Code != 200 || first.RequestID != "req-0001-abc" || len(fake.commands) != 1 {
		t.Fatalf("first %d %s", w.Code, w.Body)
	}
	w = request(h, "POST", "/api/tesla/navigate", body, token)
	var again teslaJob
	_ = json.Unmarshal(w.Body.Bytes(), &again)
	if again.ID != first.ID || len(fake.commands) != 1 {
		t.Fatalf("repeat request sent again: %s %v", w.Body, fake.commands)
	}
	if w := request(h, "GET", "/api/tesla/navigate/job?request_id=req-0001-abc", "", token); w.Code != 200 || !strings.Contains(w.Body.String(), first.ID) {
		t.Fatalf("lookup %d %s", w.Code, w.Body)
	}
	if w := request(h, "GET", "/api/tesla/navigate/job?id="+first.ID, "", token); w.Code != 200 {
		t.Fatalf("lookup by id %d", w.Code)
	}
	if w := request(h, "GET", "/api/tesla/navigate/job?request_id=never-sent-1", "", token); w.Code != 404 {
		t.Fatalf("unknown request %d", w.Code)
	}
	if w := request(h, "POST", "/api/tesla/navigate", `{"request_id":"x","stops":[{"lat":30.1,"lng":-97.1}]}`, token); w.Code != 400 {
		t.Fatalf("bad request_id %d", w.Code)
	}
	// A job recorded as running by a previous process reports "unknown".
	_ = s.Store.Put("events", "tesla_nav_req:req-restart-1", "vin", map[string]any{"id": "old", "request_id": "req-restart-1", "status": "running"})
	if w := request(h, "GET", "/api/tesla/navigate/job?request_id=req-restart-1", "", token); !strings.Contains(w.Body.String(), `"status":"unknown"`) {
		t.Fatalf("restart lookup %s", w.Body)
	}
}

// optsProvider records the options each Directions call saw.
type optsProvider struct {
	lineProvider
	mu         sync.Mutex
	departures []time.Time
	tolls      []bool
}

func (p *optsProvider) Directions(ctx context.Context, a, b routing.Point) (routing.Leg, error) {
	d, _ := routing.DepartureFrom(ctx)
	p.mu.Lock()
	p.departures = append(p.departures, d)
	p.tolls = append(p.tolls, routing.AvoidTollsFrom(ctx))
	p.mu.Unlock()
	return p.lineProvider.Directions(ctx, a, b)
}

func TestRouteDepartureAndAvoidTolls(t *testing.T) {
	s, h, token, _ := teslaSetup(t)
	p := &optsProvider{}
	s.route.Provider = p
	future := time.Now().Add(3 * time.Hour).Unix()
	body := strings.Replace(zigzag, `"departure":1795000000`, fmt.Sprintf(`"departure":%d,"avoid":["tolls"]`, future), 1)
	w := request(h, "POST", "/api/route/evaluate", body, token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"departure":%d`, future)) {
		t.Fatalf("future departure %d %s", w.Code, w.Body)
	}
	for i := range p.departures {
		if p.departures[i].Unix() != future || !p.tolls[i] {
			t.Fatalf("leg %d saw departure %v tolls %v", i, p.departures[i], p.tolls[i])
		}
	}
	// A past departure is routed as now.
	p.departures, p.tolls = nil, nil
	if w := request(h, "POST", "/api/route/evaluate", zigzag, token); w.Code != 200 || !strings.Contains(w.Body.String(), `"departure":0`) || !p.departures[0].IsZero() || p.tolls[0] {
		t.Fatalf("past departure %d %s", w.Code, w.Body)
	}
	far := strings.Replace(zigzag, `"departure":1795000000`, fmt.Sprintf(`"departure":%d`, time.Now().Add(30*24*time.Hour).Unix()), 1)
	if w := request(h, "POST", "/api/route/evaluate", far, token); w.Code != 400 {
		t.Fatalf("far departure accepted %d", w.Code)
	}
	hw := strings.Replace(zigzag, `"departure":1795000000`, `"departure":0,"avoid":["highways"]`, 1)
	if w := request(h, "POST", "/api/route/evaluate", hw, token); w.Code != 400 {
		t.Fatalf("unsupported avoid accepted %d", w.Code)
	}
}

func TestRouteBudgetPersists(t *testing.T) {
	s, h, token, _ := teslaSetup(t)
	b := s.routeBudget()
	if err := b.Take(42); err != nil {
		t.Fatal(err)
	}
	s.route.Provider = lineProvider{}
	// Any route endpoint flushes the count.
	request(h, "GET", "/api/route/search?q=warehouse", "", token)
	day := time.Now().UTC().Format("2006-01-02")
	m, err := s.Store.Get("events", "route_budget:"+day)
	if err != nil || m["used"] != float64(42) {
		t.Fatalf("stored %v %v", m, err)
	}
	restarted := &routing.Budget{Store: routeBudgetStore{s}}
	if used, _ := restarted.Usage(); used != 42 {
		t.Fatalf("restart sees %d", used)
	}
}

func TestTeslaNavigateSupersedeGuard(t *testing.T) {
	s, h, token, fake := teslaSetup(t)
	connectTesla(t, h, token)
	request(h, "POST", "/api/tesla/config", `{"client_id":"cid","vin":"5YJ3E1EA7KF000000","domain":"j.cdani.net","nav_transport":"rest","google_api_key":"gk"}`, token)
	fake.commands = nil

	s.tesla.mu.Lock()
	s.tesla.byRequest = map[string]*teslaJob{"req-running-1": {ID: "r", RequestID: "req-running-1", Status: "running"}}
	s.tesla.mu.Unlock()
	w := request(h, "POST", "/api/tesla/navigate", `{"request_id":"req-next-0001","supersedes_request_id":"req-running-1","stops":[{"lat":30.1,"lng":-97.1}]}`, token)
	if w.Code != 409 || len(fake.commands) != 0 {
		t.Fatalf("sent over a running batch: %d %s", w.Code, w.Body)
	}

	_ = s.Store.Put("events", "tesla_nav_req:req-restart-2", "vin", map[string]any{"id": "old", "request_id": "req-restart-2", "status": "running"})
	body := `{"request_id":"req-next-0002","supersedes_request_id":"req-restart-2","stops":[{"lat":30.1,"lng":-97.1}]}`
	w = request(h, "POST", "/api/tesla/navigate", body, token)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"needs_confirm":true`) || len(fake.commands) != 0 {
		t.Fatalf("unknown outcome not held: %d %s", w.Code, w.Body)
	}
	body = strings.Replace(body, `"stops"`, `"confirm_unknown":true,"stops"`, 1)
	w = request(h, "POST", "/api/tesla/navigate", body, token)
	if w.Code != 200 || len(fake.commands) != 1 || !strings.Contains(w.Body.String(), `"supersedes_request_id":"req-restart-2"`) {
		t.Fatalf("confirmed replacement %d %s", w.Code, w.Body)
	}
	// A settled or never-received previous batch does not block.
	w = request(h, "POST", "/api/tesla/navigate", `{"request_id":"req-next-0003","supersedes_request_id":"req-next-0002","stops":[{"lat":30.2,"lng":-97.2}]}`, token)
	if w.Code != 200 {
		t.Fatalf("after settled batch %d %s", w.Code, w.Body)
	}
	w = request(h, "POST", "/api/tesla/navigate", `{"request_id":"req-next-0004","supersedes_request_id":"never-arrived","stops":[{"lat":30.2,"lng":-97.2}]}`, token)
	if w.Code != 200 {
		t.Fatalf("after never-received batch %d %s", w.Code, w.Body)
	}
}
