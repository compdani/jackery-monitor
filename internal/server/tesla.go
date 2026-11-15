package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/compdani/jackery-monitor/internal/automation"
	"github.com/compdani/jackery-monitor/internal/tesla"
	"github.com/teslamotors/vehicle-command/pkg/cache"
)

// Secret names in the encrypted credentials collection.
const (
	teslaConfigSecret   = "tesla_config"
	teslaTokensSecret   = "tesla_tokens"
	teslaKeySecret      = "tesla_key"
	teslaSessionsSecret = "tesla_command_sessions"
	teslaCallbackPath   = "/api/tesla/oauth/callback"
)

// teslaRuntime is the in-memory Tesla state. Everything durable lives in
// the store; this only caches what the poller and handlers share.
type teslaRuntime struct {
	tokenMu sync.Mutex // serializes token refresh (refresh tokens are single use)
	cmdMu   sync.Mutex // one command batch at a time

	mu          sync.Mutex
	states      map[string]time.Time // OAuth state -> expiry
	tracker     *tesla.Tracker
	trackerVIN  string
	lastSample  *tesla.ChargeSample
	lastState   string
	lastPoll    time.Time
	nextPoll    time.Time
	lastErr     string
	loginNeeded bool
	job         *teslaJob
	pollNow     chan struct{}
	places      *tesla.PlaceResolver // keeps its lookup cache across batches

	// HTTP overrides the Fleet API transport in tests.
	HTTP tesla.HTTPDoer
}

type teslaJob struct {
	ID       string             `json:"id"`
	Status   string             `json:"status"` // running, done, failed
	Started  int64              `json:"started"`
	Finished int64              `json:"finished,omitempty"`
	Stops    []tesla.Stop       `json:"stops"`
	Results  []tesla.StopResult `json:"results,omitempty"`
	Error    string             `json:"error,omitempty"`
	Options  map[string]string  `json:"options"`
}

func (t *teslaRuntime) http() tesla.HTTPDoer {
	if t.HTTP != nil {
		return t.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func toMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(raw, &m)
}

func fromMap(m map[string]any, v any) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (s *Server) teslaConfig() (tesla.Config, error) {
	m, err := s.Store.Secret(s.Key, teslaConfigSecret)
	if err != nil {
		return tesla.Config{}, err
	}
	var c tesla.Config
	if err := fromMap(m, &c); err != nil {
		return tesla.Config{}, err
	}
	return c.WithDefaults(), nil
}

func (s *Server) teslaPrivateKey() (string, error) {
	m, err := s.Store.Secret(s.Key, teslaKeySecret)
	if err != nil {
		return "", err
	}
	k := str(m, "private_hex")
	if k == "" {
		return "", sql.ErrNoRows
	}
	return k, nil
}

func (s *Server) teslaTokens() (tesla.Tokens, error) {
	m, err := s.Store.Secret(s.Key, teslaTokensSecret)
	if err != nil {
		return tesla.Tokens{}, err
	}
	var t tesla.Tokens
	return t, fromMap(m, &t)
}

func (s *Server) saveTeslaTokens(t tesla.Tokens) error {
	m, err := toMap(t)
	if err != nil {
		return err
	}
	return s.Store.SaveSecret(s.Key, teslaTokensSecret, m)
}

// teslaTokenSource refreshes and persists owner tokens.
type teslaTokenSource struct {
	s   *Server
	cfg tesla.Config
}

func (ts teslaTokenSource) AccessToken(ctx context.Context) (string, error) {
	rt := &ts.s.tesla
	rt.tokenMu.Lock()
	defer rt.tokenMu.Unlock()
	t, err := ts.s.teslaTokens()
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("tesla account not connected")
	}
	if err != nil {
		return "", err
	}
	now := time.Now()
	if t.Valid(now, 5*time.Minute) {
		return t.AccessToken, nil
	}
	next, err := tesla.Refresh(ctx, rt.http(), ts.cfg, t, now)
	if err != nil {
		var te *tesla.TokenError
		if errors.As(err, &te) && te.LoginRequired() {
			rt.mu.Lock()
			rt.loginNeeded = true
			rt.mu.Unlock()
		}
		return "", err
	}
	// The old refresh token is now spent: save the new one before use.
	if err := ts.s.saveTeslaTokens(next); err != nil {
		return "", fmt.Errorf("could not save refreshed tesla tokens: %w", err)
	}
	return next.AccessToken, nil
}

