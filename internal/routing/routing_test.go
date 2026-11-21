package routing

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	rand2 "crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// gridProvider: travel time is Manhattan distance on a grid (1 unit = 100 s),
// so the optimum is easy to reason about. Points listed in blocked have no route.
type gridProvider struct {
	mu      sync.Mutex
	calls   int
	etas    int
	blocked map[string]bool
}

func cost(a, b Point) Cost {
	d := (math.Abs(a.Lat-b.Lat) + math.Abs(a.Lng-b.Lng)) * 1000
	return Cost{DistanceM: d * 10, DurationS: d * 100}
}

func (g *gridProvider) Directions(_ context.Context, a, b Point) (Leg, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	if g.blocked[b.ID] {
		return Leg{}, ErrNoRoute
	}
	return Leg{Cost: cost(a, b), Path: [][2]float64{{a.Lat, a.Lng}, {a.Lat, b.Lng}, {b.Lat, b.Lng}}}, nil
}

func (g *gridProvider) ETAs(_ context.Context, o Point, ds []Point) ([]Cost, error) {
	g.mu.Lock()
	g.etas++
	g.mu.Unlock()
	if len(ds) > MaxETADestinations {
		return nil, errors.New("too many")
	}
	out := make([]Cost, len(ds))
	for i, d := range ds {
		out[i] = cost(o, d)
	}
	return out, nil
}

func (g *gridProvider) Search(context.Context, string) ([]SearchResult, error) { return nil, nil }

func pt(id string, x, y float64) Point { return Point{ID: id, Lat: 40 + x/1000, Lng: -90 + y/1000} }

func req(visits []Point, finish *Point) Request {
	order := make([]string, len(visits))
	for i, v := range visits {
		order[i] = v.ID
	}
	return Request{V: 1, Start: pt("__start", 0, 0), Finish: finish, Visits: visits, Order: order}
}

