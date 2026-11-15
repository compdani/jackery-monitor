package ecoflow

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
)

// SolarInput is one PV string row (Python solar_inputs entry).
type SolarInput struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Source string  `json:"source"`
	Watts  float64 `json:"watts"`
}

// Detail is the ecoflow_detail dict (keys match Python).
type Detail struct {
	PowInSumW   *float64 `json:"pow_in_sum_w"`
	PowOutSumW  *float64 `json:"pow_out_sum_w"`
	PowGetPv    *float64 `json:"pow_get_pv"`
	PowGetPv2   *float64 `json:"pow_get_pv2"`
	PowGetACIn  float64  `json:"pow_get_ac_in"`
	PowGetACOut *float64 `json:"pow_get_ac_out"`
	PowGet12v   *float64 `json:"pow_get_12v"`
	CmsBattSoc  *float64 `json:"cms_batt_soc"`
	CmsChgDsg   int      `json:"cms_chg_dsg_state"`
	BmsMinTemp  *float64 `json:"bms_min_cell_temp"`
	BmsMaxTemp  *float64 `json:"bms_max_cell_temp"`
	TempPcsDC   *float64 `json:"temp_pcs_dc"`
	TempPcsAC   *float64 `json:"temp_pcs_ac"`
	Cycles      *float64 `json:"cycles"`
	CmsChgRem   *float64 `json:"cms_chg_rem_time"`
	CmsDsgRem   *float64 `json:"cms_dsg_rem_time"`
	ACOutVol    *float64 `json:"ac_out_vol"`
	ACInVol     *float64 `json:"ac_in_vol"`
	DeviceType  string   `json:"device_type"`
}

// Telemetry is the live dict shape shared with Jackery/Siseli. JSON keys
// match the Python dict keys.
type Telemetry struct {
	BatteryPercent *int         `json:"battery_percent"`
	BatteryStatus  int          `json:"battery_status"`
	BatteryTempC   *float64     `json:"battery_temp_c"`
	SolarInputW    int          `json:"solar_input_w"`
	SolarInputs    []SolarInput `json:"solar_inputs"`
	ACInputW       int          `json:"ac_input_w"`
	OutputPowerW   int          `json:"output_power_w"`
	InputPowerW    int          `json:"input_power_w"`
	CarInputW      int          `json:"car_input_w"`
	FeedInW        int          `json:"feed_in_w"`
	ACOn           bool         `json:"ac_on"`
	DCOn           bool         `json:"dc_on"`
	USBOn          bool         `json:"usb_on"`
	CarOn          bool         `json:"car_on"`
	TimeToFullH    *float64     `json:"time_to_full_h"`
	TimeRemainingH *float64     `json:"time_remaining_h"`
	Source         string       `json:"source"`
	ModelName      string       `json:"model_name"`
	EcoflowDetail  Detail       `json:"ecoflow_detail"`
	CapacityWh     *int         `json:"capacity_wh,omitempty"`
}

