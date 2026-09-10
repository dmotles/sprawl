// goalspawn.go — goal_opened -> spawn_requested (QUM-1252, M3a slice 10b).
//
// This is the handler that makes create_goal non-inert. create_goal appends a
// goal and returns; nothing is running when it does. The dispatcher picks the
// event up here, under the claim that makes it act-once, and turns it into a
// spawn request.
//
// It stops at spawn_requested rather than spawning. Two reasons, and the second
// is the load-bearing one:
//
//   - Launching a session needs the supervisor, which lives inside a
//     `sprawl enter` process. This handler runs in both that process AND in a
//     standalone `sprawl store dispatch`, which has no supervisor at all.
//   - spawn.go's SpawnHandler already owns the write-ahead sequence
//     (spawn_intent -> local resource -> spawn_committed) that makes a crashed
//     spawn reconcilable. Doing the side effect from here would duplicate it,
//     and a second, subtly different write-ahead is worse than none.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/google/uuid"
)

// NameAllocator hands out an unused agent name for a type.
//
// An interface, and taking the TYPE, because the name pools are partitioned per
// agent type and live in internal/agent — which this package must not import
// (the direction is supervisor -> store). Implemented in internal/dispatchadapt.
type NameAllocator interface {
	AllocateName(ctx context.Context, agentType string) (string, error)
}

// goalAgentSpec is what a goal type resolves to before a name is allocated.
//
// Type AND family together, because both are validated by the spawner and
// neither can be defaulted there: agentops.PrepareSpawn refuses an unknown
// family outright, so a request carrying only the type is a well-formed event
// that every consumer rejects.
type goalAgentSpec struct {
	agentType string
	family    string
}

// goalAgentTypes is the routing table from goal type to the kind of agent that
// runs it.
//
// A CLOSED map, and an unmapped goal type is a hard failure rather than a
// default: defaulting would put the wrong agent on the work while looking
// exactly like success, and the goal would close with a result nobody asked for.
// A failure, by contrast, leaves the goal visibly unstarted and the dispatcher
// retrying.
//
// The pairing itself is a policy decision, not a technical one, and it is one
// map because it is expected to be revised.
// The family is `engineering` for both, following the overwhelming repo
// precedent rather than a fresh judgement (QUM-1252 records the count). It is a
// one-line change per row if that turns out to be wrong.
var goalAgentTypes = map[GoalType]goalAgentSpec{
	GoalResearch: {agentType: "researcher", family: "engineering"},
	// An engineer rather than a researcher: investigating a bug means running
	// the code, reading the failure and reproducing it in a worktree, which is
	// the engineer card's whole shape. The prompt below says explicitly that the
	// deliverable is the diagnosis, because an engineer's default is to fix.
	GoalBugInvestigation: {agentType: "engineer", family: "engineering"},
}

// goalBranch names the worktree branch for an engine-spawned agent.
//
// Derived from the GOAL EVENT ID rather than from the goal text or the agent
// name alone: names come from a reused pool, so two goals driven by the same
// name at different times would otherwise ask for the same branch and the second
// spawn would fail on an existing branch.
//
// The `goal/` prefix is deliberately not the per-user branch prefix this repo's
// agents use — that is a workspace convention the engine has no way to read, and
// guessing one wrong is worse than a prefix that plainly says who created it.
func goalBranch(agentName string, goalEventID uuid.UUID) string {
	return "goal/" + agentName + "-" + goalEventID.String()[:8]
}

type GoalSpawnHandlerDeps struct {
	Emitter EventEmitter
	Names   NameAllocator
	Logger  *slog.Logger
}

// GoalSpawnHandler turns a goal_opened event into a spawn request.
type GoalSpawnHandler struct {
	emitter EventEmitter
	names   NameAllocator
	log     *slog.Logger
}

var _ Handler = (*GoalSpawnHandler)(nil)

func NewGoalSpawnHandler(d GoalSpawnHandlerDeps) (*GoalSpawnHandler, error) {
	switch {
	case d.Emitter == nil:
		return nil, fmt.Errorf("store: the goal spawn handler needs an event emitter; without one it would consume goal_opened events and report success while requesting nothing")
	case d.Names == nil:
		return nil, fmt.Errorf("store: the goal spawn handler needs a name allocator; spawn_requested requires an agent_name and the reconciler matches intents to local agents by name")
	}
	log := d.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &GoalSpawnHandler{emitter: d.Emitter, names: d.Names, log: log}, nil
}

type goalOpenedPayload struct {
	GoalType string `json:"goal_type"`
	Text     string `json:"text"`
	Owner    string `json:"owner"`
}

