package ecoflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SNPrefix prefixes registry serials ("ecoflow:R351…").
const SNPrefix = "ecoflow:"

// SupportedTypes are the accepted device_type values.
var SupportedTypes = []string{"DELTA_3_MAX_PLUS", "DELTA_3", "DELTA_3_1500"}

// DefaultType is used when none/unknown is given.
const DefaultType = "DELTA_3_MAX_PLUS"

// AllRoles are the valid roles, in canonical order.
var AllRoles = []string{"solar", "battery", "output"}

// StaleAfterS is how long telemetry stays fresh for the overlay.
const StaleAfterS = 90.0

// DefaultCapacityWh is the approximate Wh for known models.
var DefaultCapacityWh = map[string]int{
	"DELTA_3_MAX_PLUS": 2048,
	"DELTA_3":          1024,
	"DELTA_3_1500":     1536,
}

// IsEcoflowSN reports whether value carries the "ecoflow:" prefix.
func IsEcoflowSN(value string) bool { return strings.HasPrefix(value, SNPrefix) }

// MakeSN returns the prefixed serial; it errors on an empty serial.
func MakeSN(rawSN string) (string, error) {
	raw := strings.TrimSpace(rawSN)
	if raw == "" {
		return "", errors.New("device serial is required")
	}
	if strings.HasPrefix(raw, SNPrefix) {
		return raw, nil
	}
	return SNPrefix + raw, nil
}

// RawSNFromSN strips the prefix.
func RawSNFromSN(sn string) string {
	s := strings.TrimSpace(sn)
	return strings.TrimPrefix(s, SNPrefix)
}

func supported(t string) bool {
	for _, s := range SupportedTypes {
		if s == t {
			return true
		}
	}
	return false
}

func cleanRoles(roles []string, provided bool) []string {
	if !provided {
		return append([]string(nil), AllRoles...)
	}
	out := []string{}
	seen := map[string]bool{}
	for _, r := range roles {
		key := strings.ToLower(strings.TrimSpace(r))
		valid := false
		for _, a := range AllRoles {
			if a == key {
				valid = true
			}
		}
		if valid && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), AllRoles...)
	}
	return out
}

// Device is one registry row. JSON keys match the Python dict.
type Device struct {
	SN             string   `json:"sn"`
	RawSN          string   `json:"raw_sn"`
	DeviceType     string   `json:"device_type"`
	Alias          string   `json:"alias"`
	CapacityWh     int      `json:"capacity_wh"`
	SiseliDeviceSN *string  `json:"siseli_device_sn"`
	Roles          []string `json:"roles"`
	Added          float64  `json:"added"`
}

func anyString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// NormalizeRow validates a stored dict (or nil,false when unusable).
func NormalizeRow(d map[string]any, now float64) (Device, bool) {
	snIn := anyString(d["sn"])
	if strings.TrimSpace(snIn) == "" {
		snIn = anyString(d["raw_sn"])
	}
	sn, err := MakeSN(snIn)
	if err != nil {
		return Device{}, false
	}
	dtype := strings.ToUpper(strings.TrimSpace(anyString(d["device_type"])))
	if dtype == "" {
		dtype = DefaultType
	}
	if !supported(dtype) {
		dtype = DefaultType
	}
	raw := strings.TrimSpace(anyString(d["raw_sn"]))
	if raw == "" {
		raw = RawSNFromSN(sn)
	}
	alias := strings.TrimSpace(anyString(d["alias"]))
	if alias == "" {
		alias = titleCase(DefaultType)
	}
	cap := 0
	if f, ok := num(d["capacity_wh"]); ok {
		cap = int(f)
	}
	if cap <= 0 {
		cap = DefaultCapacityWh[dtype]
	}
	var siseli *string
	if s := strings.TrimSpace(anyString(d["siseli_device_sn"])); s != "" {
		siseli = &s
	}
	added := now
	if f, ok := num(d["added"]); ok && f != 0 {
		added = f
	}
	var roles []string
	provided := false
	if rs, ok := d["roles"].([]any); ok {
		provided = true
		for _, r := range rs {
			roles = append(roles, anyString(r))
		}
	}
	return Device{
		SN: sn, RawSN: raw, DeviceType: dtype, Alias: alias, CapacityWh: cap,
		SiseliDeviceSN: siseli, Roles: cleanRoles(roles, provided), Added: added,
	}, true
}

// Store persists the registry (PocketBase or a file).
type Store interface {
	Load() ([]Device, error)
	Save([]Device) error
}

