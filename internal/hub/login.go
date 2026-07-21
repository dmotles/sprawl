package hub

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/dmotles/sprawl/internal/hub/gen/hub/v1/hubv1connect"
	"github.com/dmotles/sprawl/internal/hub/store"
)

// Browser-login configuration env vars. Both are resolved at hubd boot via the
// injected getenv, never compiled in and with no default (public-repo hygiene,
// docs 04 §1.1). Naming maps the design doc's logical HUB_LOGIN_TOKEN /
// cookie-signing-key onto this repo's SPRAWL_HUB_ env-var prefix convention
// (QUM-878; matches SPRAWL_HUB_DSN / SPRAWL_HUB_SECRET_URL).
//
// SPRAWL_HUB_LOGIN_TOKEN: the shared login token the operator types into the
// login page (any non-empty string).
// SPRAWL_HUB_COOKIE_KEY: the HMAC cookie-signing key as STANDARD base64
// (e.g. `openssl rand -base64 32`), decoding to >= 32 bytes. A URL-safe-base64
// value fails to decode and cleanly disables browser login (logged at warn).
const (
	EnvHubLoginToken = "SPRAWL_HUB_LOGIN_TOKEN" //nolint:gosec // env var NAME, not a credential value
	EnvHubCookieKey  = "SPRAWL_HUB_COOKIE_KEY"  //nolint:gosec // env var NAME, not a credential value
)

// DefaultSessionTTL bounds a browser login session's lifetime. One TTL for the
// MVP (docs 04 §6, Open Questions E5/F1 kept simple).
const DefaultSessionTTL = 12 * time.Hour

// minCookieKeyBytes is the minimum decoded length of the HMAC cookie-signing
// key. 32 bytes matches SHA-256's block/output sizing and the localsecrets key.
const minCookieKeyBytes = 32

// BrowserAuth implements the browser half of hub auth (docs 04 §1/§6): a /login
// page trades the single configured login token for a signed, server-backed
// httpOnly session cookie. A nil *BrowserAuth means browser login is disabled;
// host bearer auth is a separate, always-on path.
type BrowserAuth struct {
	store      store.Store
	uid        store.UserID
	loginToken string
	cookieKey  []byte
	ttl        time.Duration
	log        *slog.Logger
}

