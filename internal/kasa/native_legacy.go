package kasa

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Legacy TP-Link Smart Home protocol (HS1xx / KP1xx non-"M" plugs): JSON
// obfuscated with an autokey XOR, TCP port 9999 with a 4-byte big-endian
// length prefix; UDP 9999 for broadcast discovery (no prefix).

const (
	legacyPort       = 9999
	legacyInitialKey = 171
)

func xorEncrypt(plain []byte) []byte {
	key := byte(legacyInitialKey)
	out := make([]byte, len(plain))
	for i, b := range plain {
		key ^= b
		out[i] = key
	}
	return out
}

func xorDecrypt(cipher []byte) []byte {
	key := byte(legacyInitialKey)
	out := make([]byte, len(cipher))
	for i, b := range cipher {
		out[i] = key ^ b
		key = b
	}
	return out
}

func legacyRequest(ctx context.Context, host string, req string, timeout time.Duration) (map[string]any, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(legacyPort)))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	payload := xorEncrypt([]byte(req))
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload)))
	copy(buf[4:], payload)
	if _, err := conn.Write(buf); err != nil {
		return nil, err
	}
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > 1<<20 {
		return nil, fmt.Errorf("bad legacy response length %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(xorDecrypt(body), &out); err != nil {
		return nil, err
	}
	return out, nil
}

type legacyDevice struct {
	host    string
	timeout time.Duration
	sys     map[string]any
	power   *float64
}

func (d *legacyDevice) Update(ctx context.Context) error {
	resp, err := legacyRequest(ctx, d.host,
		`{"system":{"get_sysinfo":{}},"emeter":{"get_realtime":{}}}`, d.timeout)
	if err != nil {
		return err
	}
	sysm, _ := resp["system"].(map[string]any)
	info, _ := sysm["get_sysinfo"].(map[string]any)
	if info == nil {
		return fmt.Errorf("legacy get_sysinfo missing in response")
	}
	// Strips expose per-child state; a single plug has relay_state directly.
	d.sys = info
	d.power = nil
	if em, ok := resp["emeter"].(map[string]any); ok {
		if rt, ok := em["get_realtime"].(map[string]any); ok {
			if v, ok := toFloat(rt["power_mw"]); ok {
				w := v / 1000.0
				d.power = &w
			} else if v, ok := toFloat(rt["power"]); ok {
				d.power = &v
			}
		}
	}
	return nil
}

func (d *legacyDevice) set(ctx context.Context, on bool) error {
	state := 0
	if on {
		state = 1
	}
	_, err := legacyRequest(ctx, d.host,
		fmt.Sprintf(`{"system":{"set_relay_state":{"state":%d}}}`, state), d.timeout)
	return err
}

func (d *legacyDevice) TurnOn(ctx context.Context) error  { return d.set(ctx, true) }
func (d *legacyDevice) TurnOff(ctx context.Context) error { return d.set(ctx, false) }

func (d *legacyDevice) str(key string) string {
	s, _ := d.sys[key].(string)
	return s
}

func (d *legacyDevice) Alias() string { return d.str("alias") }
func (d *legacyDevice) Model() string { return d.str("model") }
func (d *legacyDevice) DeviceType() string {
	model := strings.ToUpper(d.str("model"))
	typ := strings.ToLower(d.str("type") + d.str("mic_type"))
	switch {
	case strings.HasPrefix(model, "HS300") || strings.HasPrefix(model, "KP200") ||
		strings.HasPrefix(model, "KP303") || strings.HasPrefix(model, "KP400"):
		return "Strip"
	case strings.Contains(typ, "bulb"):
		return "Bulb"
	case strings.Contains(typ, "plug") || strings.Contains(typ, "outlet"):
		return "Plug"
	}
	return "Unknown"
}
func (d *legacyDevice) IsOn() bool {
	if v, ok := toFloat(d.sys["relay_state"]); ok {
		return v != 0
	}
	return false
}
func (d *legacyDevice) PowerW() *float64 { return d.power }

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// discoverLegacy broadcasts a get_sysinfo query on UDP 9999.
func discoverLegacy(ctx context.Context, timeout time.Duration, target string) (map[string]*legacyDevice, error) {
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer pc.Close()
	dst, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(target, fmt.Sprint(legacyPort)))
	if err != nil {
		return nil, err
	}
	msg := xorEncrypt([]byte(`{"system":{"get_sysinfo":{}}}`))
	for i := 0; i < 3; i++ {
		_, _ = pc.WriteTo(msg, dst)
	}
	out := map[string]*legacyDevice{}
	deadline := time.Now().Add(timeout)
	_ = pc.SetReadDeadline(deadline)
	go func() {
		<-ctx.Done()
		_ = pc.SetReadDeadline(time.Now())
	}()
	buf := make([]byte, 4096)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			break
		}
		var resp map[string]any
		if json.Unmarshal(xorDecrypt(buf[:n]), &resp) != nil {
			continue
		}
		sysm, _ := resp["system"].(map[string]any)
		info, _ := sysm["get_sysinfo"].(map[string]any)
		if info == nil {
			continue
		}
		host := addr.(*net.UDPAddr).IP.String()
		out[host] = &legacyDevice{host: host, timeout: 5 * time.Second, sys: info}
	}
	return out, nil
}
