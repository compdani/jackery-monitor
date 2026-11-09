// Package siseli implements the Solar of Things portal and local telemetry codecs.
// Persistence and packet acquisition are supplied by the caller; no PocketBase is used.
package siseli

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

type Values = map[string]any

//go:embed tables.json
var tablesJSON []byte
var tables struct {
	Controls []Values            `json:"CONTROL_DEFINITIONS"`
	Aliases  map[string][]string `json:"SETTING_KEY_ALIASES"`
	Rules    map[string][][]any  `json:"ENERGY_FLOW_RULES"`
}

func init() {
	if err := json.Unmarshal(tablesJSON, &tables); err != nil {
		panic(err)
	}
}
func clone(v Values) Values {
	out := Values{}
	for k, x := range v {
		out[k] = x
	}
	return out
}
func text(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func Number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		n, e := x.Float64()
		return n, e == nil
	case string:
		n, e := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return n, e == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
	case []any:
		for i := len(x) - 1; i >= 0; i-- {
			if n, ok := Number(x[i]); ok {
				return n, true
			}
		}
	case map[string]any:
		for _, k := range []string{"value", "val", "latest"} {
			if y, ok := x[k]; ok {
				return Number(y)
			}
		}
	}
	return 0, false
}
func num(v any) float64 { n, _ := Number(v); return n }
func nullable(v any) any {
	if n, ok := Number(v); ok {
		return n
	}
	return nil
}
func first(v Values, keys ...string) any {
	for _, k := range keys {
		if x := v[k]; x != nil && text(x) != "" {
			return x
		}
	}
	return nil
}
func IsSiseliSN(s string) bool { return strings.HasPrefix(s, "siseli:") }
func MakeSN(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty Siseli device id")
	}
	if IsSiseliSN(s) {
		return s, nil
	}
	return "siseli:" + s, nil
}
func PortalIDFromSN(s string) string { return strings.TrimPrefix(s, "siseli:") }
func MapEnergyFlowFields(f Values) Values {
	o := Values{}
	for k, rules := range tables.Rules {
		for _, r := range rules {
			mode := text(r[0])
			scale := num(r[2])
			total := 0.
			found := false
			for _, key := range r[1].([]any) {
				if n, ok := Number(f[text(key)]); ok {
					found = true
					total += n
					if mode == "first" {
						break
					}
				}
			}
			if found {
				total *= scale
				if mode == "clamp_neg" {
					total = math.Max(0, -total)
				}
				if mode == "clamp_pos" {
					total = math.Max(0, total)
				}
				o[k] = total
				break
			}
		}
	}
	return o
}
func MapLatestStateFields(f Values) Values {
	o := Values{}
	read := func(dst string, watts bool, keys ...string) {
		for _, k := range keys {
			raw, ok := f[k]
			if !ok {
				continue
			}
			unit := ""
			if m, ok := raw.(map[string]any); ok {
				raw = m["value"]
				unit = text(m["unit"])
			}
			n, ok := Number(raw)
			if !ok {
				continue
			}
			if watts && unit == "" {
				continue
			}
			if watts && strings.EqualFold(strings.TrimSpace(unit), "kw") {
				n *= 1000
			}
			o[dst] = n
			break
		}
	}
	read("batterySOC", false, "batteryRemainingCapacitySOC", "batteryPercentage", "bmsSOC", "batterySOC")
	read("batteryVoltage", false, "BatteryVoltage", "bmsBatteryVoltage", "positiveTerminalBatteryVoltage", "batteryVoltage")
	read("pvInputPower", true, "generationPower")
	if o["pvInputPower"] == nil {
		for _, k := range []string{"pvInputPower", "pvPower"} {
			if n, ok := Number(f[k]); ok {
				o["pvInputPower"] = n
				break
			}
			read("pvInputPower", true, k)
		}
	}
	if o["pvInputPower"] == nil {
		sum := 0.
		found := false
		for i := 1; i <= 4; i++ {
			if n, ok := Number(f[fmt.Sprintf("pv%dPower", i)]); ok {
				sum += n
				found = true
			}
		}
		if found {
			o["pvInputPower"] = sum
		}
	}
	read("pvVoltage", false, "PVVoltage", "pvVoltage")
	read("pvCurrent", false, "PVCurrent", "pvCurrent")
	read("loadPower", true, "load_power", "loadPower", "acOutputActivePower")
	if x := o["loadPower"]; x != nil {
		o["acOutputActivePower"] = x
	}
	read("batteryChargingCurrent", false, "ChargingCurrent", "batteryChargingCurrent")
	read("batteryDischargeCurrent", false, "batteryDischargeCurrent")
	read("batteryTempC", false, "batteryTemperature1", "batteryTemperature2")
	return o
}
func ApplyDerivedValues(v Values) {
	if v["batteryPower"] == nil {
		v["batteryPower"] = (num(v["batteryDischargeCurrent"]) - num(v["batteryChargingCurrent"])) * num(v["batteryVoltage"])
	}
	if v["loadPower"] == nil {
		v["loadPower"] = num(v["acOutputActivePower"])
	}
}
func ToTelemetry(v Values) Values {
	solar, grid := num(v["pvInputPower"]), num(v["gridPower"])
	load, ok := Number(v["loadPower"])
	if !ok {
		load = num(v["acOutputActivePower"])
	}
	charge, discharge := num(v["batteryChargingCurrent"]), num(v["batteryDischargeCurrent"])
	status := 0
	if charge > .05 {
		status = 1
	} else if discharge > .05 {
		status = 2
	}
	round := func(x float64) any { return math.RoundToEven(x) }
	inputs := []Values{}
	add := func(k, id, label string) {
		if n, ok := Number(v[k]); ok && n != 0 {
			inputs = append(inputs, Values{"id": id, "label": label, "source": "siseli", "watts": round(n)})
		}
	}
	for i := 1; i <= 4; i++ {
		add(fmt.Sprintf("pv%dPower", i), fmt.Sprintf("siseli:pv%d", i), fmt.Sprintf("Siseli PV%d", i))
	}
	if len(inputs) == 0 {
		add("pvPower", "siseli:pv1", "Siseli PV1")
		add("pv2Power", "siseli:pv2", "Siseli PV2")
	}
	if len(inputs) == 0 && solar != 0 {
		inputs = append(inputs, Values{"id": "siseli:pv", "label": "Siseli solar", "source": "siseli", "watts": round(solar)})
	}
	var soc any
	if n, ok := Number(v["batterySOC"]); ok {
		soc = round(n)
	}
	return Values{"battery_percent": soc, "solar_input_w": round(solar), "solar_inputs": inputs, "ac_input_w": round(grid), "feed_in_w": round(num(v["feedInPower"])), "output_power_w": round(load), "input_power_w": round(solar + grid), "car_input_w": 0, "battery_status": status, "battery_voltage_v": nullable(v["batteryVoltage"]), "battery_charge_a": charge, "battery_discharge_a": discharge, "pv_voltage_v": nullable(v["pvVoltage"]), "pv_current_a": nullable(v["pvCurrent"]), "battery_power_w": nullable(v["batteryPower"]), "battery_temp_c": nullable(v["batteryTempC"]), "ac_on": false, "dc_on": false, "usb_on": false, "car_on": false, "source": "siseli"}
}
func NormalizeDevice(v Values) Values {
	id := strings.TrimSpace(text(first(v, "id", "deviceId", "device_id")))
	if id == "" {
		return nil
	}
	sn, _ := MakeSN(id)
	model := text(first(v, "model", "modelName", "productName"))
	fallback := model
	if fallback == "" {
		fallback = id
	}
	name := text(first(v, "name", "deviceName", "device_name"))
	if name == "" {
		name = fallback
	}
	if model == "" {
		model = "Siseli inverter"
	}
	return Values{"device_id": sn, "device_sn": sn, "portal_device_id": id, "name": name, "model_name": model, "model_code": nil, "source": "siseli"}
}
func ResolveSettingKey(v Values, key string) string {
	keys := tables.Aliases[key]
	if len(keys) == 0 {
		keys = []string{key}
	}
	for _, k := range keys {
		if _, ok := v[k]; ok {
			return k
		}
	}
	return ""
}
func rawValue(v any) any {
	if m, ok := v.(map[string]any); ok {
		if x, ok := m["value"]; ok {
			return x
		}
		return m["valueDisplay"]
	}
	return v
}
func NormalizeControls(v Values) []Values {
	out := []Values{}
	seen := map[string]bool{}
	for _, spec := range tables.Controls {
		key := text(spec["canonical"])
		write := text(spec["write_canonical"])
		if write == "" {
			write = key
		}
		resolved := ResolveSettingKey(v, write)
		if resolved == "" {
			continue
		}
		item := clone(spec)
		item["resolved_key"], item["write_canonical"], item["dynamic"] = resolved, write, false
		n, ok := Number(rawValue(v[resolved]))
		item["value"] = nil
		if ok {
			switch spec["kind"] {
			case "switch":
				item["value"] = int(n) == int(num(spec["on_value"]))
			case "select":
				item["value"] = int(n)
			default:
				item["value"] = n
			}
		}
		out = append(out, item)
		seen[resolved] = true
		seen[key] = true
	}
	dynamic := []Values{}
	for key, entry := range v {
		if seen[key] {
			continue
		}
		m, _ := entry.(map[string]any)
		if m["isHidden"] == true || text(m["isHidden"]) == "1" {
			continue
		}
		name := text(first(m, "nameDisplay", "name"))
		if name == "" {
			name = key
		}
		hint := ""
		if m["valueTypeDict"] == "Enumeration" {
			hint = text(m["valueDisplay"])
		}
		dynamic = append(dynamic, Values{"canonical": key, "kind": "number", "name": name, "unit": text(m["unit"]), "hint": hint, "value": nullable(rawValue(entry)), "resolved_key": key, "write_canonical": key, "dynamic": true})
	}
	sort.Slice(dynamic, func(i, j int) bool {
		a, b := strings.ToLower(text(dynamic[i]["name"])), strings.ToLower(text(dynamic[j]["name"]))
		if a == b {
			return text(dynamic[i]["canonical"]) < text(dynamic[j]["canonical"])
		}
		return a < b
	})
	return append(out, dynamic...)
}
func DefaultLiveControlKeys(controls []Values) []string {
	out := []string{}
	known := map[string]bool{}
	seen := map[string]bool{}
	for _, spec := range tables.Controls {
		known[text(spec["canonical"])] = true
	}
	for _, c := range controls {
		key := text(c["canonical"])
		if known[key] && c["dynamic"] != true && !seen[key] {
			out = append(out, key)
			seen[key] = true
		}
	}
	return out
}
