// QUM-1197 item 1: the reaper's REFUSAL record must be observable in a shipped
// binary.
//
// Direction of every probe in this file: the subject is the LEVEL of the
// not-reclaimed record, not the reap decision (idlereap_race_test.go owns
// that). slog's default level is Info, so a Debug refusal record is not
// "quieter" than an Info one — it is never written at all; and because nothing
// in this repo configures a level outside cmd/hubd/main.go, an operator has no
// way to turn it on. Those are two separate facts and both are load-bearing.
// These tests are the first thing in the tree that pins the level of any of
// the four idle-reclaim messages.
//
// Controls, each named with its direction:
//
//   - POSITIVE — a refusal IS happening, so the Info probe MUST fire naming the
//     blocker: TestReclaim_FirstRefusalIsLoggedAtInfo, and the disabled /
//     abandoned / per-agent / post-reap arms below.
//   - BOUND (not a negative control: it asserts want=1, not want=0) — repeats of
//     the SAME refusal must not each reach Info, and must still be recorded at
//     Debug: TestReclaim_RepeatedIdenticalRefusalIsDemotedToDebug. Its
//     load-bearing direction is OVER-firing, which a red-first run cannot show;
//     it is demonstrated by the always-Info mutation recorded on QUM-1197.
//   - NEGATIVE — a subject with nothing to refuse, where the refusal probe MUST
//     stay silent: TestReclaim_ReapedAgent_LogsNoRefusalRecord. It asserts with
//     the same refusalRecordsAt instrument the positive arms use, so the
//     instrument is known to be able to fire.
//
// No t.Parallel() anywhere here: installCaptureSlog mutates the process-global
// slog.Default(), so these tests depend on the sequential-test guarantee, the
// same way the two existing capture users in this package do. Install it AFTER
// newReclaimFixture so its t.Cleanup restores the default logger before the
// fixture's Shutdown runs and logs — do not invert that ordering.
package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/config"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
)

const (
	refusalMsg  = "idle reclaim: agent not reclaimed"
	reapMsg     = "idle reclaim: reaping agent"
	disabledMsg = "idle reclaim: disabled"
	abandonMsg  = "idle reclaim: teardown abandoned"
)

// refusalRecordsAt is the level-aware filter; captureSlogHandler.recordsAtLevel
// lives next to recordsWithMessage so the two cannot drift.
func refusalRecordsAt(t *testing.T, h *captureSlogHandler, level slog.Level, sub string) []string {
	t.Helper()
	return h.recordsAtLevel(level, sub)
}

// addReclaimAgent registers a SECOND observable agent on an existing Real, idle
// on every predicate term. Mirrors newReclaimFixture's body; it exists because
// the dedup state is per-agent and a one-agent fixture cannot tell a per-agent
// implementation from an agent-agnostic one.
func addReclaimAgent(t *testing.T, r *Real, tmpDir, name string) (*AgentRuntime, *reclaimTestHandle) {
	t.Helper()
	agentState := testAgentState(name)
	saveTestAgent(t, tmpDir, agentState)
	handle := &reclaimTestHandle{
		runtimeTestSession: &runtimeTestSession{
			sessionID: "sess-" + name,
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
	}
	handle.urt = runtimepkg.New(runtimepkg.RuntimeConfig{Name: name, Session: handle.runtimeTestSession})
	handle.lastAct.Store(time.Now().Add(-2 * config.SuggestedIdleReclaimAfter).UnixNano())
	feedBackgroundTasks(t, handle.runtimeTestSession)
	handle.starter = &runtimeTestStarter{session: handle}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, handle.starter)
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start for %s: %v", name, err)
	}
	return rt, handle
}

