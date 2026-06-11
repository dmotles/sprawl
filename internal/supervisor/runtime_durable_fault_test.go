package supervisor

import (
	"context"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// QUM-625 slice M4 — RED-phase TDD for the durable-Faulted authority flip.
//
// These tests pin the M4 behavior change in watchHandleExit / stopWithFunc:
// when a live handle exits because it was terminally faulted, the supervisor
// must record a DURABLE Faulted status (in-memory snapshot.Status and a
// best-effort disk persist) instead of erasing the fault by flipping straight
// to Lifecycle=Stopped. A clean (non-faulted) exit and the deliberate Stop
// path must record the durable Stopped status. They are expected to FAIL
// against current HEAD (watchHandleExit erases to Stopped; stopWithFunc never
// stamps Status="stopped").

// TestWatchHandleExit_FaultedRecordsDurableFaulted exercises the real
// fault->Done()->watchHandleExit chain (mirroring the M2 fault-chain test) but
// against a real SprawlRoot with a persisted agent so the durable disk write
// can be observed. After the terminal fault fires and the handle exits, the
// supervisor must record a durable Faulted state both in-memory and on disk
// rather than erasing the fault to Stopped.
func TestWatchHandleExit_FaultedRecordsDurableFaulted(t *testing.T) {
	root := t.TempDir()
	agent := testAgentState("fault-durable")
	saveTestAgent(t, root, agent)

	sess := &faultChainSession{}
	urt := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name:    "fault-durable",
		Session: sess,
	})
	if err := urt.Start(context.Background()); err != nil {
		t.Fatalf("UnifiedRuntime.Start: %v", err)
	}
	handle := &faultChainHandle{rt: urt, sess: sess}

	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: root,
		Agent:      agent,
		Starter:    &faultChainStarter{handle: handle},
	})

	if err := rt.Start(); err != nil {
		t.Fatalf("AgentRuntime.Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = urt.Stop(stopCtx)
	})

	// Fire the terminal fault (production: session.setTerminalErr).
	sess.fireTerminalErr(backendpkg.ErrSubscriberWedged)

	// Poll up to 2s for the post-teardown durable state. watchHandleExit must
	// stamp snapshot.Status = StatusFaulted (NOT erase to Lifecycle=Stopped
	// with an empty Status).
	deadline := time.Now().Add(2 * time.Second)
	for rt.Snapshot().Status != state.StatusFaulted {
		if time.Now().After(deadline) {
			t.Fatalf("post-teardown snapshot.Status = %q, want %q (durable fault not recorded — watchHandleExit erased to Stopped)",
				rt.Snapshot().Status, state.StatusFaulted)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The unified-liveness projection of the post-teardown snapshot must be
	// Faulted (a durable on-disk "faulted" survives a stale lifecycle).
	snap := rt.Snapshot()
	if got := liveness.From(liveness.Snapshot{
		Lifecycle:  livenessToLifecycleString(snap.Liveness),
		DiskStatus: snap.Status,
	}).Liveness; got != liveness.Faulted {
		t.Errorf("post-teardown liveness projection = %v, want %v (Lifecycle=%q Status=%q)",
			got, liveness.Faulted, snap.Liveness, snap.Status)
	}

	// The durable fault must also be persisted to disk (best-effort SaveAgent).
	loaded, err := state.LoadAgent(root, "fault-durable")
	if err != nil {
		t.Fatalf("LoadAgent after fault: %v", err)
	}
	if loaded.Status != state.StatusFaulted {
		t.Errorf("on-disk Status = %q, want %q (durable fault not persisted)", loaded.Status, state.StatusFaulted)
	}
}

// TestWatchHandleExit_CleanStopWithoutReportRecordsFaulted pins the
// deliberate-Stop path when the agent had NOT reported state=complete:
// AgentRuntime.Stop must record a durable StatusFaulted (QUM-787; pre-arc
// this was StatusStopped, but StatusStopped is no longer a write target).
// A clean subprocess exit without a completion report is treated as the
// unexpected-exit case.
func TestWatchHandleExit_CleanStopWithoutReportRecordsFaulted(t *testing.T) {
	root := t.TempDir()
	agent := testAgentState("clean-stop")
	// LastReportState intentionally empty: pre-QUM-787 path landed in
	// StatusStopped; post-arc this lands in StatusFaulted.
	saveTestAgent(t, root, agent)

	session := &runtimeTestSession{
		sessionID: "sess-clean-stop",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	starter := &runtimeTestStarter{session: session}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: root,
		Agent:      agent,
		Starter:    starter,
	})

	if err := rt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	snap := rt.Snapshot()
	if snap.Status != state.StatusFaulted {
		t.Errorf("after clean Stop without complete report, snapshot.Status = %q, want %q",
			snap.Status, state.StatusFaulted)
	}
	if got := liveness.From(liveness.Snapshot{
		Lifecycle:  livenessToLifecycleString(snap.Liveness),
		DiskStatus: snap.Status,
	}).Liveness; got != liveness.Faulted {
		t.Errorf("post-Stop liveness projection = %v, want %v (Lifecycle=%q Status=%q)",
			got, liveness.Faulted, snap.Liveness, snap.Status)
	}
}

