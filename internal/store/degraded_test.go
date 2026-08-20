package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Degraded mode as a STATE, not as a per-append discovery.
//
// This exists because of a design problem that only shows up when you write the
// AC5 path out: if Open FAILS when the database is unreachable, the caller gets
// no Ledger — and with no Ledger there is no spiller, so telemetry is silently
// dropped rather than spilled, which is exactly what the requirement forbids. So
// an unreachable database at Open time yields a Ledger in a DEGRADED state
// instead of an error.
//
// The second half matters just as much: a degraded Ledger must not attempt a
// connection per event. A dial timeout is seconds, every agent turn produces
// events, and the emitters run on agents' own subscriber goroutines — so
// retrying per append would convert "the database is down" into "every agent is
// wedged", which is the precise outcome "agents never brick on the store" rules
// out.

func newDegradedLedger(t *testing.T, spill Spiller) (*Ledger, *recordingPool) {
	t.Helper()
	pool := newRecordingPool()
	// A dial error that would take a real connection attempt many seconds.
	pool.beginErr = errConnRefused
	reg := mustSeedRegistry(t)
	return &Ledger{
		enabled:     true,
		registry:    reg,
		projectID:   uuid.Nil, // unknown: the project row could not be read
		degradedErr: errConnRefused,
		appender: NewAppender(AppenderDeps{
			Pool: pool, Registry: reg, Spill: spill,
			Degraded:  errConnRefused,
			RemoteURL: "https://example.invalid/repo",
		}),
	}, pool
}

// TestDegraded_TelemetrySpillsWithoutTouchingTheConnection is the AC5 spill leg,
// plus the no-retry property.
func TestDegraded_TelemetrySpillsWithoutTouchingTheConnection(t *testing.T) {
	spill := &capturingSpiller{}
	l, pool := newDegradedLedger(t, spill)

	seq, err := l.Emit(context.Background(), EmitRequest{
		TypeName:    "turn_finished",
		TypeVersion: 1,
		Payload:     map[string]any{"session_id": "s", "input_tokens": 1, "output_tokens": 2},
	})
	if err != nil {
		t.Fatalf("a spillable event on a degraded Ledger must not surface an error: %v", err)
	}
	if seq != 0 {
		t.Errorf("seq = %d, want 0 — a spilled event has no log position", seq)
	}
	if spill.count() != 1 {
		t.Fatalf("spill holds %d record(s), want 1 — the event was neither stored nor spilled, which is a silent drop", spill.count())
	}
	if calls := pool.log(); len(calls) != 0 {
		t.Errorf("a known-degraded Ledger attempted a connection anyway (%v). A dial timeout per event, on agents' own subscriber goroutines, turns a database outage into a wedged fleet", calls)
	}
}

// TestDegraded_GoalOpenFailsLoudlyWithAHint is the AC5 loud leg.
//
// A goal is cross-host coordination. One recorded only in a local spill file is
// invisible to every other host and to the sweeper, so it reads as work nobody is
// doing while the agent that opened it believes it was recorded. That is worse
// than a refusal, which is why this is the one operation that fails.
func TestDegraded_GoalOpenFailsLoudlyWithAHint(t *testing.T) {
	spill := &capturingSpiller{}
	l, _ := newDegradedLedger(t, spill)

	_, err := l.Emit(context.Background(), EmitRequest{
		TypeName:    "goal_opened",
		TypeVersion: 1,
		Payload:     map[string]any{"goal_type": "RESEARCH", "text": "find the thing"},
	})
	if !errors.Is(err, ErrDegraded) {
		t.Fatalf("got err=%v, want ErrDegraded", err)
	}
	var hint *HintError
	if !errors.As(err, &hint) {
		t.Fatalf("the failure must carry a next action for the agent that hit it; got %T: %v", err, err)
	}
	if hint.Hint == "" {
		t.Error("the HintError carries no hint")
	}
	if spill.count() != 0 {
		t.Errorf("a goal-open spilled %d record(s) on a degraded Ledger", spill.count())
	}
	// The underlying cause must survive for diagnosis.
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("the error drops the underlying cause, so an operator cannot tell an outage from a misconfiguration: %v", err)
	}
}

// TestDegraded_GoalCloseAlsoFailsLoudly pins the other half of the contract
// pair. Closing is as much coordination as opening — a close that only exists
// locally leaves the contract open everywhere else, so the sweeper keeps poking
// an agent that believes it has finished.
func TestDegraded_GoalCloseAlsoFailsLoudly(t *testing.T) {
	l, _ := newDegradedLedger(t, &capturingSpiller{})
	closed := uuid.New()
	_, err := l.Emit(context.Background(), EmitRequest{
		TypeName:      "goal_closed",
		TypeVersion:   1,
		ClosesEventID: &closed,
		Payload:       map[string]any{"outcome": "success"},
	})
	if !errors.Is(err, ErrDegraded) {
		t.Fatalf("got err=%v, want ErrDegraded", err)
	}
}

