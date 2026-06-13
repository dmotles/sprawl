// QUM-818 — cross-restart revive: on-demand Ensure-from-disk in Real.Wake.
//
// After a weave restart, RecoverAgents deliberately does NOT auto-resume (and
// therefore never Ensures into the in-memory registry) agents whose last
// report was `complete`. Before this fix, Real.Wake resolved the target ONLY
// via runtimeRegistry.Get, so a parked `complete` agent that was visible on
// disk (status/peek read disk directly) returned `agent %q not found` from
// wake/delegate/send_message.
//
// These tests start from an EMPTY registry (the post-restart condition the
// existing complete-lifecycle tests mask by pre-seeding via
// ensureRuntimeWithStarter) and assert that Real.Wake repopulates the runtime
// from disk and resumes by the persisted SessionID — instead of "not found".
package supervisor

import (
	"context"
	"fmt"
	"strings"
	"testing"

	agentpkg "github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/state"
)

// TestWake_RegistryMiss_CompleteOnDisk_EnsuresAndResumes pins QUM-818: a
// direct wake of a parked `complete` agent that is on disk but absent from the
// in-memory registry must Ensure a runtime from disk and resume by the
// persisted SessionID, NOT return "not found".
func TestWake_RegistryMiss_CompleteOnDisk_EnsuresAndResumes(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	shortenWakeTimeouts(t)
	agentState := testAgentState("alice")
	agentState.Status = state.StatusComplete
	agentState.SessionID = "sess-alice"
	saveTestAgent(t, tmpDir, agentState)

	starter := &wakeCapturingStarter{}
	installStarter(r, starter)

	// Precondition: registry must be empty (simulating post-restart).
	if _, ok := r.runtimeRegistry.Get("alice"); ok {
		t.Fatal("precondition: registry must be empty before Wake")
	}

	if _, err := r.Wake(context.Background(), "alice", agentpkg.WakeReasonBare, ""); err != nil {
		t.Fatalf("Wake on registry-miss complete agent returned error: %v; want nil (Ensure-from-disk)", err)
	}

	specs := starter.snapshotSpecs()
	if len(specs) == 0 {
		t.Fatal("starter received zero specs; want at least one (Ensure-from-disk wake must call Start)")
	}
	if !specs[0].Resume {
		t.Errorf("specs[0].Resume = false, want true (must attempt --resume first)")
	}
	if specs[0].SessionID != "sess-alice" {
		t.Errorf("specs[0].SessionID = %q, want %q (resume by prior session-id)", specs[0].SessionID, "sess-alice")
	}

	// Ensure side effect: the runtime is now registered.
	if _, ok := r.runtimeRegistry.Get("alice"); !ok {
		t.Error("registry still has no runtime for alice after Wake; want Ensure to have registered it")
	}
}

// TestDelegate_RegistryMiss_CompleteOnDisk_AutoWakes pins QUM-818 for delegate:
// a delegate to a parked `complete` agent absent from the registry must
// auto-wake (no flag), resume by session-id, and enqueue the task.
func TestDelegate_RegistryMiss_CompleteOnDisk_AutoWakes(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	shortenWakeTimeouts(t)
	agentState := testAgentState("alice")
	agentState.Status = state.StatusComplete
	agentState.SessionID = "sess-alice"
	saveTestAgent(t, tmpDir, agentState)

	starter := &wakeCapturingStarter{}
	installStarter(r, starter)

	if _, ok := r.runtimeRegistry.Get("alice"); ok {
		t.Fatal("precondition: registry must be empty before Delegate")
	}

	task := "do X"
	if err := r.Delegate(context.Background(), "alice", task, false /* wake_if_offline */); err != nil {
		t.Fatalf("Delegate on registry-miss complete agent returned error: %v; want nil (auto-wake)", err)
	}

	specs := starter.snapshotSpecs()
	if len(specs) == 0 {
		t.Fatal("starter received zero specs; want at least one")
	}
	if !specs[0].Resume {
		t.Errorf("specs[0].Resume = false, want true")
	}
	if specs[0].SessionID != "sess-alice" {
		t.Errorf("specs[0].SessionID = %q, want %q", specs[0].SessionID, "sess-alice")
	}
	wantInjection := fmt.Sprintf(agentpkg.WakePromptDelegate, "complete", task)
	if specs[0].RestartInjection != wantInjection {
		t.Errorf("RestartInjection mismatch\n got: %q\nwant: %q", specs[0].RestartInjection, wantInjection)
	}

	if _, ok := r.runtimeRegistry.Get("alice"); !ok {
		t.Fatal("registry has no runtime for alice after Delegate; want Ensure to have registered it")
	}
	// Disk truth: the task must be durably enqueued, not just reflected in the
	// in-memory snapshot counter.
	tasks, err := state.ListTasks(tmpDir, "alice")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("persisted tasks = %d, want 1", len(tasks))
	}
	if tasks[0].Prompt != task {
		t.Errorf("persisted task prompt = %q, want %q", tasks[0].Prompt, task)
	}
}

