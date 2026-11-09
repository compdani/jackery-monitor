package ble

import (
	"bytes"
	"encoding/hex"
	"math"
	"strings"
	"testing"
)

type basicOpts struct {
	voltageV, currentA, remainAh, fullAh float64
	cycles, soc, cells, fet, prot        int
	ntcC                                 []float64
}

func defaultBasic() basicOpts {
	return basicOpts{voltageV: 53.21, currentA: -12.5, remainAh: 80.0, fullAh: 100.0, cycles: 42,
		soc: 80, ntcC: []float64{25.0, 27.5}, cells: 16, fet: 0x03}
}

func be16(v int) []byte { return []byte{byte(v >> 8), byte(v)} }

func basicPayload(o basicOpts) []byte {
	voltage := int(math.RoundToEven(o.voltageV * 100))
	current := int(math.RoundToEven(o.currentA * 100))
	if current < 0 {
		current = (current + 0x10000) & 0xFFFF
	}
	buf := make([]byte, 23+len(o.ntcC)*2)
	copy(buf[0:2], be16(voltage))
	copy(buf[2:4], be16(current))
	copy(buf[4:6], be16(int(math.RoundToEven(o.remainAh*100))))
	copy(buf[6:8], be16(int(math.RoundToEven(o.fullAh*100))))
	copy(buf[8:10], be16(o.cycles))
	copy(buf[16:18], be16(o.prot))
	buf[19] = byte(o.soc)
	buf[20] = byte(o.fet)
	buf[21] = byte(o.cells)
	buf[22] = byte(len(o.ntcC))
	for i, t := range o.ntcC {
		copy(buf[23+i*2:25+i*2], be16(int(math.RoundToEven((t+273.1)*10))))
	}
	return buf
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}
	return b
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestReadRequestChecksumMatchesKnownFrames(t *testing.T) {
	if !bytes.Equal(BuildRead(0x03), mustHex("dd a5 03 00 ff fd 77")) {
		t.Fatalf("0x03: %x", BuildRead(0x03))
	}
	if !bytes.Equal(BuildRead(0x04), mustHex("dd a5 04 00 ff fc 77")) {
		t.Fatalf("0x04: %x", BuildRead(0x04))
	}
}

func TestRoundTripBasicInfo(t *testing.T) {
	frame := BuildResponse(CmdBasic, basicPayload(defaultBasic()), 0)
	parsed, err := ParseFrame(frame)
	if err != nil || parsed.Cmd != CmdBasic {
		t.Fatalf("parse: %v %+v", err, parsed)
	}
	info, err := ParseBasicInfo(parsed.Payload)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]bool{
		"voltage_v": near(info.VoltageV, 53.21, 1e-6), "current_a": near(info.CurrentA, -12.5, 1e-6),
		"remain_ah": near(info.RemainAh, 80.0, 1e-6), "full_ah": near(info.FullAh, 100.0, 1e-6),
		"cycles": info.Cycles == 42, "soc_pct": info.SocPct == 80, "charge_fet": info.ChargeFET,
		"discharge_fet": info.DischargeFET, "cell_count": info.CellCount == 16,
		"temps_c":    len(info.TempsC) == 2 && info.TempsC[0] == 25.0 && info.TempsC[1] == 27.5,
		"temp_c":     info.TempC != nil && *info.TempC == 27.5,
		"power_w":    near(info.PowerW, 53.21*-12.5, 0.2),
		"protection": len(info.Protection.Active) == 0,
	}
	for k, ok := range checks {
		if !ok {
			t.Errorf("%s failed: %+v", k, info)
		}
	}
}

func TestProtectionBits(t *testing.T) {
	o := defaultBasic()
	o.prot = 0b0000_0001_0000_0001 // cell OV + charge OC
	info, _ := ParseBasicInfo(basicPayload(o))
	if !info.Protection.CellOvervoltage || !info.Protection.ChargeOvercurrent {
		t.Fatalf("%+v", info.Protection)
	}
	found := false
	for _, a := range info.Protection.Active {
		if a == "cell_overvoltage" {
			found = true
		}
	}
	if !found || info.Protection.Raw != 0x0101 {
		t.Fatalf("%+v", info.Protection)
	}
}

func TestCellVoltages(t *testing.T) {
	var payload []byte
	for i := 0; i < 4; i++ {
		payload = append(payload, be16(3300+i)...)
	}
	frame := BuildResponse(CmdCells, payload, 0)
	f, _ := ParseFrame(frame)
	cells, err := ParseCellVoltages(f.Payload)
	if err != nil || len(cells) != 4 || cells[0] != 3300 || cells[3] != 3303 {
		t.Fatalf("cells = %v %v", cells, err)
	}
	soc := 50.0
	merged := MergeReading(BasicInfo{SocPct: soc}, cells)
	if *merged.MinCellMv != 3300 || *merged.MaxCellMv != 3303 {
		t.Fatalf("%+v", merged)
	}
	empty := MergeReading(BasicInfo{}, nil)
	if empty.MinCellMv != nil || empty.MaxCellMv != nil || len(empty.CellMv) != 0 {
		t.Fatalf("%+v", empty)
	}
	if _, err := ParseCellVoltages([]byte{1, 2, 3}); err == nil {
		t.Fatal("odd length must fail")
	}
}

func TestExtractFramesHandlesFragmentsAndJunk(t *testing.T) {
	frame := BuildResponse(CmdBasic, basicPayload(defaultBasic()), 0)
	// junk prefix + split across two notifies + leftover start of next
	buf := append([]byte{0x00, 0xff}, frame[:10]...)
	frames, rest := ExtractFrames(buf)
	if len(frames) != 0 {
		t.Fatalf("frames = %d", len(frames))
	}
	buf = append(append(append([]byte(nil), rest...), frame[10:]...), frame[:4]...)
	frames, rest = ExtractFrames(buf)
	if len(frames) != 1 || !bytes.Equal(frames[0], frame) || !bytes.Equal(rest, frame[:4]) {
		t.Fatalf("frames=%d rest=%x", len(frames), rest)
	}
}

func TestBadChecksumRejected(t *testing.T) {
	frame := BuildResponse(CmdBasic, basicPayload(defaultBasic()), 0)
	frame[len(frame)-3] ^= 0xFF
	_, err := ParseFrame(frame)
	if err == nil || !strings.Contains(err.Error(), "checksum") || !IsJBDError(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusErrorRejected(t *testing.T) {
	_, err := ParseFrame(BuildResponse(CmdBasic, basicPayload(defaultBasic()), 0x80))
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("err = %v", err)
	}
}

func TestShortBasicInfoRejected(t *testing.T) {
	_, err := ParseBasicInfo(make([]byte, 10))
	if err == nil || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("err = %v", err)
	}
}

func TestNotAFrame(t *testing.T) {
	for _, raw := range [][]byte{nil, {1, 2, 3}, {JBDStart, 3, 0, 5, JBDEnd}} {
		if _, err := ParseFrame(raw); err == nil {
			t.Errorf("expected error for %x", raw)
		}
	}
}
