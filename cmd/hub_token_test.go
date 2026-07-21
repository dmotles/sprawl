package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/hub"
	"github.com/dmotles/sprawl/internal/hub/store"
	"github.com/dmotles/sprawl/internal/hub/token"
)

// newTestHubTokenDeps returns deps backed by a single shared memStore so
// state persists across create → list → revoke within a test.
func newTestHubTokenDeps(t *testing.T) (*hubTokenDeps, *bytes.Buffer, *bytes.Buffer, store.Store) {
	t.Helper()
	st, err := store.NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var out, errOut bytes.Buffer
	deps := &hubTokenDeps{
		Getenv:    func(string) string { return "" },
		Stdout:    &out,
		Stderr:    &errOut,
		OpenStore: func(context.Context, string) (store.Store, error) { return nonClosingStore{st}, nil },
	}
	return deps, &out, &errOut, st
}

// nonClosingStore prevents the command's deferred Close from tearing down the
// shared test store between invocations.
type nonClosingStore struct{ store.Store }

func (nonClosingStore) Close() error { return nil }

func TestHubTokenCreate_PrintsPlaintextOnceAndStoresSealedHash(t *testing.T) {
	deps, out, errOut, st := newTestHubTokenDeps(t)
	if err := runHubTokenCreate(context.Background(), deps, "postgres://x", "laptop"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Exactly one plaintext token on stdout.
	plaintext := extractToken(t, out.String())
	if strings.Count(out.String(), token.Prefix) != 1 {
		t.Fatalf("expected exactly one token in stdout, got: %q", out.String())
	}
	// A show-once warning belongs on stderr.
	if !strings.Contains(strings.ToLower(errOut.String()), "once") {
		t.Errorf("stderr should warn the token is shown once: %q", errOut.String())
	}

	// The stored record holds a sealed hash — not the plaintext — that verifies.
	tokenID, secret, err := token.Parse(plaintext)
	if err != nil {
		t.Fatalf("parse printed token: %v", err)
	}
	recs, err := st.ListTokens(context.Background(), hub.MVPUserID)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("want 1 stored token, got %d", len(recs))
	}
	rec := recs[0]
	if string(rec.TokenID) != tokenID {
		t.Errorf("stored tokenid = %q, want %q", rec.TokenID, tokenID)
	}
	if rec.Label != "laptop" {
		t.Errorf("stored label = %q, want laptop", rec.Label)
	}
	if bytes.Contains(rec.Hash, []byte(secret)) || bytes.Contains(rec.Hash, []byte(plaintext)) {
		t.Fatal("stored hash contains the plaintext secret")
	}
	ok, err := token.VerifySealed(context.Background(), st.Secrets(), rec.Hash, secret)
	if err != nil || !ok {
		t.Fatalf("stored sealed hash does not verify: ok=%v err=%v", ok, err)
	}
}

func TestHubTokenList_MetadataOnlyNoSecret(t *testing.T) {
	deps, out, _, _ := newTestHubTokenDeps(t)
	if err := runHubTokenCreate(context.Background(), deps, "postgres://x", "ci"); err != nil {
		t.Fatalf("create: %v", err)
	}
	plaintext := extractToken(t, out.String())
	tokenID, secret, _ := token.Parse(plaintext)

	out.Reset()
	if err := runHubTokenList(context.Background(), deps, "postgres://x"); err != nil {
		t.Fatalf("list: %v", err)
	}
	listing := out.String()
	if !strings.Contains(listing, tokenID) {
		t.Errorf("list should show tokenid %q: %q", tokenID, listing)
	}
	if !strings.Contains(listing, "ci") {
		t.Errorf("list should show label: %q", listing)
	}
	// The secret and full plaintext must NEVER appear in a listing.
	if strings.Contains(listing, secret) {
		t.Error("list leaked the token secret")
	}
	if strings.Contains(listing, token.Prefix) {
		t.Error("list leaked a full plaintext token")
	}
}

func TestHubTokenRevoke_InvalidatesAndShowsInList(t *testing.T) {
	deps, out, _, st := newTestHubTokenDeps(t)
	if err := runHubTokenCreate(context.Background(), deps, "postgres://x", "old"); err != nil {
		t.Fatalf("create: %v", err)
	}
	plaintext := extractToken(t, out.String())
	tokenID, _, _ := token.Parse(plaintext)

	if err := runHubTokenRevoke(context.Background(), deps, "postgres://x", tokenID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	recs, _ := st.ListTokens(context.Background(), hub.MVPUserID)
	if len(recs) != 1 || recs[0].RevokedAt == nil {
		t.Fatalf("token was not revoked: %+v", recs)
	}

	out.Reset()
	if err := runHubTokenList(context.Background(), deps, "postgres://x"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(strings.ToLower(out.String()), "revoked") {
		t.Errorf("list should mark the token revoked: %q", out.String())
	}
}

func TestHubTokenRevoke_UnknownIDErrors(t *testing.T) {
	deps, _, _, _ := newTestHubTokenDeps(t)
	err := runHubTokenRevoke(context.Background(), deps, "postgres://x", "nosuchtoken")
	if err == nil {
		t.Fatal("revoke of an unknown token id should error")
	}
}

// extractToken pulls the single sprawl_hub_ token out of captured stdout.
func extractToken(t *testing.T, s string) string {
	t.Helper()
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(f, token.Prefix) {
			return f
		}
	}
	t.Fatalf("no token found in output: %q", s)
	return ""
}