// TestReclaim_FirstRefusalIsLoggedAtInfo is the POSITIVE control: the subject
// is refusing (in_turn is busy), so the probe MUST fire. It also asserts the
// record's content, so a promotion that reduced the record to a bare "not
// reclaimed" — answering "was it assessed?" but not "which term blocked?" —
// still fails.
func TestReclaim_FirstRefusalIsLoggedAtInfo(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)
	handle.inTurn.Store(true)

	h := installCaptureSlog(t)
	r.maybeReclaimIdle(context.Background(), rt)

	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d for an in-turn agent, want 0; this file must not be measuring the log of a teardown it caused", got)
	}
	got := refusalRecordsAt(t, h, slog.LevelInfo, refusalMsg)
	if len(got) != 1 {
		t.Fatalf("INFO-level %q records = %d, want 1; a refusal that only logs at Debug leaves NO record in a shipped binary. all records:\n%s",
			refusalMsg, len(got), h.String())
	}
	for _, want := range []string{"agent=alice", "blocker=in_turn ", "in_turn=busy", "reap=false"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("INFO refusal record missing %q; a reader cannot answer which term blocked the reap. record: %s", want, got[0])
		}
	}
}

// TestReclaim_RepeatedIdenticalRefusalIsDemotedToDebug is the BOUND, and the
// flood cost measured rather than reasoned: Info records across 5 sweeps of one
// steady-state agent, counted. Its load-bearing direction is over-firing, shown
// by the always-Info mutation, not by the red-first run. The Debug count is
// asserted alongside because without it, deleting the log call entirely would
// satisfy "Info stays quiet".
func TestReclaim_RepeatedIdenticalRefusalIsDemotedToDebug(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)
	handle.inTurn.Store(true)

	h := installCaptureSlog(t)
	const sweeps = 5
	for i := 0; i < sweeps; i++ {
		r.maybeReclaimIdle(context.Background(), rt)
	}

	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d for an in-turn agent, want 0", got)
	}
	if got := refusalRecordsAt(t, h, slog.LevelInfo, refusalMsg); len(got) != 1 {
		t.Errorf("INFO-level %q records = %d across %d sweeps of ONE steady-state agent, want 1; the reaper sweeps every few seconds, so a per-sweep Info line is a log flood. records:\n%s",
			refusalMsg, len(got), sweeps, strings.Join(got, "\n"))
	}
	if got := refusalRecordsAt(t, h, slog.LevelDebug, refusalMsg); len(got) != sweeps-1 {
		t.Errorf("DEBUG-level %q records = %d across %d sweeps, want %d; the demoted repeats must still be recorded, or the dedup is deleting the record rather than lowering its level",
			refusalMsg, len(got), sweeps, sweeps-1)
	}
}

// TestReclaim_BlockerChangeReLogsAtInfo pins that the dedup key is the DECIDING
// term — a changed reason is news — and in the same breath measures the WORST
// case honestly: an agent whose blocker alternates every sweep costs one Info
// per sweep, because change-keyed dedup gives a flapping agent no protection at
// all. That number is asserted here rather than reassured about in a comment.
func TestReclaim_BlockerChangeReLogsAtInfo(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)
	idleAt := handle.lastAct.Load()

	h := installCaptureSlog(t)
	const flaps = 4
	for i := 0; i < flaps; i++ {
		if i%2 == 0 {
			// Blocker: in_turn (quiescent stays idle).
			handle.inTurn.Store(true)
			handle.lastAct.Store(idleAt)
		} else {
			// Blocker: quiescent (in_turn is idle, activity is recent).
			handle.inTurn.Store(false)
			handle.lastAct.Store(time.Now().UnixNano())
		}
		r.maybeReclaimIdle(context.Background(), rt)
	}

	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d, want 0; every sweep here refuses", got)
	}
	got := refusalRecordsAt(t, h, slog.LevelInfo, refusalMsg)
	if len(got) != flaps {
		t.Fatalf("INFO-level %q records = %d across %d ALTERNATING sweeps, want %d (one per change of blocker). records:\n%s",
			refusalMsg, len(got), flaps, flaps, strings.Join(got, "\n"))
	}
	// The trailing space and the companion term are both deliberate: without
	// them "blocker=quiescent" also matches "blocker=quiescent_unobservable",
	// which is a DIFFERENT dedup key and a different observation (D1a).
	for i, rec := range got {
		wantBlocker, wantTerm := "blocker=in_turn ", "in_turn=busy"
		if i%2 == 1 {
			wantBlocker, wantTerm = "blocker=quiescent ", "quiescent=busy"
		}
		if !strings.Contains(rec, wantBlocker) || !strings.Contains(rec, wantTerm) {
			t.Errorf("INFO record %d: want %q and %q; got: %s", i, wantBlocker, wantTerm, rec)
		}
	}
}

