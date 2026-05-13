// QUM-559: failing (RED) tests for the in-process ephemeral
// per-recipient status-notification ring. After this change
// `mcp__sprawl__report_status` no longer writes the reporter's status to the
// parent's maildir / harness queue. Instead it:
//
//   1. Mutates the reporter's state.LastReport*
//   2. Pushes a pre-rendered `<system-notification>` line onto the parent's
//      per-recipient FIFO ring (statusNotifier)
//   3. Calls parentRuntime.WakeForDelivery() (cooperative)
//
// The TUI drains the ring alongside the on-disk queue via
// Supervisor.DrainStatusNotifications.

package supervisor

import (
	"context"
	"fmt"
	"sync"
	"testing"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
)

// NOTE on wake-not-preempt: the cooperative-wake contract for the new
// per-recipient ring path is already pinned by
// TestRealReportStatus_DoesNotInterruptParentSession in
// real_runtime_test.go (around line 472): it asserts
// session.WakeForDelivery >= 1, session.ForceInterruptDelivery == 0, and
// session.Interrupt == 0 after r.ReportStatus. We do not duplicate that
// instrumentation here; if the new ring path were to forget to wake the
// parent runtime, that existing test would fail.

// TestReal_ReportStatus_PersistsLastReport_NoMaildirNoUnread is the headline
// regression test: after report_status the reporter's state.LastReport* is
// persisted, but the parent's maildir is untouched (zero entries in new/cur/
// archive) and zero unread. The pre-rendered notification line is on the
// in-process ring.
func TestReal_ReportStatus_PersistsLastReport_NoMaildirNoUnread(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "weave", Type: "manager", Family: "engineering", Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "child", Type: "engineer", Family: "engineering",
		Parent: "weave", Status: "active",
	})

	if _, err := r.ReportStatus(context.Background(), "child", "working", "doing X"); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}

	// (1) reporter state persisted.
	st, err := state.LoadAgent(tmpDir, "child")
	if err != nil {
		t.Fatalf("LoadAgent(child): %v", err)
	}
	if st.LastReportState != "working" {
		t.Errorf("LastReportState = %q, want %q", st.LastReportState, "working")
	}
	if st.LastReportMessage != "doing X" {
		t.Errorf("LastReportMessage = %q, want %q", st.LastReportMessage, "doing X")
	}
	if st.LastReportAt == "" {
		t.Error("LastReportAt should be non-empty after ReportStatus")
	}

	// (2) maildir for parent is EMPTY in every filter.
	for _, filter := range []string{"all", "unread", "read", "archived"} {
		msgs, err := messages.List(tmpDir, "weave", filter)
		if err != nil {
			t.Fatalf("messages.List(weave, %q): %v", filter, err)
		}
		if len(msgs) != 0 {
			t.Errorf("messages.List(weave, %q) returned %d entries, want 0 (QUM-559: report_status must not write to maildir)", filter, len(msgs))
		}
	}

	// (3) MessagesPeek as weave returns UnreadCount=0.
	parentCtx := backendpkg.WithCallerIdentity(context.Background(), "weave")
	peek, err := r.MessagesPeek(parentCtx)
	if err != nil {
		t.Fatalf("MessagesPeek: %v", err)
	}
	if peek.UnreadCount != 0 {
		t.Errorf("MessagesPeek.UnreadCount = %d, want 0 (no maildir write)", peek.UnreadCount)
	}

	// (4) DrainStatusNotifications("weave") returns the pre-rendered line.
	drained := r.DrainStatusNotifications("weave")
	want := "<system-notification type=\"status_change\">child changed status to working: doing X</system-notification>\n"
	if len(drained) != 1 || drained[0] != want {
		t.Errorf("DrainStatusNotifications(weave) = %#v, want exactly [%q]", drained, want)
	}
}

// TestReal_ReportStatus_DrainIsFIFO_AndClears: three sequential ReportStatus
// calls; one Drain returns three lines in FIFO order; second Drain returns
// empty (ring cleared on drain).
func TestReal_ReportStatus_DrainIsFIFO_AndClears(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "weave", Status: "active"})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "child", Parent: "weave", Status: "active",
	})

	for _, summary := range []string{"A", "B", "C"} {
		if _, err := r.ReportStatus(context.Background(), "child", "working", summary); err != nil {
			t.Fatalf("ReportStatus(%q): %v", summary, err)
		}
	}

	drained := r.DrainStatusNotifications("weave")
	if len(drained) != 3 {
		t.Fatalf("first drain len = %d, want 3 (got %#v)", len(drained), drained)
	}
	for i, summary := range []string{"A", "B", "C"} {
		want := "<system-notification type=\"status_change\">child changed status to working: " + summary + "</system-notification>\n"
		if drained[i] != want {
			t.Errorf("drained[%d] = %q, want %q", i, drained[i], want)
		}
	}

	// Second drain — ring cleared.
	second := r.DrainStatusNotifications("weave")
	if len(second) != 0 {
		t.Errorf("second drain should be empty, got %#v", second)
	}
}

