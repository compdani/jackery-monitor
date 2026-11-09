package kasa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// DefaultRetryAttempts and DefaultRetryBackoff mirror _KASA_RETRY_ATTEMPTS
// and _KASA_RETRY_BACKOFF_S.
const DefaultRetryAttempts = 3

// DefaultRetryBackoff are the delays between attempts.
var DefaultRetryBackoff = []time.Duration{400 * time.Millisecond, 800 * time.Millisecond}

// Client is the Go analogue of the kasa_client.py module functions.
type Client struct {
	Backend Backend
	// Creds lazily supplies saved Kasa cloud credentials. May be nil.
	Creds CredentialsFunc
	// Attempts is the per-call retry count (default 3).
	Attempts int
	// Backoff are the delays between attempts (default 0.4s, 0.8s).
	Backoff []time.Duration
	// Sleep is overridable in tests; defaults to a ctx-aware time.Sleep.
	Sleep func(ctx context.Context, d time.Duration) error
	Log   *slog.Logger

	versionOnce sync.Once
}

// NewClient returns a Client using the native transports.
func NewClient(creds CredentialsFunc) *Client {
	return &Client{Backend: NewNativeBackend(), Creds: creds}
}

func (c *Client) logger() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func (c *Client) credentials() *Credentials {
	if c.Creds == nil {
		return nil
	}
	return normalizeCreds(c.Creds())
}

func (c *Client) attempts() int {
	if c.Attempts > 0 {
		return c.Attempts
	}
	return DefaultRetryAttempts
}

func (c *Client) backoff() []time.Duration {
	if len(c.Backoff) > 0 {
		return c.Backoff
	}
	return DefaultRetryBackoff
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Discover does best-effort LAN discovery. A failed or empty discovery
// returns an empty slice and no error (common in Docker bridge networks).
func (c *Client) Discover(ctx context.Context, timeout time.Duration) ([]Info, error) {
	if c.Backend == nil {
		return nil, newErr(KindConfig, nil, "kasa backend not configured")
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	devs, err := c.Backend.Discover(ctx, timeout, c.credentials())
	if err != nil {
		c.logger().Warn("Kasa discover failed", "err", err)
		return []Info{}, nil
	}
	out := []Info{}
	for host, dev := range devs {
		if err := dev.Update(ctx); err != nil {
			c.logger().Warn("Kasa describe failed", "host", host, "err", err)
			continue
		}
		out = append(out, Describe(host, dev))
	}
	c.logger().Info("Kasa discovery found devices", "count", len(out))
	return out, nil
}

// withRetry runs fn with bounded retries on transient failures. Config
// errors (bad creds, missing dependency) break out immediately.
func (c *Client) withRetry(ctx context.Context, op string, fn func() (Info, error)) (Info, error) {
	var last error
	n := c.attempts()
	bo := c.backoff()
	for attempt := 0; attempt < n; attempt++ {
		info, err := fn()
		if err == nil {
			return info, nil
		}
		if IsConfig(err) {
			return Info{}, err
		}
		if ctx.Err() != nil {
			return Info{}, ctx.Err()
		}
		last = err
		if attempt < n-1 {
			idx := attempt
			if idx >= len(bo) {
				idx = len(bo) - 1
			}
			delay := bo[idx]
			c.logger().Info("Kasa attempt failed; retrying",
				"op", op, "attempt", attempt+1, "of", n, "err", err, "delay", delay)
			if serr := c.sleep(ctx, delay); serr != nil {
				return Info{}, serr
			}
		}
	}
	if last == nil {
		last = newErr(KindGeneric, nil, "%s failed", op)
	}
	return Info{}, last
}

// Status reads the current state of one device by IP, retrying transients.
func (c *Client) Status(ctx context.Context, host string) (Info, error) {
	return c.withRetry(ctx, fmt.Sprintf("status(%s)", host), func() (Info, error) {
		dev, err := c.connect(ctx, host)
		if err != nil {
			return Info{}, err
		}
		return Describe(host, dev), nil
	})
}

// SetState turns a device on/off by IP, retrying transients.
func (c *Client) SetState(ctx context.Context, host string, on bool) (Info, error) {
	return c.withRetry(ctx, fmt.Sprintf("set_state(%s, on=%v)", host, on), func() (Info, error) {
		dev, err := c.connect(ctx, host)
		if err != nil {
			return Info{}, err
		}
		if on {
			err = dev.TurnOn(ctx)
		} else {
			err = dev.TurnOff(ctx)
		}
		if err != nil {
			return Info{}, err
		}
		if err := dev.Update(ctx); err != nil {
			return Info{}, err
		}
		return Describe(host, dev), nil
	})
}

// IsAuthFailure matches the Python heuristic on the error text.
func IsAuthFailure(err error) bool {
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "challenge") || strings.Contains(lower, "credentials") ||
		strings.Contains(lower, "klap") || strings.Contains(lower, "authenticationerror")
}

func errName(err error) string {
	var ne interface{ TypeName() string }
	if errors.As(err, &ne) {
		return ne.TypeName()
	}
	return fmt.Sprintf("%T", err)
}

func (c *Client) tryConnect(ctx context.Context, host string, creds *Credentials) (Device, error) {
	dev, err := c.Backend.DiscoverSingle(ctx, host, creds)
	if err != nil {
		return nil, err
	}
	if err := dev.Update(ctx); err != nil {
		return nil, err
	}
	return dev, nil
}

// connect mirrors _connect: pass 1 with saved cloud creds (required for
// SMART-line plugs), pass 2 without (older plug fallback).
func (c *Client) connect(ctx context.Context, host string) (Device, error) {
	if c.Backend == nil {
		return nil, newErr(KindConfig, nil, "kasa backend not configured")
	}
	creds := c.credentials()
	var firstErr error
	if creds != nil {
		dev, err := c.tryConnect(ctx, host, creds)
		if err == nil {
			return dev, nil
		}
		if IsConfig(err) {
			return nil, err
		}
		if !IsAuthFailure(err) {
			return nil, newErr(KindTransient, err,
				"could not reach Kasa device at %s: %s: %v", host, errName(err), err)
		}
		firstErr = err
		c.logger().Info("Kasa credentialed connect rejected, retrying without creds (older plug fallback)", "host", host)
	}
	dev, err := c.tryConnect(ctx, host, nil)
	if err == nil {
		return dev, nil
	}
	if IsConfig(err) {
		return nil, err
	}
	// Prefer the original credentialed error if both passes failed.
	use := err
	if firstErr != nil {
		use = firstErr
	}
	msg := fmt.Sprintf("%s: %v", errName(use), use)
	lower := strings.ToLower(use.Error())
	if IsAuthFailure(use) {
		if firstErr == nil {
			msg += " — this device needs Kasa cloud credentials. Add them in the Automation tab."
		} else {
			msg += " — saved Kasa cloud credentials were rejected AND " +
				"credential-less fallback also failed. Verify the " +
				"email matches your Kasa account exactly (case as " +
				"registered) or re-add this device."
		}
		return nil, newErr(KindConfig, use, "could not reach Kasa device at %s: %s", host, msg)
	}
	if strings.Contains(lower, "zoneinfo") || strings.Contains(lower, "no time zone") {
		msg += " — server is missing tzdata; if you're seeing this, the latest image hasn't deployed yet."
	}
	return nil, newErr(KindTransient, use, "could not reach Kasa device at %s: %s", host, msg)
}