// TestDegraded_StillValidatesPayloads pins that degraded mode does not become a
// hole in validation.
//
// If it did, an outage would let malformed telemetry into the spill file, and
// every one of those records would dead-letter on replay — turning a transient
// outage into permanent data loss that nobody notices until they read the
// dead-letter directory.
func TestDegraded_StillValidatesPayloads(t *testing.T) {
	spill := &capturingSpiller{}
	l, _ := newDegradedLedger(t, spill)

	_, err := l.Emit(context.Background(), EmitRequest{
		TypeName:    "run_started",
		TypeVersion: 1,
		Payload:     map[string]any{"agent_name": "finn"}, // missing two required fields
	})
	if !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("got err=%v, want ErrSchemaViolation — degraded mode must not bypass validation", err)
	}
	if spill.count() != 0 {
		t.Errorf("an invalid payload was spilled (%d record(s)); it would dead-letter on replay, turning a transient outage into silent data loss", spill.count())
	}
}

// TestDegraded_SpillRecordCarriesTheRemoteURL pins that a spilled record can be
// resolved to a project on replay.
//
// A degraded Ledger never read the projects row, so it does not know the project
// id — the record carries uuid.Nil for it. Without the remote URL, which IS a
// project's identity, a replayer would have nothing to resolve against and every
// spilled record would dead-letter.
func TestDegraded_SpillRecordCarriesTheRemoteURL(t *testing.T) {
	spill := &capturingSpiller{}
	l, _ := newDegradedLedger(t, spill)

	if _, err := l.Emit(context.Background(), EmitRequest{
		TypeName:    "run_started",
		TypeVersion: 1,
		Payload:     map[string]any{"agent_name": "finn", "agent_type": "engineer", "session_id": "s"},
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if spill.count() != 1 {
		t.Fatalf("spill holds %d record(s), want 1", spill.count())
	}
	rec := spill.records[0]
	if rec.ProjectID != uuid.Nil {
		t.Errorf("ProjectID = %s; a degraded Ledger never read the projects row and must not invent an id", rec.ProjectID)
	}
	if rec.RemoteURL != "https://example.invalid/repo" {
		t.Errorf("RemoteURL = %q; without it a replayer cannot resolve which project this event belongs to and the record dead-letters", rec.RemoteURL)
	}
}

// TestDegraded_ReportsItselfDegraded pins that the state is observable, so
// `sprawl store doctor` can say so and a caller can decide.
func TestDegraded_ReportsItselfDegraded(t *testing.T) {
	l, _ := newDegradedLedger(t, &capturingSpiller{})
	if !l.Enabled() {
		t.Error("a degraded Ledger is still ENABLED — it is spilling, which is doing something")
	}
	if l.DegradedError() == nil {
		t.Error("DegradedError() returned nil on a degraded Ledger, so no diagnostic surface can report the outage")
	}

	// Control: a healthy Ledger reports no degradation. Without this leg, a
	// DegradedError that always returned non-nil would pass the assertion above.
	healthy, _, _ := newCapturingLedger(t)
	if healthy.DegradedError() != nil {
		t.Errorf("a healthy Ledger reports itself degraded: %v", healthy.DegradedError())
	}
	var nilLedger *Ledger
	if nilLedger.DegradedError() != nil {
		t.Error("a nil Ledger must not report itself degraded — it is DISABLED, which is a different thing")
	}
}

// TestAppender_DegradedSkipsTheTransactionEntirely is the appender-level
// counterpart, asserted separately because the Appender is what other callers
// (M1b's dispatcher) will hold.
func TestAppender_DegradedSkipsTheTransactionEntirely(t *testing.T) {
	reg := mustSeedRegistry(t)
	pool := newRecordingPool()
	spill := &capturingSpiller{}
	a := NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: spill, Degraded: errConnRefused})

	if _, err := a.Append(context.Background(), Event{
		ProjectID:          uuid.New(),
		WorkflowInstanceID: uuid.New(),
		SchemaID:           mustSchema(t, reg, "run_started").ID,
		Payload:            json.RawMessage(`{"agent_name":"f","agent_type":"e","session_id":"s"}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if calls := pool.log(); len(calls) != 0 {
		t.Errorf("a degraded Appender touched the pool: %v", calls)
	}
	if spill.count() != 1 {
		t.Errorf("spill holds %d record(s), want 1", spill.count())
	}
}
