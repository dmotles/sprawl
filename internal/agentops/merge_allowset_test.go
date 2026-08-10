package agentops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// QUM-1186 (D4): merge precondition 4 becomes a Status ALLOW-SET.
//
// It used to read `Status != StatusActive && LastReportState != "complete"`.
// With the outcome axis deleted, the second arm becomes a tautology and the
// precondition would collapse to "must be active" — silently removing the
// ability to merge a suspended or complete agent, which works today via the
// LastReportState arm. That would be a capability regression smuggled in
// under a deletion.
//
// The precondition's real job is to stop you merging a RETIRED, KILLED or
// DEAD agent. It is not to require a live process: a stopped agent's worktree
// is more quiescent than a running one's, not less.
//
// Allow-set, not deny-set, deliberately — a deny-set fails OPEN for any status
// added later.

// mergeableSetupRoot builds a sprawl root with `weave` as caller and one child
// agent at the given status, ready to merge.
func mergeableSetupRoot(t *testing.T, status string) (string, string) {
	t.Helper()
	sprawlRoot := t.TempDir()
	const agentName = "kid"

	wt := filepath.Join(sprawlRoot, ".sprawl", "worktrees", agentName)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir agent wt: %v", err)
	}
	if err := state.SaveAgent(sprawlRoot, &state.AgentState{
		Name: agentName, Type: "engineer", Family: "engineering",
		Branch: "dmotles/kid", Worktree: wt, Parent: "weave",
		Status: status,
	}); err != nil {
		t.Fatalf("SaveAgent agent: %v", err)
	}

	weaveWT := filepath.Join(sprawlRoot, "weave-wt")
	if err := os.MkdirAll(weaveWT, 0o755); err != nil {
		t.Fatalf("mkdir weave wt: %v", err)
	}
	if err := state.SaveAgent(sprawlRoot, &state.AgentState{
		Name: "weave", Type: "manager", Family: "engineering",
		Worktree: weaveWT, Status: state.StatusActive,
	}); err != nil {
		t.Fatalf("SaveAgent weave: %v", err)
	}
	return sprawlRoot, agentName
}

// mergeAllowedStatuses is the closed allow-set for precondition 4.
//
// StatusIdle is here because an idle-reclaimed agent's branch is exactly as
// merge-ready as a live one's — reclamation is a runtime memory decision, not
// a statement about the work.
//
// StatusSuspended and StatusComplete are here because they are mergeable
// TODAY (via the deleted LastReportState arm) and removing that would be a
// regression, not a simplification.
var mergeAllowedStatuses = []string{
	state.StatusActive,
	// StatusRunning is the on-disk legacy synonym for StatusActive, and every
	// axis the system consults treats the two identically —
	// liveness.LivenessFromStatus projects both to Running (checked by
	// TestMergePrecondition4_RunningIsTheLivenessTwinOfActive below), and
	// boot-resume eligibility follows that projection. Merge must not be the
	// single place they diverge.
	//
	// Unlike Suspended and Complete, this row is NOT a preserved capability: a
	// "running" agent is denied by the allow-set as it stands, so allowing it
	// is a deliberate behaviour CHANGE. It is the right one because the denial
	// was never a decision about "running" — it fell out of enumerating the
	// canonical statuses and forgetting the synonym.
	state.StatusRunning,
	state.StatusIdle,
	state.StatusSuspended,
	state.StatusComplete,
}

