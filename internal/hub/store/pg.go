package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for goose

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob" // register file:// bucket scheme
	_ "gocloud.dev/blob/memblob"  // register mem:// bucket scheme
	"gocloud.dev/secrets"
	_ "gocloud.dev/secrets/localsecrets" // register base64key:// keeper scheme
)

// pgStore is the Postgres Store implementation.
//
// Query-mapping choice (docs 07 Open Question C1, DECIDED): hand-written SQL
// with pgx, NOT sqlc and NOT an ORM. Hand-written pgx is the KISS default; the
// Phase-0 query surface is small and parameterized. sqlc remains a low-cost
// future upgrade if query code proves error-prone, but is not adopted now.
type pgStore struct {
	pool    *pgxpool.Pool
	dsn     string
	blobs   BlobStore
	secrets SecretResolver
}

// PGConfig configures a pgStore.
type PGConfig struct {
	// DSN is the Postgres connection string, injected at runtime (never
	// committed). Required.
	DSN string
	// BlobURL is a gocloud.dev/blob bucket URL (e.g. "mem://" or
	// "file:///var/lib/hub/blobs"). Defaults to "mem://" when empty.
	BlobURL string
	// SecretURL is a gocloud.dev/secrets keeper URL. Defaults to a per-process
	// random base64key:// keeper when empty (dev/test).
	SecretURL string
}

// openSQL opens a throwaway database/sql handle via the pgx stdlib driver, used
// for goose migrations (goose runs on database/sql, not pgxpool).
func openSQL(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sql: %w", err)
	}
	return db, nil
}

// NewPGStore connects a pgxpool and opens the blob + secret backends. It does
// NOT migrate; call Migrate (or `sprawl hub migrate`) first.
func NewPGStore(ctx context.Context, cfg PGConfig) (Store, error) {
	if cfg.DSN == "" {
		return nil, errors.New("store: pg DSN is required")
	}
	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("store: connect pool: %w", err)
	}

	blobURL := cfg.BlobURL
	if blobURL == "" {
		blobURL = "mem://"
	}
	bucket, err := blob.OpenBucket(ctx, blobURL)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: open bucket: %w", err)
	}

	sec, err := openSecrets(ctx, cfg.SecretURL)
	if err != nil {
		pool.Close()
		_ = bucket.Close()
		return nil, err
	}

	return &pgStore{
		pool:    pool,
		dsn:     cfg.DSN,
		blobs:   &gocloudBlob{bucket: bucket},
		secrets: sec,
	}, nil
}

// openSecrets opens a secrets keeper. An empty URL yields a per-process random
// keeper (dev/test), which is enough to exercise the resolver seam.
func openSecrets(ctx context.Context, url string) (SecretResolver, error) {
	if url == "" {
		return newRandomKeeper()
	}
	k, err := secrets.OpenKeeper(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("store: open keeper: %w", err)
	}
	return &gocloudSecrets{keeper: k}, nil
}

func (s *pgStore) Migrate(ctx context.Context) error {
	return Migrate(ctx, s.dsn)
}

func (s *pgStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *pgStore) Close() error {
	s.pool.Close()
	return errors.Join(s.blobs.Close(), s.secrets.Close())
}

func (s *pgStore) EnsureUser(ctx context.Context, u UserID) error {
	// Idempotent for the same user; a different user hits the singleton unique
	// index and errors, enforcing the exactly-one-row invariant.
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`,
		string(u))
	if err != nil {
		return fmt.Errorf("store: ensure user: %w", err)
	}
	return nil
}

func (s *pgStore) CreateToken(ctx context.Context, t TokenRecord) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tokens (token_id, user_id, token_hash, label)
		 VALUES ($1, $2, $3, $4)`,
		string(t.TokenID), string(t.UserID), t.Hash, t.Label)
	if err != nil {
		return fmt.Errorf("store: create token: %w", err)
	}
	return nil
}

func (s *pgStore) ListTokens(ctx context.Context, u UserID) ([]TokenRecord, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT token_id, user_id, token_hash, label, created_at, revoked_at
		 FROM tokens WHERE user_id = $1
		 ORDER BY created_at ASC, token_id ASC`,
		string(u))
	if err != nil {
		return nil, fmt.Errorf("store: list tokens: %w", err)
	}
	defer rows.Close()

	var out []TokenRecord
	for rows.Next() {
		var (
			t       TokenRecord
			revoked *time.Time
		)
		if err := rows.Scan(&t.TokenID, &t.UserID, &t.Hash, &t.Label, &t.CreatedAt, &revoked); err != nil {
			return nil, fmt.Errorf("store: scan token: %w", err)
		}
		t.RevokedAt = revoked
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tokens rows: %w", err)
	}
	return out, nil
}

func (s *pgStore) RevokeToken(ctx context.Context, id TokenID) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tokens SET revoked_at = now() WHERE token_id = $1 AND revoked_at IS NULL`,
		string(id))
	if err != nil {
		return fmt.Errorf("store: revoke token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either the token doesn't exist, or it is already revoked. Disambiguate
		// so an unknown id maps to ErrNotFound (matching memStore).
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM tokens WHERE token_id = $1)`,
			string(id)).Scan(&exists); err != nil {
			return fmt.Errorf("store: revoke token existence: %w", err)
		}
		if !exists {
			return fmt.Errorf("store: revoke token %q: %w", id, ErrNotFound)
		}
	}
	return nil
}

func (s *pgStore) UpsertHost(ctx context.Context, h HostRecord) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO hosts (host_id, user_id, repo_label, last_run_id)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (host_id) DO UPDATE SET
		   user_id = EXCLUDED.user_id,
		   repo_label = EXCLUDED.repo_label,
		   last_run_id = EXCLUDED.last_run_id,
		   last_seen_at = now()`,
		string(h.HostID), string(h.UserID), h.RepoLabel, h.LastRunID)
	if err != nil {
		return fmt.Errorf("store: upsert host: %w", err)
	}
	return nil
}

