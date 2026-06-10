// QUM-725: route-up tests for Real.SendMessage and Real.ReportStatus.
// RED phase — the dead-target rerouting path does not exist yet. These
// tests pin the contract:
//
//   - send_message to a Died target routes to the first live ancestor, with
//     the body wrapped via inboxprompt.WrapForDeadTarget.
//   - multi-hop dead chain enumerates every dead name in the wrapper.
//   - report_status from a child to a Died parent likewise routes up to a
//     live grandparent — the SendStatusChange envelope's `summary` field is
//     wrapped.
//   - interrupt=true sends against a dead descendant continue to enforce the
//     §8.5 ancestor gate against the ORIGINAL `to` first. When the gate is
//     satisfied, route-up still happens with the wrapped body landing in the
//     live ancestor.
package supervisor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
)

// TestReal_SendMessage_DeadTarget_SingleHop_RoutesUpToParent: target is
// persisted with Status="died" (projects to liveness.Died with no runtime).
// Parent is alive. After send, parent's pending queue has 1 entry with the
// wrapper body; target's queue is empty.
func TestReal_SendMessage_DeadTarget_SingleHop_RoutesUpToParent(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	// weave (root, alive by construction) -> alice (DIED)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "weave", Type: "root", Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "alice", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: state.StatusDied,
	})

	res, err := r.SendMessage(context.Background(), "alice", "hello body", false, false)
	if err != nil {
		t.Fatalf("SendMessage to dead alice: %v", err)
	}
	if res == nil || res.MessageID == "" {
		t.Fatalf("SendMessage result = %+v, want non-empty MessageID (route-up succeeds)", res)
	}

	// Target's queue MUST stay empty — the message routed up.
	if entries, _ := agentloop.ListPending(tmpDir, "alice"); len(entries) != 0 {
		t.Errorf("dead target pending entries = %d, want 0 (must not enqueue into dead agent)", len(entries))
	}

	// Parent (weave) receives the wrapped body. Caller defaults to "weave"
	// in tests; the originating sender in the wrapper should reflect that.
	entries, err := agentloop.ListPending(tmpDir, "weave")
	if err != nil {
		t.Fatalf("ListPending(weave): %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("weave pending entries = %d, want 1", len(entries))
	}
	wantBody := inboxprompt.WrapForDeadTarget("weave", "alice", []string{"alice"}, "hello body")
	if entries[0].Body != wantBody {
		t.Errorf("routed body mismatch\n got: %q\nwant: %q", entries[0].Body, wantBody)
	}
}

// TestReal_SendMessage_DeadTarget_MultiHop_RoutesUpToGrandparent: target +
// parent both Died; grandparent alive. The wrapper enumerates both dead
// names in chain order.
func TestReal_SendMessage_DeadTarget_MultiHop_RoutesUpToGrandparent(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "weave", Type: "root", Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "manager", Type: "manager", Family: "engineering",
		Parent: "weave", Status: state.StatusDied,
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "engineer", Type: "engineer", Family: "engineering",
		Parent: "manager", Status: state.StatusDied,
	})

	if _, err := r.SendMessage(context.Background(), "engineer", "hi", false, false); err != nil {
		t.Fatalf("SendMessage multi-hop dead: %v", err)
	}

	if entries, _ := agentloop.ListPending(tmpDir, "engineer"); len(entries) != 0 {
		t.Errorf("engineer pending = %d, want 0", len(entries))
	}
	if entries, _ := agentloop.ListPending(tmpDir, "manager"); len(entries) != 0 {
		t.Errorf("manager pending = %d, want 0 (dead parent must not receive)", len(entries))
	}

	entries, err := agentloop.ListPending(tmpDir, "weave")
	if err != nil {
		t.Fatalf("ListPending(weave): %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("weave pending = %d, want 1", len(entries))
	}
	wantBody := inboxprompt.WrapForDeadTarget("weave", "engineer", []string{"engineer", "manager"}, "hi")
	if entries[0].Body != wantBody {
		t.Errorf("multi-hop routed body mismatch\n got: %q\nwant: %q", entries[0].Body, wantBody)
	}
}