func TestValidateRejectsBadOrders(t *testing.T) {
	r := req([]Point{pt("a", 1, 0), pt("b", 2, 0)}, nil)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(Request) Request{
		"missing":  func(r Request) Request { r.Order = []string{"a"}; return r },
		"dup":      func(r Request) Request { r.Order = []string{"a", "a"}; return r },
		"unknown":  func(r Request) Request { r.Order = []string{"a", "zz"}; return r },
		"dupvisit": func(r Request) Request { r.Visits = []Point{pt("a", 1, 0), pt("a", 2, 0)}; return r },
		"version":  func(r Request) Request { r.V = 2; return r },
		"reserved": func(r Request) Request { r.Visits[0].ID = "__start"; r.Order[0] = "__start"; return r },
	}
	for name, f := range cases {
		c := req([]Point{pt("a", 1, 0), pt("b", 2, 0)}, nil)
		if err := f(c).Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Two distinct visits at identical coordinates are fine.
	same := req([]Point{pt("a", 1, 0), pt("b", 1, 0)}, nil)
	if err := same.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateIncludesFinishLegAndGeometry(t *testing.T) {
	fin := pt("__finish", 0, 0) // round trip
	r := req([]Point{pt("a", 1, 0), pt("b", 1, 1)}, &fin)
	ev, err := Evaluate(context.Background(), &gridProvider{}, r, r.Order)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Legs) != 3 || ev.Legs[2].From != "b" || ev.Legs[2].To != "__finish" {
		t.Fatalf("legs %+v", ev.Legs)
	}
	if ev.TotalDurationS != 400 || !ev.FinishIncluded {
		t.Fatalf("total %v", ev.TotalDurationS)
	}
	if got := DecodePolyline(ev.Legs[0].Geometry.Points); len(got) != 3 || math.Abs(got[2][0]-40.001) > 1e-6 {
		t.Fatalf("geometry %v", got)
	}
}

func TestEvaluateZeroStopRoundTripMakesNoCalls(t *testing.T) {
	fin := pt("__finish", 0, 0)
	g := &gridProvider{}
	ev, err := Evaluate(context.Background(), g, req(nil, &fin), nil)
	if err != nil || g.calls != 0 || len(ev.Legs) != 1 || ev.TotalDurationS != 0 {
		t.Fatalf("%v %d %+v", err, g.calls, ev)
	}
}

func TestEvaluateReportsUnreachableIDs(t *testing.T) {
	r := req([]Point{pt("a", 1, 0), pt("b", 2, 0)}, nil)
	_, err := Evaluate(context.Background(), &gridProvider{blocked: map[string]bool{"b": true}}, r, r.Order)
	var ue *UnreachableError
	if !errors.As(err, &ue) || len(ue.IDs) != 1 || ue.IDs[0] != "b" {
		t.Fatalf("%v", err)
	}
}

func TestOptimizeFindsBetterOrderAndKeepsEveryID(t *testing.T) {
	// Collected zig-zag along a line; best is 1,2,3,4,5 then home.
	visits := []Point{pt("v3", 3, 0), pt("v1", 1, 0), pt("v5", 5, 0), pt("v2", 2, 0), pt("v4", 4, 0)}
	fin := pt("__finish", 6, 0)
	r := req(visits, &fin)
	cmp, err := Optimize(context.Background(), &gridProvider{}, r)
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Improved || cmp.Proposed == nil {
		t.Fatalf("no improvement: %+v", cmp)
	}
	if got := strings.Join(cmp.Proposed.Order, ","); got != "v1,v2,v3,v4,v5" {
		t.Fatalf("order %s", got)
	}
	if cmp.Proposed.TotalDurationS >= cmp.Baseline.TotalDurationS {
		t.Fatal("proposal not better")
	}
	if cmp.SavingS != cmp.Baseline.TotalDurationS-cmp.Proposed.TotalDurationS {
		t.Fatal("saving mismatch")
	}
}

func TestOptimizeKeepsOrderWhenAlreadyBest(t *testing.T) {
	visits := []Point{pt("v1", 1, 0), pt("v2", 2, 0), pt("v3", 3, 0)}
	cmp, err := Optimize(context.Background(), &gridProvider{}, req(visits, nil))
	if err != nil || cmp.Improved || cmp.Proposed != nil {
		t.Fatalf("%v %+v", err, cmp)
	}
}

func TestOptimizeLargeTripUsesNeighbourMatrix(t *testing.T) {
	var visits []Point
	for i := 60; i >= 1; i-- { // reverse order: worst case
		visits = append(visits, pt(fmt.Sprintf("v%d", i), float64(i%10), float64(i/10)))
	}
	r := req(visits, nil)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	g := &gridProvider{}
	cmp, err := Optimize(context.Background(), g, r)
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Matrix.Complete || cmp.Matrix.Neighbors != Neighbors {
		t.Fatalf("matrix %+v", cmp.Matrix)
	}
	if !cmp.Improved {
		t.Fatal("expected improvement on a reversed snake")
	}
	seen := map[string]bool{}
	for _, id := range cmp.Proposed.Order {
		seen[id] = true
	}
	if len(seen) != 60 || len(cmp.Proposed.Order) != 60 {
		t.Fatal("proposal lost or repeated visits")
	}
	if est := CallsForOptimize(r); est > MaxCallsPerRun || g.etas+g.calls+1 > est {
		t.Fatalf("estimate %d, used %d", est, g.etas+g.calls)
	}
}

func TestHundredVisitEstimateFitsPerRunCap(t *testing.T) {
	var visits []Point
	for i := 0; i < MaxVisits; i++ {
		visits = append(visits, pt(fmt.Sprintf("v%d", i), float64(i%10), float64(i/10)))
	}
	fin := pt("__finish", 0, 0)
	if n := CallsForOptimize(req(visits, &fin)); n > MaxCallsPerRun {
		t.Fatalf("100 visits need %d calls", n)
	}
}

func TestBudget(t *testing.T) {
	day := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	b := &Budget{Limit: 3, Now: func() time.Time { return day }}
	if b.Take(2) != nil || b.Check(2) == nil || b.Take(2) == nil {
		t.Fatal("cap not applied")
	}
	day = day.Add(24 * time.Hour)
	if b.Take(3) != nil {
		t.Fatal("cap did not reset next day")
	}
	if (&Budget{}).Check(MaxCallsPerRun+1) == nil {
		t.Fatal("per-run cap not applied")
	}
}

func TestSimplifyKeepsEndsAndLimit(t *testing.T) {
	var pts [][2]float64
	for i := 0; i < 1000; i++ {
		pts = append(pts, [2]float64{40 + float64(i)*1e-4, -90 + math.Sin(float64(i)/20)*1e-3})
	}
	s := Simplify(pts, 100)
	if len(s) > 100 || s[0] != pts[0] || s[len(s)-1] != pts[999] {
		t.Fatalf("len %d", len(s))
	}
}

// appleFake checks auth and answers token, directions, etas and search.
type appleFake struct {
	pub     *ecdsa.PublicKey
	tokens  int
	paths   []string
	queries map[string]url.Values
}

func (f *appleFake) Do(r *http.Request) (*http.Response, error) {
	reply := func(code int, s string) (*http.Response, error) {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(s)), Header: http.Header{}}, nil
	}
	f.paths = append(f.paths, r.URL.Path)
	if f.queries == nil {
		f.queries = map[string]url.Values{}
	}
	f.queries[r.URL.Path] = r.URL.Query()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch r.URL.Path {
	case "/v1/token":
		if !verifyJWT(auth, f.pub) {
			return reply(401, `{"error":"bad jwt"}`)
		}
		f.tokens++
		return reply(200, `{"accessToken":"acc","expiresInSeconds":1800}`)
	}
	if auth != "acc" {
		return reply(401, `{}`)
	}
	switch r.URL.Path {
	case "/v1/directions":
		if r.URL.Query().Get("avoid") == "Tolls" {
			// Apple may still list a tolled route first.
			return reply(200, `{"routes":[{"distanceMeters":1000,"durationSeconds":150,"hasTolls":true,"stepIndexes":[]},
			  {"distanceMeters":1500,"durationSeconds":240,"hasTolls":false,"stepIndexes":[]}],"steps":[],"stepPaths":[]}`)
		}
		return reply(200, `{"routes":[{"distanceMeters":1200,"durationSeconds":180,"stepIndexes":[0,1]}],
		  "steps":[{"stepPathIndex":0},{"stepPathIndex":1}],
		  "stepPaths":[[{"latitude":40,"longitude":-90},{"latitude":40.001,"longitude":-90}],[{"latitude":40.001,"longitude":-90},{"latitude":40.001,"longitude":-90.002}]]}`)
	case "/v1/etas":
		ds := strings.Split(r.URL.Query().Get("destinations"), "|")
		var parts []string
		for i := len(ds) - 1; i >= 0; i-- { // reversed: matched by coordinate
			var lat, lng float64
			fmt.Sscanf(ds[i], "%f,%f", &lat, &lng)
			parts = append(parts, fmt.Sprintf(`{"destination":{"latitude":%f,"longitude":%f},"distanceMeters":%d,"expectedTravelTimeSeconds":%d}`, lat, lng, (i+1)*100, (i+1)*10))
		}
		return reply(200, `{"etas":[`+strings.Join(parts, ",")+`]}`)
	case "/v1/search":
		return reply(200, `{"results":[{"name":"Home","formattedAddressLines":["1 Main St","Town"],"coordinate":{"latitude":41.5,"longitude":-87.4}}]}`)
	}
	return reply(404, `{}`)
}

