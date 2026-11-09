package kasa

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// KLAP is the encrypted local protocol of the KASA SMART line. Messages are
// the SMART JSON-RPC shape ({"method": "get_device_info", ...}) wrapped in an
// AES-128-CBC session derived from a two-step handshake. It is implemented
// from the published python-kasa behaviour (KlapTransport v1 and v2).
//
// NOTE: not exercised against real hardware in this port; the unit tests
// cover the key schedule and an in-process fake KLAP server.

const (
	blankEmail    = "kasa@tp-link.net"
	blankPassword = "kasaSetup"
)

type klapVersion int

const (
	klapV1 klapVersion = 1 // md5-based auth hash
	klapV2 klapVersion = 2 // sha256/sha1-based auth hash
)

func sha256sum(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func md5sum(b []byte) []byte  { s := md5.Sum(b); return s[:] }
func sha1sum(b []byte) []byte { s := sha1.Sum(b); return s[:] }

func klapAuthHash(v klapVersion, user, pass string) []byte {
	if v == klapV2 {
		return sha256sum(sha1sum([]byte(user)), sha1sum([]byte(pass)))
	}
	return md5sum(append(md5sum([]byte(user)), md5sum([]byte(pass))...))
}

func klapHandshake1Hash(v klapVersion, local, remote, auth []byte) []byte {
	if v == klapV2 {
		return sha256sum(local, remote, auth)
	}
	return sha256sum(local, auth)
}

func klapHandshake2Hash(v klapVersion, local, remote, auth []byte) []byte {
	if v == klapV2 {
		return sha256sum(remote, local, auth)
	}
	return sha256sum(remote, auth)
}

// klapSession is the symmetric encryption state after the handshake.
type klapSession struct {
	key []byte // 16
	iv  []byte // 12 byte prefix
	seq int32
	sig []byte // 28
}

func newKlapSession(local, remote, userHash []byte) *klapSession {
	ivFull := sha256sum([]byte("iv"), local, remote, userHash)
	return &klapSession{
		key: sha256sum([]byte("lsk"), local, remote, userHash)[:16],
		iv:  append([]byte(nil), ivFull[:12]...),
		seq: int32(binary.BigEndian.Uint32(ivFull[len(ivFull)-4:])),
		sig: sha256sum([]byte("ldk"), local, remote, userHash)[:28],
	}
}

func (s *klapSession) ivSeq() []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(s.seq))
	return append(append([]byte(nil), s.iv...), b...)
}

func pkcs7Pad(b []byte, bs int) []byte {
	n := bs - len(b)%bs
	return append(append([]byte(nil), b...), bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 || len(b)%aes.BlockSize != 0 {
		return nil, errors.New("bad padding length")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		return nil, errors.New("bad padding")
	}
	return b[:len(b)-n], nil
}

// encrypt returns signature+ciphertext and the sequence number used.
func (s *klapSession) encrypt(msg []byte) ([]byte, int32) {
	s.seq++
	block, _ := aes.NewCipher(s.key)
	padded := pkcs7Pad(msg, aes.BlockSize)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, s.ivSeq()).CryptBlocks(ct, padded)
	seqb := make([]byte, 4)
	binary.BigEndian.PutUint32(seqb, uint32(s.seq))
	sig := sha256sum(s.sig, seqb, ct)
	return append(sig, ct...), s.seq
}

func (s *klapSession) decrypt(data []byte) ([]byte, error) {
	if len(data) < 32+aes.BlockSize {
		return nil, errors.New("klap response too short")
	}
	block, _ := aes.NewCipher(s.key)
	ct := data[32:]
	if len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("klap response not block aligned")
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, s.ivSeq()).CryptBlocks(pt, ct)
	return pkcs7Unpad(pt)
}

// klapTransport performs handshakes and encrypted requests against one host.
type klapTransport struct {
	host    string
	port    int
	http    *http.Client
	version klapVersion
	session *klapSession
	mu      sync.Mutex
}

func newKlapTransport(host string, port int, timeout time.Duration) *klapTransport {
	jar, _ := cookiejar.New(nil)
	if port == 0 {
		port = 80
	}
	return &klapTransport{
		host: host, port: port,
		http: &http.Client{Timeout: timeout, Jar: jar},
	}
}

func (t *klapTransport) url(path string) string {
	return fmt.Sprintf("http://%s/app/%s", net.JoinHostPort(t.host, fmt.Sprint(t.port)), path)
}

func (t *klapTransport) post(ctx context.Context, u string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, err
}

var errKlapAuth = errors.New("KLAP handshake1 challenge mismatch: credentials rejected by device")

// handshake chooses between saved creds, then the blank "kasa setup" creds.
func (t *klapTransport) handshake(ctx context.Context, creds *Credentials) error {
	local := make([]byte, 16)
	if _, err := rand.Read(local); err != nil {
		return err
	}
	status, data, err := t.post(ctx, t.url("handshake1"), local)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("handshake1 HTTP %d", status)
	}
	if len(data) < 48 {
		return errors.New("handshake1 response too short")
	}
	remote, serverHash := data[:16], data[16:48]

	type cand struct{ user, pass string }
	cands := []cand{}
	if creds != nil {
		cands = append(cands, cand{creds.Email, creds.Password})
	}
	cands = append(cands, cand{blankEmail, blankPassword})
	var auth []byte
	var ver klapVersion
	matched := false
	for _, c := range cands {
		for _, v := range []klapVersion{klapV2, klapV1} {
			ah := klapAuthHash(v, c.user, c.pass)
			if bytes.Equal(klapHandshake1Hash(v, local, remote, ah), serverHash) {
				auth, ver, matched = ah, v, true
				break
			}
		}
		if matched {
			break
		}
	}
	if !matched {
		return errKlapAuth
	}
	status, _, err = t.post(ctx, t.url("handshake2"), klapHandshake2Hash(ver, local, remote, auth))
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("handshake2 HTTP %d (credentials rejected?)", status)
	}
	t.version = ver
	t.session = newKlapSession(local, remote, auth)
	return nil
}