// TestWatchHandleExit_StopAfterCompleteReportRecordsComplete pins the
// QUM-786 contract that watchHandleExit lands in StatusComplete when the
// agent reported state=complete via Real.ReportStatus. Real.ReportStatus
// intentionally SKIPS syncRuntimeFromState on terminal reports, so the
// in-memory snapshot.LastReport.State is stale at teardown time; the
// classifier must read the canonical value from disk (agentops.Report
// wrote it synchronously) rather than trust the stale snapshot. Pre-fix
// against this guard the runtime stamped StatusFaulted, breaking the
// arc's central acceptance criterion.
func TestWatchHandleExit_StopAfterCompleteReportRecordsComplete(t *testing.T) {
	root := t.TempDir()
	agent := testAgentState("complete-after-report")
	// On-disk LastReportState empty at Start — Start() seeds the snapshot
	// from disk so the in-memory snapshot.LastReport.State is also empty
	// (this is what Real.ReportStatus's skip-sync optimization preserves).
	saveTestAgent(t, root, agent)

	session := &runtimeTestSession{
		sessionID: "sess-complete-after-report",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	starter := &runtimeTestStarter{session: session}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: root,
		Agent:      agent,
		Starter:    starter,
	})

	if err := rt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Simulate agentops.Report writing LastReportState=complete to disk
	// without syncing the in-memory snapshot — mirrors Real.ReportStatus's
	// skip-sync-on-terminal optimization (real.go ~2002).
	cur, err := state.LoadAgent(root, "complete-after-report")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	cur.LastReportState = "complete"
	if err := state.SaveAgent(root, cur); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	snap := rt.Snapshot()
	if snap.Status != state.StatusComplete {
		t.Errorf("after Stop following state=complete report, snapshot.Status = %q, want %q (watchHandleExit read stale in-memory LastReportState instead of disk)",
			snap.Status, state.StatusComplete)
	}
	loaded, err := state.LoadAgent(root, "complete-after-report")
	if err != nil {
		t.Fatalf("LoadAgent after Stop: %v", err)
	}
	if loaded.Status != state.StatusComplete {
		t.Errorf("on-disk Status after Stop = %q, want %q", loaded.Status, state.StatusComplete)
	}
}

// TestSnapshotFromAgentState_SeedsAllRestingStatuses (R7) guards that the
// runtime snapshot seeded from a disk AgentState projects onto the right
// unified liveness for every durable resting status. This is mostly a
// guard — liveness.From decodes DiskStatus directly — but it pins the
// freshly-added faulted/stopped statuses end-to-end through
// snapshotFromAgentState.
func TestSnapshotFromAgentState_SeedsAllRestingStatuses(t *testing.T) {
	cases := []struct {
		diskStatus string
		want       liveness.AgentLiveness
	}{
		{state.StatusSuspended, liveness.Suspended},
		{state.StatusResumeFailed, liveness.ResumeFailed},
		{state.StatusFaulted, liveness.Faulted},
		{state.StatusStopped, liveness.Stopped},
		{state.StatusKilled, liveness.Killed},
		{state.StatusRetired, liveness.Retired},
		{state.StatusRetiring, liveness.Retiring},
		// NOTE: "active" is deliberately omitted. snapshotFromAgentState maps a
		// disk Status="active" to Lifecycle=Registered with NO live handle, and
		// liveness.From(registered + DiskStatus="active") projects to Unstarted,
		// not Running — Running requires an attached/started live handle, which a
		// pure disk-seeded snapshot does not have. The task's "active"->Running
		// row only holds with a live handle; it is covered by the Start-path
		// tests, not by snapshotFromAgentState seeding.
	}
	for _, tc := range cases {
		t.Run(tc.diskStatus, func(t *testing.T) {
			snap := snapshotFromAgentState(&state.AgentState{Name: "x", Status: tc.diskStatus})
			got := liveness.From(liveness.Snapshot{
				Lifecycle:  livenessToLifecycleString(snap.Liveness),
				DiskStatus: snap.Status,
			}).Liveness
			if got != tc.want {
				t.Errorf("disk Status %q -> liveness %v, want %v (Lifecycle=%q Status=%q)",
					tc.diskStatus, got, tc.want, snap.Liveness, snap.Status)
			}
		})
	}
}
