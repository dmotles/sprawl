package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// storeFactory produces a freshly-migrated, isolated Store for one subtest.
// memStore and pgStore each register a factory (mem_test.go / pg_test.go); the
// suite runs the identical asserts against every registered factory. This is
// the central correctness lever: the two impls MUST be behaviorally identical.
type storeFactory struct {
	name string
	new  func(t *testing.T) Store
}

// storeFactories is appended to by the per-impl test files' init funcs.
var storeFactories []storeFactory

const testUser UserID = "u-test"

// TestStoreSuite runs the shared contract against every registered impl.
func TestStoreSuite(t *testing.T) {
	if len(storeFactories) == 0 {
		t.Fatal("no store factories registered")
	}
	for _, f := range storeFactories {
		t.Run(f.name, func(t *testing.T) {
			t.Run("UsersOneRow", func(t *testing.T) { testUsersOneRow(t, f.new) })
			t.Run("TokenCRUD", func(t *testing.T) { testTokenCRUD(t, f.new) })
			t.Run("InstanceRegList", func(t *testing.T) { testInstances(t, f.new) })
			t.Run("ActiveHostAdvisory", func(t *testing.T) { testActiveHost(t, f.new) })
			t.Run("Sessions", func(t *testing.T) { testSessions(t, f.new) })
			t.Run("ForeignKeys", func(t *testing.T) { testForeignKeys(t, f.new) })
			t.Run("MigrateIdempotent", func(t *testing.T) { testMigrateIdempotent(t, f.new) })
			t.Run("BlobRoundTrip", func(t *testing.T) { testBlobRoundTrip(t, f.new) })
			t.Run("SecretResolve", func(t *testing.T) { testSecretResolve(t, f.new) })
		})
	}
}

// seeded returns a store with the singleton user already ensured.
func seeded(t *testing.T, newStore func(t *testing.T) Store) (Store, context.Context) {
	t.Helper()
	st := newStore(t)
	ctx := context.Background()
	if err := st.EnsureUser(ctx, testUser); err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	return st, ctx
}

func testUsersOneRow(t *testing.T, newStore func(t *testing.T) Store) {
	st := newStore(t)
	ctx := context.Background()

	if err := st.EnsureUser(ctx, testUser); err != nil {
		t.Fatalf("EnsureUser first: %v", err)
	}
	// Idempotent: same user again must succeed.
	if err := st.EnsureUser(ctx, testUser); err != nil {
		t.Fatalf("EnsureUser idempotent: %v", err)
	}
	// A different user violates the exactly-one-row invariant.
	if err := st.EnsureUser(ctx, "u-other"); err == nil {
		t.Fatal("EnsureUser with a second distinct user: want error, got nil")
	}
}

func testTokenCRUD(t *testing.T, newStore func(t *testing.T) Store) {
	st, ctx := seeded(t, newStore)

	// Insert multiple tokens so list ordering + multi-row content are exercised
	// (the classic mem-map vs pg-query divergence). Contract: ordered by
	// CreatedAt asc, ties broken by TokenID.
	hashes := map[TokenID][]byte{
		"tok-1": {0xde, 0xad},
		"tok-2": {0xbe, 0xef},
		"tok-3": {0x01, 0x02, 0x03},
	}
	for _, id := range []TokenID{"tok-1", "tok-2", "tok-3"} {
		if err := st.CreateToken(ctx, TokenRecord{
			TokenID: id, UserID: testUser, Hash: hashes[id], Label: string(id),
		}); err != nil {
			t.Fatalf("CreateToken %s: %v", id, err)
		}
	}

	// Duplicate TokenID must error (both impls, structurally in pg via PK).
	if err := st.CreateToken(ctx, TokenRecord{TokenID: "tok-1", UserID: testUser, Hash: []byte{0x00}}); err == nil {
		t.Error("CreateToken duplicate TokenID: want error, got nil")
	}

	got, err := st.ListTokens(ctx, testUser)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListTokens len = %d, want 3", len(got))
	}
	// Assert deterministic ordering by TokenID (ties in CreatedAt fall back to it).
	wantOrder := []TokenID{"tok-1", "tok-2", "tok-3"}
	for i, w := range wantOrder {
		if got[i].TokenID != w {
			t.Errorf("ListTokens[%d].TokenID = %q, want %q", i, got[i].TokenID, w)
		}
	}
	// Content parity: hash preserved verbatim, label set, active, timestamp set.
	for _, tok := range got {
		if !bytes.Equal(tok.Hash, hashes[tok.TokenID]) {
			t.Errorf("%s Hash = %x, want %x", tok.TokenID, tok.Hash, hashes[tok.TokenID])
		}
		if tok.Label != string(tok.TokenID) {
			t.Errorf("%s Label = %q, want %q", tok.TokenID, tok.Label, tok.TokenID)
		}
		if tok.RevokedAt != nil {
			t.Errorf("%s RevokedAt = %v, want nil (active)", tok.TokenID, tok.RevokedAt)
		}
		if tok.CreatedAt.IsZero() {
			t.Errorf("%s CreatedAt is zero, want set", tok.TokenID)
		}
	}

	// Revoke exactly one; the others stay active.
	if err := st.RevokeToken(ctx, "tok-2"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	got, err = st.ListTokens(ctx, testUser)
	if err != nil {
		t.Fatalf("ListTokens after revoke: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListTokens after revoke len = %d, want 3", len(got))
	}
	for _, tok := range got {
		if tok.TokenID == "tok-2" {
			if tok.RevokedAt == nil {
				t.Error("tok-2 RevokedAt = nil after revoke, want set")
			}
		} else if tok.RevokedAt != nil {
			t.Errorf("%s RevokedAt = %v, want nil (unaffected)", tok.TokenID, tok.RevokedAt)
		}
	}

	// Revoking an unknown token is ErrNotFound.
	if err := st.RevokeToken(ctx, "tok-missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RevokeToken(unknown) err = %v, want ErrNotFound", err)
	}
}

