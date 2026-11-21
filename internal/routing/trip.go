package routing

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

// Inf marks an unknown or unreachable travel cost.
var Inf = math.Inf(1)

// MaxVisits is the most visits one trip request may contain.
const MaxVisits = 100

// ContractVersion is the request/response version (field "v").
const ContractVersion = 1

// MinImprovementS is how much estimated time a proposal must save before it
// is offered instead of the current order.
const MinImprovementS = 60

// MinImprovementM is how much distance a proposal must save before it is
// offered when optimizing for distance (about a quarter mile).
const MinImprovementM = 400

// Objectives for Optimize: the soonest finish (default) or the fewest
// driven metres.
const (
	ObjectiveTime     = "time"
	ObjectiveDistance = "distance"
)

// Request is a whole trip: a start, visits and an optional fixed finish.
// Order is the visit order to evaluate (evaluate) or the baseline to beat
// (optimize). A nil Finish means the trip may end at any visit.
type Request struct {
	V         int      `json:"v"`
	Start     Point    `json:"start"`
	Finish    *Point   `json:"finish"`
	Visits    []Point  `json:"visits"`
	Order     []string `json:"order"`
	Departure int64    `json:"departure"`
	// Avoid lists road features to avoid; only "tolls" is supported.
	Avoid []string `json:"avoid,omitempty"`
	// LockFirst keeps the first LockFirst visits of Order in place ("visit
	// next"); optimize reorders only the rest.
	LockFirst int `json:"lock_first,omitempty"`
	// ServiceS is the default time spent at each visit (a visit's own
	// service_s overrides it).
	ServiceS float64 `json:"service_s,omitempty"`
	// Objective is what optimize minimizes: "time" (default, when the trip
	// ends) or "distance" (total driven distance). Deadlines come first
	// either way.
	Objective string `json:"objective,omitempty"`
}

// ByDistance reports whether optimize minimizes distance instead of time.
func (r Request) ByDistance() bool { return r.Objective == ObjectiveDistance }

// MaxServiceS bounds a stop's service time.
const MaxServiceS = 3600

// LatePenalty weighs one second of lateness against one second of trip
// time when ranking orders: arriving on time comes first.
const LatePenalty = 100

// LatePenaltyM weighs one second of lateness against metres driven when
// optimizing for distance (a minute late outweighs 60 km).
const LatePenaltyM = 1000

// departAt is the schedule's time zero (unix seconds).
func (r Request) departAt() int64 {
	if r.Departure > 0 {
		return r.Departure
	}
	return time.Now().Unix()
}

// timed reports whether any visit has a time window.
func (r Request) timed() bool {
	for _, v := range r.Visits {
		if v.Earliest > 0 || v.Latest > 0 {
			return true
		}
	}
	return false
}

func (r Request) serviceOf(p Point) float64 {
	if p.ServiceS > 0 {
		return p.ServiceS
	}
	return r.ServiceS
}

// AvoidTolls reports whether the request asks to avoid tolls.
func (r Request) AvoidTolls() bool {
	for _, a := range r.Avoid {
		if a == "tolls" {
			return true
		}
	}
	return false
}

// InputError is a client mistake in a Request (HTTP 400).
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func bad(f string, a ...any) error { return &InputError{Msg: fmt.Sprintf(f, a...)} }

func validPoint(p Point) bool {
	return !math.IsNaN(p.Lat) && !math.IsNaN(p.Lng) && p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180 && !(p.Lat == 0 && p.Lng == 0)
}

