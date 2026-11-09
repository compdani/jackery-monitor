package ecoflow

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/compdani/jackery-monitor/internal/ecoflow/efpb"
)

// Params is a flat EcoFlow params dict (snake_case protobuf names or
// camelCase JSON names). Numbers decoded from protobuf are float64.
type Params = map[string]any

// BMSHeartbeatCommands are the (cmdFunc, cmdId) pairs that carry a
// Delta3BMSHeartBeatReport.
var BMSHeartbeatCommands = map[[2]int32]bool{
	{3, 1}: true, {3, 2}: true, {3, 30}: true, {3, 50}: true,
	{32, 1}: true, {32, 3}: true, {32, 50}: true, {32, 51}: true, {32, 52}: true,
	{254, 24}: true, {254, 25}: true, {254, 26}: true, {254, 27}: true,
	{254, 28}: true, {254, 29}: true, {254, 30}: true,
}

// DisplayExtraFloats are Max Plus DisplayPropertyUpload floats absent from
// the vendored schema (field numbers from ecoflow-energy-ha / ioBroker).
var DisplayExtraFloats = map[uint64]string{
	70: "pow_get_pv2",
}

// OutputSetFields maps an output port to its Delta3SetCommand field.
var OutputSetFields = map[string]string{
	"ac":  "cfg_ac_out_open",
	"dc":  "cfg_dc12v_out_open",
	"usb": "cfg_usb_open",
}

func msgToParams(m proto.Message) (Params, error) {
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return nil, err
	}
	out := Params{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func flatten(d map[string]any, parent, sep string, out Params) {
	for k, v := range d {
		key := k
		if parent != "" {
			key = parent + sep + k
		}
		if sub, ok := v.(map[string]any); ok {
			flatten(sub, key, sep, out)
		} else {
			out[key] = v
		}
	}
}

func xorDecode(pdata []byte, seq int32) []byte {
	if len(pdata) == 0 {
		return nil
	}
	out := make([]byte, len(pdata))
	k := byte(seq & 0xFF)
	for i, b := range pdata {
		out[i] = b ^ k
	}
	return out
}

func readVarint(buf []byte, i int) (uint64, int, bool) {
	var result uint64
	var shift uint
	for i < len(buf) {
		b := buf[i]
		i++
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, i, true
		}
		shift += 7
		if shift > 63 {
			break
		}
	}
	return 0, i, false
}

// ExtractUndeclaredFloats scans protobuf wire bytes for fixed32 (wire type
// 5) fields by number. Used for Max Plus keys (e.g. pow_get_pv2 = 70)
// missing from the vendored DisplayPropertyUpload descriptor. A nil
// fieldMap uses DisplayExtraFloats.
func ExtractUndeclaredFloats(pdata []byte, fieldMap map[uint64]string) map[string]float64 {
	mapping := fieldMap
	if mapping == nil {
		mapping = DisplayExtraFloats
	}
	out := map[string]float64{}
	if len(pdata) == 0 || len(mapping) == 0 {
		return out
	}
	i, n := 0, len(pdata)
	for i < n {
		tag, ni, ok := readVarint(pdata, i)
		if !ok {
			break
		}
		i = ni
		fieldNum := tag >> 3
		switch tag & 7 {
		case 0:
			_, ni, ok := readVarint(pdata, i)
			if !ok {
				return out
			}
			i = ni
		case 1:
			i += 8
		case 2:
			length, ni, ok := readVarint(pdata, i)
			if !ok || uint64(ni)+length > uint64(n) {
				return out
			}
			i = ni + int(length)
		case 5:
			if i+4 > n {
				return out
			}
			if name, ok := mapping[fieldNum]; ok {
				out[name] = float64(math.Float32frombits(binary.LittleEndian.Uint32(pdata[i:])))
			}
			i += 4
		default:
			return out
		}
	}
	return out
}

func deriveOutputs(r Params) {
	switch r["flow_info_12v"] {
	case float64(14):
		r["cfg_dc12v_out_open"] = float64(1)
	case float64(4):
		r["cfg_dc12v_out_open"] = float64(0)
	}
	switch r["flow_info_ac_out"] {
	case float64(14):
		r["cfg_ac_out_open"] = float64(1)
	case float64(4):
		r["cfg_ac_out_open"] = float64(0)
	}
	var vals []any
	for _, k := range []string{"flow_info_qcusb1", "flow_info_qcusb2", "flow_info_typec1", "flow_info_typec2"} {
		if v, ok := r[k]; ok {
			vals = append(vals, v)
		}
	}
	if len(vals) > 0 {
		any14, all4 := false, true
		for _, v := range vals {
			if v == float64(14) {
				any14 = true
			}
			if v != float64(4) {
				all4 = false
			}
		}
		if any14 {
			r["cfg_usb_open"] = float64(1)
		} else if all4 {
			r["cfg_usb_open"] = float64(0)
		}
	}
}

