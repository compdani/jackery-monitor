package siseli

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const StaleSeconds = 1800

func DecodedToCanonical(s Values) Values {
	solar, ok := Number(s["generation_power_w"])
	if !ok {
		solar = num(s["pv_w"]) + num(s["pv2_power_w"])
	}
	grid, feed := 0., 0.
	switch s["mains_current_flow_direction"] {
	case "Inverter To Mains":
		feed = num(s["mains_power_w"])
	case "Idle":
	default:
		grid = num(s["mains_power_w"])
	}
	v := Values{"pvInputPower": solar, "loadPower": nullable(s["load_w"]), "gridPower": grid, "feedInPower": feed, "batteryVoltage": nullable(s["bat_v"]), "batteryChargingCurrent": num(s["bat_charge_current"]), "batteryDischargeCurrent": num(s["dischg_current"])}
	for dst, src := range map[string]string{"batterySOC": "bat_cap", "pvVoltage": "pv_v", "pvCurrent": "pv_a", "pv1Power": "pv_w", "pv2Power": "pv2_power_w"} {
		if n, ok := Number(s[src]); ok {
			v[dst] = n
		}
	}
	return v
}
func ReadingsFromTelemetry(t Values) Values {
	o := Values{}
	for k, v := range map[string]string{"solar_w": "solar_input_w", "load_w": "output_power_w", "grid_w": "ac_input_w", "feed_in_w": "feed_in_w", "battery_v": "battery_voltage_v", "charge_a": "battery_charge_a", "discharge_a": "battery_discharge_a", "soc": "battery_percent"} {
		o[k] = t[v]
	}
	return o
}
func ShouldSkipPortalLatest(running bool, entry Values, now, staleSeconds float64) bool {
	ts, ok := Number(entry["ts"])
	return running && entry["origin"] == "local" && ok && now-ts < staleSeconds
}
func ProbeOutcome(errText string, running, packets, decoded bool, readings Values) Values {
	ok := false
	outcome := "no-packets"
	detail := "Listened, but no packets arrived from that inverter IP. Check the IP, that this host is on the same network, and that ARP inspection is not blocking the path."
	var r any
	if decoded {
		ok = true
		outcome = "decoded"
		detail = "Decoded an MQTT publish from the inverter."
		r = readings
	} else if errText != "" && !running {
		outcome = "capture-failed"
		detail = errText
	} else if packets {
		ok = true
		outcome = "packets"
		detail = "Packets from the inverter are on the path, but no MQTT publish arrived yet. The dongle often waits a few minutes."
	} else if errText != "" {
		outcome = "capture-failed"
		detail = errText
	}
	return Values{"ok": ok, "outcome": outcome, "detail": detail, "readings": r}
}
func DescribeMQTTStreams(rows []Values, since *float64) string {
	groups := [3][]string{}
	for _, r := range rows {
		idx := 1
		if r["encrypted"] == true {
			idx = 2
		} else if m := object(r["readings"]); len(m) > 0 && (since == nil || num(r["readings_ts"]) >= *since) {
			idx = 0
		}
		groups[idx] = append(groups[idx], fmt.Sprintf("%s:%v", text(r["ip"]), r["port"]))
	}
	parts := []string{}
	if len(groups[0]) > 0 {
		parts = append(parts, "Decoded an MQTT publish from "+strings.Join(groups[0], ", ")+".")
	}
	if len(groups[1]) > 0 {
		parts = append(parts, "Saw MQTT to "+strings.Join(groups[1], ", ")+", but no publish decoded yet. The dongle often waits a few minutes.")
	}
	if len(groups[2]) > 0 {
		parts = append(parts, "Saw encrypted MQTT to "+strings.Join(groups[2], ", ")+". That stream cannot be decoded.")
	}
	return strings.Join(parts, " ")
}

var unsafeDeviceID = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func SanitizeDeviceID(s string) string {
	s = strings.Trim(unsafeDeviceID.ReplaceAllString(strings.TrimSpace(s), "_"), "_")
	if s == "" {
		return "siseli_inverter_1"
	}
	return s
}

