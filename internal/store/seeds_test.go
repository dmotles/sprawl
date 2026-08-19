package store

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// The seed event-type schemas are compiled into the binary (Appendix B item
// 14): degraded-mode validation has to work with the DB unreachable, so the
// appender cannot depend on reading event_type_schemas to know what a pinned
// schema_id means.
//
// Their ids are DERIVED (uuid.NewSHA1 over "<name>@<version>") rather than
// random, so the embedded registry and the SQL that seeds the table agree
// without either being generated from the other. The goldens below are the
// mechanism that keeps that true: an id computed at runtime and compared to
// another runtime computation asserts nothing, since a change to the derivation
// moves both sides together.

// wantSeeds is the M1a seed set with its GOLDEN ids written out as literals.
//
// HONEST ABOUT WHAT THIS PROVES: these literals were RECORDED from the first
// implementation, so they are not an independent oracle and they did not catch
// anything the day they were written. What they are is a CHANGE DETECTOR, and
// that is the property worth having here. If a future edit altered the
// derivation — a new namespace, "@v1" instead of "@1", a rename — every derived
// id would move, every schema_id already pinned in a real event log would stop
// resolving, and a test that compared one derivation against another derivation
// would stay green through all of it. Recorded goldens are the only thing that
// fails in that scenario.
var wantSeeds = []struct {
	name    string
	version int
	id      string
	opens   bool
	closes  string
	// spillable decides whether a degraded-mode outage loses this event to a
	// local file or fails the caller. Getting it wrong on a contract type is
	// silent data loss, so it is pinned per type rather than trusted.
	spillable bool
	// required is the exact `required` list, so a silently relaxed schema
	// (which would let a malformed payload into the log) fails here.
	required []string
}{
	{
		name: "repo_initialized", version: 1,
		spillable: true,
		id:        "0c923406-a2a7-5ef2-b011-d841d807e664",
		required:  []string{"git_sha", "remote_url"},
	},
	{
		name: "run_started", version: 1,
		spillable: true,
		id:        "0bd29d8f-eed8-5dc8-a8f8-c1712af384b3",
		required:  []string{"agent_name", "agent_type", "session_id"},
	},
	{
		name: "turn_finished", version: 1,
		spillable: true,
		id:        "f4f14d6c-5cb3-57c4-a379-ee699165de93",
		required:  []string{"session_id", "input_tokens", "output_tokens"},
	},
	{
		name: "run_finished", version: 1,
		spillable: true,
		id:        "538a89f3-fe07-53c8-b17b-07a9cc14a5fd",
		required:  []string{"session_id", "outcome"},
	},
	{
		name: "agent_spawned", version: 1,
		spillable: true,
		id:        "d470020f-ba51-560d-a9d6-9a604f4c2738",
		opens:     true,
		required:  []string{"agent_name", "agent_type"},
	},
	{
		name: "agent_retired", version: 1,
		spillable: true,
		id:        "88342d9c-cb92-5de7-a634-ad7051f5e211",
		closes:    "agent_spawned",
		required:  []string{"agent_name", "outcome"},
	},
	{
		name: "goal_opened", version: 1,
		id:       "ab44ff33-a479-5d10-a2bb-a669ffb4ba3c",
		opens:    true,
		required: []string{"goal_type", "text"},
	},
	{
		name: "goal_closed", version: 1,
		id:       "866d4ba6-b748-548c-a2d9-4dd9c8197601",
		closes:   "goal_opened",
		required: []string{"outcome"},
	},
}

func TestSeedRegistry_MatchesGoldenIDsAndWiring(t *testing.T) {
	reg, err := SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	if got := len(reg.All()); got != len(wantSeeds) {
		t.Fatalf("registry holds %d seed schemas, want %d — the golden list below would only cover part of it", got, len(wantSeeds))
	}

	for _, w := range wantSeeds {
		wantID, err := uuid.Parse(w.id)
		if err != nil {
			t.Fatalf("golden id for %s is not a uuid: %v", w.name, err)
		}
		got, ok := reg.ByID(wantID)
		if !ok {
			t.Errorf("%s@%d: no seed schema resolves from its golden id %s — either the id derivation changed (which strands every schema_id already pinned in a real log) or the seed is missing",
				w.name, w.version, w.id)
			continue
		}
		if got.Name != w.name || got.Version != w.version {
			t.Errorf("golden id %s resolves to %s@%d, want %s@%d", w.id, got.Name, got.Version, w.name, w.version)
		}
		if got.Opens != w.opens {
			t.Errorf("%s@%d opens=%v, want %v — opens is what makes the appender insert an open_contracts row", w.name, w.version, got.Opens, w.opens)
		}
		if got.Closes != w.closes {
			t.Errorf("%s@%d closes=%q, want %q — closes is what makes the appender delete the contract", w.name, w.version, got.Closes, w.closes)
		}
		if got.Spillable != w.spillable {
			t.Errorf("%s@%d spillable=%v, want %v", w.name, w.version, got.Spillable, w.spillable)
		}
	}
}