// TestReal_ReportStatus_DeadParent_RoutesToLiveGrandparent: child reports
// status; its parent is Died; routing must redirect the SendStatusChange
// envelope to the live grandparent with the `summary` field wrapped.
func TestReal_ReportStatus_DeadParent_RoutesToLiveGrandparent(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "weave", Type: "root", Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "manager", Type: "manager", Family: "engineering",
		Parent: "weave", Status: state.StatusDied,
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "child", Type: "engineer", Family: "engineering",
		Parent: "manager", Status: "active",
	})

	if _, err := r.ReportStatus(context.Background(), "child", "working", "polishing tests"); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}

	// Manager (dead) must NOT receive the status_change envelope.
	if envs, _ := messages.DrainStatusChange(tmpDir, "manager"); len(envs) != 0 {
		t.Errorf("dead manager status_change envelopes = %d, want 0", len(envs))
	}

	// Live grandparent (weave) must receive the routed-up envelope.
	envs, err := messages.DrainStatusChange(tmpDir, "weave")
	if err != nil {
		t.Fatalf("DrainStatusChange(weave): %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("weave status_change envelopes = %d, want 1", len(envs))
	}
	var payload messages.StatusChangePayload
	if err := json.Unmarshal([]byte(envs[0].Body), &payload); err != nil {
		t.Fatalf("decode status_change payload: %v", err)
	}
	wantSummary := inboxprompt.WrapForDeadTarget("child", "manager", []string{"manager"}, "polishing tests")
	if payload.Summary != wantSummary {
		t.Errorf("routed status_change summary mismatch\n got: %q\nwant: %q", payload.Summary, wantSummary)
	}
	if payload.State != "working" {
		t.Errorf("routed status_change state = %q, want %q", payload.State, "working")
	}
}

// TestReal_SendMessage_InterruptTrue_DeadDescendant_GateFiresOnOriginalTarget:
// the §8.5 ancestor-gate is evaluated against the *original* `to` first; a
// sibling that fails the gate must still be rejected with the existing
// error message, regardless of whether the recipient is dead.
func TestReal_SendMessage_InterruptTrue_DeadDescendant_GateFiresOnOriginalTarget(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "weave", Type: "root", Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "alice", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: state.StatusDied,
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "bob", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: "active",
	})

	// bob (sibling, not ancestor) tries interrupt=true on dead alice.
	ctx := backendpkg.WithCallerIdentity(context.Background(), "bob")
	_, err := r.SendMessage(ctx, "alice", "stop", true, false)
	if err == nil {
		t.Fatal("SendMessage(interrupt=true) sibling -> dead sibling returned nil error; want §8.5 gate rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ancestor") && !strings.Contains(msg, "§8.5") {
		t.Errorf("gate error %q should still mention 'ancestor' or '§8.5' — even for dead targets", msg)
	}
}

// TestReal_SendMessage_InterruptTrue_DeadDescendant_GatePass_RoutesUp: when
// the caller IS an ancestor of the original dead target, the gate passes,
// and the routed-up wrapped message lands in the (live) intermediate or
// grandparent ancestor.
//
// Setup: weave (caller, root, alive) -> manager (alive) -> engineer (DIED).
// Caller = weave. Original target = engineer. The first live ancestor walking
// up from engineer is "manager" — that's where the wrapped body must land.
func TestReal_SendMessage_InterruptTrue_DeadDescendant_GatePass_RoutesUp(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "weave", Type: "root", Status: "active",
	})
	managerState := &state.AgentState{
		Name: "manager", Type: "manager", Family: "engineering",
		Parent: "weave", Status: "active",
	}
	saveTestAgent(t, tmpDir, managerState)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "engineer", Type: "engineer", Family: "engineering",
		Parent: "manager", Status: state.StatusDied,
	})

	// Manager needs a running runtime so the rerouted interrupt can actually
	// force-interrupt that live recipient.
	session := &runtimeTestSession{
		sessionID: "sess-manager",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, managerState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	// Caller weave (default in tests) is ancestor of engineer (via manager).
	if _, err := r.SendMessage(context.Background(), "engineer", "stop now", true, false); err != nil {
		t.Fatalf("SendMessage(interrupt=true) ancestor -> dead descendant: %v", err)
	}

	// engineer's queue empty; manager's queue holds wrapped interrupt entry.
	if entries, _ := agentloop.ListPending(tmpDir, "engineer"); len(entries) != 0 {
		t.Errorf("engineer pending = %d, want 0", len(entries))
	}
	entries, err := agentloop.ListPending(tmpDir, "manager")
	if err != nil {
		t.Fatalf("ListPending(manager): %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("manager pending = %d, want 1 (routed-up wrapped interrupt)", len(entries))
	}
	if entries[0].Class != agentloop.ClassInterrupt {
		t.Errorf("routed-up class = %q, want interrupt", entries[0].Class)
	}
	wantBody := inboxprompt.WrapForDeadTarget("weave", "engineer", []string{"engineer"}, "stop now")
	if entries[0].Body != wantBody {
		t.Errorf("routed-up interrupt body mismatch\n got: %q\nwant: %q", entries[0].Body, wantBody)
	}

	// The live ancestor's session must have received the force-interrupt.
	if got := session.forceInterruptDeliveryCalls.Load(); got < 1 {
		t.Errorf("manager.forceInterruptDeliveryCalls = %d, want >= 1 (interrupt must follow the routed-up message)", got)
	}
}
