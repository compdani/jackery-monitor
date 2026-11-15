package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type cfFixture struct {
	key     *rsa.PrivateKey
	kid     string
	fetches atomic.Int32
	srv     *httptest.Server
}

func newCFFixture(t *testing.T) *cfFixture {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &cfFixture{key: k, kid: "kid-1"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		enc := base64.RawURLEncoding.EncodeToString
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": f.kid, "n": enc(k.N.Bytes()), "e": enc(big.NewInt(int64(k.E)).Bytes())}}})
	}))
	t.Cleanup(f.srv.Close)
	return f
}
func (f *cfFixture) sign(header, claims map[string]any) string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	input := enc(header) + "." + enc(claims)
	sum := sha256.Sum256([]byte(input))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func (f *cfFixture) claims(mod func(map[string]any)) map[string]any {
	c := map[string]any{"iss": "https://team.cloudflareaccess.com", "aud": []any{"AUD"}, "exp": float64(time.Now().Add(time.Hour).Unix()), "email": "Owner@Example.com"}
	if mod != nil {
		mod(c)
	}
	return c
}

// Ported from tests/test_cf_access.py.
func TestCFAccessVerification(t *testing.T) {
	f := newCFFixture(t)
	c := &CFAccess{Team: "team", Audience: "AUD", CertsURL: f.srv.URL}
	ctx := context.Background()
	hdr := map[string]any{"alg": "RS256", "kid": "kid-1"}
	if got := c.Verify(ctx, f.sign(hdr, f.claims(nil))); got != "owner@example.com" {
		t.Fatal(got)
	}
	for name, token := range map[string]string{
		"aud":       f.sign(hdr, f.claims(func(m map[string]any) { m["aud"] = "OTHER" })),
		"iss":       f.sign(hdr, f.claims(func(m map[string]any) { m["iss"] = "https://evil.cloudflareaccess.com" })),
		"expired":   f.sign(hdr, f.claims(func(m map[string]any) { m["exp"] = float64(time.Now().Add(-time.Minute).Unix()) })),
		"alg none":  f.sign(map[string]any{"alg": "none", "kid": "kid-1"}, f.claims(nil)),
		"no email":  f.sign(hdr, f.claims(func(m map[string]any) { delete(m, "email") })),
		"malformed": "a.b",
		"garbage":   "!!.!!.!!",
	} {
		if got := c.Verify(ctx, token); got != "" {
			t.Fatal(name, got)
		}
	}
	good := f.sign(hdr, f.claims(nil))
	parts := strings.Split(good, ".")
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"x@y","aud":"AUD","iss":"https://team.cloudflareaccess.com","exp":9999999999}`)) + "." + parts[2]
	if c.Verify(ctx, tampered) != "" {
		t.Fatal("bad signature accepted")
	}
	// Unknown kid triggers exactly one forced refetch.
	before := f.fetches.Load()
	if c.Verify(ctx, f.sign(map[string]any{"alg": "RS256", "kid": "rotated"}, f.claims(nil))) != "" || f.fetches.Load() != before+1 {
		t.Fatal("unknown kid", f.fetches.Load()-before)
	}
	c.Emails = []string{"someone@else.com"}
	if c.Verify(ctx, good) != "" {
		t.Fatal("allowlist ignored")
	}
	c.Emails = []string{"owner@example.com"}
	if c.Verify(ctx, good) == "" {
		t.Fatal("allowlisted email rejected")
	}
	for _, unconfigured := range []*CFAccess{{Team: "team"}, {Audience: "AUD"}, nil} {
		if unconfigured.Configured() || unconfigured.Verify(ctx, good) != "" {
			t.Fatal("unconfigured verifier active")
		}
	}
	// A failed refresh serves the stale key set.
	f.srv.Close()
	c.expires = time.Time{}
	if c.Verify(ctx, good) == "" {
		t.Fatal("stale JWKS not served")
	}
}

func TestCFAccessMintsSessionForExistingAccount(t *testing.T) {
	s, h := testServer(t)
	f := newCFFixture(t)
	s.CFAccess = &CFAccess{Team: "team", Audience: "AUD", CertsURL: f.srv.URL}
	token := f.sign(map[string]any{"alg": "RS256", "kid": "kid-1"}, f.claims(nil))
	call := func(path, assertion string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if assertion != "" {
			r.Header.Set(cfAccessHeader, assertion)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	// Access never creates an account: setup is still required locally.
	if w := call("/api/status", token); w.Code != 401 || !strings.Contains(w.Body.String(), "setup_required") {
		t.Fatal(w.Code, w.Body.String())
	}
	contractToken(t, h)
	if w := call("/api/status", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("/api/status", "bogus"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := call("/api/auth/me", token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "admin") {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()
	if len(cookie) == 0 || cookie[0].Name != "jackery_session" || !cookie[0].HttpOnly {
		t.Fatal(cookie)
	}
	r := httptest.NewRequest("GET", "/api/status", nil)
	r.AddCookie(cookie[0])
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}
