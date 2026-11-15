package kasa

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// KLAP-era (SMART) devices answer a TDP discovery datagram on UDP 20002 rather
// than the legacy XOR get_sysinfo broadcast on 9999. The query is python-kasa's
// fixed 16-byte DISCOVERY_QUERY_2 header: version 2, opcode 1, CRC 0x463cb5d3.
const klapDiscoveryPort = 20002

var klapDiscoveryQuery = []byte{0x02, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46, 0x3c, 0xb5, 0xd3}

// DiscoveryResult is the parsed `result` object of a 20002 response.
type DiscoveryResult struct {
	Host       string `json:"ip"`
	DeviceID   string `json:"device_id"`
	DeviceType string `json:"device_type"`
	Model      string `json:"device_model"`
	MAC        string `json:"mac"`
	Encrypt    string `json:"encrypt_type"`
	HTTPPort   int    `json:"http_port"`
}

// ParseKlapDiscovery validates the 16-byte TDP header and decodes the JSON
// payload. Responses without an IP use the datagram source address.
func ParseKlapDiscovery(datagram []byte, from string) (DiscoveryResult, error) {
	if len(datagram) <= 16 {
		return DiscoveryResult{}, errors.New("discovery response too short")
	}
	if datagram[0] != 0x02 {
		return DiscoveryResult{}, fmt.Errorf("unsupported discovery version %d", datagram[0])
	}
	if size := int(binary.BigEndian.Uint16(datagram[4:6])); size != 0 && size != len(datagram)-16 {
		return DiscoveryResult{}, errors.New("discovery length mismatch")
	}
	var doc struct {
		Result struct {
			DiscoveryResult
			Scheme struct {
				Type     string `json:"encrypt_type"`
				HTTPPort int    `json:"http_port"`
			} `json:"mgt_encrypt_schm"`
		} `json:"result"`
	}
	if err := json.Unmarshal(datagram[16:], &doc); err != nil {
		return DiscoveryResult{}, err
	}
	r := doc.Result.DiscoveryResult
	r.Encrypt, r.HTTPPort = doc.Result.Scheme.Type, doc.Result.Scheme.HTTPPort
	if r.Host == "" {
		r.Host = from
	}
	if r.Host == "" {
		return DiscoveryResult{}, errors.New("discovery response without host")
	}
	return r, nil
}

// discoverKlap broadcasts the TDP query and collects responses until timeout.
func discoverKlap(ctx context.Context, timeout time.Duration, target string, port int) (map[string]DiscoveryResult, error) {
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer pc.Close()
	if port == 0 {
		port = klapDiscoveryPort
	}
	dst, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(target, fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	for i := 0; i < 3; i++ {
		_, _ = pc.WriteTo(klapDiscoveryQuery, dst)
	}
	_ = pc.SetReadDeadline(time.Now().Add(timeout))
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = pc.SetReadDeadline(time.Now())
		case <-stop:
		}
	}()
	out := map[string]DiscoveryResult{}
	buf := make([]byte, 8192)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			break
		}
		from := ""
		if u, ok := addr.(*net.UDPAddr); ok {
			from = u.IP.String()
		}
		r, err := ParseKlapDiscovery(buf[:n], from)
		if err != nil {
			continue
		}
		// Only KLAP is implemented natively; AES/TPAP devices are skipped
		// rather than reported as controllable.
		if r.Encrypt != "" && !strings.EqualFold(r.Encrypt, "KLAP") {
			continue
		}
		out[r.Host] = r
	}
	return out, nil
}
