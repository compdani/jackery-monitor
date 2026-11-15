package kasa

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeDev struct {
	alias, model, typ string
	on                bool
	power             *float64
	updateErr         error
}

func (f *fakeDev) Update(context.Context) error  { return f.updateErr }
func (f *fakeDev) TurnOn(context.Context) error  { f.on = true; return nil }
func (f *fakeDev) TurnOff(context.Context) error { f.on = false; return nil }
func (f *fakeDev) Alias() string                 { return f.alias }
func (f *fakeDev) Model() string                 { return f.model }
func (f *fakeDev) DeviceType() string            { return f.typ }
func (f *fakeDev) IsOn() bool                    { return f.on }
func (f *fakeDev) PowerW() *float64              { return f.power }

type fakeBackend struct {
	// each call to DiscoverSingle pops the next behaviour
	steps []func(creds *Credentials) (Device, error)
	calls []*Credentials
	disc  map[string]Device
}

func (b *fakeBackend) Discover(context.Context, time.Duration, *Credentials) (map[string]Device, error) {
	return b.disc, nil
}
func (b *fakeBackend) DiscoverSingle(_ context.Context, _ string, c *Credentials) (Device, error) {
	b.calls = append(b.calls, c)
	i := len(b.calls) - 1
	if i >= len(b.steps) {
		i = len(b.steps) - 1
	}
	return b.steps[i](c)
}

func testClient(b Backend, creds *Credentials) (*Client, *[]time.Duration) {
	var slept []time.Duration
	return &Client{
		Backend: b,
		Creds:   func() *Credentials { return creds },
		Sleep:   func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
	}, &slept
}

func TestRetryTransientThenSuccess(t *testing.T) {
	w := 12.5
	b := &fakeBackend{steps: []func(*Credentials) (Device, error){
		func(*Credentials) (Device, error) { return nil, errors.New("connection reset") },
		func(*Credentials) (Device, error) { return nil, errors.New("timeout") },
		func(*Credentials) (Device, error) {
			return &fakeDev{alias: "Heater", model: "KP125", typ: "Plug", on: true, power: &w}, nil
		},
	}}
	c, slept := testClient(b, nil)
	info, err := c.Status(context.Background(), "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if info.Alias != "Heater" || !info.IsOn || info.PowerW == nil || *info.PowerW != 12.5 {
		t.Fatalf("info = %+v", info)
	}
	if len(*slept) != 2 || (*slept)[0] != 400*time.Millisecond || (*slept)[1] != 800*time.Millisecond {
		t.Fatalf("backoff = %v", *slept)
	}
}

func TestRetryExhaustedReturnsTransientWithLastError(t *testing.T) {
	b := &fakeBackend{steps: []func(*Credentials) (Device, error){
		func(*Credentials) (Device, error) { return nil, errors.New("boom") },
	}}
	c, _ := testClient(b, nil)
	_, err := c.Status(context.Background(), "10.0.0.5")
	if !IsTransient(err) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if len(b.calls) != 3 {
		t.Fatalf("attempts = %d", len(b.calls))
	}
}

func TestConfigErrorIsNotRetried(t *testing.T) {
	b := &fakeBackend{steps: []func(*Credentials) (Device, error){
		func(*Credentials) (Device, error) { return nil, errors.New("AuthenticationError: bad challenge") },
	}}
	c, slept := testClient(b, nil)
	_, err := c.Status(context.Background(), "10.0.0.5")
	if !IsConfig(err) {
		t.Fatalf("want config error, got %v", err)
	}
	if !strings.Contains(err.Error(), "needs Kasa cloud credentials") {
		t.Fatalf("msg = %v", err)
	}
	if len(b.calls) != 1 || len(*slept) != 0 { // no creds → single pass, no retry
		t.Fatalf("calls=%d slept=%v", len(b.calls), *slept)
	}
}

func TestCredentialedPassFallsBackWithoutCreds(t *testing.T) {
	b := &fakeBackend{steps: []func(*Credentials) (Device, error){
		func(c *Credentials) (Device, error) { return nil, errors.New("KLAP challenge failed") },
		func(c *Credentials) (Device, error) { return &fakeDev{alias: "Old", on: false}, nil },
	}}
	c, _ := testClient(b, &Credentials{Email: "  User@Example.com ", Password: "Pw"})
	info, err := c.Status(context.Background(), "10.0.0.6")
	if err != nil || info.Alias != "Old" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	if b.calls[0] == nil || b.calls[0].Email != "User@Example.com" || b.calls[0].Password != "Pw" {
		t.Fatalf("pass 1 creds = %+v (email must be trimmed but case preserved)", b.calls[0])
	}
	if b.calls[1] != nil {
		t.Fatal("pass 2 must send no creds")
	}
}

func TestBothPassesAuthFailurePrefersCredentialedMessage(t *testing.T) {
	b := &fakeBackend{steps: []func(*Credentials) (Device, error){
		func(c *Credentials) (Device, error) {
			if c != nil {
				return nil, errors.New("credentials rejected (first)")
			}
			return nil, errors.New("klap challenge (second)")
		},
	}}
	c, _ := testClient(b, &Credentials{Email: "a@b.c", Password: "x"})
	_, err := c.Status(context.Background(), "h")
	if !IsConfig(err) || !strings.Contains(err.Error(), "(first)") ||
		!strings.Contains(err.Error(), "rejected AND credential-less fallback also failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetStateTurnsOnAndReturnsInfo(t *testing.T) {
	dev := &fakeDev{alias: "Plug", typ: "Plug"}
	b := &fakeBackend{steps: []func(*Credentials) (Device, error){
		func(*Credentials) (Device, error) { return dev, nil },
	}}
	c, _ := testClient(b, nil)
	info, err := c.SetState(context.Background(), "h", true)
	if err != nil || !info.IsOn {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	info, _ = c.SetState(context.Background(), "h", false)
	if info.IsOn {
		t.Fatal("expected off")
	}
}

func TestDiscoverSkipsFailedDescribe(t *testing.T) {
	b := &fakeBackend{disc: map[string]Device{
		"1.1.1.1": &fakeDev{alias: "A", model: "HS103", typ: "Plug"},
		"1.1.1.2": &fakeDev{updateErr: errors.New("nope")},
	}}
	c, _ := testClient(b, nil)
	out, err := c.Discover(context.Background(), 0)
	if err != nil || len(out) != 1 || out[0].Host != "1.1.1.1" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestDescribeDefaults(t *testing.T) {
	info := Describe("9.9.9.9", &fakeDev{})
	if info.Alias != "9.9.9.9" || info.Type != "unknown" || info.Model != nil || info.PowerW != nil {
		t.Fatalf("%+v", info)
	}
}