func TestAppleProvider(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand2.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	cfg := AppleConfig{TeamID: "ABCDE12345", KeyID: "KEY1234567", PrivateKeyPEM: pemKey}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	f := &appleFake{pub: &key.PublicKey}
	a := &Apple{Config: cfg, HTTP: f, Budget: &Budget{Limit: 100}}
	ctx := context.Background()
	leg, err := a.Directions(ctx, pt("a", 0, 0), pt("b", 1, 0))
	if err != nil || leg.DurationS != 180 || len(leg.Path) != 3 {
		t.Fatalf("%v %+v", err, leg)
	}
	costs, err := a.ETAs(ctx, pt("a", 0, 0), []Point{pt("b", 1, 0), pt("c", 2, 0)})
	if err != nil || costs[0].DurationS != 10 || costs[1].DurationS != 20 {
		t.Fatalf("%v %+v", err, costs)
	}
	res, err := a.Search(ctx, "home")
	if err != nil || len(res) != 1 || res[0].Address != "1 Main St, Town" {
		t.Fatalf("%v %+v", err, res)
	}
	if f.tokens != 1 {
		t.Fatalf("token fetched %d times", f.tokens)
	}
	used, _ := a.Budget.Usage()
	if used != 4 {
		t.Fatalf("budget used %d", used)
	}
	var claims map[string]any
	jwt, _ := a.authJWT()
	raw, _ := decodeSeg(strings.Split(jwt, ".")[1])
	_ = json.Unmarshal(raw, &claims)
	if claims["scope"] != "server_api" || claims["iss"] != "ABCDE12345" {
		t.Fatalf("claims %v", claims)
	}
	if (AppleConfig{TeamID: "x"}).Validate() == nil {
		t.Fatal("bad config accepted")
	}
}