// TestReal_ReportStatus_StatusFlipsForCompleteFailure: complete→done,
// failure→problem (regression guard; behavior carried over from QUM-295).
func TestReal_ReportStatus_StatusFlipsForCompleteFailure(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "weave", Status: "active"})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "child", Parent: "weave", Status: "active",
	})

	if _, err := r.ReportStatus(context.Background(), "child", "complete", "ok"); err != nil {
		t.Fatalf("ReportStatus(complete): %v", err)
	}
	st, _ := state.LoadAgent(tmpDir, "child")
	if st.Status != "done" {
		t.Errorf("complete: Status = %q, want done", st.Status)
	}

	// reset to active for the failure check
	st.Status = "active"
	if err := state.SaveAgent(tmpDir, st); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	if _, err := r.ReportStatus(context.Background(), "child", "failure", "oops"); err != nil {
		t.Fatalf("ReportStatus(failure): %v", err)
	}
	st, _ = state.LoadAgent(tmpDir, "child")
	if st.Status != "problem" {
		t.Errorf("failure: Status = %q, want problem", st.Status)
	}
}

// TestReal_ReportStatus_NoParent_NoRingPush: orphan reporter — state still
// persists, but no ring push anywhere. Drain on any name returns empty.
func TestReal_ReportStatus_NoParent_NoRingPush(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "solo", Parent: "", Status: "active",
	})

	if _, err := r.ReportStatus(context.Background(), "solo", "complete", "done"); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}
	st, _ := state.LoadAgent(tmpDir, "solo")
	if st.Status != "done" {
		t.Errorf("Status = %q, want done", st.Status)
	}

	for _, name := range []string{"solo", "weave", ""} {
		if got := r.DrainStatusNotifications(name); len(got) != 0 {
			t.Errorf("DrainStatusNotifications(%q) for orphan reporter = %#v, want empty", name, got)
		}
	}
}

// TestReal_ReportStatus_InvalidState_NoMutation: an invalid state returns an
// error; on-disk state is unchanged; ring is empty.
func TestReal_ReportStatus_InvalidState_NoMutation(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "weave", Status: "active"})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "child", Parent: "weave", Status: "active",
	})

	_, err := r.ReportStatus(context.Background(), "child", "garbage", "x")
	if err == nil {
		t.Fatal("expected error for invalid state")
	}

	st, _ := state.LoadAgent(tmpDir, "child")
	if st.LastReportState != "" {
		t.Errorf("LastReportState = %q, want empty (no mutation on invalid state)", st.LastReportState)
	}
	if st.LastReportMessage != "" {
		t.Errorf("LastReportMessage = %q, want empty", st.LastReportMessage)
	}
	if got := r.DrainStatusNotifications("weave"); len(got) != 0 {
		t.Errorf("ring not empty after invalid state: %#v", got)
	}
}

// TestReal_ReportStatus_ConcurrentSafe: N goroutines all push concurrently;
// total drained equals N. Guards the mutex on the per-recipient ring.
func TestReal_ReportStatus_ConcurrentSafe(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "weave", Status: "active"})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "child", Parent: "weave", Status: "active",
	})

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			summary := fmt.Sprintf("s%d", i)
			if _, err := r.ReportStatus(context.Background(), "child", "working", summary); err != nil {
				t.Errorf("ReportStatus(%q): %v", summary, err)
			}
		}()
	}
	wg.Wait()

	drained := r.DrainStatusNotifications("weave")
	if len(drained) != N {
		t.Errorf("concurrent drain count = %d, want %d (lost or duplicated under contention)", len(drained), N)
	}
}

// TestReal_ReportStatus_CrossRecipientFIFOIsolation: two parent/child pairs
// reporting interleaved. Each parent's ring must hold only its own child's
// lines, in submission order — no cross-contamination across recipients.
func TestReal_ReportStatus_CrossRecipientFIFOIsolation(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "parentA", Status: "active"})
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "parentB", Status: "active"})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "childA", Parent: "parentA", Status: "active",
	})
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "childB", Parent: "parentB", Status: "active",
	})

	// Interleave: childA -> a1, childB -> b1, childA -> a2.
	if _, err := r.ReportStatus(context.Background(), "childA", "working", "a1"); err != nil {
		t.Fatalf("ReportStatus(childA, a1): %v", err)
	}
	if _, err := r.ReportStatus(context.Background(), "childB", "working", "b1"); err != nil {
		t.Fatalf("ReportStatus(childB, b1): %v", err)
	}
	if _, err := r.ReportStatus(context.Background(), "childA", "working", "a2"); err != nil {
		t.Fatalf("ReportStatus(childA, a2): %v", err)
	}

	drainedA := r.DrainStatusNotifications("parentA")
	wantA := []string{
		"<system-notification type=\"status_change\">childA changed status to working: a1</system-notification>\n",
		"<system-notification type=\"status_change\">childA changed status to working: a2</system-notification>\n",
	}
	if len(drainedA) != len(wantA) {
		t.Fatalf("parentA drain len = %d, want %d (got %#v)", len(drainedA), len(wantA), drainedA)
	}
	for i, want := range wantA {
		if drainedA[i] != want {
			t.Errorf("parentA[%d] = %q, want %q", i, drainedA[i], want)
		}
	}

	drainedB := r.DrainStatusNotifications("parentB")
	wantB := []string{
		"<system-notification type=\"status_change\">childB changed status to working: b1</system-notification>\n",
	}
	if len(drainedB) != len(wantB) {
		t.Fatalf("parentB drain len = %d, want %d (got %#v)", len(drainedB), len(wantB), drainedB)
	}
	for i, want := range wantB {
		if drainedB[i] != want {
			t.Errorf("parentB[%d] = %q, want %q", i, drainedB[i], want)
		}
	}
}
