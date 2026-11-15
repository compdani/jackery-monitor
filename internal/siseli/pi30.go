package siseli

import (
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

//go:embed pi30_tables.json
var pi30JSON []byte
var piTables struct {
	Blocks map[string]string `json:"blocks"`
	Fields map[string][]struct {
		Index int               `json:"index"`
		Key   string            `json:"key"`
		Cast  string            `json:"cast"`
		Enum  map[string]string `json:"enum"`
	} `json:"fields"`
	Bits  map[string][][]any `json:"bits"`
	Flags [][]string         `json:"flags"`
}

func init() {
	if err := json.Unmarshal(pi30JSON, &piTables); err != nil {
		panic(err)
	}
}
func CRC16Xmodem(b []byte) uint16 {
	var c uint16
	for _, v := range b {
		c ^= uint16(v) << 8
		for i := 0; i < 8; i++ {
			if c&0x8000 != 0 {
				c = (c << 1) ^ 0x1021
			} else {
				c <<= 1
			}
		}
	}
	return c
}
func VerifyPI30Frame(b []byte) ([]byte, bool) {
	if len(b) > 0 && b[len(b)-1] == '\r' {
		b = b[:len(b)-1]
	}
	if len(b) < 3 || b[0] != '(' {
		return nil, false
	}
	payload := b[:len(b)-2]
	want := CRC16Xmodem(payload)
	got := binary.BigEndian.Uint16(b[len(b)-2:])
	bump := func(x byte) byte {
		switch x {
		case 0x28, 0x0d, 0x0a, 0:
			return x + 1
		}
		return x
	}
	if got != want && got != uint16(bump(byte(want>>8)))<<8|uint16(bump(byte(want))) {
		return nil, false
	}
	return payload[1:], true
}
func piShape(name string, t []string) bool {
	single := func(pattern string) bool {
		if len(t) != 1 {
			return false
		}
		return regexp.MustCompile(pattern).MatchString(t[0])
	}
	switch name {
	case "cCft":
		return single(`^PI[0-9]{2}$`)
	case "G5E9", "ahLb":
		return single(`^[0-9A-Za-z]{6,32}$`)
	case "o2lC", "ag5g":
		return single(`^VERFW:[0-9.]+$`)
	case "MrfS":
		return len(t) == 25
	case "sJqt":
		return single(`^([ED][a-z]+){1,2}$`)
	case "G4WT":
		return len(t) >= 21 && len(t) <= 24
	case "zZ3K":
		return single(`^[A-Z]$`)
	case "DB48", "wb83", "oTLG", "48rR":
		if len(t) < 2 {
			return false
		}
		for _, s := range t {
			if !regexp.MustCompile(`^[0-9]+$`).MatchString(s) {
				return false
			}
		}
		return true
	case "lCMp":
		return single(`^[0-9]{14}$`)
	case "7v9T":
		return len(t) == 10 && regexp.MustCompile(`^[0-9]{2}\.[0-9]{2}$`).MatchString(t[5])
	case "EMu5":
		if len(t) != 1 || len(t[0]) < 3 || len(t[0]) > 32 || t[0] == "NAK" {
			return false
		}
		for _, c := range t[0] {
			if unicode.IsLetter(c) {
				return true
			}
		}
		return false
	case "9gbt":
		return single(`^[0-9]{1,8}$`)
	case "cT7S", "mA9W":
		return single(`^[0-9]{8}$`)
	case "UefO":
		return len(t) == 10
	}
	return false
}

type PI30Detection struct {
	Verdict  string   `json:"verdict"`
	Verified int      `json:"verified"`
	Total    int      `json:"total"`
	Mapped   []string `json:"mapped"`
	Vetoed   bool     `json:"vetoed"`
}
type PI30Decode struct {
	Values    Values        `json:"values"`
	Context   Values        `json:"context"`
	Signature string        `json:"signature"`
	HadQPIGS  bool          `json:"had_qpigs"`
	Detection PI30Detection `json:"detection"`
}

func DetectPI30(blocks map[string][]byte) PI30Detection {
	d := PI30Detection{Verdict: "not-pi30", Total: len(blocks), Mapped: []string{}}
	for name, b := range blocks {
		p, ok := VerifyPI30Frame(b)
		if !ok {
			continue
		}
		d.Verified++
		if name == "cCft" && string(p) != "PI30" {
			d.Vetoed = true
		}
		if piShape(name, strings.Split(string(p), " ")) {
			d.Mapped = append(d.Mapped, name)
		}
	}
	sort.Strings(d.Mapped)
	if !d.Vetoed && d.Verified >= 3 && 2*d.Verified >= d.Total {
		d.Verdict = "unknown-names"
		if len(d.Mapped) >= 2 {
			d.Verdict = "pi30"
		}
	}
	return d
}
func DecodePI30(blocks map[string][]byte) PI30Decode {
	r := PI30Decode{Values: Values{}, Context: Values{}, Detection: DetectPI30(blocks)}
	if r.Detection.Verdict != "pi30" {
		return r
	}
	rated := 0.
	if p, ok := VerifyPI30Frame(blocks["MrfS"]); ok {
		t := strings.Split(string(p), " ")
		if len(t) == 25 {
			rated = num(t[5])
		}
	}
	sig := [3]string{}
	for _, name := range r.Detection.Mapped {
		p, _ := VerifyPI30Frame(blocks[name])
		t := strings.Split(string(p), " ")
		query := piTables.Blocks[name]
		for _, f := range piTables.Fields[query] {
			if f.Index >= len(t) {
				continue
			}
			s := t[f.Index]
			if f.Enum != nil {
				if value, ok := f.Enum[s]; ok {
					r.Values[f.Key] = value
				}
				continue
			}
			switch f.Cast {
			case "_as_text":
				r.Values[f.Key] = s
			case "_as_int":
				if n, e := strconv.Atoi(s); e == nil {
					r.Values[f.Key] = n
				}
			default:
				if n, ok := Number(s); ok {
					r.Values[f.Key] = n
				}
			}
		}
		switch query {
		case "QPIGS":
			r.HadQPIGS = true
			sig[1], sig[2] = t[16], t[20]
			for _, spec := range []struct {
				Index int
				Kind  string
			}{{16, "status"}, {20, "float"}} {
				for _, b := range piTables.Bits[spec.Kind] {
					idx := int(num(b[0]))
					if idx < len(t[spec.Index]) {
						c := t[spec.Index][idx]
						if c == '0' || c == '1' {
							r.Values[text(b[1])] = c == '1'
						}
					}
				}
			}
			if rated > 3000 {
				if n, e := strconv.Atoi(t[11]); e == nil {
					r.Values["pi30_heatsink_temp_c"] = n
				}
			}
		case "QMOD":
			sig[0] = t[0]
			if m := map[string]string{"P": "Power on", "S": "Standby", "L": "Line", "B": "Battery", "F": "Fault"}[t[0]]; m != "" {
				r.Values["pi30_mode"] = m
			}
		case "QFLAG":
			enabled := map[rune]bool{}
			valid := false
			on := false
			for _, c := range t[0] {
				if c == 'E' || c == 'D' {
					valid = true
					on = c == 'E'
				} else if valid {
					enabled[c] = on
				}
			}
			for _, f := range piTables.Flags {
				if on, ok := enabled[rune(f[0][0])]; ok {
					v := "disabled"
					if on {
						v = "enabled"
					}
					r.Values[f[1]] = v
				}
			}
		case "QET":
			r.Values["pi30_pv_energy_total_kwh"] = num(t[0]) / 1000
		case "QLT":
			r.Values["pi30_load_energy_total_kwh"] = num(t[0]) / 1000
		case "QMN":
			r.Values["pi30_model"] = t[0]
		case "QVFW":
			r.Values["pi30_firmware_version"] = strings.TrimPrefix(t[0], "VERFW:")
		case "QPI":
			r.Context["protocol"] = t[0]
		case "QT":
			s := t[0]
			r.Context["inverter_clock"] = s[:4] + "-" + s[4:6] + "-" + s[6:8] + " " + s[8:10] + ":" + s[10:12] + ":" + s[12:14]
		}
	}
	if sig != [3]string{} {
		r.Signature = strings.Join(sig[:], "|")
	}
	return r
}
