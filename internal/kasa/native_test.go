package kasa

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestXorRoundTrip(t *testing.T) {
	in := []byte(`{"system":{"get_sysinfo":{}}}`)
	if got := xorDecrypt(xorEncrypt(in)); !bytes.Equal(got, in) {
		t.Fatalf("got %q", got)
	}
	// Known vector: first byte 171 ^ '{' (0x7b) = 0xd0.
	if xorEncrypt([]byte("{"))[0] != 0xd0 {
		t.Fatal("bad first byte")
	}
}

func TestLegacyDeviceGetters(t *testing.T) {
	// The legacy TCP port is fixed (9999) and may be busy on dev machines, so
	// only the sysinfo-derived getters are exercised here.
	d := &legacyDevice{sys: map[string]any{"alias": "HS", "model": "HS110(US)", "type": "IOT.SMARTPLUGSWITCH", "relay_state": float64(1)}}
	if d.Alias() != "HS" || !d.IsOn() || d.DeviceType() != "Plug" || d.Model() != "HS110(US)" {
		t.Fatalf("legacy getters: %+v", d)
	}
}

// fakeKlap is an in-process KLAP v2 device.
type fakeKlap struct {
	auth    []byte
	local   []byte
	remote  []byte
	session *klapSession
	on      bool
}

func (f *fakeKlap) handler(user, pass string) http.Handler {
	mux := http.NewServeMux()
	f.auth = klapAuthHash(klapV2, user, pass)
	mux.HandleFunc("/app/handshake1", func(w http.ResponseWriter, r *http.Request) {
		f.local, _ = io.ReadAll(r.Body)
		f.remote = make([]byte, 16)
		_, _ = rand.Read(f.remote)
		w.Write(append(append([]byte(nil), f.remote...), klapHandshake1Hash(klapV2, f.local, f.remote, f.auth)...))
	})
	mux.HandleFunc("/app/handshake2", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, klapHandshake2Hash(klapV2, f.local, f.remote, f.auth)) {
			http.Error(w, "bad", 403)
			return
		}
		f.session = newKlapSession(f.local, f.remote, f.auth)
	})
	mux.HandleFunc("/app/request", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seq, _ := strconv.Atoi(r.URL.Query().Get("seq"))
		f.session.seq = int32(seq)
		plain, err := f.session.decrypt(body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(plain, &req)
		result := map[string]any{}
		switch req.Method {
		case "get_device_info":
			result = map[string]any{"nickname": "R2FyYWdl", "model": "KP125M", "type": "SMART.KASAPLUG", "device_on": f.on}
		case "get_current_power":
			result = map[string]any{"current_power": 41}
		case "set_device_info":
			f.on, _ = req.Params["device_on"].(bool)
		}
		out, _ := json.Marshal(map[string]any{"error_code": 0, "result": result})
		// Devices answer with the request's seq.
		f.session.seq = int32(seq)
		w.Write(encryptAtSeq(f.session, out))
	})
	return mux
}

// encryptAtSeq encrypts with the session's current seq without incrementing.
func encryptAtSeq(s *klapSession, msg []byte) []byte {
	s.seq--
	data, _ := s.encrypt(msg)
	return data
}

func TestKlapEndToEndWithFakeDevice(t *testing.T) {
	f := &fakeKlap{}
	srv := httptest.NewServer(f.handler("me@example.com", "pw"))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(portStr)
	b := &NativeBackend{Timeout: 2 * time.Second, KlapPort: port, SkipLegacy: true}
	ctx := context.Background()
	dev, err := b.DiscoverSingle(ctx, host, &Credentials{Email: "me@example.com", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dev.Update(ctx); err != nil {
		t.Fatal(err)
	}
	if dev.Alias() != "Garage" || dev.Model() != "KP125M" || dev.DeviceType() != "Plug" || dev.IsOn() {
		t.Fatalf("device = %s %s %s %v", dev.Alias(), dev.Model(), dev.DeviceType(), dev.IsOn())
	}
	if p := dev.PowerW(); p == nil || *p != 41 {
		t.Fatalf("power = %v", p)
	}
	if err := dev.TurnOn(ctx); err != nil {
		t.Fatal(err)
	}
	_ = dev.Update(ctx)
	if !dev.IsOn() {
		t.Fatal("expected on after TurnOn")
	}
}

func TestKlapRejectsWrongCredentials(t *testing.T) {
	f := &fakeKlap{}
	srv := httptest.NewServer(f.handler("me@example.com", "pw"))
	defer srv.Close()
	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(portStr)
	b := &NativeBackend{Timeout: 2 * time.Second, KlapPort: port, SkipLegacy: true}
	_, err := b.DiscoverSingle(context.Background(), host, &Credentials{Email: "me@example.com", Password: "WRONG"})
	if err == nil || !IsAuthFailure(err) {
		t.Fatalf("want auth failure, got %v", err)
	}
}

func TestKlapAuthHashVectors(t *testing.T) {
	// v1: md5(md5(user)+md5(pass)); v2: sha256(sha1(user)+sha1(pass)).
	if len(klapAuthHash(klapV1, "a", "b")) != 16 || len(klapAuthHash(klapV2, "a", "b")) != 32 {
		t.Fatal("hash sizes")
	}
	s := newKlapSession([]byte("0123456789abcdef"), []byte("fedcba9876543210"), klapAuthHash(klapV2, "a", "b"))
	if len(s.key) != 16 || len(s.iv) != 12 || len(s.sig) != 28 {
		t.Fatal("session key sizes")
	}
}