// Validate checks IDs and coordinates, and that Order lists every visit
// exactly once and nothing else.
func (r Request) Validate() error {
	if r.V != ContractVersion {
		return bad("unsupported request version %d (want %d)", r.V, ContractVersion)
	}
	for _, a := range r.Avoid {
		if a != "tolls" {
			return bad("unsupported avoid option %q (only \"tolls\")", a)
		}
	}
	if r.ServiceS < 0 || r.ServiceS > MaxServiceS {
		return bad("service_s must be 0 to %d", MaxServiceS)
	}
	if r.Objective != "" && r.Objective != ObjectiveTime && r.Objective != ObjectiveDistance {
		return bad("unsupported objective %q (\"time\" or \"distance\")", r.Objective)
	}
	if r.LockFirst < 0 || r.LockFirst > len(r.Visits) {
		return bad("lock_first must be 0 to the number of visits")
	}
	if len(r.Visits) > MaxVisits {
		return bad("at most %d visits per trip", MaxVisits)
	}
	if !validPoint(r.Start) {
		return bad("invalid start coordinates")
	}
	if r.Finish != nil && !validPoint(*r.Finish) {
		return bad("invalid finish coordinates")
	}
	ids := map[string]bool{}
	reserved := map[string]bool{r.Start.ID: true}
	if r.Finish != nil {
		reserved[r.Finish.ID] = true
	}
	for _, v := range r.Visits {
		if v.ID == "" || len(v.ID) > 64 {
			return bad("visit id missing or too long")
		}
		if ids[v.ID] || reserved[v.ID] {
			return bad("duplicate id %q", v.ID)
		}
		if !validPoint(v) {
			return bad("visit %q: invalid coordinates", v.ID)
		}
		if v.ServiceS < 0 || v.ServiceS > MaxServiceS {
			return bad("visit %q: service_s must be 0 to %d", v.ID, MaxServiceS)
		}
		if v.Earliest < 0 || v.Latest < 0 || (v.Earliest > 0 && v.Latest > 0 && v.Earliest > v.Latest) {
			return bad("visit %q: invalid time window", v.ID)
		}
		ids[v.ID] = true
	}
	if len(r.Order) != len(r.Visits) {
		return bad("order must list every visit exactly once (%d of %d)", len(r.Order), len(r.Visits))
	}
	seen := map[string]bool{}
	for _, id := range r.Order {
		if !ids[id] {
			return bad("order has unknown visit %q", id)
		}
		if seen[id] {
			return bad("order repeats visit %q", id)
		}
		seen[id] = true
	}
	return nil
}

func (r Request) byID() map[string]Point {
	m := make(map[string]Point, len(r.Visits))
	for _, v := range r.Visits {
		m[v.ID] = v
	}
	return m
}

// Sequence is start, the visits in order, then the finish if any.
func (r Request) sequence(order []string) []Point {
	m := r.byID()
	seq := []Point{r.Start}
	for _, id := range order {
		seq = append(seq, m[id])
	}
	if r.Finish != nil {
		seq = append(seq, *r.Finish)
	}
	return seq
}

// Geometry is an encoded leg path.
type Geometry struct {
	Encoding        string `json:"encoding"`         // "polyline5": Google encoded polyline, 1e5 precision
	CoordinateOrder string `json:"coordinate_order"` // "lat,lng"
	Points          string `json:"points"`
	Vertices        int    `json:"vertices"`
}

// LegOut is one leg of an evaluated trip.
type LegOut struct {
	From      string   `json:"from"`
	To        string   `json:"to"`
	DistanceM float64  `json:"distance_m"`
	DurationS float64  `json:"duration_s"`
	HasTolls  bool     `json:"has_tolls"`
	Geometry  Geometry `json:"geometry"`
	// Schedule, in seconds after departure: arrival at To, waiting for its
	// window to open, and lateness past its latest time.
	ArriveS float64 `json:"arrive_s"`
	WaitS   float64 `json:"wait_s,omitempty"`
	LateS   float64 `json:"late_s,omitempty"`
}

// Evaluation is a fixed-order trip: legs and totals (driving only: no
// service or waiting time).
type Evaluation struct {
	Order          []string `json:"order"`
	Legs           []LegOut `json:"legs"`
	TotalDistanceM float64  `json:"total_distance_m"`
	TotalDurationS float64  `json:"total_duration_s"`
	FinishIncluded bool     `json:"finish_included"`
	// TollLegs counts legs on a toll road (even when tolls were avoided,
	// Apple uses one when no reasonable toll-free route exists).
	TollLegs int `json:"toll_legs"`
	// Schedule totals: time at stops, waiting for windows, and when the
	// trip ends (seconds after departure: driving + service + waiting).
	TotalServiceS float64  `json:"total_service_s"`
	TotalWaitS    float64  `json:"total_wait_s"`
	FinishS       float64  `json:"finish_s"`
	LateS         float64  `json:"late_s"`
	Late          []string `json:"late,omitempty"`
}

// UnreachableError lists points no road route reaches.
type UnreachableError struct{ IDs []string }

func (e *UnreachableError) Error() string {
	return "no driving route to: " + strings.Join(e.IDs, ", ")
}

// Concurrency bounds parallel provider calls.
var Concurrency = 6

// MaxVerticesPerLeg and MaxVerticesTotal bound display geometry.
const (
	MaxVerticesPerLeg = 400
	MaxVerticesTotal  = 20000
)

// CallsForEvaluate estimates provider calls for one evaluation.
func CallsForEvaluate(r Request) int { return len(r.sequence(r.Order)) - 1 + 1 }