func testInstances(t *testing.T, newStore func(t *testing.T) Store) {
	st, ctx := seeded(t, newStore)

	// Register two hosts; second register of host-a is an idempotent upsert.
	if err := st.RegisterInstance(ctx, InstanceRegistration{
		HostID: "host-a", RunID: "run-1", RepoLabel: "sprawl", UserID: testUser,
	}); err != nil {
		t.Fatalf("RegisterInstance host-a: %v", err)
	}
	if err := st.RegisterInstance(ctx, InstanceRegistration{
		HostID: "host-a", RunID: "run-2", RepoLabel: "sprawl", UserID: testUser,
	}); err != nil {
		t.Fatalf("RegisterInstance host-a again: %v", err)
	}
	if err := st.RegisterInstance(ctx, InstanceRegistration{
		HostID: "host-b", RunID: "run-3", RepoLabel: "other", UserID: testUser,
	}); err != nil {
		t.Fatalf("RegisterInstance host-b: %v", err)
	}

	got, err := st.ListInstances(ctx)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListInstances len = %d, want 2 (upsert collapses host-a)", len(got))
	}
	// Deterministic ordering by HostID.
	if got[0].HostID != "host-a" || got[1].HostID != "host-b" {
		t.Fatalf("ListInstances order = [%q, %q], want [host-a, host-b]", got[0].HostID, got[1].HostID)
	}
	if got[0].RepoLabel != "sprawl" {
		t.Errorf("host-a RepoLabel = %q, want sprawl", got[0].RepoLabel)
	}
	for _, inst := range got {
		if inst.Active {
			t.Errorf("%s Active = true, want false (no active_host set yet)", inst.HostID)
		}
		if inst.LastSeenUnixMs == 0 {
			t.Errorf("%s LastSeenUnixMs = 0, want set", inst.HostID)
		}
	}

	// Positive Active case: set host-a as the active host for a project; it must
	// then report Active=true while host-b stays false.
	if err := st.UpsertProject(ctx, ProjectRecord{ProjectID: "proj-1", UserID: testUser}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if err := st.SetActiveHost(ctx, "proj-1", "host-a"); err != nil {
		t.Fatalf("SetActiveHost: %v", err)
	}
	got, err = st.ListInstances(ctx)
	if err != nil {
		t.Fatalf("ListInstances after SetActiveHost: %v", err)
	}
	for _, inst := range got {
		want := inst.HostID == "host-a"
		if inst.Active != want {
			t.Errorf("%s Active = %v, want %v", inst.HostID, inst.Active, want)
		}
	}
}

func testActiveHost(t *testing.T, newStore func(t *testing.T) Store) {
	st, ctx := seeded(t, newStore)

	const proj ProjectID = "proj-1"
	if err := st.UpsertProject(ctx, ProjectRecord{ProjectID: proj, UserID: testUser, RepoLabel: "sprawl"}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if err := st.UpsertHost(ctx, HostRecord{HostID: "host-a", UserID: testUser}); err != nil {
		t.Fatalf("UpsertHost host-a: %v", err)
	}
	if err := st.UpsertHost(ctx, HostRecord{HostID: "host-b", UserID: testUser}); err != nil {
		t.Fatalf("UpsertHost host-b: %v", err)
	}

	// Unknown project → ErrNotFound.
	if _, err := st.ReadActiveHost(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadActiveHost(unknown) err = %v, want ErrNotFound", err)
	}
	// Known project with no marker set yet → also ErrNotFound.
	if _, err := st.ReadActiveHost(ctx, proj); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadActiveHost(known, unset) err = %v, want ErrNotFound", err)
	}

	// SetActiveHost requires both project and host to exist (active_host FKs).
	if err := st.SetActiveHost(ctx, "ghost-proj", "host-a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetActiveHost(missing project) err = %v, want ErrNotFound", err)
	}
	if err := st.SetActiveHost(ctx, proj, "ghost-host"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetActiveHost(missing host) err = %v, want ErrNotFound", err)
	}

	if err := st.SetActiveHost(ctx, proj, "host-a"); err != nil {
		t.Fatalf("SetActiveHost host-a: %v", err)
	}
	ah, err := st.ReadActiveHost(ctx, proj)
	if err != nil {
		t.Fatalf("ReadActiveHost: %v", err)
	}
	if ah.HostID != "host-a" {
		t.Errorf("HostID = %q, want host-a", ah.HostID)
	}
	if ah.HeartbeatAt.IsZero() {
		t.Error("HeartbeatAt is zero, want set")
	}

	// Upsert: one row per project, second set overwrites.
	if err := st.SetActiveHost(ctx, proj, "host-b"); err != nil {
		t.Fatalf("SetActiveHost host-b: %v", err)
	}
	ah, err = st.ReadActiveHost(ctx, proj)
	if err != nil {
		t.Fatalf("ReadActiveHost after overwrite: %v", err)
	}
	if ah.HostID != "host-b" {
		t.Errorf("HostID = %q, want host-b (overwrite)", ah.HostID)
	}
}

