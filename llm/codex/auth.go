package codex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mxcd/aikido/llm"
)

// Protocol constants of the ChatGPT login, as used by the Codex CLI.
const (
	DefaultIssuer = "https://auth.openai.com"
	ClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	// deviceRedirectURI is fixed by OpenAI for the device-code exchange.
	deviceRedirectURI = "https://auth.openai.com/deviceauth/callback"
	// refreshSkew refreshes an access token this long before it expires.
	refreshSkew = 5 * time.Minute
	// deviceCodeTTL is how long a device code stays valid.
	deviceCodeTTL = 15 * time.Minute
)

// ErrReauthRequired means the login is gone for good (refresh token reused,
// expired or revoked): the user has to log in again. Retrying cannot help.
var ErrReauthRequired = errors.New("aikido/codex: ChatGPT login expired or revoked, log in again")

// ErrDeviceLoginDisabled means the issuer does not offer device-code login
// (it answered 404 to the user-code request).
var ErrDeviceLoginDisabled = errors.New("aikido/codex: device-code login is not enabled")

// Tokens is one ChatGPT login. RefreshToken rotates on every refresh and is
// single-use: the caller must persist what OnRefresh hands over, and must not
// share it with another process (a replayed refresh token revokes the login).
type Tokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	// AccountID and PlanType come from the access token's claims.
	AccountID string
	PlanType  string
	// ExpiresAt is the access token's exp claim.
	ExpiresAt time.Time
}

// AuthOptions configure the login endpoints. Nil or zero fields use defaults.
type AuthOptions struct {
	// Issuer overrides DefaultIssuer (tests).
	Issuer string
	// HTTPClient overrides a 30 s timeout client.
	HTTPClient *http.Client
}

func (o *AuthOptions) issuer() string {
	if o != nil && o.Issuer != "" {
		return strings.TrimRight(o.Issuer, "/")
	}
	return DefaultIssuer
}

func (o *AuthOptions) client() *http.Client {
	if o != nil && o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// TokensFromJWT builds Tokens from a token response, reading account, plan and
// expiry from the access token. The signature is not verified: the token only
// travels between OpenAI and the caller.
func TokensFromJWT(accessToken, refreshToken, idToken string) (Tokens, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return Tokens{}, errors.New("aikido/codex: access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return Tokens{}, fmt.Errorf("aikido/codex: access token payload: %w", err)
	}
	var claims struct {
		Exp  float64 `json:"exp"`
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
			PlanType  string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Tokens{}, fmt.Errorf("aikido/codex: access token claims: %w", err)
	}
	return Tokens{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IDToken:      idToken,
		AccountID:    claims.Auth.AccountID,
		PlanType:     claims.Auth.PlanType,
		ExpiresAt:    time.Unix(int64(claims.Exp), 0),
	}, nil
}

// ParseAuthJSON reads a Codex CLI ~/.codex/auth.json. Only ChatGPT logins with
// an unexpired access token are accepted. Note the refresh token is then
// shared with that CLI: whichever side refreshes first logs the other out.
func ParseAuthJSON(raw []byte) (Tokens, error) {
	var f struct {
		AuthMode string `json:"auth_mode"`
		Tokens   *struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return Tokens{}, fmt.Errorf("aikido/codex: auth.json: %w", err)
	}
	if f.AuthMode != "chatgpt" || f.Tokens == nil || f.Tokens.AccessToken == "" || f.Tokens.RefreshToken == "" {
		return Tokens{}, errors.New("aikido/codex: auth.json holds no ChatGPT login")
	}
	t, err := TokensFromJWT(f.Tokens.AccessToken, f.Tokens.RefreshToken, f.Tokens.IDToken)
	if err != nil {
		return Tokens{}, err
	}
	if t.AccountID == "" {
		t.AccountID = f.Tokens.AccountID
	}
	if !t.ExpiresAt.After(time.Now()) {
		return Tokens{}, errors.New("aikido/codex: auth.json access token has expired")
	}
	return t, nil
}

// DeviceCode is a started device login: show VerificationURL and UserCode to
// the user, then call PollDeviceLogin.
type DeviceCode struct {
	DeviceAuthID    string
	UserCode        string
	VerificationURL string
	Interval        time.Duration
	ExpiresAt       time.Time
}

// StartDeviceLogin asks the issuer for a device code.
func StartDeviceLogin(ctx context.Context, o *AuthOptions) (DeviceCode, error) {
	var resp struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		UserCodeAlt  string          `json:"usercode"`
		Interval     json.RawMessage `json:"interval"` // a string on the wire, a number tolerated
	}
	status, body, err := postJSON(ctx, o, "/api/accounts/deviceauth/usercode", map[string]string{"client_id": ClientID})
	if err != nil {
		return DeviceCode{}, err
	}
	switch {
	case status == http.StatusNotFound:
		return DeviceCode{}, ErrDeviceLoginDisabled
	case status != http.StatusOK:
		return DeviceCode{}, fmt.Errorf("aikido/codex: device code: status %d: %w", status, statusSentinel(status))
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return DeviceCode{}, fmt.Errorf("aikido/codex: device code response: %w", err)
	}
	if resp.UserCode == "" {
		resp.UserCode = resp.UserCodeAlt
	}
	if resp.DeviceAuthID == "" || resp.UserCode == "" {
		return DeviceCode{}, errors.New("aikido/codex: device code response incomplete")
	}
	seconds, _ := strconv.Atoi(strings.Trim(string(resp.Interval), `" `))
	return DeviceCode{
		DeviceAuthID:    resp.DeviceAuthID,
		UserCode:        resp.UserCode,
		VerificationURL: o.issuer() + "/codex/device",
		Interval:        time.Duration(max(seconds, 3)) * time.Second,
		ExpiresAt:       time.Now().Add(deviceCodeTTL),
	}, nil
}

