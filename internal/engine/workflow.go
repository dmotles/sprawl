package engine

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/store"
)

// A workflow is DATA, not code: a Definition is a list of Steps, and the engine
// interprets it. Nothing in this file spawns an agent, renders a prompt, or
// touches the database — those are the interpreter's job, and keeping them out
// is what makes "does this event advance this step" answerable by a pure
// function that a test can exhaust.
//
// What is deliberately NOT here, so its absence is not read as an oversight:
// persisting a Definition into workflow_defs.steps, executing an OutcomePolicy,
// and any guard language beyond a Go func. Each lands with the interpreter that
// needs it.

// Action is what an OutcomePolicy does when a step reports failure.
type Action int

const (
	// ActionAbort ends the workflow instance. The zero value on purpose: a
	// policy nobody filled in must not silently retry forever.
	ActionAbort Action = iota
	// ActionRetry re-runs the same step, up to MaxRetries.
	ActionRetry
	// ActionBacktrack rewinds to BacktrackTo and resumes from there.
	ActionBacktrack
	// ActionEscalate hands the decision to the goal's owner.
	ActionEscalate
)

func (a Action) String() string {
	switch a {
	case ActionAbort:
		return "abort"
	case ActionRetry:
		return "retry"
	case ActionBacktrack:
		return "backtrack"
	case ActionEscalate:
		return "escalate"
	}
	return fmt.Sprintf("Action(%d)", int(a))
}

// OutcomePolicy declares what happens when a step fails. Declared per step
// because the right answer differs by step: a failed spawn is worth retrying, a
// failed investigation is worth escalating.
type OutcomePolicy struct {
	OnFailure Action
	// MaxRetries is meaningful only for ActionRetry, and counts RE-runs: 0
	// retries means the step runs once.
	MaxRetries int
	// BacktrackTo names the step to rewind to. Meaningful only for
	// ActionBacktrack, and Validate requires it to name a step in the same
	// definition — a backtrack target resolved at runtime would fail in the
	// middle of a goal rather than at load.
	BacktrackTo string
}

// ReEngagement decides who redoes work when a step is re-run.
type ReEngagement int

const (
	// ReEngageOriginal wakes the same agent with the accumulated context. The
	// zero value: it is the cheaper of the two and does not discard work.
	ReEngageOriginal ReEngagement = iota
	// DiscardAndRedo abandons the attempt and starts a fresh agent, typically
	// on a stronger model.
	DiscardAndRedo
)

func (r ReEngagement) String() string {
	switch r {
	case ReEngageOriginal:
		return "re-engage-original"
	case DiscardAndRedo:
		return "discard-and-redo"
	}
	return fmt.Sprintf("ReEngagement(%d)", int(r))
}

// Step is one stage of a workflow: it waits for an event of a declared type,
// and while it waits there may be an agent working on it.
type Step struct {
	// Name identifies the step within its definition. Unique per definition —
	// BacktrackTo resolves against it, and a duplicate would make the target
	// ambiguous.
	Name string
	// TriggerEventType is the event_type_schemas id that advances this step.
	// A SCHEMA ID and not a name, matching the appender: names are resolved
	// once, at seed time, and everything downstream refers to the pinned id.
	TriggerEventType uuid.UUID
	// Guard narrows the trigger beyond its type — "this result, for this goal",
	// not merely "some result". Nil means the type alone is sufficient.
	//
	// A Go func rather than an expression language because the only callers are
	// in-process definitions; a serialised guard is a problem for whoever first
	// needs a definition to round-trip through workflow_defs.steps.
	Guard func(store.Event) bool
	// AgentCard is the agent_cards id whose card resolves the model and prompt
	// for the agent this step spawns. uuid.Nil means the step spawns nobody and
	// only waits.
	AgentCard uuid.UUID
	// PromptTemplate is the task text handed to that agent.
	PromptTemplate string
	// EmitOnDone is the schema id of the event appended when this step
	// completes. uuid.Nil means the step's completion is recorded by its
	// trigger event alone.
	EmitOnDone uuid.UUID
	// Outcome and ReEngagement are read by the interpreter, not by Advance.
	Outcome      OutcomePolicy
	ReEngagement ReEngagement
}

// Definition is a workflow declared as data. Instances pin (Name, Version), so
// editing a definition never changes the shape of a goal already in flight.
type Definition struct {
	Name    string
	Version int
	// TriggerEventSchema is the event type whose arrival starts an instance.
	TriggerEventSchema uuid.UUID
	Steps              []Step
}

// Validate reports every way a definition is unusable, at load time.
//
// At load and not at run: a workflow instance can sit open for hours, and a
// definition defect that first surfaces when step 4 backtracks to a step that
// does not exist strands a goal that already spent an agent's time.
func (d Definition) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("engine: workflow definition has no name")
	}
	if d.Version <= 0 {
		return fmt.Errorf("engine: workflow %q has version %d, want >= 1 — instances pin the version, and a zero pin cannot be distinguished from an unset one", d.Name, d.Version)
	}
	if d.TriggerEventSchema == uuid.Nil {
		return fmt.Errorf("engine: workflow %q declares no trigger event schema, so nothing could ever start an instance of it", d.Name)
	}
	if len(d.Steps) == 0 {
		return fmt.Errorf("engine: workflow %q has no steps", d.Name)
	}
	names := make(map[string]int, len(d.Steps))
	for i, s := range d.Steps {
		if s.Name == "" {
			return fmt.Errorf("engine: workflow %q step %d has no name", d.Name, i)
		}
		if prev, dup := names[s.Name]; dup {
			return fmt.Errorf("engine: workflow %q has two steps named %q (%d and %d), so a backtrack target naming it is ambiguous", d.Name, s.Name, prev, i)
		}
		names[s.Name] = i
		if s.TriggerEventType == uuid.Nil {
			return fmt.Errorf("engine: workflow %q step %q declares no trigger event type, so no event could ever advance it", d.Name, s.Name)
		}
		if s.Outcome.MaxRetries < 0 {
			return fmt.Errorf("engine: workflow %q step %q has MaxRetries %d, want >= 0", d.Name, s.Name, s.Outcome.MaxRetries)
		}
	}
	// Backtrack targets are checked in a second pass so a definition may
	// backtrack to a step declared after it.
	for _, s := range d.Steps {
		if s.Outcome.OnFailure != ActionBacktrack {
			continue
		}
		if s.Outcome.BacktrackTo == "" {
			return fmt.Errorf("engine: workflow %q step %q backtracks but names no target", d.Name, s.Name)
		}
		if _, ok := names[s.Outcome.BacktrackTo]; !ok {
			return fmt.Errorf("engine: workflow %q step %q backtracks to %q, which is not a step in this workflow", d.Name, s.Name, s.Outcome.BacktrackTo)
		}
	}
	return nil
}

