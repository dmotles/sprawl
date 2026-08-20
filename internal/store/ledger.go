package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ledger is the event log as every caller outside this package sees it.
//
// ITS DEFINING PROPERTY IS THAT IT CANNOT BREAK AN AGENT. Every caller is a
// subscriber on an agent's runtime EventBus, or a step inside spawn/retire — a
// panic there takes the agent's turn with it, and an error there propagates into
// an operation that has nothing to do with the event log.
//
// So the do-nothing case is not a special case checked at each call site: a nil
// *Ledger IS a working Ledger whose methods succeed and record nothing. nil is
// also what every call site gets when the feature is off, which is the default,
// so this is the common path and not an edge.
type Ledger struct {
	enabled   bool
	appender  *Appender
	pool      *pgxpool.Pool
	registry  *Registry
	projectID uuid.UUID
	dsnSource string
	// degradedErr is non-nil when the event log was unreachable at Open.
	//
	// Open returns a DEGRADED Ledger rather than an error in that case, and the
	// reason is not politeness: with no Ledger there is no spiller, so telemetry
	// would be silently DROPPED instead of spilled — the one outcome the
	// degraded-mode requirement forbids.
	degradedErr error
	log         *slog.Logger
}

// LedgerConfig is what Open needs. Resolved by the caller so this package does
// not reach for the environment or the filesystem on its own.
type LedgerConfig struct {
	// Enabled is the .sprawl/config.yaml feature flag.
	Enabled bool
	// DSN and DSNSource come from ResolveDSN. DSNSource names the ORIGIN and
	// must never contain the DSN.
	DSN       string
	DSNSource string
	// RemoteURL identifies the project — the repo's remote URL is the unique
	// key for a project's namespace in the log.
	RemoteURL string
	// GitSHA is recorded on the first-enable repo_initialized event.
	GitSHA string
	// SprawlRoot is where the degraded-mode spill lives.
	SprawlRoot string
	Logger     *slog.Logger
	Now        func() time.Time

	// migrate is a test seam only. Production leaves it nil, and Open does not
	// migrate at all — see TestOpen_DoesNotMigrate_SchemaReadinessIsCheckedInstead
	// for why applying migrations with the application DSN was a defect rather
	// than a convenience.
	migrate func(ctx context.Context, dsn string) error
}

