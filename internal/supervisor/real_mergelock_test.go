package supervisor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/sprawlmcp/calllog"
	"github.com/dmotles/sprawl/internal/state"
)

// QUM-588 Part 1: per-sprawl-root serialization of Real.Merge.
//
// Design (oracle-approved):
//   * MergeOutcome gains two fields: QueuedBehind (string, "" iff uncontended)
//     and QueueWait (time.Duration, 0 iff uncontended).
//   * Real gains a mergeSem (chan struct{}, capacity 1) acquired by Merge before
//     invoking mergeFn, plus mergeInflightMu/mergeInflight for non-blocking
//     contention probing.
//   * If Merge observes an in-flight merge at probe time, it captures the
//     in-flight agent name, blocks on the sem, then on acquisition populates
//     outcome.QueuedBehind / outcome.QueueWait.
//   * Contended path emits two checkpoints via composeCheckpoint(callID):
//     "merge.queued" before blocking and "merge.starting" after acquiring.
//     The implementer encodes contention details under the kv["line"] key
//     (e.g. "behind=agent-a elapsed=42ms") so the existing extractKVLine
//     path conveys them through SetProgressEmitter unchanged.
//   * Cancellation: a blocked caller whose ctx is cancelled returns ctx.Err()
//     and outcome == nil without deadlocking the in-flight holder.
//   * Non-merge operations (Spawn/Retire/Kill) are unaffected.

