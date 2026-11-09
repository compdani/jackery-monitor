package ble

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// StaleAfterS is how long a BMS reading stays fresh.
const StaleAfterS = 90.0

var macNonHex = regexp.MustCompile(`[^0-9A-Fa-f]`)

// NormalizeMAC returns AA:BB:CC:DD:EE:FF or an error unless it is 6 bytes.
func NormalizeMAC(mac string) (string, error) {
	hexOnly := strings.ToUpper(macNonHex.ReplaceAllString(mac, ""))
	if len(hexOnly) != 12 {
		return "", errors.New("MAC must be 6 bytes (AA:BB:CC:DD:EE:FF)")
	}
	parts := make([]string, 6)
	for i := 0; i < 6; i++ {
		parts[i] = hexOnly[i*2 : i*2+2]
	}
	return strings.Join(parts, ":"), nil
}

// SocItem is a (soc_pct, capacity_wh) pair.
type SocItem struct{ SocPct, CapacityWh float64 }

// WeightedSOC is the capacity-weighted SOC (pack-only). ok is false when no
// pack has capacity.
func WeightedSOC(items []SocItem) (float64, bool) {
	total, stored := 0.0, 0.0
	for _, it := range items {
		if it.CapacityWh <= 0 {
			continue
		}
		total += it.CapacityWh
		stored += it.SocPct * it.CapacityWh / 100.0
	}
	if total <= 0 {
		return 0, false
	}
	return math.Max(0, math.Min(100, stored/total*100.0)), true
}

// InferCapacityWh is full_ah * pack_voltage from a 0x03 reading, if both
// look real.
func InferCapacityWh(r *Reading) (int, bool) {
	if r == nil || r.FullAh == nil || r.VoltageV == nil {
		return 0, false
	}
	fa, v := *r.FullAh, *r.VoltageV
	if fa <= 0 || v <= 0 {
		return 0, false
	}
	wh := int(math.RoundToEven(fa * v))
	if wh < 1 {
		wh = 1
	}
	return wh, true
}

// Pack is one registered BMS pack (bms_devices.json "packs" entry).
type Pack struct {
	MAC            string  `json:"mac"`
	Alias          string  `json:"alias"`
	CapacityWh     int     `json:"capacity_wh"`
	SiseliDeviceSN *string `json:"siseli_device_sn"`
	Added          float64 `json:"added"`
}

// InverterRow holds per-inverter BMS settings.
type InverterRow struct {
	UseAsMain bool `json:"use_as_main"`
}

// BmsData is the persisted document: {"packs": [...], "inverters": {...}}.
type BmsData struct {
	Packs     []Pack                 `json:"packs"`
	Inverters map[string]InverterRow `json:"inverters"`
}

// BmsStore persists the registry.
type BmsStore interface {
	Load() (BmsData, error)
	Save(BmsData) error
}

// BmsFileStore is a JSON file store matching /data/bms_devices.json.
type BmsFileStore struct{ Path string }

// Load reads the file; a missing file is empty.
func (f BmsFileStore) Load() (BmsData, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return BmsData{}, nil
	}
	if err != nil {
		return BmsData{}, err
	}
	var d BmsData
	if err := json.Unmarshal(data, &d); err != nil {
		return BmsData{}, err
	}
	return d, nil
}