func (h *GoalSpawnHandler) Handle(ctx context.Context, ev DispatchedEvent) error {
	var goal goalOpenedPayload
	if err := json.Unmarshal(ev.Payload, &goal); err != nil {
		return fmt.Errorf("store: goal_opened %s has an unreadable payload: %w", ev.ID, err)
	}

	spec, ok := goalAgentTypes[GoalType(goal.GoalType)]
	if !ok {
		return fmt.Errorf("store: goal_opened %s has goal_type %q, which no agent type is mapped to (mapped: %v); refusing rather than spawning a default agent onto work nobody chose it for",
			ev.ID, goal.GoalType, mappedGoalTypes())
	}
	// The owner becomes the spawned agent's parent. An agent with no parent has
	// nobody to report to, and its close notification would go to whatever the
	// notify handler's host-level fallback owner names — successfully, and to the
	// wrong agent.
	if goal.Owner == "" {
		return fmt.Errorf("store: goal_opened %s names no owner, so a spawned agent would have no parent to report to", ev.ID)
	}
	if goal.Text == "" {
		return fmt.Errorf("store: goal_opened %s has empty text, which is the entire task the agent would be given", ev.ID)
	}

	// Named BEFORE the append, because the log cannot be edited: an unnamed
	// spawn_requested is unreconcilable by construction (the reconciler matches
	// by name), so it would sit there forever matching nothing.
	name, err := h.names.AllocateName(ctx, spec.agentType)
	if err != nil {
		return fmt.Errorf("store: allocating a %s name for goal_opened %s: %w", spec.agentType, ev.ID, err)
	}

	if _, err := h.emitter.Emit(ctx, EmitRequest{
		TypeName:    "spawn_requested",
		TypeVersion: 1,
		// The GOAL's workflow instance, not a new one. It is how the spawned
		// agent's reread_my_goal and report_result find the goal at all; a fresh
		// id would spawn a healthy-looking agent onto an empty workflow that
		// closes nothing.
		WorkflowInstanceID: ev.WorkflowInstanceID,
		Payload: map[string]any{
			"agent_name": name,
			"agent_type": spec.agentType,
			"family":     spec.family,
			"parent":     goal.Owner,
			"branch":     goalBranch(name, ev.ID),
			"prompt":     goalPrompt(GoalType(goal.GoalType), goal, ev),
			// NO `model`, DELIBERATELY (QUM-1337). The seed defines the field and
			// SpawnRequest carries it, but it is an OPERATOR OVERRIDE, not a
			// record of the resolved model: it lands on AgentState.Model, which
			// BuildAgentSessionSpec ranks ABOVE the card
			// (internal/agentloop/session_spec.go). Resolving the card here and
			// pinning the answer would therefore make every engine-spawned agent
			// immune to a later card edit — the exact property QUM-1251 exists to
			// provide. Left empty, the card wins at launch time.
			//
			// This guidance lives here rather than in the seed's json_schema:
			// spawn_requested@1 is already published, and a published
			// (name,version) row is immutable, so editing the seed breaks
			// `sprawl store migrate` (QUM-1252). Verbatim:
			//
			//   An OPERATOR OVERRIDE of the launch model, not a record of the
			//   resolved one. It lands on AgentState.Model, which outranks the
			//   agent type's card — so a spawner that filled this in with the
			//   card's own answer would pin that answer and make the agent
			//   immune to later card edits. The engine leaves it empty on
			//   purpose (QUM-1337); the card resolves the model at launch.
		},
	}); err != nil {
		return fmt.Errorf("store: requesting a %s for goal_opened %s: %w", spec.agentType, ev.ID, err)
	}
	h.log.Info("requested an agent for a goal",
		"goal_event_id", ev.ID, "goal_type", goal.GoalType, "agent", name, "agent_type", spec.agentType)
	return nil
}

// goalPrompt is the entire task the spawned agent gets.
//
// It carries the two ids because an agent that cannot name its own workflow
// cannot read its log or close its goal — and a goal that is never closed is the
// engine's characteristic failure: it looks like work in progress forever.
//
// PROSE, NOT MARKUP (QUM-1348). This text is delivered to the agent as a marked
// `<system-notification type="goal">` first message, but the envelope is built
// at the delivery seam (internal/dispatchadapt), NOT here. The log is
// append-only, so a tag name written into a payload is pinned forever and the
// renderer's vocabulary becomes part of the durable wire format.
//
// THE REPORTING DIRECTIVE LIVES HERE RATHER THAN IN AN AGENT CARD (QUM-1346).
// Cards are keyed by agent_type, so putting it on the researcher/engineer cards
// would apply it to every researcher and engineer — prose-spawned ones
// included, for whom send_message to their manager is the ONLY reporting
// channel and for whom report_result is not merely unnecessary but unusable
// (it requires a goal_event_id and closes a goal contract). The directive is a
// property of the DELIVERY MODE, not of the role, so it belongs with the other
// mode-specific facts: the ids, reread_my_goal, get_workflow_log.
func goalPrompt(gt GoalType, goal goalOpenedPayload, ev DispatchedEvent) string {
	var deliverable string
	switch gt {
	case GoalBugInvestigation:
		deliverable = "Your deliverable is the DIAGNOSIS, not a fix: find the root cause and report it. " +
			"Do not change behaviour to make the symptom go away."
	default:
		deliverable = "Your deliverable is the answer to the question, reported as your result."
	}
	return fmt.Sprintf(`%s

%s

This work is an engine-driven goal (goal_type %q). Its identifiers:
  goal_event_id:        %s
  workflow_instance_id: %s

Use `+"`reread_my_goal`"+` if you lose track of what you were asked, and `+"`get_workflow_log`"+`
to see what has already happened on it. When you are done, close the goal with
`+"`report_result`"+` — nothing else closes it, and until it is closed the goal reads
as work still in progress.

Use `+"`send_message`"+` for mid-flow communication with other agents; that stays
legitimate. But deliver your ENTIRE final report through `+"`report_result`"+`. Closing
the goal already notifies %s, so a summary message on top of it is a duplicate —
do not also message your parent.`,
		goal.Text, deliverable, string(gt), ev.ID, ev.WorkflowInstanceID, goal.Owner)
}

// mappedGoalTypes lists the driveable goal types, sorted so the error message is
// stable across runs (Go map iteration order is not).
func mappedGoalTypes() []string {
	out := make([]string, 0, len(goalAgentTypes))
	for gt := range goalAgentTypes {
		out = append(out, string(gt))
	}
	sort.Strings(out)
	return out
}
