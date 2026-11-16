package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// teslaFake answers Tesla auth and Fleet API requests.
type teslaFake struct {
	mu       sync.Mutex
	state    string // vehicle state
	charge   string // charge_state JSON
	commands []map[string]any
	forms    []url.Values
}

func (f *teslaFake) Do(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	reply := func(status int, s string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(s)), Header: http.Header{}}, nil
	}
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/oauth2/v3/token"):
		form, _ := url.ParseQuery(string(body))
		f.forms = append(f.forms, form)
		return reply(200, `{"access_token":"acc","refresh_token":"ref","expires_in":28800,"scope":"openid vehicle_cmds"}`)
	case strings.HasSuffix(p, "/maps/api/geocode/json"):
		ll := r.URL.Query().Get("latlng")
		if ll == "40.00000,-90.00000" { // only a house 40 m north
			return reply(200, `{"status":"OK","results":[{"place_id":"Pfar","formatted_address":"far house","geometry":{"location":{"lat":40.00036,"lng":-90},"location_type":"ROOFTOP"}}]}`)
		}
		parts := strings.Split(ll, ",")
		return reply(200, `{"status":"OK","results":[{"place_id":"P`+ll+`","formatted_address":"addr","geometry":{"location":{"lat":`+parts[0]+`,"lng":`+parts[1]+`},"location_type":"ROOFTOP"}}]}`)
	case strings.HasSuffix(p, "/wake_up"):
		f.state = "online"
		return reply(200, `{"response":{"state":"asleep"}}`)
	case strings.HasSuffix(p, "/vehicle_data"):
		if f.state != "online" {
			return reply(408, `{"error":"vehicle unavailable"}`)
		}
		return reply(200, `{"response":{"charge_state":`+f.charge+`}}`)
	case strings.Contains(p, "/command/"):
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		f.commands = append(f.commands, m)
		return reply(200, `{"response":{"result":true,"reason":""}}`)
	case strings.HasPrefix(p, "/api/1/vehicles/"):
		return reply(200, `{"response":{"state":"`+f.state+`"}}`)
	}
	return reply(404, `{"error":"unexpected `+p+`"}`)
}

func teslaSetup(t *testing.T) (*Server, http.Handler, string, *teslaFake) {
	t.Helper()
	s, h := testServer(t)
	w := request(h, "POST", "/api/auth/setup", `{"username":"owner","password":"secret1"}`, "")
	if w.Code != 200 {
		t.Fatalf("setup %d %s", w.Code, w.Body)
	}
	var auth struct{ Token string }
	_ = json.Unmarshal(w.Body.Bytes(), &auth)
	fake := &teslaFake{state: "asleep"}
	s.tesla.HTTP = fake
	cfg := `{"client_id":"cid","client_secret":"csecret","vin":"5YJ3E1EA7KF000000","domain":"j.cdani.net","nav_transport":"rest"}`
	if w := request(h, "POST", "/api/tesla/config", cfg, auth.Token); w.Code != 200 {
		t.Fatalf("config %d %s", w.Code, w.Body)
	}
	return s, h, auth.Token, fake
}

func connectTesla(t *testing.T, h http.Handler, token string) {
	t.Helper()
	w := request(h, "GET", "/api/tesla/oauth/start", "", token)
	var start struct{ URL string }
	_ = json.Unmarshal(w.Body.Bytes(), &start)
	u, err := url.Parse(start.URL)
	if err != nil || u.Host != "auth.tesla.com" {
		t.Fatalf("authorize url %q", start.URL)
	}
	state := u.Query().Get("state")
	if u.Query().Get("redirect_uri") != "https://j.cdani.net/api/tesla/oauth/callback" {
		t.Fatalf("redirect_uri %s", u.Query().Get("redirect_uri"))
	}
	// Tesla redirects the browser back; no app session is required.
	w = request(h, "GET", "/api/tesla/oauth/callback?code=abc&state="+url.QueryEscape(state), "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Tesla connected") {
		t.Fatalf("callback %d %s", w.Code, w.Body)
	}
	// A state value works once.
	if w := request(h, "GET", "/api/tesla/oauth/callback?code=abc&state="+url.QueryEscape(state), "", ""); w.Code != 400 {
		t.Fatalf("replayed state accepted: %d", w.Code)
	}
}