type LocalConfig struct {
	DeviceID         string        `json:"device_id"`
	InverterIP       string        `json:"inverter_ip"`
	RouterIP         string        `json:"router_ip"`
	Interface        string        `json:"sniff_iface"`
	BrokerIP         string        `json:"mqtt_broker_ip"`
	TelemetryTimeout time.Duration `json:"telemetry_timeout"`
}

func (c LocalConfig) Normalize() LocalConfig {
	c.DeviceID = SanitizeDeviceID(c.DeviceID)
	if c.TelemetryTimeout <= 0 {
		c.TelemetryTimeout = 1800 * time.Second
	}
	return c
}
func (c LocalConfig) Validate() error {
	for _, s := range []string{c.InverterIP, c.RouterIP, c.BrokerIP} {
		if s != "" && net.ParseIP(s) == nil {
			return fmt.Errorf("invalid IP address %q", s)
		}
	}
	return nil
}

// SensorRegistered intentionally accepts all wire keys, matching the Python shim.
func SensorRegistered(string) bool { return true }

type Message struct {
	Broker  string
	Topic   string
	Payload []byte
}

// MQTTTransport supplies already decrypted publishes. Run must return on cancellation.
// Packet capture adapters may implement this interface without a broker subscription.
type MQTTTransport interface {
	Run(context.Context, func(Message) error) error
}
type SnapshotSink interface {
	Publish(context.Context, Values) error
}
type SinkFunc func(context.Context, Values) error

func (f SinkFunc) Publish(ctx context.Context, v Values) error { return f(ctx, v) }

// LocalClient owns decoder state, diagnostic streams and its bounded capture ring.
type LocalClient struct {
	mu        sync.Mutex
	config    LocalConfig
	sink      SnapshotSink
	state     Values
	last      time.Time
	intervals []time.Duration
	streams   map[string]Values
	capture   bool
	captures  []Values
	running   bool
	lastError string
}

