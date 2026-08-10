// QUM-789 lifecycle arc #2 — auto-wake-on-complete + retired/retiring gate.
//
// These tests pin the new behavior for delegate / send_message / peek when
// the recipient is in a post-QUM-787 terminal-class status:
//
//   - Status==complete  → delegate/send_message AUTO-WAKE without any flag;
//     peek returns introspection (no error).
//   - Status==retired/retiring → delegate/send_message return TerminalAgentError;
//     peek returns TerminalAgentError.
//   - Status==faulted/killed/died/paused/resume_failed remain governed by the
//     QUM-726 wake_if_offline gate; peek must NOT error
//     on these (they are introspectable).
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

// TestSendMessage_StatusComplete_AutoWakes_NoFlag mirrors the delegate test
// for send_message: a send_message targeting Status=complete must auto-wake
// and persist the body without any flag.
func TestSendMessage_StatusComplete_AutoWakes_NoFlag(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Status = state.StatusComplete
	saveTestAgent(t, tmpDir, agentState)

	starter := &wakeCapturingStarter{}
	_ = ensureRuntimeWithStarter(t, r, tmpDir, agentState, starter)

	body := "hello"
	res, err := r.SendMessage(context.Background(), "alice", body, false /* interrupt */, false /* wake_if_offline */)
	if err != nil {
		t.Fatalf("SendMessage on complete agent returned error: %v; want nil (auto-wake)", err)
	}
	if res == nil || res.MessageID == "" {
		t.Fatalf("SendMessage result = %+v, want non-empty MessageID", res)
	}

	specs := starter.snapshotSpecs()
	if len(specs) == 0 {
		t.Fatal("starter received zero specs; want at least one (auto-wake must call Start)")
	}
	wantInjection := fmt.Sprintf(agentpkg.WakePromptSendMessage, body)
	if specs[0].RestartInjection != wantInjection {
		t.Errorf("RestartInjection mismatch\n got: %q\nwant: %q", specs[0].RestartInjection, wantInjection)
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

// TestSendMessage_RetiredOrRetiring_ReturnsTerminalAgentError pins that both
// truly-terminal statuses reject a send with the canonical error.
//
// QUM-1186: this was retiring-only, and the retired case lived in the
// delegate twin (TestDelegate_RetiredOrRetiring_ReturnsTerminalAgentError),
// which is deleted with the tool. Widened to the full table here so deleting
// the delegate test does not quietly narrow the matrix — that would be a
// coverage loss disguised as a deletion.
func TestSendMessage_RetiredOrRetiring_ReturnsTerminalAgentError(t *testing.T) {
	for _, st := range []string{state.StatusRetired, state.StatusRetiring} {
		t.Run(st, func(t *testing.T) {
			r, tmpDir := newFakeReal(t)
			saveTestAgent(t, tmpDir, &state.AgentState{
				Name:   "alice",
				Type:   "engineer",
				Family: "engineering",
				Parent: "weave",
				Status: st,
			})

			_, err := r.SendMessage(context.Background(), "alice", "hello", false, false)
			if err == nil {
				t.Fatalf("SendMessage to %s agent returned nil error; want TerminalAgentError", st)
			}
			for _, want := range []string{"no longer running", `"alice"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing substring %q", err.Error(), want)
				}
			}
		})
	}
}

// TestPeek_StatusComplete_Succeeds pins QUM-789: peek on a complete agent
// returns introspection (no TerminalAgentError) — even with no live runtime.
func TestPeek_StatusComplete_Succeeds(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:            "alice",
		Type:            "engineer",
		Family:          "engineering",
		Parent:          "weave",
		Status:          state.StatusComplete,
		LastReportState: "complete",
		LastReportAt:    "2026-06-06T12:00:00Z",
	})

	res, err := r.Peek(context.Background(), "alice", 10)
	if err != nil {
		t.Fatalf("Peek on complete agent returned error: %v; want nil (introspectable)", err)
	}
	if res == nil {
		t.Fatal("Peek result = nil; want non-nil")
	}
	if res.Status != state.StatusComplete {
		t.Errorf("Peek.Status = %q, want %q", res.Status, state.StatusComplete)
	}
}

// TestPeek_FaultClasses_Succeed pins QUM-789: peek on faulted/killed/died/
// paused/resume_failed must succeed (introspectable) — only retired/retiring
// short-circuit to TerminalAgentError.
func TestPeek_FaultClasses_Succeed(t *testing.T) {
	classes := []string{
		state.StatusFaulted,
		state.StatusKilled,
		state.StatusDied,
		state.StatusPaused,
		state.StatusResumeFailed,
	}
	for _, st := range classes {
		t.Run(st, func(t *testing.T) {
			r, tmpDir := newFakeReal(t)
			saveTestAgent(t, tmpDir, &state.AgentState{
				Name:            "alice",
				Type:            "engineer",
				Family:          "engineering",
				Parent:          "weave",
				Status:          st,
				LastReportState: "failure",
				LastReportAt:    "2026-06-06T12:00:00Z",
			})

			res, err := r.Peek(context.Background(), "alice", 10)
			if err != nil {
				t.Fatalf("Peek on %s agent returned error: %v; want nil (introspectable)", st, err)
			}
			if res == nil || res.Status != st {
				t.Errorf("Peek.Status = %v, want %q", res, st)
			}
		})
	}
}

// TestPeek_RetiredOrRetiring_ReturnsTerminalAgentError pins QUM-789:
// peek narrows the TerminalAgentError gate to retired/retiring only.
func TestPeek_RetiredOrRetiring_ReturnsTerminalAgentError(t *testing.T) {
	for _, st := range []string{state.StatusRetired, state.StatusRetiring} {
		t.Run(st, func(t *testing.T) {
			r, tmpDir := newFakeReal(t)
			saveTestAgent(t, tmpDir, &state.AgentState{
				Name:            "alice",
				Type:            "engineer",
				Family:          "engineering",
				Parent:          "weave",
				Status:          st,
				LastReportState: "complete",
				LastReportAt:    "2026-06-06T12:00:00Z",
			})

			_, err := r.Peek(context.Background(), "alice", 10)
			if err == nil {
				t.Fatalf("Peek on %s agent returned nil error; want TerminalAgentError", st)
			}
			if !strings.Contains(err.Error(), "no longer running") {
				t.Errorf("error %q missing 'no longer running'", err.Error())
			}
		})
	}
}