// Map converts the telemetry into the generic dict form (JSON round trip)
// used by the overlay helpers and the dashboard.
func (t Telemetry) Map() map[string]any {
	b, _ := json.Marshal(t)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

// DetailMap is Detail as a generic dict.
func (d Detail) DetailMap() map[string]any {
	b, _ := json.Marshal(d)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

// pyRound is Python's round(): half to even.
func pyRound(f float64) int { return int(math.RoundToEven(f)) }

// DefaultAPIHost is the EcoFlow cloud host.
const DefaultAPIHost = "api.ecoflow.com"

// ResolveClientID returns a stable private-API MQTT client id:
// ANDROID_<32 upper hex>_<userId>. A preferred id is reused only when it
// matches exactly that shape for this user; the long legacy MD5 form is
// rejected by the broker (CONNACK rc=5).
func ResolveClientID(userID, preferred string) (string, error) {
	uid := strings.TrimSpace(userID)
	if uid == "" {
		return "", errors.New("user_id is required")
	}
	pref := strings.TrimSpace(preferred)
	if pref != "" {
		parts := strings.SplitN(pref, "_", 3)
		if len(parts) == 3 && parts[0] == "ANDROID" && len(parts[1]) == 32 &&
			strings.Trim(parts[1], "0123456789ABCDEF") == "" && parts[2] == uid {
			return pref, nil
		}
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ANDROID_" + strings.ToUpper(hex.EncodeToString(b)) + "_" + uid, nil
}

// num mirrors _num: bool and nil are not numbers; numeric strings parse.
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case bool:
		return 0, false
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint32:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

func numPtr(v any) *float64 {
	if f, ok := num(v); ok {
		return &f
	}
	return nil
}

// first returns the first key present with a non-nil value.
func first(p Params, keys ...string) any {
	for _, k := range keys {
		if v, ok := p[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

// toInt mirrors Python int(value) for flags (accepts float, int, numeric str).
func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(x))
		return i, err == nil
	}
	f, ok := num(v)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// flowOn: Delta 3 flow_info_*: 14 = on, 4 = off.
func flowOn(v any) *bool {
	i, ok := toInt(v)
	if !ok {
		return nil
	}
	t, f := true, false
	switch i {
	case 14:
		return &t
	case 4:
		return &f
	}
	return nil
}

// SolarInputsFromParams builds the per-string solar list from dual-PV
// EcoFlow fields. Zero readings are kept so the UI can show both inputs.
func SolarInputsFromParams(p Params, labelPrefix string) []SolarInput {
	if labelPrefix == "" {
		labelPrefix = "EcoFlow"
	}
	pairs := []struct {
		keys  [2]string
		label string
	}{
		{[2]string{"pow_get_pv", "powGetPv"}, labelPrefix + " PV1"},
		{[2]string{"pow_get_pv2", "powGetPv2"}, labelPrefix + " PV2"},
	}
	out := []SolarInput{}
	for _, pr := range pairs {
		w, ok := num(first(p, pr.keys[0], pr.keys[1]))
		if !ok {
			continue
		}
		out = append(out, SolarInput{ID: pr.keys[0], Label: pr.label, Source: "ecoflow", Watts: float64(pyRound(w))})
	}
	return out
}

func titleCase(deviceType string) string {
	words := strings.Split(strings.ReplaceAll(deviceType, "_", " "), " ")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}

// TelemetryOpts are the keyword arguments of params_to_telemetry.
type TelemetryOpts struct {
	DeviceType string // default DELTA_3_MAX_PLUS
	Alias      string
	CapacityWh int
}

// ParamsToTelemetry maps a flat EcoFlow params dict onto dashboard
// telemetry.
func ParamsToTelemetry(p Params, o TelemetryOpts) Telemetry {
	if p == nil {
		p = Params{}
	}
	deviceType := o.DeviceType
	if deviceType == "" {
		deviceType = "DELTA_3_MAX_PLUS"
	}
	prefix := strings.TrimSpace(o.Alias)
	if prefix == "" {
		prefix = titleCase(deviceType)
	}
	solarInputs := SolarInputsFromParams(p, prefix)
	solar := 0.0
	for _, in := range solarInputs {
		solar += in.Watts
	}
	if len(solarInputs) == 0 {
		if solo, ok := num(first(p, "pow_get_pv", "powGetPv", "solar_input_w")); ok {
			solar = solo
			solarInputs = []SolarInput{{ID: "pow_get_pv", Label: prefix + " PV1", Source: "ecoflow", Watts: float64(pyRound(solo))}}
		}
	}

	socPtr := numPtr(first(p, "cms_batt_soc", "cmsBattSoc", "bms_batt_soc", "bmsBattSoc", "soc"))
	batteryStatus := 0
	if raw := first(p, "cms_chg_dsg_state", "cmsChgDsgState", "bms_chg_dsg_state", "bmsChgDsgState", "chg_dsg_state"); raw != nil {
		if i, ok := toInt(raw); ok {
			batteryStatus = i
		}
	}

	acIn, _ := num(first(p, "pow_get_ac_in", "powGetAcIn"))
	acOutPtr := numPtr(first(p, "pow_get_ac_out", "powGetAcOut"))
	if acOutPtr != nil {
		v := math.Abs(*acOutPtr)
		acOutPtr = &v
	}
	totalOut := numPtr(first(p, "pow_out_sum_w", "powOutSumW"))
	totalIn := numPtr(first(p, "pow_in_sum_w", "powInSumW"))
	var output float64
	if totalOut != nil {
		output = *totalOut
	} else if acOutPtr != nil {
		output = *acOutPtr
	}
	inputW := solar + acIn
	if totalIn != nil {
		inputW = *totalIn
	}

	acOn := flowOn(first(p, "flow_info_ac_out", "flowInfoAcOut"))
	if acOn == nil {
		if cfg := first(p, "cfg_ac_out_open", "cfgAcOutOpen"); cfg != nil {
			if i, ok := toInt(cfg); ok {
				b := i != 0
				acOn = &b
			}
		}
	}
	dcOn := flowOn(first(p, "flow_info_12v", "flowInfo12v"))
	if dcOn == nil {
		if cfg := first(p, "cfg_dc12v_out_open", "cfgDc12vOutOpen", "dc_out_open"); cfg != nil {
			if i, ok := toInt(cfg); ok {
				b := i != 0
				dcOn = &b
			}
		}
	}
	usbOn := false
	if cfg := first(p, "cfg_usb_open", "cfgUsbOpen"); cfg != nil {
		if i, ok := toInt(cfg); ok {
			usbOn = i != 0
		}
	}

	temp := numPtr(first(p, "bms_max_cell_temp", "bmsMaxCellTemp", "bms_min_cell_temp", "temp", "battery_temp_c"))
	chgMin := numPtr(first(p, "cms_chg_rem_time", "cmsChgRemTime", "bms_chg_rem_time"))
	dsgMin := numPtr(first(p, "cms_dsg_rem_time", "cmsDsgRemTime", "bms_dsg_rem_time"))

	detail := Detail{
		PowInSumW:   numPtr(first(p, "pow_in_sum_w", "powInSumW")),
		PowOutSumW:  numPtr(first(p, "pow_out_sum_w", "powOutSumW")),
		PowGetPv:    numPtr(first(p, "pow_get_pv", "powGetPv")),
		PowGetPv2:   numPtr(first(p, "pow_get_pv2", "powGetPv2")),
		PowGetACIn:  acIn,
		PowGetACOut: acOutPtr,
		PowGet12v:   numPtr(first(p, "pow_get_12v", "powGet12v")),
		CmsBattSoc:  socPtr,
		CmsChgDsg:   batteryStatus,
		BmsMinTemp:  numPtr(first(p, "bms_min_cell_temp")),
		BmsMaxTemp:  numPtr(first(p, "bms_max_cell_temp")),
		TempPcsDC:   numPtr(first(p, "temp_pcs_dc")),
		TempPcsAC:   numPtr(first(p, "temp_pcs_ac")),
		Cycles:      numPtr(first(p, "cycles")),
		CmsChgRem:   chgMin,
		CmsDsgRem:   dsgMin,
		ACOutVol:    numPtr(first(p, "plug_in_info_ac_out_vol", "plugInInfoAcOutVol")),
		ACInVol:     numPtr(first(p, "plug_in_info_ac_in_vol", "plugInInfoAcInVol")),
		DeviceType:  deviceType,
	}

	t := Telemetry{
		BatteryStatus: batteryStatus,
		BatteryTempC:  temp,
		SolarInputW:   pyRound(solar),
		SolarInputs:   solarInputs,
		ACInputW:      pyRound(acIn),
		OutputPowerW:  pyRound(output),
		InputPowerW:   pyRound(inputW),
		ACOn:          acOn != nil && *acOn,
		DCOn:          dcOn != nil && *dcOn,
		USBOn:         usbOn,
		Source:        "ecoflow",
		ModelName:     titleCase(deviceType),
		EcoflowDetail: detail,
	}
	if socPtr != nil {
		v := pyRound(*socPtr)
		t.BatteryPercent = &v
	}
	if chgMin != nil && *chgMin > 0 {
		v := *chgMin / 60.0
		t.TimeToFullH = &v
	}
	if dsgMin != nil && *dsgMin > 0 {
		v := *dsgMin / 60.0
		t.TimeRemainingH = &v
	}
	if o.CapacityWh > 0 {
		c := o.CapacityWh
		t.CapacityWh = &c
	}
	return t
}

// MergeParams merges sparse MQTT deltas into the accumulated params
// without mutating dst.
func MergeParams(dst, src Params) Params {
	out := make(Params, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if v != nil {
			out[k] = v
		}
	}
	return out
}