func (s *Server) teslaSessionCache() *cache.SessionCache {
	m, err := s.Store.Secret(s.Key, teslaSessionsSecret)
	if err == nil {
		if raw, ok := m["cache"].(string); ok {
			if c, err := cache.Import(strings.NewReader(raw)); err == nil {
				return c
			}
		}
	}
	return cache.New(4)
}

func (s *Server) saveTeslaSessionCache(c *cache.SessionCache) {
	var buf bytes.Buffer
	if c.Export(&buf) == nil {
		_ = s.Store.SaveSecret(s.Key, teslaSessionsSecret, map[string]any{"cache": buf.String()})
	}
}

func (s *Server) teslaClient(cfg tesla.Config, withKey bool) (*tesla.Client, error) {
	c := &tesla.Client{Config: cfg, HTTP: s.tesla.http(), Tokens: teslaTokenSource{s: s, cfg: cfg}, Places: s.teslaPlaces(cfg.GoogleAPIKey)}
	if withKey {
		k, err := s.teslaPrivateKey()
		if err != nil {
			return nil, errors.New("no virtual key: POST /api/tesla/key first")
		}
		c.PrivateKeyHex = k
		c.Sessions = s.teslaSessionCache()
	}
	return c, nil
}

// teslaPlaces returns the shared place resolver for this API key.
func (s *Server) teslaPlaces(key string) *tesla.PlaceResolver {
	rt := &s.tesla
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.places == nil || rt.places.APIKey != key {
		rt.places = &tesla.PlaceResolver{HTTP: rt.http(), APIKey: key}
	}
	return rt.places
}

// costRate returns the monitor's home electricity rate function.
func (s *Server) costRate() (tesla.RateFunc, string) {
	m, err := s.Store.Get("settings", "cost_plan")
	if err != nil {
		return nil, ""
	}
	var p automation.CostPlan
	if fromMap(m, &p) != nil || p.Validate() != nil {
		return nil, ""
	}
	cur := p.Currency
	if cur == "" {
		cur = "USD"
	}
	return func(ts int64) float64 {
		_, offset := time.Unix(ts, 0).In(time.Local).Zone()
		return p.RateAt(ts, offset)
	}, cur
}

func (s *Server) homeCheck(radius float64) func(tesla.ChargeSample) bool {
	m, err := s.Store.Secret(s.Key, "location")
	if err != nil {
		return nil
	}
	lat, ok1 := m["latitude"].(float64)
	lng, ok2 := m["longitude"].(float64)
	if !ok1 || !ok2 {
		return nil
	}
	return func(cs tesla.ChargeSample) bool {
		if cs.Lat == nil || cs.Lng == nil {
			return true // no location scope: assume home for non-fast charging
		}
		return tesla.DistanceM(lat, lng, *cs.Lat, *cs.Lng) <= radius
	}
}

// ---------------------------------------------------------------- routes

// teslaPublic serves the two Tesla paths that must work without an app
// session: the hosted public key and the OAuth redirect (validated by its
// one-time state value).
func (s *Server) teslaPublic(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case tesla.PublicKeyPath:
		k, err := s.teslaPrivateKey()
		if err != nil {
			http.NotFound(w, r)
			return true
		}
		pemBytes, err := tesla.PublicKeyPEM(k)
		if err != nil {
			http.Error(w, "key unavailable", 500)
			return true
		}
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(pemBytes)
		return true
	case teslaCallbackPath:
		s.teslaCallback(w, r)
		return true
	}
	return false
}

func teslaPage(w http.ResponseWriter, status int, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta name=viewport content="width=device-width,initial-scale=1"><title>%s</title><body style="font:16px system-ui;padding:24px;max-width:560px;margin:auto"><h2>%s</h2><p>%s</p><p><a href="/">Back to the dashboard</a></p>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(msg))
}

