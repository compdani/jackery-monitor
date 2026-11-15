package server

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// CFAccess ports cf_access.py: RS256 Cloudflare Access assertions verified
// against the team JWKS, with pinned alg, issuer, audience, expiry (30 s
// leeway) and an optional email allowlist. Inert until team and audience
// are configured.
type CFAccess struct {
	Team, Audience string
	Emails         []string
	HTTP           *http.Client
	// CertsURL overrides the JWKS URL (tests); Issuer stays the team domain.
	CertsURL string
	Now      func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	expires time.Time
}

const cfAccessHeader = "Cf-Access-Jwt-Assertion"

// CFAccessFromEnv reads JACKERY_CF_ACCESS_{TEAM,AUD,EMAILS}.
func CFAccessFromEnv() *CFAccess {
	c := &CFAccess{Team: strings.TrimSpace(os.Getenv("JACKERY_CF_ACCESS_TEAM")), Audience: strings.TrimSpace(os.Getenv("JACKERY_CF_ACCESS_AUD"))}
	for _, e := range strings.Split(os.Getenv("JACKERY_CF_ACCESS_EMAILS"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			c.Emails = append(c.Emails, e)
		}
	}
	return c
}
func (c *CFAccess) Configured() bool { return c != nil && c.Team != "" && c.Audience != "" }
func (c *CFAccess) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
func (c *CFAccess) issuer() string { return "https://" + c.Team + ".cloudflareaccess.com" }
func b64url(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// jwks returns cached keys (one hour); a failed fetch serves the stale set.
func (c *CFAccess) jwks(ctx context.Context, force bool) map[string]*rsa.PublicKey {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.keys != nil && c.now().Before(c.expires) {
		return c.keys
	}
	url := c.CertsURL
	if url == "" {
		url = c.issuer() + "/cdn-cgi/access/certs"
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	fetch := func() (map[string]*rsa.PublicKey, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, io.ErrUnexpectedEOF
		}
		var doc struct {
			Keys []struct{ Kty, Kid, N, E string }
		}
		if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
			return nil, err
		}
		keys := map[string]*rsa.PublicKey{}
		for _, k := range doc.Keys {
			if k.Kty != "RSA" || k.Kid == "" {
				continue
			}
			n, err1 := b64url(k.N)
			e, err2 := b64url(k.E)
			if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 8 {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		}
		return keys, nil
	}
	keys, err := fetch()
	if err != nil {
		return c.keys
	}
	c.keys, c.expires = keys, c.now().Add(time.Hour)
	return keys
}

// Verify returns the lowercased email for a valid assertion, else "".
func (c *CFAccess) Verify(ctx context.Context, token string) string {
	if !c.Configured() || token == "" {
		return ""
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	var header struct{ Alg, Kid string }
	raw, err := b64url(parts[0])
	if err != nil || json.Unmarshal(raw, &header) != nil || header.Alg != "RS256" || header.Kid == "" {
		return ""
	}
	sig, err := b64url(parts[2])
	if err != nil {
		return ""
	}
	key := c.jwks(ctx, false)[header.Kid]
	if key == nil {
		key = c.jwks(ctx, true)[header.Kid] // key rotation: refetch once
	}
	if key == nil {
		return ""
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
		return ""
	}
	raw, err = b64url(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp+30 < float64(c.now().Unix()) {
		return ""
	}
	if claims["iss"] != c.issuer() {
		return ""
	}
	audOK := false
	switch a := claims["aud"].(type) {
	case string:
		audOK = a == c.Audience
	case []any:
		for _, v := range a {
			audOK = audOK || v == c.Audience
		}
	}
	if !audOK {
		return ""
	}
	email, _ := claims["email"].(string)
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ""
	}
	if len(c.Emails) > 0 {
		for _, e := range c.Emails {
			if e == email {
				return email
			}
		}
		return ""
	}
	return email
}

// cfAccessSession mints an app session for a verified Access identity so
// follow-ups and the WebSocket use the normal session path. The account is
// the user whose username matches the email, else the first account; no
// account is ever created (setup still has to happen locally).
func (s *Server) cfAccessSession(w http.ResponseWriter, r *http.Request) {
	if !s.CFAccess.Configured() || r.Header.Get(cfAccessHeader) == "" || s.user(r) != nil {
		return
	}
	email := s.CFAccess.Verify(r.Context(), r.Header.Get(cfAccessHeader))
	if email == "" {
		return
	}
	var user *core.Record
	if users, err := s.Store.App.FindAllRecords("users"); err == nil {
		for _, u := range users {
			if strings.EqualFold(u.GetString("username"), email) {
				user = u
				break
			}
		}
		if user == nil && len(users) > 0 {
			user = users[0]
			for _, u := range users[1:] {
				if u.GetDateTime("created").Time().Before(user.GetDateTime("created").Time()) {
					user = u
				}
			}
		}
	}
	if user == nil {
		return
	}
	t, err := user.NewAuthToken()
	if err != nil {
		return
	}
	secure := strings.ToLower(os.Getenv("JACKERY_COOKIE_SECURE"))
	http.SetCookie(w, &http.Cookie{Name: "jackery_session", Value: t, Path: "/", MaxAge: 30 * 86400, HttpOnly: true, Secure: secure == "1" || secure == "true" || secure == "yes" || secure == "on", SameSite: http.SameSiteLaxMode})
	r.Header.Set("Authorization", "Bearer "+t)
}