// TestReclaim_ReapedAgent_LogsNoRefusalRecord is the NEGATIVE control, aimed at
// the refusal record with a subject that has nothing to refuse: the fixture is
// idle on every term, so the refusal probe MUST stay silent at both levels. The
// reap record is asserted FIRST so a sweep that never reached assessment reads
// as a setup failure rather than as silence.
func TestReclaim_ReapedAgent_LogsNoRefusalRecord(t *testing.T) {
	r, _, rt, _ := newReclaimFixture(t)

	h := installCaptureSlog(t)
	r.maybeReclaimIdle(context.Background(), rt)

	if got := refusalRecordsAt(t, h, slog.LevelInfo, reapMsg); len(got) != 1 {
		t.Fatalf("INFO-level %q records = %d, want 1; without a reap this test's silence proves nothing. all records:\n%s",
			reapMsg, len(got), h.String())
	}
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		if got := refusalRecordsAt(t, h, level, refusalMsg); len(got) != 0 {
			t.Errorf("%v-level %q records = %d for an agent that WAS reaped, want 0. records:\n%s",
				level, refusalMsg, len(got), strings.Join(got, "\n"))
		}
	}
}

// TestReclaim_RefusalDedupIsPerAgent is the wiring half of the per-agent claim.
// TestRefusalLevel_IsChangeKeyedAndPerAgent pins the helper; this pins that the
// call site passes the AGENT. Mutating the call site to a constant name leaves
// every single-agent test in this file green while swallowing the first refusal
// of every agent after the first — which is the whole defect being fixed.
func TestReclaim_RefusalDedupIsPerAgent(t *testing.T) {
	r, tmpDir, aliceRT, aliceHandle := newReclaimFixture(t)
	bobRT, bobHandle := addReclaimAgent(t, r, tmpDir, "bob")
	aliceHandle.inTurn.Store(true)
	bobHandle.inTurn.Store(true)

	h := installCaptureSlog(t)
	r.maybeReclaimIdle(context.Background(), aliceRT)
	r.maybeReclaimIdle(context.Background(), bobRT)

	got := refusalRecordsAt(t, h, slog.LevelInfo, refusalMsg)
	if len(got) != 2 {
		t.Fatalf("INFO-level %q records = %d for TWO agents refusing on the same blocker, want 2. records:\n%s",
			refusalMsg, len(got), strings.Join(got, "\n"))
	}
	if !strings.Contains(got[0], "agent=alice") || !strings.Contains(got[1], "agent=bob") {
		t.Errorf("INFO records name the wrong agents; want alice then bob. records:\n%s", strings.Join(got, "\n"))
	}
}

// TestReclaim_ReapForgetsTheRefusalMemory pins the CALL SITE of forgetRefusal.
// Without it, an agent that refused, then became reapable, and later refuses
// again for the same reason is demoted to Debug forever — invisible again.
// Nothing else in this file constrains
// when production forgets, so this asserts through the helper deliberately: the
// key string is the production one, and an implementation that never calls
// forgetRefusal returns Debug here.
func TestReclaim_ReapForgetsTheRefusalMemory(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)
	handle.inTurn.Store(true)

	h := installCaptureSlog(t)
	r.maybeReclaimIdle(context.Background(), rt) // refuses: blocker in_turn, logged at Info
	if got := refusalRecordsAt(t, h, slog.LevelInfo, refusalMsg); len(got) != 1 {
		t.Fatalf("precondition: INFO refusal records = %d, want 1", len(got))
	}

	handle.inTurn.Store(false)
	r.maybeReclaimIdle(context.Background(), rt) // reapable now
	if got := handle.stopCalls.Load(); got != 1 {
		t.Fatalf("precondition: stopCalls = %d after the agent became reapable, want 1", got)
	}

	if got := r.refusalLevel("alice", "assess:in_turn"); got != slog.LevelInfo {
		t.Errorf("refusal level after a reap = %v, want %v; the reap must forget why the agent used to refuse, or its next refusal is silent",
			got, slog.LevelInfo)
	}
}