// mergeDeniedStatuses is every status constant precondition 4 must REFUSE.
//
// It is deliberately NOT a claim about production reachability — it used to say
// "every other status constant that precondition 4 can actually observe", and
// that was false of two of its members. The other six have live production
// writers (deliberately not indexed by filename here: a hand-maintained file
// list rots silently on the first writer move, which is how the sentence this
// replaces went wrong). StatusDone and StatusRetired do not, and they are here
// so merge fails CLOSED if one ever reaches disk out of contract — a hand-edit,
// a foreign tool, a future writer:
//
//   - StatusDone is a v0 legacy token with no producer since QUM-615
//     (bd024ab). It does NOT belong in mergeUnreachableStatuses: its rewrite in
//     migrate() is gated on SchemaVersion < 1, unlike the always-on stopped
//     rewrite, so a SaveAgent-built fixture (stamped at CurrentSchemaVersion)
//     reads back as "done" and a stopped-style proof would be aimed at a gate
//     that does not hold. It is nonetheless unreachable in production, because
//     bd024ab introduced the schema stamp and deleted the last writer of "done"
//     in the SAME commit — no binary has ever emitted "done" at
//     schema_version >= 1, and every file that could carry it is v0 and
//     migrates to StatusComplete on read. The capability that legacy token
//     represents is therefore intact, which
//     TestMergePrecondition4_LegacyDoneAgentMergesAsComplete checks end to end;
//     the version gate itself is pinned by
//     TestLoadAgent_LegacyTokenRewriteIsVersionGated_StoppedIsNot in
//     internal/state.
//   - StatusRetired has no PRODUCTION writer, current or historical — test
//     fixtures do write it, including this file's own deny-set loop, so "no
//     writer at all" would be false and "never persisted" stronger than the
//     evidence supports. retire.go writes "retiring" and then DELETES the
//     state file, so a retired agent fails precondition 1, not 4; that
//     precedence is checked by
//     TestMergePrecondition4_RetiredAgentIsGoneNotDenied below. Nothing
//     migrates "retired" away, so if a legacy or foreign file ever carried it
//     this entry is the only guard standing behind it.
//
// Kept as an explicit list rather than "whatever is not allowed" so that
// TestMergePrecondition4_EveryStatusConstantIsClassified can force a decision
// when a new status is introduced.
var mergeDeniedStatuses = []string{
	state.StatusKilled,
	state.StatusRetired,
	state.StatusRetiring,
	state.StatusDone,
	state.StatusResumeFailed,
	state.StatusFaulted,
	state.StatusPaused,
	state.StatusDied,
}

// mergeUnreachableStatuses never reach precondition 4 as themselves: LoadAgent
// rewrites them on read, so Merge only ever sees their migrated value. They are
// classified here so the exhaustiveness test below stays honest rather than
// being satisfied by a row that silently tests the migration instead.
//
// Membership requires an ALWAYS-ON rewrite. A version-gated one does not
// qualify — see the StatusDone note on mergeDeniedStatuses above, which is the
// case that made this distinction worth writing down.
var mergeUnreachableStatuses = []string{
	// StatusStopped is the legacy sentinel; the always-on QUM-787 read
	// migration rewrites it before any caller sees it.
	state.StatusStopped,
}

// TestMergePrecondition4_UnreachableStatusesAreRewrittenBeforeTheyAreSeen
// documents WHY each member of mergeUnreachableStatuses is unreachable, so the
// claim is checked rather than asserted in a comment. If a migration ever stops
// firing, this fails and the status moves back into one of the reachable lists.
//
// It loops the LIST rather than naming StatusStopped, which is what makes the
// "membership requires an ALWAYS-ON rewrite" rule structural instead of prose:
// mergeableSetupRoot writes through SaveAgent, which stamps
// CurrentSchemaVersion, so a version-gated rewrite cannot fire here and a
// member added on that weaker basis fails immediately. That is exactly the trap
// StatusDone would have fallen into.
func TestMergePrecondition4_UnreachableStatusesAreRewrittenBeforeTheyAreSeen(t *testing.T) {
	if len(mergeUnreachableStatuses) == 0 {
		t.Fatal("mergeUnreachableStatuses is empty; this test would assert nothing")
	}
	for _, s := range mergeUnreachableStatuses {
		t.Run(s, func(t *testing.T) {
			sprawlRoot, agentName := mergeableSetupRoot(t, s)
			loaded, err := state.LoadAgent(sprawlRoot, agentName)
			if err != nil {
				t.Fatalf("LoadAgent: %v", err)
			}
			if loaded.Status == s {
				t.Fatalf("Status survived load as %q; it is reachable after all and must be classified as allowed or denied. "+
					"If its rewrite is version-GATED rather than always-on, it does not belong in mergeUnreachableStatuses at all", loaded.Status)
			}

			// The rewrite is only half of "before they are SEEN". Being on this
			// list excludes the status from AllowsEvery/RejectsEvery, so without
			// a Merge call here it has NO merge-behaviour coverage at all and a
			// rewrite that retargets it into the deny-set is a capability
			// removal wearing a migration's clothes.
			//
			// A FRESH root deliberately: the LoadAgent above persists the
			// migration back to disk, so reusing sprawlRoot would hand Merge an
			// already-normalised file and leave the composition untested while
			// looking tested. Same trap as
			// TestMergePrecondition4_LegacyDoneAgentMergesAsComplete.
			//
			// Hardcoded "must succeed", NOT derived from where loaded.Status
			// falls in the allow/deny lists. A derived expectation is satisfied
			// by ANY rewrite target — retarget the always-on arm at
			// StatusKilled and a derived form goes green while the capability
			// is gone, which is the escape this assertion exists to close.
			// Mutation control, run: migrate()'s stopped arm rewritten to
			// StatusKilled -> fires HERE (`err = ... cannot be merged (status:
			// "killed")`, want nil), while the probe above still passes.
			mergeRoot, mergeName := mergeableSetupRoot(t, s)
			if _, err := Merge(context.Background(), mergeTestDeps(mergeRoot), mergeName, "", true, false, false); err != nil {
				t.Fatalf("Merge of an agent stored as %q: err = %v, want nil — the rewrite target must stay mergeable. "+
					"If a legacy token ever legitimately migrates into the deny-set, rewrite this row to say so deliberately; "+
					"do NOT widen mergeAllowedStatuses to make it green", s, err)
			}
		})
	}
}

