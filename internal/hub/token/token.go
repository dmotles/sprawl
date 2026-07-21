// Package token owns the sprawl hub host-bearer-token crypto and wire format.
// It is the single home of all token hashing: the store layer stays
// hashing-free (it persists an opaque []byte), and callers (the `hub token`
// admin CLI and the auth interceptor) go through this package.
//
// Wire format: sprawl_hub_<tokenid>_<secret>
//
//   - The "sprawl_hub_" prefix aids secret-scanning tools.
//   - <tokenid> is an opaque, non-secret lookup id (O(1) indexed row lookup).
//   - <secret> is the high-entropy secret; only a hash of it is ever stored.
//
// Both <tokenid> and <secret> are lowercase base32 (RFC 4648, no padding) so
// the field delimiter '_' can never appear inside a field (base64url's alphabet
// includes '_', which would corrupt parsing — base32's does not).
package token

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Prefix is the readable, secret-scanner-friendly token prefix.
const Prefix = "sprawl_hub_"

// Random-field byte lengths. tokenid is a non-secret lookup id (80 bits is
// ample to avoid collisions); secret carries the entropy (256 bits).
const (
	tokenIDBytes = 10
	secretBytes  = 32
)

// argon2id parameters (OWASP recommended baseline: m=19 MiB, t=2, p=1). They
// are baked, untagged, into the encoded hash; changing them invalidates every
// existing token, so treat them as a wire contract for the token lifetime.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024 // KiB → 19 MiB
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// b32 is lowercase (via ToLower/ToUpper wrapping) base32 without padding.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// ErrMalformed is returned by Parse when a presented token is not a
// well-formed sprawl_hub_<tokenid>_<secret> string.
var ErrMalformed = errors.New("token: malformed bearer token")

// Minted is the result of Mint. Plaintext is shown to the operator exactly
// once and never persisted; only a hash of Secret is stored.
type Minted struct {
	Plaintext string
	TokenID   string
	Secret    string
}

// Mint generates a fresh token with CSPRNG tokenid and secret.
func Mint() (Minted, error) {
	id, err := randField(tokenIDBytes)
	if err != nil {
		return Minted{}, fmt.Errorf("token: mint tokenid: %w", err)
	}
	secret, err := randField(secretBytes)
	if err != nil {
		return Minted{}, fmt.Errorf("token: mint secret: %w", err)
	}
	return Minted{
		Plaintext: Prefix + id + "_" + secret,
		TokenID:   id,
		Secret:    secret,
	}, nil
}

// randField returns n CSPRNG bytes as a canonical lowercase base32 string.
func randField(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return encodeField(buf), nil
}

func encodeField(b []byte) string { return strings.ToLower(b32.EncodeToString(b)) }

// validateField checks that s is a canonical lowercase, no-padding base32
// field. Uppercase, padded, or otherwise non-canonical encodings are rejected
// so the wire format stays unambiguous.
func validateField(s string) error {
	if s == "" {
		return ErrMalformed
	}
	raw, err := b32.DecodeString(strings.ToUpper(s))
	if err != nil {
		return ErrMalformed
	}
	if encodeField(raw) != s {
		return ErrMalformed
	}
	return nil
}

// Parse validates and splits a presented token into its tokenid and secret.
func Parse(plaintext string) (tokenID, secret string, err error) {
	rest, ok := strings.CutPrefix(plaintext, Prefix)
	if !ok {
		return "", "", ErrMalformed
	}
	id, sec, ok := strings.Cut(rest, "_")
	if !ok {
		return "", "", ErrMalformed
	}
	// Both fields must be canonical base32 — this rejects empty fields, an
	// extra delimiter (which lands '_' inside sec), uppercase, and whitespace.
	if err := validateField(id); err != nil {
		return "", "", ErrMalformed
	}
	if err := validateField(sec); err != nil {
		return "", "", ErrMalformed
	}
	return id, sec, nil
}

// HashSecret computes the argon2id hash of secret with a fresh random salt.
// The encoded form is salt(16) || rawHash(32); we own both sides so no PHC
// string parsing is needed.
func HashSecret(secret string) ([]byte, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("token: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	encoded := make([]byte, 0, argonSaltLen+argonKeyLen)
	encoded = append(encoded, salt...)
	encoded = append(encoded, key...)
	return encoded, nil
}

// VerifySecret reports whether secret hashes to encoded, in constant time.
// A malformed/short encoded returns false without panicking.
func VerifySecret(encoded []byte, secret string) bool {
	if len(encoded) != argonSaltLen+argonKeyLen {
		return false
	}
	salt := encoded[:argonSaltLen]
	want := encoded[argonSaltLen:]
	got := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Sealer is the subset of store.SecretResolver this package needs to seal the
// argon2id hash under the per-deploy pepper (the keeper key). Declared locally
// so token never imports store; store.SecretResolver satisfies it structurally.
type Sealer interface {
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// SealedHash hashes secret (argon2id) then seals the hash via the Sealer. The
// keeper key is the per-deploy pepper, so a DB dump alone cannot verify tokens.
func SealedHash(ctx context.Context, s Sealer, secret string) ([]byte, error) {
	encoded, err := HashSecret(secret)
	if err != nil {
		return nil, err
	}
	sealed, err := s.Encrypt(ctx, encoded)
	if err != nil {
		return nil, fmt.Errorf("token: seal hash: %w", err)
	}
	return sealed, nil
}

// VerifySealed decrypts a sealed hash then constant-time-verifies secret. A
// decrypt failure returns (false, err); a hash mismatch returns (false, nil).
func VerifySealed(ctx context.Context, s Sealer, sealed []byte, secret string) (bool, error) {
	encoded, err := s.Decrypt(ctx, sealed)
	if err != nil {
		return false, fmt.Errorf("token: unseal hash: %w", err)
	}
	return VerifySecret(encoded, secret), nil
}
