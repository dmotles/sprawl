package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The Ledger is the surface every caller outside this package uses, and its
// single most important property is that IT CANNOT BREAK AN AGENT.
//
// Every emitter is a subscriber on an agent's runtime EventBus or a step inside
// spawn/retire. A panic there takes the agent's turn with it; an error there
// propagates into an operation that has nothing to do with the event log. So a
// nil or disabled Ledger is not a special case to be checked at each of a dozen
// call sites — it is a working object whose methods do nothing. The nil-safety
// tests below are the mechanism behind "agents never brick on the store", not
// defensive decoration.

// TestNilLedger_EveryMethodIsSafeAndSilent pins that a nil *Ledger behaves.
//
// nil is the value every call site gets when the feature is off, which is the
// DEFAULT and therefore the overwhelmingly common case. If any method paniced or
// errored on nil, enabling nothing at all would break the fleet.
func TestNilLedger_EveryMethodIsSafeAndSilent(t *testing.T) {
	var l *Ledger
	ctx := context.Background()

	if l.Enabled() {
		t.Error("a nil Ledger must report itself disabled")
	}
	if got := l.ProjectID(); got != uuid.Nil {
		t.Errorf("a nil Ledger's ProjectID = %s, want the nil uuid", got)
	}
	if seq, err := l.Append(ctx, Event{}); err != nil || seq != 0 {
		t.Errorf("a nil Ledger's Append = (%d, %v), want (0, nil): a disabled store must not surface errors into an agent's operation", seq, err)
	}
	if seq, err := l.Emit(ctx, EmitRequest{TypeName: "run_started", TypeVersion: 1}); err != nil || seq != 0 {
		t.Errorf("a nil Ledger's Emit = (%d, %v), want (0, nil)", seq, err)
	}
	if src := l.DSNSource(); src != "" {
		t.Errorf("a nil Ledger's DSNSource = %q, want empty", src)
	}
	l.Close() // must not panic
}

func TestOpen_DisabledReturnsNoLedgerAndNoError(t *testing.T) {
	l, err := Open(context.Background(), LedgerConfig{Enabled: false, DSN: "postgres://ignored/db"})
	if err != nil {
		t.Fatalf("opening a disabled store must not error: %v", err)
	}
	if l != nil {
		t.Error("a disabled store must yield a nil Ledger, so every call site takes the do-nothing path")
	}
	if l.Enabled() {
		t.Error("the returned Ledger reports itself enabled")
	}
}

// TestOpen_EnabledWithoutDSNFailsLoudlyWithAHint is the loud half of the
// resolution story.
//
// ResolveDSN treats an absent DSN as a quiet non-error, because the store is
// opt-in and every invocation resolves it. But once someone has explicitly
// turned the feature ON, a missing DSN is a misconfiguration, and silently
// running with the store disabled is the worst outcome: the operator believes
// events are being recorded and nothing says otherwise until they query an
// empty log.
func TestOpen_EnabledWithoutDSNFailsLoudlyWithAHint(t *testing.T) {
	_, err := Open(context.Background(), LedgerConfig{Enabled: true, DSN: ""})
	if err == nil {
		t.Fatal("enabling the store with no DSN must fail loudly, not silently disable it")
	}
	var hint *HintError
	if !errors.As(err, &hint) {
		t.Fatalf("the error must carry a next action; got %T: %v", err, err)
	}
	if !strings.Contains(hint.Hint, EnvDSN) {
		t.Errorf("the hint should name %s so the caller knows where to put the DSN; got: %q", EnvDSN, hint.Hint)
	}
	if !strings.Contains(hint.Hint, SecretsFileName) {
		t.Errorf("the hint should also name the secrets file; got: %q", hint.Hint)
	}
}

// TestEmitRequest_RequiresAnExplicitVersion pins that there is no "latest" in
// this API at all.
//
// Appendix B item 6 requires an emit call to carry a pinned schema. A name alone
// would have to mean "latest", and the moment it did, a schema bump would change
// the validation applied to in-flight instances. Refusing a zero version makes
// the pin structural rather than a convention someone has to remember.
func TestEmitRequest_RequiresAnExplicitVersion(t *testing.T) {
	// enabled: true is required for this assertion to reach the pin check at
	// all — a DISABLED Ledger correctly no-ops before validating anything, and
	// a fixture that forgot this would pass the test while measuring the
	// disabled path.
	l := &Ledger{registry: mustSeedRegistry(t), enabled: true}
	_, err := l.Emit(context.Background(), EmitRequest{TypeName: "run_started"})
	if err == nil {
		t.Fatal("Emit with no TypeVersion must be refused: a name without a version can only mean `latest`, which the design forbids")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("the error should say the version is what is missing; got: %v", err)
	}
}