// TestSeedRegistry_RequiredFieldsAreExact pins each seed's `required` list.
//
// A schema whose required list quietly shrank would still validate every
// payload the emitters send today, so no other assertion in this package would
// notice — while the log silently began accepting events missing the fields
// every consumer assumes are there.
func TestSeedRegistry_RequiredFieldsAreExact(t *testing.T) {
	reg, err := SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	for _, w := range wantSeeds {
		s, ok := reg.ByName(w.name, w.version)
		if !ok {
			t.Errorf("%s@%d missing from the registry", w.name, w.version)
			continue
		}
		var doc struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(s.JSONSchema, &doc); err != nil {
			t.Errorf("%s@%d json_schema does not parse: %v", w.name, w.version, err)
			continue
		}
		if len(doc.Required) != len(w.required) {
			t.Errorf("%s@%d requires %v, want %v", w.name, w.version, doc.Required, w.required)
			continue
		}
		set := map[string]bool{}
		for _, r := range doc.Required {
			set[r] = true
		}
		for _, r := range w.required {
			if !set[r] {
				t.Errorf("%s@%d does not require %q (requires %v)", w.name, w.version, r, doc.Required)
			}
		}
	}
}

// TestSeedRegistry_EverySchemaIsValidatable is the anti-vacuity check on the
// subset decision: a seed schema using a keyword the validator refuses would
// make every append of that type fail at runtime, in production, with the DB up.
// Nothing else in the package would catch it, because the validator's own tests
// use hand-written schemas rather than the real seeds.
func TestSeedRegistry_EverySchemaIsValidatable(t *testing.T) {
	reg, err := SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	all := reg.All()
	if len(all) == 0 {
		t.Fatal("registry is empty — the loop below would assert nothing")
	}
	for _, s := range all {
		// An empty payload exercises the schema's own keywords without needing
		// a per-type fixture: an unsupported keyword is refused before any
		// payload check, so it surfaces here regardless of what the payload is.
		err := Validate(s.JSONSchema, []byte(`{}`))
		if err != nil && !isSchemaViolation(err) {
			t.Errorf("%s@%d uses a schema keyword the validator cannot enforce, so every append of this type would fail at runtime: %v",
				s.Name, s.Version, err)
		}
	}
}

// TestSeedRegistry_ClosesNamesAnExistingOpener pins referential integrity
// inside the seed set. `closes` holds a NAME, so it is not FK-checked by
// Postgres — a typo would produce a close type that can never close anything,
// and the failure would appear as a contract that stays open forever.
func TestSeedRegistry_ClosesNamesAnExistingOpener(t *testing.T) {
	reg, err := SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	openers := map[string]bool{}
	for _, s := range reg.All() {
		if s.Opens {
			openers[s.Name] = true
		}
	}
	if len(openers) == 0 {
		t.Fatal("no seed schema is marked opens — the check below would be vacuous")
	}
	var checked int
	for _, s := range reg.All() {
		if s.Closes == "" {
			continue
		}
		checked++
		if !openers[s.Closes] {
			t.Errorf("%s@%d closes %q, which is not an opens-typed seed schema (openers: %v) — this close type can never close anything",
				s.Name, s.Version, s.Closes, openers)
		}
	}
	if checked == 0 {
		t.Fatal("no seed schema declares `closes` — this assertion inspected nothing, so its silence is not evidence")
	}
}

// TestSeedRegistry_ContractTypesAreNeverSpillable is the safety invariant behind
// the degraded-mode split, asserted as a RULE over the whole seed set rather
// than as two per-type goldens.
//
// A goal recorded only in a local spill file is invisible to every other host
// and to the sweeper: it reads as work nobody is doing, while the agent that
// opened it believes it was recorded. The per-type goldens above would catch
// today's two contract types flipping; this catches a NEW contract type being
// added spillable, which is the more likely mistake because whoever adds it will
// be copying an existing telemetry seed.
//
// agent_spawned/agent_retired are the deliberate exception and are named here
// explicitly rather than silently skipped: they are lifecycle telemetry that
// also happens to form a contract pair (Appendix B item 12), they are replayed
// rather than depended on for coordination, and the appender mints the event id
// before the append so a replay deduplicates against events.id UNIQUE.
func TestSeedRegistry_ContractTypesAreNeverSpillable(t *testing.T) {
	reg, err := SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	lifecycleExceptions := map[string]bool{"agent_spawned": true, "agent_retired": true}

	var checked int
	for _, s := range reg.All() {
		isContract := s.Opens || s.Closes != ""
		if !isContract || lifecycleExceptions[s.Name] {
			continue
		}
		checked++
		if s.Spillable {
			t.Errorf("%s@%d takes part in an open/close contract AND is spillable: a contract recorded only in a local spill file is invisible to every other host and to the sweeper. If this type really is replay-safe, add it to lifecycleExceptions with the reason",
				s.Name, s.Version)
		}
	}
	if checked == 0 {
		t.Fatal("no non-exempt contract type was examined, so this assertion's silence is not evidence")
	}
}
