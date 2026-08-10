package supervisor

import (
	"testing"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// QUM-1186 (D3): this file used to also hold the three tests that pinned the
// LastReportState-driven teardown classifier — Stop-after-complete-report =>
// StatusComplete, Stop-without-report => StatusFaulted, and the
// watchHandleExit twin. That classifier is gone: teardown now branches on an
// in-memory stop REASON, and an expected exit is never a fault. The
// replacements live in runtime_stop_reason_test.go. The SyncAgentState
// projection below is unrelated to the classifier and survives unchanged.

// QUM-787 — SyncAgentState's resting-liveness projection must treat
// StatusComplete identically to StatusStopped: when there is no live
// handle and disk Status is complete, the snapshot's Liveness collapses
// to Unstarted so liveness.From can decode DiskStatus.
func TestSyncAgentState_StatusCompleteProjectsToUnstarted(t *testing.T) {
	root := t.TempDir()
	agent := testAgentState("sync-complete")
	saveTestAgent(t, root, agent)

	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: root,
		Agent:      agent,
	})

	// Simulate a torn-down agent whose disk Status has been stamped to
	// complete (e.g. by the stopWithFunc path above).
	completed := testAgentState("sync-complete")
	completed.Status = state.StatusComplete
	rt.SyncAgentState(completed)

	snap := rt.Snapshot()
	if snap.Liveness != liveness.Unstarted {
		t.Errorf("post-sync Liveness = %v, want %v (complete must collapse to Unstarted with no live handle)",
			snap.Liveness, liveness.Unstarted)
	}
	if snap.Status != state.StatusComplete {
		t.Errorf("post-sync Status = %q, want %q", snap.Status, state.StatusComplete)
	}
}
