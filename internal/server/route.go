package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/routing"
)

// routeConfigSecret holds the Apple Maps Server API key for trip routing.
const routeConfigSecret = "route_config"

// routeTimeout is the product limit for one evaluate/optimize request.
const routeTimeout = 55 * time.Second

// routeRuntime caches the provider (and its access token) and the daily
// call budget.
type routeRuntime struct {
	mu     sync.Mutex
	apple  *routing.Apple
	budget routing.Budget
	// Provider overrides the Apple provider in tests.
	Provider routing.Provider
	// HTTP overrides the transport for the Apple provider in tests.
	HTTP routing.HTTPDoer
}

type routeConfig struct {
	routing.AppleConfig
	// DailyCallLimit caps provider calls per UTC day (0: default).
	DailyCallLimit int `json:"daily_call_limit,omitempty"`
}

// routeBudgetStore keeps the day's routing call count in events, so a
// restart does not reset the daily cap.
type routeBudgetStore struct{ s *Server }

func (b routeBudgetStore) LoadUsage(day string) (int, error) {
	m, err := b.s.Store.Get("events", "route_budget:"+day)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, _ := m["used"].(float64)
	return int(n), nil
}

func (b routeBudgetStore) SaveUsage(day string, used int) error {
	return b.s.Store.Put("events", "route_budget:"+day, "", map[string]any{"day": day, "used": used, "ts": float64(time.Now().Unix())})
}

// routeBudget returns the shared daily budget, backed by the store.
func (s *Server) routeBudget() *routing.Budget {
	rt := &s.route
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.budget.Store == nil {
		rt.budget.Store = routeBudgetStore{s}
	}
	return &rt.budget
}

// Future departures further out than this are refused; a departure within
// routeDepartureSlack of now is routed as "now".
const (
	routeMaxDepartureAhead = 7 * 24 * time.Hour
	routeDepartureSlack    = 5 * time.Minute
)

// routeDeparture reads the request's departure: zero time for "now", or a
// future time to price travel at.
func routeDeparture(unix int64, now time.Time) (time.Time, error) {
	if unix <= 0 {
		return time.Time{}, nil
	}
	t := time.Unix(unix, 0)
	if t.Before(now.Add(routeDepartureSlack)) {
		return time.Time{}, nil // now, or already past
	}
	if t.After(now.Add(routeMaxDepartureAhead)) {
		return time.Time{}, &routing.InputError{Msg: "departure must be within 7 days"}
	}
	return t, nil
}

func (s *Server) routeConfig() (routeConfig, error) {
	m, err := s.Store.Secret(s.Key, routeConfigSecret)
	if err != nil {
		return routeConfig{}, err
	}
	var c routeConfig
	return c, fromMap(m, &c)
}

// routeProvider returns the configured provider, or an error the app can
// show (HTTP 409: routing not set up).
func (s *Server) routeProvider() (routing.Provider, *routing.Budget, error) {
	s.routeBudget()
	rt := &s.route
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.Provider != nil {
		return rt.Provider, &rt.budget, nil
	}
	cfg, err := s.routeConfig()
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, errors.New("trip routing is not set up: configure Apple Maps in the server dashboard's Tesla settings")
	}
	if err != nil {
		return nil, nil, err
	}
	rt.budget.Limit = cfg.DailyCallLimit
	if rt.apple == nil || rt.apple.Config != cfg.AppleConfig {
		var h routing.HTTPDoer = &http.Client{Timeout: 20 * time.Second}
		if rt.HTTP != nil {
			h = rt.HTTP
		}
		rt.apple = &routing.Apple{Config: cfg.AppleConfig, HTTP: h, Budget: &rt.budget}
	}
	return rt.apple, &rt.budget, nil
}