// TestMerge_SerializesConcurrent — two concurrent Merge calls must run
// strictly sequentially. The first runs uncontended (QueuedBehind == "");
// the second is queued behind the first and observes QueuedBehind set to
// the first caller's agent name plus a positive QueueWait.
func TestMerge_SerializesConcurrent(t *testing.T) {
	r, _ := newFakeReal(t)

	var entered int64 // number of mergeFn entries
	enteredCh := make(chan string, 2)
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})

	r.mergeFn = func(_ context.Context, _ *agentops.MergeDeps, name, _ string, _, _ bool) (*agentops.MergeOutcome, error) {
		atomic.AddInt64(&entered, 1)
		enteredCh <- name
		switch name {
		case "agent-a":
			<-releaseA
		case "agent-b":
			<-releaseB
		}
		return &agentops.MergeOutcome{ResolvedBranch: "main"}, nil
	}

	type result struct {
		outcome *agentops.MergeOutcome
		err     error
	}
	resA := make(chan result, 1)
	resB := make(chan result, 1)

	// G1: kick off agent-a; it will block in mergeFn until releaseA is closed.
	go func() {
		o, err := r.Merge(context.Background(), "", "agent-a", "", false)
		resA <- result{o, err}
	}()

	// Wait for G1 to enter mergeFn (i.e. it owns the lock).
	select {
	case who := <-enteredCh:
		if who != "agent-a" {
			t.Fatalf("first mergeFn entry = %q, want agent-a", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent-a mergeFn entry")
	}

	// G2: kick off agent-b. It must block on the merge lock and NOT enter
	// mergeFn until releaseA is closed.
	go func() {
		o, err := r.Merge(context.Background(), "", "agent-b", "", false)
		resB <- result{o, err}
	}()

	// Within 50ms, G2's mergeFn must NOT be entered.
	select {
	case who := <-enteredCh:
		t.Fatalf("agent-b mergeFn entered while agent-a still holds the lock (got %q) — Real.Merge is not serialized", who)
	case <-time.After(50 * time.Millisecond):
		// Good: agent-b is queued behind.
	}
	if got := atomic.LoadInt64(&entered); got != 1 {
		t.Fatalf("mergeFn entries while contended = %d, want 1", got)
	}

	// Release agent-a, let it return; then wait for agent-b to enter mergeFn
	// and release it as well.
	close(releaseA)

	// Wait for agent-b mergeFn entry.
	select {
	case who := <-enteredCh:
		if who != "agent-b" {
			t.Fatalf("second mergeFn entry = %q, want agent-b", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent-b mergeFn entry after release")
	}
	close(releaseB)

	var gotA, gotB result
	select {
	case gotA = <-resA:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent-a Merge to return")
	}
	select {
	case gotB = <-resB:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent-b Merge to return")
	}

	if gotA.err != nil {
		t.Fatalf("agent-a Merge err = %v, want nil", gotA.err)
	}
	if gotB.err != nil {
		t.Fatalf("agent-b Merge err = %v, want nil", gotB.err)
	}
	if gotA.outcome == nil || gotB.outcome == nil {
		t.Fatalf("outcomes: a=%+v b=%+v, both want non-nil", gotA.outcome, gotB.outcome)
	}

	// agent-a was uncontended.
	if gotA.outcome.QueuedBehind != "" {
		t.Errorf("agent-a QueuedBehind = %q, want empty (uncontended)", gotA.outcome.QueuedBehind)
	}
	if gotA.outcome.QueueWait != 0 {
		t.Errorf("agent-a QueueWait = %v, want 0 (uncontended)", gotA.outcome.QueueWait)
	}

	// agent-b was queued behind agent-a.
	if gotB.outcome.QueuedBehind != "agent-a" {
		t.Errorf("agent-b QueuedBehind = %q, want %q", gotB.outcome.QueuedBehind, "agent-a")
	}
	if gotB.outcome.QueueWait <= 0 {
		t.Errorf("agent-b QueueWait = %v, want > 0", gotB.outcome.QueueWait)
	}

	// mergeFn must have run exactly twice (once per caller).
	if got := atomic.LoadInt64(&entered); got != 2 {
		t.Errorf("total mergeFn entries = %d, want 2", got)
	}
}

// TestMerge_BlockedCallerCancelUnblocks — a caller blocked on the merge
// lock must abort when its context is cancelled, returning ctx.Err() and
// outcome==nil, without deadlocking the in-flight holder.
func TestMerge_BlockedCallerCancelUnblocks(t *testing.T) {
	r, _ := newFakeReal(t)

	enteredA := make(chan struct{})
	releaseA := make(chan struct{})

	r.mergeFn = func(_ context.Context, _ *agentops.MergeDeps, name, _ string, _, _ bool) (*agentops.MergeOutcome, error) {
		if name == "agent-a" {
			close(enteredA)
			<-releaseA
		}
		return &agentops.MergeOutcome{ResolvedBranch: "main"}, nil
	}

	resA := make(chan error, 1)
	go func() {
		_, err := r.Merge(context.Background(), "", "agent-a", "", false)
		resA <- err
	}()

	select {
	case <-enteredA:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent-a to enter mergeFn")
	}

	// agent-b blocks on the lock with a cancellable context.
	ctxB, cancelB := context.WithCancel(context.Background())
	type result struct {
		outcome *agentops.MergeOutcome
		err     error
	}
	resB := make(chan result, 1)
	go func() {
		o, err := r.Merge(ctxB, "", "agent-b", "", false)
		resB <- result{o, err}
	}()

	// Give agent-b a moment to park on the lock.
	time.Sleep(20 * time.Millisecond)
	cancelB()

	select {
	case got := <-resB:
		if got.err == nil {
			t.Fatal("agent-b Merge err = nil, want context.Canceled")
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Errorf("agent-b Merge err = %v, want errors.Is(err, context.Canceled)", got.err)
		}
		if got.outcome != nil {
			t.Errorf("agent-b outcome = %+v, want nil on cancel", got.outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent-b Merge did not return after ctx cancellation (deadlock?)")
	}

	// agent-a must still be able to complete cleanly.
	close(releaseA)
	select {
	case err := <-resA:
		if err != nil {
			t.Errorf("agent-a Merge err = %v, want nil after independent unblock", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent-a Merge did not return after release — cancellation of agent-b broke the in-flight holder")
	}
}

// TestMerge_UncontendedNoQueueMessage — a lone Merge call observes no
// QueuedBehind / QueueWait values on its outcome.
func TestMerge_UncontendedNoQueueMessage(t *testing.T) {
	r, _ := newFakeReal(t)
	r.mergeFn = func(context.Context, *agentops.MergeDeps, string, string, bool, bool) (*agentops.MergeOutcome, error) {
		return &agentops.MergeOutcome{NoOp: false, ResolvedBranch: "main"}, nil
	}
	outcome, err := r.Merge(context.Background(), "", "alpha", "", false)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if outcome == nil {
		t.Fatal("Merge returned nil outcome")
	}
	if outcome.QueuedBehind != "" {
		t.Errorf("QueuedBehind = %q, want empty (uncontended)", outcome.QueuedBehind)
	}
	if outcome.QueueWait != 0 {
		t.Errorf("QueueWait = %v, want 0 (uncontended)", outcome.QueueWait)
	}
}

// TestMerge_NonMergeOpsNotSerialized — the merge lock must NOT serialize
// non-merge operations. While Merge is holding the lock, Spawn must still
// return promptly.
func TestMerge_NonMergeOpsNotSerialized(t *testing.T) {
	r, _ := newFakeReal(t)

	enteredA := make(chan struct{})
	releaseA := make(chan struct{})
	r.mergeFn = func(_ context.Context, _ *agentops.MergeDeps, _, _ string, _, _ bool) (*agentops.MergeOutcome, error) {
		close(enteredA)
		<-releaseA
		return &agentops.MergeOutcome{}, nil
	}

	resA := make(chan error, 1)
	go func() {
		_, err := r.Merge(context.Background(), "", "agent-a", "", false)
		resA <- err
	}()
	select {
	case <-enteredA:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent-a to enter mergeFn")
	}

	// Spawn must NOT block on the merge lock.
	r.spawnFn = func(*agentops.SpawnDeps, string, string, string, string, bool) (*state.AgentState, error) {
		return &state.AgentState{Name: "n", Type: "engineer", Family: "engineering", Parent: "weave", Status: "active"}, nil
	}
	spawnDone := make(chan error, 1)
	go func() {
		_, err := r.Spawn(context.Background(), SpawnRequest{
			Family: "engineering", Type: "engineer", Prompt: "x", Branch: "b",
		})
		spawnDone <- err
	}()
	select {
	case err := <-spawnDone:
		if err != nil {
			t.Errorf("Spawn err while merge held lock = %v, want nil", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Spawn blocked while Merge held the merge lock — non-merge ops must not be serialized")
	}

	// Release the in-flight merge so the test exits cleanly.
	close(releaseA)
	select {
	case <-resA:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out draining agent-a Merge")
	}
}

// TestMerge_EmitsQueuedAndStartingCheckpoints — a contended Merge must emit
// "merge.queued" and "merge.starting" checkpoints visible to the
// SetProgressEmitter consumer.
//
// IMPLEMENTER NOTE: composeCheckpoint's progress-emitter path extracts the
// kv key "line" via extractKVLine. To convey the in-flight agent name +
// elapsed time without changing the fan-out path, encode them under "line"
// in the merge.queued / merge.starting checkpoint payload — e.g.:
//
//	cp("merge.queued",   "line", fmt.Sprintf("behind=%s elapsed=%s", inflight, since))
//	cp("merge.starting", "line", fmt.Sprintf("behind=%s waited=%s",  inflight, since))
//
// This test asserts only on the step names and that the tail line references
// the in-flight agent name "agent-a"; the implementer picks the exact
// formatting.
func TestMerge_EmitsQueuedAndStartingCheckpoints(t *testing.T) {
	r, _ := newFakeReal(t)

	type ev struct{ callID, step, tail string }
	var mu sync.Mutex
	var events []ev
	r.SetProgressEmitter(func(callID, step, tail string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev{callID, step, tail})
	})

	enteredA := make(chan struct{})
	releaseA := make(chan struct{})
	r.mergeFn = func(_ context.Context, _ *agentops.MergeDeps, name, _ string, _, _ bool) (*agentops.MergeOutcome, error) {
		if name == "agent-a" {
			close(enteredA)
			<-releaseA
		}
		return &agentops.MergeOutcome{}, nil
	}

	// Run agent-a uncontended (no call_id needed) so it acquires the lock.
	resA := make(chan error, 1)
	go func() {
		_, err := r.Merge(context.Background(), "", "agent-a", "", false)
		resA <- err
	}()
	select {
	case <-enteredA:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent-a to enter mergeFn")
	}

	// agent-b runs with a call_id so the progress emitter receives events.
	const callID = "call-b"
	ctxB := calllog.WithCallID(context.Background(), callID)
	resB := make(chan error, 1)
	go func() {
		_, err := r.Merge(ctxB, "", "agent-b", "", false)
		resB <- err
	}()

	// Give agent-b a moment to park and emit the merge.queued checkpoint.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		var seenQueued bool
		for _, e := range events {
			if e.step == "merge.queued" && e.callID == callID {
				seenQueued = true
				break
			}
		}
		mu.Unlock()
		if seenQueued {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(releaseA)
	if err := <-resA; err != nil {
		t.Fatalf("agent-a Merge: %v", err)
	}
	select {
	case err := <-resB:
		if err != nil {
			t.Fatalf("agent-b Merge: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent-b Merge did not return after release")
	}

	mu.Lock()
	defer mu.Unlock()
	var queued, starting *ev
	for i := range events {
		if events[i].callID != callID {
			continue
		}
		switch events[i].step {
		case "merge.queued":
			if queued == nil {
				queued = &events[i]
			}
		case "merge.starting":
			if starting == nil {
				starting = &events[i]
			}
		}
	}
	if queued == nil {
		t.Fatalf("missing merge.queued checkpoint for callID %q; got events=%+v", callID, events)
	}
	if !strings.Contains(queued.tail, "agent-a") {
		t.Errorf("merge.queued tail = %q, want to reference in-flight agent %q (encode via kv[\"line\"])", queued.tail, "agent-a")
	}
	if starting == nil {
		t.Fatalf("missing merge.starting checkpoint for callID %q; got events=%+v", callID, events)
	}
	if !strings.Contains(starting.tail, "agent-a") {
		t.Errorf("merge.starting tail = %q, want to reference in-flight agent %q (encode via kv[\"line\"])", starting.tail, "agent-a")
	}
}
