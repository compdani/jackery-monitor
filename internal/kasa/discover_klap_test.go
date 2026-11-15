package kasa

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func klapReply(payload string) []byte {
	h := make([]byte, 16)
	h[0] = 2
	binary.BigEndian.PutUint16(h[4:6], uint16(len(payload)))
	return append(h, payload...)
}

func TestParseKlapDiscovery(t *testing.T) {
	r, err := ParseKlapDiscovery(klapReply(`{"result":{"device_id":"abc","device_type":"SMART.KASAPLUG","device_model":"KP125M(US)","ip":"10.0.0.9","mac":"AA","mgt_encrypt_schm":{"encrypt_type":"KLAP","http_port":80}}}`), "10.0.0.1")
	if err != nil || r.Host != "10.0.0.9" || r.Model != "KP125M(US)" || r.Encrypt != "KLAP" || r.HTTPPort != 80 {
		t.Fatal(r, err)
	}
	r, err = ParseKlapDiscovery(klapReply(`{"result":{"device_type":"SMART.KASAPLUG"}}`), "10.0.0.7")
	if err != nil || r.Host != "10.0.0.7" {
		t.Fatal(r, err)
	}
	for _, bad := range [][]byte{nil, make([]byte, 16), append([]byte{3}, klapReply(`{}`)[1:]...), append(klapReply(`{}`), 'x')} {
		if _, err := ParseKlapDiscovery(bad, "h"); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}

// A loopback UDP responder answers the fixed TDP query; the discovered KLAP
// device then handshakes lazily against the fake HTTP device.
func TestKlapDiscoveryEndToEnd(t *testing.T) {
	f := &fakeKlap{}
	srv := httptest.NewServer(f.handler("me@example.com", "pw"))
	defer srv.Close()
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			n, from, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			if !bytes.Equal(buf[:n], klapDiscoveryQuery) {
				continue
			}
			_, _ = udp.WriteTo(klapReply(`{"result":{"device_type":"SMART.KASAPLUG","device_model":"KP125M","ip":"127.0.0.1","mgt_encrypt_schm":{"encrypt_type":"KLAP","http_port":`+portStr+`}}}`), from)
			_, _ = udp.WriteTo(klapReply(`{"result":{"ip":"127.0.0.2","mgt_encrypt_schm":{"encrypt_type":"AES"}}}`), from)
		}
	}()
	b := &NativeBackend{Timeout: 2 * time.Second, BroadcastAddr: "127.0.0.1", KlapDiscoveryPort: udp.LocalAddr().(*net.UDPAddr).Port, SkipLegacyDiscovery: true}
	c := NewClient(func() *Credentials { return &Credentials{Email: "me@example.com", Password: "pw"} })
	c.Backend = b
	infos, err := c.Discover(context.Background(), 500*time.Millisecond)
	if err != nil || len(infos) != 1 {
		t.Fatal(infos, err)
	}
	if infos[0].Host != "127.0.0.1" || infos[0].Alias != "Garage" || infos[0].Type != "Plug" || infos[0].PowerW == nil || *infos[0].PowerW != 41 {
		t.Fatalf("%+v", infos[0])
	}
}