func TestTeslaConfigHidesSecret(t *testing.T) {
	_, h, token, _ := teslaSetup(t)
	w := request(h, "GET", "/api/tesla/config", "", token)
	if strings.Contains(w.Body.String(), "csecret") || !strings.Contains(w.Body.String(), `"has_client_secret":true`) {
		t.Fatalf("config %s", w.Body)
	}
	// Saving without a secret keeps the stored one.
	request(h, "POST", "/api/tesla/config", `{"client_id":"cid","vin":"5YJ3E1EA7KF000000","domain":"j.cdani.net"}`, token)
	if w := request(h, "GET", "/api/tesla/config", "", token); !strings.Contains(w.Body.String(), `"has_client_secret":true`) {
		t.Fatalf("secret lost: %s", w.Body)
	}
	if w := request(h, "POST", "/api/tesla/config", `{"client_id":"cid","domain":"https://j.cdani.net"}`, token); w.Code != 400 {
		t.Fatalf("bad domain accepted: %d", w.Code)
	}
	if w := request(h, "GET", "/api/tesla/config", "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated config read: %d", w.Code)
	}
}

func TestTeslaPublicKeyIsPublicAndStable(t *testing.T) {
	_, h, token, _ := teslaSetup(t)
	const path = "/.well-known/appspecific/com.tesla.3p.public-key.pem"
	if w := request(h, "GET", path, "", ""); w.Code != 404 {
		t.Fatalf("key before generation: %d", w.Code)
	}
	w := request(h, "POST", "/api/tesla/key", `{}`, token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"created":true`) {
		t.Fatalf("generate %d %s", w.Code, w.Body)
	}
	pub := request(h, "GET", path, "", "")
	if pub.Code != 200 || !strings.HasPrefix(pub.Body.String(), "-----BEGIN PUBLIC KEY-----") {
		t.Fatalf("public key %d %q", pub.Code, pub.Body)
	}
	if strings.Contains(pub.Body.String(), "PRIVATE") {
		t.Fatal("private key exposed")
	}
	if w := request(h, "POST", "/api/tesla/key", `{}`, token); !strings.Contains(w.Body.String(), `"created":false`) {
		t.Fatalf("key replaced without rotate: %s", w.Body)
	}
	if again := request(h, "GET", path, "", ""); again.Body.String() != pub.Body.String() {
		t.Fatal("public key changed")
	}
}

func TestTeslaOAuthAndNavigateREST(t *testing.T) {
	_, h, token, fake := teslaSetup(t)
	connectTesla(t, h, token)
	if f := fake.forms[0]; f.Get("grant_type") != "authorization_code" || f.Get("client_secret") != "csecret" || f.Get("code") != "abc" {
		t.Fatalf("exchange form %v", f)
	}
	w := request(h, "GET", "/api/tesla/status", "", token)
	if !strings.Contains(w.Body.String(), `"connected":true`) {
		t.Fatalf("status %s", w.Body)
	}

	nine := `{"stops":[` + strings.TrimSuffix(strings.Repeat(`{"lat":1,"lng":1},`, 9), ",") + `]}`
	for _, path := range []string{"/api/tesla/navigate", "/api/tesla/navigate/gps"} {
		if w := request(h, "POST", path, nine, token); w.Code != 400 {
			t.Fatalf("%s: 9 stops accepted: %d", path, w.Code)
		}
	}
	// Without a Google key the places route refuses.
	w = request(h, "POST", "/api/tesla/navigate", `{"stops":[{"lat":30.1,"lng":-97.1},{"lat":30.2,"lng":-97.2}]}`, token)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "Google Maps API key") {
		t.Fatalf("no-key navigate %d %s", w.Code, w.Body)
	}
	if w := request(h, "POST", "/api/tesla/config", `{"client_id":"cid","vin":"5YJ3E1EA7KF000000","domain":"j.cdani.net","nav_transport":"rest","google_api_key":"gk"}`, token); w.Code != 200 {
		t.Fatalf("config %d %s", w.Code, w.Body)
	}
	if w := request(h, "GET", "/api/tesla/config", "", token); strings.Contains(w.Body.String(), `"gk"`) || !strings.Contains(w.Body.String(), `"has_google_api_key":true`) || !strings.Contains(w.Body.String(), `"has_client_secret":true`) {
		t.Fatalf("config leaks or lost secrets: %s", w.Body)
	}
	fake.commands = nil
	w = request(h, "POST", "/api/tesla/navigate", `{"stops":[{"lat":30.1,"lng":-97.1},{"lat":30.2,"lng":-97.2}]}`, token)
	if w.Code != 200 || len(fake.commands) != 1 || fake.commands[0]["waypoints"] != "refId:P30.10000,-97.10000,refId:P30.20000,-97.20000" {
		t.Fatalf("waypoints navigate %d %s %v", w.Code, w.Body, fake.commands)
	}

	fake.commands = nil
	w = request(h, "POST", "/api/tesla/navigate/gps", `{"stop_delay_seconds":0,"stops":[{"lat":30.1,"lng":-97.1},{"lat":30.2,"lng":-97.2},{"lat":30.3,"lng":-97.3}]}`, token)
	if w.Code != 200 {
		t.Fatalf("navigate %d %s", w.Code, w.Body)
	}
	if len(fake.commands) != 3 {
		t.Fatalf("commands %v", fake.commands)
	}
	for i, want := range []float64{0, 2, 2} {
		if fake.commands[i]["order"] != want {
			t.Fatalf("stop %d order %v", i, fake.commands[i]["order"])
		}
	}
	// Per-request override of the order semantics.
	fake.commands = nil
	request(h, "POST", "/api/tesla/navigate/gps", `{"stop_delay_seconds":0,"stops":[{"lat":30.1,"lng":-97.1},{"lat":30.2,"lng":-97.2}],"order_mode":"position"}`, token)
	if fake.commands[0]["order"] != 1.0 || fake.commands[1]["order"] != 2.0 {
		t.Fatalf("position orders %v", fake.commands)
	}
	if w := request(h, "GET", "/api/tesla/navigate/last", "", token); !strings.Contains(w.Body.String(), `"status":"done"`) {
		t.Fatalf("last job %s", w.Body)
	}
}

func TestTeslaNavigateOne(t *testing.T) {
	_, h, token, fake := teslaSetup(t)
	connectTesla(t, h, token)
	if w := request(h, "GET", "/api/tesla/status", "", token); !strings.Contains(w.Body.String(), `"nav_stop_delay_seconds":10`) {
		t.Fatalf("default delay missing: %s", w.Body)
	}
	for _, tc := range []struct {
		body  string
		order float64
	}{
		{`{"lat":30.1,"lng":-97.1}`, 2},
		{`{"lat":30.1,"lng":-97.1,"position":"append"}`, 2},
		{`{"lat":30.1,"lng":-97.1,"position":"prepend"}`, 1},
		{`{"lat":30.1,"lng":-97.1,"position":"prepend","order":3}`, 3},
	} {
		fake.commands = nil
		w := request(h, "POST", "/api/tesla/navigate/one", tc.body, token)
		if w.Code != 200 || len(fake.commands) != 1 || fake.commands[0]["order"] != tc.order || fake.commands[0]["lat"] != 30.1 {
			t.Fatalf("%s: %d %s %v", tc.body, w.Code, w.Body, fake.commands)
		}
	}
	if w := request(h, "POST", "/api/tesla/navigate/one", `{"lat":30.1,"lng":-97.1,"position":"middle"}`, token); w.Code != 400 {
		t.Fatalf("bad position accepted: %d", w.Code)
	}
	if w := request(h, "POST", "/api/tesla/navigate/one", `{"lat":0,"lng":0}`, token); w.Code != 400 {
		t.Fatalf("0,0 accepted: %d", w.Code)
	}
	if w := request(h, "POST", "/api/tesla/navigate/gps", `{"stop_delay_seconds":61,"stops":[{"lat":30.1,"lng":-97.1}]}`, token); w.Code != 400 {
		t.Fatalf("delay 61 accepted: %d", w.Code)
	}
	if w := request(h, "GET", "/api/tesla/navigate/last", "", token); !strings.Contains(w.Body.String(), `"position":"prepend"`) {
		t.Fatalf("last job %s", w.Body)
	}
	// Every command sent shows up in the raw command log, newest first.
	var log struct {
		Commands []struct {
			Command, Transport, Endpoint, Response string
			Params                                 map[string]any
			OK                                     bool
		} `json:"commands"`
	}
	w := request(h, "GET", "/api/tesla/commands?limit=2", "", token)
	_ = json.Unmarshal(w.Body.Bytes(), &log)
	if w.Code != 200 || len(log.Commands) != 2 {
		t.Fatalf("command log %d %s", w.Code, w.Body)
	}
	got := log.Commands[0]
	if got.Command != "navigation_gps_request" || got.Transport != "rest" || !got.OK || got.Params["order"] != 3.0 ||
		!strings.HasSuffix(got.Endpoint, "/command/navigation_gps_request") || !strings.Contains(got.Response, "result") {
		t.Fatalf("newest record %+v", got)
	}
	if w := request(h, "POST", "/api/tesla/commands/clear", "", token); w.Code != 200 {
		t.Fatalf("clear %d", w.Code)
	}
	if w := request(h, "GET", "/api/tesla/commands", "", token); !strings.Contains(w.Body.String(), `"commands":[]`) {
		t.Fatalf("after clear %s", w.Body)
	}
}

func TestTeslaResolvePlacesAndChosenPlaces(t *testing.T) {
	_, h, token, fake := teslaSetup(t)
	connectTesla(t, h, token)
	stops := `[{"lat":30.1,"lng":-97.1},{"lat":40,"lng":-90}]`
	if w := request(h, "POST", "/api/tesla/places/resolve", `{"stops":`+stops+`}`, token); w.Code != 409 {
		t.Fatalf("resolve without key %d", w.Code)
	}
	if w := request(h, "POST", "/api/tesla/config", `{"client_id":"cid","vin":"5YJ3E1EA7KF000000","domain":"j.cdani.net","nav_transport":"rest","google_api_key":"gk"}`, token); w.Code != 200 {
		t.Fatalf("config %d %s", w.Code, w.Body)
	}
	var out struct {
		MaxDistanceM float64 `json:"max_distance_m"`
		Stops        []struct {
			Match *struct {
				PlaceID string `json:"place_id"`
			} `json:"match"`
			Candidates []struct {
				PlaceID   string  `json:"place_id"`
				DistanceM float64 `json:"distance_m"`
			} `json:"candidates"`
			Error string `json:"error"`
		} `json:"stops"`
	}
	w := request(h, "POST", "/api/tesla/places/resolve", `{"stops":`+stops+`}`, token)
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out.MaxDistanceM != 30 || len(out.Stops) != 2 || out.Stops[0].Match == nil ||
		out.Stops[1].Match != nil || len(out.Stops[1].Candidates) != 1 || out.Stops[1].Candidates[0].PlaceID != "Pfar" || !strings.Contains(out.Stops[1].Error, "within 30 m") {
		t.Fatalf("resolve %d %s", w.Code, w.Body)
	}
	// The app's distance override.
	w = request(h, "POST", "/api/tesla/places/resolve", `{"max_distance_m":45,"stops":`+stops+`}`, token)
	if !strings.Contains(w.Body.String(), `"max_distance_m":45`) || strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("resolve 45 m %s", w.Body)
	}
	if w := request(h, "POST", "/api/tesla/places/resolve", `{"max_distance_m":1,"stops":`+stops+`}`, token); w.Code != 400 {
		t.Fatalf("1 m accepted: %d", w.Code)
	}

	// Sending: the far stop blocks the route at 30 m...
	fake.commands = nil
	w = request(h, "POST", "/api/tesla/navigate", `{"stops":`+stops+`}`, token)
	if w.Code != 502 || len(fake.commands) != 0 || !strings.Contains(w.Body.String(), "stop 2: no exact address") {
		t.Fatalf("navigate blocked %d %s", w.Code, w.Body)
	}
	// ...but goes once the user picked its place.
	w = request(h, "POST", "/api/tesla/navigate", `{"stops":[{"lat":30.1,"lng":-97.1},{"lat":40,"lng":-90,"place_id":"Pfar"}]}`, token)
	if w.Code != 200 || len(fake.commands) != 1 || fake.commands[0]["waypoints"] != "refId:P30.10000,-97.10000,refId:Pfar" {
		t.Fatalf("navigate chosen %d %s %v", w.Code, w.Body, fake.commands)
	}
}

func TestTeslaPollRecordsChargingCost(t *testing.T) {
	s, h, token, fake := teslaSetup(t)
	connectTesla(t, h, token)
	request(h, "POST", "/api/cost/plan", `{"type":"flat","rate_per_kwh":0.2,"currency":"USD"}`, token)
	fake.state = "online"
	now := time.Now().Unix()
	fake.charge = `{"charging_state":"Charging","charge_energy_added":0,"charger_power":7,"battery_level":40,"timestamp":` + itoa(now*1000) + `}`
	ctx := context.Background()
	if wait := s.pollTeslaOnce(ctx); wait != 5*time.Minute {
		t.Fatalf("charging poll interval %v", wait)
	}
	fake.charge = `{"charging_state":"Charging","charge_energy_added":9,"charger_power":7,"battery_level":55,"timestamp":` + itoa((now+3600)*1000) + `}`
	s.pollTeslaOnce(ctx)
	fake.charge = `{"charging_state":"Disconnected","timestamp":` + itoa((now+4000)*1000) + `}`
	if wait := s.pollTeslaOnce(ctx); wait != 30*time.Minute {
		t.Fatalf("idle poll interval %v", wait)
	}
	w := request(h, "GET", "/api/tesla/charging/sessions", "", token)
	var out struct {
		Sessions  []map[string]any
		TotalCost float64 `json:"total_cost"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	// 9 kWh added at 90% efficiency = 10 kWh from the wall at $0.20.
	if len(out.Sessions) != 1 || out.TotalCost != 2 || out.Sessions[0]["active"] != false {
		t.Fatalf("sessions %s", w.Body)
	}
	// While asleep the poller does not request vehicle_data.
	fake.state = "asleep"
	s.pollTeslaOnce(ctx)
}

func TestTeslaAppTokenAuthenticates(t *testing.T) {
	_, h, token, _ := teslaSetup(t)
	w := request(h, "POST", "/api/tesla/app_token", `{"days":30}`, token)
	var out struct{ Token string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Token == "" {
		t.Fatalf("app token %s", w.Body)
	}
	if w := request(h, "GET", "/api/tesla/status", "", out.Token); w.Code != 200 {
		t.Fatalf("app token rejected: %d", w.Code)
	}
}

func itoa(v int64) string { b, _ := json.Marshal(v); return string(b) }
