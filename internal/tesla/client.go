package tesla

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/teslamotors/vehicle-command/pkg/cache"
	"github.com/teslamotors/vehicle-command/pkg/connector/inet"
	"github.com/teslamotors/vehicle-command/pkg/protocol/protobuf/carserver"
	universal "github.com/teslamotors/vehicle-command/pkg/protocol/protobuf/universalmessage"
	"github.com/teslamotors/vehicle-command/pkg/vehicle"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// TokenSource returns a currently valid owner access token, refreshing and
// persisting tokens as needed.
type TokenSource interface {
	AccessToken(ctx context.Context) (string, error)
}

// ErrAsleep means the car is asleep or offline, so live data is unavailable.
var ErrAsleep = errors.New("vehicle is asleep or offline")

// APIError is a non-2xx Fleet API response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("fleet api: %d %s", e.Status, e.Message) }

// Client calls the Fleet API for one vehicle.
type Client struct {
	Config        Config
	HTTP          HTTPDoer
	Tokens        TokenSource
	PrivateKeyHex string
	// Sessions caches signed-command sessions between calls, avoiding a
	// handshake round trip. Optional.
	Sessions *cache.SessionCache
	// Places resolves stops to Google place IDs for NavigateWaypoints,
	// accepting matches within PlaceMaxDistanceM of the pin (0: default).
	Places            *PlaceResolver
	PlaceMaxDistanceM float64
	// StopDelay is the pause between per-stop GPS commands (zero: none).
	StopDelay time.Duration
	// Log records every command sent to the car. Optional.
	Log       *CommandLog
	UserAgent string
	Now       func() time.Time
	// PollInterval is how often Wake checks whether the car came online.
	PollInterval time.Duration
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return "jackery-monitor-tesla/1"
}

// do performs a Fleet API request and returns the "response" member.
func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	if c.Config.VIN == "" {
		return nil, errors.New("vin not configured")
	}
	token, err := c.Tokens.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+c.Config.Host()+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var env struct {
		Response json.RawMessage `json:"response"`
		Error    string          `json:"error"`
		Desc     string          `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode == http.StatusRequestTimeout {
		return nil, ErrAsleep
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(env.Error + " " + env.Desc)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 300 {
				msg = msg[:300]
			}
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	return env.Response, nil
}

func (c *Client) vinPath() string { return "/api/1/vehicles/" + url.PathEscape(c.Config.VIN) }

// State returns "online", "asleep" or "offline" without waking the car.
func (c *Client) State(ctx context.Context) (string, error) {
	raw, err := c.do(ctx, "GET", c.vinPath(), nil)
	if err != nil {
		return "", err
	}
	var v struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	return v.State, nil
}

// Wake wakes the car and waits until it reports online or ctx ends. Tesla
// limits wakes to 3 per minute, so this sends one wake and then polls.
func (c *Client) Wake(ctx context.Context) error {
	if st, err := c.State(ctx); err == nil && st == "online" {
		return nil
	}
	if _, err := c.do(ctx, "POST", c.vinPath()+"/wake_up", nil); err != nil {
		return err
	}
	interval := c.PollInterval
	if interval <= 0 {
		interval = 4 * time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("vehicle did not wake: %w", ctx.Err())
		case <-time.After(interval):
		}
		st, err := c.State(ctx)
		if err == nil && st == "online" {
			return nil
		}
	}
}

// ChargeSample is one charge_state (+ location) reading.
type ChargeSample struct {
	TS             int64    `json:"ts"`
	State          string   `json:"charging_state"`
	EnergyAddedKWh float64  `json:"charge_energy_added"`
	PowerKW        float64  `json:"charger_power"`
	Voltage        float64  `json:"charger_voltage"`
	CurrentA       float64  `json:"charger_actual_current"`
	BatteryLevel   int      `json:"battery_level"`
	ChargeLimit    int      `json:"charge_limit_soc"`
	FastCharger    bool     `json:"fast_charger_present"`
	FastType       string   `json:"fast_charger_type,omitempty"`
	MinutesToFull  int      `json:"minutes_to_full_charge"`
	Lat            *float64 `json:"latitude,omitempty"`
	Lng            *float64 `json:"longitude,omitempty"`
}

// Charging reports whether energy is (about to be) flowing.
func (s ChargeSample) Charging() bool { return s.State == "Charging" || s.State == "Starting" }

// PluggedIn reports whether a cable is connected.
func (s ChargeSample) PluggedIn() bool { return s.State != "" && s.State != "Disconnected" }

// ChargeState reads charge_state and drive_state location. It does not
// wake the car; an asleep car returns ErrAsleep.
func (c *Client) ChargeState(ctx context.Context) (ChargeSample, error) {
	raw, err := c.do(ctx, "GET", c.vinPath()+"/vehicle_data?endpoints="+url.QueryEscape("charge_state;location_data"), nil)
	if err != nil {
		return ChargeSample{}, err
	}
	var v struct {
		Charge struct {
			ChargeSample
			Timestamp int64 `json:"timestamp"`
		} `json:"charge_state"`
		Drive struct {
			Lat *float64 `json:"latitude"`
			Lng *float64 `json:"longitude"`
		} `json:"drive_state"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ChargeSample{}, err
	}
	s := v.Charge.ChargeSample
	s.TS = v.Charge.Timestamp / 1000
	if s.TS == 0 {
		s.TS = c.now().Unix()
	}
	s.Lat, s.Lng = v.Drive.Lat, v.Drive.Lng
	return s, nil
}