func decodeSeg(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

func brute(m [][]float64, visits []int, fin int) float64 {
	best := Inf
	var rec func(o []int, left []int)
	rec = func(o []int, left []int) {
		if len(left) == 0 {
			best = math.Min(best, tourCost(m, o, fin))
			return
		}
		for i := range left {
			rest := append(append([]int{}, left[:i]...), left[i+1:]...)
			rec(append(o, left[i]), rest)
		}
	}
	rec(nil, visits)
	return best
}

func TestExactMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := 0; trial < 20; trial++ {
		n := 2 + trial%6
		size := n + 2
		m := make([][]float64, size)
		for i := range m {
			m[i] = make([]float64, size)
			for j := range m[i] {
				if i != j {
					m[i][j] = float64(1 + rng.IntN(1000)) // asymmetric
				}
			}
		}
		visits := make([]int, n)
		for i := range visits {
			visits[i] = i + 1
		}
		for _, fin := range []int{-1, n + 1} {
			got := exact(plainProblem(m, fin), visits)
			if len(got) != n {
				t.Fatalf("exact returned %v", got)
			}
			seen := map[int]bool{}
			for _, v := range got {
				seen[v] = true
			}
			if len(seen) != n {
				t.Fatalf("exact lost visits: %v", got)
			}
			if c, b := tourCost(m, got, fin), brute(m, visits, fin); math.Abs(c-b) > 1e-9 {
				t.Fatalf("trial %d fin %d: exact %v brute %v", trial, fin, c, b)
			}
		}
	}
}

func TestOptimizeLoopReachesKnownOptimum(t *testing.T) {
	// 4x5 lattice, start and finish at the corner, visits shuffled. A
	// closed tour of 20 lattice points in unit steps (100 s) is optimal.
	var visits []Point
	for x := 0; x < 4; x++ {
		for y := 0; y < 5; y++ {
			if x == 0 && y == 0 {
				continue
			}
			visits = append(visits, pt(fmt.Sprintf("p%d%d", x, y), float64(x), float64(y)))
		}
	}
	rng := rand.New(rand.NewPCG(7, 7))
	rng.Shuffle(len(visits), func(i, j int) { visits[i], visits[j] = visits[j], visits[i] })
	home := pt("__finish", 0, 0)
	cmp, err := Optimize(context.Background(), &gridProvider{}, req(visits, &home))
	if err != nil || cmp.Proposed == nil {
		t.Fatalf("%v %+v", err, cmp.Message)
	}
	if math.Abs(cmp.Proposed.TotalDurationS-2000) > 1 {
		t.Fatalf("loop %v s, optimum 2000 s", cmp.Proposed.TotalDurationS)
	}
}