// Evaluate routes the visits in r.Order with road geometry.
func Evaluate(ctx context.Context, p Provider, r Request, order []string) (Evaluation, error) {
	seq := r.sequence(order)
	n := len(seq) - 1
	legs := make([]Leg, n)
	errs := make([]error, n)
	sem := make(chan struct{}, max(Concurrency, 1))
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		a, b := seq[i], seq[i+1]
		if a.Lat == b.Lat && a.Lng == b.Lng {
			legs[i] = Leg{Path: [][2]float64{{a.Lat, a.Lng}}}
			continue
		}
		wg.Add(1)
		go func(i int, a, b Point) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			legs[i], errs[i] = p.Directions(ctx, a, b)
		}(i, a, b)
	}
	wg.Wait()
	var unreachable []string
	for i, err := range errs {
		if err == nil {
			continue
		}
		if errors.Is(err, ErrNoRoute) {
			unreachable = append(unreachable, seq[i+1].ID)
			continue
		}
		return Evaluation{}, err
	}
	if len(unreachable) > 0 {
		return Evaluation{}, &UnreachableError{IDs: unreachable}
	}
	ev := Evaluation{Order: append([]string{}, order...), Legs: make([]LegOut, n), FinishIncluded: r.Finish != nil}
	perLeg := MaxVerticesPerLeg
	if n > 0 && n*perLeg > MaxVerticesTotal {
		perLeg = max(MaxVerticesTotal/n, 2)
	}
	for i, l := range legs {
		path := Simplify(l.Path, perLeg)
		ev.Legs[i] = LegOut{
			From: seq[i].ID, To: seq[i+1].ID,
			DistanceM: math.Round(l.DistanceM), DurationS: math.Round(l.DurationS), HasTolls: l.HasTolls,
			Geometry: Geometry{Encoding: "polyline5", CoordinateOrder: "lat,lng", Points: EncodePolyline(path), Vertices: len(path)},
		}
		ev.TotalDistanceM += ev.Legs[i].DistanceM
		ev.TotalDurationS += ev.Legs[i].DurationS
		if l.HasTolls {
			ev.TollLegs++
		}
	}
	schedule(r, seq, &ev)
	return ev, nil
}

// schedule fills arrival, waiting and lateness per leg from the legs'
// durations, the visits' windows and service times.
func schedule(r Request, seq []Point, ev *Evaluation) {
	depart := r.departAt()
	t := 0.0
	for i := range ev.Legs {
		l := &ev.Legs[i]
		to := seq[i+1]
		t += l.DurationS
		l.ArriveS = math.Round(t)
		if i+1 == len(seq)-1 && r.Finish != nil {
			break // the finish has no window or service
		}
		if to.Earliest > 0 {
			if open := float64(to.Earliest - depart); t < open {
				l.WaitS = math.Round(open - t)
				ev.TotalWaitS += l.WaitS
				t = open
			}
		}
		if to.Latest > 0 {
			if late := t - float64(to.Latest-depart); late > 0 {
				l.LateS = math.Round(late)
				ev.LateS += l.LateS
				ev.Late = append(ev.Late, to.ID)
			}
		}
		svc := r.serviceOf(to)
		ev.TotalServiceS += svc
		t += svc
	}
	ev.FinishS = math.Round(t)
}

// Comparison is the result of optimize: the baseline and (when it is
// better by at least MinImprovementS) a proposed order, both evaluated
// the same way in the same request.
type Comparison struct {
	Baseline Evaluation  `json:"baseline"`
	Proposed *Evaluation `json:"proposed,omitempty"`
	Improved bool        `json:"improved"`
	SavingS  float64     `json:"saving_s"`
	// SavingM is the driving distance saved (negative when longer).
	SavingM float64 `json:"saving_m"`
	// Objective is what the search minimized ("time" or "distance").
	Objective string `json:"objective"`
	Message   string `json:"message"`
	// Matrix describes how candidate orders were scored.
	Matrix MatrixInfo `json:"matrix"`
}

// MatrixInfo reports the travel-time matrix the search used.
type MatrixInfo struct {
	Nodes     int  `json:"nodes"`
	Pairs     int  `json:"pairs"`
	Complete  bool `json:"complete"`
	Neighbors int  `json:"neighbors,omitempty"`
}

// FullMatrixMaxVisits: up to this many visits every pair is priced;
// above it each point prices only its nearest Neighbors (plus its baseline
// successor and the finish), so the search can only use priced edges.
const (
	FullMatrixMaxVisits = 30
	Neighbors           = 20
)

type matrixPlan struct {
	dests [][]int // per origin node: destination nodes to price
	calls int
}

