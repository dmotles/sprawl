package backend

import (
	"context"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-867: the live /compact wire order (verified against Claude Code 2.1.198,
// confirmed by QA sentry) emits the two `system`/`subtype:"status"` compaction
// frames BEFORE `system/init`:
//
//	user (/compact submit)
//	system/session_state_changed (running)
//	system/status  status=compacting        <-- in-progress label
//	system/status  compact_result=failed     <-- failure toast
//	system/init                               <-- turn context allocated HERE
//	assistant / result
//
// Before the fix, runReader dropped the two status frames as stray between-turn
// telemetry (currentTurn==nil, failing the tf!=nil||preInitTrigger||replayEcho
// gate), so they never reached the frame router → MapProtocolMessage →
// CompactFailedMsg/CompactingStatusMsg was dead on the manual /compact path.
// These integration tests exercise the REAL runReader routing gate (the unit
// tests that call MapProtocolMessage directly bypass it).

// findRouted returns the first routed frame matching pred, or nil.
func findRouted(frames []routedFrame, pred func(routedFrame) bool) *routedFrame {
	for i := range frames {
		if pred(frames[i]) {
			return &frames[i]
		}
	}
	return nil
}

// TestSession_FrameRouter_PreInitCompactStatus_Routed proves the pre-init
// compaction status frames (status:"compacting" and compact_result:"failed")
// are routed to the frame router even though they arrive before system/init,
// mirroring the task_notification preInitTrigger carve-out. They must carry
// PreInit=true (publish-only; no turn lifecycle opened) and must NOT open a turn
// (QUM-570/QUM-815 — no premature InTurn leak).
func TestSession_FrameRouter_PreInitCompactStatus_Routed(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	rec := &frameRouterRecorder{}
	installFrameRouter(t, session, rec.handler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Faithful pre-init sequence: a stray state-change + a non-replay user submit
	// (both observer-only), then the two compaction status frames.
	transport.feedMessage(t, `{"type":"system","subtype":"session_state_changed","state":"running","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"system","subtype":"status","status":"compacting","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"Not enough messages to compact.","session_id":"sess-1"}`)

	// Both status frames must reach the router pre-init.
	rec.waitForCount(t, 2, 3*time.Second)

	// Pre-init: the status frames must NOT have opened a turn.
	if session.InTurn() {
		t.Error("a pre-init compaction status frame opened a turn (InTurn=true), want false")
	}

	frames := rec.snapshot()
	compacting := findRouted(frames, func(f routedFrame) bool {
		return f.msg.Type == "system" && f.msg.Subtype == "status" && f.msg.Raw != nil &&
			containsStatus(t, f.msg, "compacting", "")
	})
	if compacting == nil {
		t.Fatal("compacting status frame was NOT routed (dropped before reaching the frame router)")
	}
	if !compacting.turn.PreInit {
		t.Error("compacting status frame routed with PreInit=false; want true (publish-only, no turn lifecycle)")
	}
	if !compacting.turn.Autonomous {
		t.Error("compacting status frame routed with Autonomous=false; want true")
	}

	failed := findRouted(frames, func(f routedFrame) bool {
		return f.msg.Type == "system" && f.msg.Subtype == "status" && f.msg.Raw != nil &&
			containsStatus(t, f.msg, "", "failed")
	})
	if failed == nil {
		t.Fatal("compact_result:failed status frame was NOT routed (dropped before reaching the frame router)")
	}
	if !failed.turn.PreInit {
		t.Error("failed status frame routed with PreInit=false; want true")
	}

	// The stray session_state_changed frame must still be observer-only.
	if stray := findRouted(frames, func(f routedFrame) bool {
		return f.msg.Subtype == "session_state_changed"
	}); stray != nil {
		t.Error("session_state_changed was routed; it must remain observer-only")
	}

	// The subsequent init opens the turn and the result closes it — normal path.
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	rec.waitForCount(t, 4, 3*time.Second)
}

// TestSession_FrameRouter_PreInitUnrelatedStatusNotRouted proves the carve-out
// is narrow: a pre-init system/status frame that is neither compacting nor a
// compact_result is NOT routed (stays observer-only), so the gate is not
// broadened beyond the compaction frames (QUM-867).
func TestSession_FrameRouter_PreInitUnrelatedStatusNotRouted(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	rec := &frameRouterRecorder{}
	installFrameRouter(t, session, rec.handler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// A status frame carrying neither compact_result nor status:"compacting".
	transport.feedMessage(t, `{"type":"system","subtype":"status","status":"thinking","session_id":"sess-1"}`)
	time.Sleep(200 * time.Millisecond)
	if n := rec.count(); n != 0 {
		t.Fatalf("router was invoked %d times for an unrelated pre-init status frame, want 0", n)
	}
	if session.InTurn() {
		t.Error("unrelated status frame opened a turn (InTurn=true), want false")
	}
}

// containsStatus reports whether msg's CompactStatus has the given status and/or
// compact_result (empty string = don't care for that field).
func containsStatus(t *testing.T, msg *protocol.Message, wantStatus, wantResult string) bool {
	t.Helper()
	var cs protocol.CompactStatus
	if protocol.ParseAs(msg, &cs) != nil {
		return false
	}
	if wantStatus != "" && cs.Status != wantStatus {
		return false
	}
	if wantResult != "" && cs.CompactResult != wantResult {
		return false
	}
	return true
}