func NewLocalClient(config LocalConfig, sink SnapshotSink) *LocalClient {
	return &LocalClient{config: config.Normalize(), sink: sink, state: Values{}, streams: map[string]Values{}}
}
func (l *LocalClient) Snapshot() Values  { l.mu.Lock(); defer l.mu.Unlock(); return clone(l.state) }
func (l *LocalClient) Running() bool     { l.mu.Lock(); defer l.mu.Unlock(); return l.running }
func (l *LocalClient) LastError() string { l.mu.Lock(); defer l.mu.Unlock(); return l.lastError }
func (l *LocalClient) Run(ctx context.Context, t MQTTTransport) error {
	if err := l.config.Validate(); err != nil {
		return err
	}
	l.mu.Lock()
	if l.running {
		l.mu.Unlock()
		return errors.New("local client already running")
	}
	l.running = true
	l.lastError = ""
	l.mu.Unlock()
	err := t.Run(ctx, func(m Message) error { _, err := l.ParsePayload(ctx, m); return err })
	l.mu.Lock()
	l.running = false
	if err != nil && !errors.Is(err, context.Canceled) {
		l.lastError = err.Error()
	}
	l.mu.Unlock()
	return err
}
func (l *LocalClient) Fresh(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	timeout := l.config.TelemetryTimeout
	for _, d := range l.intervals {
		if 3*d > timeout {
			timeout = 3 * d
		}
	}
	if timeout > 30*time.Minute {
		timeout = 30 * time.Minute
	}
	return l.running && !l.last.IsZero() && now.Sub(l.last) < timeout
}
func (l *LocalClient) SetMQTTCapture(enabled bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.capture = enabled
	l.captures = nil
}
func (l *LocalClient) MQTTCaptures() []Values {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.captures
	l.captures = nil
	if out == nil {
		return []Values{}
	}
	return out
}
func (l *LocalClient) NoteStream(ip string, port int, fields Values) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := fmt.Sprintf("%s:%d", ip, port)
	row := l.streams[key]
	if row == nil {
		row = Values{"ip": ip, "port": port}
		l.streams[key] = row
	}
	for k, v := range fields {
		switch k {
		case "payload_bytes", "mqtt_packets":
			row[k] = num(row[k]) + num(v)
		default:
			row[k] = v
		}
	}
	row["ts"] = float64(time.Now().UnixNano()) / 1e9
}
func (l *LocalClient) MQTTStreams() []Values {
	l.mu.Lock()
	defer l.mu.Unlock()
	keys := []string{}
	for k := range l.streams {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []Values{}
	for _, k := range keys {
		out = append(out, clone(l.streams[k]))
	}
	return out
}
func CRC16Modbus(b []byte) uint16 {
	crc := uint16(0xffff)
	for _, v := range b {
		crc ^= uint16(v)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0xa001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}
func ModbusRows(blocks map[string][]byte) map[string][]uint16 {
	rows := map[string][]uint16{}
	for k, b := range blocks {
		if len(b) < 5 || b[1] != 3 || int(b[2])+5 != len(b) || b[2]%2 != 0 || CRC16Modbus(b[:len(b)-2]) != binary.LittleEndian.Uint16(b[len(b)-2:]) {
			continue
		}
		regs := []uint16{}
		for i := 3; i < len(b)-2; i += 2 {
			regs = append(regs, binary.BigEndian.Uint16(b[i:]))
		}
		rows[k] = regs
	}
	return rows
}
func DecodeModbus(blocks map[string][]byte) Values {
	rows := ModbusRows(blocks)
	v := Values{}
	if p := rows["8eyo"]; len(p) >= 3 {
		v["pv_v"], v["pv_a"], v["pv_w"], v["generation_power_w"] = float64(p[0])/10, float64(p[1])/100, int(p[2]), int(p[2])
	}
	if p := rows["WfP8"]; len(p) >= 3 {
		v["bat_cap"], v["bat_v"], v["bat_charge_current"] = int(p[0]), float64(p[1])/10, float64(p[2])/100
		if len(p) >= 6 {
			v["dischg_current"] = float64(p[5]) / 100
		}
		if len(p) >= 7 {
			v["load_w"] = int(p[6])
		}
	}
	return v
}
func DecodeASCII(blocks map[string][]byte) Values {
	out := Values{}
	schemas := map[string][]string{"2ONL": {"bat_series_count", "bat_v", "bat_cap", "bat_charge_current", "dischg_current", "bus_voltage"}, "2l0E": {"out_v", "out_hz", "apparent_va", "load_w", "load_pct", "output_dc_comp", "rated_apparent_va", "inductor_current_a"}, "Mpod": {"pv_v", "pv_current_a", "pv_w"}, "noeP": {"pv2_v", "pv2_current_a", "pv2_power_w"}, "V4W3": {"pv_temp", "inverter_temperature_c", "boost_temperature_c", "transformer_temperature_c", "max_temperature_c", "fan_1_speed", "fan_2_speed"}, "WdRR": {"grid_v", "grid_hz", "high_point_of_mains_power_loss_voltage_v", "low_point_of_mains_power_loss_voltage_v", "high_frequency_of_mains_power_loss_hz", "low_frequency_of_mains_power_loss_hz", "mains_wdrr_value"}}
	for name, keys := range schemas {
		tokens := strings.Fields(strings.Trim(string(blocks[name]), "()\x00\r\n "))
		for i, k := range keys {
			if i >= len(tokens) {
				break
			}
			n, ok := Number(tokens[i])
			if !ok {
				continue
			}
			if (k == "bat_v" || k == "bat_cap") && (n < 0 || n > 100) {
				continue
			}
			if (k == "bat_charge_current" || k == "dischg_current") && (n < 0 || n > 1000) {
				continue
			}
			out[k] = n
		}
	}
	if x, ok := Number(out["mains_wdrr_value"]); ok {
		if x < 0 {
			out["mains_power_w"] = -x
			out["mains_current_flow_direction"] = "Inverter To Mains"
		} else {
			out["mains_power_w"] = x
			out["mains_current_flow_direction"] = "Mains To Inverter"
		}
	}
	if _, ok := out["pv_w"]; ok {
		out["generation_power_w"] = num(out["pv_w"]) + num(out["pv2_power_w"])
	}
	if b := blocks["SUCV"]; len(b) > 0 {
		out["model_code"] = strings.TrimSpace(string(b))
	}
	return out
}
func ExtractBlocks(payload []byte) (map[string][]byte, error) {
	start := strings.IndexByte(string(payload), '{')
	end := strings.LastIndexByte(string(payload), '}')
	if start < 0 || end < start {
		return nil, errors.New("missing JSON payload")
	}
	var obj any
	if err := json.Unmarshal(payload[start:end+1], &obj); err != nil {
		return nil, err
	}
	blocks := map[string][]byte{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if name, ok := x["cn"].(string); ok {
				if encoded, ok := x["co"].(string); ok {
					encoded = strings.TrimSpace(encoded)
					b, err := base64.StdEncoding.DecodeString(encoded)
					if err != nil {
						b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(encoded, "="))
					}
					if err == nil && strings.TrimSpace(name) != "" {
						blocks[strings.TrimSpace(name)] = b
					}
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(obj)
	return blocks, nil
}
func (l *LocalClient) ParsePayload(ctx context.Context, m Message) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	blocks, err := ExtractBlocks(m.Payload)
	if err != nil {
		return false, nil
	}
	v := DecodeASCII(blocks)
	if len(v) == 0 {
		v = DecodeModbus(blocks)
	}
	if len(v) == 0 {
		v = DecodePI30(blocks).Values
	}
	if len(v) == 0 {
		return false, nil
	}
	parts := strings.Split(m.Topic, "/")
	if len(parts) >= 2 && parts[0] == "dtu" {
		v["dtu_id"] = parts[1]
	}
	now := time.Now()
	l.mu.Lock()
	selected := l.config.BrokerIP == "" || l.config.BrokerIP == m.Broker
	if selected {
		for k, x := range v {
			l.state[k] = x
		}
	}
	if selected && !l.last.IsZero() && now.After(l.last) {
		l.intervals = append(l.intervals, now.Sub(l.last))
		if len(l.intervals) > 8 {
			l.intervals = l.intervals[len(l.intervals)-8:]
		}
	}
	if selected {
		l.last = now
	}
	snapshot := clone(l.state)
	if m.Broker != "" {
		snapshot["broker_ip"] = m.Broker
	}
	if l.capture {
		rows := ModbusRows(blocks)
		names := []string{}
		for name := range rows {
			names = append(names, name)
		}
		sort.Strings(names)
		listed := []Values{}
		for _, name := range names {
			used := []int{}
			indices := map[string][]int{"8eyo": {0, 1, 2}, "WfP8": {0, 1, 2, 5, 6}}[name]
			for _, i := range indices {
				if i < len(rows[name]) {
					used = append(used, i)
				}
			}
			listed = append(listed, Values{"name": name, "registers": rows[name], "used": used})
		}
		var load any = "not mapped"
		if x, ok := v["load_w"]; ok {
			load = x
		}
		l.captures = append(l.captures, Values{"ts": float64(now.UnixNano()) / 1e9, "topic": m.Topic, "broker": m.Broker, "mapped": clone(v), "load": load, "blocks": listed})
		if len(l.captures) > 100 {
			l.captures = l.captures[len(l.captures)-100:]
		}
	}
	l.mu.Unlock()
	if l.config.BrokerIP != "" && l.config.BrokerIP != m.Broker {
		return true, nil
	}
	if l.sink != nil {
		if err := l.sink.Publish(ctx, snapshot); err != nil {
			return true, err
		}
	}
	return true, nil
}
