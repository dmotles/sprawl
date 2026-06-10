// QUM-726: wake-on-traffic supervisor-level tests.
//
// These tests pin the new offline-target handling for Real.SendMessage and
// Real.Delegate when wake_if_offline is false vs true. They also pin the
// bare Real.Wake path's RestartInjection plumbing (WakeReasonBare).
//
// RED phase: the canonical "Delivery failed: …" error is not surfaced yet,
// and the runtime starter never sees a non-empty RestartInjection from a
// wake-on-traffic invocation. These tests fail until the implementation in
// real.go (SendMessage/Delegate offline-handling + Wake injection) and
// runtime.go (Wake threads restartInjection into both specs) is wired.
package supervisor

import (
	"context"
	"fmt"
	"testing"

	agentpkg "github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/state"
)

// offlineStatuses enumerates the disk-status tokens that project to
// offline-but-recoverable liveness. The §726 contract is the canonical error
// fires for ALL of them when wake_if_offline=false.
func offlineStatuses() []string {
	return []string{
		state.StatusPaused,
		state.StatusKilled,
		state.StatusDied,
		state.StatusFaulted,
		state.StatusResumeFailed,
	}
}

// canonicalOfflineError is the byte-pinned error string the canonical
// QUM-726 SendMessage offline-without-wake path must return. The %s is the
// agent's projected liveness state (e.g. "paused").
const canonicalOfflineError = "Delivery failed: agent %s is %s. Set wake_if_offline: true to wake and deliver."

func TestSendMessage_OfflineTarget_NoFlag_ReturnsCanonicalError(t *testing.T) {
	for _, st := range offlineStatuses() {
		t.Run(st, func(t *testing.T) {
			r, tmpDir := newFakeReal(t)
			saveTestAgent(t, tmpDir, &state.AgentState{
				Name:   "alice",
				Type:   "engineer",
				Family: "engineering",
				Parent: "weave",
				Status: st,
			})

			res, err := r.SendMessage(context.Background(), "alice", "hello body", false, false)
			if err == nil {
				t.Fatalf("SendMessage to %s agent returned nil error; want canonical wake-not-permitted error (res=%+v)", st, res)
			}
			if res != nil {
				t.Errorf("SendMessage result = %+v, want nil on failure", res)
			}
			// The error must explicitly nominate alice + advise the
			// wake_if_offline knob. Don't hard-pin the disk-status token
			// because the contract is projected-liveness which equals the
			// disk token in these isolated unit cases.
			want := fmt.Sprintf(canonicalOfflineError, "alice", st)
			if err.Error() != want {
				t.Errorf("error mismatch\n got: %q\nwant: %q", err.Error(), want)
			}

			// Nothing should have been enqueued: the offline guard fires
			// before persistence.
			entries, _ := agentloop.ListPending(tmpDir, "alice")
			if len(entries) != 0 {
				t.Errorf("pending entries = %d, want 0 (offline guard must precede enqueue)", len(entries))
			}
		})
	}
}

// TestSendMessage_OfflineTarget_WakeIfOffline_WakesAndInjectsPrompt pins the
// happy-path: paused recipient + wake_if_offline=true →
//   - the runtime starter receives a RuntimeStartSpec with RestartInjection
//     equal to BuildWakePrompt(WakeReasonSendMessage, "paused", body),
//   - the message body is persisted to the recipient's maildir / queue.
func TestSendMessage_OfflineTarget_WakeIfOffline_WakesAndInjectsPrompt(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Status = state.StatusPaused
	saveTestAgent(t, tmpDir, agentState)

	starter := &wakeCapturingStarter{}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, starter)
	_ = rt // runtime registered with the offline disk status; no Start() yet — the wake path drives starter.Start.

	body := "hello"
	res, err := r.SendMessage(context.Background(), "alice", body, false, true)
	if err != nil {
		t.Fatalf("SendMessage with wake_if_offline=true: %v", err)
	}
	if res == nil || res.MessageID == "" {
		t.Fatalf("SendMessage result = %+v, want non-empty MessageID on wake+deliver", res)
	}

	specs := starter.snapshotSpecs()
	if len(specs) == 0 {
		t.Fatal("starter received zero specs; want at least one (Wake should call Start)")
	}
	wantInjection := fmt.Sprintf(agentpkg.WakePromptSendMessage, body)
	if specs[0].RestartInjection != wantInjection {
		t.Errorf("RestartInjection mismatch\n got: %q\nwant: %q", specs[0].RestartInjection, wantInjection)
	}

	// Message must be persisted (durable inbox entry).
	entries, err := agentloop.ListPending(tmpDir, "alice")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending entries = %d, want 1 (wake+deliver must persist)", len(entries))
	}
	if entries[0].Body != body {
		t.Errorf("persisted body = %q, want %q", entries[0].Body, body)
	}
}