func TestEmit_UnknownTypeNameIsRefused(t *testing.T) {
	l := &Ledger{registry: mustSeedRegistry(t), enabled: true}
	_, err := l.Emit(context.Background(), EmitRequest{TypeName: "no_such_type", TypeVersion: 1})
	if err == nil {
		t.Fatal("Emit with an unknown type name must be refused")
	}
	if !strings.Contains(err.Error(), "no_such_type") {
		t.Errorf("the error should name the type; got: %v", err)
	}
}

func mustSeedRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	return reg
}

// TestEmit_MarshalsPayloadAndValidatesBeforeTouchingTheDatabase pins that Emit
// goes through the same validation the appender does, so the convenience wrapper
// cannot become a way around it.
func TestEmit_MarshalsPayloadAndValidatesBeforeTouchingTheDatabase(t *testing.T) {
	pool := newRecordingPool()
	reg := mustSeedRegistry(t)
	l := &Ledger{
		registry:  reg,
		appender:  NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: &capturingSpiller{}}),
		projectID: uuid.New(),
		enabled:   true,
	}

	// run_started requires session_id, which this payload omits.
	_, err := l.Emit(context.Background(), EmitRequest{
		TypeName:    "run_started",
		TypeVersion: 1,
		Payload:     map[string]any{"agent_name": "finn", "agent_type": "engineer"},
	})
	if !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("got err=%v, want ErrSchemaViolation — Emit must not be a way around validation", err)
	}
	if calls := pool.log(); len(calls) != 0 {
		t.Errorf("a rejected Emit touched the database: %v", calls)
	}

	// Control: the corrected payload goes through.
	if _, err := l.Emit(context.Background(), EmitRequest{
		TypeName:    "run_started",
		TypeVersion: 1,
		Payload:     map[string]any{"agent_name": "finn", "agent_type": "engineer", "session_id": "s-1"},
	}); err != nil {
		t.Fatalf("control: a valid Emit must succeed: %v", err)
	}
	if indexOf(pool.log(), "insert_event") < 0 {
		t.Errorf("control: a valid Emit did not insert: %v", pool.log())
	}
}

// TestEmit_MintsAWorkflowInstanceWhenTheCallerHasNone pins that a caller with no
// workflow context still produces a valid event.
//
// events.workflow_instance_id is NOT NULL, and the M1a emitters (run started,
// turn finished, spawn, retire) are pure telemetry with no workflow to belong
// to. Without this, every lifecycle append would fail the NOT NULL constraint —
// and it would fail only against a real database, so no hermetic test would
// notice.
func TestEmit_MintsAWorkflowInstanceWhenTheCallerHasNone(t *testing.T) {
	pool := newRecordingPool()
	reg := mustSeedRegistry(t)
	l := &Ledger{
		registry:  reg,
		appender:  NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: &capturingSpiller{}}),
		projectID: uuid.New(),
		enabled:   true,
	}
	if _, err := l.Emit(context.Background(), EmitRequest{
		TypeName:    "turn_finished",
		TypeVersion: 1,
		Payload:     map[string]any{"session_id": "s", "input_tokens": 1, "output_tokens": 2},
	}); err != nil {
		t.Fatalf("Emit with no WorkflowInstanceID must still succeed: %v", err)
	}
}

// TestDisabledLedger_EmitShortCircuitsBeforeValidation pins that a disabled
// Ledger no-ops BEFORE it inspects the request at all.
//
// Written because a fixture bug in this file initially constructed a Ledger
// without enabled:true, and two validation assertions passed for the wrong
// reason — they were measuring the disabled path. That is worth an assertion of
// its own in both directions: a disabled store must not reject a bad request
// (there is nothing to reject into), and an enabled one must.
func TestDisabledLedger_EmitShortCircuitsBeforeValidation(t *testing.T) {
	disabled := &Ledger{registry: mustSeedRegistry(t)}
	if seq, err := disabled.Emit(context.Background(), EmitRequest{TypeName: "no_such_type", TypeVersion: 0}); err != nil || seq != 0 {
		t.Errorf("a disabled Ledger returned (%d, %v) for a request that is invalid twice over; it must no-op", seq, err)
	}

	// Control: enabled, same request, is refused. Without this the assertion
	// above is satisfied by a Ledger that never validates anything.
	enabled := &Ledger{registry: mustSeedRegistry(t), enabled: true}
	if _, err := enabled.Emit(context.Background(), EmitRequest{TypeName: "no_such_type", TypeVersion: 0}); err == nil {
		t.Error("an enabled Ledger accepted a request with no version and an unknown type")
	}
}

