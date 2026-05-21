// Tests for QUM-550 slice 1: Real.SendMessage. These tests pin the new
// unified send_message Supervisor method (the MCP send_async + send_interrupt
// collapse) and the rewiring of legacy SendAsync / SendInterrupt to route
// through the cooperative-wake / force-interrupt paths respectively.
//
// RED phase: Real.SendMessage does not exist yet, and the runtimeTestSession
// fields wakeForDeliveryCalls / forceInterruptDeliveryCalls used below do not
// exist on the existing fake either — both are intentional compile-fail
// markers. When the implementation lands the missing symbols come with it and
// the tests turn green.
//
// Where the new RuntimeHandle methods are referenced (WakeForDelivery,
// ForceInterruptDelivery), the tests rely on the corresponding production
// counters (snapshot.WakeCount / snapshot.InterruptCount) staying consistent
// with the pre-existing wiring conventions established in real_runtime_test.go
// (TestRealSendAsync_SignalsInterruptAfterFullPersistence et al).
package supervisor

import (
	"context"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
)

// TestReal_SendMessage_InterruptFalse_DoesNotCallSessionInterrupt_EvenWhenTurnRunning
// pins the QUM-549 fix: send_message(interrupt=false) must persist + enqueue +
// cooperatively wake the recipient, never calling Session.Interrupt regardless
// of whether a turn is currently running. The wake path goes through the
// runtime handle's new WakeForDelivery method, which never forwards to the
// backend session.
func TestReal_SendMessage_InterruptFalse_DoesNotCallSessionInterrupt_EvenWhenTurnRunning(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	res, err := r.SendMessage(context.Background(), "alice", "hello body", false)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res == nil || res.MessageID == "" {
		t.Fatalf("SendMessage result = %+v, want non-empty MessageID", res)
	}

	// QUM-549 lock-in: cooperative wake path must NOT call Session.Interrupt.
	if session.interrupts != 0 {
		t.Errorf("session.Interrupt called %d times for interrupt=false send_message; want 0 (QUM-549)", session.interrupts)
	}
	// Cooperative wake path MUST have signalled the new WakeForDelivery
	// counter at least once; the existing InterruptDelivery counter must
	// stay at zero (cooperative ≠ interrupt-delivery).
	if session.wakeForDeliveryCalls < 1 {
		t.Errorf("session.WakeForDelivery calls = %d, want >= 1", session.wakeForDeliveryCalls)
	}
	if session.forceInterruptDeliveryCalls != 0 {
		t.Errorf("session.ForceInterruptDelivery calls = %d, want 0 for interrupt=false", session.forceInterruptDeliveryCalls)
	}

	// Persistence: the queue entry must be ClassAsync, body forwarded, subject empty.
	entries, err := agentloop.ListPending(tmpDir, "alice")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Class != agentloop.ClassAsync {
		t.Errorf("Class = %q, want async", e.Class)
	}
	if e.Body != "hello body" {
		t.Errorf("Body = %q, want %q", e.Body, "hello body")
	}
	if e.Subject != "" {
		t.Errorf("Subject = %q, want empty (send_message takes no subject)", e.Subject)
	}
}

// TestReal_SendMessage_InterruptTrue_CallsSessionInterrupt_WhenTurnRunningTrue
// pins the force-interrupt path when a turn is in flight: send_message(
// interrupt=true) must invoke Session.Interrupt on the recipient.
func TestReal_SendMessage_InterruptTrue_CallsSessionInterrupt_WhenTurnRunningTrue(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	// Caller is weave (the supervisor's default callerName) → recipient is
	// "alice" whose Parent is weave (testAgentState default). Ancestor gate
	// is satisfied.
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	res, err := r.SendMessage(context.Background(), "alice", "stop now", true)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res == nil || res.MessageID == "" {
		t.Fatalf("SendMessage result = %+v, want non-empty MessageID", res)
	}
	if !res.Interrupted {
		t.Error("res.Interrupted = false, want true for interrupt=true")
	}

	if session.forceInterruptDeliveryCalls < 1 {
		t.Errorf("session.ForceInterruptDelivery calls = %d, want >= 1", session.forceInterruptDeliveryCalls)
	}

	// Persistence: ClassInterrupt.
	entries, err := agentloop.ListPending(tmpDir, "alice")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending entries = %d, want 1", len(entries))
	}
	if entries[0].Class != agentloop.ClassInterrupt {
		t.Errorf("Class = %q, want interrupt", entries[0].Class)
	}
}

// TestReal_SendMessage_InterruptTrue_CallsSessionInterrupt_WhenIdle pins the
// QUM-549 blind-spot fix: send_message(interrupt=true) must call
// Session.Interrupt unconditionally — even when the recipient is idle.
func TestReal_SendMessage_InterruptTrue_CallsSessionInterrupt_WhenIdle(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	if _, err := r.SendMessage(context.Background(), "alice", "stop now", true); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if session.forceInterruptDeliveryCalls < 1 {
		t.Errorf("session.ForceInterruptDelivery calls = %d, want >= 1 (idle recipient must still be interrupted — QUM-549)", session.forceInterruptDeliveryCalls)
	}
}

// TestReal_SendMessage_InterruptTrue_RequiresAncestor pins the §8.5 gate:
// callers that are not an ancestor of `to` cannot use interrupt=true.
func TestReal_SendMessage_InterruptTrue_RequiresAncestor(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	// Two siblings under weave. "bob" tries to interrupt "alice" — not an
	// ancestor, so the gate should reject. Persist a weave root-state file
	// so the ancestry walk terminates cleanly at the root.
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "weave", Type: "root", Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "alice", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "bob", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active",
	})

	ctx := backendpkg.WithCallerIdentity(context.Background(), "bob")
	_, err := r.SendMessage(ctx, "alice", "stop", true)
	if err == nil {
		t.Fatal("SendMessage(interrupt=true) sibling→sibling returned nil error; want ancestor-gate rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ancestor") && !strings.Contains(msg, "§8.5") {
		t.Errorf("error message %q should mention 'ancestor' or '§8.5' (parent→descendants gate)", msg)
	}
}