// nodes: 0 start, 1..n visits (in Visits order), n+1 finish when fixed.
func planMatrix(r Request, base []int) matrixPlan {
	n := len(r.Visits)
	pts := r.nodes()
	finish := -1
	if r.Finish != nil {
		finish = n + 1
	}
	succ := map[int]int{}
	prev := 0
	for _, v := range base {
		succ[prev] = v
		prev = v
	}
	plan := matrixPlan{dests: make([][]int, n+1)}
	for o := 0; o <= n; o++ {
		set := map[int]bool{}
		if n <= FullMatrixMaxVisits {
			for v := 1; v <= n; v++ {
				set[v] = true
			}
		} else {
			for _, v := range nearest(pts, o, n, Neighbors) {
				set[v] = true
			}
			if s, ok := succ[o]; ok {
				set[s] = true
			}
		}
		delete(set, o)
		if finish >= 0 {
			set[finish] = true
		}
		var ds []int
		for v := 1; v <= n+1; v++ {
			if set[v] {
				ds = append(ds, v)
			}
		}
		plan.dests[o] = ds
		plan.calls += (len(ds) + MaxETADestinations - 1) / MaxETADestinations
	}
	return plan
}

func (r Request) nodes() []Point {
	pts := append([]Point{r.Start}, r.Visits...)
	if r.Finish != nil {
		pts = append(pts, *r.Finish)
	}
	return pts
}

// nearest returns up to k visit nodes nearest to node o by great-circle
// distance. This only chooses which pairs to price; costs are road times.
func nearest(pts []Point, o, n, k int) []int {
	type cand struct {
		v int
		d float64
	}
	cs := make([]cand, 0, n)
	for v := 1; v <= n; v++ {
		if v != o {
			cs = append(cs, cand{v, haversine(pts[o], pts[v])})
		}
	}
	// partial selection sort is fine for n <= 100
	for i := 0; i < len(cs) && i < k; i++ {
		m := i
		for j := i + 1; j < len(cs); j++ {
			if cs[j].d < cs[m].d {
				m = j
			}
		}
		cs[i], cs[m] = cs[m], cs[i]
	}
	out := []int{}
	for i := 0; i < len(cs) && i < k; i++ {
		out = append(out, cs[i].v)
	}
	return out
}

func haversine(a, b Point) float64 {
	const r = 6371000
	rad := math.Pi / 180
	dLat := (b.Lat - a.Lat) * rad
	dLng := (b.Lng - a.Lng) * rad
	h := math.Pow(math.Sin(dLat/2), 2) + math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Pow(math.Sin(dLng/2), 2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(h)))
}

// CallsForOptimize estimates provider calls for an optimize request.
func CallsForOptimize(r Request) int {
	base := r.indexOrder(r.Order)
	legs := len(r.Visits) + 1
	return planMatrix(r, base).calls + 2*legs + 1
}

func (r Request) indexOrder(order []string) []int {
	idx := make(map[string]int, len(r.Visits))
	for i, v := range r.Visits {
		idx[v.ID] = i + 1
	}
	out := make([]int, len(order))
	for i, id := range order {
		out[i] = idx[id]
	}
	return out
}

// OptimizeTime bounds the local search.
var OptimizeTime = 5 * time.Second

