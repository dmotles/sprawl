package hub

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/hub/store"
)

const testLoginToken = "correct-horse-battery-staple"

// noRedirectClient returns an httptest server's client that does NOT follow
// redirects, so a 303 → /app/ leaves the Set-Cookie header observable.
func noRedirectClient(ts *httptest.Server) *http.Client {
	c := ts.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

// newBrowserAuthServer builds a server with browser login ENABLED over a seeded
// memStore, and returns the test server + the store.
func newBrowserAuthServer(t *testing.T) (*httptest.Server, store.Store) {
	t.Helper()
	st := newMemStore(t)
	if err := st.EnsureUser(context.Background(), MVPUserID); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	ba := NewBrowserAuth(st, MVPUserID, testLoginToken, testCookieKey(), DefaultSessionTTL, nil)
	if ba == nil {
		t.Fatal("NewBrowserAuth returned nil for a valid config")
	}
	srv := NewServer(HubConfig{Store: st, Login: ba})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

// postLogin POSTs token in the body (never the URL) to /login.
func postLogin(t *testing.T, ts *httptest.Server, token string) *http.Response {
	t.Helper()
	form := url.Values{"token": {token}}
	resp, err := noRedirectClient(ts).Post(ts.URL+"/login",
		"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	return resp
}

// sessionCookie returns the sprawl_hub_session cookie from a response, or nil.
func sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

func TestLogin_WrongToken_401_NoCookie(t *testing.T) {
	ts, _ := newBrowserAuthServer(t)
	resp := postLogin(t, ts, "wrong-token")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if c := sessionCookie(resp); c != nil {
		t.Errorf("a session cookie was set on a rejected login: %+v", c)
	}
	// A rejected login must NOT take the success (redirect-to-/app/) path — no
	// Location header. With no cookie minted there is no session id to back a
	// login_sessions record, so this is the observable "no record" guarantee.
	if loc := resp.Header.Get("Location"); loc != "" {
		t.Errorf("rejected login redirected to %q; must not reach the success path", loc)
	}
}

func TestLogin_CorrectToken_SetsSignedCookieAndRecord(t *testing.T) {
	ts, st := newBrowserAuthServer(t)
	resp := postLogin(t, ts, testLoginToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 303 or 200", resp.StatusCode)
	}
	c := sessionCookie(resp)
	if c == nil {
		t.Fatal("no session cookie set on a successful login")
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie attrs: HttpOnly=%v Secure=%v SameSite=%v, want all set + Strict",
			c.HttpOnly, c.Secure, c.SameSite)
	}
	// The cookie must verify and its id must back a server-side record.
	id, ok := verifySession(testCookieKey(), c.Value)
	if !ok {
		t.Fatalf("session cookie did not verify: %q", c.Value)
	}
	rec, err := st.GetLoginSession(context.Background(), store.LoginSessionID(id))
	if err != nil {
		t.Fatalf("no login_sessions record for the minted cookie: %v", err)
	}
	if rec.UserID != MVPUserID {
		t.Errorf("record UserID = %q, want %q", rec.UserID, MVPUserID)
	}
}

func TestLogin_NoTokenLeakInResponse(t *testing.T) {
	ts, _ := newBrowserAuthServer(t)
	resp := postLogin(t, ts, testLoginToken)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), testLoginToken) {
		t.Error("login token leaked into the response body")
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			if strings.Contains(v, testLoginToken) {
				t.Errorf("login token leaked into response header %s: %q", k, v)
			}
		}
	}
}

