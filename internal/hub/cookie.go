package hub

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"time"
)

// cookieName is the browser session cookie's name (docs 04 §6). The value is an
// opaque, HMAC-signed session id — never a login token.
const cookieName = "sprawl_hub_session"

// signSession returns the cookie value for a session id: "<id>.<mac>", where
// mac is base64url(HMAC-SHA256(key, id)). The id itself is opaque and stored
// server-side (login_sessions); the MAC lets the server reject a forged or
// tampered cookie without a store round-trip.
func signSession(key []byte, id string) string {
	return id + "." + base64.RawURLEncoding.EncodeToString(mac(key, id))
}

// verifySession validates a cookie value produced by signSession and returns
// the embedded session id. ok is false for any malformed, tampered, or
// wrong-key value. The MAC comparison is constant-time (hmac.Equal).
func verifySession(key []byte, value string) (id string, ok bool) {
	// Exactly one '.' separates the id from the MAC; an id never contains '.'
	// (newSessionID emits base64url, no '.').
	idPart, macPart, found := strings.Cut(value, ".")
	if !found || idPart == "" || macPart == "" || strings.Contains(macPart, ".") {
		return "", false
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(macPart)
	if err != nil {
		return "", false
	}
	if !hmac.Equal(gotMAC, mac(key, idPart)) {
		return "", false
	}
	return idPart, true
}

// mac computes HMAC-SHA256(key, id).
func mac(key []byte, id string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(id))
	return h.Sum(nil)
}

// newSessionID mints an opaque, high-entropy session id: 32 CSPRNG bytes encoded
// base64url (unpadded, so no '.' to collide with the sign/verify delimiter).
func newSessionID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// newSessionCookie builds the session cookie with the security attributes from
// docs 04 §6: HttpOnly (no JS access → XSS can't exfil), Secure (HTTPS only),
// SameSite=Strict (CSRF resistance; no external IdP redirect to accommodate),
// Path "/", and a bounded lifetime.
func newSessionCookie(value string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ttl.Seconds()),
	}
}

// clearSessionCookie builds a deletion cookie (MaxAge < 0) that matches the
// original's attributes so the browser replaces and expires it on logout.
func clearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	}
}