func (s *Server) teslaCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	rt := &s.tesla
	rt.mu.Lock()
	exp, ok := rt.states[state]
	delete(rt.states, state)
	rt.mu.Unlock()
	if state == "" || !ok || time.Now().After(exp) {
		teslaPage(w, 400, "Tesla sign-in expired", "Start the connection again from the dashboard.")
		return
	}
	if e := q.Get("error"); e != "" {
		teslaPage(w, 400, "Tesla sign-in was cancelled", e+" "+q.Get("error_description"))
		return
	}
	cfg, err := s.teslaConfig()
	if err != nil {
		teslaPage(w, 500, "Tesla not configured", "Save the Tesla configuration first.")
		return
	}
	t, err := tesla.ExchangeCode(r.Context(), rt.http(), cfg, q.Get("code"), time.Now())
	if err != nil {
		s.Store.App.Logger().Error("tesla code exchange", "error", err)
		teslaPage(w, 502, "Tesla sign-in failed", err.Error())
		return
	}
	rt.tokenMu.Lock()
	err = s.saveTeslaTokens(t)
	rt.tokenMu.Unlock()
	if err != nil {
		teslaPage(w, 500, "Could not save Tesla tokens", err.Error())
		return
	}
	rt.mu.Lock()
	rt.loginNeeded = false
	rt.mu.Unlock()
	s.teslaKick()
	teslaPage(w, 200, "Tesla connected", "Scopes: "+t.Scope)
}

func (s *Server) teslaRoutes(w http.ResponseWriter, r *http.Request, b map[string]any) bool {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/tesla/") {
		return false
	}
	route := r.Method + " " + strings.TrimPrefix(p, "/api/tesla")
	switch route {
	case "GET /status":
		jsonOut(w, 200, s.teslaStatus())
	case "GET /config":
		cfg, err := s.teslaConfig()
		if errors.Is(err, sql.ErrNoRows) {
			jsonOut(w, 200, map[string]any{"configured": false})
			return true
		}
		if err != nil {
			storageError(w, err)
			return true
		}
		hasSecret, hasGoogle := cfg.ClientSecret != "", cfg.GoogleAPIKey != ""
		cfg.ClientSecret, cfg.GoogleAPIKey = "", ""
		out, _ := toMap(cfg)
		out["configured"] = true
		out["has_client_secret"] = hasSecret
		out["has_google_api_key"] = hasGoogle
		jsonOut(w, 200, out)
	case "POST /config":
		s.teslaSaveConfig(w, b)
	case "POST /key":
		s.teslaKey(w, b)
	case "POST /partner/register":
		s.teslaRegisterPartner(w, r)
	case "GET /oauth/start":
		s.teslaOAuthStart(w, r)
	case "POST /oauth/forget":
		s.tesla.tokenMu.Lock()
		err := s.Store.Delete("credentials", teslaTokensSecret)
		s.tesla.tokenMu.Unlock()
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			storageError(w, err)
			return true
		}
		jsonOut(w, 200, map[string]any{"ok": true})
	case "POST /wake":
		cfg, err := s.teslaConfig()
		if err != nil {
			fail(w, 409, "tesla not configured")
			return true
		}
		c, _ := s.teslaClient(cfg, false)
		ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
		defer cancel()
		if err := c.Wake(ctx); err != nil {
			fail(w, 502, err.Error())
			return true
		}
		jsonOut(w, 200, map[string]any{"ok": true, "state": "online"})
	case "POST /navigate":
		s.teslaNavigate(w, r, b)
	case "GET /navigate/last":
		s.tesla.mu.Lock()
		job := s.tesla.job
		s.tesla.mu.Unlock()
		if job == nil {
			fail(w, 404, "no navigation sent yet")
			return true
		}
		jsonOut(w, 200, job)
	case "GET /charging/sessions":
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 500 {
			limit = 50
		}
		s.teslaSessions(w, limit)
	case "POST /charging/poll":
		s.teslaKick()
		jsonOut(w, 202, map[string]any{"ok": true})
	case "POST /app_token":
		// A long-lived token for the TeslaHelper phone app, tied to the
		// signed-in user (a password change revokes it).
		u := s.user(r)
		days := 365
		if v, ok := b["days"].(float64); ok && v >= 1 && v <= 730 {
			days = int(v)
		}
		t, err := u.NewStaticAuthToken(time.Duration(days) * 24 * time.Hour)
		if err != nil {
			fail(w, 500, "could not issue token")
			return true
		}
		jsonOut(w, 200, map[string]any{"token": t, "expires_in_days": days})
	default:
		fail(w, 404, "unknown tesla route: "+route)
	}
	return true
}