func (s *pgStore) RegisterInstance(ctx context.Context, r InstanceRegistration) error {
	return s.UpsertHost(ctx, HostRecord{
		HostID:    r.HostID,
		UserID:    r.UserID,
		RepoLabel: r.RepoLabel,
		LastRunID: r.RunID,
	})
}

func (s *pgStore) ListInstances(ctx context.Context) ([]InstanceRecord, error) {
	// A host is Active when it holds the active-host marker for any project.
	rows, err := s.pool.Query(ctx,
		`SELECT h.host_id, h.repo_label,
		        EXISTS (SELECT 1 FROM active_host a WHERE a.host_id = h.host_id) AS active,
		        h.last_seen_at
		 FROM hosts h
		 ORDER BY h.host_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list instances: %w", err)
	}
	defer rows.Close()

	var out []InstanceRecord
	for rows.Next() {
		var (
			rec      InstanceRecord
			lastSeen time.Time
		)
		if err := rows.Scan(&rec.HostID, &rec.RepoLabel, &rec.Active, &lastSeen); err != nil {
			return nil, fmt.Errorf("store: scan instance: %w", err)
		}
		rec.LastSeenUnixMs = lastSeen.UnixMilli()
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list instances rows: %w", err)
	}
	return out, nil
}

func (s *pgStore) UpsertProject(ctx context.Context, p ProjectRecord) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO projects (project_id, user_id, repo_label)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (project_id) DO UPDATE SET
		   user_id = EXCLUDED.user_id,
		   repo_label = EXCLUDED.repo_label`,
		string(p.ProjectID), string(p.UserID), p.RepoLabel)
	if err != nil {
		return fmt.Errorf("store: upsert project: %w", err)
	}
	return nil
}

func (s *pgStore) SetActiveHost(ctx context.Context, project ProjectID, holder HostID) error {
	// Advisory marker: one row per project, upsert. No fence/lease/epoch. The
	// user_id is looked up from the project so the row carries it (plan I9).
	// The INSERT..SELECT emits a row only when BOTH the project and the host
	// exist, so a missing project or host yields zero affected rows — mapped to
	// ErrNotFound to match memStore (and the active_host FK intent).
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO active_host (project_id, user_id, host_id, heartbeat_at)
		 SELECT p.project_id, p.user_id, h.host_id, now()
		 FROM projects p, hosts h
		 WHERE p.project_id = $1 AND h.host_id = $2
		 ON CONFLICT (project_id) DO UPDATE SET
		   host_id = EXCLUDED.host_id,
		   heartbeat_at = now()`,
		string(project), string(holder))
	if err != nil {
		return fmt.Errorf("store: set active host: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: set active host for project %q / host %q: %w", project, holder, ErrNotFound)
	}
	return nil
}

func (s *pgStore) ReadActiveHost(ctx context.Context, project ProjectID) (*ActiveHost, error) {
	var a ActiveHost
	err := s.pool.QueryRow(ctx,
		`SELECT project_id, host_id, heartbeat_at FROM active_host WHERE project_id = $1`,
		string(project)).Scan(&a.ProjectID, &a.HostID, &a.HeartbeatAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: active host for %q: %w", project, ErrNotFound)
		}
		return nil, fmt.Errorf("store: read active host: %w", err)
	}
	return &a, nil
}

func (s *pgStore) CreateSession(ctx context.Context, sess SessionRecord) error {
	var projectID *string
	if sess.ProjectID != "" {
		p := string(sess.ProjectID)
		projectID = &p
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (session_id, user_id, host_id, project_id, run_id, head_seq)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		string(sess.SessionID), string(sess.UserID), string(sess.HostID),
		projectID, sess.RunID, int64(sess.HeadSeq))
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

func (s *pgStore) GetSession(ctx context.Context, id SessionID) (*SessionRecord, error) {
	var (
		sess      SessionRecord
		projectID *string
		endedAt   *time.Time
		headSeq   int64
	)
	err := s.pool.QueryRow(ctx,
		`SELECT session_id, user_id, host_id, project_id, run_id, head_seq, created_at, ended_at
		 FROM sessions WHERE session_id = $1`,
		string(id)).Scan(&sess.SessionID, &sess.UserID, &sess.HostID, &projectID,
		&sess.RunID, &headSeq, &sess.CreatedAt, &endedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: session %q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get session: %w", err)
	}
	if projectID != nil {
		sess.ProjectID = ProjectID(*projectID)
	}
	sess.HeadSeq = Seq(headSeq)
	sess.EndedAt = endedAt
	return &sess, nil
}

// AppendStream/ReadStream/HeadSeq: SHAPE ONLY this slice. The session_stream
// table + blob-body plumbing land in P0-4 (docs 07 §4).
var errStreamNotImpl = errors.New("store: session stream not implemented (P0-4)")

func (s *pgStore) AppendStream(context.Context, SessionID, []Event) (Seq, error) {
	return 0, errStreamNotImpl
}

func (s *pgStore) ReadStream(context.Context, SessionID, Seq, Seq) ([]Event, error) {
	return nil, errStreamNotImpl
}

func (s *pgStore) HeadSeq(context.Context, SessionID) (Seq, error) {
	return 0, errStreamNotImpl
}

func (s *pgStore) Blobs() BlobStore { return s.blobs }

func (s *pgStore) Secrets() SecretResolver { return s.secrets }

// compile-time assertion.
var _ Store = (*pgStore)(nil)