func (s *Server) routePlanRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/route/") {
		return false
	}
	defer func() {
		if err := s.routeBudget().Flush(); err != nil {
			log.Printf("route budget: save usage: %v", err)
		}
	}()
	switch r.Method + " " + strings.TrimPrefix(r.URL.Path, "/api/route") {
	case "GET /config":
		cfg, err := s.routeConfig()
		if errors.Is(err, sql.ErrNoRows) {
			jsonOut(w, 200, map[string]any{"configured": false, "provider": "apple"})
			return true
		}
		if err != nil {
			storageError(w, err)
			return true
		}
		used, limit := s.routeBudget().Usage()
		if cfg.DailyCallLimit > 0 {
			limit = cfg.DailyCallLimit
		}
		jsonOut(w, 200, map[string]any{
			"configured": true, "provider": "apple",
			"team_id": cfg.TeamID, "key_id": cfg.KeyID, "has_private_key": cfg.PrivateKeyPEM != "",
			"daily_call_limit": limit, "used_today": used, "max_calls_per_request": routing.MaxCallsPerRun,
		})
	case "POST /config":
		var next routeConfig
		if err := fromMap(b, &next); err != nil {
			fail(w, 400, "invalid config")
			return true
		}
		if next.PrivateKeyPEM == "" { // keep the stored key
			if prev, err := s.routeConfig(); err == nil {
				next.PrivateKeyPEM = prev.PrivateKeyPEM
			}
		}
		if err := next.Validate(); err != nil {
			fail(w, 400, err.Error())
			return true
		}
		if next.DailyCallLimit < 0 || next.DailyCallLimit > 25000 {
			fail(w, 400, "daily_call_limit must be from 0 (default) to 25000")
			return true
		}
		m, _ := toMap(next)
		if err := s.Store.SaveSecret(s.Key, routeConfigSecret, m); err != nil {
			storageError(w, err)
			return true
		}
		jsonOut(w, 200, map[string]any{"ok": true})
	case "POST /evaluate":
		s.routeRun(w, r, b, false)
	case "POST /optimize":
		s.routeRun(w, r, b, true)
	case "POST /test":
		p, budget, err := s.routeProvider()
		if err != nil {
			fail(w, 409, err.Error())
			return true
		}
		if err := budget.Check(2); err != nil {
			fail(w, 429, err.Error())
			return true
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		results, err := p.Search(ctx, "Apple Park, Cupertino, California")
		if err != nil {
			routeFail(w, err)
			return true
		}
		if len(results) == 0 {
			fail(w, 502, "Apple accepted the request but returned no sample search results")
			return true
		}
		jsonOut(w, 200, map[string]any{"ok": true, "provider": "apple"})
	case "GET /search":
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(q) < 3 || len(q) > 200 {
			fail(w, 400, "q must be 3 to 200 characters")
			return true
		}
		p, budget, err := s.routeProvider()
		if err != nil {
			fail(w, 409, err.Error())
			return true
		}
		if err := budget.Check(2); err != nil {
			fail(w, 429, err.Error())
			return true
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		res, err := p.Search(ctx, q)
		if err != nil {
			routeFail(w, err)
			return true
		}
		jsonOut(w, 200, map[string]any{"v": routing.ContractVersion, "provider": "apple", "results": res})
	default:
		fail(w, 404, "unknown route endpoint")
	}
	return true
}

func routeFail(w http.ResponseWriter, err error) {
	var in *routing.InputError
	var un *routing.UnreachableError
	var ae *routing.APIError
	switch {
	case errors.As(err, &in):
		fail(w, 400, err.Error())
	case errors.As(err, &un):
		jsonOut(w, 422, map[string]any{"detail": err.Error(), "unreachable": un.IDs})
	case errors.Is(err, routing.ErrBudget):
		fail(w, 429, err.Error())
	case errors.As(err, &ae) && ae.Status == 429:
		fail(w, 429, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		fail(w, 504, "routing took too long; the current plan is unchanged")
	default:
		fail(w, 502, err.Error())
	}
}

// routeRun evaluates the given order, or (optimize) compares it with the
// best order found. Both use one frozen request: same points, same
// departure, same provider and method.
func (s *Server) routeRun(w http.ResponseWriter, r *http.Request, b map[string]any, optimize bool) {
	var req routing.Request
	if err := fromMap(b, &req); err != nil {
		fail(w, 400, "invalid trip")
		return
	}
	if err := req.Validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	started := time.Now()
	departure, err := routeDeparture(req.Departure, started)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	p, budget, err := s.routeProvider()
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	need := routing.CallsForEvaluate(req)
	if optimize {
		need = routing.CallsForOptimize(req)
	}
	if err := budget.Check(need); err != nil {
		fail(w, 429, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()
	// The schedule (arrivals, windows) counts from the departure used.
	req.Departure = started.Unix()
	if !departure.IsZero() {
		req.Departure = departure.Unix()
	}
	travel := "Apple Maps driving estimates for leaving now; not recomputed per leg for later traffic"
	var departureOut int64
	if !departure.IsZero() {
		ctx = routing.WithDeparture(ctx, departure)
		departureOut = departure.Unix()
		travel = "Apple Maps driving estimates for every leg as if leaving at the departure time; not recomputed per leg for later arrival times"
	}
	tolls := "allowed"
	if req.AvoidTolls() {
		ctx = routing.WithAvoidTolls(ctx)
		tolls = "avoided where Apple finds a reasonable toll-free route (see has_tolls per leg); the stop-order search itself ranks on Apple ETAs, which cannot avoid tolls"
	}
	optimizer := fmt.Sprintf("exact for the priced travel times up to %d stops; above that a heuristic search, not guaranteed globally optimal", routing.ExactMaxVisits)
	out := map[string]any{
		"v":           routing.ContractVersion,
		"provider":    "apple",
		"computed_at": started.Unix(),
		// 0 means "now": a past or near-now departure is routed as now.
		"departure": departureOut,
		"assumptions": map[string]any{
			"travel_times":  travel,
			"service_time":  fmt.Sprintf("%.0f s per stop unless a stop sets its own", req.ServiceS),
			"waiting_time":  "included when a stop's window has not opened yet",
			"lock_first":    req.LockFirst,
			"finish":        req.Finish != nil,
			"optimizer":     optimizer,
			"tolls":         tolls,
			"min_saving_s":  routing.MinImprovementS,
			"geometry":      "display only, simplified; metrics come from unsimplified routes",
			"calls_planned": need,
		},
	}
	if optimize {
		cmp, err := routing.Optimize(ctx, p, req)
		if err != nil {
			routeFail(w, err)
			return
		}
		out["comparison"] = cmp
	} else {
		ev, err := routing.Evaluate(ctx, p, req, req.Order)
		if err != nil {
			routeFail(w, err)
			return
		}
		out["evaluation"] = ev
	}
	out["elapsed_ms"] = time.Since(started).Milliseconds()
	jsonOut(w, 200, out)
}