func (s *Server) teslaSaveConfig(w http.ResponseWriter, b map[string]any) {
	var next tesla.Config
	if err := fromMap(b, &next); err != nil {
		fail(w, 400, "invalid config: "+err.Error())
		return
	}
	prev, err := s.teslaConfig()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		storageError(w, err)
		return
	}
	// Keep the stored secret unless a new one is supplied.
	if next.ClientSecret == "" {
		next.ClientSecret = prev.ClientSecret
	}
	if next.GoogleAPIKey == "" {
		next.GoogleAPIKey = prev.GoogleAPIKey
	} else if next.GoogleAPIKey == "-" { // explicit removal
		next.GoogleAPIKey = ""
	}
	next = next.WithDefaults()
	if err := next.Validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	m, err := toMap(next)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := s.Store.SaveSecret(s.Key, teslaConfigSecret, m); err != nil {
		storageError(w, err)
		return
	}
	s.teslaKick()
	jsonOut(w, 200, map[string]any{"ok": true, "public_key_url": next.PublicKeyURL(), "redirect_uri": next.RedirectURI, "pairing_url": next.PairingURL()})
}

func (s *Server) teslaKey(w http.ResponseWriter, b map[string]any) {
	existing, err := s.teslaPrivateKey()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		storageError(w, err)
		return
	}
	rotate, _ := b["rotate"].(bool)
	importPEM := str(b, "private_key_pem")
	if existing != "" && !rotate && importPEM == "" {
		pub, _ := tesla.PublicKeyPEM(existing)
		jsonOut(w, 200, map[string]any{"ok": true, "created": false, "public_key_pem": string(pub)})
		return
	}
	var priv string
	if importPEM != "" {
		priv, err = tesla.ImportPEM([]byte(importPEM))
	} else {
		priv, err = tesla.GenerateKey()
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := s.Store.SaveSecret(s.Key, teslaKeySecret, map[string]any{"private_hex": priv, "created": time.Now().Unix()}); err != nil {
		storageError(w, err)
		return
	}
	// Sessions belong to the old key.
	_ = s.Store.Delete("credentials", teslaSessionsSecret)
	pub, _ := tesla.PublicKeyPEM(priv)
	out := map[string]any{"ok": true, "created": true, "public_key_pem": string(pub)}
	if cfg, err := s.teslaConfig(); err == nil {
		out["public_key_url"] = cfg.PublicKeyURL()
		out["pairing_url"] = cfg.PairingURL()
	}
	jsonOut(w, 200, out)
}