// NewBrowserAuth constructs the browser-auth component, or returns nil when it
// cannot be safely enabled: no login token, no usable store, or a cookie key
// shorter than minCookieKeyBytes. A nil logger uses a discard logger.
func NewBrowserAuth(st store.Store, uid store.UserID, loginToken string, cookieKey []byte, ttl time.Duration, log *slog.Logger) *BrowserAuth {
	if st == nil || loginToken == "" || len(cookieKey) < minCookieKeyBytes {
		return nil
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return &BrowserAuth{
		store:      st,
		uid:        uid,
		loginToken: loginToken,
		cookieKey:  cookieKey,
		ttl:        ttl,
		log:        log,
	}
}

// ResolveBrowserAuth builds a BrowserAuth from the environment (via the injected
// getenv), or returns nil to disable browser login. The login token and the
// base64-encoded cookie-signing key resolve from SPRAWL_HUB_LOGIN_TOKEN /
// SPRAWL_HUB_COOKIE_KEY. A real deploy injects these from its secret store into
// the process env (the SecretResolver seam is Encrypt/Decrypt-only, so it can't
// hand back a plaintext login token — QUM-878 Decision 2). NEVER logs the token
// or key bytes, only the enabled/disabled reason.
func ResolveBrowserAuth(getenv func(string) string, st store.Store, uid store.UserID, log *slog.Logger) *BrowserAuth {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	log = log.With("component", "hub-login")

	token := getenv(EnvHubLoginToken)
	keyB64 := getenv(EnvHubCookieKey)
	if token == "" || keyB64 == "" {
		log.Info("browser login disabled: SPRAWL_HUB_LOGIN_TOKEN and/or SPRAWL_HUB_COOKIE_KEY not set (host bearer auth unaffected)")
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		log.Warn("browser login disabled: SPRAWL_HUB_COOKIE_KEY is not valid base64")
		return nil
	}
	if len(key) < minCookieKeyBytes {
		log.Warn("browser login disabled: SPRAWL_HUB_COOKIE_KEY too short", "min_bytes", minCookieKeyBytes)
		return nil
	}
	ba := NewBrowserAuth(st, uid, token, key, DefaultSessionTTL, log)
	if ba == nil {
		log.Warn("browser login disabled: no usable store")
		return nil
	}
	log.Info("browser login enabled")
	return ba
}

// LoginHandler serves GET /login (the login form) and POST /login (token →
// cookie). The spa filesystem, when present, provides the SPA's index.html for
// the GET; otherwise a minimal inline form is served so a node-less dev build
// still has a working login page.
func (b *BrowserAuth) LoginHandler(spa fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			b.serveForm(w, spa)
		case http.MethodPost:
			b.handleLogin(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// serveForm writes the SPA index.html (if embedded) or a minimal inline form.
func (b *BrowserAuth) serveForm(w http.ResponseWriter, spa fs.FS) {
	if spa != nil {
		if data, err := fs.ReadFile(spa, "index.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, loginFallbackHTML)
}

// handleLogin verifies the posted token in constant time and, on match, mints a
// session record + signed cookie and redirects to the SPA. A mismatch returns
// 401 with no cookie and no record (docs 04 §1.2).
func (b *BrowserAuth) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	presented := r.PostFormValue("token")
	// Constant-time compare (docs 04 §1.1). ConstantTimeCompare returns 0 on a
	// length mismatch, which only reveals length — acceptable for a single
	// shared secret.
	if subtle.ConstantTimeCompare([]byte(presented), []byte(b.loginToken)) != 1 {
		http.Error(w, "not authorized", http.StatusUnauthorized)
		return
	}

	id, err := newSessionID()
	if err != nil {
		b.log.Error("login: session id generation failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	if err := b.store.CreateLoginSession(r.Context(), store.LoginSessionRecord{
		SessionID: store.LoginSessionID(id),
		UserID:    b.uid,
		CreatedAt: now,
		ExpiresAt: now.Add(b.ttl),
	}); err != nil {
		b.log.Error("login: create session record failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, newSessionCookie(signSession(b.cookieKey, id), b.ttl))
	// Land on the SPA; the cookie now rides subsequent requests.
	http.Redirect(w, r, "/app/", http.StatusSeeOther)
}

// LogoutHandler serves POST /logout: delete the server-side record (revoking
// the cookie) and clear the browser cookie. Idempotent — a missing/invalid
// cookie still clears.
func (b *BrowserAuth) LogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if ck, err := r.Cookie(cookieName); err == nil {
			if id, ok := verifySession(b.cookieKey, ck.Value); ok {
				// Best-effort: an already-absent record (double logout) is fine.
				if err := b.store.DeleteLoginSession(r.Context(), store.LoginSessionID(id)); err != nil {
					b.log.Debug("logout: delete session record", "error", err)
				}
			}
		}
		http.SetCookie(w, clearSessionCookie())
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// authenticateCookie validates a request's session cookie for the interceptor:
// verify the HMAC signature, look up the server-side record, and enforce
// expiry. Any failure collapses to the uniform errUnauthenticated (no oracle).
func (b *BrowserAuth) authenticateCookie(ctx context.Context, header http.Header) error {
	ck, err := (&http.Request{Header: header}).Cookie(cookieName)
	if err != nil {
		return errUnauthenticated
	}
	id, ok := verifySession(b.cookieKey, ck.Value)
	if !ok {
		return errUnauthenticated
	}
	rec, err := b.store.GetLoginSession(ctx, store.LoginSessionID(id))
	if err != nil {
		// An infra failure (DB outage) is logged server-side so operators can
		// tell it apart from a genuinely unknown/revoked session; both still
		// collapse to the uniform reject on the wire (no oracle). Mirrors the
		// bearer path in authenticate().
		if !errors.Is(err, store.ErrNotFound) {
			b.log.Warn("cookie auth: session lookup failed (returning uniform unauthenticated)", "error", err)
		}
		return errUnauthenticated
	}
	// TODO(multi-user): also require rec.UserID == b.uid once more than one user
	// exists. Harmless today — the single MVP user is stamped on every record
	// (docs 04 §3) — but a real multi-user deploy must bind the cookie to its user.
	if time.Now().After(rec.ExpiresAt) {
		return errUnauthenticated
	}
	return nil
}

// cookieEligible reports whether a procedure may be authenticated by a browser
// session cookie. Only read RPCs are eligible; RegisterInstance is a host action
// and stays bearer-only (a browser must not register a host).
func cookieEligible(procedure string) bool {
	return procedure == hubv1connect.HubServiceListInstancesProcedure
}

// loginFallbackHTML is the minimal login page served when no SPA is embedded
// (dev without a built web/dist). It posts the token in the request BODY, never
// the URL (docs 04 §6, token-in-URL hygiene). No secret is embedded.
const loginFallbackHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>sprawl hub — login</title></head>
<body>
  <h1>sprawl hub</h1>
  <form method="post" action="/login">
    <label>Login token: <input type="password" name="token" autocomplete="off"></label>
    <button type="submit">Log in</button>
  </form>
</body>
</html>
`
