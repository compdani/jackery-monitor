package ble

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Timing defaults of bms_ble.py.
const (
	ScanDefault = 8 * time.Second
	PollTimeout = 8 * time.Second
	NotifyWait  = 5 * time.Second
)

var nameHints = []string{
	"jbd", "overkill", "xiaoxiang", "xiao xiang", "smartbms", "smart bms",
	"temgot", "sp04s", "sp10s", "sp16s", "sp20s",
}

func uuidBlob(v string) string {
	return strings.ReplaceAll(strings.ToLower(v), "-", "")
}

// LooksLikeJBD flags likely JBD modules by name, service UUID or service
// data key (0xFF00).
func LooksLikeJBD(name string, serviceUUIDs []string, serviceData map[string][]byte) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n != "" {
		for _, h := range nameHints {
			if strings.Contains(n, h) {
				return true
			}
		}
	}
	for _, u := range serviceUUIDs {
		if strings.Contains(uuidBlob(u), "ff00") {
			return true
		}
	}
	for k := range serviceData {
		if strings.Contains(uuidBlob(k), "ff00") {
			return true
		}
	}
	return false
}

// BleStatus is the structured availability reply (bleak_status).
type BleStatus struct {
	Available bool    `json:"available"`
	Error     *string `json:"error"`
}

// ScanRow is one nearby device from Poller.Scan.
type ScanRow struct {
	MAC          string   `json:"mac"`
	Name         string   `json:"name"`
	RSSI         *int     `json:"rssi"`
	ServiceUUIDs []string `json:"service_uuids"`
	LikelyJBD    bool     `json:"likely_jbd"`
}

// Poller scans and polls JBD BMS packs through an Adapter.
// Sequential connect-query-disconnect: these modules drop notifications if
// two centrals stay connected.
type Poller struct {
	Adapter Adapter
	// Lock defaults to GlobalAdapterLock().
	Lock *AdapterLock
	Log  *slog.Logger
	// Now is overridable in tests.
	Now func() time.Time
}

func (p *Poller) lock() *AdapterLock {
	if p.Lock != nil {
		return p.Lock
	}
	return GlobalAdapterLock()
}

func (p *Poller) logger() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

func (p *Poller) now() float64 {
	t := time.Now
	if p.Now != nil {
		t = p.Now
	}
	return float64(t().UnixNano()) / 1e9
}

// Status reports whether the BLE stack is usable.
func (p *Poller) Status() BleStatus {
	if p.Adapter == nil {
		m := "no BLE adapter configured"
		return BleStatus{Error: &m}
	}
	if err := p.Adapter.Available(); err != nil {
		m := err.Error()
		return BleStatus{Error: &m}
	}
	return BleStatus{Available: true}
}

// Scan returns nearby BLE advertisements, likely JBD modules first. JBD UART
// modules often omit a name and 0xFF00 from the advertisement, so every
// nearby device is listed and likely matches are flagged.
func (p *Poller) Scan(ctx context.Context, timeout time.Duration) ([]ScanRow, error) {
	if st := p.Status(); !st.Available {
		return nil, &ErrUnavailable{Msg: *st.Error}
	}
	if timeout <= 0 {
		timeout = ScanDefault
	}
	lock := p.lock()
	if !lock.Acquire(ctx, 2*time.Second) {
		return nil, &ErrUnavailable{Msg: "BLE adapter busy (another BMS operation is running)"}
	}
	defer lock.Release()

	scanCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var mu sync.Mutex
	seen := map[string]bool{}
	var out []ScanRow
	err := p.Adapter.Scan(scanCtx, func(a Advertisement) {
		addr := strings.ToUpper(a.Address)
		mu.Lock()
		defer mu.Unlock()
		if addr == "" || seen[addr] {
			return
		}
		seen[addr] = true
		name := a.LocalName
		if name == "" {
			name = a.Name
		}
		display := name
		if display == "" {
			display = addr
		}
		rssi := a.RSSI
		uuids := a.ServiceUUIDs
		if uuids == nil {
			uuids = []string{}
		}
		out = append(out, ScanRow{
			MAC: addr, Name: display, RSSI: &rssi, ServiceUUIDs: uuids,
			LikelyJBD: LooksLikeJBD(name, a.ServiceUUIDs, a.ServiceData),
		})
	})
	if err != nil {
		return nil, &ErrUnavailable{Msg: fmt.Sprintf("BLE scan failed: %v", err)}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	mu.Lock()
	defer mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := 1, 1
		if out[i].LikelyJBD {
			li = 0
		}
		if out[j].LikelyJBD {
			lj = 0
		}
		if li != lj {
			return li < lj
		}
		ri, rj := -999, -999
		if out[i].RSSI != nil {
			ri = *out[i].RSSI
		}
		if out[j].RSSI != nil {
			rj = *out[j].RSSI
		}
		return -ri < -rj
	})
	if out == nil {
		out = []ScanRow{}
	}
	return out, nil
}