func TestDoubleBridgeIsAPermutation(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	o := []int{1, 2, 3, 4, 5, 6, 7, 8, 9}
	for i := 0; i < 200; i++ {
		got := doubleBridge(o, rng)
		seen := map[int]bool{}
		for _, v := range got {
			seen[v] = true
		}
		if len(got) != len(o) || len(seen) != len(o) {
			t.Fatalf("%v", got)
		}
	}
}

func TestMatchETAs(t *testing.T) {
	ds := []Point{{Lat: 40.1, Lng: -90}, {Lat: 40.2, Lng: -90}}
	a := []Cost{{DurationS: 1}, {DurationS: 2}}
	// reversed answers matched by coordinate
	got := matchETAs(ds, []Point{ds[1], ds[0]}, a)
	if got[0].DurationS != 2 || got[1].DurationS != 1 {
		t.Fatalf("by coordinate %+v", got)
	}
	// snapped coordinates fall back to position when all were answered
	snapped := []Point{{Lat: 40.1003, Lng: -90}, {Lat: 40.2004, Lng: -90}}
	got = matchETAs(ds, snapped, a)
	if got[0].DurationS != 1 || got[1].DurationS != 2 {
		t.Fatalf("by position %+v", got)
	}
	// a missing answer is unreachable, not guessed
	got = matchETAs(ds, snapped[:1], a[:1])
	if !math.IsInf(got[0].DurationS, 1) || !math.IsInf(got[1].DurationS, 1) {
		t.Fatalf("partial %+v", got)
	}
}

// strictProvider refuses zero-length ETAs, as a real provider may.
type strictProvider struct{ gridProvider }

func (s *strictProvider) ETAs(ctx context.Context, o Point, ds []Point) ([]Cost, error) {
	for _, d := range ds {
		if d.Lat == o.Lat && d.Lng == o.Lng {
			return nil, errors.New("origin equals destination")
		}
	}
	return s.gridProvider.ETAs(ctx, o, ds)
}

func TestOptimizeCoLocatedStops(t *testing.T) {
	// Two parcels for one building, one at the start point.
	visits := []Point{pt("b1", 3, 0), pt("far", 9, 0), pt("b2", 3, 0), pt("here", 0, 0), pt("mid", 5, 0)}
	home := pt("__finish", 0, 0)
	cmp, err := Optimize(context.Background(), &strictProvider{}, req(visits, &home))
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Proposed == nil || cmp.Proposed.TotalDurationS != 1800 {
		t.Fatalf("%+v", cmp)
	}
}

type memBudgetStore map[string]int

func (m memBudgetStore) LoadUsage(day string) (int, error)    { return m[day], nil }
func (m memBudgetStore) SaveUsage(day string, used int) error { m[day] = used; return nil }

func TestBudgetPersistsAcrossRestart(t *testing.T) {
	day := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return day }
	st := memBudgetStore{}
	b := &Budget{Limit: 10, Now: now, Store: st}
	if b.Take(7) != nil || b.Flush() != nil || st["2026-11-20"] != 7 {
		t.Fatalf("not saved: %v", st)
	}
	restarted := &Budget{Limit: 10, Now: now, Store: st}
	if used, _ := restarted.Usage(); used != 7 {
		t.Fatalf("restart lost usage: %d", used)
	}
	if restarted.Take(4) == nil {
		t.Fatal("cap ignored saved usage")
	}
}

func TestValidateAvoid(t *testing.T) {
	r := req([]Point{pt("a", 1, 0)}, nil)
	r.Avoid = []string{"tolls"}
	if r.Validate() != nil || !r.AvoidTolls() {
		t.Fatal("tolls rejected")
	}
	r.Avoid = []string{"highways"}
	if r.Validate() == nil {
		t.Fatal("unsupported avoid accepted")
	}
}