// TestOpen_UnreachableDatabaseYieldsADegradedLedgerNotAnError is AC5's
// precondition, and the reason Open cannot simply fail.
//
// With no Ledger there is no spiller, so telemetry would be silently DROPPED
// rather than spilled. That is the one outcome the degraded-mode requirement
// rules out, so an unreachable database has to produce a working object whose
// behaviour differs — not an error the caller has to interpret.
//
// Hermetic: port 1 on loopback refuses immediately, so this needs no container.
func TestOpen_UnreachableDatabaseYieldsADegradedLedgerNotAnError(t *testing.T) {
	root := t.TempDir()
	l, err := Open(context.Background(), LedgerConfig{
		Enabled:    true,
		DSN:        "postgres://nobody@127.0.0.1:1/nothing?sslmode=disable&connect_timeout=1",
		DSNSource:  EnvDSN,
		RemoteURL:  "https://example.invalid/degraded",
		SprawlRoot: root,
	})
	if err != nil {
		t.Fatalf("an unreachable database must not fail Open — the caller would then have no spiller and telemetry would be dropped: %v", err)
	}
	if l == nil {
		t.Fatal("Open returned no Ledger for an unreachable database, so every emitter would silently no-op instead of spilling")
	}
	t.Cleanup(l.Close)

	if !l.Enabled() {
		t.Error("a degraded Ledger must report itself ENABLED — it is spilling, which is doing something")
	}
	if l.DegradedError() == nil {
		t.Error("DegradedError() is nil, so no diagnostic surface could report the outage")
	}

	// The spill leg: telemetry lands on disk and the caller sees no error.
	if _, err := l.Emit(context.Background(), EmitRequest{
		TypeName:    "run_started",
		TypeVersion: 1,
		Payload:     map[string]any{"agent_name": "finn", "agent_type": "engineer", "session_id": "s-1"},
	}); err != nil {
		t.Errorf("telemetry against a degraded Ledger returned an error to its emitter: %v", err)
	}
	entries, readErr := os.ReadDir(SpillDir(root))
	if readErr != nil {
		t.Fatalf("no spill directory was created, so the telemetry event was silently dropped: %v", readErr)
	}
	if len(entries) == 0 {
		t.Error("the spill directory is empty — the event was neither stored nor spilled")
	}

	// The loud leg: coordination is refused, with a hint.
	_, goalErr := l.Emit(context.Background(), EmitRequest{
		TypeName:    "goal_opened",
		TypeVersion: 1,
		Payload:     map[string]any{"goal_type": "RESEARCH", "text": "x"},
	})
	if !errors.Is(goalErr, ErrDegraded) {
		t.Errorf("opening a goal against a degraded Ledger: got err=%v, want ErrDegraded", goalErr)
	}
}

// TestOpen_ARefusedDatabaseIsNotDegradedMode pins the other side of the split.
//
// Degraded mode is for "unreachable". A database that ANSWERS and refuses — bad
// credentials, a permission error, a migration that will not apply — is a real
// misconfiguration, and hiding it behind a quietly growing spill directory would
// mean nobody discovers it until they query an empty log. A malformed DSN is the
// cheapest reachable instance of "not a transport failure".
func TestOpen_ARefusedDatabaseIsNotDegradedMode(t *testing.T) {
	_, err := Open(context.Background(), LedgerConfig{
		Enabled:    true,
		DSN:        "this is not a dsn at all",
		DSNSource:  EnvDSN,
		RemoteURL:  "https://example.invalid/bad-dsn",
		SprawlRoot: t.TempDir(),
	})
	if err == nil {
		t.Error("an unparseable DSN must be an error, not degraded mode — degraded mode would hide a permanent misconfiguration behind a growing spill directory")
	}
}
