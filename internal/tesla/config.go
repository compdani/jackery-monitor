// Package tesla talks to a Tesla vehicle through the Fleet API.
//
// It covers what jackery-monitor needs: OAuth tokens, the virtual-key pair
// whose public half is hosted at /.well-known/appspecific/, waking the car,
// reading charge state, and sending navigation stops. Signed commands use
// Tesla's github.com/teslamotors/vehicle-command library. The package has no
// PocketBase dependency; callers persist Config, Tokens and keys.
package tesla

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Pause between per-stop GPS commands.
const (
	DefaultStopDelaySeconds = 10
	MaxStopDelaySeconds     = 60
)

// MaxStops is the most navigation stops sent in one batch. The car's
// navigation does not show more, so this is a hard limit.
const MaxStops = 8

// Region base URLs (developer.tesla.com "Regions and Countries").
var regionHosts = map[string]string{
	"na": "fleet-api.prd.na.vn.cloud.tesla.com",
	"eu": "fleet-api.prd.eu.vn.cloud.tesla.com",
}

const (
	AuthorizeURL = "https://auth.tesla.com/oauth2/v3/authorize"
	TokenURL     = "https://fleet-auth.prd.vn.cloud.tesla.com/oauth2/v3/token"
	// UserScopes are requested for the vehicle owner's token.
	UserScopes = "openid offline_access vehicle_device_data vehicle_location vehicle_cmds vehicle_charging_cmds"
	// PartnerScopes are requested for the partner (client_credentials) token.
	PartnerScopes = "openid vehicle_device_data vehicle_cmds vehicle_charging_cmds"
)

// Navigation transports.
const (
	TransportSigned = "signed" // Vehicle Command Protocol via vehicle-command
	TransportREST   = "rest"   // plain Fleet API JSON command
)

// Navigation order modes for navigation_gps_request.
const (
	// OrderEnum treats order as RemoteNavTripOrder_E: the first stop
	// replaces the trip (0) and the rest are appended (2).
	OrderEnum = "enum"
	// OrderPosition sends each stop's 1-based position.
	OrderPosition = "position"
	// OrderOneThree sends 1 for the first stop and 3 for every later one.
	OrderOneThree = "one_three"
)

// Config is the non-secret integration configuration plus the client secret.
type Config struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	VIN          string `json:"vin"`
	Region       string `json:"region"`
	// Domain hosts the public key, e.g. "j.cdani.net". It must share its
	// root domain with an allowed origin of the Tesla developer app.
	Domain      string `json:"domain"`
	RedirectURI string `json:"redirect_uri,omitempty"`

	NavTransport string `json:"nav_transport,omitempty"`
	NavOrderMode string `json:"nav_order_mode,omitempty"`
	// NavStopDelaySeconds is the pause between per-stop GPS commands; the
	// car drops stops sent back to back. Default 10.
	NavStopDelaySeconds int `json:"nav_stop_delay_seconds,omitempty"`
	// GoogleAPIKey (Geocoding API) turns stop coordinates into the place IDs
	// that navigation_waypoints_request needs. Secret, like ClientSecret.
	GoogleAPIKey string `json:"google_api_key,omitempty"`
	// PlaceMaxDistanceM is how far a matched address may be from the stop's
	// pin for places sends. Default 50 m.
	PlaceMaxDistanceM float64 `json:"place_max_distance_m,omitempty"`

	// ChargeEfficiency converts energy added to the battery into energy
	// drawn from the wall (wall = added / efficiency). Default 0.9.
	ChargeEfficiency float64 `json:"charge_efficiency,omitempty"`
	// HomeRadiusM decides whether a charge happened at home, using the
	// monitor's saved location. Default 250 m.
	HomeRadiusM float64 `json:"home_radius_m,omitempty"`
	// PollIdleMinutes is how often the car's online/asleep state is checked
	// while not charging. PollChargingMinutes is the charge-state cadence
	// while charging. Defaults 30 and 5. Each check is a billable request.
	PollIdleMinutes     int  `json:"poll_idle_minutes,omitempty"`
	PollChargingMinutes int  `json:"poll_charging_minutes,omitempty"`
	PollingDisabled     bool `json:"polling_disabled,omitempty"`
}

