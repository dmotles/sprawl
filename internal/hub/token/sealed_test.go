package token

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
)

// xorSealer is a trivial, non-deterministic Sealer fake: it prepends a random
// nonce and XORs the plaintext with a repeating key derived from the nonce.
// Non-determinism mirrors the real gocloud envelope keeper so we exercise the
// "same secret seals to different ciphertext, both verify" property.
type xorSealer struct{ key byte }

func (s xorSealer) Encrypt(_ context.Context, pt []byte) ([]byte, error) {
	var nonce [1]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(pt)+1)
	out = append(out, nonce[0])
	for _, b := range pt {
		out = append(out, b^s.key^nonce[0])
	}
	return out, nil
}

func (s xorSealer) Decrypt(_ context.Context, ct []byte) ([]byte, error) {
	if len(ct) == 0 {
		return nil, errors.New("xorSealer: empty ciphertext")
	}
	nonce := ct[0]
	out := make([]byte, 0, len(ct)-1)
	for _, b := range ct[1:] {
		out = append(out, b^s.key^nonce)
	}
	return out, nil
}

type failingSealer struct{}

func (failingSealer) Encrypt(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("boom encrypt")
}

func (failingSealer) Decrypt(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("boom decrypt")
}

func TestSealedHash_VerifyRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := xorSealer{key: 0x5a}
	sealed, err := SealedHash(ctx, s, "hunter2")
	if err != nil {
		t.Fatalf("SealedHash error: %v", err)
	}
	ok, err := VerifySealed(ctx, s, sealed, "hunter2")
	if err != nil {
		t.Fatalf("VerifySealed error: %v", err)
	}
	if !ok {
		t.Fatal("VerifySealed returned false for the correct secret")
	}
	ok, err = VerifySealed(ctx, s, sealed, "hunter3")
	if err != nil {
		t.Fatalf("VerifySealed error: %v", err)
	}
	if ok {
		t.Fatal("VerifySealed returned true for a wrong secret")
	}
}

func TestSealedHash_IsSealed(t *testing.T) {
	ctx := context.Background()
	s := xorSealer{key: 0x5a}
	sealed, err := SealedHash(ctx, s, "hunter2")
	if err != nil {
		t.Fatalf("SealedHash error: %v", err)
	}
	// The sealed bytes must NOT be a usable argon2 hash — if SealedHash
	// skipped the Encrypt step and returned HashSecret() directly, the sealed
	// blob would verify against the secret. It must not.
	if VerifySecret(sealed, "hunter2") {
		t.Fatal("sealed blob verifies as a raw argon2 hash — sealing not applied")
	}
	// After decrypting, the recovered bytes ARE a usable argon2 hash.
	recovered, err := s.Decrypt(ctx, sealed)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}
	if !VerifySecret(recovered, "hunter2") {
		t.Fatal("decrypted sealed blob does not verify — hash not sealed correctly")
	}
}

func TestVerifySealed_CorruptedSealedIsFalseNoPanic(t *testing.T) {
	ctx := context.Background()
	s := xorSealer{key: 0x5a}
	sealed, err := SealedHash(ctx, s, "hunter2")
	if err != nil {
		t.Fatalf("SealedHash error: %v", err)
	}
	// Truncate so Decrypt succeeds (xorSealer tolerates any length) but yields
	// a too-short salt||hash blob. Must be (false, nil), never a panic.
	truncated := sealed[:len(sealed)/2]
	ok, err := VerifySealed(ctx, s, truncated, "hunter2")
	if err != nil {
		t.Fatalf("VerifySealed on truncated blob returned error: %v", err)
	}
	if ok {
		t.Fatal("VerifySealed accepted a truncated sealed blob")
	}
}

func TestSealedHash_NonDeterministic(t *testing.T) {
	ctx := context.Background()
	s := xorSealer{key: 0x11}
	a, err := SealedHash(ctx, s, "same")
	if err != nil {
		t.Fatalf("SealedHash error: %v", err)
	}
	b, err := SealedHash(ctx, s, "same")
	if err != nil {
		t.Fatalf("SealedHash error: %v", err)
	}
	if string(a) == string(b) {
		t.Fatal("two seals of the same secret are identical")
	}
	for _, sealed := range [][]byte{a, b} {
		ok, err := VerifySealed(ctx, s, sealed, "same")
		if err != nil || !ok {
			t.Fatalf("VerifySealed(sealed) = (%v, %v), want (true, nil)", ok, err)
		}
	}
}

func TestSealedHash_EncryptErrorPropagates(t *testing.T) {
	if _, err := SealedHash(context.Background(), failingSealer{}, "x"); err == nil {
		t.Fatal("SealedHash swallowed an Encrypt error")
	}
}

func TestVerifySealed_DecryptErrorPropagates(t *testing.T) {
	ok, err := VerifySealed(context.Background(), failingSealer{}, []byte("whatever"), "x")
	if err == nil {
		t.Fatal("VerifySealed swallowed a Decrypt error")
	}
	if ok {
		t.Fatal("VerifySealed returned true on a Decrypt error")
	}
}