func decodeMessageByType(pdata []byte, cmdFunc, cmdID int32) Params {
	switch {
	case cmdFunc == 254 && cmdID == 21:
		msg := &efpb.Delta3DisplayPropertyUpload{}
		if proto.Unmarshal(pdata, msg) != nil {
			return nil
		}
		res, err := msgToParams(msg)
		if err != nil {
			return nil
		}
		// Recover Max Plus PV2 (and any other undeclared floats).
		for k, v := range ExtractUndeclaredFloats(pdata, nil) {
			if cur, ok := res[k]; !ok || cur == nil {
				res[k] = v
			}
		}
		deriveOutputs(res)
		return res
	case cmdFunc == 254 && cmdID == 22:
		msg := &efpb.Delta3RuntimePropertyUpload{}
		if proto.Unmarshal(pdata, msg) != nil {
			return nil
		}
		res, _ := msgToParams(msg)
		return res
	case cmdFunc == 32 && cmdID == 2:
		msg := &efpb.Delta3CMSHeartBeatReport{}
		if proto.Unmarshal(pdata, msg) != nil {
			return nil
		}
		res, _ := msgToParams(msg)
		return res
	case BMSHeartbeatCommands[[2]int32{cmdFunc, cmdID}]:
		msg := &efpb.Delta3BMSHeartBeatReport{}
		if proto.Unmarshal(pdata, msg) != nil {
			return nil
		}
		res, _ := msgToParams(msg)
		return res
	}
	return nil
}

const b64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/="

// maybeB64 mirrors base64.b64decode(raw, validate=True) with raw fallback.
func maybeB64(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	for _, c := range raw {
		if !strings.ContainsRune(b64Alphabet, rune(c)) {
			return raw
		}
	}
	dec, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return raw
	}
	return dec
}

// PeekHeaderCmds returns "cmdFunc/cmdId" labels from a HeaderMessage
// without a full decode.
func PeekHeaderCmds(raw []byte) []string {
	raw = maybeB64(raw)
	msg := &efpb.Delta3HeaderMessage{}
	if proto.Unmarshal(raw, msg) != nil {
		return nil
	}
	var out []string
	for _, h := range msg.GetHeader() {
		out = append(out, fmt.Sprintf("%d/%d", h.GetCmdFunc(), h.GetCmdId()))
	}
	return out
}

// DecodePropertyPayload decodes one private-API MQTT property push into a
// flat params dict. It returns nil when the payload is not a recognisable
// Delta 3 protobuf.
func DecodePropertyPayload(raw []byte) Params {
	raw = maybeB64(raw)
	msg := &efpb.Delta3HeaderMessage{}
	if err := proto.Unmarshal(raw, msg); err != nil {
		slog.Debug("ecoflow header parse failed", "err", err)
		return nil
	}
	if len(msg.GetHeader()) == 0 {
		return nil
	}
	merged := Params{}
	for _, h := range msg.GetHeader() {
		pdata := h.GetPdata()
		if len(pdata) == 0 {
			continue
		}
		if h.GetEncType() == 1 && h.GetSrc() != 32 {
			pdata = xorDecode(pdata, h.GetSeq())
		}
		for k, v := range decodeMessageByType(pdata, h.GetCmdFunc(), h.GetCmdId()) {
			merged[k] = v
		}
	}
	if len(merged) == 0 {
		return nil
	}
	flat := Params{}
	flatten(merged, "", "_", flat)
	return flat
}

// TimeSeq is the 31-bit millisecond sequence used on outgoing headers.
func TimeSeq() int32 { return int32(time.Now().UnixMilli() % 2147483647) }

// BuildQuotaRequest builds the protobuf "get all" request published on the
// get topic.
func BuildQuotaRequest(deviceSN string) []byte {
	from := "SolarPowMonitor"
	seq := TimeSeq()
	src, dest := int32(32), int32(32)
	pkt := &efpb.Delta3SendHeaderMsg{Msg: []*efpb.Delta3Header{{
		Src: &src, Dest: &dest, Seq: &seq, From: &from,
	}}}
	b, err := proto.Marshal(pkt)
	if err != nil {
		return nil
	}
	return b
}

func setIntField(m protoreflect.Message, name string, value int64) bool {
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return false
	}
	switch fd.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		m.Set(fd, protoreflect.ValueOfInt32(int32(value)))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		m.Set(fd, protoreflect.ValueOfUint32(uint32(value)))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		m.Set(fd, protoreflect.ValueOfInt64(value))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		m.Set(fd, protoreflect.ValueOfUint64(uint64(value)))
	case protoreflect.FloatKind:
		m.Set(fd, protoreflect.ValueOfFloat32(float32(value)))
	case protoreflect.DoubleKind:
		m.Set(fd, protoreflect.ValueOfFloat64(float64(value)))
	case protoreflect.BoolKind:
		m.Set(fd, protoreflect.ValueOfBool(value != 0))
	default:
		return false
	}
	return true
}

// BuildSetCommand builds a ConfigWrite (cmdFunc=254, cmdId=17) carrying one
// Delta3SetCommand field. It returns nil when the field is unknown.
func BuildSetCommand(fieldName string, value int, deviceSN string) []byte {
	payload := &efpb.Delta3SetCommand{}
	if !setIntField(payload.ProtoReflect(), fieldName, int64(value)) {
		slog.Error("unknown Delta3 set field", "field", fieldName)
		return nil
	}
	pdata, err := proto.Marshal(payload)
	if err != nil {
		return nil
	}
	i32 := func(v int32) *int32 { return &v }
	seq := TimeSeq()
	msg := &efpb.Delta3Header{
		Src: i32(32), Dest: i32(2), DSrc: i32(1), DDest: i32(1),
		CmdFunc: i32(254), CmdId: i32(17), NeedAck: i32(1), Seq: &seq,
		ProductId: i32(1), Version: i32(19), PayloadVer: i32(1),
		DeviceSn: &deviceSN, DataLen: i32(int32(len(pdata))), Pdata: pdata,
	}
	b, err := proto.Marshal(&efpb.Delta3SendHeaderMsg{Msg: []*efpb.Delta3Header{msg}})
	if err != nil {
		return nil
	}
	return b
}