func TestAppleDirectionsAvoidTollsAndDeparture(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand2.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	f := &appleFake{pub: &key.PublicKey}
	a := &Apple{Config: AppleConfig{TeamID: "ABCDE12345", KeyID: "KEY1234567", PrivateKeyPEM: pemKey}, HTTP: f, Budget: &Budget{Limit: 100}}
	dep := time.Date(2026, 11, 21, 15, 4, 0, 0, time.UTC)
	ctx := WithAvoidTolls(WithDeparture(context.Background(), dep))
	leg, err := a.Directions(ctx, pt("a", 0, 0), pt("b", 1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if leg.HasTolls || leg.DurationS != 240 {
		t.Fatalf("took tolled route: %+v", leg.Cost)
	}
	q := f.queries["/v1/directions"]
	if q.Get("avoid") != "Tolls" || q.Get("departureDate") != "2026-11-21T15:04:00Z" {
		t.Fatalf("query %v", q)
	}
	if _, err := a.ETAs(ctx, pt("a", 0, 0), []Point{pt("b", 1, 0)}); err != nil {
		t.Fatal(err)
	}
	if q := f.queries["/v1/etas"]; q.Get("departureDate") == "" || q.Get("avoid") != "" {
		t.Fatalf("etas query %v", q)
	}
}

func TestOptimizeHonoursDeadline(t *testing.T) {
	// v5 lies the other way from the start and must be reached within
	// 400 s: the current order (1..4 then 5, arriving at 1100 s) is late,
	// so v5 goes first.
	const dep = 1_800_000_000
	visits := []Point{pt("v1", 1, 0), pt("v2", 2, 0), pt("v3", 3, 0), pt("v4", 4, 0), pt("v5", 0, -3)}
	r := req(visits, nil)
	r.Departure = dep
	r.Visits[4].Latest = dep + 400
	base, err := Evaluate(context.Background(), &gridProvider{}, r, r.Order)
	if err != nil || base.LateS == 0 || len(base.Late) != 1 {
		t.Fatalf("baseline should be late: %+v %v", base, err)
	}
	cmp, err := Optimize(context.Background(), &gridProvider{}, r)
	if err != nil || cmp.Proposed == nil {
		t.Fatalf("%v %+v", err, cmp.Message)
	}
	if cmp.Proposed.LateS != 0 || cmp.Proposed.Order[0] != "v5" {
		t.Fatalf("order %v late %v", cmp.Proposed.Order, cmp.Proposed.LateS)
	}
}

func TestEvaluateScheduleWaitAndService(t *testing.T) {
	const dep = 1_800_000_000
	r := req([]Point{pt("a", 1, 0), pt("b", 2, 0)}, nil)
	r.Departure = dep
	r.ServiceS = 60
	r.Visits[1].Earliest = dep + 1000 // opens late: wait
	ev, err := Evaluate(context.Background(), &gridProvider{}, r, r.Order)
	if err != nil {
		t.Fatal(err)
	}
	// a at 100, served till 160, b at 260, waits till 1000, served till 1060
	if ev.Legs[0].ArriveS != 100 || ev.Legs[1].ArriveS != 260 || ev.Legs[1].WaitS != 740 || ev.FinishS != 1060 || ev.TotalServiceS != 120 {
		t.Fatalf("%+v", ev)
	}
}

func TestOptimizeLockFirstKeepsPrefix(t *testing.T) {
	visits := []Point{pt("v5", 5, 0), pt("v3", 3, 0), pt("v1", 1, 0), pt("v4", 4, 0), pt("v2", 2, 0)}
	r := req(visits, nil)
	r.LockFirst = 1
	cmp, err := Optimize(context.Background(), &gridProvider{}, r)
	if err != nil || cmp.Proposed == nil {
		t.Fatalf("%v %+v", err, cmp.Message)
	}
	if got := strings.Join(cmp.Proposed.Order, ","); got != "v5,v4,v3,v2,v1" {
		t.Fatalf("order %s", got)
	}
	bad := r
	bad.LockFirst = 9
	if bad.Validate() == nil {
		t.Fatal("lock_first past the visits accepted")
	}
}

func TestExactWithDeadlinesMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for trial := 0; trial < 30; trial++ {
		n := 3 + trial%5
		size := n + 1
		p := plainProblem(make([][]float64, size), -1)
		for i := range p.m {
			p.m[i] = make([]float64, size)
			for j := range p.m[i] {
				if i != j {
					p.m[i][j] = float64(1 + rng.IntN(300))
				}
			}
		}
		for v := 1; v <= n; v++ {
			p.service[v] = float64(rng.IntN(60))
			if rng.IntN(3) == 0 {
				p.close[v] = float64(200 + rng.IntN(800))
			}
			if rng.IntN(4) == 0 {
				p.open[v] = float64(rng.IntN(500))
			}
		}
		visits := make([]int, n)
		for i := range visits {
			visits[i] = i + 1
		}
		// brute force over feasible (on-time) orders
		best := Inf
		var rec func(o, left []int)
		rec = func(o, left []int) {
			if len(left) == 0 {
				if lateOf(p, o) == 0 {
					best = math.Min(best, p.cost(o))
				}
				return
			}
			for i := range left {
				rest := append(append([]int{}, left[:i]...), left[i+1:]...)
				rec(append(append([]int{}, o...), left[i]), rest)
			}
		}
		rec(nil, visits)
		got := exact(p, visits)
		if math.IsInf(best, 1) {
			if got != nil {
				t.Fatalf("trial %d: exact found %v where none is on time", trial, got)
			}
			continue
		}
		if got == nil || math.Abs(p.cost(got)-best) > 1e-9 || lateOf(p, got) != 0 {
			t.Fatalf("trial %d: exact %v (%v) brute %v", trial, got, func() float64 {
				if got == nil {
					return -1
				}
				return p.cost(got)
			}(), best)
		}
	}
}

func lateOf(p *problem, order []int) float64 {
	t, late, prev := p.t0, 0.0, p.origin
	for _, v := range order {
		if a := t + p.m[prev][v]; a > p.close[v] {
			late += a - p.close[v]
		}
		t = p.step(t, prev, v)
		prev = v
	}
	return late
}

// detourProvider: the "highway" leg a -> b is fast but long, so the fastest
// order and the shortest order differ.
type detourProvider struct{ gridProvider }

func (d *detourProvider) adjust(a, b Point, c Cost) Cost {
	if (a.ID == "__start" && b.ID == "far") || (a.ID == "far" && b.ID == "__start") {
		// fast highway: quick but many metres
		return Cost{DistanceM: c.DistanceM * 5, DurationS: c.DurationS / 5}
	}
	return c
}

func (d *detourProvider) Directions(ctx context.Context, a, b Point) (Leg, error) {
	l, err := d.gridProvider.Directions(ctx, a, b)
	l.Cost = d.adjust(a, b, l.Cost)
	return l, err
}

func (d *detourProvider) ETAs(ctx context.Context, o Point, ds []Point) ([]Cost, error) {
	out, err := d.gridProvider.ETAs(ctx, o, ds)
	for i := range out {
		out[i] = d.adjust(o, ds[i], out[i])
	}
	return out, err
}

func TestOptimizeByDistancePrefersFewerMetres(t *testing.T) {
	visits := []Point{pt("near", 1, 0), pt("far", 10, 0)}
	home := pt("__finish", 0, 0)
	// near, far: 2000 s, 200 m. far, near (highway first): 1200 s, 600 m.
	r := req([]Point{visits[1], visits[0]}, &home)
	r.Order = []string{"near", "far"}
	byTime, err := Optimize(context.Background(), &detourProvider{}, r)
	if err != nil {
		t.Fatal(err)
	}
	if byTime.Objective != ObjectiveTime {
		t.Fatalf("objective %q", byTime.Objective)
	}
	if byTime.Proposed == nil || byTime.Proposed.Order[0] != "far" {
		t.Fatalf("by time want far first, got %+v %s", byTime.Proposed, byTime.Message)
	}
	r.Objective = ObjectiveDistance
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	byDist, err := Optimize(context.Background(), &detourProvider{}, r)
	if err != nil {
		t.Fatal(err)
	}
	if byDist.Proposed != nil || byDist.Objective != ObjectiveDistance {
		t.Fatalf("by distance near-first is already shortest; got %+v %s", byDist.Proposed, byDist.Message)
	}
	// Reverse the baseline: by distance it should propose near first.
	r.Order = []string{"far", "near"}
	byDist, err = Optimize(context.Background(), &detourProvider{}, r)
	if err != nil || byDist.Proposed == nil || byDist.Proposed.Order[0] != "near" || byDist.SavingM < MinImprovementM {
		t.Fatalf("%v %+v %s", err, byDist.Proposed, byDist.Message)
	}
	if byDist.SavingS >= 0 {
		t.Fatalf("shorter order should be slower here, saving_s %v", byDist.SavingS)
	}
	if !strings.Contains(byDist.Message, "mi") || !strings.Contains(byDist.Message, "longer") {
		t.Fatalf("message %q", byDist.Message)
	}
}

func TestOptimizeByDistanceLargeTripAndDeadlines(t *testing.T) {
	// On the grid distance is proportional to time, so the distance
	// objective must reach the same lattice optimum through the heuristics.
	var visits []Point
	for x := 0; x < 4; x++ {
		for y := 0; y < 5; y++ {
			if x == 0 && y == 0 {
				continue
			}
			visits = append(visits, pt(fmt.Sprintf("p%d%d", x, y), float64(x), float64(y)))
		}
	}
	rng := rand.New(rand.NewPCG(7, 7))
	rng.Shuffle(len(visits), func(i, j int) { visits[i], visits[j] = visits[j], visits[i] })
	home := pt("__finish", 0, 0)
	r := req(visits, &home)
	r.Objective = ObjectiveDistance
	cmp, err := Optimize(context.Background(), &gridProvider{}, r)
	if err != nil || cmp.Proposed == nil {
		t.Fatalf("%v %s", err, cmp.Message)
	}
	if math.Abs(cmp.Proposed.TotalDistanceM-200) > 1 {
		t.Fatalf("loop %v m, optimum 200 m", cmp.Proposed.TotalDistanceM)
	}

	// A deadline still beats distance: windows skip the exact search.
	small := []Point{pt("a", 1, 0), pt("b", 2, 0), pt("c", 3, 0)}
	r = req([]Point{small[2], small[1], small[0]}, nil)
	r.Objective = ObjectiveDistance
	r.Departure = time.Now().Unix()
	r.Visits[0].Latest = r.Departure + 350 // c (300 s away) is on time only if first
	cmp, err = Optimize(context.Background(), &gridProvider{}, r)
	if err != nil {
		t.Fatal(err)
	}
	ev := cmp.Baseline
	if cmp.Proposed != nil {
		ev = *cmp.Proposed
	}
	if ev.Order[0] != "c" {
		t.Fatalf("deadline stop should lead, got %v (%s)", ev.Order, cmp.Message)
	}
}

func TestValidateObjective(t *testing.T) {
	r := req([]Point{pt("a", 1, 0)}, nil)
	for _, o := range []string{"", "time", "distance"} {
		r.Objective = o
		if err := r.Validate(); err != nil {
			t.Fatalf("%q: %v", o, err)
		}
	}
	r.Objective = "miles"
	if r.Validate() == nil {
		t.Fatal("unknown objective accepted")
	}
}