// request sends one SMART method and returns its "result" object.
func (t *klapTransport) request(ctx context.Context, method string, params any) (map[string]any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	body, err := json.Marshal(map[string]any{
		"method": method, "params": params,
		"request_time_milis": time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	payload, seq := t.session.encrypt(body)
	u := t.url("request") + "?" + url.Values{"seq": {fmt.Sprint(seq)}}.Encode()
	status, data, err := t.post(ctx, u, payload)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		t.session = nil
		return nil, fmt.Errorf("request HTTP %d", status)
	}
	plain, err := t.session.decrypt(data)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ErrorCode int            `json:"error_code"`
		Result    map[string]any `json:"result"`
	}
	if err := json.Unmarshal(plain, &resp); err != nil {
		return nil, err
	}
	if resp.ErrorCode != 0 {
		return nil, fmt.Errorf("smart error_code %d for %s", resp.ErrorCode, method)
	}
	return resp.Result, nil
}

type smartDevice struct {
	t     *klapTransport
	info  map[string]any
	power *float64
}

func (d *smartDevice) Update(ctx context.Context) error {
	info, err := d.t.request(ctx, "get_device_info", nil)
	if err != nil {
		return err
	}
	d.info = info
	d.power = nil
	if res, err := d.t.request(ctx, "get_current_power", nil); err == nil {
		if v, ok := toFloat(res["current_power"]); ok {
			d.power = &v
		}
	}
	return nil
}

func (d *smartDevice) set(ctx context.Context, on bool) error {
	_, err := d.t.request(ctx, "set_device_info", map[string]any{"device_on": on})
	return err
}
func (d *smartDevice) TurnOn(ctx context.Context) error  { return d.set(ctx, true) }
func (d *smartDevice) TurnOff(ctx context.Context) error { return d.set(ctx, false) }

func (d *smartDevice) Alias() string {
	nick, _ := d.info["nickname"].(string)
	if nick == "" {
		return ""
	}
	if b, err := base64.StdEncoding.DecodeString(nick); err == nil {
		return string(b)
	}
	return nick
}
func (d *smartDevice) Model() string { s, _ := d.info["model"].(string); return s }
func (d *smartDevice) DeviceType() string {
	typ := strings.ToUpper(fmt.Sprint(d.info["type"]))
	switch {
	case strings.Contains(typ, "PLUG"):
		return "Plug"
	case strings.Contains(typ, "BULB"):
		return "Bulb"
	case strings.Contains(typ, "STRIP"):
		return "Strip"
	}
	return "Unknown"
}
func (d *smartDevice) IsOn() bool       { b, _ := d.info["device_on"].(bool); return b }
func (d *smartDevice) PowerW() *float64 { return d.power }

// NativeBackend implements Backend with the legacy and KLAP transports.
type NativeBackend struct {
	// Timeout bounds each network operation (default 5s).
	Timeout time.Duration
	// BroadcastAddr is the discovery target (default 255.255.255.255).
	BroadcastAddr string
	// KlapPort overrides the HTTP port (default 80); used by tests.
	KlapPort int
	// LegacyProbe, when false, skips the legacy TCP 9999 attempt.
	SkipLegacy bool
}

// NewNativeBackend returns a backend with defaults.
func NewNativeBackend() *NativeBackend { return &NativeBackend{Timeout: 5 * time.Second} }

func (b *NativeBackend) timeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return 5 * time.Second
}

// Discover finds legacy devices by UDP broadcast. KLAP-only devices (UDP
// 20002 discovery) are not found this way; connect to them by IP instead.
func (b *NativeBackend) Discover(ctx context.Context, timeout time.Duration, _ *Credentials) (map[string]Device, error) {
	target := b.BroadcastAddr
	if target == "" {
		target = "255.255.255.255"
	}
	found, err := discoverLegacy(ctx, timeout, target)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Device, len(found))
	for h, d := range found {
		out[h] = d
	}
	return out, nil
}

// DiscoverSingle identifies the protocol: legacy TCP 9999 first (cheap), then
// KLAP over HTTP.
func (b *NativeBackend) DiscoverSingle(ctx context.Context, host string, creds *Credentials) (Device, error) {
	var legacyErr error
	if !b.SkipLegacy {
		d := &legacyDevice{host: host, timeout: b.timeout()}
		if legacyErr = d.Update(ctx); legacyErr == nil {
			return d, nil
		}
	}
	t := newKlapTransport(host, b.KlapPort, b.timeout())
	if err := t.handshake(ctx, creds); err != nil {
		if errors.Is(err, errKlapAuth) {
			return nil, err
		}
		if legacyErr != nil {
			return nil, fmt.Errorf("legacy: %v; http: %w", legacyErr, err)
		}
		return nil, err
	}
	return &smartDevice{t: t}, nil
}