// Save writes atomically.
func (f BmsFileStore) Save(d BmsData) error {
	if d.Packs == nil {
		d.Packs = []Pack{}
	}
	if d.Inverters == nil {
		d.Inverters = map[string]InverterRow{}
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// BmsRegistry is the persisted registry of BMS packs assigned to Siseli
// inverters.
type BmsRegistry struct {
	mu        sync.Mutex
	packs     []Pack
	inverters map[string]InverterRow
	store     BmsStore
	Now       func() time.Time
}

// NewBmsRegistry loads from store (nil = memory only).
func NewBmsRegistry(store BmsStore) *BmsRegistry {
	r := &BmsRegistry{store: store, inverters: map[string]InverterRow{}, Now: time.Now}
	if store != nil {
		if d, err := store.Load(); err == nil {
			r.packs = d.Packs
			if d.Inverters != nil {
				r.inverters = d.Inverters
			}
		}
	}
	return r
}

func (r *BmsRegistry) save() {
	if r.store != nil {
		_ = r.store.Save(BmsData{Packs: r.packs, Inverters: r.inverters})
	}
}

// ListPacks returns every pack.
func (r *BmsRegistry) ListPacks() []Pack {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Pack(nil), r.packs...)
}

// ListPacksFor returns packs assigned to one Siseli serial.
func (r *BmsRegistry) ListPacksFor(siseliSN string) []Pack {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Pack{}
	for _, p := range r.packs {
		if p.SiseliDeviceSN != nil && *p.SiseliDeviceSN == siseliSN {
			out = append(out, p)
		}
	}
	return out
}

func (r *BmsRegistry) find(mac string) *Pack {
	for i := range r.packs {
		if r.packs[i].MAC == mac {
			return &r.packs[i]
		}
	}
	return nil
}

// Get returns a pack by (any format of) MAC.
func (r *BmsRegistry) Get(mac string) (Pack, bool) {
	n, err := NormalizeMAC(mac)
	if err != nil {
		return Pack{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.find(n); p != nil {
		return *p, true
	}
	return Pack{}, false
}

// BmsUpsertOpts are Upsert's optional arguments.
type BmsUpsertOpts struct {
	Alias      string
	CapacityWh *int
	// SiseliDeviceSN nil leaves the assignment; "" clears it.
	SiseliDeviceSN *string
}

func (r *BmsRegistry) ensureInverter(sn string) InverterRow {
	row, ok := r.inverters[sn]
	if !ok {
		row = InverterRow{UseAsMain: true}
		r.inverters[sn] = row
	}
	return row
}

// Upsert adds or updates a pack.
func (r *BmsRegistry) Upsert(mac string, o BmsUpsertOpts) (Pack, error) {
	n, err := NormalizeMAC(mac)
	if err != nil {
		return Pack{}, err
	}
	alias := strings.TrimSpace(o.Alias)
	if alias == "" {
		alias = n
	}
	var sn *string
	if o.SiseliDeviceSN != nil {
		if s := strings.TrimSpace(*o.SiseliDeviceSN); s != "" {
			sn = &s
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ex := r.find(n); ex != nil {
		ex.Alias = alias
		if o.CapacityWh != nil {
			ex.CapacityWh = *o.CapacityWh
		}
		if o.SiseliDeviceSN != nil {
			ex.SiseliDeviceSN = sn
			if sn != nil {
				r.ensureInverter(*sn)
			}
		}
		r.save()
		return *ex, nil
	}
	p := Pack{MAC: n, Alias: alias, SiseliDeviceSN: sn, Added: float64(r.Now().UnixNano()) / 1e9}
	if o.CapacityWh != nil {
		p.CapacityWh = *o.CapacityWh
	}
	r.packs = append(r.packs, p)
	if sn != nil {
		r.ensureInverter(*sn)
	}
	r.save()
	return p, nil
}

// SetCapacityWh updates a pack's capacity.
func (r *BmsRegistry) SetCapacityWh(mac string, wh int) (Pack, bool) {
	n, err := NormalizeMAC(mac)
	if err != nil {
		return Pack{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.find(n)
	if p == nil {
		return Pack{}, false
	}
	p.CapacityWh = wh
	r.save()
	return *p, true
}

// Delete removes a pack.
func (r *BmsRegistry) Delete(mac string) bool {
	n, err := NormalizeMAC(mac)
	if err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	before := len(r.packs)
	kept := r.packs[:0]
	for _, p := range r.packs {
		if p.MAC != n {
			kept = append(kept, p)
		}
	}
	r.packs = kept
	changed := len(r.packs) != before
	if changed {
		r.save()
	}
	return changed
}

// UseAsMain reports whether the BMS SOC is the headline for this inverter.
func (r *BmsRegistry) UseAsMain(sn string) bool {
	if sn == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.inverters[sn]
	if !ok {
		return false
	}
	return row.UseAsMain
}

// SetUseAsMain sets the per-inverter flag.
func (r *BmsRegistry) SetUseAsMain(sn string, enabled bool) (InverterRow, error) {
	sn = strings.TrimSpace(sn)
	if sn == "" {
		return InverterRow{}, errors.New("siseli device sn is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureInverter(sn)
	row := InverterRow{UseAsMain: enabled}
	r.inverters[sn] = row
	r.save()
	return row, nil
}

// ------------------------------------------------------------------ overlay

func fresh(r *Reading, now, staleS float64) bool {
	if r == nil {
		return false
	}
	if r.Error != nil && r.SocPct == nil {
		return false
	}
	if r.TS == nil {
		return false
	}
	if now-*r.TS > staleS {
		return false
	}
	return r.SocPct != nil
}

func anyF(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

func cloneTele(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func stripInverterSOC(tele map[string]any) map[string]any {
	out := cloneTele(tele)
	out["ignore_inverter_soc"] = true
	delete(out, "inverter_soc_pct")
	delete(out, "main_soc_pct")
	if b, _ := out["bms_source"].(bool); !b {
		out["battery_percent"] = nil
		out["system_soc_pct"] = nil
	}
	return out
}

// ApplyGridEstimate sets dashboard-only grid watts (never sent to Siseli).
// Off leaves grid at 0. On, grid is the shortfall after solar and battery
// discharge ("battery_discharge_w"). It mutates and returns tele.
func ApplyGridEstimate(tele map[string]any, enabled bool) map[string]any {
	solar := math.Max(0, anyF(tele["solar_input_w"]))
	if !enabled {
		tele["ac_input_w"] = 0
		tele["input_power_w"] = int(math.RoundToEven(solar))
		return tele
	}
	load := math.Max(0, anyF(tele["output_power_w"]))
	batteryOut := math.Max(0, anyF(tele["battery_discharge_w"]))
	grid := 0
	if batteryOut+solar < load {
		grid = int(math.RoundToEven(load - solar - batteryOut))
	}
	tele["ac_input_w"] = grid
	tele["input_power_w"] = int(math.RoundToEven(solar + float64(grid)))
	return tele
}

// OverlayOpts are the keyword arguments of overlay_telemetry.
type OverlayOpts struct {
	Packs     []Pack
	Live      map[string]Reading
	UseAsMain bool
	// Now is a unix timestamp; zero means time.Now().
	Now    float64
	StaleS float64
	// CapacityOverrideWh overrides the summed pack capacity when > 0.
	CapacityOverrideWh int
	IgnoreInverterSOC  bool
	CalcGrid           bool
}

// OverlayTelemetry copies Siseli telemetry and, when enabled, replaces the
// headline SOC with the capacity-weighted BMS SOC. The portal
// battery_percent is preserved as inverter_soc_pct / main_soc_pct unless
// IgnoreInverterSOC. It does not mutate tele.
func OverlayTelemetry(tele map[string]any, o OverlayOpts) map[string]any {
	if tele == nil {
		return nil
	}
	out := cloneTele(tele)
	finish := func(row map[string]any) map[string]any { return ApplyGridEstimate(row, o.CalcGrid) }
	strip := func(row map[string]any) map[string]any {
		if o.IgnoreInverterSOC {
			return stripInverterSOC(row)
		}
		return row
	}

	if len(o.Packs) == 0 {
		return finish(strip(out))
	}
	if !o.UseAsMain && !o.IgnoreInverterSOC {
		return finish(out)
	}
	now := o.Now
	if now == 0 {
		now = float64(time.Now().UnixNano()) / 1e9
	}
	staleS := o.StaleS
	if staleS == 0 {
		staleS = StaleAfterS
	}

	var weights []SocItem
	currents, powers := 0.0, 0.0
	var voltage *float64
	var hottest *float64
	totalWh := 0
	for _, p := range o.Packs {
		var reading *Reading
		if rd, ok := o.Live[p.MAC]; ok && p.MAC != "" {
			rc := rd
			reading = &rc
		}
		wh := p.CapacityWh
		if wh <= 0 {
			if inferred, ok := InferCapacityWh(reading); ok {
				wh = inferred
			} else {
				wh = 0
			}
		}
		if wh > 0 {
			totalWh += wh
		}
		if !fresh(reading, now, staleS) {
			continue
		}
		if reading.SocPct == nil || wh <= 0 {
			continue
		}
		weights = append(weights, SocItem{SocPct: *reading.SocPct, CapacityWh: float64(wh)})
		if reading.CurrentA != nil {
			currents += *reading.CurrentA
		}
		if reading.PowerW != nil {
			powers += *reading.PowerW
		}
		if voltage == nil && reading.VoltageV != nil {
			v := *reading.VoltageV
			voltage = &v
		}
		if reading.TempC != nil && (hottest == nil || *reading.TempC > *hottest) {
			t := *reading.TempC
			hottest = &t
		}
	}
	soc, ok := WeightedSOC(weights)
	if !ok {
		return finish(strip(out))
	}
	out["inverter_soc_pct"] = out["battery_percent"]
	out["main_soc_pct"] = out["battery_percent"]
	rounded := roundN(soc, 1)
	out["battery_percent"] = rounded
	out["system_soc_pct"] = rounded
	out["bms_source"] = true
	capWh := totalWh
	if o.CapacityOverrideWh > 0 {
		capWh = o.CapacityOverrideWh
	}
	if capWh != 0 {
		out["capacity_wh"] = capWh
	}
	if voltage != nil {
		out["battery_voltage_v"] = roundN(*voltage, 3)
	}
	out["battery_charge_a"] = math.Max(0, currents)
	out["battery_discharge_a"] = math.Max(0, -currents)
	out["battery_power_w"] = roundN(powers, 1)
	// Live load is the larger of the inverter's output and the packs'
	// discharge. Siseli often has no load register; a discharging pack still
	// knows the watts leaving the battery. Charging stays out of this figure.
	siseliLoad := anyF(out["output_power_w"])
	bmsDischarge := math.Max(0, -powers)
	out["output_power_w"] = int(math.RoundToEven(math.Max(siseliLoad, bmsDischarge)))
	out["battery_discharge_w"] = int(math.RoundToEven(bmsDischarge))
	if hottest != nil {
		out["battery_temp_c"] = *hottest
	}
	switch {
	case currents > 0.05:
		out["battery_status"] = 1
	case currents < -0.05:
		out["battery_status"] = 2
	default:
		out["battery_status"] = 0
	}
	return finish(strip(out))
}

// UIPackRows are Jackery-shaped pack rows so the existing Live packs card
// can render them.
func UIPackRows(packs []Pack, live map[string]Reading, now, staleS float64) []map[string]any {
	if now == 0 {
		now = float64(time.Now().UnixNano()) / 1e9
	}
	if staleS == 0 {
		staleS = StaleAfterS
	}
	rows := []map[string]any{}
	for i, p := range packs {
		var reading *Reading
		if rd, ok := live[p.MAC]; ok {
			rc := rd
			reading = &rc
		}
		isFresh := fresh(reading, now, staleS)
		var soc, temp, volt, cur, minC, maxC, ts, errV, cells, cycles any
		watts := 0.0
		if reading != nil {
			if reading.SocPct != nil {
				soc = *reading.SocPct
			}
			if reading.PowerW != nil {
				watts = *reading.PowerW
			}
			if reading.TempC != nil {
				temp = *reading.TempC
			}
			if reading.VoltageV != nil {
				volt = *reading.VoltageV
			}
			if reading.CurrentA != nil {
				cur = *reading.CurrentA
			}
			if reading.MinCellMv != nil {
				minC = *reading.MinCellMv
			}
			if reading.MaxCellMv != nil {
				maxC = *reading.MaxCellMv
			}
			if reading.CellCount != nil {
				cells = *reading.CellCount
			}
			if reading.Cycles != nil {
				cycles = *reading.Cycles
			}
			if reading.TS != nil {
				ts = *reading.TS
			}
			if reading.Error != nil && *reading.Error != "" {
				errV = *reading.Error
			}
		}
		ipw, opw := 0, 0
		if watts > 0 {
			ipw = int(math.Max(0, math.RoundToEven(watts)))
		}
		if watts < 0 {
			opw = int(math.Max(0, math.RoundToEven(-watts)))
		}
		if !isFresh && errV == nil {
			if ts != nil {
				errV = "stale"
			} else {
				errV = "no reading"
			}
		}
		alias := p.Alias
		if alias == "" {
			alias = p.MAC
		}
		rows = append(rows, map[string]any{
			"deviceSn": p.MAC, "deviceOrder": i, "rb": soc, "ip": ipw, "op": opw, "it": temp,
			"source": "bms", "alias": alias, "capacity_wh": p.CapacityWh,
			"voltage_v": volt, "current_a": cur, "min_cell_mv": minC, "max_cell_mv": maxC,
			"cell_count": cells, "cycles": cycles, "error": errV, "ts": ts,
		})
	}
	return rows
}

// LatestLiveTS is the newest reading timestamp across the packs.
func LatestLiveTS(packs []Pack, live map[string]Reading) (float64, bool) {
	var latest float64
	have := false
	for _, p := range packs {
		r, ok := live[p.MAC]
		if !ok || r.TS == nil {
			continue
		}
		if !have || *r.TS > latest {
			latest, have = *r.TS, true
		}
	}
	return latest, have
}

func (p Pack) String() string { return fmt.Sprintf("%s (%s)", p.Alias, p.MAC) }
