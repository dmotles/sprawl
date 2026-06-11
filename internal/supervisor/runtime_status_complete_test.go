package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentops"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// QUM-787 — explicit Stop after the agent reported state=complete must
// record a durable StatusComplete (not StatusStopped, not StatusFaulted).
// Both the in-memory snapshot.Status and the disk Status must reflect
// "complete" so the agent is later revivable via wake/delegate per the
// QUM-786 lifecycle arc.
func TestStopWithFunc_CompleteReportRecordsDurableComplete(t *testing.T) {
	root := t.TempDir()
	agent := testAgentState("stop-complete")
	agent.LastReportState = agentops.ReportStateComplete
	saveTestAgent(t, root, agent)

	session := &runtimeTestSession{
		sessionID: "sess-stop-complete",
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
	if snap.Status != state.StatusComplete {
		t.Errorf("snapshot.Status = %q, want %q (Stop after complete report should record complete)",
			snap.Status, state.StatusComplete)
	}

	loaded, err := state.LoadAgent(root, "stop-complete")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if loaded.Status != state.StatusComplete {
		t.Errorf("on-disk Status = %q, want %q", loaded.Status, state.StatusComplete)
	}
}

// QUM-787 — explicit Stop with NO completion report on file is the
// "unexpected clean exit" path: the system thought the agent was working
// but the subprocess exited deliberately without reporting done. The
// durable status must be StatusFaulted (NOT StatusStopped; the set-site
// no longer writes StatusStopped).
func TestStopWithFunc_NoReportRecordsDurableFaulted(t *testing.T) {
	root := t.TempDir()
	agent := testAgentState("stop-noreport")
	// LastReportState intentionally empty.
	saveTestAgent(t, root, agent)

	session := &runtimeTestSession{
		sessionID: "sess-stop-noreport",
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
		t.Errorf("snapshot.Status = %q, want %q (Stop without complete report should record faulted)",
			snap.Status, state.StatusFaulted)
	}

	loaded, err := state.LoadAgent(root, "stop-noreport")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if loaded.Status != state.StatusFaulted {
		t.Errorf("on-disk Status = %q, want %q", loaded.Status, state.StatusFaulted)
	}
}

// QUM-787 — watchHandleExit on an expected (non-faulted) exit must split
// the durable status based on LastReportState: complete → StatusComplete,
// otherwise → StatusFaulted. There is no remaining set-site for
// StatusStopped on the watch path.
func TestWatchHandleExit_ExpectedExitWithCompleteReport(t *testing.T) {
	root := t.TempDir()
	agent := testAgentState("watch-complete")
	agent.LastReportState = agentops.ReportStateComplete
	saveTestAgent(t, root, agent)

	sess := &faultChainSession{}
	urt := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name:    "watch-complete",
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

	// Deliberate Stop → expectingExit=true → watchHandleExit takes the
	// "expected exit" branch.
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for rt.Snapshot().Status != state.StatusComplete {
		if time.Now().After(deadline) {
			t.Fatalf("post-exit Status = %q, want %q", rt.Snapshot().Status, state.StatusComplete)
		}
		time.Sleep(10 * time.Millisecond)
	}

	loaded, err := state.LoadAgent(root, "watch-complete")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if loaded.Status != state.StatusComplete {
		t.Errorf("on-disk Status = %q, want %q", loaded.Status, state.StatusComplete)
	}
}

// QUM-787 — Real.Shutdown must promote an agent to StatusSuspended when
// the post-Stop disk Status was just landed by stopWithFunc (no complete
// report on file → StatusFaulted under the new semantics) — i.e. the
// pre-Stop disk Status was a healthy non-fault state. This pins the
// preStop disk-capture branch added in Real.Shutdown so the legacy
// active→suspended Shutdown contract still holds under QUM-787.
//
// This is covered as a behavioral matrix in TestRealShutdown_TransitionMatrix
// (the running→suspended / active→suspended / suspended→suspended cases
// all exercise the new preStop-aware promotion). The faulted→faulted
// case in that same matrix covers the preserve branch.

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