// PollDeviceLogin waits until the user confirmed the code (or it expired, or
// ctx ended), then exchanges it for tokens.
func PollDeviceLogin(ctx context.Context, o *AuthOptions, dc DeviceCode) (Tokens, error) {
	for {
		status, body, err := postJSON(ctx, o, "/api/accounts/deviceauth/token",
			map[string]string{"device_auth_id": dc.DeviceAuthID, "user_code": dc.UserCode})
		if err != nil {
			return Tokens{}, err
		}
		switch status {
		case http.StatusOK:
			var grant struct {
				AuthorizationCode string `json:"authorization_code"`
				CodeVerifier      string `json:"code_verifier"`
			}
			if err := json.Unmarshal(body, &grant); err != nil || grant.AuthorizationCode == "" {
				return Tokens{}, errors.New("aikido/codex: device token response incomplete")
			}
			return exchange(ctx, o, url.Values{
				"grant_type":    {"authorization_code"},
				"client_id":     {ClientID},
				"code":          {grant.AuthorizationCode},
				"redirect_uri":  {deviceRedirectURI},
				"code_verifier": {grant.CodeVerifier},
			}, "")
		case http.StatusForbidden, http.StatusNotFound:
			// not confirmed yet
		default:
			return Tokens{}, fmt.Errorf("aikido/codex: device login: status %d: %w", status, statusSentinel(status))
		}
		if time.Now().Add(dc.Interval).After(dc.ExpiresAt) {
			return Tokens{}, errors.New("aikido/codex: device code expired before it was confirmed")
		}
		select {
		case <-ctx.Done():
			return Tokens{}, ctx.Err()
		case <-time.After(dc.Interval):
		}
	}
}

// TokenSource hands out a valid access token, refreshing it when it is about
// to expire. One TokenSource per login and process: the mutex keeps exactly
// one refresh in flight, since a second refresh with the same (rotated)
// refresh token would revoke the login.
type TokenSource struct {
	mu        sync.Mutex
	tok       Tokens
	opts      *AuthOptions
	onRefresh func(context.Context, Tokens) error
}

// NewTokenSource wraps a login. onRefresh (may be nil) receives every rotated
// login and should persist it; aikido never writes tokens anywhere.
func NewTokenSource(t Tokens, onRefresh func(context.Context, Tokens) error, o *AuthOptions) *TokenSource {
	return &TokenSource{tok: t, opts: o, onRefresh: onRefresh}
}

// Token returns a login whose access token is valid for at least a few more
// minutes. If persisting a refreshed login fails the error is returned, but the
// new login is kept in memory, so the next call succeeds without refreshing.
func (s *TokenSource) Token(ctx context.Context) (Tokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Until(s.tok.ExpiresAt) > refreshSkew {
		return s.tok, nil
	}
	fresh, err := exchange(ctx, s.opts, nil, s.tok.RefreshToken)
	if err != nil {
		return Tokens{}, err
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = s.tok.RefreshToken
	}
	if fresh.IDToken == "" {
		fresh.IDToken = s.tok.IDToken
	}
	if fresh.AccountID == "" {
		fresh.AccountID = s.tok.AccountID
	}
	s.tok = fresh
	if s.onRefresh != nil {
		if err := s.onRefresh(ctx, fresh); err != nil {
			return Tokens{}, fmt.Errorf("aikido/codex: persist refreshed login: %w", err)
		}
	}
	return fresh, nil
}

// permanentRefreshErrors are the token-endpoint codes after which only a new
// login helps.
var permanentRefreshErrors = []string{"refresh_token_reused", "refresh_token_expired", "refresh_token_invalidated", "invalid_grant"}

// exchange calls /oauth/token: with form for an authorization code, or a
// refresh when refreshToken is set.
func exchange(ctx context.Context, o *AuthOptions, form url.Values, refreshToken string) (Tokens, error) {
	var (
		status int
		body   []byte
		err    error
	)
	if refreshToken != "" {
		status, body, err = postJSON(ctx, o, "/oauth/token", map[string]string{
			"client_id": ClientID, "grant_type": "refresh_token", "refresh_token": refreshToken,
		})
	} else {
		status, body, err = post(ctx, o, "/oauth/token", "application/x-www-form-urlencoded", []byte(form.Encode()))
	}
	if err != nil {
		return Tokens{}, err
	}
	if status != http.StatusOK {
		if refreshToken != "" && status < 500 && status != http.StatusTooManyRequests {
			if status == http.StatusUnauthorized {
				return Tokens{}, ErrReauthRequired
			}
			for _, code := range permanentRefreshErrors {
				if bytes.Contains(body, []byte(code)) {
					return Tokens{}, fmt.Errorf("%s: %w", code, ErrReauthRequired)
				}
			}
		}
		return Tokens{}, fmt.Errorf("aikido/codex: token endpoint: status %d: %w", status, statusSentinel(status))
	}
	var t struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return Tokens{}, errors.New("aikido/codex: token response without access token")
	}
	return TokensFromJWT(t.AccessToken, t.RefreshToken, t.IDToken)
}

func postJSON(ctx context.Context, o *AuthOptions, path string, v any) (int, []byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0, nil, err
	}
	return post(ctx, o, path, "application/json", raw)
}

func post(ctx context.Context, o *AuthOptions, path, contentType string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.issuer()+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := o.client().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("aikido/codex: %s: %v: %w", path, err, llm.ErrServerError)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, err
}