// StopResult is the outcome for one stop of a navigation batch.
type StopResult struct {
	Index   int    `json:"index"`
	Order   int32  `json:"order"`
	PlaceID string `json:"place_id,omitempty"`
	Address string `json:"address,omitempty"`
	// DistanceM is how far the matched place is from the stop's pin.
	DistanceM *float64 `json:"distance_m,omitempty"`
	OK        bool     `json:"ok"`
	Error     string   `json:"error,omitempty"`
}

// NavigateWaypoints sends up to MaxStops stops as one trip: every stop is
// resolved to a Google place first, and nothing is sent unless all resolve;
// then a single navigation_waypoints_request carries them in order. The car
// must be awake.
func (c *Client) NavigateWaypoints(ctx context.Context, stops []Stop) ([]StopResult, error) {
	if err := ValidateStops(stops); err != nil {
		return nil, err
	}
	results := make([]StopResult, len(stops))
	places := make([]Place, len(stops))
	unmatched := 0
	for i, s := range stops {
		results[i].Index = i
		if s.PlaceID != "" {
			places[i] = Place{ID: s.PlaceID}
			results[i].PlaceID = s.PlaceID
			continue
		}
		if c.Places == nil || c.Places.APIKey == "" {
			return nil, errors.New("multi-stop routes need a Google Maps API key (Geocoding API) in the Tesla settings")
		}
		pl, err := c.Places.Match(ctx, s, c.PlaceMaxDistanceM)
		if err != nil {
			// Keep checking the rest so every problem stop is reported.
			results[i].Error = err.Error()
			unmatched++
			continue
		}
		places[i] = pl
		d := pl.DistanceM
		results[i].PlaceID, results[i].Address, results[i].DistanceM = pl.ID, pl.Address, &d
	}
	if unmatched > 0 {
		for i := range results {
			if results[i].Error == "" {
				results[i].Error = "not sent: another stop needs an address"
			}
		}
		return results, nil
	}
	waypoints := WaypointsString(places)
	var err error
	if c.Config.NavTransport == TransportREST {
		err = c.restCommand(ctx, "navigation_waypoints_request", map[string]any{"waypoints": waypoints})
	} else {
		err = c.withInfotainment(ctx, func(send sendFunc) error {
			return send("navigation_waypoints_request", map[string]any{"waypoints": waypoints}, EncodeWaypointsAction(waypoints))
		})
	}
	for i := range results {
		if err != nil {
			results[i].Error = err.Error()
		} else {
			results[i].OK = true
		}
	}
	return results, nil
}

// SendOne inserts a single GPS stop before the existing route. Always order 1.
func (c *Client) SendOne(ctx context.Context, s Stop) (StopResult, error) {
	if err := ValidateStops([]Stop{s}); err != nil {
		return StopResult{}, err
	}
	res, err := c.sendGPS(ctx, []Stop{s}, []int32{OrderPrepend})
	if len(res) == 0 {
		return StopResult{Order: OrderPrepend}, err
	}
	return res[0], err
}