// FindCharacteristic locates a characteristic by UUID across all services.
func FindCharacteristic(c Conn, uuid string) (Characteristic, bool) {
	want := uuidBlob(uuid)
	for _, s := range c.Services() {
		for _, ch := range s.Characteristics {
			if uuidBlob(ch.UUID) == want {
				return ch, true
			}
		}
	}
	return Characteristic{}, false
}

// PollOne connects, reads 0x03 then 0x04, and disconnects.
func (p *Poller) PollOne(ctx context.Context, address string, timeout time.Duration) (Reading, error) {
	if st := p.Status(); !st.Available {
		return Reading{}, &ErrUnavailable{Msg: *st.Error}
	}
	if timeout <= 0 {
		timeout = PollTimeout
	}
	connCtx, cancel := context.WithTimeout(ctx, timeout)
	conn, err := p.Adapter.Connect(connCtx, address)
	cancel()
	if err != nil {
		return Reading{}, err
	}
	defer func() {
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer dcancel()
		_ = conn.Disconnect(dctx)
	}()

	notifyCh, ok := FindCharacteristic(conn, JBDNotifyUUID)
	if !ok {
		return Reading{}, fmt.Errorf("JBD notify characteristic %s not found", JBDNotifyUUID)
	}
	writeCh, ok := FindCharacteristic(conn, JBDWriteUUID)
	if !ok {
		return Reading{}, fmt.Errorf("JBD write characteristic %s not found", JBDWriteUUID)
	}

	var mu sync.Mutex
	var buf []byte
	var frames [][]byte
	got := make(chan struct{}, 1)
	onNotify := func(_ Characteristic, data []byte) {
		mu.Lock()
		buf = append(buf, data...)
		extracted, rest := ExtractFrames(buf)
		buf = rest
		if len(extracted) > 0 {
			frames = append(frames, extracted...)
			select {
			case got <- struct{}{}:
			default:
			}
		}
		mu.Unlock()
	}
	takeCmd := func(cmd byte) (Frame, bool) {
		mu.Lock()
		defer mu.Unlock()
		for i, raw := range frames {
			f, err := ParseFrame(raw)
			if err != nil {
				continue
			}
			if f.Cmd == cmd {
				frames = append(frames[:i], frames[i+1:]...)
				return f, true
			}
		}
		return Frame{}, false
	}

	nctx, ncancel := context.WithTimeout(ctx, timeout)
	err = conn.StartNotify(nctx, notifyCh, onNotify)
	ncancel()
	if err != nil {
		return Reading{}, err
	}
	defer func() {
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer scancel()
		_ = conn.StopNotify(sctx, notifyCh)
	}()

	var basic *BasicInfo
	var cells []int
	for _, cmd := range []byte{CmdBasic, CmdCells} {
		parsed, have := takeCmd(cmd)
		if !have {
			select {
			case <-got:
			default:
			}
			wctx, wcancel := context.WithTimeout(ctx, timeout)
			err := conn.Write(wctx, writeCh, BuildRead(cmd), false)
			wcancel()
			if err != nil {
				return Reading{}, err
			}
			wait := NotifyWait
			if timeout < wait {
				wait = timeout
			}
			deadline := time.NewTimer(wait)
			for !have {
				select {
				case <-got:
					parsed, have = takeCmd(cmd)
				case <-deadline.C:
					return Reading{}, fmt.Errorf("timeout waiting for 0x%02x", cmd)
				case <-ctx.Done():
					deadline.Stop()
					return Reading{}, ctx.Err()
				}
			}
			deadline.Stop()
		}
		if cmd == CmdBasic {
			bi, err := ParseBasicInfo(parsed.Payload)
			if err != nil {
				return Reading{}, err
			}
			basic = &bi
		} else {
			cells, err = ParseCellVoltages(parsed.Payload)
			if err != nil {
				return Reading{}, err
			}
		}
	}
	if basic == nil {
		return Reading{}, jbdErr("no basic-info response")
	}
	reading := MergeReading(*basic, cells)
	ts := p.now()
	reading.TS = &ts
	reading.Error = nil
	return reading, nil
}

// PollAll polls sequentially. Failed MACs land as {error, ts} readings so
// the UI can show them.
func (p *Poller) PollAll(ctx context.Context, addresses []string, timeout time.Duration) map[string]Reading {
	lock := p.lock()
	lock.Acquire(ctx, 0)
	defer lock.Release()
	out := make(map[string]Reading, len(addresses))
	for _, addr := range addresses {
		r, err := p.PollOne(ctx, addr, timeout)
		if err != nil {
			p.logger().Warn("BMS poll failed", "addr", addr, "err", err)
			msg := err.Error()
			if len(msg) > 240 {
				msg = msg[:240]
			}
			ts := p.now()
			out[addr] = Reading{TS: &ts, Error: &msg}
			continue
		}
		out[addr] = r
	}
	return out
}
