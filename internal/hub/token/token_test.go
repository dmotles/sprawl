package token

import (
	"strings"
	"testing"
)

func TestMint_RoundTripsThroughParse(t *testing.T) {
	m, err := Mint()
	if err != nil {
		t.Fatalf("Mint() error: %v", err)
	}
	if !strings.HasPrefix(m.Plaintext, Prefix) {
		t.Fatalf("plaintext %q lacks prefix %q", m.Plaintext, Prefix)
	}
	if m.TokenID == "" || m.Secret == "" {
		t.Fatalf("Mint returned empty field(s): tokenid=%q secret=%q", m.TokenID, m.Secret)
	}
	gotID, gotSecret, err := Parse(m.Plaintext)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", m.Plaintext, err)
	}
	if gotID != m.TokenID {
		t.Errorf("parsed tokenid = %q, want %q", gotID, m.TokenID)
	}
	if gotSecret != m.Secret {
		t.Errorf("parsed secret = %q, want %q", gotSecret, m.Secret)
	}
}

func TestMint_FieldsAreDelimiterSafe(t *testing.T) {
	m, err := Mint()
	if err != nil {
		t.Fatalf("Mint() error: %v", err)
	}
	// The '_' delimiter must never appear inside a base32 field, or Parse
	// (which cuts on the first '_' after the prefix) would corrupt.
	if strings.Contains(m.TokenID, "_") {
		t.Errorf("tokenid %q contains delimiter '_'", m.TokenID)
	}
	if strings.Contains(m.Secret, "_") {
		t.Errorf("secret %q contains delimiter '_'", m.Secret)
	}
}

func TestMint_IsRandomAcrossCalls(t *testing.T) {
	a, err := Mint()
	if err != nil {
		t.Fatalf("Mint() error: %v", err)
	}
	b, err := Mint()
	if err != nil {
		t.Fatalf("Mint() error: %v", err)
	}
	if a.TokenID == b.TokenID {
		t.Errorf("two Mints produced identical tokenids %q", a.TokenID)
	}
	if a.Secret == b.Secret {
		t.Errorf("two Mints produced identical secrets")
	}
}

func TestParse_Rejects(t *testing.T) {
	good, err := Mint()
	if err != nil {
		t.Fatalf("Mint() error: %v", err)
	}
	cases := map[string]string{
		"empty":            "",
		"wrong prefix":     "sprawl_" + good.TokenID + "_" + good.Secret,
		"prefix only":      Prefix,
		"no secret":        Prefix + good.TokenID,
		"empty tokenid":    Prefix + "_" + good.Secret,
		"empty secret":     Prefix + good.TokenID + "_",
		"non-base32 chars": Prefix + "!!!!" + "_" + "!!!!",
		"extra delimiter":  Prefix + good.TokenID + "_" + good.Secret + "_extra",
		"uppercase field":  Prefix + strings.ToUpper(good.TokenID) + "_" + good.Secret,
		"leading space":    " " + good.Plaintext,
		"trailing space":   good.Plaintext + " ",
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Parse(tok); err == nil {
				t.Errorf("Parse(%q) = nil error, want rejection", tok)
			}
		})
	}
}

func TestHashSecret_VerifyRoundTrip(t *testing.T) {
	encoded, err := HashSecret("s3cr3t-value")
	if err != nil {
		t.Fatalf("HashSecret error: %v", err)
	}
	if !VerifySecret(encoded, "s3cr3t-value") {
		t.Fatal("VerifySecret returned false for the correct secret")
	}
	if VerifySecret(encoded, "s3cr3t-valuE") {
		t.Fatal("VerifySecret returned true for a wrong secret")
	}
}

func TestHashSecret_UsesRandomSalt(t *testing.T) {
	a, err := HashSecret("same-secret")
	if err != nil {
		t.Fatalf("HashSecret error: %v", err)
	}
	b, err := HashSecret("same-secret")
	if err != nil {
		t.Fatalf("HashSecret error: %v", err)
	}
	if string(a) == string(b) {
		t.Fatal("two hashes of the same secret are identical — salt not random")
	}
	// Both must still verify.
	if !VerifySecret(a, "same-secret") || !VerifySecret(b, "same-secret") {
		t.Fatal("salted hashes failed to verify")
	}
}

func TestVerifySecret_MalformedEncodedIsFalse(t *testing.T) {
	for _, enc := range [][]byte{nil, {}, {0x01, 0x02}, make([]byte, argonSaltLen)} {
		if VerifySecret(enc, "anything") {
			t.Errorf("VerifySecret(%v) = true, want false for malformed input", enc)
		}
	}
}

func TestVerifySecret_NearMissSecretsRejected(t *testing.T) {
	const secret = "correct-horse-battery-staple"
	encoded, err := HashSecret(secret)
	if err != nil {
		t.Fatalf("HashSecret error: %v", err)
	}
	nearMisses := []string{
		"correct-horse-battery-stapl",
		"correct-horse-battery-staplee",
		"Correct-horse-battery-staple",
		"",
		" correct-horse-battery-staple",
	}
	for _, s := range nearMisses {
		if VerifySecret(encoded, s) {
			t.Errorf("VerifySecret accepted near-miss %q", s)
		}
	}
}
