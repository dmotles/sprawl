// Bounds on the kind:system stdin frame (QUM-730 × QUM-925 incident).
//
// A restart of the root weave injected ONE 38,673-byte stdin frame containing 123
// byte-identical copies of the liveness-check notification, destroying the
// session's context. Every layer above WriteSystemMessage concatenates whatever
// its drain happened to find, so frame size was a function of how long a consumer
// had been broken — unbounded by construction.
//
// These assertions are written from the INCIDENT, not from the mechanism: they
// say nothing about liveness checks, the maildir, or the heartbeat. They hold for
// any channel that ever accretes, which is the point — the defence must not depend
// on predicting which one breaks next.

package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// theIncidentBody is the verbatim liveness-check line whose 123-fold repetition
// was the incident. Used here only as a realistic repeated payload; nothing in
// these tests is liveness-specific.
const theIncidentBody = `<system-notification type="liveness_check">This is an automated liveness check from the sprawl system. If there's no work to do just ignore this message. If you're still waiting on something or you were in the middle of something, please continue your work.</system-notification>` + "\n"

// TestWriteSystemMessage_IdenticalLinesCollapse is the test that would have caught
// the incident. 123 identical lines carry exactly as much instruction as one.
func TestWriteSystemMessage_IdenticalLinesCollapse(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", IsRoot: true, Session: mock})

	frame := strings.Repeat(theIncidentBody, 123)
	if _, err := rt.WriteSystemMessage(context.Background(), frame, "next", nil); err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.writes) != 1 {
		t.Fatalf("got %d stdin writes, want exactly 1", len(mock.writes))
	}
	got := mock.writes[0].Message.Content

	// Positive control: the payload must still be present. Without this leg a
	// mechanism that dropped the frame entirely would satisfy the count below.
	if !strings.Contains(got, "automated liveness check") {
		t.Fatalf("the notification body is absent from the written frame entirely; "+
			"a collapse that drops the payload is worse than the flood it replaces (got %q)", got)
	}
	if n := strings.Count(got, "automated liveness check"); n != 1 {
		t.Errorf("the written stdin frame carries the identical notification %d times, want exactly 1 — "+
			"this is the 38,673-byte context-destroying frame the incident was made of", n)
	}
}

// TestWriteSystemMessage_BoundedBytes pins the byte-level property the incident
// actually was, independent of whether the lines are identical. Distinct lines
// cannot be collapsed, so this is the only thing standing between a broken
// consumer and the model's context window.
func TestWriteSystemMessage_BoundedBytes(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", IsRoot: true, Session: mock})

	// Distinct lines — the status_change shape, which carries real per-agent
	// information and therefore cannot be deduped away.
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&b, "<system-notification type=\"status_change\">agent-%04d changed status to "+
			"working: doing a thing</system-notification>\n", i)
	}
	oversized := b.String()
	// Guard the fixture itself: if these lines were accidentally identical the
	// dedup pass would collapse them and truncation would never be exercised —
	// this test would then pass while measuring nothing. (It did, once.)
	if n := strings.Count(oversized, "agent-0001 "); n != 1 {
		t.Fatalf("fixture lines are not distinct (%d copies of one line) — dedup would satisfy "+
			"this test without truncation ever running", n)
	}
	if len(oversized) < 4*maxSystemFrameBytes {
		t.Fatalf("fixture is only %d bytes, needs to comfortably exceed the %d-byte cap "+
			"or this test cannot exercise truncation", len(oversized), maxSystemFrameBytes)
	}

	if _, err := rt.WriteSystemMessage(context.Background(), oversized, "next", nil); err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.writes) != 1 {
		t.Fatalf("got %d stdin writes, want exactly 1", len(mock.writes))
	}
	got := mock.writes[0].Message.Content
	if len(got) > maxSystemFrameBytes {
		t.Errorf("wrote a %d-byte system frame, cap is %d — frame size is still a function of "+
			"how long a consumer was broken", len(got), maxSystemFrameBytes)
	}
	// Truncation must announce itself; a silently shortened frame is a delivery
	// failure the recipient cannot see.
	if !strings.Contains(got, systemFrameTruncationMarker) {
		t.Errorf("frame was truncated with no marker — the recipient cannot tell it is reading a "+
			"partial batch (want %q in the frame)", systemFrameTruncationMarker)
	}
}

// TestWriteSystemMessage_SmallFrameUntouched is the negative control for both
// assertions above: an ordinary frame must pass through byte-identical, so
// neither the collapse nor the cap can be satisfied by mangling everything.
func TestWriteSystemMessage_SmallFrameUntouched(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", IsRoot: true, Session: mock})

	frame := "<system-notification type=\"message\">From alice — mcp__sprawl__messages_read(id=k3f)</system-notification>\n" +
		"<system-notification type=\"message\">From bob — mcp__sprawl__messages_read(id=m9q)</system-notification>\n"
	if _, err := rt.WriteSystemMessage(context.Background(), frame, "next", nil); err != nil {
		t.Fatalf("WriteSystemMessage: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if got := mock.writes[0].Message.Content; got != frame {
		t.Errorf("an ordinary two-line frame was altered in transit:\n got %q\nwant %q", got, frame)
	}
}

// TestWriteUserPrompt_NotDeduped pins the scope of the collapse as a deliberate
// non-change: a human typing the same prompt twice must reach the CLI twice. The
// bound belongs to the supervisor-originated system channel only.
func TestWriteUserPrompt_NotDeduped(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave", IsRoot: true, Session: mock})

	dup := "run the tests\nrun the tests\n"
	if _, err := rt.WriteUserPrompt(context.Background(), dup, "next"); err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if got := mock.writes[0].Message.Content; got != dup {
		t.Errorf("a kind:user prompt was deduped; only kind:system is collapsed:\n got %q\nwant %q", got, dup)
	}
}

// TestLivenessNudge_ManyArmsOneConsume isolates the QUM-730 flag from the
// frame-level dedup above.
//
// This exists because the handle-level equivalent
// (TestWeaveRuntimeHandle_LivenessNudge_ManyArmsRenderOneLine) is satisfied by
// EITHER defence: mutating the nudge back into an accumulating counter left it
// green, because boundSystemFrame deduped the resulting 123 identical lines. That
// is defence in depth working as intended, but it means the handle test does not
// constrain the flag. This one does — it never reaches a frame.
func TestLivenessNudge_ManyArmsOneConsume(t *testing.T) {
	rt := New(RuntimeConfig{Name: "alice", Session: &mockUnifiedSession{cancelResults: map[string]bool{}}})

	for i := 0; i < 123; i++ {
		rt.RequestLivenessNudge()
	}

	consumed := 0
	for i := 0; i < 200; i++ {
		if rt.ConsumeLivenessNudge() {
			consumed++
		}
	}
	if consumed != 1 {
		t.Errorf("123 arms yielded %d consumes, want exactly 1 — the nudge is accumulating, which is "+
			"the durable-envelope defect the flag replaced", consumed)
	}
}

// TestLivenessNudge_NotArmed_NeverConsumes is the negative control: an unarmed
// runtime must never report a pending nudge, so the "exactly 1" above cannot be
// satisfied by a consume that always returns true on its first call.
func TestLivenessNudge_NotArmed_NeverConsumes(t *testing.T) {
	rt := New(RuntimeConfig{Name: "alice", Session: &mockUnifiedSession{cancelResults: map[string]bool{}}})
	for i := 0; i < 10; i++ {
		if rt.ConsumeLivenessNudge() {
			t.Fatalf("ConsumeLivenessNudge reported a pending nudge on an unarmed runtime (call %d)", i)
		}
	}
}
