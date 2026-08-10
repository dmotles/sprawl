package agentops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
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
	state.StatusIdle,
	state.StatusSuspended,
	state.StatusComplete,
}

// mergeDeniedStatuses is every other status constant that precondition 4 can
// actually observe. Kept as an explicit list rather than "whatever is not
// allowed" so that TestMergePrecondition4_EveryStatusConstantIsClassified can
// force a decision when a new status is introduced.
var mergeDeniedStatuses = []string{
	state.StatusRunning,
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
			// do next, not merely that it refused.
			if !strings.Contains(err.Error(), "wake") {
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
