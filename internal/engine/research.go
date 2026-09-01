package engine

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/store"
)

// The RESEARCH workflow (QUM-1252, M3a) — the first goal type the engine drives
// end to end.
//
// RESEARCH is first because it is READ-MOSTLY: it needs no merge machinery, so
// the spine (goal -> claim -> card-resolved spawn -> result -> owner notified
// -> closed) can be proven without also proving the change-goal family, which
// is M3b.

// ResearchWorkflowName and ResearchWorkflowVersion are the pin. Instances carry
// both, so editing this definition never changes the shape of a goal already in
// flight — bump the version rather than the steps.
const (
	ResearchWorkflowName    = "research"
	ResearchWorkflowVersion = 1
)

// researchPrompt is the researcher's task text.
//
// It names the ASK path explicitly because the alternative failure is
// expensive and quiet: a researcher that cannot resolve an ambiguity and does
// not know it may ask will guess, and a confidently wrong report is worse than
// a blocked goal, which at least the sweeper can see.
const researchPrompt = `Research goal.

Answer the questions in your goal against the requested sources. Report what you
found AND what you could not establish — an unanswered question reported as such
is a result; an unanswered question answered by guessing is not.

If the goal is ambiguous, ask rather than assume: an open question is visible
work, a wrong assumption is invisible until it has been built on.`

// ResearchDefinition builds the RESEARCH workflow, resolving every event type
// through the seed registry so the definition carries PINNED schema ids.
//
// Resolved rather than hard-coded because a literal uuid in this file and the
// derivation in seeds.go would be two sources of truth for one id, and the
// derivation is the one the appender uses. An unresolvable name is an error
// here, at construction, rather than a uuid.Nil trigger that no event can ever
// match — that failure mode is a goal that hangs forever with nothing logged.
//
// THE STEPS, and why the sequence is what it is:
//
//	spawn-researcher  awaits spawn_committed  — the write-ahead's close. Waiting
//	                                            on the COMMIT rather than on the
//	                                            request is what makes the step
//	                                            mean "the agent exists".
//	await-result      awaits goal_closed      — the RESULT. Under the generic
//	                                            contract pair the result and the
//	                                            close are one event, carrying
//	                                            outcome: success|failure|
//	                                            aborted|superseded.
//	notify-owner      awaits notify_acked     — owner_notify is emitted by the
//	                                            existing M1b dispatcher wiring on
//	                                            goal_closed; this step waits for
//	                                            the ACK, so a goal is not complete
//	                                            until its owner has actually seen
//	                                            the result. That is the whole
//	                                            point of "never let a RESULT land
//	                                            unobserved".
//
// No step carries a Guard, and that is deliberate rather than unfinished:
// Advance already refuses an event whose WorkflowInstanceID is not this
// instance's, and every event of one goal shares that id. A guard re-checking
// "is this my goal" would be a second, weaker copy of a check that already
// holds.
func ResearchDefinition(reg *store.Registry, researcherCard uuid.UUID) (Definition, error) {
	if researcherCard == uuid.Nil {
		return Definition{}, fmt.Errorf("engine: the research workflow needs a researcher agent card; uuid.Nil means the step spawns nobody, so every research goal would wait forever for a spawn_committed that nothing emits")
	}

	// resolve fails the whole construction on the FIRST miss, naming it. A
	// constructor that collected misses would still have to refuse, and the
	// first unresolvable name is the actionable one.
	resolve, resolveErr := schemaResolver(reg, ResearchWorkflowName)

	d := Definition{
		Name:               ResearchWorkflowName,
		Version:            ResearchWorkflowVersion,
		TriggerEventSchema: resolve("goal_opened"),
		Steps: []Step{
			{
				Name:             "spawn-researcher",
				TriggerEventType: resolve("spawn_committed"),
				AgentCard:        researcherCard,
				PromptTemplate:   researchPrompt,
				// A spawn is worth retrying: the common failures are a busy
				// host or a transient worktree error, neither of which is a
				// reason to abandon the goal. Three, then escalate rather than
				// loop — a host that has failed three spawns is not going to
				// succeed on the fourth.
				Outcome: OutcomePolicy{OnFailure: ActionRetry, MaxRetries: 3},
			},
			{
				Name:             "await-result",
				TriggerEventType: resolve("goal_closed"),
				// Escalate, not retry: if the researcher failed, re-running the
				// step re-runs the WAIT, not the research. Whether to re-engage
				// or discard is a rework decision, and rework is a human or
				// owner call rather than something the engine retries into.
				Outcome: OutcomePolicy{OnFailure: ActionEscalate},
			},
			{
				Name:             "notify-owner",
				TriggerEventType: resolve("notify_acked"),
				// The sweeper owns re-delivery of a lost notification, with
				// backoff and a cap. A retry here would be a second, competing
				// re-delivery mechanism with no shared cap between them.
				Outcome: OutcomePolicy{OnFailure: ActionEscalate},
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
