package ble

import (
	"errors"
	"fmt"
	"strconv"
)

// JBD / Xiaoxiang / Overkill BMS frame codec. BLE service 0xFF00, notify
// 0xFF01, write 0xFF02. No I/O here — bms.go talks to the adapter.
const (
	JBDStart byte = 0xDD
	JBDEnd   byte = 0x77
	JBDRead  byte = 0xA5
	JBDWrite byte = 0x5A

	CmdBasic byte = 0x03
	CmdCells byte = 0x04

	// 16-bit UUIDs advertised by the JBD UART module.
	JBDServiceUUID = "0000ff00-0000-1000-8000-00805f9b34fb"
	JBDNotifyUUID  = "0000ff01-0000-1000-8000-00805f9b34fb"
	JBDWriteUUID   = "0000ff02-0000-1000-8000-00805f9b34fb"
)

// ProtectionBits are the protection flag names, LSB first.
var ProtectionBits = [...]string{
	"cell_overvoltage", "cell_undervoltage", "pack_overvoltage", "pack_undervoltage",
	"charge_overtemp", "charge_undertemp", "discharge_overtemp", "discharge_undertemp",
	"charge_overcurrent", "discharge_overcurrent", "short_circuit", "front_ic_error",
	"software_lock",
}

// JBDError is a malformed or checksum-failed frame.
type JBDError struct{ Msg string }

func (e *JBDError) Error() string { return e.Msg }

func jbdErr(format string, a ...any) error { return &JBDError{Msg: fmt.Sprintf(format, a...)} }

// IsJBDError reports whether err is a *JBDError.
func IsJBDError(err error) bool {
	var e *JBDError
	return errors.As(err, &e)
}

// Checksum is the two-byte checksum: 0x10000 minus (status + len + payload).
func Checksum(status, length int, payload []byte) uint16 {
	total := status + length
	for _, b := range payload {
		total += int(b)
	}
	return uint16((0x10000 - total) & 0xFFFF)
}

// BuildRead builds a read request: DD A5 CMD 00 CKH CKL 77.
func BuildRead(cmd byte) []byte {
	ck := Checksum(int(cmd), 0, nil)
	return []byte{JBDStart, JBDRead, cmd, 0, byte(ck >> 8), byte(ck), JBDEnd}
}

// BuildResponse builds a valid response frame (tests and mock poller).
func BuildResponse(cmd byte, payload []byte, status byte) []byte {
	ck := Checksum(int(status), len(payload), payload)
	out := []byte{JBDStart, cmd, status, byte(len(payload))}
	out = append(out, payload...)
	return append(out, byte(ck>>8), byte(ck), JBDEnd)
}

// ExtractFrames pulls complete DD…77 frames out of a notify buffer. BLE
// notifications often fragment a response; the leftover bytes are returned.
func ExtractFrames(buf []byte) (frames [][]byte, rest []byte) {
	i, n := 0, len(buf)
	for i < n {
		start := -1
		for j := i; j < n; j++ {
			if buf[j] == JBDStart {
				start = j
				break
			}
		}
		if start < 0 {
			return frames, nil
		}
		if start+4 > n {
			return frames, append([]byte(nil), buf[start:]...)
		}
		total := 4 + int(buf[start+3]) + 3 // header + payload + ckh + ckl + end
		if start+total > n {
			return frames, append([]byte(nil), buf[start:]...)
		}
		if buf[start+total-1] != JBDEnd {
			i = start + 1
			continue
		}
		frames = append(frames, append([]byte(nil), buf[start:start+total]...))
		i = start + total
	}
	if i < n {
		rest = append([]byte(nil), buf[i:]...)
	}
	return frames, rest
}

// Frame is a decoded response.
type Frame struct {
	Cmd     byte
	Status  byte
	Payload []byte
}

// ParseFrame decodes one complete response, failing with *JBDError on
// garbage, bad checksum or a non-zero BMS status.
func ParseFrame(raw []byte) (Frame, error) {
	if len(raw) < 7 || raw[0] != JBDStart || raw[len(raw)-1] != JBDEnd {
		return Frame{}, jbdErr("not a JBD frame")
	}
	cmd, status, length := raw[1], raw[2], int(raw[3])
	if len(raw) != 4+length+3 {
		return Frame{}, jbdErr("length mismatch: header %d frame %d", length, len(raw))
	}
	payload := raw[4 : 4+length]
	ck := uint16(raw[len(raw)-3])<<8 | uint16(raw[len(raw)-2])
	if exp := Checksum(int(status), length, payload); ck != exp {
		return Frame{}, jbdErr("checksum 0x%04x != 0x%04x", ck, exp)
	}
	if status != 0 {
		return Frame{}, jbdErr("BMS status 0x%02x", status)
	}
	return Frame{Cmd: cmd, Status: status, Payload: append([]byte(nil), payload...)}, nil
}

