package tesla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPDoer is the subset of *http.Client the package uses.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Tokens are the owner's OAuth tokens. Tesla refresh tokens are single use:
// persist the Tokens returned by Refresh before using the new access token.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// ExpiresAt is the access token's expiry, Unix seconds.
	ExpiresAt int64  `json:"expires_at"`
	Scope     string `json:"scope,omitempty"`
	// RefreshedAt is when the refresh token was issued, Unix seconds. Tesla
	// expires refresh tokens after three months.
	RefreshedAt int64 `json:"refreshed_at"`
}

// Valid reports whether the access token is usable for at least margin.
func (t Tokens) Valid(now time.Time, margin time.Duration) bool {
	return t.AccessToken != "" && now.Add(margin).Unix() < t.ExpiresAt
}

// AuthCodeURL returns the authorize URL for the owner to sign in.
func AuthCodeURL(c Config, state string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", c.RedirectURI)
	q.Set("scope", UserScopes)
	q.Set("state", state)
	q.Set("prompt_missing_scopes", "true")
	return AuthorizeURL + "?" + q.Encode()
}

// TokenError is a non-2xx response from the token endpoint.
type TokenError struct {
	Status int
	Code   string
	Detail string
}

func (e *TokenError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("tesla token endpoint: %d %s: %s", e.Status, e.Code, e.Detail)
	}
	return fmt.Sprintf("tesla token endpoint: %d %s", e.Status, e.Detail)
}

// LoginRequired reports whether the owner must sign in again.
func (e *TokenError) LoginRequired() bool {
	return e.Status == 401 || e.Code == "login_required" || e.Code == "invalid_grant"
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func postToken(ctx context.Context, h HTTPDoer, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := h.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var tr tokenResponse
	_ = json.Unmarshal(raw, &tr)
	if resp.StatusCode/100 != 2 || tr.AccessToken == "" {
		detail := tr.Description
		if detail == "" {
			detail = strings.TrimSpace(string(raw))
			if len(detail) > 300 {
				detail = detail[:300]
			}
		}
		return nil, &TokenError{Status: resp.StatusCode, Code: tr.Error, Detail: detail}
	}
	return &tr, nil
}

func (tr *tokenResponse) tokens(now time.Time, previousRefresh string) Tokens {
	t := Tokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    now.Unix() + tr.ExpiresIn,
		Scope:        tr.Scope,
		RefreshedAt:  now.Unix(),
	}
	if t.RefreshToken == "" {
		t.RefreshToken = previousRefresh
	}
	return t
}

// ExchangeCode trades an authorization code for owner tokens.
func ExchangeCode(ctx context.Context, h HTTPDoer, c Config, code string, now time.Time) (Tokens, error) {
	if c.ClientSecret == "" {
		return Tokens{}, errors.New("client_secret required")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	form.Set("code", code)
	form.Set("audience", c.Audience())
	form.Set("redirect_uri", c.RedirectURI)
	tr, err := postToken(ctx, h, form)
	if err != nil {
		return Tokens{}, err
	}
	return tr.tokens(now, ""), nil
}

// Refresh exchanges the single-use refresh token for new tokens.
func Refresh(ctx context.Context, h HTTPDoer, c Config, t Tokens, now time.Time) (Tokens, error) {
	if t.RefreshToken == "" {
		return Tokens{}, &TokenError{Status: 401, Code: "login_required", Detail: "no refresh token"}
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", c.ClientID)
	form.Set("refresh_token", t.RefreshToken)
	tr, err := postToken(ctx, h, form)
	if err != nil {
		return Tokens{}, err
	}
	return tr.tokens(now, t.RefreshToken), nil
}

// PartnerToken gets a client_credentials token for partner endpoints.
func PartnerToken(ctx context.Context, h HTTPDoer, c Config) (string, error) {
	if c.ClientSecret == "" {
		return "", errors.New("client_secret required")
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	form.Set("scope", PartnerScopes)
	form.Set("audience", c.Audience())
	tr, err := postToken(ctx, h, form)
	if err != nil {
		return "", err
	}
	return tr.AccessToken, nil
}