// Open connects the event log, migrates it, and registers the project.
//
// Returns (nil, nil) when the flag is off: callers hold a *Ledger and never
// have to branch on whether the feature is on.
func Open(ctx context.Context, cfg LedgerConfig) (*Ledger, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if cfg.DSN == "" {
		// ResolveDSN treats an absent DSN as a quiet non-error because the
		// store is opt-in. Once someone has explicitly turned it ON, though,
		// silently running disabled is the worst outcome available: the
		// operator believes events are being recorded and nothing says
		// otherwise until they query an empty log.
		return nil, &HintError{
			Err: fmt.Errorf("the event log is enabled in .sprawl/config.yaml but no DSN is configured"),
			Hint: fmt.Sprintf("set %s, or put `db_dsn: <dsn>` in a 0600 ~/.config/sprawl/%s — never in .sprawl/config.yaml, which is tracked in a public repo",
				EnvDSN, SecretsFileName),
		}
	}
	if cfg.RemoteURL == "" {
		return nil, &HintError{
			Err:  fmt.Errorf("the event log is enabled but this repo has no remote URL, which is a project's identity"),
			Hint: "add a git remote (`git remote add origin <url>`), or disable the store with `sprawl config set event_log.enabled false`",
		}
	}

	// Parse the DSN BEFORE anything else, and treat a parse failure as the
	// configuration error it is.
	//
	// This is separated from the reachability check below because
	// isTransportFailure cannot tell them apart: an unparseable DSN produces a
	// non-PgError, exactly like a dial failure, so routing on that alone would
	// send a permanent typo into degraded mode and hide it behind a quietly
	// growing spill directory. The DSN is not echoed back — it is a credential.
	if _, err := pgxpool.ParseConfig(cfg.DSN); err != nil {
		return nil, &HintError{
			Err:  fmt.Errorf("store: the event-log DSN from %s is not a valid Postgres connection string: %w", describeSource(cfg.DSNSource), err),
			Hint: "check the value for typos (the DSN itself is not shown here because it is a credential); the expected form is postgres://user:password@host:5432/dbname",
		}
	}

	registry, err := SeedRegistry()
	if err != nil {
		return nil, err
	}
	spiller := &FileSpiller{Root: cfg.SprawlRoot, Now: cfg.Now}

	// degraded builds an ENABLED Ledger that spills telemetry and refuses
	// coordination.
	//
	// It returns (ledger, nil) rather than an error, and that is load-bearing
	// rather than lenient: with no Ledger there is no spiller, so telemetry
	// would be silently DROPPED instead of spilled — the one outcome the
	// degraded-mode requirement forbids. The caller gets a working object whose
	// behaviour differs, not an error it has to interpret.
	degraded := func(cause error) (*Ledger, error) {
		log.Warn("event log unreachable — running degraded: telemetry spills, goal open/close is refused",
			"error", cause, "dsn_source", cfg.DSNSource, "spill_dir", SpillDir(cfg.SprawlRoot))
		return &Ledger{
			enabled:     true,
			registry:    registry,
			dsnSource:   cfg.DSNSource,
			degradedErr: cause,
			log:         log,
			appender: NewAppender(AppenderDeps{
				Registry:  registry,
				Spill:     spiller,
				Now:       cfg.Now,
				Logger:    log,
				Degraded:  cause,
				RemoteURL: cfg.RemoteURL,
			}),
		}, nil
	}

	// OPEN DOES NOT MIGRATE. Migration is `sprawl store migrate`, run by an
	// operator with a privileged DSN.
	//
	// It used to, and that was a defect severe enough to make the store's two
	// goals mutually exclusive. goose reads its own goose_db_version table on
	// every Up() even with nothing pending, and the least-privilege app role
	// holds nothing on that table — so the deployment shape 00002 documents
	// produced 42501, a PgError, hence a refusal, hence not survivable, hence a
	// hard error from Open, which store.Process caches for the process lifetime,
	// hence a nil emitter for every agent and every lifecycle event DROPPED. Not
	// spilled, not dead-lettered, not surfaced: the fifth outcome spill.go says
	// does not exist.
	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		if isTransportFailure(err) {
			return degraded(err)
		}
		return nil, fmt.Errorf("store: connecting to the event log: %w", err)
	}

	// Readiness instead. isTransportFailure separates "unreachable" (survivable,
	// degrade) from "the database answered and refused" (a real
	// misconfiguration that degraded mode would hide behind a growing spill
	// directory).
	if err := verifySchemaReady(ctx, pool, registry); err != nil {
		pool.Close()
		if isTransportFailure(err) {
			return degraded(err)
		}
		return nil, err
	}

	l := &Ledger{
		enabled:   true,
		pool:      pool,
		registry:  registry,
		dsnSource: cfg.DSNSource,
		log:       log,
	}
	l.appender = NewAppender(AppenderDeps{
		Pool:      pool,
		Registry:  registry,
		Spill:     spiller,
		Now:       cfg.Now,
		Logger:    log,
		RemoteURL: cfg.RemoteURL,
	})

	// A WARNING rather than a failure. An over-privileged DSN means history is
	// rewritable, which an operator must know — but refusing to start would
	// brick every agent over a configuration problem, and the degraded-mode
	// requirement forbids that.
	if err := VerifyAppendOnly(ctx, pool); err != nil {
		log.Warn("the event log is not append-only for this connection", "error", err)
	}

	projectID, created, err := ensureProject(ctx, pool, cfg.RemoteURL)
	if err != nil {
		pool.Close()
		if isTransportFailure(err) {
			return degraded(err)
		}
		return nil, err
	}
	l.projectID = projectID

	if created {
		if _, err := l.Emit(ctx, EmitRequest{
			TypeName:    "repo_initialized",
			TypeVersion: 1,
			Payload: map[string]any{
				"git_sha":    cfg.GitSHA,
				"remote_url": cfg.RemoteURL,
			},
		}); err != nil {
			// Non-fatal: the project row exists, which is what everything else
			// needs. Losing the marker event is not worth refusing to start.
			log.Warn("could not record repo_initialized", "error", err)
		}
	}
	return l, nil
}