// Optimize prices travel times, searches for a shorter visit order, then
// evaluates the baseline and the candidate with the same method. Up to
// ExactMaxVisits the order is exact for the priced travel times; above
// that it is a heuristic (nearest neighbour, relocate/2-opt local search
// and seeded iterated local search) and not guaranteed globally optimal.
func Optimize(ctx context.Context, p Provider, r Request) (Comparison, error) {
	n := len(r.Visits)
	base := r.indexOrder(r.Order)
	cmp := Comparison{Objective: ObjectiveTime}
	if r.ByDistance() {
		cmp.Objective = ObjectiveDistance
	}
	if n < 2 {
		ev, err := Evaluate(ctx, p, r, r.Order)
		if err != nil {
			return cmp, err
		}
		cmp.Baseline = ev
		cmp.Message = "Nothing to reorder."
		return cmp, nil
	}
	if n-r.LockFirst < 2 {
		ev, err := Evaluate(ctx, p, r, r.Order)
		if err != nil {
			return cmp, err
		}
		cmp.Baseline = ev
		cmp.Message = "Nothing to reorder after the stops kept first."
		return cmp, nil
	}
	m, dm, info, err := buildMatrix(ctx, p, r, base)
	if err != nil {
		return cmp, err
	}
	cmp.Matrix = info
	pr := newProblem(r, m, base[:r.LockFirst])
	if r.ByDistance() {
		pr.d = dm
	}
	best := search(pr, base[r.LockFirst:], time.Now().Add(OptimizeTime))
	proposed := make([]string, 0, n)
	for _, v := range append(append([]int{}, base[:r.LockFirst]...), best...) {
		proposed = append(proposed, r.Visits[v-1].ID)
	}

	baseline, err := Evaluate(ctx, p, r, r.Order)
	if err != nil {
		return cmp, err
	}
	cmp.Baseline = baseline
	if equalOrder(proposed, r.Order) {
		cmp.Message = "No better order found; keeping the current order."
		return cmp, nil
	}
	prop, err := Evaluate(ctx, p, r, proposed)
	if err != nil {
		return cmp, err
	}
	// Lateness first, then when the trip ends (driving + stops + waiting;
	// without windows or service this is the driving time) or, optimizing
	// for distance, the distance driven.
	saving := baseline.FinishS - prop.FinishS
	cmp.SavingS = math.Round(saving)
	savingM := baseline.TotalDistanceM - prop.TotalDistanceM
	cmp.SavingM = math.Round(savingM)
	if r.ByDistance() {
		switch {
		case prop.LateS < baseline.LateS-1:
			cmp.Message = fmt.Sprintf("Late at %d stop(s) instead of %d (%.0f min less late).", len(prop.Late), len(baseline.Late), (baseline.LateS-prop.LateS)/60)
			if savingM >= MinImprovementM {
				cmp.Message += " Also " + miles(savingM) + " shorter."
			}
		case prop.LateS > baseline.LateS+1:
			cmp.Message = "The shorter order found would be later at time-limited stops; keeping the current order."
			return cmp, nil
		case savingM < MinImprovementM:
			cmp.Message = fmt.Sprintf("The best order found saves %s, less than %s; keeping the current order.", miles(math.Max(savingM, 0)), miles(MinImprovementM))
			return cmp, nil
		default:
			cmp.Message = "Estimated " + miles(savingM) + " less driving"
			switch {
			case saving <= -60:
				cmp.Message += fmt.Sprintf(", %.0f min longer.", -saving/60)
			case saving >= 60:
				cmp.Message += fmt.Sprintf(", and %.0f min shorter.", saving/60)
			default:
				cmp.Message += "."
			}
		}
		cmp.Proposed = &prop
		cmp.Improved = true
		return cmp, nil
	}
	switch {
	case prop.LateS < baseline.LateS-1:
		cmp.Message = fmt.Sprintf("Late at %d stop(s) instead of %d (%.0f min less late).", len(prop.Late), len(baseline.Late), (baseline.LateS-prop.LateS)/60)
		if saving >= 60 {
			cmp.Message += fmt.Sprintf(" Also %.0f min shorter.", saving/60)
		}
	case prop.LateS > baseline.LateS+1:
		cmp.Message = "The shorter order found would be later at time-limited stops; keeping the current order."
		return cmp, nil
	case saving < MinImprovementS:
		cmp.Message = fmt.Sprintf("The best order found saves %.0f s, less than %d s; keeping the current order.", math.Max(saving, 0), MinImprovementS)
		return cmp, nil
	default:
		cmp.Message = fmt.Sprintf("Estimated %.0f min shorter.", saving/60)
	}
	cmp.Proposed = &prop
	cmp.Improved = true
	return cmp, nil
}

// miles formats metres as miles with km in brackets.
func miles(m float64) string {
	return fmt.Sprintf("%.1f mi (%.1f km)", m/1609.344, m/1000)
}

func equalOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// buildMatrix prices the planned pairs with ETAs calls: travel times and
// the distances of those (fastest) routes.
func buildMatrix(ctx context.Context, p Provider, r Request, base []int) ([][]float64, [][]float64, MatrixInfo, error) {
	pts := r.nodes()
	size := len(pts)
	m := make([][]float64, size)
	dm := make([][]float64, size)
	for i := range m {
		m[i] = make([]float64, size)
		dm[i] = make([]float64, size)
		for j := range m[i] {
			if i != j {
				m[i][j] = Inf
				dm[i][j] = Inf
			}
		}
	}
	plan := planMatrix(r, base)
	type job struct {
		o     int
		dests []int
	}
	var jobs []job
	pairs := 0
	for o, all := range plan.dests {
		pairs += len(all)
		// Co-located points (same building, a stop at the start) cost
		// nothing; providers may refuse or omit a zero-length ETA.
		var ds []int
		for _, d := range all {
			if pts[o].Lat == pts[d].Lat && pts[o].Lng == pts[d].Lng {
				m[o][d], dm[o][d] = 0, 0
				continue
			}
			ds = append(ds, d)
		}
		for i := 0; i < len(ds); i += MaxETADestinations {
			jobs = append(jobs, job{o, ds[i:min(i+MaxETADestinations, len(ds))]})
		}
	}
	sem := make(chan struct{}, max(Concurrency, 1))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			dests := make([]Point, len(j.dests))
			for i, d := range j.dests {
				dests[i] = pts[d]
			}
			costs, err := p.ETAs(ctx, pts[j.o], dests)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			for i, d := range j.dests {
				m[j.o][d] = costs[i].DurationS
				dm[j.o][d] = costs[i].DistanceM
			}
		}(j)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, nil, MatrixInfo{}, firstErr
	}
	info := MatrixInfo{Nodes: size, Pairs: pairs, Complete: len(r.Visits) <= FullMatrixMaxVisits}
	if !info.Complete {
		info.Neighbors = Neighbors
	}
	return m, dm, info, nil
}