func testSessions(t *testing.T, newStore func(t *testing.T) Store) {
	st, ctx := seeded(t, newStore)

	if err := st.UpsertHost(ctx, HostRecord{HostID: "host-1", UserID: testUser}); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	sess := SessionRecord{
		SessionID: "sess-1",
		UserID:    testUser,
		HostID:    "host-1",
		RunID:     "run-1",
	}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := st.GetSession(ctx, "sess-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", got.SessionID)
	}
	if got.HostID != "host-1" {
		t.Errorf("HostID = %q, want host-1", got.HostID)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want set")
	}

	if _, err := st.GetSession(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSession(missing) err = %v, want ErrNotFound", err)
	}
}

// testForeignKeys pins the referential-integrity + uniqueness parity that pg
// gets structurally from the schema and memStore must replicate by hand.
func testForeignKeys(t *testing.T, newStore func(t *testing.T) Store) {
	st, ctx := seeded(t, newStore) // singleton user "u-test" ensured

	// Writes referencing an unknown user are rejected (users FK).
	if err := st.CreateToken(ctx, TokenRecord{TokenID: "t", UserID: "ghost", Hash: []byte{1}}); err == nil {
		t.Error("CreateToken(unknown user): want error, got nil")
	}
	if err := st.UpsertHost(ctx, HostRecord{HostID: "h", UserID: "ghost"}); err == nil {
		t.Error("UpsertHost(unknown user): want error, got nil")
	}
	if err := st.RegisterInstance(ctx, InstanceRegistration{HostID: "h", UserID: "ghost"}); err == nil {
		t.Error("RegisterInstance(unknown user): want error, got nil")
	}
	if err := st.UpsertProject(ctx, ProjectRecord{ProjectID: "p", UserID: "ghost"}); err == nil {
		t.Error("UpsertProject(unknown user): want error, got nil")
	}

	// Duplicate token hash is rejected (tokens_hash_uq).
	hash := []byte{0xaa, 0xbb}
	if err := st.CreateToken(ctx, TokenRecord{TokenID: "tk-1", UserID: testUser, Hash: hash}); err != nil {
		t.Fatalf("CreateToken tk-1: %v", err)
	}
	if err := st.CreateToken(ctx, TokenRecord{TokenID: "tk-2", UserID: testUser, Hash: hash}); err == nil {
		t.Error("CreateToken(duplicate hash): want error, got nil")
	}

	// Session referencing an unknown host is rejected (sessions.host_id FK).
	// Both impls reject; the exact error class differs (pg surfaces the raw FK
	// violation, mem an ErrNotFound), so the parity contract is "non-nil".
	if err := st.CreateSession(ctx, SessionRecord{SessionID: "s", UserID: testUser, HostID: "ghost-host"}); err == nil {
		t.Error("CreateSession(unknown host): want error, got nil")
	}
}

func testMigrateIdempotent(t *testing.T, newStore func(t *testing.T) Store) {
	st := newStore(t)
	ctx := context.Background()
	// The factory already migrated once; re-running Migrate must be a clean,
	// idempotent no-op (goose skips already-applied versions; mem is a no-op).
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (second run): %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (third run): %v", err)
	}
}

func testBlobRoundTrip(t *testing.T, newStore func(t *testing.T) Store) {
	st := newStore(t)
	ctx := context.Background()
	b := st.Blobs()

	key := "blob://proj/agent/unit-1"
	body := []byte("hello durable body")
	if err := b.Put(ctx, key, body); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("Get = %q, want %q", got, body)
	}
	if _, err := b.Get(ctx, "blob://missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) err = %v, want ErrNotFound", err)
	}
}

func testSecretResolve(t *testing.T, newStore func(t *testing.T) Store) {
	st := newStore(t)
	ctx := context.Background()
	sec := st.Secrets()

	plaintext := []byte("host-token-pepper")
	ct, err := sec.Encrypt(ctx, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// A real keeper must transform the bytes — guard against a no-op "encryptor"
	// passing the round-trip vacuously.
	if bytes.Equal(ct, plaintext) {
		t.Error("ciphertext equals plaintext; encryption is a no-op")
	}
	pt, err := sec.Decrypt(ctx, ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Errorf("Decrypt = %q, want %q", pt, plaintext)
	}
}