// WithDefaults fills optional fields.
func (c Config) WithDefaults() Config {
	c.Region = strings.ToLower(strings.TrimSpace(c.Region))
	if c.Region == "" {
		c.Region = "na"
	}
	c.Domain = strings.ToLower(strings.TrimSpace(c.Domain))
	c.VIN = strings.ToUpper(strings.TrimSpace(c.VIN))
	if c.RedirectURI == "" && c.Domain != "" {
		c.RedirectURI = "https://" + c.Domain + "/api/tesla/oauth/callback"
	}
	if c.NavTransport == "" {
		c.NavTransport = TransportSigned
	}
	if c.NavOrderMode == "" {
		c.NavOrderMode = OrderEnum
	}
	if c.PlaceMaxDistanceM <= 0 {
		c.PlaceMaxDistanceM = DefaultPlaceMaxDistanceM
	}
	if c.NavStopDelaySeconds <= 0 {
		c.NavStopDelaySeconds = DefaultStopDelaySeconds
	}
	if c.ChargeEfficiency <= 0 {
		c.ChargeEfficiency = 0.9
	}
	if c.HomeRadiusM <= 0 {
		c.HomeRadiusM = 250
	}
	if c.PollIdleMinutes <= 0 {
		c.PollIdleMinutes = 30
	}
	if c.PollChargingMinutes <= 0 {
		c.PollChargingMinutes = 5
	}
	return c
}

// Validate checks a config after WithDefaults.
func (c Config) Validate() error {
	if c.ClientID == "" {
		return errors.New("client_id required")
	}
	if _, ok := regionHosts[c.Region]; !ok {
		return fmt.Errorf("region must be one of na, eu")
	}
	if c.Domain == "" || strings.ContainsAny(c.Domain, "/:@ ") {
		return errors.New("domain must be a bare host name, e.g. j.cdani.net")
	}
	if c.VIN != "" && len(c.VIN) != 17 {
		return errors.New("vin must be 17 characters")
	}
	if u, err := url.Parse(c.RedirectURI); err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("redirect_uri must be an https URL")
	}
	if c.NavTransport != TransportSigned && c.NavTransport != TransportREST {
		return errors.New("nav_transport must be signed or rest")
	}
	if c.NavOrderMode != OrderEnum && c.NavOrderMode != OrderPosition && c.NavOrderMode != OrderOneThree {
		return errors.New("nav_order_mode must be enum, position or one_three")
	}
	if c.PlaceMaxDistanceM < 0 || c.PlaceMaxDistanceM > 200 {
		return errors.New("place_max_distance_m must be from 0 to 200")
	}
	if c.NavStopDelaySeconds > MaxStopDelaySeconds {
		return fmt.Errorf("nav_stop_delay_seconds must be at most %d", MaxStopDelaySeconds)
	}
	if c.ChargeEfficiency > 1 {
		return errors.New("charge_efficiency must be in (0, 1]")
	}
	return nil
}

// Host is the Fleet API host for the configured region.
func (c Config) Host() string { return regionHosts[c.Region] }

// Audience is the token audience for the configured region.
func (c Config) Audience() string { return "https://" + c.Host() }

// PublicKeyURL is where Tesla fetches the virtual-key public key.
func (c Config) PublicKeyURL() string {
	return "https://" + c.Domain + PublicKeyPath
}

// PairingURL opens the Tesla app to add this integration's key to the car.
func (c Config) PairingURL() string {
	u := "https://tesla.com/_ak/" + c.Domain
	if c.VIN != "" {
		u += "?vin=" + url.QueryEscape(c.VIN)
	}
	return u
}

// PublicKeyPath is the path Tesla requires for the hosted public key.
const PublicKeyPath = "/.well-known/appspecific/com.tesla.3p.public-key.pem"