// sendGPS sends one navigation_gps_request per stop, StopDelay apart,
// stopping at the first failure.
func (c *Client) sendGPS(ctx context.Context, stops []Stop, orders []int32) ([]StopResult, error) {
	if len(stops) != 1 {
		return nil, errors.New("GPS supports single-stop sends only")
	}
	orders = []int32{OrderPrepend}

	results := make([]StopResult, 0, len(stops))
	sendAll := func(send func(Stop, int32) error) {
		for i, s := range stops {
			if i > 0 && c.StopDelay > 0 {
				select {
				case <-ctx.Done():
					results = append(results, StopResult{Index: i, Order: orders[i], Error: ctx.Err().Error()})
					return
				case <-time.After(c.StopDelay):
				}
			}
			order := orders[i]
			res := StopResult{Index: i, Order: order}
			if err := send(s, order); err != nil {
				res.Error = err.Error()
				results = append(results, res)
				return
			}
			res.OK = true
			results = append(results, res)
		}
	}
	if c.Config.NavTransport == TransportREST {
		sendAll(func(s Stop, order int32) error {
			return c.restCommand(ctx, "navigation_gps_request", map[string]any{"lat": s.Lat, "lon": s.Lng, "order": order})
		})
		return results, nil
	}
	err := c.withInfotainment(ctx, func(send sendFunc) error {
		sendAll(func(s Stop, order int32) error {
			return send("navigation_gps_request", map[string]any{"lat": s.Lat, "lon": s.Lng, "order": order}, EncodeGPSAction(s, order))
		})
		return nil
	})
	return results, err
}

// restCommand posts a plain Fleet API command and checks its result.
func (c *Client) restCommand(ctx context.Context, name string, body map[string]any) (err error) {
	start := time.Now()
	path := c.vinPath() + "/command/" + name
	var raw json.RawMessage
	defer func() {
		c.Log.Add(record(start, CommandRecord{
			Command: name, Transport: "rest", Endpoint: "POST " + path, Params: body, Response: string(raw),
		}, err))
	}()
	raw, err = c.do(ctx, "POST", path, body)
	if err != nil {
		return err
	}
	var r struct {
		Result bool   `json:"result"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if !r.Result {
		return fmt.Errorf("car rejected command: %s", r.Reason)
	}
	return nil
}

// sendFunc sends one serialized carserver.Action; name and params describe
// it for the command log.
type sendFunc func(name string, params map[string]any, payload []byte) error

// withInfotainment opens a signed infotainment session and runs fn with a
// sender for serialized carserver.Action payloads.
func (c *Client) withInfotainment(ctx context.Context, fn func(send sendFunc) error) error {
	if c.PrivateKeyHex == "" {
		return errors.New("no virtual key generated")
	}
	key, err := commandKey(c.PrivateKeyHex)
	if err != nil {
		return err
	}
	token, err := c.Tokens.AccessToken(ctx)
	if err != nil {
		return err
	}
	conn := inet.NewConnection(c.Config.VIN, "Bearer "+token, c.Config.Host(), c.userAgent())
	defer conn.Close()
	car, err := vehicle.NewVehicle(conn, key, c.Sessions)
	if err != nil {
		return err
	}
	if err := car.Connect(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer car.Disconnect()
	if err := car.StartSession(ctx, []universal.Domain{universal.Domain_DOMAIN_INFOTAINMENT}); err != nil {
		return fmt.Errorf("start session (is the key paired with the car?): %w", err)
	}
	if c.Sessions != nil {
		defer func() { _ = car.UpdateCachedSessions(c.Sessions) }()
	}
	return fn(func(name string, params map[string]any, payload []byte) (err error) {
		start := time.Now()
		var reply string
		defer func() {
			c.Log.Add(record(start, CommandRecord{
				Command: name, Transport: "signed", Endpoint: "signed_command (infotainment)",
				Params: params, PayloadHex: hex.EncodeToString(payload), Response: reply,
			}, err))
		}()
		return sendInfotainment(ctx, car, conn, payload, &reply)
	})
}

// sendInfotainment sends payload and stores the car's decoded reply in reply.
func sendInfotainment(ctx context.Context, car *vehicle.Vehicle, conn *inet.Connection, payload []byte, reply *string) error {
	raw, err := car.Send(ctx, universal.Domain_DOMAIN_INFOTAINMENT, payload, conn.PreferredAuthMethod())
	if err != nil {
		return err
	}
	var resp carserver.Response
	if err := proto.Unmarshal(raw, &resp); err != nil {
		*reply = hex.EncodeToString(raw)
		return fmt.Errorf("unreadable vehicle response (command may have succeeded): %w", err)
	}
	if j, err := protojson.Marshal(&resp); err == nil {
		*reply = string(j)
	}
	if resp.GetActionStatus().GetResult() == carserver.OperationStatus_E_OPERATIONSTATUS_ERROR {
		reason := resp.GetActionStatus().GetResultReason().GetPlainText()
		if reason == "" {
			reason = "unspecified error"
		}
		return fmt.Errorf("car could not execute command: %s", reason)
	}
	return nil
}