func TestLogin_GET_ServesForm(t *testing.T) {
	ts, _ := newBrowserAuthServer(t)
	resp, err := ts.Client().Get(ts.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestLogout_ClearsCookieAndRecord(t *testing.T) {
	ts, st := newBrowserAuthServer(t)
	// Establish a session.
	loginResp := postLogin(t, ts, testLoginToken)
	loginResp.Body.Close()
	c := sessionCookie(loginResp)
	if c == nil {
		t.Fatal("login did not set a cookie")
	}
	id, _ := verifySession(testCookieKey(), c.Value)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/logout", nil)
	req.AddCookie(c)
	resp, err := noRedirectClient(ts).Do(req)
	if err != nil {
		t.Fatalf("POST /logout: %v", err)
	}
	defer resp.Body.Close()

	cleared := sessionCookie(resp)
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("logout did not clear the cookie: %+v", cleared)
	}
	if _, err := st.GetLoginSession(context.Background(), store.LoginSessionID(id)); err == nil {
		t.Error("login_sessions record survived logout (should be deleted)")
	}
}

func TestLogout_NoCookie_StillClears(t *testing.T) {
	ts, _ := newBrowserAuthServer(t)
	// Logout with no session cookie must be idempotent: no panic/500, and it
	// still emits a clearing cookie.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/logout", nil)
	resp, err := noRedirectClient(ts).Do(req)
	if err != nil {
		t.Fatalf("POST /logout (no cookie): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		t.Errorf("logout without a cookie returned %d, want < 500", resp.StatusCode)
	}
	if c := sessionCookie(resp); c == nil || c.MaxAge >= 0 {
		t.Errorf("logout without a cookie did not emit a clearing cookie: %+v", c)
	}
}

func TestNewBrowserAuth_DisabledPaths(t *testing.T) {
	st := newMemStore(t)
	_ = st.EnsureUser(context.Background(), MVPUserID)
	key := testCookieKey()

	if NewBrowserAuth(st, MVPUserID, "", key, DefaultSessionTTL, nil) != nil {
		t.Error("NewBrowserAuth(empty token) = non-nil, want nil (disabled)")
	}
	if NewBrowserAuth(st, MVPUserID, "tok", key[:16], DefaultSessionTTL, nil) != nil {
		t.Error("NewBrowserAuth(key < 32 bytes) = non-nil, want nil (disabled)")
	}
	if NewBrowserAuth(nil, MVPUserID, "tok", key, DefaultSessionTTL, nil) != nil {
		t.Error("NewBrowserAuth(nil store) = non-nil, want nil (disabled)")
	}
	if NewBrowserAuth(st, MVPUserID, "tok", key, DefaultSessionTTL, nil) == nil {
		t.Error("NewBrowserAuth(valid) = nil, want enabled")
	}
}

func TestLogin_Disabled_503(t *testing.T) {
	st := newMemStore(t)
	_ = st.EnsureUser(context.Background(), MVPUserID)
	srv := NewServer(HubConfig{Store: st}) // Login nil → browser login disabled
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/login"},
		{http.MethodPost, "/login"},
		{http.MethodPost, "/logout"},
	} {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503 (disabled)", tc.method, tc.path, resp.StatusCode)
		}
	}
}

// --- ResolveBrowserAuth env matrix (T6) ---

func b64key(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestResolveBrowserAuth_Matrix(t *testing.T) {
	st := newMemStore(t)
	_ = st.EnsureUser(context.Background(), MVPUserID)

	cases := []struct {
		name    string
		token   string
		key     string
		wantNil bool
	}{
		{"enabled", "tok", b64key(32), false},
		{"token-unset", "", b64key(32), true},
		{"key-unset", "tok", "", true},
		{"key-too-short", "tok", b64key(16), true},
		{"key-bad-base64", "tok", "not@@base64", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string {
				switch k {
				case EnvHubLoginToken:
					return tc.token
				case EnvHubCookieKey:
					return tc.key
				}
				return ""
			}
			ba := ResolveBrowserAuth(getenv, st, MVPUserID, nil)
			if tc.wantNil && ba != nil {
				t.Errorf("ResolveBrowserAuth = non-nil, want nil (disabled)")
			}
			if !tc.wantNil && ba == nil {
				t.Errorf("ResolveBrowserAuth = nil, want enabled")
			}
		})
	}
}