// problem is the order search on a priced matrix: node 0 is the start,
// visits are 1..n, the finish (if fixed) is fin. The searched visits
// leave from origin at time t0 (after any locked visits). Costs are the
// schedule: travel, waiting for windows and service, plus LatePenalty per
// second late. Without windows or service that is the travel time. With d
// set (optimizing for distance) the score is metres driven plus
// LatePenaltyM per second late; m still drives the schedule.
type problem struct {
	m       [][]float64
	d       [][]float64
	origin  int
	t0      float64
	fin     int
	open    []float64 // seconds after departure; 0 = none
	close   []float64 // +Inf = none
	service []float64
}

func newProblem(r Request, m [][]float64, locked []int) *problem {
	n := len(r.Visits)
	size := len(m)
	pr := &problem{m: m, fin: -1, open: make([]float64, size), close: make([]float64, size), service: make([]float64, size)}
	if r.Finish != nil {
		pr.fin = n + 1
	}
	depart := r.departAt()
	for i := range pr.close {
		pr.close[i] = Inf
	}
	for i, v := range r.Visits {
		if v.Earliest > 0 {
			pr.open[i+1] = float64(v.Earliest - depart)
		}
		if v.Latest > 0 {
			pr.close[i+1] = float64(v.Latest - depart)
		}
		pr.service[i+1] = r.serviceOf(v)
	}
	t, prev := 0.0, 0
	for _, v := range locked {
		t = pr.step(t, prev, v)
		prev = v
	}
	pr.origin, pr.t0 = prev, t
	return pr
}

// step travels prev -> v from time t: arrives, waits, serves; returns the
// departure time from v.
func (p *problem) step(t float64, prev, v int) float64 {
	t += p.m[prev][v]
	if t < p.open[v] {
		t = p.open[v]
	}
	return t + p.service[v]
}

// cost scores an order of the searched visits.
func (p *problem) cost(order []int) float64 {
	t, late, prev, dist := p.t0, 0.0, p.origin, 0.0
	for _, v := range order {
		arrive := t + p.m[prev][v]
		if arrive > p.close[v] {
			late += arrive - p.close[v]
		}
		t = p.step(t, prev, v)
		if p.d != nil {
			dist += p.d[prev][v]
		}
		prev = v
	}
	if p.fin >= 0 {
		t += p.m[prev][p.fin]
		if p.d != nil {
			dist += p.d[prev][p.fin]
		}
	}
	if p.d != nil {
		return dist + LatePenaltyM*late
	}
	return t + LatePenalty*late
}

// edge is the objective's cost of travelling a -> b (greedy choices).
func (p *problem) edge(a, b int) float64 {
	if p.d != nil {
		return p.d[a][b]
	}
	return p.m[a][b]
}

// windowed reports whether any searched visit has a time window.
func (p *problem) windowed() bool {
	for i := range p.open {
		if p.open[i] > 0 || !math.IsInf(p.close[i], 1) {
			return true
		}
	}
	return false
}

// tourCost: start(0) -> order... -> finish (if fin >= 0), travel only.
func tourCost(m [][]float64, order []int, fin int) float64 {
	return plainProblem(m, fin).cost(order)
}

func plainProblem(m [][]float64, fin int) *problem {
	size := len(m)
	p := &problem{m: m, fin: fin, open: make([]float64, size), close: make([]float64, size), service: make([]float64, size)}
	for i := range p.close {
		p.close[i] = Inf
	}
	return p
}

// ExactMaxVisits: up to this many visits the order is solved exactly
// (Held-Karp dynamic programming over the full matrix).
const ExactMaxVisits = 12

// ilsStall stops iterated local search after this many perturbations in a
// row without a better tour.
const ilsStall = 150