// TestDelegate_OfflineTarget_NoFlag_ReturnsCanonicalError mirrors the
// SendMessage table for Delegate: paused/killed/died/faulted +
// wake_if_offline=false ⇒ canonical error.
func TestDelegate_OfflineTarget_NoFlag_ReturnsCanonicalError(t *testing.T) {
	for _, st := range offlineStatuses() {
		t.Run(st, func(t *testing.T) {
			r, tmpDir := newFakeReal(t)
			saveTestAgent(t, tmpDir, &state.AgentState{
				Name:   "alice",
				Type:   "engineer",
				Family: "engineering",
				Parent: "weave",
				Status: st,
			})

			err := r.Delegate(context.Background(), "alice", "do X", false)
			if err == nil {
				t.Fatalf("Delegate to %s agent returned nil error; want canonical wake-not-permitted error", st)
			}
			want := fmt.Sprintf(canonicalOfflineError, "alice", st)
			if err.Error() != want {
				t.Errorf("error mismatch\n got: %q\nwant: %q", err.Error(), want)
			}
		})
	}
}

// TestDelegate_OfflineTarget_WakeIfOffline_HardRedirectInjected: paused
// recipient + wake_if_offline=true ⇒
//   - the starter sees RestartInjection ==
//     BuildWakePrompt(WakeReasonDelegate, "paused", "do X"),
//   - the task is enqueued via the existing task path.
func TestDelegate_OfflineTarget_WakeIfOffline_HardRedirectInjected(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Status = state.StatusPaused
	saveTestAgent(t, tmpDir, agentState)

	starter := &wakeCapturingStarter{}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, starter)

	task := "do X"
	if err := r.Delegate(context.Background(), "alice", task, true); err != nil {
		t.Fatalf("Delegate(wake_if_offline=true) on paused agent: %v", err)
	}

	specs := starter.snapshotSpecs()
	if len(specs) == 0 {
		t.Fatal("starter received zero specs; want at least one")
	}
	wantInjection := fmt.Sprintf(agentpkg.WakePromptDelegate, "paused", task)
	if specs[0].RestartInjection != wantInjection {
		t.Errorf("RestartInjection mismatch\n got: %q\nwant: %q", specs[0].RestartInjection, wantInjection)
	}
	// Task must still hit the standard task queue.
	if got := rt.Snapshot().QueueDepth; got < 1 {
		t.Errorf("QueueDepth = %d, want >= 1 (wake-with-delegate must enqueue the task)", got)
	}
}

// TestReal_Wake_BareReason_BuildsBareTemplate pins the bare-wake path: when
// Real.Wake is called with WakeReasonBare + empty body, the runtime starter
// observes RestartInjection == fmt.Sprintf(WakePromptBare, previousState).
func TestReal_Wake_BareReason_BuildsBareTemplate(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Status = state.StatusPaused
	saveTestAgent(t, tmpDir, agentState)

	starter := &wakeCapturingStarter{}
	_ = ensureRuntimeWithStarter(t, r, tmpDir, agentState, starter)

	res, err := r.Wake(context.Background(), "alice", agentpkg.WakeReasonBare, "")
	if err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if res == nil {
		t.Fatal("Wake returned nil WakeResult on success")
	}
	specs := starter.snapshotSpecs()
	if len(specs) == 0 {
		t.Fatal("starter received zero specs")
	}
	want := fmt.Sprintf(agentpkg.WakePromptBare, "paused")
	if specs[0].RestartInjection != want {
		t.Errorf("RestartInjection mismatch\n got: %q\nwant: %q", specs[0].RestartInjection, want)
	}
}