func u16(p []byte, off int) int { return int(p[off])<<8 | int(p[off+1]) }
func s16(p []byte, off int) int {
	v := u16(p, off)
	if v >= 0x8000 {
		return v - 0x10000
	}
	return v
}

// Protection are the decoded protection flags (JSON keys match Python).
type Protection struct {
	CellOvervoltage      bool     `json:"cell_overvoltage"`
	CellUndervoltage     bool     `json:"cell_undervoltage"`
	PackOvervoltage      bool     `json:"pack_overvoltage"`
	PackUndervoltage     bool     `json:"pack_undervoltage"`
	ChargeOvertemp       bool     `json:"charge_overtemp"`
	ChargeUndertemp      bool     `json:"charge_undertemp"`
	DischargeOvertemp    bool     `json:"discharge_overtemp"`
	DischargeUndertemp   bool     `json:"discharge_undertemp"`
	ChargeOvercurrent    bool     `json:"charge_overcurrent"`
	DischargeOvercurrent bool     `json:"discharge_overcurrent"`
	ShortCircuit         bool     `json:"short_circuit"`
	FrontICError         bool     `json:"front_ic_error"`
	SoftwareLock         bool     `json:"software_lock"`
	Raw                  int      `json:"raw"`
	Active               []string `json:"active"`
}

// ParseProtection decodes the 16-bit protection word.
func ParseProtection(bits int) Protection {
	flag := func(i int) bool { return bits&(1<<i) != 0 }
	p := Protection{
		CellOvervoltage: flag(0), CellUndervoltage: flag(1), PackOvervoltage: flag(2),
		PackUndervoltage: flag(3), ChargeOvertemp: flag(4), ChargeUndertemp: flag(5),
		DischargeOvertemp: flag(6), DischargeUndertemp: flag(7), ChargeOvercurrent: flag(8),
		DischargeOvercurrent: flag(9), ShortCircuit: flag(10), FrontICError: flag(11),
		SoftwareLock: flag(12), Raw: bits, Active: []string{},
	}
	for i, name := range ProtectionBits {
		if flag(i) {
			p.Active = append(p.Active, name)
		}
	}
	return p
}

// BasicInfo is the 0x03 reply (JSON keys match the Python dict).
type BasicInfo struct {
	VoltageV     float64    `json:"voltage_v"`
	CurrentA     float64    `json:"current_a"`
	PowerW       float64    `json:"power_w"`
	RemainAh     float64    `json:"remain_ah"`
	FullAh       float64    `json:"full_ah"`
	Cycles       int        `json:"cycles"`
	BalanceLow   int        `json:"balance_low"`
	BalanceHigh  int        `json:"balance_high"`
	Protection   Protection `json:"protection"`
	Version      int        `json:"version"`
	SocPct       float64    `json:"soc_pct"`
	ChargeFET    bool       `json:"charge_fet"`
	DischargeFET bool       `json:"discharge_fet"`
	CellCount    int        `json:"cell_count"`
	NTCCount     int        `json:"ntc_count"`
	TempsC       []float64  `json:"temps_c"`
	TempC        *float64   `json:"temp_c"`
}

// roundN is Python's round(x, n) (correctly rounded, half-to-even on the
// exact binary value).
func roundN(f float64, n int) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'f', n, 64), 64)
	return v
}

