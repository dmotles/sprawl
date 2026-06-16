// Tests for QUM-550 slice 1: Real.SendMessage. These tests pin the unified
// send_message Supervisor method (the MCP send_async + send_interrupt collapse).
//
// QUM-821: send_message(interrupt=true) no longer takes a separate force-
// interrupt path. Both interrupt=true and interrupt=false deliver via the
// cooperative WakeForDelivery; urgency for interrupt=true is carried by the
// enqueued ClassInterrupt entry, which drains to stdin at priority "now". The
// bare Session.Interrupt frame is reserved for Esc-abort and is never issued for
// message delivery — so these tests assert session.interrupts == 0.
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

	res, err := r.SendMessage(context.Background(), "alice", "hello body", false, false)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res == nil || res.MessageID == "" {
		t.Fatalf("SendMessage result = %+v, want non-empty MessageID", res)
	}

	// QUM-549 lock-in: cooperative wake path must NOT call Session.Interrupt.
	if got := session.interrupts.Load(); got != 0 {
		t.Errorf("session.Interrupt called %d times for interrupt=false send_message; want 0 (QUM-549)", got)
	}
	// Cooperative wake path MUST have signalled the WakeForDelivery counter at
	// least once.
	if got := session.wakeForDeliveryCalls.Load(); got < 1 {
		t.Errorf("session.WakeForDelivery calls = %d, want >= 1", got)
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

// TestReal_SendMessage_InterruptTrue_RoutesViaNowPriorityWake pins the QUM-821
// urgency tier: send_message(interrupt=true) persists a ClassInterrupt entry and
// cooperatively wakes the recipient (WakeForDelivery) so the entry drains to
// stdin at priority "now". The bare Session.Interrupt frame is NO LONGER issued
// for delivery — urgency is carried by the message priority, and the bare
// interrupt is reserved for Esc-abort only (QUM-619 idle-interrupt-inject path
// deleted).
func TestReal_SendMessage_InterruptTrue_RoutesViaNowPriorityWake(t *testing.T) {
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

	res, err := r.SendMessage(context.Background(), "alice", "stop now", true, false)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res == nil || res.MessageID == "" {
		t.Fatalf("SendMessage result = %+v, want non-empty MessageID", res)
	}
	if !res.Interrupted {
		t.Error("res.Interrupted = false, want true for interrupt=true (API contract preserved)")
	}

	// QUM-821: interrupt=true now cooperatively wakes (the now-priority drain
	// does the preempting) and must NOT issue a bare Session.Interrupt frame.
	if got := session.wakeForDeliveryCalls.Load(); got < 1 {
		t.Errorf("session.WakeForDelivery calls = %d, want >= 1 (interrupt=true routes via now-priority wake)", got)
	}
	if got := session.interrupts.Load(); got != 0 {
		t.Errorf("session.Interrupt calls = %d, want 0 (bare interrupt is Esc-only — QUM-821)", got)
	}

	// Persistence: ClassInterrupt (the carrier of now-priority into the drain).
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

// TestReal_SendMessage_InterruptTrue_WhenIdle_NoBareInterrupt pins that an idle
// recipient is woken via the now-priority delivery path (a stdin write wakes the
// CLI command queue) and is NOT bare-interrupted (QUM-821 deletes the QUM-619
// idle-interrupt-inject content path that used to cancel the idle recipient's
// turn).
func TestReal_SendMessage_InterruptTrue_WhenIdle_NoBareInterrupt(t *testing.T) {
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

	if _, err := r.SendMessage(context.Background(), "alice", "stop now", true, false); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if got := session.wakeForDeliveryCalls.Load(); got < 1 {
		t.Errorf("session.WakeForDelivery calls = %d, want >= 1 (idle recipient woken via now-priority delivery)", got)
	}
	if got := session.interrupts.Load(); got != 0 {
		t.Errorf("session.Interrupt calls = %d, want 0 (idle recipient must NOT be bare-interrupted — QUM-821)", got)
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
	_, err := r.SendMessage(ctx, "alice", "stop", true, false)
	if err == nil {
		t.Fatal("SendMessage(interrupt=true) sibling→sibling returned nil error; want ancestor-gate rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ancestor") && !strings.Contains(msg, "§8.5") {
		t.Errorf("error message %q should mention 'ancestor' or '§8.5' (parent→descendants gate)", msg)
	}
}

// TestReal_SendMessage_TerminalStatus_ReturnsClearerError pins QUM-680: when
// the recipient is persisted with a terminal lifecycle status (stopped /
// retired) AND no live runtime is registered for it, SendMessage must surface
// a clear "no longer running" error referencing the last reported state and
// timestamp — rather than silently enqueueing into a dead agent's pending
// queue. Note: faulted/killed/died/paused now fall under the QUM-726
// wake-on-traffic gate which returns a different canonical error.
func TestReal_SendMessage_TerminalStatus_ReturnsClearerError(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	// Seed a retired recipient. Deliberately do NOT register a runtime — the
	// terminal-status gate only fires when there is no live runtime to fall
	// back on. QUM-787: StatusRetired is the canonical truly-terminal status
	// after IsTerminal narrowed to {retired, retiring}; faulted/stopped flow
	// through the QUM-726 wake-on-traffic gate, not TerminalAgentError.
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:            "alice",
		Type:            "engineer",
		Family:          "engineering",
		Parent:          "weave",
		Status:          state.StatusRetired,
		LastReportState: "failure",
		LastReportAt:    "2026-06-06T12:00:00Z",
	})

	res, err := r.SendMessage(context.Background(), "alice", "hello body", false, false)
	if err == nil {
		t.Fatalf("SendMessage to faulted agent returned nil error; want descriptive terminal-status error (res=%+v)", res)
	}
	if res != nil {
		t.Errorf("SendMessage result = %+v, want nil when send fails", res)
	}
	for _, want := range []string{"no longer running", "failure", `"alice"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing substring %q", err.Error(), want)
		}
	}

	// Nothing should have been enqueued into the (dead) recipient's pending
	// queue.
	entries, listErr := agentloop.ListPending(tmpDir, "alice")
	if listErr != nil {
		// ListPending may return nil/empty for an absent queue dir — only
		// fail on truly unexpected errors.
		t.Fatalf("ListPending: %v", listErr)
	}
	if len(entries) != 0 {
		t.Errorf("pending entries = %d, want 0 (must not enqueue into terminal agent)", len(entries))
	}
}
