package tesla

import (
	"bytes"
	"context"
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
	Sessions  *cache.SessionCache
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
	Index int    `json:"index"`
	Order int32  `json:"order"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Navigate sends up to MaxStops stops, in order, through the configured
// transport. It stops at the first failure, because later stops would be
// appended out of order; resend from the failed stop. The car must be awake.
func (c *Client) Navigate(ctx context.Context, stops []Stop) ([]StopResult, error) {
	if err := ValidateStops(stops); err != nil {
		return nil, err
	}
	if c.Config.NavTransport == TransportREST {
		return c.navigateREST(ctx, stops)
	}
	return c.navigateSigned(ctx, stops)
}

func (c *Client) navigateREST(ctx context.Context, stops []Stop) ([]StopResult, error) {
	results := make([]StopResult, 0, len(stops))
	for i, s := range stops {
		order := OrderFor(c.Config.NavOrderMode, i)
		res := StopResult{Index: i, Order: order}
		raw, err := c.do(ctx, "POST", c.vinPath()+"/command/navigation_gps_request", map[string]any{"lat": s.Lat, "lon": s.Lng, "order": order})
		if err == nil {
			var r struct {
				Result bool   `json:"result"`
				Reason string `json:"reason"`
			}
			if jerr := json.Unmarshal(raw, &r); jerr != nil {
				err = jerr
			} else if !r.Result {
				err = fmt.Errorf("car rejected stop: %s", r.Reason)
			}
		}
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			return results, nil
		}
		res.OK = true
		results = append(results, res)
	}
	return results, nil
}

func (c *Client) navigateSigned(ctx context.Context, stops []Stop) ([]StopResult, error) {
	if c.PrivateKeyHex == "" {
		return nil, errors.New("no virtual key generated")
	}
	key, err := commandKey(c.PrivateKeyHex)
	if err != nil {
		return nil, err
	}
	token, err := c.Tokens.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	conn := inet.NewConnection(c.Config.VIN, "Bearer "+token, c.Config.Host(), c.userAgent())
	defer conn.Close()
	car, err := vehicle.NewVehicle(conn, key, c.Sessions)
	if err != nil {
		return nil, err
	}
	if err := car.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer car.Disconnect()
	if err := car.StartSession(ctx, []universal.Domain{universal.Domain_DOMAIN_INFOTAINMENT}); err != nil {
		return nil, fmt.Errorf("start session (is the key paired with the car?): %w", err)
	}
	if c.Sessions != nil {
		defer func() { _ = car.UpdateCachedSessions(c.Sessions) }()
	}
	results := make([]StopResult, 0, len(stops))
	for i, s := range stops {
		order := OrderFor(c.Config.NavOrderMode, i)
		res := StopResult{Index: i, Order: order}
		err := sendInfotainment(ctx, car, conn, EncodeGPSAction(s, order))
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			return results, nil
		}
		res.OK = true
		results = append(results, res)
	}
	return results, nil
}

func sendInfotainment(ctx context.Context, car *vehicle.Vehicle, conn *inet.Connection, payload []byte) error {
	raw, err := car.Send(ctx, universal.Domain_DOMAIN_INFOTAINMENT, payload, conn.PreferredAuthMethod())
	if err != nil {
		return err
	}
	var resp carserver.Response
	if err := proto.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("unreadable vehicle response (command may have succeeded): %w", err)
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
