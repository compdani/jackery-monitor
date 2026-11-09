package ecoflow

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ReconnectConfig is the silent-session auto-reconnect setting (the JSON of
// /data/ecoflow_reconnect.json).
//
// Manual reconnect always works. Auto force-restart of a connected-but-silent
// MQTT session only runs when AutoReconnect is on and the local time falls
// inside [ReconnectStart, ReconnectEnd) (or any time if both are unset).
type ReconnectConfig struct {
	AutoReconnect  bool    `json:"auto_reconnect"`
	ReconnectStart *string `json:"reconnect_start"`
	ReconnectEnd   *string `json:"reconnect_end"`
}

// ReconnectPatch is the partial update accepted by Set. Pointers distinguish
// "absent" from "clear": set ClearStart/ClearEnd to clear a bound.
type ReconnectPatch struct {
	AutoReconnect  *bool
	ReconnectStart *string // nil with SetStart=false: untouched
	ReconnectEnd   *string
	SetStart       bool
	SetEnd         bool
}

// ReconnectStore persists the config (raw, un-validated). Load returns
// ok=false when nothing is stored.
type ReconnectStore interface {
	Load() (ReconnectConfig, bool, error)
	Save(ReconnectConfig) error
}

// Reconnect holds the config behind a lock with validation, like the
// ecoflow_reconnect module.
type Reconnect struct {
	mu    sync.Mutex
	store ReconnectStore
	mem   *ReconnectConfig
}

// NewReconnect wraps a store (nil = in memory).
func NewReconnect(store ReconnectStore) *Reconnect { return &Reconnect{store: store} }

// DefaultReconnect are the defaults (auto on, any time).
func DefaultReconnect() ReconnectConfig { return ReconnectConfig{AutoReconnect: true} }

// parseHHMM returns minutes from midnight, ok=false when invalid.
func parseHHMM(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, false
	}
	var hh, mm int
	if _, err := fmt.Sscanf(parts[0], "%d", &hh); err != nil {
		return 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &mm); err != nil {
		return 0, false
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

func formatHHMM(min int) string {
	min = ((min % 1440) + 1440) % 1440
	return fmt.Sprintf("%02d:%02d", min/60, min%60)
}

// inSleepWindow is true if minute is inside [start, end), wrapping midnight.
func inSleepWindow(minute int, start, end string) bool {
	a, okA := parseHHMM(start)
	b, okB := parseHHMM(end)
	if !okA || !okB || a == b {
		return false
	}
	m := ((minute % 1440) + 1440) % 1440
	if a < b {
		return a <= m && m < b
	}
	return m >= a || m < b
}

// CleanReconnect validates and normalises a config. It errors for malformed
// times or a half-set window.
func CleanReconnect(c ReconnectConfig) (ReconnectConfig, error) {
	norm := func(p *string, name string) (*string, error) {
		if p == nil || *p == "" {
			return nil, nil
		}
		mins, ok := parseHHMM(*p)
		if !ok {
			return nil, fmt.Errorf("%s must be HH:MM", name)
		}
		s := formatHHMM(mins)
		return &s, nil
	}
	start, err := norm(c.ReconnectStart, "reconnect_start")
	if err != nil {
		return c, err
	}
	end, err := norm(c.ReconnectEnd, "reconnect_end")
	if err != nil {
		return c, err
	}
	if (start == nil) != (end == nil) {
		return c, errors.New("reconnect_start and reconnect_end must both be set or both cleared")
	}
	return ReconnectConfig{AutoReconnect: c.AutoReconnect, ReconnectStart: start, ReconnectEnd: end}, nil
}

func (r *Reconnect) loadRaw() ReconnectConfig {
	if r.store != nil {
		if c, ok, err := r.store.Load(); err == nil && ok {
			return c
		}
		return DefaultReconnect()
	}
	if r.mem != nil {
		return *r.mem
	}
	return DefaultReconnect()
}

// Get returns the validated config; invalid stored data falls back to the
// defaults.
func (r *Reconnect) Get() ReconnectConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := CleanReconnect(r.loadRaw())
	if err != nil {
		return DefaultReconnect()
	}
	return c
}

// Set merges a patch onto the stored config, validates and persists it.
func (r *Reconnect) Set(p ReconnectPatch) (ReconnectConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.loadRaw()
	if p.AutoReconnect != nil {
		cur.AutoReconnect = *p.AutoReconnect
	}
	if p.SetStart {
		cur.ReconnectStart = p.ReconnectStart
	}
	if p.SetEnd {
		cur.ReconnectEnd = p.ReconnectEnd
	}
	cleaned, err := CleanReconnect(cur)
	if err != nil {
		return ReconnectConfig{}, err
	}
	if r.store != nil {
		if err := r.store.Save(cleaned); err != nil {
			return cleaned, err
		}
	} else {
		r.mem = &cleaned
	}
	return cleaned, nil
}

// InAllowedWindow is true when auto force-reconnect may run (ignores the
// AutoReconnect flag). nowTS is a unix timestamp; tzOffsetS shifts to local.
func (r *Reconnect) InAllowedWindow(nowTS float64, tzOffsetS int) bool {
	cfg := r.Get()
	if cfg.ReconnectStart == nil || cfg.ReconnectEnd == nil {
		return true
	}
	local := int64(nowTS) + int64(tzOffsetS)
	minute := int(((local % 86400) + 86400) % 86400 / 60)
	return inSleepWindow(minute, *cfg.ReconnectStart, *cfg.ReconnectEnd)
}

// AutoReconnectAllowed is true when the flag is on and the window allows it.
func (r *Reconnect) AutoReconnectAllowed(nowTS float64, tzOffsetS int) bool {
	if !r.Get().AutoReconnect {
		return false
	}
	return r.InAllowedWindow(nowTS, tzOffsetS)
}