// TestSendMessage_Died_NoLiveAncestor_WakeIfOffline_WakesOriginal pins the
// deadFallback wake path (real.go:1617-1641). When the recipient is Died and
// no live ancestor exists (here: parent `weave` is also Died → WalkDeadAncestors
// returns walkErr "reached root with no live ancestor"), the offline gate
// fires. With wake_if_offline=true the original recipient is woken — NOT the
// dead chain top — and the body is persisted against the original recipient.
func TestSendMessage_Died_NoLiveAncestor_WakeIfOffline_WakesOriginal(t *testing.T) {
	r, tmpDir := newFakeReal(t)

	// Parent first — saved as Died so WalkDeadAncestors cannot resolve a
	// live ancestor and walkErr fires, flipping deadFallback=true.
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "weave",
		Type:   "weave",
		Family: "weave",
		Parent: "",
		Status: state.StatusDied,
	})

	agentState := testAgentState("alice")
	agentState.Status = state.StatusDied
	saveTestAgent(t, tmpDir, agentState)

	starter := &wakeCapturingStarter{}
	_ = ensureRuntimeWithStarter(t, r, tmpDir, agentState, starter)

	body := "hello"
	res, err := r.SendMessage(context.Background(), "alice", body, false, true)
	if err != nil {
		t.Fatalf("SendMessage with wake_if_offline=true on Died+no-live-ancestor: %v", err)
	}
	if res == nil || res.MessageID == "" {
		t.Fatalf("SendMessage result = %+v, want non-empty MessageID on deadFallback wake", res)
	}

	specs := starter.snapshotSpecs()
	if len(specs) == 0 {
		t.Fatal("starter received zero specs; want at least one (deadFallback must Wake the original recipient)")
	}
	wantInjection := fmt.Sprintf(agentpkg.WakePromptSendMessage, body)
	if specs[0].RestartInjection != wantInjection {
		t.Errorf("RestartInjection mismatch\n got: %q\nwant: %q", specs[0].RestartInjection, wantInjection)
	}

	// The body must be persisted to the ORIGINAL recipient (alice) — not
	// routed up to weave. real.go:1637 explicitly resets to = originalTo
	// after the wake fallback fires.
	entries, err := agentloop.ListPending(tmpDir, "alice")
	if err != nil {
		t.Fatalf("ListPending(alice): %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending entries for alice = %d, want 1 (deadFallback delivers to original recipient)", len(entries))
	}
	if entries[0].Body != body {
		t.Errorf("persisted body = %q, want %q", entries[0].Body, body)
	}
}

// TestSendMessage_RunningTarget_WakeIfOffline_NoWakeFired pins that the
// offline gate is a strict guard — when the recipient is Running, passing
// wake_if_offline=true must NOT trigger a wake spec on the starter. The
// message is enqueued through the normal path.
func TestSendMessage_RunningTarget_WakeIfOffline_NoWakeFired(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice") // Status defaults to "active"
	saveTestAgent(t, tmpDir, agentState)

	starter := &wakeCapturingStarter{}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, starter)
	if err := rt.Start(); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}
	// Drain the Start call from the offline-gate negative assertion below:
	// rt.Start above intentionally hits the starter once; the negative
	// assertion checks for NO ADDITIONAL specs after SendMessage.
	preSendSpecCount := len(starter.snapshotSpecs())

	body := "hello"
	res, err := r.SendMessage(context.Background(), "alice", body, false, true)
	if err != nil {
		t.Fatalf("SendMessage on Running recipient with wake_if_offline=true: %v", err)
	}
	if res == nil || res.MessageID == "" {
		t.Fatalf("SendMessage result = %+v, want non-empty MessageID on normal-path send", res)
	}

	postSpecs := starter.snapshotSpecs()
	if len(postSpecs) != preSendSpecCount {
		t.Errorf("starter spec count = %d, want %d (offline gate must NOT fire for Running recipient)", len(postSpecs), preSendSpecCount)
	}

	entries, err := agentloop.ListPending(tmpDir, "alice")
	if err != nil {
		t.Fatalf("ListPending(alice): %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("pending entries = %d, want 1", len(entries))
	}
	if entries[0].Body != body {
		t.Errorf("persisted body = %q, want %q", entries[0].Body, body)
	}
}