// TestReclaim_DisabledRefusalIsLoggedAtInfoOnce pins the third refusal site.
// Read its direction narrowly: this arm is NOT what a default install exercises.
// real.go only constructs the reaper when the knob is > 0, so on a default
// install there is no sweep at all and the operator's record is NewReal's own
// "idle reclaim: DISABLED (default)" line. This site is reached by a direct call
// or a knob change. Same shape as the steady-state bound: one Info, the rest
// demoted.
func TestReclaim_DisabledRefusalIsLoggedAtInfoOnce(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)
	r.idleReclaimAfter.set(0)

	h := installCaptureSlog(t)
	const sweeps = 5
	for i := 0; i < sweeps; i++ {
		r.maybeReclaimIdle(context.Background(), rt)
	}

	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d with the reaper disabled, want 0", got)
	}
	if got := refusalRecordsAt(t, h, slog.LevelInfo, disabledMsg); len(got) != 1 {
		t.Errorf("INFO-level %q records = %d across %d sweeps, want 1. all records:\n%s",
			disabledMsg, len(got), sweeps, h.String())
	}
	if got := refusalRecordsAt(t, h, slog.LevelDebug, disabledMsg); len(got) != sweeps-1 {
		t.Errorf("DEBUG-level %q records = %d across %d sweeps, want %d",
			disabledMsg, len(got), sweeps, sweeps-1)
	}
}

// TestReclaim_AbandonedTeardownIsLoggedAtInfo covers the rarest and most
// interesting refusal in the file: the reaper decided to reap, waited, and then
// backed off because work arrived. At Debug it leaves nothing behind. The
// interleaving recipe is TestReclaim_AgentBecomesBusyDuringTheWait_IsAbandoned's;
// the assertion here is only about the record.
func TestReclaim_AbandonedTeardownIsLoggedAtInfo(t *testing.T) {
	r, tmpDir, rt, handle := newReclaimFixture(t)
	handle.flipInTurnAfter.Store(1)

	h := installCaptureSlog(t)
	done := make(chan struct{})
	go func() { defer close(done); r.maybeReclaimIdle(context.Background(), rt) }()

	select {
	case <-done:
		t.Fatalf("precondition: reclaim completed before the turn ended (stopCalls=%d)", handle.stopCalls.Load())
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := agentloop.Enqueue(tmpDir, "alice", agentloop.Entry{
		ShortID: "m-window", Class: agentloop.ClassAsync, From: "weave", Body: "new work",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	handle.flipInTurnAfter.Store(1 << 30)
	handle.urt.EventBus().Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnCompleted})

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("maybeReclaimIdle did not return after the turn ended")
	}

	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("precondition: stopCalls = %d, want 0 — the teardown must have been ABANDONED for this record to exist", got)
	}
	got := refusalRecordsAt(t, h, slog.LevelInfo, abandonMsg)
	if len(got) != 1 {
		t.Fatalf("INFO-level %q records = %d, want 1. all records:\n%s", abandonMsg, len(got), h.String())
	}
	if !strings.Contains(got[0], "blocker=pending_queue") {
		t.Errorf("abandon record does not name the term that saved the agent; got: %s", got[0])
	}

	// The CALL-SITE half of the namespacing, which the white-box unit cannot
	// reach because it passes keys by hand. The mail is still queued, so the next
	// sweep refuses at phase A on the same term the abandonment named. Drop the
	// "abandon:" prefix in production and this record collides with the
	// abandonment's key and is demoted to Debug — i.e. an operator sees the
	// reaper back off and then never sees why it keeps refusing.
	r.maybeReclaimIdle(context.Background(), rt)
	after := refusalRecordsAt(t, h, slog.LevelInfo, refusalMsg)
	if len(after) != 1 {
		t.Fatalf("INFO-level %q records = %d after an abandonment on the SAME term, want 1; the abandon and assess sites must not share a dedup key. all records:\n%s",
			refusalMsg, len(after), h.String())
	}
	if !strings.Contains(after[0], "blocker=pending_queue") {
		t.Errorf("post-abandon refusal record blocker, want pending_queue: %s", after[0])
	}
}