// FileStore is a JSON file store matching /data/ecoflow_devices.json.
type FileStore struct{ Path string }

// Load reads and normalises rows; a missing file is empty.
func (f FileStore) Load() ([]Device, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	var out []Device
	for _, raw := range doc.Devices {
		if row, ok := NormalizeRow(raw, now); ok {
			out = append(out, row)
		}
	}
	return out, nil
}

// Save writes atomically.
func (f FileStore) Save(devs []Device) error {
	if devs == nil {
		devs = []Device{}
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

// Registry is the persisted set of EcoFlow power stations.
type Registry struct {
	mu      sync.Mutex
	devices []Device
	store   Store
	Now     func() time.Time
}

// NewRegistry loads from store (nil = memory only); a load error starts empty.
func NewRegistry(store Store) *Registry {
	r := &Registry{store: store, Now: time.Now}
	if store != nil {
		if devs, err := store.Load(); err == nil {
			r.devices = devs
		}
	}
	return r
}

func (r *Registry) save() {
	if r.store != nil {
		_ = r.store.Save(r.devices)
	}
}

func (r *Registry) nowTS() float64 { return float64(r.Now().UnixNano()) / 1e9 }

func cp(d Device) Device {
	d.Roles = append([]string(nil), d.Roles...)
	if d.SiseliDeviceSN != nil {
		s := *d.SiseliDeviceSN
		d.SiseliDeviceSN = &s
	}
	return d
}

// ListDevices returns all rows.
func (r *Registry) ListDevices() []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Device, len(r.devices))
	for i, d := range r.devices {
		out[i] = cp(d)
	}
	return out
}

// ListForSiseli returns rows linked to one Siseli serial.
func (r *Registry) ListForSiseli(siseliSN string) []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Device{}
	for _, d := range r.devices {
		if d.SiseliDeviceSN != nil && *d.SiseliDeviceSN == siseliSN {
			out = append(out, cp(d))
		}
	}
	return out
}

