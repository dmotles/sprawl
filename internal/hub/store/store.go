package store

import "context"

// Store is the hub's persistence boundary. Everything the hub persists goes
// through this one interface; the app tier never imports a driver directly.
//
// The interface is intentionally broader than this slice's callers require:
// the token, instance, project, active-host, and session methods are shaped now
// so P0-3 (host tokens, instances) and P0-4 (sessions, stream) do not have to
// reshape it. Where no caller exists this slice, the method is either fully
// implemented (cheap for memStore, and the shared suite exercises it) or
// stubbed with the final signature — noted per group below.
type Store interface {
	// --- bootstrap / cross-cutting ---

	// Migrate applies pending schema migrations. pgStore runs embedded goose
	// migrations; memStore is a no-op (its schema is just maps).
	Migrate(ctx context.Context) error
	// EnsureUser idempotently inserts the singleton user row. Inserting a
	// different UserID violates the one-row invariant and returns an error.
	EnsureUser(ctx context.Context, u UserID) error
	// Ping reports backend reachability; wired into /readyz. memStore always
	// returns nil.
	Ping(ctx context.Context) error
	// Close releases backend resources (pool, bucket, keeper).
	Close() error

	// --- tokens (hashed bearer tokens only; no hashing logic here) ---

	// CreateToken inserts a token row. A duplicate TokenID returns an error.
	// CreatedAt is stamped by the store if the caller leaves it zero.
	CreateToken(ctx context.Context, t TokenRecord) error
	// ListTokens returns the user's tokens (active and revoked) ordered by
	// CreatedAt ascending, ties broken by TokenID, so both impls agree.
	ListTokens(ctx context.Context, u UserID) ([]TokenRecord, error)
	// RevokeToken stamps RevokedAt. An unknown TokenID returns ErrNotFound.
	RevokeToken(ctx context.Context, id TokenID) error

	// --- hosts / instances (aligned to the proto shapes) ---

	UpsertHost(ctx context.Context, h HostRecord) error
	RegisterInstance(ctx context.Context, r InstanceRegistration) error
	// ListInstances returns all hosts ordered by HostID. A host's Active flag
	// is true when it currently holds the active-host marker for any project.
	ListInstances(ctx context.Context) ([]InstanceRecord, error)

	// --- projects + advisory active-host marker (docs 07 §3) ---

	UpsertProject(ctx context.Context, p ProjectRecord) error
	// SetActiveHost upserts the advisory marker for a project. Advisory only:
	// it does not fence or reject session writes. The project and host must
	// already exist (UpsertProject / UpsertHost / RegisterInstance) — otherwise
	// it returns ErrNotFound.
	SetActiveHost(ctx context.Context, project ProjectID, holder HostID) error
	// ReadActiveHost returns the marker, or ErrNotFound if none is set.
	ReadActiveHost(ctx context.Context, project ProjectID) (*ActiveHost, error)

	// --- sessions ---

	CreateSession(ctx context.Context, s SessionRecord) error
	// GetSession returns the session, or ErrNotFound.
	GetSession(ctx context.Context, id SessionID) (*SessionRecord, error)

	// --- login sessions (browser auth, docs 04 §6) ---
	// A DEDICATED table (QUM-878 Decision 1): a browser login has no host, so
	// it must not use the host-bound enter `sessions` table above.

	// CreateLoginSession inserts a browser login-session row. A duplicate
	// SessionID returns an error; an unknown UserID violates the users FK.
	// CreatedAt is stamped by the store if the caller leaves it zero.
	CreateLoginSession(ctx context.Context, s LoginSessionRecord) error
	// GetLoginSession returns the login session, or ErrNotFound.
	GetLoginSession(ctx context.Context, id LoginSessionID) (*LoginSessionRecord, error)
	// DeleteLoginSession removes a login session (logout / revocation). An
	// unknown SessionID returns ErrNotFound (parity with RevokeToken).
	DeleteLoginSession(ctx context.Context, id LoginSessionID) error

	// --- durable seq'd stream (docs 07 §4) — SHAPE ONLY this slice ---
	// The session_stream table and blob-body plumbing land in P0-4. memStore
	// implements these over a map; pgStore returns a not-implemented error.

	AppendStream(ctx context.Context, sess SessionID, events []Event) (Seq, error)
	ReadStream(ctx context.Context, sess SessionID, fromSeq, toSeq Seq) ([]Event, error)
	HeadSeq(ctx context.Context, sess SessionID) (Seq, error)

	// --- blob + secret seams (gocloud.dev) ---

	// Blobs returns the object-storage handle for large opaque bodies
	// (transcripts, memory bodies, attachments).
	Blobs() BlobStore
	// Secrets returns the secret resolver (host-token pepper, cookie key, DB
	// creds resolve through it in P0-3/P0-4).
	Secrets() SecretResolver
}