// writeRawV0MergeableAgent writes a GENUINE v0 state file — no schema_version
// key — so LoadAgent's version-gated migration actually fires. A struct-literal
// fixture cannot do this: SaveAgent stamps CurrentSchemaVersion on write, so
// the gate never runs and the legacy token survives verbatim.
func writeRawV0MergeableAgent(t *testing.T, sprawlRoot, name, status string) {
	t.Helper()
	wt := filepath.Join(sprawlRoot, ".sprawl", "worktrees", name)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir agent wt: %v", err)
	}
	dir := state.AgentsDir(sprawlRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	raw := fmt.Sprintf(`{"name":%q,"type":"engineer","family":"engineering",`+
		`"branch":"dmotles/%s","worktree":%q,"parent":"weave","status":%q}`,
		name, name, wt, status)
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write raw v0 fixture: %v", err)
	}
}

// TestMergePrecondition4_LegacyDoneAgentMergesAsComplete is the answer to
// "did QUM-1186 drop the ability to merge a `done` agent?". It did not, and
// this checks the whole composition rather than asserting it in prose:
// a genuine v0 file carrying the legacy token "done" is migrated to
// StatusComplete on read, and StatusComplete is in the allow-set, so the merge
// succeeds. The capability M12 shipped survives; only its representation moved.
//
// Deliberately NOT written as a "LoadAgent rewrites it before merge sees it"
// probe in the style of TestMergePrecondition4_UnreachableStatusesAreRewrittenBeforeTheyAreSeen.
// That probe would be aimed at the wrong gate: the "done" rewrite fires only
// for SchemaVersion < 1, while the "stopped" rewrite is always-on. See
// TestLoadAgent_LegacyTokenRewriteIsVersionGated_StoppedIsNot in internal/state.
func TestMergePrecondition4_LegacyDoneAgentMergesAsComplete(t *testing.T) {
	sprawlRoot := t.TempDir()
	writeRawV0MergeableAgent(t, sprawlRoot, "kid", state.StatusDone)

	weaveWT := filepath.Join(sprawlRoot, "weave-wt")
	if err := os.MkdirAll(weaveWT, 0o755); err != nil {
		t.Fatalf("mkdir weave wt: %v", err)
	}
	if err := state.SaveAgent(sprawlRoot, &state.AgentState{
		Name: "weave", Type: "manager", Family: "engineering",
		Worktree: weaveWT, Status: state.StatusActive,
	}); err != nil {
		t.Fatalf("SaveAgent weave: %v", err)
	}

	// Precondition, asserted from the RAW BYTES rather than via LoadAgent.
	// LoadAgent persists the migration back to disk, so probing with it would
	// normalise the fixture to complete/v4 and leave Merge reading an already-
	// migrated file — the composition would go untested while looking tested.
	// (That is how this test was first written; the mutation control fired on
	// the probe line and never reached the Merge call.)
	raw, err := os.ReadFile(filepath.Join(state.AgentsDir(sprawlRoot), "kid.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !strings.Contains(string(raw), `"status":"done"`) {
		t.Fatalf("fixture does not carry the legacy token: %s", raw)
	}
	if strings.Contains(string(raw), "schema_version") {
		t.Fatalf("fixture is not v0 — it carries a schema_version, so migrate's gate will not fire: %s", raw)
	}

	// Merge is the first and only reader: its own LoadAgent runs the migration.
	if _, err := Merge(context.Background(), mergeTestDeps(sprawlRoot), "kid", "", true, false, false); err != nil {
		t.Fatalf("Merge of a legacy v0 \"done\" agent: err = %v, want nil — it migrates to complete, which is mergeable", err)
	}

	// AFTER the merge (so it cannot re-create the read-ordering trap above):
	// pin that the success came from the MIGRATED value. Without this, deleting
	// the migrate arm AND restoring StatusDone to the allow-set would leave the
	// test green while its name says "MergesAsComplete" — green for the wrong
	// reason, under exactly the change this test exists to argue against.
	after, err := state.LoadAgent(sprawlRoot, "kid")
	if err != nil {
		t.Fatalf("LoadAgent after merge: %v", err)
	}
	if after.Status != state.StatusComplete {
		t.Errorf("post-merge Status = %q, want %q — the merge must have succeeded via the migration, not by `done` becoming mergeable",
			after.Status, state.StatusComplete)
	}
}

// TestMergePrecondition4_RetiredAgentIsGoneNotDenied checks the PRECEDENCE the
// StatusRetired deny-set entry depends on: when the state file is absent, Merge
// fails at precondition 1 ("not found") and never reaches the precondition-4
// status rejection.
//
// Scope, stated precisely because the distinction is the point: this deletes
// the file directly and does NOT drive the retire path, so it does not check
// that retire deletes the file. That half is real coverage elsewhere, not an
// assumption: retire_test.go in this package asserts "state file still loads
// after Retire; expected removal" on the direct, sub-agent and cascade paths.
// Cited by assertion text rather than file:line so the hand-off stays greppable
// when the lines move.
// What it establishes is the half this comment
// would otherwise merely assert — that a missing file loses the race to
// precondition 4 — so if retire ever persists `retired` INSTEAD of deleting,
// the deny-set entry stops being a dead guard and becomes load-bearing.
func TestMergePrecondition4_RetiredAgentIsGoneNotDenied(t *testing.T) {
	sprawlRoot, agentName := mergeableSetupRoot(t, state.StatusActive)
	if err := state.DeleteAgent(sprawlRoot, agentName); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	_, err := Merge(context.Background(), mergeTestDeps(sprawlRoot), agentName, "", true, false, false)
	if err == nil {
		t.Fatal("Merge of a deleted agent returned nil error")
	}
	if strings.Contains(err.Error(), "cannot be merged") {
		t.Errorf("error %q is the precondition-4 status rejection, but a missing state file must fail EARLIER, at precondition 1. "+
			"This test does not drive retire, so it cannot tell you WHY the file is present — only that the precedence the "+
			"StatusRetired deny-set entry relies on no longer holds", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q should be the precondition-1 not-found error", err)
	}
}

// TestMergePrecondition4_RunningIsTheLivenessTwinOfActive checks the reason
// StatusRunning is in the allow-set, rather than asserting it in a comment —
// the same discipline as TestMergePrecondition4_UnreachableStatusesAreRewrittenBeforeTheyAreSeen
// above. If the "running" alias is ever dropped from the liveness projection,
// the two statuses stop being twins and this row's justification evaporates;
// this fails then instead of the comment quietly going stale.
func TestMergePrecondition4_RunningIsTheLivenessTwinOfActive(t *testing.T) {
	running, okRunning := liveness.LivenessFromStatus(state.StatusRunning)
	active, okActive := liveness.LivenessFromStatus(state.StatusActive)
	if !okRunning || !okActive {
		t.Fatalf("LivenessFromStatus: running ok=%v, active ok=%v; both must project", okRunning, okActive)
	}
	if running != active {
		t.Fatalf("LivenessFromStatus(running) = %v, LivenessFromStatus(active) = %v; the merge allow-set entry for StatusRunning rests on these being equal", running, active)
	}
	// Equality alone would survive both being remapped to something else,
	// while merge.go and the allow-set comment name Running specifically.
	if running != liveness.Running {
		t.Fatalf("LivenessFromStatus(running) = %v, want liveness.Running; the comments in merge.go and mergeAllowedStatuses name Running by value", running)
	}
}

// mergeStatusesDeliberatelyOmittedFromHint are allow-set entries the
// operator-facing error does NOT name, by decision rather than by drift.
//
// StatusRunning: nobody who is SHOWN that error has status "running" (they
// would have merged), so naming a legacy synonym there is noise.
var mergeStatusesDeliberatelyOmittedFromHint = []string{
	state.StatusRunning,
}

// parseHintStatuses extracts the status names the precondition-4 error actually
// enumerates. It parses the REAL error rather than a mirror copy of it: a mirror
// is a third hand-maintained list, and a test that compares two of the three
// copies is green exactly when the copy it never looked at is the one that drifted.
func parseHintStatuses(t *testing.T, errMsg string) []string {
	t.Helper()
	const prefix = "Merge requires status "
	i := strings.Index(errMsg, prefix)
	if i < 0 {
		t.Fatalf("precondition-4 error %q does not contain %q; this test can no longer see the hint it checks", errMsg, prefix)
	}
	rest := errMsg[i+len(prefix):]
	j := strings.Index(rest, " — ")
	if j < 0 {
		t.Fatalf("precondition-4 error %q has no %q separator after the status list; cannot delimit the hint", errMsg, " — ")
	}
	list := rest[:j]

	var out []string
	for _, part := range strings.Split(list, ",") {
		for _, word := range strings.Split(part, " or ") {
			if w := strings.TrimSpace(word); w != "" {
				out = append(out, w)
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("parsed zero statuses out of hint %q", list)
	}
	return out
}

// TestMergePrecondition4_HintNamesEveryAllowedStatusExceptTheLegacySynonym
// makes divergence between the operator-facing error and the allow-set a
// decision instead of an accident, in BOTH directions: a status merge accepts
// but does not advertise, and a status the error promises but merge rejects.
//
// It reads the enumeration out of a real rejection, so there is no mirror list
// standing between the assertion and the string an operator sees.
func TestMergePrecondition4_HintNamesEveryAllowedStatusExceptTheLegacySynonym(t *testing.T) {
	sprawlRoot, agentName := mergeableSetupRoot(t, state.StatusKilled)
	_, err := Merge(context.Background(), mergeTestDeps(sprawlRoot), agentName, "", true, false, false)
	if err == nil {
		t.Fatal("Merge of a killed agent returned nil error; wanted the precondition-4 rejection that carries the hint")
	}

	named := map[string]bool{}
	for _, s := range parseHintStatuses(t, err.Error()) {
		named[s] = true
	}
	allowed := map[string]bool{}
	for _, s := range mergeAllowedStatuses {
		allowed[s] = true
	}
	omitted := map[string]bool{}
	for _, s := range mergeStatusesDeliberatelyOmittedFromHint {
		omitted[s] = true
	}

	// Direction 1: every allowed status is named, unless omitting it was a
	// decision recorded above.
	for _, s := range mergeAllowedStatuses {
		if !named[s] && !omitted[s] {
			t.Errorf("status %q is mergeable but the error does not name it. "+
				"Add it to the hint in merge.go, or to mergeStatusesDeliberatelyOmittedFromHint if leaving it out is deliberate.", s)
		}
	}
	// Direction 2: the error never promises a status merge would reject.
	for s := range named {
		if !allowed[s] {
			t.Errorf("the precondition-4 error names %q, but merge rejects it — the hint promises something that cannot work", s)
		}
	}
	// Direction 3: a recorded omission that is no longer omitted is a stale
	// decision, not a silent success.
	for _, s := range mergeStatusesDeliberatelyOmittedFromHint {
		if named[s] {
			t.Errorf("%q is recorded as deliberately omitted from the hint, but the error names it; drop it from mergeStatusesDeliberatelyOmittedFromHint", s)
		}
	}
}

func TestMergePrecondition4_AllowsEveryAllowedStatus(t *testing.T) {
	for _, s := range mergeAllowedStatuses {
		t.Run(s, func(t *testing.T) {
			sprawlRoot, agentName := mergeableSetupRoot(t, s)
			deps := mergeTestDeps(sprawlRoot)
			if _, err := Merge(context.Background(), deps, agentName, "", true, false, false); err != nil {
				t.Fatalf("Merge of status=%q: err = %v, want nil (status is in the allow-set)", s, err)
			}
		})
	}
}

func TestMergePrecondition4_RejectsEveryDeniedStatus(t *testing.T) {
	for _, s := range mergeDeniedStatuses {
		t.Run(s, func(t *testing.T) {
			sprawlRoot, agentName := mergeableSetupRoot(t, s)
			deps := mergeTestDeps(sprawlRoot)
			_, err := Merge(context.Background(), deps, agentName, "", true, false, false)
			if err == nil {
				t.Fatalf("Merge of status=%q: err = nil, want a precondition-4 rejection", s)
			}
			if !strings.Contains(err.Error(), "cannot be merged") {
				t.Fatalf("Merge of status=%q rejected for the WRONG reason: %v", s, err)
			}
			// /cli-ux-best-practices: the error must tell the caller what to
			// do next — and the advice must be ACTIONABLE for the status it is
			// given for. `wake` errors on retired/retiring, so those two get a
			// different remedy; suggesting a wake there would be advice that
			// cannot work.
			if state.IsTerminal(s) {
				if !strings.Contains(err.Error(), "retire --merge") {
					t.Errorf("Merge of terminal status=%q: error %q should point at retire --merge, not wake", s, err.Error())
				}
				if strings.Contains(err.Error(), "wake it first") {
					t.Errorf("Merge of terminal status=%q: error %q advises a wake, which errors for this status", s, err.Error())
				}
			} else if !strings.Contains(err.Error(), "wake") {
				t.Errorf("Merge of status=%q: error %q lacks a next-action hint", s, err.Error())
			}
		})
	}
}

// TestMergePrecondition4_EveryStatusConstantIsClassified is the forcing
// function against rot. It enumerates every state.Status* constant and fails
// if any is absent from BOTH lists above.
//
// Allow-sets are safe against a new status silently becoming mergeable, but
// they are NOT safe against a new status silently becoming UNmergeable — the
// author of a new status would never learn that merge had an opinion about
// it. This test makes introducing a status a decision instead of an
// inheritance.
//
// The constant list is maintained by hand because Go has no enum reflection;
// the guard is that adding a constant without adding it here leaves it in
// neither list only if the author also edits this file, and the diff makes
// that visible.
func TestMergePrecondition4_EveryStatusConstantIsClassified(t *testing.T) {
	all := []struct{ name, value string }{
		{"StatusActive", state.StatusActive},
		{"StatusRunning", state.StatusRunning},
		{"StatusSuspended", state.StatusSuspended},
		{"StatusKilled", state.StatusKilled},
		{"StatusRetired", state.StatusRetired},
		{"StatusRetiring", state.StatusRetiring},
		{"StatusDone", state.StatusDone},
		{"StatusResumeFailed", state.StatusResumeFailed},
		{"StatusFaulted", state.StatusFaulted},
		{"StatusStopped", state.StatusStopped},
		{"StatusPaused", state.StatusPaused},
		{"StatusDied", state.StatusDied},
		{"StatusComplete", state.StatusComplete},
		{"StatusIdle", state.StatusIdle},
	}

	classified := map[string]int{}
	for _, s := range mergeAllowedStatuses {
		classified[s]++
	}
	for _, s := range mergeDeniedStatuses {
		classified[s]++
	}
	for _, s := range mergeUnreachableStatuses {
		classified[s]++
	}

	for _, c := range all {
		switch classified[c.value] {
		case 0:
			t.Errorf("state.%s (%q) is in NEITHER the merge allow-set nor the deny list. "+
				"Adding a status must be a decision: put it in mergeAllowedStatuses, mergeDeniedStatuses or mergeUnreachableStatuses.",
				c.name, c.value)
		case 1: // exactly one list — correct
		default:
			t.Errorf("state.%s (%q) appears in BOTH lists", c.name, c.value)
		}
	}

	// The reverse direction: nothing in either list may be a status that no
	// longer exists as a constant.
	known := map[string]bool{}
	for _, c := range all {
		known[c.value] = true
	}
	for s := range classified {
		if !known[s] {
			t.Errorf("%q is classified by the merge lists but is not a state.Status* constant", s)
		}
	}
}