// search returns the best order found for the searched visits; base is
// kept unless a candidate scores strictly lower.
func search(p *problem, base []int, deadline time.Time) []int {
	best := append([]int{}, base...)
	bestC := p.cost(best)
	consider := func(o []int) bool {
		if c := p.cost(o); c < bestC-1e-9 {
			best, bestC = append([]int{}, o...), c
			return true
		}
		return false
	}
	if len(base) <= ExactMaxVisits {
		if o := exactFor(p, base); o != nil {
			consider(o)
			return best
		}
		// No order meets every deadline (or, by distance, windows make the
		// exact search inapplicable): fall through to the penalized
		// heuristics, which put lateness first.
	}
	consider(improve(p, append([]int{}, base...), deadline))
	if nn := nearestNeighbour(p, base); nn != nil {
		consider(improve(p, nn, deadline))
	}
	// Iterated local search: kick the best tour with a double-bridge move
	// and re-optimize; keep strict improvements. Seeded, so a given matrix
	// always gives the same answer.
	if len(base) >= 8 {
		rng := rand.New(rand.NewPCG(uint64(len(base)), 0x7e51a))
		for stall := 0; stall < ilsStall && time.Now().Before(deadline); stall++ {
			if consider(improve(p, doubleBridge(best, rng), deadline)) {
				stall = -1
			}
		}
	}
	return best
}

// exact solves the order with Held-Karp on the earliest time each state
// can be reached, treating deadlines as hard. Waiting is FIFO-safe, so this
// is the order that ends soonest with nobody late. nil when no priced
// order meets every deadline.
func exact(p *problem, visits []int) []int {
	n := len(visits)
	if n == 0 {
		return nil
	}
	full := 1<<n - 1
	dp := make([]float64, (full+1)*n)
	par := make([]int8, (full+1)*n)
	for i := range dp {
		dp[i] = Inf
	}
	reach := func(t float64, from, v int) float64 {
		if t+p.m[from][v] > p.close[v] {
			return Inf
		}
		return p.step(t, from, v)
	}
	for j, v := range visits {
		dp[(1<<j)*n+j] = reach(p.t0, p.origin, v)
		par[(1<<j)*n+j] = -1
	}
	for mask := 1; mask <= full; mask++ {
		for j := 0; j < n; j++ {
			if mask&(1<<j) == 0 {
				continue
			}
			cur := dp[mask*n+j]
			if math.IsInf(cur, 1) {
				continue
			}
			for k := 0; k < n; k++ {
				if mask&(1<<k) != 0 {
					continue
				}
				nm := mask | 1<<k
				if c := reach(cur, visits[j], visits[k]); c < dp[nm*n+k] {
					dp[nm*n+k], par[nm*n+k] = c, int8(j)
				}
			}
		}
	}
	last, bestC := -1, Inf
	for j := 0; j < n; j++ {
		c := dp[full*n+j]
		if p.fin >= 0 {
			c += p.m[visits[j]][p.fin]
		}
		if c < bestC {
			last, bestC = j, c
		}
	}
	if last < 0 {
		return nil
	}
	out := make([]int, n)
	for mask, j, i := full, last, n-1; i >= 0; i-- {
		out[i] = visits[j]
		pj := int(par[mask*n+j])
		mask &^= 1 << j
		j = pj
	}
	return out
}

// exactFor runs exact for the problem's objective. By distance it is exact
// only without time windows (distance then ignores the schedule); with
// windows it returns nil and the heuristics decide.
func exactFor(p *problem, visits []int) []int {
	if p.d == nil {
		return exact(p, visits)
	}
	if p.windowed() {
		return nil
	}
	q := *p
	q.m, q.d, q.t0 = p.d, nil, 0
	q.service = make([]float64, len(p.service))
	return exact(&q, visits)
}

// doubleBridge cuts the tour into four parts A B C D and returns A C B D,
// a move local search cannot undo in one step.
func doubleBridge(o []int, rng *rand.Rand) []int {
	n := len(o)
	cuts := []int{1 + rng.IntN(n-3)}
	cuts = append(cuts, cuts[0]+1+rng.IntN(n-cuts[0]-2))
	cuts = append(cuts, cuts[1]+1+rng.IntN(n-cuts[1]-1))
	a, b, c := cuts[0], cuts[1], cuts[2]
	out := make([]int, 0, n)
	out = append(out, o[:a]...)
	out = append(out, o[b:c]...)
	out = append(out, o[a:b]...)
	out = append(out, o[c:]...)
	return out
}

func nearestNeighbour(p *problem, visits []int) []int {
	left := map[int]bool{}
	for _, v := range visits {
		left[v] = true
	}
	out := make([]int, 0, len(visits))
	cur := p.origin
	for len(left) > 0 {
		next, nc := -1, Inf
		for _, v := range visits { // deterministic order
			if c := p.edge(cur, v); left[v] && c < nc {
				next, nc = v, c
			}
		}
		if next < 0 {
			return nil // no priced edge onward
		}
		out = append(out, next)
		delete(left, next)
		cur = next
	}
	return out
}

