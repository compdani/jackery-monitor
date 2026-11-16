package tesla

import (
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Golden bytes produced by serializing the same messages with the community
// tesla-protocol 6.3.0 Python bindings (car_server_pb2).
func TestEncodeGPSActionMatchesProtocolBindings(t *testing.T) {
	cases := []struct {
		stop  Stop
		order int32
		want  string
	}{
		{Stop{Lat: 30.222, Lng: -97.618}, OrderReplace, "1215aa03120946b6f3fdd4383e4011643bdf4f8d6758c0"},
		{Stop{Lat: 30.222, Lng: -97.618}, OrderAppend, "1217aa03140946b6f3fdd4383e4011643bdf4f8d6758c01802"},
		{Stop{Lat: 47.6062, Lng: -122.3321}, OrderPrepend, "1217aa031409e86a2bf697cd4740111ac05b2041955ec01801"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(EncodeGPSAction(c.stop, c.order)); got != c.want {
			t.Errorf("EncodeGPSAction(%v, %d) = %s, want %s", c.stop, c.order, got, c.want)
		}
	}
}

func TestEncodeWaypointsActionMatchesProtocolBindings(t *testing.T) {
	want := "1248d205450a4372656649643a4368494a4951427041473261685941525f3631323847635455456f2c72656649643a4368494a775f5f5f5f393647685941524356567767356354376330"
	got := hex.EncodeToString(EncodeWaypointsAction("refId:ChIJIQBpAG2ahYAR_6128GcTUEo,refId:ChIJw____96GhYARCVVwg5cT7c0"))
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestOrderFor(t *testing.T) {
	var enum, pos, alt []int32
	for i := 0; i < 3; i++ {
		enum = append(enum, OrderFor(OrderEnum, i))
		pos = append(pos, OrderFor(OrderPosition, i))
		alt = append(alt, OrderFor(OrderOneThree, i))
	}
	if alt[0] != 1 || alt[1] != 3 || alt[2] != 3 {
		t.Errorf("one_three orders %v", alt)
	}
	if enum[0] != 0 || enum[1] != 2 || enum[2] != 2 {
		t.Errorf("enum orders %v", enum)
	}
	if pos[0] != 1 || pos[1] != 2 || pos[2] != 3 {
		t.Errorf("position orders %v", pos)
	}
}

func TestValidateStops(t *testing.T) {
	ok := Stop{Lat: 1, Lng: 2}
	if err := ValidateStops([]Stop{ok}); err != nil {
		t.Fatal(err)
	}
	nine := make([]Stop, 9)
	for i := range nine {
		nine[i] = ok
	}
	if ValidateStops(nine) == nil {
		t.Error("9 stops accepted")
	}
	if ValidateStops(nil) == nil {
		t.Error("empty batch accepted")
	}
	if ValidateStops([]Stop{{Lat: 91, Lng: 0}}) == nil {
		t.Error("bad latitude accepted")
	}
}

func TestKeyRoundTrip(t *testing.T) {
	priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := PublicKeyPEM(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(pubPEM)
	if block == nil || block.Type != "PUBLIC KEY" {
		t.Fatalf("bad PEM %q", pubPEM)
	}
	if _, err := x509.ParsePKIXPublicKey(block.Bytes); err != nil {
		t.Fatal(err)
	}
	if _, err := commandKey(priv); err != nil {
		t.Fatal(err)
	}
}

func TestConfigDefaultsAndValidate(t *testing.T) {
	c := Config{ClientID: "id", Domain: " J.cdani.net ", VIN: "5yj3e1ea7kf000000"}.WithDefaults()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.RedirectURI != "https://j.cdani.net/api/tesla/oauth/callback" {
		t.Errorf("redirect %s", c.RedirectURI)
	}
	if c.PublicKeyURL() != "https://j.cdani.net/.well-known/appspecific/com.tesla.3p.public-key.pem" {
		t.Errorf("key url %s", c.PublicKeyURL())
	}
	if c.PairingURL() != "https://tesla.com/_ak/j.cdani.net?vin=5YJ3E1EA7KF000000" {
		t.Errorf("pairing %s", c.PairingURL())
	}
	if c.Host() != "fleet-api.prd.na.vn.cloud.tesla.com" {
		t.Errorf("host %s", c.Host())
	}
	bad := c
	bad.Domain = "https://j.cdani.net"
	if bad.Validate() == nil {
		t.Error("URL accepted as domain")
	}
}

type fakeHTTP struct {
	handle func(*http.Request) (int, string)
	reqs   []*http.Request
	bodies []string
}

func (f *fakeHTTP) Do(r *http.Request) (*http.Response, error) {
	var b []byte
	if r.Body != nil {
		b, _ = io.ReadAll(r.Body)
	}
	f.reqs = append(f.reqs, r)
	f.bodies = append(f.bodies, string(b))
	status, body := f.handle(r)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func TestRefreshKeepsRotatedToken(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	h := &fakeHTTP{handle: func(r *http.Request) (int, string) {
		return 200, `{"access_token":"a2","refresh_token":"r2","expires_in":28800}`
	}}
	c := Config{ClientID: "id", Domain: "j.cdani.net"}.WithDefaults()
	got, err := Refresh(context.Background(), h, c, Tokens{RefreshToken: "r1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "a2" || got.RefreshToken != "r2" || got.ExpiresAt != now.Unix()+28800 {
		t.Fatalf("tokens %+v", got)
	}
	if h.reqs[0].URL.String() != TokenURL {
		t.Errorf("token url %s", h.reqs[0].URL)
	}
	form, _ := url.ParseQuery(h.bodies[0])
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "r1" || form.Get("client_id") != "id" {
		t.Errorf("form %v", form)
	}
}

func TestRefreshLoginRequired(t *testing.T) {
	h := &fakeHTTP{handle: func(r *http.Request) (int, string) {
		return 401, `{"error":"login_required","error_description":"refresh token expired"}`
	}}
	_, err := Refresh(context.Background(), h, Config{ClientID: "id"}.WithDefaults(), Tokens{RefreshToken: "r"}, time.Now())
	te, ok := err.(*TokenError)
	if !ok || !te.LoginRequired() {
		t.Fatalf("err %v", err)
	}
}

type staticToken string

func (s staticToken) AccessToken(context.Context) (string, error) { return string(s), nil }

func testClient(h *fakeHTTP, transport string) *Client {
	cfg := Config{ClientID: "id", Domain: "j.cdani.net", VIN: "5YJ3E1EA7KF000000", NavTransport: transport}.WithDefaults()
	return &Client{Config: cfg, HTTP: h, Tokens: staticToken("tok"), PollInterval: time.Millisecond}
}

func TestNavigateRESTSendsOrdersAndStopsOnFailure(t *testing.T) {
	calls := 0
	h := &fakeHTTP{handle: func(r *http.Request) (int, string) {
		calls++
		if calls == 3 {
			return 200, `{"response":{"result":false,"reason":"busy"}}`
		}
		return 200, `{"response":{"result":true,"reason":""}}`
	}}
	c := testClient(h, TransportREST)
	stops := []Stop{{Lat: 1, Lng: 1}, {Lat: 2, Lng: 2}, {Lat: 3, Lng: 3}, {Lat: 4, Lng: 4}}
	res, err := c.NavigateGPS(context.Background(), stops)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || !res[0].OK || !res[1].OK || res[2].OK || !strings.Contains(res[2].Error, "busy") {
		t.Fatalf("results %+v", res)
	}
	var first, second map[string]any
	_ = json.Unmarshal([]byte(h.bodies[0]), &first)
	_ = json.Unmarshal([]byte(h.bodies[1]), &second)
	if first["order"] != 0.0 || second["order"] != 2.0 {
		t.Errorf("orders %v %v", first["order"], second["order"])
	}
	if h.reqs[0].Header.Get("Authorization") != "Bearer tok" {
		t.Error("missing bearer")
	}
	if !strings.HasSuffix(h.reqs[0].URL.Path, "/api/1/vehicles/5YJ3E1EA7KF000000/command/navigation_gps_request") {
		t.Errorf("path %s", h.reqs[0].URL.Path)
	}
}

func TestSendGPSPausesBetweenStopsAndSendOne(t *testing.T) {
	var at []time.Time
	h := &fakeHTTP{handle: func(r *http.Request) (int, string) {
		at = append(at, time.Now())
		return 200, `{"response":{"result":true,"reason":""}}`
	}}
	c := testClient(h, TransportREST)
	c.StopDelay = 40 * time.Millisecond
	if _, err := c.NavigateGPS(context.Background(), []Stop{{Lat: 1, Lng: 1}, {Lat: 2, Lng: 2}, {Lat: 3, Lng: 3}}); err != nil {
		t.Fatal(err)
	}
	if len(at) != 3 || at[1].Sub(at[0]) < 40*time.Millisecond || at[2].Sub(at[1]) < 40*time.Millisecond {
		t.Fatalf("no pause between stops: %v", at)
	}
	res, err := c.SendOne(context.Background(), Stop{Lat: 5, Lng: 5}, OrderPrepend)
	if err != nil || !res.OK || res.Order != 1 {
		t.Fatalf("send one %+v %v", res, err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(h.bodies[3]), &body)
	if body["order"] != 1.0 || body["lat"] != 5.0 {
		t.Fatalf("body %v", body)
	}
	// A cancelled context stops the pause and reports the stop as failed.
	ctx, cancel := context.WithCancel(context.Background())
	c.StopDelay = time.Hour
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	res2, _ := c.NavigateGPS(ctx, []Stop{{Lat: 1, Lng: 1}, {Lat: 2, Lng: 2}})
	if len(res2) != 2 || !res2[0].OK || res2[1].OK {
		t.Fatalf("cancel results %+v", res2)
	}
	for _, p := range []string{"", "append", "prepend", "x"} {
		o, err := OrderForPlacement(p)
		want := map[string]int32{"": 2, "append": 2, "prepend": 1}[p]
		if (p == "x") != (err != nil) || (err == nil && o != want) {
			t.Errorf("placement %q -> %d %v", p, o, err)
		}
	}
}

func geocodeFake(t *testing.T, fleet func(*http.Request) (int, string)) *fakeHTTP {
	return &fakeHTTP{handle: func(r *http.Request) (int, string) {
		if strings.HasPrefix(r.URL.String(), GeocodeURL) {
			q := r.URL.Query()
			if q.Get("key") != "gkey" {
				return 200, `{"status":"REQUEST_DENIED","error_message":"bad key"}`
			}
			ll := q.Get("latlng")
			if ll == "3.00000,3.00000" && q.Get("result_type") != "" {
				return 200, `{"status":"ZERO_RESULTS","results":[]}`
			}
			return 200, `{"status":"OK","results":[{"place_id":"P` + ll + `","formatted_address":"addr ` + ll + `"}]}`
		}
		return fleet(r)
	}}
}

func TestNavigateWaypointsSendsOneOrderedRoute(t *testing.T) {
	h := geocodeFake(t, func(r *http.Request) (int, string) { return 200, `{"response":{"result":true,"reason":""}}` })
	c := testClient(h, TransportREST)
	c.Places = &PlaceResolver{HTTP: h, APIKey: "gkey"}
	stops := []Stop{{Lat: 1, Lng: 1}, {Lat: 2, Lng: 2.5}, {Lat: 3, Lng: 3}}
	res, err := c.NavigateWaypoints(context.Background(), stops)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || !res[0].OK || !res[2].OK || res[1].PlaceID != "P2.00000,2.50000" {
		t.Fatalf("results %+v", res)
	}
	var cmds []string
	for i, r := range h.reqs {
		if strings.Contains(r.URL.Path, "/command/") {
			cmds = append(cmds, r.URL.Path)
			var body map[string]any
			_ = json.Unmarshal([]byte(h.bodies[i]), &body)
			want := "refId:P1.00000,1.00000,refId:P2.00000,2.50000,refId:P3.00000,3.00000"
			if body["waypoints"] != want {
				t.Fatalf("waypoints %v", body["waypoints"])
			}
		}
	}
	if len(cmds) != 1 || !strings.HasSuffix(cmds[0], "/command/navigation_waypoints_request") {
		t.Fatalf("commands %v", cmds)
	}
	// A resend uses the cache: no more geocoding requests.
	n := len(h.reqs)
	if _, err := c.NavigateWaypoints(context.Background(), stops); err != nil {
		t.Fatal(err)
	}
	if len(h.reqs) != n+1 {
		t.Fatalf("resend made %d requests", len(h.reqs)-n)
	}
}

func TestNavigateWaypointsSendsNothingIfAStopFails(t *testing.T) {
	h := geocodeFake(t, func(r *http.Request) (int, string) { return 200, `{"response":{"result":true}}` })
	c := testClient(h, TransportREST)
	c.Places = &PlaceResolver{HTTP: h, APIKey: "wrong"}
	res, err := c.NavigateWaypoints(context.Background(), []Stop{{Lat: 1, Lng: 1}, {Lat: 2, Lng: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].OK || !strings.Contains(res[0].Error, "REQUEST_DENIED") {
		t.Fatalf("results %+v", res)
	}
	for _, r := range h.reqs {
		if strings.Contains(r.URL.Path, "/command/") {
			t.Fatal("command sent despite failed lookup")
		}
	}
	c.Places = nil
	if _, err := c.NavigateWaypoints(context.Background(), []Stop{{Lat: 1, Lng: 1}}); err == nil || !strings.Contains(err.Error(), "Google Maps API key") {
		t.Fatalf("missing key err %v", err)
	}
}

func TestChargeStateAsleep(t *testing.T) {
	h := &fakeHTTP{handle: func(r *http.Request) (int, string) { return 408, `{"error":"vehicle unavailable"}` }}
	if _, err := testClient(h, TransportSigned).ChargeState(context.Background()); err != ErrAsleep {
		t.Fatalf("err %v", err)
	}
}

func TestChargeStateParses(t *testing.T) {
	h := &fakeHTTP{handle: func(r *http.Request) (int, string) {
		if !strings.Contains(r.URL.RawQuery, "endpoints=charge_state%3Blocation_data") {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		return 200, `{"response":{"charge_state":{"charging_state":"Charging","charge_energy_added":3.5,"charger_power":7,"battery_level":55,"timestamp":1700000000000},"drive_state":{"latitude":30.1,"longitude":-97.2}}}`
	}}
	s, err := testClient(h, TransportSigned).ChargeState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.TS != 1700000000 || !s.Charging() || s.EnergyAddedKWh != 3.5 || s.Lat == nil || *s.Lat != 30.1 {
		t.Fatalf("sample %+v", s)
	}
}

func TestWakePollsUntilOnline(t *testing.T) {
	states := []string{"asleep", "asleep", "online"}
	h := &fakeHTTP{handle: func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/wake_up") {
			return 200, `{"response":{"state":"asleep"}}`
		}
		st := states[0]
		if len(states) > 1 {
			states = states[1:]
		}
		return 200, `{"response":{"state":"` + st + `"}}`
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := testClient(h, TransportSigned).Wake(ctx); err != nil {
		t.Fatal(err)
	}
	wakes := 0
	for _, r := range h.reqs {
		if strings.HasSuffix(r.URL.Path, "/wake_up") {
			wakes++
		}
	}
	if wakes != 1 {
		t.Errorf("wake_up sent %d times", wakes)
	}
}

func flatRate(r float64) RateFunc { return func(int64) float64 { return r } }

func TestTrackerSessionCost(t *testing.T) {
	tr := &Tracker{VIN: "V", Efficiency: 0.9, Currency: "USD", Rate: flatRate(0.30)}
	steps := []ChargeSample{
		{TS: 1000, State: "Charging", EnergyAddedKWh: 0, BatteryLevel: 40, PowerKW: 7},
		{TS: 1600, State: "Charging", EnergyAddedKWh: 4.5, BatteryLevel: 50, PowerKW: 7.2},
		{TS: 2200, State: "Stopped", EnergyAddedKWh: 9, BatteryLevel: 60},
		{TS: 2800, State: "Charging", EnergyAddedKWh: 9, BatteryLevel: 60},
		{TS: 3400, State: "Complete", EnergyAddedKWh: 13.5, BatteryLevel: 70},
	}
	for _, s := range steps {
		if f := tr.Observe(s); f != nil {
			t.Fatalf("session closed early at %d", s.TS)
		}
	}
	f := tr.Observe(ChargeSample{TS: 4000, State: "Disconnected"})
	if f == nil {
		t.Fatal("unplug did not close the session")
	}
	// 13.5 kWh into the battery at 90% => 15 kWh from the wall at $0.30.
	if f.AddedKWh != 13.5 || f.WallKWh != 15 || f.Cost != 4.5 || f.UnpricedKWh != 0 {
		t.Fatalf("session %+v", f)
	}
	if f.StartLevel != 40 || f.EndLevel != 70 || f.EndTS != 3400 || f.UnpluggedTS != 4000 || f.PeakPowerKW != 7.2 {
		t.Fatalf("session %+v", f)
	}
	if tr.Current != nil {
		t.Fatal("tracker still has a current session")
	}
}

func TestTrackerTOUAndLateStart(t *testing.T) {
	// $0.10 before ts 2000, $0.40 after.
	rate := func(ts int64) float64 {
		if ts < 2000 {
			return 0.10
		}
		return 0.40
	}
	tr := &Tracker{VIN: "V", Efficiency: 1, Rate: rate}
	tr.Observe(ChargeSample{TS: 1000, State: "Charging", EnergyAddedKWh: 2}) // first seen mid-session
	tr.Observe(ChargeSample{TS: 1800, State: "Charging", EnergyAddedKWh: 3}) // +1 at midpoint 1400 -> 0.10
	tr.Observe(ChargeSample{TS: 3000, State: "Charging", EnergyAddedKWh: 5}) // +2 at midpoint 2400 -> 0.80
	s := tr.Current
	if s.UnpricedKWh != 2 || s.WallKWh != 5 {
		t.Fatalf("session %+v", s)
	}
	if got := round(s.Cost, 4); got != 0.2+0.1+0.8 {
		t.Fatalf("cost %v", got)
	}
}

func TestTrackerNewPlugInResetsCounter(t *testing.T) {
	tr := &Tracker{VIN: "V", Efficiency: 1, Rate: flatRate(1)}
	tr.Observe(ChargeSample{TS: 100, State: "Charging", EnergyAddedKWh: 0})
	tr.Observe(ChargeSample{TS: 200, State: "Charging", EnergyAddedKWh: 10})
	f := tr.Observe(ChargeSample{TS: 900, State: "Charging", EnergyAddedKWh: 1})
	if f == nil || f.AddedKWh != 10 {
		t.Fatalf("old session %+v", f)
	}
	if tr.Current == nil || tr.Current.StartTS != 900 || tr.Current.AddedKWh != 1 {
		t.Fatalf("new session %+v", tr.Current)
	}
}

func TestTrackerAwayAndSupercharger(t *testing.T) {
	away := &Tracker{VIN: "V", Efficiency: 1, Rate: flatRate(1), Home: func(ChargeSample) bool { return false }}
	away.Observe(ChargeSample{TS: 1, State: "Charging"})
	away.Observe(ChargeSample{TS: 2, State: "Charging", EnergyAddedKWh: 4})
	if s := away.Current; s.AtHome || s.Cost != 0 || s.UnpricedKWh != 4 {
		t.Fatalf("away session %+v", s)
	}
	sc := &Tracker{VIN: "V", Efficiency: 1, Rate: flatRate(1), Home: func(ChargeSample) bool { return true }}
	sc.Observe(ChargeSample{TS: 1, State: "Charging", FastCharger: true})
	if sc.Current.AtHome {
		t.Fatal("supercharger session counted as home")
	}
}

func TestDistanceM(t *testing.T) {
	// One degree of latitude is about 111.2 km.
	if d := DistanceM(0, 0, 1, 0); d < 111000 || d > 111400 {
		t.Fatalf("distance %v", d)
	}
}