// Get looks up by prefixed or raw serial.
func (r *Registry) Get(sn string) (Device, bool) {
	key, err := MakeSN(sn)
	if err != nil {
		return Device{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.devices {
		if d.SN == key {
			return cp(d), true
		}
	}
	return Device{}, false
}

// UpsertOpts are the optional Upsert arguments.
type UpsertOpts struct {
	DeviceType string
	Alias      string
	CapacityWh *int
	// SiseliDeviceSN nil leaves the link; a pointer to "" clears it.
	SiseliDeviceSN *string
	// Roles nil leaves roles alone (defaults on create).
	Roles []string
}

// Upsert adds or updates a device. It errors on unsupported device types.
func (r *Registry) Upsert(rawSN string, o UpsertOpts) (Device, error) {
	sn, err := MakeSN(rawSN)
	if err != nil {
		return Device{}, err
	}
	dtype := strings.ToUpper(strings.TrimSpace(o.DeviceType))
	if dtype == "" {
		dtype = DefaultType
	}
	if !supported(dtype) {
		return Device{}, fmt.Errorf("unsupported device_type: %s", o.DeviceType)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var ex *Device
	for i := range r.devices {
		if r.devices[i].SN == sn {
			ex = &r.devices[i]
		}
	}
	if ex == nil {
		alias := strings.TrimSpace(o.Alias)
		if alias == "" {
			alias = titleCase(dtype)
		}
		cap := DefaultCapacityWh[dtype]
		if o.CapacityWh != nil {
			cap = *o.CapacityWh
		}
		r.devices = append(r.devices, Device{
			SN: sn, RawSN: RawSNFromSN(sn), DeviceType: dtype, Alias: alias,
			CapacityWh: cap, Roles: append([]string(nil), AllRoles...), Added: r.nowTS(),
		})
		ex = &r.devices[len(r.devices)-1]
	} else {
		ex.DeviceType = dtype
		if a := strings.TrimSpace(o.Alias); a != "" {
			ex.Alias = a
		}
		if o.CapacityWh != nil {
			ex.CapacityWh = *o.CapacityWh
		}
	}
	if o.SiseliDeviceSN != nil {
		if s := strings.TrimSpace(*o.SiseliDeviceSN); s != "" {
			ex.SiseliDeviceSN = &s
		} else {
			ex.SiseliDeviceSN = nil
		}
	}
	if o.Roles != nil {
		ex.Roles = cleanRoles(o.Roles, true)
	}
	r.save()
	return cp(*ex), nil
}

// ErrNotFound is returned by Link for an unknown device.
var ErrNotFound = errors.New("ecoflow device not found")

// Link attaches a device to a Siseli serial (nil/empty unlinks).
func (r *Registry) Link(sn string, siseliSN *string, roles []string) (Device, error) {
	key, err := MakeSN(sn)
	if err != nil {
		return Device{}, ErrNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.devices {
		row := &r.devices[i]
		if row.SN != key {
			continue
		}
		link := ""
		if siseliSN != nil {
			link = strings.TrimSpace(*siseliSN)
		}
		if link != "" {
			row.SiseliDeviceSN = &link
		} else {
			row.SiseliDeviceSN = nil
		}
		if roles != nil {
			row.Roles = cleanRoles(roles, true)
		} else if link != "" && len(row.Roles) == 0 {
			row.Roles = append([]string(nil), AllRoles...)
		}
		r.save()
		return cp(*row), nil
	}
	return Device{}, ErrNotFound
}

// Delete removes a device.
func (r *Registry) Delete(sn string) bool {
	key, err := MakeSN(sn)
	if err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	before := len(r.devices)
	kept := r.devices[:0]
	for _, d := range r.devices {
		if d.SN != key {
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

// FleetMetaRow is one unattached device for the Live fleet strip.
type FleetMetaRow struct {
	DeviceID   string  `json:"device_id"`
	DeviceSN   string  `json:"device_sn"`
	Name       string  `json:"name"`
	ModelName  string  `json:"model_name"`
	ModelCode  *string `json:"model_code"`
	Source     string  `json:"source"`
	DeviceType string  `json:"device_type"`
	CapacityWh int     `json:"capacity_wh"`
}

// FleetMeta lists unattached devices (rows linked to a Siseli are omitted;
// their watts overlay onto the Siseli view instead).
func (r *Registry) FleetMeta() []FleetMetaRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []FleetMetaRow{}
	for _, d := range r.devices {
		if d.SiseliDeviceSN != nil && *d.SiseliDeviceSN != "" {
			continue
		}
		name := d.Alias
		if name == "" {
			name = d.SN
		}
		dt := d.DeviceType
		if dt == "" {
			dt = DefaultType
		}
		out = append(out, FleetMetaRow{
			DeviceID: d.SN, DeviceSN: d.SN, Name: name, ModelName: titleCase(dt),
			Source: "ecoflow", DeviceType: d.DeviceType, CapacityWh: d.CapacityWh,
		})
	}
	return out
}

// ---------------------------------------------------------------- overlay

// Live is one device's live entry (server.state.ecoflow[sn]).
type Live struct {
	Telemetry map[string]any `json:"telemetry"`
	Detail    map[string]any `json:"detail"`
	TS        *float64       `json:"ts"`
}

// LinkedRow is the {sn, alias, roles, capacity_wh, …} row passed to the
// overlay (a registry row, optionally plain).
type LinkedRow struct {
	SN         string
	Alias      string
	DeviceType string
	Roles      []string // nil = all roles
	CapacityWh float64
}

// LinkedFromDevices converts registry rows to overlay rows.
func LinkedFromDevices(ds []Device) []LinkedRow {
	out := make([]LinkedRow, len(ds))
	for i, d := range ds {
		out[i] = LinkedRow{SN: d.SN, Alias: d.Alias, DeviceType: d.DeviceType,
			Roles: d.Roles, CapacityWh: float64(d.CapacityWh)}
	}
	return out
}

func asF(v any) float64 {
	f, _ := num(v)
	return f
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func isFresh(live *Live, now, staleS float64) bool {
	if live == nil || live.TS == nil {
		return false
	}
	if now-*live.TS > staleS {
		return false
	}
	tele := live.Telemetry
	if tele == nil {
		return false
	}
	return tele["battery_percent"] != nil || tele["solar_input_w"] != nil
}

// WeightedSOC is the capacity-weighted SOC from (soc_pct, capacity_wh)
// pairs; ok is false when there is no usable capacity.
func WeightedSOC(items [][2]float64) (float64, bool) {
	total, stored := 0.0, 0.0
	for _, it := range items {
		soc, wh := it[0], it[1]
		if wh <= 0 {
			continue
		}
		total += wh
		stored += soc * wh / 100.0
	}
	if total <= 0 {
		return 0, false
	}
	return math.Max(0, math.Min(100, stored/total*100.0)), true
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func has(roles []string, r string) bool {
	for _, x := range roles {
		if x == r {
			return true
		}
	}
	return false
}

// LinkedRowsForSiseli returns linked devices with live telemetry for the
// expandable Live card.
func LinkedRowsForSiseli(reg *Registry, siseliSN string, liveBySN map[string]Live, now float64, staleS float64) []map[string]any {
	out := []map[string]any{}
	for _, d := range reg.ListForSiseli(siseliSN) {
		var lp *Live
		if l, ok := liveBySN[d.SN]; ok {
			lc := l
			lp = &lc
		}
		fresh := isFresh(lp, now, staleS)
		var tele, detail map[string]any
		var ts any
		if lp != nil {
			tele, detail = lp.Telemetry, lp.Detail
			if lp.TS != nil {
				ts = *lp.TS
			}
		}
		roles := d.Roles
		if len(roles) == 0 {
			roles = AllRoles
		}
		row := map[string]any{
			"sn": d.SN, "alias": d.Alias, "device_type": d.DeviceType,
			"roles": roles, "capacity_wh": d.CapacityWh, "fresh": fresh,
			"telemetry": cloneMap(tele), "detail": cloneMap(detail), "ts": ts,
		}
		out = append(out, row)
	}
	return out
}

// OverlayOpts are the keyword arguments of overlay_telemetry.
type OverlayOpts struct {
	Linked   []LinkedRow
	LiveBySN map[string]Live
	// Now is a unix timestamp; zero means time.Now().
	Now    float64
	StaleS float64
	// BMSAlready mirrors bms_already (accepted for parity; unused).
	BMSAlready bool
	// LoadSources filters which load_inputs contribute to output_power_w.
	// nil = all; non-nil (even empty) = only those ids.
	LoadSources []string
}

// OverlayTelemetry merges linked EcoFlow solar/battery/output into Siseli
// telemetry without mutating tele. Battery role uses capacity-weighted SOC
// with any existing BMS headline SOC.
func OverlayTelemetry(tele map[string]any, o OverlayOpts) map[string]any {
	if tele == nil {
		return nil
	}
	out := cloneMap(tele)
	now := o.Now
	if now == 0 {
		now = float64(time.Now().UnixNano()) / 1e9
	}
	staleS := o.StaleS
	if staleS == 0 {
		staleS = StaleAfterS
	}

	var solarInputs []any
	if si, ok := out["solar_inputs"].([]any); ok {
		solarInputs = append(solarInputs, si...)
	} else if si, ok := out["solar_inputs"].([]map[string]any); ok {
		for _, m := range si {
			solarInputs = append(solarInputs, m)
		}
	}
	extraSolar := 0.0
	var loadInputs []map[string]any
	haveOutputRole := false
	var battWeights [][2]float64
	var linkedPayload []map[string]any
	ecoflowCapacityWh := 0.0

	baseLoad := asF(out["output_power_w"])
	baseCapacityWh := asF(out["capacity_wh"])

	for _, d := range o.Linked {
		roles := d.Roles
		if roles == nil {
			roles = AllRoles
		}
		var lp *Live
		if l, ok := o.LiveBySN[d.SN]; ok {
			lc := l
			lp = &lc
		}
		regWh := d.CapacityWh
		if has(roles, "battery") && regWh > 0 {
			ecoflowCapacityWh += regWh
		}
		var liveTS any
		var liveDetail map[string]any
		if lp != nil {
			liveDetail = lp.Detail
			if lp.TS != nil {
				liveTS = *lp.TS
			}
		}
		if !isFresh(lp, now, staleS) {
			detail := liveDetail
			if detail == nil {
				detail = map[string]any{}
			}
			linkedPayload = append(linkedPayload, map[string]any{
				"sn": d.SN, "alias": d.Alias, "device_type": d.DeviceType,
				"roles": roles, "fresh": false, "telemetry": map[string]any{},
				"detail": detail, "ts": liveTS,
			})
			if has(roles, "output") {
				haveOutputRole = true
			}
			continue
		}
		et := lp.Telemetry
		detail := lp.Detail
		linkedPayload = append(linkedPayload, map[string]any{
			"sn": d.SN, "alias": d.Alias, "device_type": d.DeviceType,
			"roles": roles, "fresh": true, "telemetry": cloneMap(et),
			"detail": cloneMap(detail), "ts": liveTS,
		})
		if has(roles, "solar") {
			inputs := toMapList(et["solar_inputs"])
			for _, inp := range inputs {
				watts := asF(inp["watts"])
				id := anyString(inp["id"])
				if id == "" || inp["id"] == nil {
					id = fmt.Sprintf("%s:%s", d.SN, anyString(inp["label"]))
				}
				label := anyString(inp["label"])
				if label == "" {
					label = "EcoFlow PV"
				}
				solarInputs = append(solarInputs, map[string]any{
					"id": id, "label": label, "source": "ecoflow",
					"device_sn": d.SN, "watts": watts,
				})
				extraSolar += math.Max(0, watts)
			}
			if len(inputs) == 0 {
				if w := asF(et["solar_input_w"]); w > 0 {
					alias := d.Alias
					if alias == "" {
						alias = "EcoFlow"
					}
					solarInputs = append(solarInputs, map[string]any{
						"id": d.SN + ":pv", "label": alias + " solar", "source": "ecoflow",
						"device_sn": d.SN, "watts": w,
					})
					extraSolar += w
				}
			}
		}
		if has(roles, "output") {
			haveOutputRole = true
			alias := d.Alias
			if alias == "" {
				alias = "EcoFlow"
			}
			loadInputs = append(loadInputs, map[string]any{
				"id": d.SN, "label": alias + " load", "source": "ecoflow",
				"device_sn": d.SN,
				"watts":     pyRound(math.Max(0, asF(et["output_power_w"]))),
			})
		}
		if has(roles, "battery") {
			if et["battery_percent"] != nil && regWh > 0 {
				battWeights = append(battWeights, [2]float64{asF(et["battery_percent"]), regWh})
			}
		}
	}

	if len(solarInputs) > 0 {
		out["solar_inputs"] = solarInputs
	}
	if extraSolar != 0 {
		base := asF(out["solar_input_w"])
		out["siseli_solar_w"] = base
		out["additional_solar_w"] = pyRound(extraSolar)
		out["solar_input_w"] = pyRound(base + extraSolar)
		out["input_power_w"] = pyRound(asF(out["solar_input_w"]) + asF(out["ac_input_w"]))
	}

	if haveOutputRole {
		allLoads := []map[string]any{{
			"id": "siseli", "label": "Siseli load", "source": "siseli",
			"watts": pyRound(math.Max(0, baseLoad)),
		}}
		allLoads = append(allLoads, loadInputs...)
		out["load_inputs"] = allLoads
		out["siseli_output_w"] = pyRound(math.Max(0, baseLoad))
		selected := map[string]bool{}
		if o.LoadSources != nil {
			for _, s := range o.LoadSources {
				selected[s] = true
			}
		} else {
			for _, row := range allLoads {
				selected[anyString(row["id"])] = true
			}
		}
		total := 0.0
		for _, row := range allLoads {
			if selected[anyString(row["id"])] {
				total += asF(row["watts"])
			}
		}
		add := 0.0
		for _, row := range loadInputs {
			if selected[anyString(row["id"])] {
				add += asF(row["watts"])
			}
		}
		out["additional_output_w"] = pyRound(add)
		out["output_power_w"] = pyRound(total)
	}

	// Capacity: always Siseli/BMS base + registered EcoFlow battery Wh.
	if ecoflowCapacityWh > 0 {
		out["capacity_wh"] = int(baseCapacityWh + ecoflowCapacityWh)
		out["ecoflow_capacity_wh"] = int(ecoflowCapacityWh)
	}

	if len(battWeights) > 0 {
		weights := append([][2]float64(nil), battWeights...)
		if out["battery_percent"] != nil && baseCapacityWh > 0 {
			weights = append([][2]float64{{asF(out["battery_percent"]), baseCapacityWh}}, weights...)
		}
		if soc, ok := WeightedSOC(weights); ok {
			if out["inverter_soc_pct"] == nil && !truthy(out["bms_source"]) {
				out["inverter_soc_pct"] = out["battery_percent"]
				out["main_soc_pct"] = out["battery_percent"]
			}
			rounded, _ := strconv.ParseFloat(strconv.FormatFloat(soc, 'f', 1, 64), 64)
			out["battery_percent"] = rounded
			out["system_soc_pct"] = rounded
			out["ecoflow_source"] = true
		}
	}

	if len(linkedPayload) > 0 {
		out["linked_ecoflow"] = linkedPayload
		out["ecoflow_linked"] = true
	}
	return out
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return true
}

func toMapList(v any) []map[string]any {
	switch x := v.(type) {
	case []map[string]any:
		return x
	case []SolarInput:
		out := make([]map[string]any, len(x))
		for i, s := range x {
			out[i] = map[string]any{"id": s.ID, "label": s.Label, "source": s.Source, "watts": s.Watts}
		}
		return out
	case []any:
		var out []map[string]any
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}