// Registry holds the definitions this process knows, keyed by name AND version.
//
// Both, because the point of the pin is that several versions of one workflow
// are live at once: an instance started yesterday must keep running yesterday's
// steps while new instances start on today's.
type Registry struct {
	defs map[string]map[int]Definition
}

// Register validates d and files it under (Name, Version). Re-registering an
// existing pin is refused rather than silently overwriting: an instance in
// flight resolves its steps through this map, so a redefinition under the same
// version would change a running goal's shape — which is the exact thing
// versioning exists to prevent.
func (r *Registry) Register(d Definition) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if r.defs == nil {
		r.defs = make(map[string]map[int]Definition)
	}
	byVersion, ok := r.defs[d.Name]
	if !ok {
		byVersion = make(map[int]Definition)
		r.defs[d.Name] = byVersion
	}
	if _, dup := byVersion[d.Version]; dup {
		return fmt.Errorf("engine: workflow %s@%d is already registered", d.Name, d.Version)
	}
	byVersion[d.Version] = d
	return nil
}

// Lookup resolves an instance's pin. It never falls back to another version:
// a missing pin is an error, because running an instance against the nearest
// available definition would silently change the workflow underneath it.
func (r *Registry) Lookup(name string, version int) (Definition, error) {
	d, ok := r.defs[name][version]
	if !ok {
		return Definition{}, fmt.Errorf("engine: no workflow definition registered for %s@%d", name, version)
	}
	return d, nil
}

// Instance is a workflow in flight. It pins the definition it started under,
// by name AND version.
type Instance struct {
	ID uuid.UUID
	// DefName and DefVersion are the pin. Carried as values rather than as a
	// pointer to a Definition so that an instance rehydrated from Postgres
	// names the version it started under even when the registry has since
	// loaded a newer one.
	DefName    string
	DefVersion int
	// Cursor is the index of the step currently awaiting its trigger event.
	// Cursor == len(Steps) means the instance is complete.
	Cursor int
}

// Done reports whether every step has been advanced past.
func (i Instance) Done(d Definition) bool { return i.Cursor >= len(d.Steps) }

// Advance is the verdict on one event against one instance.
//
// Not-advancing is an ordinary, expected result and NOT an error: the log is a
// single global stream, so the overwhelming majority of events an instance is
// offered belong to something else. Reason exists so that "it did not advance"
// is diagnosable without re-deriving why.
type Advanced struct {
	OK bool
	// From and To are the cursor before and after. Equal when OK is false.
	From, To int
	// Step is the step that was advanced past. Meaningful only when OK.
	Step Step
	// Reason states why the event did not advance the instance. Empty when OK.
	Reason string
}

// Advance decides whether ev advances inst, and returns the resulting instance.
//
// Pure: it neither appends nor mutates its receiver's argument. The returned
// Instance is the caller's to persist, which keeps the decision testable
// without a database and keeps the append in the caller's transaction where the
// savepoint pattern needs it.
func (d Definition) Advance(inst Instance, ev store.Event) (Instance, Advanced, error) {
	if inst.DefName != d.Name || inst.DefVersion != d.Version {
		return inst, Advanced{}, fmt.Errorf("engine: instance pins %s@%d but was advanced against %s@%d", inst.DefName, inst.DefVersion, d.Name, d.Version)
	}
	if inst.Cursor < 0 {
		return inst, Advanced{}, fmt.Errorf("engine: instance %s has a negative cursor (%d)", inst.ID, inst.Cursor)
	}
	no := func(reason string) (Instance, Advanced, error) {
		return inst, Advanced{From: inst.Cursor, To: inst.Cursor, Reason: reason}, nil
	}
	if inst.Done(d) {
		return no("the instance is already complete")
	}
	step := d.Steps[inst.Cursor]
	if ev.SchemaID != step.TriggerEventType {
		// THE acceptance criterion: a wrong-typed event does not advance a
		// step. Cheap to state and easy to get wrong in the direction that
		// never fails — an engine that advances on any event still completes
		// every happy-path goal.
		return no(fmt.Sprintf("step %q waits for event type %s, got %s", step.Name, step.TriggerEventType, ev.SchemaID))
	}
	if ev.WorkflowInstanceID != inst.ID {
		return no(fmt.Sprintf("event belongs to workflow instance %s, not %s", ev.WorkflowInstanceID, inst.ID))
	}
	if step.Guard != nil && !step.Guard(ev) {
		return no(fmt.Sprintf("step %q guard rejected the event", step.Name))
	}
	inst.Cursor++
	return inst, Advanced{OK: true, From: inst.Cursor - 1, To: inst.Cursor, Step: step}, nil
}