// TestRefusalLevel_IsChangeKeyedAndPerAgent is the white-box unit on the helper
// the four call sites share (QUM-1197). The per-agent arm is the one that
// matters: an agent-agnostic implementation would swallow the FIRST refusal of
// every agent after the first, which is the exact defect this change removes.
func TestRefusalLevel_IsChangeKeyedAndPerAgent(t *testing.T) {
	r, _ := newFakeReal(t)

	if got := r.refusalLevel("alice", "assess:in_turn"); got != slog.LevelInfo {
		t.Errorf("first refusal for alice logged at %v, want %v", got, slog.LevelInfo)
	}
	if got := r.refusalLevel("alice", "assess:in_turn"); got != slog.LevelDebug {
		t.Errorf("repeat refusal for alice logged at %v, want %v", got, slog.LevelDebug)
	}
	if got := r.refusalLevel("alice", "assess:quiescent"); got != slog.LevelInfo {
		t.Errorf("changed refusal for alice logged at %v, want %v", got, slog.LevelInfo)
	}
	if got := r.refusalLevel("alice", "assess:in_turn_unobservable"); got != slog.LevelInfo {
		t.Errorf("busy→unobservable on the same term logged at %v, want %v; D1a: losing the observation is news, not a repeat",
			got, slog.LevelInfo)
	}
	// The live direction of the call-site namespacing: an abandonment recording a
	// term must not demote a later phase-A refusal on that same term. The reverse
	// direction cannot happen (forgetRefusal runs before phase B), so it is not
	// asserted — an assertion for an unreachable direction is not a check.
	r.forgetRefusal("alice")
	if got := r.refusalLevel("alice", "abandon:in_turn"); got != slog.LevelInfo {
		t.Errorf("first abandon refusal for alice logged at %v, want %v", got, slog.LevelInfo)
	}
	if got := r.refusalLevel("alice", "assess:in_turn"); got != slog.LevelInfo {
		t.Errorf("phase-A refusal after an abandon on the SAME term logged at %v, want %v; the call-site namespace is what keeps one from swallowing the other",
			got, slog.LevelInfo)
	}
	if got := r.refusalLevel("bob", "assess:in_turn"); got != slog.LevelInfo {
		t.Errorf("FIRST refusal for bob logged at %v, want %v; the dedup state must be per-agent", got, slog.LevelInfo)
	}
	r.forgetRefusal("alice")
	if got := r.refusalLevel("alice", "assess:in_turn_unobservable"); got != slog.LevelInfo {
		t.Errorf("refusal after forgetRefusal logged at %v, want %v; an agent that stopped refusing and starts again is news", got, slog.LevelInfo)
	}
}

// TestRefusalLevel_ConcurrentForOneAgent makes refusalMu's absence DETECTABLE.
// Production sweeps serially, so this is not a reproduction of a live race — it
// is what turns "the lock is intended" into "the lock is checked": with
// refusalMu removed, -race reports a write/write on lastRefusal here, and
// without this arm that mutation leaves the whole suite green.
func TestRefusalLevel_ConcurrentForOneAgent(t *testing.T) {
	r, _ := newFakeReal(t)

	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = r.refusalLevel("alice", fmt.Sprintf("assess:term-%d-%d", g, i))
			}
		}(g)
	}
	wg.Wait()
}
