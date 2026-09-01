package engine

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/store"
)

func researchTestRegistry(t *testing.T) *store.Registry {
	t.Helper()
	reg, err := store.SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	return reg
}

func mustResearchDef(t *testing.T) Definition {
	t.Helper()
	d, err := ResearchDefinition(researchTestRegistry(t), uuid.New())
	if err != nil {
		t.Fatalf("ResearchDefinition: %v", err)
	}
	return d
}

// TestResearchDefinition_IsValid is the cheapest real assertion available: the
// constructor's whole job is to produce something Validate accepts, and every
// defect Validate names (a nil trigger, a duplicate step name, a backtrack to
// nowhere) is one this definition could plausibly ship with.
func TestResearchDefinition_IsValid(t *testing.T) {
	if err := mustResearchDef(t).Validate(); err != nil {
		t.Errorf("the RESEARCH definition does not validate: %v", err)
	}
}

// TestResearchDefinition_ResolvesEveryTriggerToAPinnedSeedID.
//
// The point of resolving names through the registry is that the definition
// carries PINNED ids, so this asserts every step trigger is a real seed id and
// not uuid.Nil. A definition built with a typo'd name that silently yielded Nil
// would produce a workflow no event can ever advance — and Validate's
// non-Nil check is the only thing between here and that, so it is asserted
// against the registry rather than against itself.
func TestResearchDefinition_ResolvesEveryTriggerToAPinnedSeedID(t *testing.T) {
	reg := researchTestRegistry(t)
	d, err := ResearchDefinition(reg, uuid.New())
	if err != nil {
		t.Fatalf("ResearchDefinition: %v", err)
	}

	known := map[uuid.UUID]string{}
	for _, s := range reg.All() {
		known[s.ID] = s.Name
	}
	if d.TriggerEventSchema == uuid.Nil || known[d.TriggerEventSchema] != "goal_opened" {
		t.Errorf("the definition's trigger is %s (%q), want the pinned goal_opened id", d.TriggerEventSchema, known[d.TriggerEventSchema])
	}
	wantOrder := []string{"spawn_committed", "goal_closed", "notify_acked"}
	if len(d.Steps) != len(wantOrder) {
		t.Fatalf("the definition has %d steps, want %d", len(d.Steps), len(wantOrder))
	}
	for i, s := range d.Steps {
		name, ok := known[s.TriggerEventType]
		if !ok {
			t.Errorf("step %q triggers on %s, which is not a seed schema id", s.Name, s.TriggerEventType)
			continue
		}
		if name != wantOrder[i] {
			t.Errorf("step %d (%q) triggers on %q, want %q — the step order IS the goal's shape", i, s.Name, name, wantOrder[i])
		}
	}
}

// TestResearchDefinition_CarriesTheResearcherCardOnTheSpawnStep.
//
// A spawn step with uuid.Nil for AgentCard spawns NOBODY (that is what Nil
// means on Step.AgentCard), so a definition that dropped the card would await a
// spawn_committed that nothing will ever emit — a goal that hangs forever with
// no error anywhere. The later steps must NOT carry a card: they wait, and a
// card on them would spawn a second agent per goal.
func TestResearchDefinition_CarriesTheResearcherCardOnTheSpawnStep(t *testing.T) {
	card := uuid.New()
	d, err := ResearchDefinition(researchTestRegistry(t), card)
	if err != nil {
		t.Fatalf("ResearchDefinition: %v", err)
	}
	if d.Steps[0].AgentCard != card {
		t.Errorf("the spawn step carries card %s, want the researcher card %s", d.Steps[0].AgentCard, card)
	}
	if d.Steps[0].PromptTemplate == "" {
		t.Error("the spawn step has no prompt template, so the researcher would be spawned with no task")
	}
	for _, s := range d.Steps[1:] {
		if s.AgentCard != uuid.Nil {
			t.Errorf("await-step %q carries agent card %s; a card on a waiting step spawns a second agent per goal", s.Name, s.AgentCard)
		}
	}
}

// TestResearchDefinition_RefusesAZeroCard. A definition is built once at
// startup and then drives every research goal, so a missing card must fail
// there rather than produce goals that hang.
func TestResearchDefinition_RefusesAZeroCard(t *testing.T) {
	_, err := ResearchDefinition(researchTestRegistry(t), uuid.Nil)
	if err == nil {
		t.Fatal("ResearchDefinition accepted uuid.Nil for the researcher card, which would spawn nobody and hang every research goal")
	}
	if !strings.Contains(err.Error(), "card") {
		t.Errorf("the error should name the missing card; got: %v", err)
	}
}

// TestResearchDefinition_RefusesARegistryMissingASchema.
//
// The POSITIVE CONTROL for the name resolution: with an empty registry every
// lookup fails, so a constructor that ignored the miss and stored uuid.Nil is
// caught here. Without this, "resolves names correctly" is only ever tested on
// a registry where every name happens to exist.
func TestResearchDefinition_RefusesARegistryMissingASchema(t *testing.T) {
	_, err := ResearchDefinition(&store.Registry{}, uuid.New())
	if err == nil {
		t.Fatal("ResearchDefinition built a definition from an empty registry; every step trigger would be uuid.Nil and no event could advance the goal")
	}
	if !strings.Contains(err.Error(), "goal_opened") {
		t.Errorf("the error should name the first schema it could not resolve; got: %v", err)
	}
}

// TestResearchDefinition_ReplaysAHappyPathGoalToCompletion is the end-to-end
// assertion at the definition level: the exact event sequence a real research
// goal produces drives the instance to Done.
//
// Its NEGATIVE CONTROL is the second half — the same sequence missing the
// middle event leaves the instance incomplete. A definition whose steps all
// triggered on the same type would pass the first half and fail the second.
func TestResearchDefinition_ReplaysAHappyPathGoalToCompletion(t *testing.T) {
	reg := researchTestRegistry(t)
	d, err := ResearchDefinition(reg, uuid.New())
	if err != nil {
		t.Fatalf("ResearchDefinition: %v", err)
	}
	inst := Instance{ID: uuid.New(), DefName: d.Name, DefVersion: d.Version}

	ev := func(name string) store.Event {
		s, ok := reg.ByName(name, 1)
		if !ok {
			t.Fatalf("%s@1 missing from the seed registry", name)
		}
		return store.Event{SchemaID: s.ID, WorkflowInstanceID: inst.ID}
	}

	happy := []store.Event{ev("spawn_committed"), ev("goal_closed"), ev("notify_acked")}
	got, err := d.Replay(inst, happy)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !got.Done(d) {
		t.Fatalf("a complete research goal left the instance at cursor %d of %d steps", got.Cursor, len(d.Steps))
	}

	// Negative control: drop the result and the goal must NOT complete.
	withoutResult := []store.Event{ev("spawn_committed"), ev("notify_acked")}
	got, err = d.Replay(inst, withoutResult)
	if err != nil {
		t.Fatalf("Replay without the result: %v", err)
	}
	if got.Done(d) {
		t.Error("a research goal that never produced a result reported complete")
	}
}
