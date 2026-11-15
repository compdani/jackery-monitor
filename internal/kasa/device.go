package kasa

import (
	"context"
	"strings"
	"time"
)

// Credentials are Kasa cloud account credentials. They are required for the
// KASA SMART line even for local control; older plugs ignore them.
//
// The email is NOT lowercased: the KLAP challenge hash uses it exactly as
// registered. Only surrounding whitespace is trimmed.
type Credentials struct {
	Email    string
	Password string
}

// CredentialsFunc lazily supplies saved credentials. Returning nil means
// none are saved. It is called on every connect so edits take effect.
type CredentialsFunc func() *Credentials

// Info is the JSON-safe device description (Python _describe dict).
type Info struct {
	Host   string   `json:"host"`
	Alias  string   `json:"alias"`
	Model  *string  `json:"model"`
	Type   string   `json:"type"`
	IsOn   bool     `json:"is_on"`
	PowerW *float64 `json:"power_w"`
}

// Device is one connected plug. Update must refresh state read by the
// accessor methods.
type Device interface {
	Update(ctx context.Context) error
	TurnOn(ctx context.Context) error
	TurnOff(ctx context.Context) error
	Alias() string
	Model() string
	// DeviceType is python-kasa's DeviceType.name (e.g. "Plug", "Strip").
	DeviceType() string
	IsOn() bool
	// PowerW is the live draw in watts, or nil without energy monitoring.
	PowerW() *float64
}

// Backend abstracts the transport (python-kasa's Discover).
type Backend interface {
	// Discover finds devices by broadcast. creds may be nil.
	Discover(ctx context.Context, timeout time.Duration, creds *Credentials) (map[string]Device, error)
	// DiscoverSingle connects to one host and returns it after identifying
	// the protocol. The caller runs Update. creds may be nil.
	DiscoverSingle(ctx context.Context, host string, creds *Credentials) (Device, error)
}

// Describe converts a device into the dashboard dict.
func Describe(host string, dev Device) Info {
	alias := dev.Alias()
	if alias == "" {
		alias = host
	}
	var model *string
	if m := dev.Model(); m != "" {
		model = &m
	}
	typ := dev.DeviceType()
	if typ == "" {
		typ = "unknown"
	}
	return Info{
		Host:   host,
		Alias:  alias,
		Model:  model,
		Type:   typ,
		IsOn:   dev.IsOn(),
		PowerW: dev.PowerW(),
	}
}

func normalizeCreds(c *Credentials) *Credentials {
	if c == nil {
		return nil
	}
	return &Credentials{Email: strings.TrimSpace(c.Email), Password: c.Password}
}