// improve applies first-improvement relocate (segments of 1-3) and 2-opt
// moves until none helps or the deadline passes. Costs are asymmetric and
// time-dependent, so every candidate is re-scored in full.
func improve(p *problem, o []int, deadline time.Time) []int {
	n := len(o)
	cur := p.cost(o)
	cand := make([]int, n)
	for improved := true; improved && time.Now().Before(deadline); {
		improved = false
		// relocate
		for seg := 1; seg <= 3 && !improved; seg++ {
			for i := 0; i+seg <= n && !improved; i++ {
				for j := 0; j <= n-seg && !improved; j++ {
					if j == i {
						continue
					}
					moveSegment(cand, o, i, seg, j)
					if c := p.cost(cand); c < cur-1e-9 {
						copy(o, cand)
						cur, improved = c, true
					}
				}
			}
		}
		// 2-opt
		for i := 0; i < n-1 && !improved; i++ {
			for j := i + 1; j < n && !improved; j++ {
				copy(cand, o)
				for a, b := i, j; a < b; a, b = a+1, b-1 {
					cand[a], cand[b] = cand[b], cand[a]
				}
				if c := p.cost(cand); c < cur-1e-9 {
					copy(o, cand)
					cur, improved = c, true
				}
			}
			if time.Now().After(deadline) {
				break
			}
		}
	}
	return o
}

// moveSegment writes into dst the order src with src[i:i+seg] moved so it
// starts at index j of the result.
func moveSegment(dst, src []int, i, seg, j int) {
	rest := make([]int, 0, len(src)-seg)
	rest = append(rest, src[:i]...)
	rest = append(rest, src[i+seg:]...)
	k := 0
	for x := 0; x < j; x++ {
		dst[k] = rest[x]
		k++
	}
	for x := 0; x < seg; x++ {
		dst[k] = src[i+x]
		k++
	}
	for x := j; x < len(rest); x++ {
		dst[k] = rest[x]
		k++
	}
}

// EncodePolyline encodes [lat,lng] points in Google's polyline format at
// 1e5 precision.
func EncodePolyline(pts [][2]float64) string {
	var b strings.Builder
	var pLat, pLng int64
	for _, p := range pts {
		lat := int64(math.Round(p[0] * 1e5))
		lng := int64(math.Round(p[1] * 1e5))
		encodeSigned(&b, lat-pLat)
		encodeSigned(&b, lng-pLng)
		pLat, pLng = lat, lng
	}
	return b.String()
}

func encodeSigned(b *strings.Builder, v int64) {
	u := uint64(v << 1)
	if v < 0 {
		u = ^u
	}
	for u >= 0x20 {
		b.WriteByte(byte((0x20 | (u & 0x1f)) + 63))
		u >>= 5
	}
	b.WriteByte(byte(u + 63))
}

// Simplify reduces a path to at most maxPts points with Douglas-Peucker,
// always keeping both ends. It changes display geometry only.
func Simplify(pts [][2]float64, maxPts int) [][2]float64 {
	if len(pts) <= maxPts || len(pts) < 3 {
		return pts
	}
	for tol := 0.00001; ; tol *= 2 {
		keep := make([]bool, len(pts))
		keep[0], keep[len(pts)-1] = true, true
		dp(pts, 0, len(pts)-1, tol, keep)
		out := make([][2]float64, 0, maxPts)
		for i, k := range keep {
			if k {
				out = append(out, pts[i])
			}
		}
		if len(out) <= maxPts || tol > 1 {
			return out
		}
	}
}

func dp(pts [][2]float64, a, b int, tol float64, keep []bool) {
	if b <= a+1 {
		return
	}
	idx, dmax := -1, 0.0
	for i := a + 1; i < b; i++ {
		if d := segDist(pts[i], pts[a], pts[b]); d > dmax {
			idx, dmax = i, d
		}
	}
	if dmax > tol && idx > 0 {
		keep[idx] = true
		dp(pts, a, idx, tol, keep)
		dp(pts, idx, b, tol, keep)
	}
}

func segDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if dx == 0 && dy == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	t := ((p[0]-a[0])*dx + (p[1]-a[1])*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(p[0]-(a[0]+t*dx), p[1]-(a[1]+t*dy))
}

// DecodePolyline decodes a polyline5 string into [lat,lng] points.
func DecodePolyline(s string) [][2]float64 {
	var out [][2]float64
	var lat, lng int64
	i := 0
	next := func() (int64, bool) {
		var res uint64
		shift := uint(0)
		for i < len(s) {
			b := uint64(s[i]) - 63
			i++
			res |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				v := int64(res >> 1)
				if res&1 != 0 {
					v = ^v
				}
				return v, true
			}
		}
		return 0, false
	}
	for i < len(s) {
		dLat, ok1 := next()
		dLng, ok2 := next()
		if !ok1 || !ok2 {
			break
		}
		lat += dLat
		lng += dLng
		out = append(out, [2]float64{float64(lat) / 1e5, float64(lng) / 1e5})
	}
	return out
}