func (s *Server) teslaRegisterPartner(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.teslaConfig()
	if err != nil {
		fail(w, 409, "tesla not configured")
		return
	}
	if _, err := s.teslaPrivateKey(); err != nil {
		fail(w, 409, "generate the key first: Tesla fetches it during registration")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	token, err := tesla.PartnerToken(ctx, s.tesla.http(), cfg)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	body, _ := json.Marshal(map[string]string{"domain": cfg.Domain})
	req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Audience()+"/api/1/partner_accounts", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.tesla.http().Do(req)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	defer resp.Body.Close()
	var out any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	jsonOut(w, resp.StatusCode, map[string]any{"ok": resp.StatusCode/100 == 2, "status": resp.StatusCode, "tesla": out})
}

func (s *Server) teslaOAuthStart(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.teslaConfig()
	if err != nil {
		fail(w, 409, "tesla not configured")
		return
	}
	if cfg.ClientSecret == "" {
		fail(w, 409, "client_secret missing")
		return
	}
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	state := base64.RawURLEncoding.EncodeToString(buf)
	rt := &s.tesla
	rt.mu.Lock()
	if rt.states == nil {
		rt.states = map[string]time.Time{}
	}
	now := time.Now()
	for k, exp := range rt.states {
		if now.After(exp) {
			delete(rt.states, k)
		}
	}
	rt.states[state] = now.Add(15 * time.Minute)
	rt.mu.Unlock()
	u := tesla.AuthCodeURL(cfg, state)
	if r.URL.Query().Get("redirect") == "1" {
		http.Redirect(w, r, u, http.StatusFound)
		return
	}
	jsonOut(w, 200, map[string]any{"url": u, "redirect_uri": cfg.RedirectURI})
}

func optString(b map[string]any, k string, allowed ...string) (string, error) {
	v := str(b, k)
	if v == "" {
		return "", nil
	}
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s must be one of %s", k, strings.Join(allowed, ", "))
}

func (s *Server) teslaNavigate(w http.ResponseWriter, r *http.Request, b map[string]any) {
	cfg, err := s.teslaConfig()
	if err != nil {
		fail(w, 409, "tesla not configured")
		return
	}
	var req struct {
		Stops []tesla.Stop `json:"stops"`
	}
	if err := fromMap(b, &req); err != nil {
		fail(w, 400, "invalid stops")
		return
	}
	if err := tesla.ValidateStops(req.Stops); err != nil {
		fail(w, 400, err.Error())
		return
	}
	// Per-request overrides make it possible to compare transports and
	// order semantics on the car without changing the saved config.
	transport, err := optString(b, "transport", tesla.TransportSigned, tesla.TransportREST)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	order, err := optString(b, "order_mode", tesla.OrderEnum, tesla.OrderPosition, tesla.OrderOneThree)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	method, err := optString(b, "method", tesla.MethodWaypoints, tesla.MethodGPS)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if method != "" {
		cfg.NavMethod = method
	}
	if transport != "" {
		cfg.NavTransport = transport
	}
	if order != "" {
		cfg.NavOrderMode = order
	}
	wait := true
	if v, ok := b["wait"].(bool); ok {
		wait = v
	}
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	job := &teslaJob{ID: base64.RawURLEncoding.EncodeToString(buf), Status: "running", Started: time.Now().Unix(), Stops: req.Stops, Options: map[string]string{"method": cfg.NavMethod, "transport": cfg.NavTransport, "order_mode": cfg.NavOrderMode}}
	if !s.tesla.cmdMu.TryLock() {
		fail(w, 409, "another navigation batch is being sent")
		return
	}
	s.tesla.mu.Lock()
	s.tesla.job = job
	s.tesla.mu.Unlock()
	run := func() {
		defer s.tesla.cmdMu.Unlock()
		// Not tied to the HTTP request: the phone can return to Flex while
		// the car wakes.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		results, err := s.teslaSendStops(ctx, cfg, req.Stops)
		s.tesla.mu.Lock()
		job.Results, job.Finished = results, time.Now().Unix()
		job.Status = "done"
		if err != nil {
			job.Status, job.Error = "failed", err.Error()
		} else {
			for _, res := range results {
				if !res.OK {
					job.Status = "failed"
					job.Error = fmt.Sprintf("stop %d: %s", res.Index+1, res.Error)
				}
			}
		}
		snapshot := *job
		s.tesla.mu.Unlock()
		if m, err := toMap(snapshot); err == nil {
			_ = s.Store.Put("events", "tesla_nav:"+snapshot.ID, cfg.VIN, m)
		}
	}
	if !wait {
		go run()
		jsonOut(w, 202, job)
		return
	}
	run()
	s.tesla.mu.Lock()
	out := *job
	s.tesla.mu.Unlock()
	status := 200
	if out.Status != "done" {
		status = 502
	}
	jsonOut(w, status, out)
}

func (s *Server) teslaSendStops(ctx context.Context, cfg tesla.Config, stops []tesla.Stop) ([]tesla.StopResult, error) {
	c, err := s.teslaClient(cfg, cfg.NavTransport == tesla.TransportSigned)
	if err != nil {
		return nil, err
	}
	wctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	err = c.Wake(wctx)
	cancel()
	if err != nil {
		return nil, err
	}
	results, err := c.Navigate(ctx, stops)
	if c.Sessions != nil {
		s.saveTeslaSessionCache(c.Sessions)
	}
	return results, err
}

func (s *Server) teslaSessions(w http.ResponseWriter, limit int) {
	cfg, err := s.teslaConfig()
	if err != nil {
		jsonOut(w, 200, map[string]any{"sessions": []any{}})
		return
	}
	rows, err := s.Store.App.FindRecordsByFilter("tesla_sessions", "device_sn = {:vin}", "-ts", limit, 0, map[string]any{"vin": cfg.VIN})
	if err != nil {
		storageError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	total, totalKWh := 0.0, 0.0
	for _, r := range rows {
		var p map[string]any
		if json.Unmarshal([]byte(r.GetString("payload")), &p) == nil {
			out = append(out, p)
			if v, ok := p["cost"].(float64); ok {
				total += v
			}
			if v, ok := p["energy_wall_kwh"].(float64); ok {
				totalKWh += v
			}
		}
	}
	_, currency := s.costRate()
	jsonOut(w, 200, map[string]any{"sessions": out, "total_cost": round2(total), "total_wall_kwh": round2(totalKWh), "currency": currency})
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

func (s *Server) teslaStatus() map[string]any {
	out := map[string]any{"configured": false}
	cfg, err := s.teslaConfig()
	if err == nil {
		out["configured"] = true
		out["vin"] = cfg.VIN
		out["domain"] = cfg.Domain
		out["region"] = cfg.Region
		out["public_key_url"] = cfg.PublicKeyURL()
		out["pairing_url"] = cfg.PairingURL()
		out["redirect_uri"] = cfg.RedirectURI
		out["nav_transport"] = cfg.NavTransport
		out["nav_order_mode"] = cfg.NavOrderMode
		out["nav_method"] = cfg.NavMethod
		out["has_google_api_key"] = cfg.GoogleAPIKey != ""
		out["polling_disabled"] = cfg.PollingDisabled
	}
	_, keyErr := s.teslaPrivateKey()
	out["has_key"] = keyErr == nil
	if t, err := s.teslaTokens(); err == nil {
		out["connected"] = t.RefreshToken != ""
		out["access_expires_at"] = t.ExpiresAt
		out["refresh_token_issued_at"] = t.RefreshedAt
		// Tesla expires refresh tokens three months after issue.
		out["refresh_token_expires_at"] = t.RefreshedAt + 90*24*3600
		out["scope"] = t.Scope
	} else {
		out["connected"] = false
	}
	rt := &s.tesla
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out["login_required"] = rt.loginNeeded
	out["vehicle_state"] = rt.lastState
	if !rt.lastPoll.IsZero() {
		out["last_poll"] = rt.lastPoll.Unix()
	}
	if !rt.nextPoll.IsZero() {
		out["next_poll"] = rt.nextPoll.Unix()
	}
	if rt.lastErr != "" {
		out["last_error"] = rt.lastErr
	}
	if rt.lastSample != nil {
		out["charge"] = rt.lastSample
	}
	if rt.tracker != nil && rt.tracker.Current != nil {
		out["active_session"] = rt.tracker.Current
	}
	if rt.job != nil {
		out["last_navigation"] = rt.job
	}
	return out
}

// ---------------------------------------------------------------- poller

func (s *Server) teslaPollChan() chan struct{} {
	s.tesla.mu.Lock()
	defer s.tesla.mu.Unlock()
	if s.tesla.pollNow == nil {
		s.tesla.pollNow = make(chan struct{}, 1)
	}
	return s.tesla.pollNow
}

// teslaKick asks the poller to run now instead of at its next interval.
func (s *Server) teslaKick() {
	select {
	case s.teslaPollChan() <- struct{}{}:
	default:
	}
}

// PollTesla records charge state until ctx ends. While idle it only checks
// whether the car is online (which never wakes it); it reads charge state
// when the car is online, and every PollChargingMinutes while charging.
func (s *Server) PollTesla(ctx context.Context) {
	kick := s.teslaPollChan()
	for {
		wait := s.pollTeslaOnce(ctx)
		s.tesla.mu.Lock()
		s.tesla.nextPoll = time.Now().Add(wait)
		s.tesla.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-kick:
		case <-time.After(wait):
		}
	}
}

func (s *Server) pollTeslaOnce(ctx context.Context) time.Duration {
	const notReady = 10 * time.Minute
	cfg, err := s.teslaConfig()
	if err != nil || cfg.VIN == "" || cfg.PollingDisabled {
		return notReady
	}
	if t, err := s.teslaTokens(); err != nil || t.RefreshToken == "" {
		return notReady
	}
	idle := time.Duration(cfg.PollIdleMinutes) * time.Minute
	charging := time.Duration(cfg.PollChargingMinutes) * time.Minute
	tr := s.teslaTracker(cfg)
	c, _ := s.teslaClient(cfg, false)
	pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	setErr := func(state string, err error) {
		s.tesla.mu.Lock()
		s.tesla.lastPoll = time.Now()
		if state != "" {
			s.tesla.lastState = state
		}
		s.tesla.lastErr = ""
		if err != nil {
			s.tesla.lastErr = err.Error()
		}
		s.tesla.mu.Unlock()
	}

	active := tr.Current != nil && tr.Current.Active
	if !active {
		st, err := c.State(pctx)
		if err != nil {
			setErr("", err)
			return idle
		}
		if st != "online" {
			setErr(st, nil)
			return idle
		}
	}
	sample, err := c.ChargeState(pctx)
	if errors.Is(err, tesla.ErrAsleep) {
		setErr("asleep", nil)
		return idle
	}
	if err != nil {
		setErr("", err)
		if active {
			return charging
		}
		return idle
	}
	setErr("online", nil)
	s.tesla.mu.Lock()
	s.tesla.lastSample = &sample
	s.tesla.mu.Unlock()

	if sample.PluggedIn() || active {
		if m, err := toMap(sample); err == nil {
			_ = s.Store.Put("tesla_samples", fmt.Sprintf("%s:%d", cfg.VIN, sample.TS), cfg.VIN, m)
		}
	}
	finished := tr.Observe(sample)
	for _, sess := range []*tesla.Session{finished, tr.Current} {
		if sess == nil {
			continue
		}
		if m, err := toMap(sess); err == nil {
			m["ts"] = float64(sess.StartTS)
			_ = s.Store.Put("tesla_sessions", sess.Key, cfg.VIN, m)
		}
	}
	if tr.Current != nil && sample.Charging() {
		return charging
	}
	return idle
}

// teslaTracker returns the tracker for the configured VIN, restoring an
// open session from the store after a restart.
func (s *Server) teslaTracker(cfg tesla.Config) *tesla.Tracker {
	rate, currency := s.costRate()
	home := s.homeCheck(cfg.HomeRadiusM)
	s.tesla.mu.Lock()
	defer s.tesla.mu.Unlock()
	tr := s.tesla.tracker
	if tr == nil || s.tesla.trackerVIN != cfg.VIN {
		tr = &tesla.Tracker{VIN: cfg.VIN}
		rows, err := s.Store.App.FindRecordsByFilter("tesla_sessions", "device_sn = {:vin}", "-ts", 1, 0, map[string]any{"vin": cfg.VIN})
		if err == nil && len(rows) == 1 {
			var sess tesla.Session
			if json.Unmarshal([]byte(rows[0].GetString("payload")), &sess) == nil && sess.Active {
				tr.Current = &sess
			}
		}
		s.tesla.tracker, s.tesla.trackerVIN = tr, cfg.VIN
	}
	// Settings can change between polls.
	tr.Efficiency, tr.Rate, tr.Currency, tr.Home = cfg.ChargeEfficiency, rate, currency, home
	return tr
}
