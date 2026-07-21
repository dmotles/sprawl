// Package store is the hub's persistence boundary. App code depends on the
// Store interface only — never on pgx, database/sql, or a blob SDK directly.
// Two implementations satisfy it: memStore (in-memory, hermetic) and pgStore
// (Postgres via hand-written pgx). They MUST be behaviorally identical; the
// shared table-driven suite in suite_test.go is what keeps them honest.
package store

import (
	"errors"
	"time"
)

// Identifier types. All are opaque strings; the single-user MVP keeps UserID
// constant everywhere (docs/design/hub/07-storage-persistence.md §0).
type (
	UserID         string
	HostID         string
	ProjectID      string
	SessionID      string
	LoginSessionID string
	TokenID        string
	Seq            int64
)

// ErrNotFound is returned when a requested row/blob does not exist. Both impls
// map their backend's not-found signal (pgx.ErrNoRows, gcerrors.NotFound) to
// this sentinel so callers get identical behavior.
var ErrNotFound = errors.New("store: not found")

// TokenRecord is one row of the tokens table. It stores a HASHED bearer token
// only — never plaintext. This slice does not hash (P0-3 owns hashing and the
// pepper); callers supply Hash already computed.
type TokenRecord struct {
	TokenID   TokenID
	UserID    UserID
	Hash      []byte // token_hash BYTEA; hashed bearer token, never plaintext
	Label     string
	CreatedAt time.Time
	RevokedAt *time.Time // nil == active
}

// HostRecord is one row of the hosts table (per machine/install).
type HostRecord struct {
	HostID    HostID
	UserID    UserID
	RepoLabel string
	LastRunID string // last RegisterInstance RunID
	FirstSeen time.Time
	LastSeen  time.Time
}

// InstanceRegistration mirrors the proto RegisterInstanceRequest. Registering
// an instance is an idempotent upsert of the host row keyed by HostID.
type InstanceRegistration struct {
	HostID    HostID
	RunID     string
	RepoLabel string
	UserID    UserID
}

// InstanceRecord mirrors the proto Instance (list view). It is derived from
// hosts (plus active_host for Active). ClientsConnected is not persisted in
// Phase 0 (it is runtime-tracked in a later slice) and reads 0.
type InstanceRecord struct {
	HostID           HostID
	RepoLabel        string
	Active           bool
	ClientsConnected int32
	LastSeenUnixMs   int64
}

// ProjectRecord is one row of the projects table (per repo/project).
type ProjectRecord struct {
	ProjectID ProjectID
	UserID    UserID
	RepoLabel string
	CreatedAt time.Time
}

// ActiveHost is the advisory active-host marker: one row per project recording
// which host is currently the active writer. It is advisory only — no fence,
// no lease, no epoch (docs 07 §3).
type ActiveHost struct {
	ProjectID   ProjectID
	HostID      HostID
	HeartbeatAt time.Time
}

// SessionRecord is one row of the sessions table (one per `sprawl enter`).
type SessionRecord struct {
	SessionID SessionID
	UserID    UserID
	HostID    HostID
	ProjectID ProjectID
	RunID     string
	HeadSeq   Seq
	CreatedAt time.Time
	EndedAt   *time.Time // nil == open
}

// LoginSessionRecord is one row of the login_sessions table — a browser login
// session minted by /login (docs 04 §6). It is DELIBERATELY distinct from
// SessionRecord (the `sprawl enter` session, which carries a mandatory host FK):
// a browser login has no host, so QUM-878 Decision 1 gives it a dedicated table
// with no fake host reference. ExpiresAt is persisted verbatim; expiry
// enforcement lives in the auth layer, not the store. Deleting the row revokes
// the cookie on its next use.
type LoginSessionRecord struct {
	SessionID LoginSessionID
	UserID    UserID
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Event is one row of the durable seq'd session stream (docs 07 §4). The shape
// is locked here so P0-4 does not reshape the interface; the stream table and
// blob-body plumbing land in P0-4, so AppendStream/ReadStream/HeadSeq are
// stubbed this slice (see Store).
type Event struct {
	Seq       Seq
	Kind      string
	BlobKey   string
	Payload   []byte
	CreatedAt time.Time
}
