package engine

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/store"
)

// The BUG_INVESTIGATION workflow (QUM-1252, M3a) — the second goal type, and
// the one that proves REWORK.
//
// RESEARCH is a straight line: every step either happens or the goal is stuck.
// BUG_INVESTIGATION is the first workflow whose owner can look at a finished
// result and say "no". That backward edge is the whole reason this definition
// exists alongside RESEARCH, and it is what exercises Recover's backtrack and
// re-engagement paths against a real definition rather than a fixture.

// BugInvestigationWorkflowName and BugInvestigationWorkflowVersion are the pin.
const (
	BugInvestigationWorkflowName    = "bug_investigation"
	BugInvestigationWorkflowVersion = 1
)

const bugInvestigationPrompt = `Bug investigation goal.

Find the ROOT CAUSE, not the first thing that makes the symptom go away. Report
the mechanism: what happens, in what order, and why that produces what was
observed.

If you cannot reproduce it, say so and report what you ruled out — a narrowed
search is a result. A plausible story that was never reproduced is not, and it
will be sent back.`

// BugInvestigationDefinition builds the BUG_INVESTIGATION workflow.
//
// THE STEPS:
//
//	spawn-investigator  awaits spawn_committed  — as in RESEARCH: the write-ahead's
//	                                              close, so the step means "the
//	                                              agent exists".
//	await-diagnosis     awaits goal_closed      — the RESULT.
//	verify-diagnosis    awaits notify_acked     — the owner's ACCEPTANCE. The
//	                                              owner acking is what completes
//	                                              the goal; the owner appending
//	                                              rework_requested instead is the
//	                                              step's FAILURE, and that is the
//	                                              REWORK path below.
//
// This is why the goal is not complete at goal_closed. A diagnosis nobody
// accepted is a claim, and closing on it would make "investigated" and
// "understood" the same state.
//
// THE REWORK PATH. verify-diagnosis backtracks to spawn-investigator rather
// than escalating, because a rejected diagnosis has a well-defined next action —
// investigate again — and escalating would hand the owner back the decision they
// just made. The rework's own `rework_requested` event carries
// `follows_event_id` pointing at the rejected attempt, so the second
// investigation is linked to the first rather than looking like an unrelated
// goal.
//
// It backtracks with DiscardAndRedo, and that is the load-bearing choice: the
// investigator that produced the rejected diagnosis is the one whose model of
// the bug is already wrong, and re-engaging it with its own context is how a
// wrong theory survives a rework. RESEARCH's spawn step keeps the default
// ReEngageOriginal for the opposite reason — a spawn that failed on a busy host
// has no wrong theory to discard.
//
// The backtrack is deliberately UNCAPPED at the definition level: the cap on
// rework rounds belongs to the owner, who has to keep asking for it, rather than
// to a constant here that would silently strand a goal mid-argument. A rework
// loop is visible in the log by construction, which is not true of a retry.
func BugInvestigationDefinition(reg *store.Registry, investigatorCard uuid.UUID) (Definition, error) {
	if investigatorCard == uuid.Nil {
		return Definition{}, fmt.Errorf("engine: the bug_investigation workflow needs an investigator agent card; uuid.Nil means the step spawns nobody, so every bug goal would wait forever for a spawn_committed that nothing emits")
	}

	resolve, resolveErr := schemaResolver(reg, BugInvestigationWorkflowName)

	d := Definition{
		Name:               BugInvestigationWorkflowName,
		Version:            BugInvestigationWorkflowVersion,
		TriggerEventSchema: resolve("goal_opened"),
		Steps: []Step{
			{
				Name:             "spawn-investigator",
				TriggerEventType: resolve("spawn_committed"),
				AgentCard:        investigatorCard,
				PromptTemplate:   bugInvestigationPrompt,
				Outcome:          OutcomePolicy{OnFailure: ActionRetry, MaxRetries: 3},
			},
			{
				Name:             "await-diagnosis",
				TriggerEventType: resolve("goal_closed"),
				// Escalate: if the investigator gave up rather than reported,
				// retrying re-runs the WAIT, not the investigation.
				Outcome: OutcomePolicy{OnFailure: ActionEscalate},
			},
			{
				Name:             "verify-diagnosis",
				TriggerEventType: resolve("notify_acked"),
				Outcome: OutcomePolicy{
					OnFailure:   ActionBacktrack,
					BacktrackTo: "spawn-investigator",
				},
				ReEngagement: DiscardAndRedo,
			},
		},
	}
	if err := resolveErr(); err != nil {
		return Definition{}, err
	}
	if err := d.Validate(); err != nil {
		return Definition{}, err
	}
	return d, nil
}
