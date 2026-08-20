package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
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

	// isTransportFailure is what separates "the database is unreachable" from
	// "the database refused this". Only the first is survivable: a rejected
	// migration or a permission error is a real misconfiguration that degraded
	// mode would hide behind a growing spill directory.
	if err := Migrate(ctx, cfg.DSN); err != nil {
		if isTransportFailure(err) {
			return degraded(err)
		}
		return nil, err
	}
	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		if isTransportFailure(err) {
			return degraded(err)
		}
		return nil, fmt.Errorf("store: connecting to the event log: %w", err)
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

// ensureProject registers the project by remote URL, returning its id and
// whether this call created it.
//
// remote_url is UNIQUE, so two hosts enabling the store concurrently converge on
// one project rather than racing to create two. The DO UPDATE is a no-op that
// exists only so RETURNING yields a row in the already-exists case; DO NOTHING
// would return no rows and the second host would see "no rows in result set".
//
// `xmax = 0` distinguishes an inserted row from an updated one. It is a
// Postgres implementation detail (a freshly inserted tuple has no deleting
// transaction id) rather than portable SQL, and it is used because the obvious
// alternatives are both wrong here: `excluded` is not in scope in RETURNING, and
// a SELECT-then-INSERT would report "created" on whichever of two concurrent
// hosts lost the race. Its only consumer is whether to emit repo_initialized, so
// the blast radius of it being wrong is one duplicate or one missing marker
// event, not a correctness failure.
func ensureProject(ctx context.Context, pool PgPool, remoteURL string) (uuid.UUID, bool, error) {
	var id uuid.UUID
	var created bool
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (id, remote_url, created_at) VALUES ($1, $2, now())
		 ON CONFLICT (remote_url) DO UPDATE SET remote_url = projects.remote_url
		 RETURNING id, (xmax = 0)`,
		uuid.New(), remoteURL).Scan(&id, &created); err != nil {
		return uuid.Nil, false, fmt.Errorf("store: registering project: %w", err)
	}
	return id, created, nil
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

// Close releases the pool. Safe on nil and idempotent.
func (l *Ledger) Close() {
	if l == nil || l.pool == nil {
		return
	}
	l.pool.Close()
	l.pool = nil
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
