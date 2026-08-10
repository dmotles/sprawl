package agentops

import (
	"context"
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

// mergeDeniedStatuses is every other status constant that precondition 4 can
// actually observe. Kept as an explicit list rather than "whatever is not
// allowed" so that TestMergePrecondition4_EveryStatusConstantIsClassified can
// force a decision when a new status is introduced.
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
var mergeUnreachableStatuses = []string{
	// StatusStopped is the legacy sentinel; the always-on QUM-787 read
	// migration rewrites it before any caller sees it.
	state.StatusStopped,
}

// TestMergePrecondition4_StoppedIsRewrittenBeforeItIsSeen documents WHY
// StatusStopped is unreachable, so the claim is checked rather than asserted
// in a comment. If the migration ever stops firing, this fails and the status
// moves back into one of the reachable lists.
func TestMergePrecondition4_StoppedIsRewrittenBeforeItIsSeen(t *testing.T) {
	sprawlRoot, agentName := mergeableSetupRoot(t, state.StatusStopped)
	loaded, err := state.LoadAgent(sprawlRoot, agentName)
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if loaded.Status == state.StatusStopped {
		t.Fatalf("Status survived load as %q; StatusStopped is reachable after all and must be classified as allowed or denied", loaded.Status)
	}
}

// TestMergePrecondition4_RunningIsTheLivenessTwinOfActive checks the reason
// StatusRunning is in the allow-set, rather than asserting it in a comment —
// the same discipline as TestMergePrecondition4_StoppedIsRewrittenBeforeItIsSeen
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