// verifySchemaReady checks that the schema is applied and the seed event types
// are published, using ONLY privileges the least-privilege app role holds.
//
// One query does both jobs. Counting the registry's own ids in
// event_type_schemas fails with 42P01 (undefined_table) when migrations have
// never run, and returns a short count when the binary carries seeds the
// database has not published — which is the case that would otherwise surface
// much later as an FK violation on a pinned schema_id, at append time, in
// production.
//
// It requires SELECT on event_type_schemas and nothing else. That constraint is
// the whole point: a readiness check the app role cannot run is a readiness
// check that fails for every correct deployment.
func verifySchemaReady(ctx context.Context, pool PgPool, registry *Registry) error {
	all := registry.All()
	ids := make([]uuid.UUID, 0, len(all))
	for _, s := range all {
		ids = append(ids, s.ID)
	}

	var found int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM event_type_schemas WHERE id = ANY($1)`, ids).Scan(&found); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return &HintError{
				Err:  fmt.Errorf("store: the event log schema is not usable from this connection: %w", err),
				Hint: "run `sprawl store migrate` with a privileged DSN (the application role deliberately cannot migrate); if it has already been run, check that this DSN points at the right database and that its role inherits sprawl_app",
			}
		}
		return err // transport: the caller degrades
	}
	if found != len(ids) {
		return &HintError{
			Err: fmt.Errorf("store: the event log has %d of this build's %d event-type schemas published",
				found, len(ids)),
			Hint: "run `sprawl store migrate` with a privileged DSN to publish them; until then an append would fail its schema_id foreign key",
		}
	}
	return nil
}

// ensureProject registers the project by remote URL, returning its id and
// whether this call created it.
//
// USES ONLY SELECT AND INSERT, and that constraint drives the shape. The obvious
// one-statement form —
// `INSERT ... ON CONFLICT (remote_url) DO UPDATE SET remote_url = projects.remote_url RETURNING id, (xmax = 0)` —
// requires the UPDATE privilege even though the update is a no-op, and the
// least-privilege app role deliberately does not hold UPDATE on projects. That
// is not a hypothetical: it produced "permission denied for table projects"
// (42501) the first time this was exercised as a real login role inheriting
// sprawl_app, which is the deployment 00002_m1a_app_role.sql documents. Granting
// UPDATE to make one clever statement work would have weakened the privilege
// story to save two round trips.
//
// remote_url is UNIQUE, so the read-insert-read shape is race-safe rather than
// merely lucky: two hosts enabling the store concurrently converge on one
// project, and whichever loses the insert reads the winner's row. `created` is
// true only for the caller whose INSERT actually returned a row, so
// repo_initialized is emitted exactly once.
func ensureProject(ctx context.Context, pool PgPool, remoteURL string) (uuid.UUID, bool, error) {
	var id uuid.UUID

	// Already registered: the overwhelmingly common case after first enable.
	err := pool.QueryRow(ctx, `SELECT id FROM projects WHERE remote_url = $1`, remoteURL).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("store: looking up project: %w", err)
	}

	// DO NOTHING rather than DO UPDATE, for the privilege reason above. It
	// returns no row when another host won the race, which is why the read below
	// is not redundant.
	err = pool.QueryRow(ctx,
		`INSERT INTO projects (id, remote_url, created_at) VALUES ($1, $2, now())
		 ON CONFLICT (remote_url) DO NOTHING
		 RETURNING id`, uuid.New(), remoteURL).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("store: registering project: %w", err)
	}

	// Lost the race: read the winner's row.
	if err := pool.QueryRow(ctx,
		`SELECT id FROM projects WHERE remote_url = $1`, remoteURL).Scan(&id); err != nil {
		return uuid.Nil, false, fmt.Errorf("store: reading concurrently-registered project: %w", err)
	}
	return id, false, nil
}

// Enabled reports whether this Ledger records anything. Safe on nil.
func (l *Ledger) Enabled() bool { return l != nil && l.enabled }

// ProjectID is the project this Ledger writes under. Safe on nil.
func (l *Ledger) ProjectID() uuid.UUID {
	if l == nil {
		return uuid.Nil
	}
	return l.projectID
}

// logger returns a usable logger even when none was configured.
//
// NOT DEFENSIVE PADDING — l.log is only populated by Open, so any Ledger built
// any other way (every hermetic test fixture, and any future constructor) panics
// on its first warn. That is what happened: RecordHandoff was the first Ledger
// method to log, and it segfaulted in slog.Logger.Enabled on a fixture-built
// Ledger. A nil *slog.Logger is a nil-pointer dereference, not a no-op, so the
// nil-Ledger-is-a-working-Ledger promise this type makes everywhere else did not
// extend to logging until this existed.
func (l *Ledger) logger() *slog.Logger {
	if l == nil || l.log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return l.log
}

// DegradedError reports why the event log is unreachable, or nil.
//
// A degraded Ledger is still ENABLED — it is spilling, which is doing
// something — so Enabled() alone cannot tell an operator that events are not
// reaching the database. This is what `sprawl store doctor` reports. Safe on
// nil, where it returns nil: a nil Ledger is DISABLED, which is a different
// state from degraded and must not be reported as an outage.
func (l *Ledger) DegradedError() error {
	if l == nil {
		return nil
	}
	return l.degradedErr
}

// DSNSource names where the DSN came from. It never contains the DSN, so it is
// safe to print. Safe on nil.
func (l *Ledger) DSNSource() string {
	if l == nil {
		return ""
	}
	return l.dsnSource
}

// Pool exposes the connection pool for read-only diagnostics (`store doctor`,
// `store status`). Safe on nil.
func (l *Ledger) Pool() *pgxpool.Pool {
	if l == nil {
		return nil
	}
	return l.pool
}

// Registry exposes the event-type registry so callers can pin a schema id.
// Safe on nil.
func (l *Ledger) Registry() *Registry {
	if l == nil {
		return nil
	}
	return l.registry
}

// Close releases the pool. Safe on nil, and safe to call twice.
//
// FOR SHORT-LIVED PROCESSES ONLY — `sprawl store …`, a migration, a test. A
// long-lived process must NOT call this on the value from Process(), which is a
// package singleton shared by every agent in the process: closing it would take
// the pool out from under all of them.
//
// It deliberately does not nil out the field. pgxpool.Pool.Close is itself
// idempotent, so the nil-ing bought nothing and cost an unsynchronised write
// racing every Pool() reader. Leaving the (closed) pool in place is also more
// honest: a caller that keeps using a closed Ledger gets pgx's "closed pool"
// error rather than a nil dereference — and note that error is NOT a PgError, so
// isTransportFailure classes it as transport and a spillable event would spill.
// That is the reason the "short-lived only" rule above is a rule and not a
// preference.
func (l *Ledger) Close() {
	if l == nil || l.pool == nil {
		return
	}
	l.pool.Close()
}

// Append writes a fully-formed event. Safe on nil: records nothing, returns
// (0, nil).
func (l *Ledger) Append(ctx context.Context, ev Event) (int64, error) {
	if !l.Enabled() {
		return 0, nil
	}
	if ev.ProjectID == uuid.Nil {
		ev.ProjectID = l.projectID
	}
	return l.appender.Append(ctx, ev)
}

// EmitRequest is the convenience form: name the event type and its VERSION, and
// hand over a payload to be marshalled.
type EmitRequest struct {
	// TypeName and TypeVersion together ARE the pin. There is deliberately no
	// way to express "latest" in this struct: a name alone would have to mean
	// latest, and the moment it did, publishing a new schema version would
	// change the validation applied to in-flight workflow instances.
	TypeName    string
	TypeVersion int

	// WorkflowInstanceID may be zero, in which case one is minted. The M1a
	// lifecycle emitters are pure telemetry with no workflow to belong to, and
	// events.workflow_instance_id is NOT NULL.
	WorkflowInstanceID uuid.UUID

	EventID        uuid.UUID
	AgentSessionID *uuid.UUID
	OwnerAgentID   *uuid.UUID
	ClosesEventID  *uuid.UUID
	ArtifactID     *uuid.UUID
	Payload        any
}

// Emit resolves the pinned schema, marshals the payload, and appends. Safe on
// nil: records nothing, returns (0, nil).
//
// It is a thin wrapper on purpose — it resolves a (name, version) to an id and
// marshals a payload, and then goes through exactly the same Append path, so it
// cannot become a route around validation or contract maintenance.
func (l *Ledger) Emit(ctx context.Context, req EmitRequest) (int64, error) {
	if !l.Enabled() {
		return 0, nil
	}
	if req.TypeVersion <= 0 {
		return 0, fmt.Errorf("store: emitting %q requires an explicit schema version: an event type name without a version could only mean `latest`, and validation must be pinned", req.TypeName)
	}
	schema, ok := l.registry.ByName(req.TypeName, req.TypeVersion)
	if !ok {
		return 0, fmt.Errorf("store: no event-type schema %s@%d in this build", req.TypeName, req.TypeVersion)
	}

	payload := json.RawMessage(`{}`)
	if req.Payload != nil {
		b, err := json.Marshal(req.Payload)
		if err != nil {
			return 0, fmt.Errorf("store: marshalling %s@%d payload: %w", req.TypeName, req.TypeVersion, err)
		}
		payload = b
	}

	instance := req.WorkflowInstanceID
	if instance == uuid.Nil {
		instance = uuid.New()
	}

	return l.appender.Append(ctx, Event{
		ID:                 req.EventID,
		ProjectID:          l.projectID,
		WorkflowInstanceID: instance,
		SchemaID:           schema.ID,
		AgentSessionID:     req.AgentSessionID,
		OwnerAgentID:       req.OwnerAgentID,
		ClosesEventID:      req.ClosesEventID,
		ArtifactID:         req.ArtifactID,
		Payload:            payload,
	})
}

// describeSource renders where a DSN came from, for an error message. Never the
// DSN itself.
func describeSource(source string) string {
	if source == "" {
		return "the configured source"
	}
	return source
}
