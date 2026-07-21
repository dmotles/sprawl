package hub

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func testCookieKey() []byte {
	// A fixed 32-byte key for deterministic tests. NOT a real secret.
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func TestSignSession_RoundTrips(t *testing.T) {
	key := testCookieKey()
	id := "abc123session"
	value := signSession(key, id)

	got, ok := verifySession(key, value)
	if !ok {
		t.Fatalf("verifySession(sign(%q)) = ok false, want true", id)
	}
	if got != id {
		t.Errorf("recovered id = %q, want %q", got, id)
	}
}

func TestSignSession_HasSingleDelimitedMAC(t *testing.T) {
	value := signSession(testCookieKey(), "sid")
	// Format is "<id>.<b64(mac)>": exactly one '.' separating id from MAC.
	if n := strings.Count(value, "."); n != 1 {
		t.Fatalf("cookie value %q has %d '.' separators, want exactly 1", value, n)
	}
	if !strings.HasPrefix(value, "sid.") {
		t.Errorf("cookie value %q does not carry the id as its prefix", value)
	}
}

func TestVerifySession_WrongKeyFails(t *testing.T) {
	value := signSession(testCookieKey(), "sid")

	otherKey := make([]byte, 32)
	for i := range otherKey {
		otherKey[i] = 0xff
	}
	if _, ok := verifySession(otherKey, value); ok {
		t.Fatal("verifySession accepted a value signed with a different key")
	}
}

func TestVerifySession_TamperedFails(t *testing.T) {
	key := testCookieKey()
	value := signSession(key, "sid")

	// Tamper with the id portion (before the '.').
	tamperedID := "xid" + value[3:]
	if _, ok := verifySession(key, tamperedID); ok {
		t.Error("verifySession accepted a value with a tampered id")
	}

	// Tamper with the MAC portion (flip the first MAC char, right after the '.').
	dot := strings.IndexByte(value, '.')
	b := []byte(value)
	b[dot+1] ^= 0x01
	if _, ok := verifySession(key, string(b)); ok {
		t.Error("verifySession accepted a value with a tampered MAC")
	}
}

func TestVerifySession_Malformed(t *testing.T) {
	key := testCookieKey()
	for _, v := range []string{"", "no-dot", "id.", ".mac", "id.not-base64!!", "a.b.c"} {
		if _, ok := verifySession(key, v); ok {
			t.Errorf("verifySession(%q) = ok true, want false (malformed)", v)
		}
	}
}

func TestNewSessionID_UniqueAndURLSafe(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := newSessionID()
		if err != nil {
			t.Fatalf("newSessionID: %v", err)
		}
		// 32 CSPRNG bytes base64url-unpadded is 43 chars; a shorter id betrays a
		// low-entropy/counter impl. (CSPRNG-ness itself isn't behaviorally
		// testable; length is the cheap proxy.)
		if len(id) < 43 {
			t.Fatalf("session id %q is %d chars, want >= 43 (32 random bytes)", id, len(id))
		}
		// URL/cookie-safe: no '.', no ';', no whitespace (the '.' would collide
		// with the sign/verify delimiter).
		if strings.ContainsAny(id, ".;, \t\r\n") {
			t.Errorf("session id %q contains an unsafe character", id)
		}
		if seen[id] {
			t.Fatalf("newSessionID produced a duplicate: %q", id)
		}
		seen[id] = true
	}
}

func TestNewSessionCookie_Attributes(t *testing.T) {
	c := newSessionCookie("signed-value", time.Hour)
	if c.Name != cookieName {
		t.Errorf("Name = %q, want %q", c.Name, cookieName)
	}
	if c.Value != "signed-value" {
		t.Errorf("Value = %q, want signed-value", c.Value)
	}
	if !c.HttpOnly {
		t.Error("cookie is not HttpOnly (XSS can exfil it)")
	}
	if !c.Secure {
		t.Error("cookie is not Secure (would ride cleartext)")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
	if c.MaxAge != int(time.Hour.Seconds()) {
		t.Errorf("MaxAge = %d, want %d (ttl must be honored)", c.MaxAge, int(time.Hour.Seconds()))
	}
}

func TestClearSessionCookie_Deletes(t *testing.T) {
	c := clearSessionCookie()
	if c.Name != cookieName {
		t.Errorf("Name = %q, want %q", c.Name, cookieName)
	}
	if c.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want < 0 (deletion)", c.MaxAge)
	}
	if c.Value != "" {
		t.Errorf("Value = %q, want empty (deletion)", c.Value)
	}
	// A deletion cookie must keep the same security attributes so the browser
	// matches and replaces the original.
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("clear cookie attrs mismatch: HttpOnly=%v Secure=%v SameSite=%v Path=%q",
			c.HttpOnly, c.Secure, c.SameSite, c.Path)
	}
}
