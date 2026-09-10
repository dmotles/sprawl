// goalopen.go — opening a goal (QUM-1252, M3a slice 10b).
//
// This is the write that starts everything. `create_goal` calls it, and that is
// ALL create_goal does: the tool appends `goal_opened` and returns. It does not
// spawn anybody.
//
// That is the log-driven shape, and it is a decision rather than an omission.
// The alternative — have create_goal spawn the agent inline and then record what
// it did — puts the side effect before the log, so a host that dies between the
// two leaves a running agent no event mentions, or an event no agent backs.
// Going through the log first means the goal is a durable fact the moment the
// tool returns, and the dispatcher drives the spawn from it with the
// act-once machinery (event_claims) that already exists. A crash before the
// spawn is then a goal the dispatcher picks up on the next pass, not a
// reconciliation problem.
//
// The cost is real and worth naming: create_goal returning success means the
// GOAL is recorded, not that anyone is working it yet. Anything that reports to
// a human has to say that honestly rather than implying a running agent.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrUnmigratedGoalType is returned when a goal type has no workflow definition
// driving it yet.
//
// A sentinel rather than a bare message because the caller has a genuine branch
// to take on it — this is the one refusal with a working alternative, and the
// alternative is the legacy `spawn` path. The guidance lives in the sentinel's
// own text so it survives every wrapping: a caller that only sees the outermost
// message still learns where to go.
var ErrUnmigratedGoalType = errors.New("no workflow definition drives it, so opening it would create a contract nothing can close; use the legacy `spawn` path for this work")

// GoalType is the closed set of goal types the engine drives.
//
// Closed, and not the free string goal_opened.json permits, for the reason
// GoalOutcome is closed: the seed's validator implements a keyword subset with
// no `enum`, so this is the only layer that can hold the line. The difference
// here is what a miss costs — an unrecognised outcome is a bad row in a report,
// while an unrecognised goal type is a contract that opens, sits in
// open_contracts forever, and is driven by nothing.
type GoalType string

const (
	// GoalResearch and GoalBugInvestigation are the two types migrated to the
	// engine in M3a. The strings match the workflow definition names in
	// internal/engine (ResearchWorkflowName, BugInvestigationWorkflowName) —
	// that is the join between a goal and the definition that drives it.
	GoalResearch         GoalType = "research"
	GoalBugInvestigation GoalType = "bug_investigation"
)

// MigratedGoalTypes is the routing table's engine side, in the order a human
// should be offered them.
//
// Migration is type-by-type with no flag day: every type NOT in this list still
// goes through the legacy prose `spawn` path, which stays fully functional. Add
// a type here only when a workflow definition exists to drive it.
var MigratedGoalTypes = []GoalType{GoalResearch, GoalBugInvestigation}

func migratedGoalType(gt GoalType) bool {
	for _, v := range MigratedGoalTypes {
		if v == gt {
			return true
		}
	}
	return false
}

// OpenedGoal is what the caller needs to refer to the goal afterwards.
//
// Both ids are returned because they answer different questions and the caller
// cannot derive either from the other: GoalEventID is the contract that
// report_result closes, WorkflowID is the instance get_workflow_log reads.
type OpenedGoal struct {
	GoalEventID uuid.UUID
	WorkflowID  uuid.UUID
	GoalType    GoalType
}

// OpenGoal appends a `goal_opened` event and returns its ids.
//
// It mints the workflow instance id itself rather than letting Emit mint one,
// because Emit returns a seq and the instance id would otherwise be
// unrecoverable without reading the event back — and the caller needs it in the
// same breath.
func (l *Ledger) OpenGoal(ctx context.Context, goalType GoalType, text, owner string) (OpenedGoal, error) {
	// The disabled check is NOT redundant with Emit's. Emit is disabled-safe by
	// design: with the flag off it records nothing and returns (0, nil), which
	// is right for telemetry and wrong here. This function mints its ids BEFORE
	// emitting, so inheriting that behaviour would hand back a well-formed goal
	// id for an event that does not exist, and every later reference to it would
	// come back empty for a reason nobody could see.
	if !l.Enabled() {
		return OpenedGoal{}, fmt.Errorf("store: the event log is disabled on this host, so a goal cannot be recorded; enable it with `sprawl config set event_log.enabled true`")
	}
	if !migratedGoalType(goalType) {
		return OpenedGoal{}, fmt.Errorf("store: %q is not an engine-driven goal type (those are %v): %w", goalType, MigratedGoalTypes, ErrUnmigratedGoalType)
	}
	if text == "" {
		return OpenedGoal{}, fmt.Errorf("store: a %s goal needs text; it is the entire task the spawned agent will be given", goalType)
	}
	if owner == "" {
		// Optional in the seed, required here. `owner` is who OWNER_NOTIFY is
		// addressed to when the goal closes; without it the notify handler falls
		// through to the dispatcher's host-level FallbackOwner, so the result
		// would be announced to whoever that host happens to name rather than to
		// whoever asked for the work.
		return OpenedGoal{}, fmt.Errorf("store: a %s goal needs an owner; it is who the result is reported to when the goal closes", goalType)
	}

	g := OpenedGoal{GoalEventID: uuid.New(), WorkflowID: uuid.New(), GoalType: goalType}
	// The brief may be a whole file's contents (QUM-1347), so it goes through
	// the spill: short text inline, long text to an artifact the event
	// references. The artifact is written FIRST — an event referencing a row
	// that does not exist reads exactly like a lost one.
	payload := map[string]any{"goal_type": string(goalType), "owner": owner}
	artifactID, err := l.putTextField(ctx, payload, "text", artifactKindGoalText, text)
	if err != nil {
		return OpenedGoal{}, fmt.Errorf("store: opening a %s goal: %w", goalType, err)
	}
	if _, err := l.Emit(ctx, EmitRequest{
		TypeName:           "goal_opened",
		TypeVersion:        1,
		EventID:            g.GoalEventID,
		WorkflowInstanceID: g.WorkflowID,
		ArtifactID:         artifactID,
		Payload:            payload,
	}); err != nil {
		// Deliberately not wrapped in a friendlier message: a degraded store
		// returns a HintError with the operator's next action attached, and
		// re-wrapping loses nothing but adding a second explanation on top of it
		// buries the one that names a remedy.
		return OpenedGoal{}, fmt.Errorf("store: opening a %s goal: %w", goalType, err)
	}
	return g, nil
}