// ParseBasicInfo decodes a 0x03 payload (≥ 23 bytes before the NTC array).
func ParseBasicInfo(payload []byte) (BasicInfo, error) {
	if len(payload) < 23 {
		return BasicInfo{}, jbdErr("basic-info payload too short (%d)", len(payload))
	}
	ntcCount := int(payload[22])
	ntcEnd := 23 + ntcCount*2
	temps := []float64{}
	if len(payload) >= ntcEnd {
		for i := 0; i < ntcCount; i++ {
			kx10 := u16(payload, 23+i*2)
			temps = append(temps, roundN(float64(kx10)/10.0-273.1, 1))
		}
	}
	fet := payload[20]
	voltage := float64(u16(payload, 0)) / 100.0
	current := float64(s16(payload, 2)) / 100.0
	var tempC *float64
	if len(temps) > 0 {
		m := temps[0]
		for _, t := range temps {
			if t > m {
				m = t
			}
		}
		tempC = &m
	}
	return BasicInfo{
		VoltageV:     roundN(voltage, 3),
		CurrentA:     roundN(current, 3),
		PowerW:       roundN(voltage*current, 1),
		RemainAh:     roundN(float64(u16(payload, 4))/100.0, 3),
		FullAh:       roundN(float64(u16(payload, 6))/100.0, 3),
		Cycles:       u16(payload, 8),
		BalanceLow:   u16(payload, 12),
		BalanceHigh:  u16(payload, 14),
		Protection:   ParseProtection(u16(payload, 16)),
		Version:      int(payload[18]),
		SocPct:       float64(payload[19]),
		ChargeFET:    fet&0x01 != 0,
		DischargeFET: fet&0x02 != 0,
		CellCount:    int(payload[21]),
		NTCCount:     ntcCount,
		TempsC:       temps,
		TempC:        tempC,
	}, nil
}

// ParseCellVoltages decodes a 0x04 payload: uint16 mV per cell, big-endian.
func ParseCellVoltages(payload []byte) ([]int, error) {
	if len(payload)%2 != 0 {
		return nil, jbdErr("cell-voltage payload odd length")
	}
	out := make([]int, 0, len(payload)/2)
	for i := 0; i < len(payload); i += 2 {
		out = append(out, u16(payload, i))
	}
	return out, nil
}

// Reading is the poll result stored in state.bms_live[mac]. JSON keys match
// the Python dict; error readings carry soc_pct=null.
type Reading struct {
	VoltageV     *float64    `json:"voltage_v,omitempty"`
	CurrentA     *float64    `json:"current_a,omitempty"`
	PowerW       *float64    `json:"power_w,omitempty"`
	RemainAh     *float64    `json:"remain_ah,omitempty"`
	FullAh       *float64    `json:"full_ah,omitempty"`
	Cycles       *int        `json:"cycles,omitempty"`
	BalanceLow   *int        `json:"balance_low,omitempty"`
	BalanceHigh  *int        `json:"balance_high,omitempty"`
	Protection   *Protection `json:"protection,omitempty"`
	Version      *int        `json:"version,omitempty"`
	SocPct       *float64    `json:"soc_pct"`
	ChargeFET    *bool       `json:"charge_fet,omitempty"`
	DischargeFET *bool       `json:"discharge_fet,omitempty"`
	CellCount    *int        `json:"cell_count,omitempty"`
	NTCCount     *int        `json:"ntc_count,omitempty"`
	TempsC       []float64   `json:"temps_c,omitempty"`
	TempC        *float64    `json:"temp_c,omitempty"`
	CellMv       []int       `json:"cell_mv,omitempty"`
	MinCellMv    *int        `json:"min_cell_mv"`
	MaxCellMv    *int        `json:"max_cell_mv"`
	TS           *float64    `json:"ts"`
	Error        *string     `json:"error"`
}

func fp(f float64) *float64 { return &f }
func ip(i int) *int         { return &i }
func bp(b bool) *bool       { return &b }

// MergeReading combines 0x03 + 0x04 into the shape stored in bms_live.
func MergeReading(b BasicInfo, cells []int) Reading {
	prot := b.Protection
	r := Reading{
		VoltageV: fp(b.VoltageV), CurrentA: fp(b.CurrentA), PowerW: fp(b.PowerW),
		RemainAh: fp(b.RemainAh), FullAh: fp(b.FullAh), Cycles: ip(b.Cycles),
		BalanceLow: ip(b.BalanceLow), BalanceHigh: ip(b.BalanceHigh), Protection: &prot,
		Version: ip(b.Version), SocPct: fp(b.SocPct), ChargeFET: bp(b.ChargeFET),
		DischargeFET: bp(b.DischargeFET), CellCount: ip(b.CellCount), NTCCount: ip(b.NTCCount),
		TempsC: b.TempsC, TempC: b.TempC,
	}
	if len(cells) > 0 {
		r.CellMv = append([]int(nil), cells...)
		lo, hi := cells[0], cells[0]
		for _, c := range cells {
			if c < lo {
				lo = c
			}
			if c > hi {
				hi = c
			}
		}
		r.MinCellMv, r.MaxCellMv = &lo, &hi
	} else {
		r.CellMv = []int{}
	}
	return r
}
