package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJWT builds an unsigned access token with the claims the package reads.
func fakeJWT(t *testing.T, exp time.Time, account string) string {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{
		"exp":                         exp.Unix(),
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account, "chatgpt_plan_type": "plus"},
	})
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(claims) + ".sig"
}

func TestTokenSource_RefreshRotatesOnceAndPersists(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/oauth/token" || body["grant_type"] != "refresh_token" || body["refresh_token"] != "rt.old" || body["client_id"] != ClientID {
			t.Errorf("unexpected refresh %s %v", r.URL.Path, body)
		}
		calls.Add(1)
		time.Sleep(20 * time.Millisecond) // let the other callers pile up on the mutex
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": fakeJWT(t, time.Now().Add(240*time.Hour), "acct"), "refresh_token": "rt.new"})
	}))
	defer srv.Close()

	var persisted []Tokens
	var mu sync.Mutex
	src := NewTokenSource(Tokens{AccessToken: "old", RefreshToken: "rt.old", ExpiresAt: time.Now().Add(time.Minute)},
		func(_ context.Context, t Tokens) error {
			mu.Lock()
			persisted = append(persisted, t)
			mu.Unlock()
			return nil
		},
		&AuthOptions{Issuer: srv.URL})

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := src.Token(context.Background())
			if err != nil || tok.RefreshToken != "rt.new" || tok.AccountID != "acct" || tok.PlanType != "plus" {
				t.Errorf("Token = %+v, %v", tok, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || len(persisted) != 1 || persisted[0].RefreshToken != "rt.new" {
		t.Errorf("refreshes = %d, persisted = %d, want 1 each", calls.Load(), len(persisted))
	}
}

func TestTokenSource_PermanentErrorsNeedReauth(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status int
		body   string
		reauth bool
	}{
		{400, `{"error":{"code":"refresh_token_reused"}}`, true},
		{400, `{"error":"invalid_grant"}`, true},
		{401, `{}`, true},
		{503, `refresh_token_reused`, false}, // a 5xx is never taken as permanent
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		src := NewTokenSource(Tokens{RefreshToken: "rt.x"}, nil, &AuthOptions{Issuer: srv.URL})
		_, err := src.Token(context.Background())
		if errors.Is(err, ErrReauthRequired) != tc.reauth {
			t.Errorf("%d %s: err = %v, reauth want %v", tc.status, tc.body, err, tc.reauth)
		}
		srv.Close()
	}
}

func TestDeviceLogin_PendingThenSuccess(t *testing.T) {
	t.Parallel()
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			_, _ = io.WriteString(w, `{"device_auth_id":"dev1","user_code":"K7QM-4XPD","interval":"5"}`)
		case "/api/accounts/deviceauth/token":
			if polls.Add(1) < 3 {
				w.WriteHeader(http.StatusForbidden) // not confirmed yet
				return
			}
			_, _ = io.WriteString(w, `{"authorization_code":"code1","code_challenge":"ch","code_verifier":"ver1"}`)
		case "/oauth/token":
			raw, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(raw))
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || form.Get("grant_type") != "authorization_code" ||
				form.Get("code") != "code1" || form.Get("code_verifier") != "ver1" || form.Get("redirect_uri") != deviceRedirectURI {
				t.Errorf("exchange form = %v", form)
			}
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rt.1","id_token":"id"}`, fakeJWT(t, time.Now().Add(time.Hour), "acct"))
		}
	}))
	defer srv.Close()
	o := &AuthOptions{Issuer: srv.URL}

	dc, err := StartDeviceLogin(context.Background(), o)
	if err != nil || dc.UserCode != "K7QM-4XPD" || dc.Interval != 5*time.Second || dc.VerificationURL != srv.URL+"/codex/device" {
		t.Fatalf("StartDeviceLogin = %+v, %v", dc, err)
	}
	dc.Interval = 10 * time.Millisecond
	tok, err := PollDeviceLogin(context.Background(), o, dc)
	if err != nil || tok.RefreshToken != "rt.1" || tok.AccountID != "acct" || polls.Load() != 3 {
		t.Fatalf("PollDeviceLogin = %+v, %v after %d polls", tok, err, polls.Load())
	}
}

func TestParseAuthJSON(t *testing.T) {
	t.Parallel()
	valid, expired := fakeJWT(t, time.Now().Add(time.Hour), ""), fakeJWT(t, time.Now().Add(-time.Hour), "")
	file := func(mode, access string) []byte {
		return fmt.Appendf(nil, `{"auth_mode":%q,"tokens":{"access_token":%q,"refresh_token":"rt.1","account_id":"acct-file"}}`, mode, access)
	}
	for name, tc := range map[string]struct {
		raw []byte
		ok  bool
	}{
		"chatgpt login": {file("chatgpt", valid), true},
		"api key mode":  {file("apikey", valid), false},
		"expired":       {file("chatgpt", expired), false},
		"not json":      {[]byte("nope"), false},
	} {
		tok, err := ParseAuthJSON(tc.raw)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", name, err)
		}
		if tc.ok && tok.AccountID != "acct-file" {
			t.Errorf("%s: account id %q, want the file's when the claim is empty", name, tok.AccountID)
		}
	}
}

// A caller that gives up mid-refresh must not lose the rotated login.
func TestTokenSourceRefreshSurvivesCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": fakeJWT(t, time.Now().Add(time.Hour), "acct"), "refresh_token": "rt.new"})
	}))
	defer srv.Close()
	var saved Tokens
	persisted := make(chan struct{})
	ts := NewTokenSource(Tokens{AccessToken: "old", RefreshToken: "rt.old", ExpiresAt: time.Now()}, func(_ context.Context, tok Tokens) error {
		saved = tok
		close(persisted)
		return nil
	}, &AuthOptions{Issuer: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, _ = ts.Token(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(release)
	select {
	case <-persisted:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh was dropped with the cancelled caller")
	}
	if saved.RefreshToken != "rt.new" {
		t.Fatalf("persisted %q, want rt.new", saved.RefreshToken)
	}
}
