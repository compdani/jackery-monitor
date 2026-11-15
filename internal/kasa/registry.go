package kasa

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SavedDevice is one registry row (kasa_devices.json "devices" entry).
// JSON keys match the Python dicts.
type SavedDevice struct {
	Host             string   `json:"host"`
	Alias            string   `json:"alias"`
	Model            *string  `json:"model"`
	Type             *string  `json:"type"`
	Added            float64  `json:"added"`
	LastTested       *float64 `json:"last_tested"`
	JackeryDeviceSN  *string  `json:"jackery_device_sn"`
	LastSeenTS       *float64 `json:"last_seen_ts,omitempty"`
	LastError        *string  `json:"last_error,omitempty"`
	ConsecutiveFails int      `json:"consecutive_failures,omitempty"`
	LastFailedTS     *float64 `json:"last_failed_ts,omitempty"`
	LastKnownIsOn    *bool    `json:"last_known_is_on,omitempty"`
}

// Store persists the registry. The other agent can implement it on top of
// PocketBase; FileStore matches the legacy JSON file.
type Store interface {
	Load() ([]SavedDevice, error)
	Save([]SavedDevice) error
}

// FileStore is a JSON file store: {"devices": [...]}.
type FileStore struct{ Path string }

// Load reads the file; a missing file is an empty registry.
func (f FileStore) Load() ([]SavedDevice, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Devices []SavedDevice `json:"devices"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc.Devices, nil
}

// Save writes atomically (tmp + rename).
func (f FileStore) Save(devs []SavedDevice) error {
	if devs == nil {
		devs = []SavedDevice{}
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(map[string]any{"devices": devs}, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// Registry is the persisted set of tested Kasa plugs (KasaRegistry).
type Registry struct {
	mu      sync.Mutex
	devices []SavedDevice
	store   Store
	// Now is overridable in tests.
	Now func() time.Time
	Log *slog.Logger
}

// NewRegistry loads from store (nil store = memory only). A load error
// starts empty and is logged, like the Python version.
func NewRegistry(store Store) *Registry {
	r := &Registry{store: store, Now: time.Now}
	if store != nil {
		devs, err := store.Load()
		if err != nil {
			r.logger().Warn("kasa devices store unreadable; starting empty", "err", err)
		} else {
			r.devices = devs
		}
	}
	return r
}

func (r *Registry) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *Registry) nowTS() float64 { return float64(r.Now().UnixNano()) / 1e9 }

func (r *Registry) save() {
	if r.store == nil {
		return
	}
	if err := r.store.Save(r.devices); err != nil {
		r.logger().Error("failed to save kasa devices", "err", err)
	}
}

// ListAll returns a copy of every saved device.
func (r *Registry) ListAll() []SavedDevice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]SavedDevice(nil), r.devices...)
}

// ListForJackery returns devices assigned to the given Jackery serial plus
// every unassigned (legacy) device, so pre-assignment data is never hidden.
func (r *Registry) ListForJackery(sn string) []SavedDevice {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []SavedDevice{}
	for _, d := range r.devices {
		if d.JackeryDeviceSN == nil || *d.JackeryDeviceSN == "" || *d.JackeryDeviceSN == sn {
			out = append(out, d)
		}
	}
	return out
}

func (r *Registry) find(host string) *SavedDevice {
	for i := range r.devices {
		if r.devices[i].Host == host {
			return &r.devices[i]
		}
	}
	return nil
}

// Get returns a copy of the device with that host.
func (r *Registry) Get(host string) (SavedDevice, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d := r.find(host); d != nil {
		return *d, true
	}
	return SavedDevice{}, false
}

// UpsertOpts are the optional Upsert arguments.
type UpsertOpts struct {
	Alias      string
	Model      *string
	Type       *string
	MarkTested bool
	// JackerySN nil leaves the assignment alone; a pointer to "" unassigns.
	JackerySN *string
}

// Upsert adds or updates a device by host.
func (r *Registry) Upsert(host string, o UpsertOpts) (SavedDevice, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return SavedDevice{}, errors.New("host is required")
	}
	alias := strings.TrimSpace(o.Alias)
	if alias == "" {
		alias = host
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.nowTS()
	var assignment *string
	if o.JackerySN != nil {
		if s := strings.TrimSpace(*o.JackerySN); s != "" {
			assignment = &s
		}
	}
	if ex := r.find(host); ex != nil {
		ex.Alias = alias
		if o.Model != nil {
			ex.Model = o.Model
		}
		if o.Type != nil {
			ex.Type = o.Type
		}
		if o.MarkTested {
			ex.LastTested = &now
		}
		if o.JackerySN != nil {
			ex.JackeryDeviceSN = assignment
		}
		r.save()
		return *ex, nil
	}
	d := SavedDevice{
		Host: host, Alias: alias, Model: o.Model, Type: o.Type,
		Added: now, JackeryDeviceSN: assignment,
	}
	if o.MarkTested {
		d.LastTested = &now
	}
	r.devices = append(r.devices, d)
	r.save()
	return d, nil
}

// Delete removes a device; true if it existed.
func (r *Registry) Delete(host string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	before := len(r.devices)
	kept := r.devices[:0]
	for _, d := range r.devices {
		if d.Host != host {
			kept = append(kept, d)
		}
	}
	r.devices = kept
	changed := len(r.devices) != before
	if changed {
		r.save()
	}
	return changed
}

// HostsInUse returns saved hosts referenced by any rule's "kasa_host".
func HostsInUse(rules []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, rule := range rules {
		if h, _ := rule["kasa_host"].(string); h != "" {
			out[h] = true
		}
	}
	return out
}

// ProbeResult is the outcome of one reconcile probe.
type ProbeResult struct {
	Success bool
	IsOn    *bool
	Error   string
	Model   string
	Alias   string
}

// MaxErrorLen truncates stored probe errors.
const MaxErrorLen = 240

// UpdateProbe records a probe outcome. It returns false for unknown hosts.
func (r *Registry) UpdateProbe(host string, p ProbeResult) (SavedDevice, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.find(host)
	if d == nil {
		return SavedDevice{}, false
	}
	now := r.nowTS()
	if p.Success {
		d.LastSeenTS = &now
		d.LastError = nil
		d.ConsecutiveFails = 0
		if p.IsOn != nil {
			v := *p.IsOn
			d.LastKnownIsOn = &v
		}
		// Don't overwrite a user-edited alias; only fill when empty.
		if p.Model != "" && (d.Model == nil || *d.Model == "") {
			m := p.Model
			d.Model = &m
		}
		if p.Alias != "" && d.Alias == "" {
			d.Alias = p.Alias
		}
	} else {
		msg := p.Error
		if msg == "" {
			msg = "probe failed"
		}
		if len(msg) > MaxErrorLen {
			msg = msg[:MaxErrorLen]
		}
		d.LastError = &msg
		d.ConsecutiveFails++
		d.LastFailedTS = &now
	}
	r.save()
	return *d, true
}

// Status values returned by StatusOf.
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusUnknown = "unknown"
)

// StatusOf is "online" | "offline" | "unknown" (never probed).
func (r *Registry) StatusOf(d SavedDevice) string {
	if d.ConsecutiveFails > 0 {
		return StatusOffline
	}
	if d.LastSeenTS != nil && *d.LastSeenTS != 0 {
		return StatusOnline
	}
	return StatusUnknown
}

// IsOnline reports StatusOf == online.
func (r *Registry) IsOnline(d SavedDevice) bool { return r.StatusOf(d) == StatusOnline }

// OfflineCount counts devices currently offline (Automation tab dot).
func (r *Registry) OfflineCount() int {
	r.mu.Lock()
	devs := append([]SavedDevice(nil), r.devices...)
	r.mu.Unlock()
	n := 0
	for _, d := range devs {
		if r.StatusOf(d) == StatusOffline {
			n++
		}
	}
	return n
}

func (d SavedDevice) String() string { return fmt.Sprintf("%s (%s)", d.Alias, d.Host) }