// TestSendMessage_RegistryMiss_CompleteOnDisk_AutoWakes pins QUM-818 for
// send_message: identical chokepoint via Real.Wake.
func TestSendMessage_RegistryMiss_CompleteOnDisk_AutoWakes(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	shortenWakeTimeouts(t)
	agentState := testAgentState("alice")
	agentState.Status = state.StatusComplete
	agentState.SessionID = "sess-alice"
	saveTestAgent(t, tmpDir, agentState)

	starter := &wakeCapturingStarter{}
	installStarter(r, starter)

	if _, ok := r.runtimeRegistry.Get("alice"); ok {
		t.Fatal("precondition: registry must be empty before SendMessage")
	}

	body := "hello"
	res, err := r.SendMessage(context.Background(), "alice", body, false /* interrupt */, false /* wake_if_offline */)
	if err != nil {
		t.Fatalf("SendMessage on registry-miss complete agent returned error: %v; want nil (auto-wake)", err)
	}
	if res == nil || res.MessageID == "" {
		t.Fatalf("SendMessage result = %+v, want non-empty MessageID", res)
	}

	specs := starter.snapshotSpecs()
	if len(specs) == 0 {
		t.Fatal("starter received zero specs; want at least one")
	}
	if !specs[0].Resume {
		t.Errorf("specs[0].Resume = false, want true")
	}
	if specs[0].SessionID != "sess-alice" {
		t.Errorf("specs[0].SessionID = %q, want %q", specs[0].SessionID, "sess-alice")
	}

	entries, err := agentloop.ListPending(tmpDir, "alice")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending entries = %d, want 1", len(entries))
	}
	if entries[0].Body != body {
		t.Errorf("persisted body = %q, want %q", entries[0].Body, body)
	}
}

// TestDelegate_RegistryMiss_FaultedOnDisk_WakeIfOffline_Resumes pins QUM-818
// for the broader offline family: a `faulted` agent on disk but absent from
// the registry, delegated with wake_if_offline=true, must also Ensure-from-disk
// and resume — the same latent gap as `complete`.
func TestDelegate_RegistryMiss_FaultedOnDisk_WakeIfOffline_Resumes(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	shortenWakeTimeouts(t)
	agentState := testAgentState("alice")
	agentState.Status = state.StatusFaulted
	agentState.SessionID = "sess-alice"
	saveTestAgent(t, tmpDir, agentState)

	starter := &wakeCapturingStarter{}
	installStarter(r, starter)

	if _, ok := r.runtimeRegistry.Get("alice"); ok {
		t.Fatal("precondition: registry must be empty before Delegate")
	}

	if err := r.Delegate(context.Background(), "alice", "do X", true /* wake_if_offline */); err != nil {
		t.Fatalf("Delegate on registry-miss faulted agent (wake_if_offline) returned error: %v; want nil", err)
	}

	specs := starter.snapshotSpecs()
	if len(specs) == 0 {
		t.Fatal("starter received zero specs; want at least one")
	}
	if !specs[0].Resume {
		t.Errorf("specs[0].Resume = false, want true")
	}
	if specs[0].SessionID != "sess-alice" {
		t.Errorf("specs[0].SessionID = %q, want %q", specs[0].SessionID, "sess-alice")
	}
}

// TestWake_RegistryMiss_RetiredOrRetiring_StillErrors pins the negative case:
// terminal {retired, retiring} agents on disk must still error from a direct
// wake even when the registry is empty — no regression to terminal gating.
func TestWake_RegistryMiss_RetiredOrRetiring_StillErrors(t *testing.T) {
	for _, st := range []string{state.StatusRetired, state.StatusRetiring} {
		t.Run(st, func(t *testing.T) {
			r, tmpDir := newFakeReal(t)
			agentState := testAgentState("alice")
			agentState.Status = st
			saveTestAgent(t, tmpDir, agentState)

			starter := &wakeCapturingStarter{}
			installStarter(r, starter)

			if _, ok := r.runtimeRegistry.Get("alice"); ok {
				t.Fatal("precondition: registry must be empty before Wake")
			}

			_, err := r.Wake(context.Background(), "alice", agentpkg.WakeReasonBare, "")
			if err == nil {
				t.Fatalf("Wake on %s agent returned nil error; want terminal rejection", st)
			}
			if !strings.Contains(err.Error(), "not found") {
				t.Errorf("error %q missing 'not found' (terminal-on-disk miss should not Ensure)", err.Error())
			}
			if len(starter.snapshotSpecs()) != 0 {
				t.Errorf("starter received specs for terminal agent; want zero (no runtime construction/start)")
			}
		})
	}
}
