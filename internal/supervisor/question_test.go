package supervisor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeConsumer is a test-local QuestionConsumer that records OnEnqueue and
// OnCancel callbacks under its own mutex so tests can make assertions about
// what the queue notified it of.
type fakeConsumer struct {
	name string

	mu        sync.Mutex
	enqueued  []*PendingQuestion
	cancelled []cancelledRecord
}

type cancelledRecord struct {
	RequestID string
	Reason    string
}

// Compile-time guard: keep fakeConsumer in sync with the QuestionConsumer
// interface. If the interface drifts and the fake stops satisfying it, this
// line fails to compile before any test runs.
var _ QuestionConsumer = (*fakeConsumer)(nil)

func newFakeConsumer(name string) *fakeConsumer {
	return &fakeConsumer{name: name}
}

func (f *fakeConsumer) Name() string { return f.name }

func (f *fakeConsumer) OnEnqueue(pq *PendingQuestion) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueued = append(f.enqueued, pq)
}

func (f *fakeConsumer) OnCancel(requestID, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, cancelledRecord{RequestID: requestID, Reason: reason})
}

func (f *fakeConsumer) snapshotEnqueued() []*PendingQuestion {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*PendingQuestion, len(f.enqueued))
	copy(out, f.enqueued)
	return out
}

func (f *fakeConsumer) snapshotCancelled() []cancelledRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]cancelledRecord, len(f.cancelled))
	copy(out, f.cancelled)
	return out
}

// waitForDepth polls PeekQuestions until depth==target or timeout fires.
func waitForDepth(t *testing.T, r *Real, target int, timeout time.Duration) *PendingQuestion {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		depth, head := r.PeekQuestions()
		if depth == target {
			return head
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitForDepth(%d): timed out, last depth=%d", target, depth)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// drainQuestions ensures the queue is empty by cancelling everything in it.
// Use as a t.Cleanup to prevent goroutine leaks from failed tests. Guards
// against a buggy implementation that never decrements depth by giving up
// after ~2s rather than spinning forever.
func drainQuestions(t *testing.T, r *Real) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		depth, head := r.PeekQuestions()
		if depth == 0 || head == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Logf("drainQuestions: gave up after 2s with depth=%d (possible buggy impl)", depth)
			return
		}
		r.CancelQuestion(head.Req.RequestID, "test-cleanup")
	}
}

// waitForEnqueuedCount polls the consumer's OnEnqueue notification count
// until it reaches target or the timeout expires. This is stricter than
// waitForDepth because it observes the consumer-visible side effect, not
// just queue depth — ensuring OnEnqueue has actually fired.
func waitForEnqueuedCount(t *testing.T, c *fakeConsumer, target int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		n := len(c.snapshotEnqueued())
		if n == target {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitForEnqueuedCount(%s, %d): timed out, last count=%d", c.name, target, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForCancelledCount polls the consumer's OnCancel notification count
// until it reaches target or the timeout expires.
func waitForCancelledCount(t *testing.T, c *fakeConsumer, target int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		n := len(c.snapshotCancelled())
		if n == target {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitForCancelledCount(%s, %d): timed out, last count=%d", c.name, target, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestQuestionQueue_InterfaceSatisfied(t *testing.T) {
	var _ Supervisor = (*Real)(nil)
}

func TestQuestionQueue_FIFO(t *testing.T) {
	r, _ := newTestSupervisor(t)
	t.Cleanup(func() { drainQuestions(t, r) })

	consumer := newFakeConsumer("tui")
	if err := r.RegisterQuestionConsumer(consumer); err != nil {
		t.Fatalf("RegisterQuestionConsumer: %v", err)
	}
	t.Cleanup(func() { r.UnregisterQuestionConsumer("tui") })

	type result struct {
		id   string
		resp QuestionResponse
		err  error
	}
	results := make(chan result, 3)

	// Gate goroutines to enforce deterministic enqueue order. Each goroutine
	// waits for its predecessor to be visible in the queue (depth==i) before
	// calling AskUserQuestion.
	enqueue := func(id string) {
		go func() {
			req := QuestionRequest{RequestID: id, From: "agent1"}
			resp, err := r.AskUserQuestion(context.Background(), req)
			results <- result{id: id, resp: resp, err: err}
		}()
	}

	enqueue("q1")
	waitForDepth(t, r, 1, 2*time.Second)
	enqueue("q2")
	waitForDepth(t, r, 2, 2*time.Second)
	enqueue("q3")
	head := waitForDepth(t, r, 3, 2*time.Second)

	if head == nil || head.Req.RequestID != "q1" {
		t.Fatalf("head = %+v, want q1", head)
	}

	// Resolve in order and assert goroutines unblock in the SAME order.
	// The results channel receive immediately after each Resolve must yield
	// the ID we just resolved — proving FIFO completion (not just FIFO head).
	expectNext := func(wantID string) {
		t.Helper()
		select {
		case res := <-results:
			if res.err != nil {
				t.Errorf("AskUserQuestion(%s) err = %v", res.id, res.err)
			}
			if res.id != wantID {
				t.Fatalf("FIFO violation: next unblocked goroutine = %q, want %q", res.id, wantID)
			}
			if res.resp.Outcome != OutcomeAnswered {
				t.Errorf("AskUserQuestion(%s) outcome = %q, want answered", res.id, res.resp.Outcome)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for AskUserQuestion(%s) goroutine to return", wantID)
		}
	}

	if ok := r.ResolveQuestion("q1", QuestionResponse{RequestID: "q1", Outcome: OutcomeAnswered}); !ok {
		t.Fatal("ResolveQuestion(q1) returned false")
	}
	expectNext("q1")
	head = waitForDepth(t, r, 2, 2*time.Second)
	if head == nil || head.Req.RequestID != "q2" {
		t.Fatalf("head after q1 resolve = %+v, want q2", head)
	}

	if ok := r.ResolveQuestion("q2", QuestionResponse{RequestID: "q2", Outcome: OutcomeAnswered}); !ok {
		t.Fatal("ResolveQuestion(q2) returned false")
	}
	expectNext("q2")
	head = waitForDepth(t, r, 1, 2*time.Second)
	if head == nil || head.Req.RequestID != "q3" {
		t.Fatalf("head after q2 resolve = %+v, want q3", head)
	}

	if ok := r.ResolveQuestion("q3", QuestionResponse{RequestID: "q3", Outcome: OutcomeAnswered}); !ok {
		t.Fatal("ResolveQuestion(q3) returned false")
	}
	expectNext("q3")
	waitForDepth(t, r, 0, 2*time.Second)
}

func TestQuestionQueue_ConcurrentEnqueue(t *testing.T) {
	r, _ := newTestSupervisor(t)
	t.Cleanup(func() { drainQuestions(t, r) })

	consumer := newFakeConsumer("tui")
	if err := r.RegisterQuestionConsumer(consumer); err != nil {
		t.Fatalf("RegisterQuestionConsumer: %v", err)
	}
	t.Cleanup(func() { r.UnregisterQuestionConsumer("tui") })

	const N = 20
	start := make(chan struct{})
	done := make(chan struct{}, N)

	for i := 0; i < N; i++ {
		i := i
		go func() {
			<-start
			req := QuestionRequest{
				RequestID: fmt.Sprintf("q-%d", i),
				From:      fmt.Sprintf("a%d", i),
			}
			_, _ = r.AskUserQuestion(context.Background(), req)
			done <- struct{}{}
		}()
	}

	close(start)
	// Wait on consumer-visible OnEnqueue count rather than queue depth — that
	// way a buggy impl that increments depth but never notifies the consumer
	// still fails this test.
	waitForEnqueuedCount(t, consumer, N, 5*time.Second)

	// Assert exactly the N RequestIDs were observed on the consumer's OnEnqueue.
	enq := consumer.snapshotEnqueued()
	if len(enq) != N {
		t.Fatalf("consumer OnEnqueue len = %d, want %d", len(enq), N)
	}
	seen := make(map[string]bool, N)
	for _, pq := range enq {
		if seen[pq.Req.RequestID] {
			t.Errorf("duplicate enqueue notification for %q", pq.Req.RequestID)
		}
		seen[pq.Req.RequestID] = true
	}
	for i := 0; i < N; i++ {
		want := fmt.Sprintf("q-%d", i)
		if !seen[want] {
			t.Errorf("missing enqueue notification for %q", want)
		}
	}

	// Cancel all to release goroutines.
	for i := 0; i < N; i++ {
		r.CancelQuestion(fmt.Sprintf("q-%d", i), "test-done")
	}

	// All N goroutines should return.
	for i := 0; i < N; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for AskUserQuestion goroutine %d to return", i)
		}
	}
}

func TestQuestionQueue_ResolveIdempotent(t *testing.T) {
	r, _ := newTestSupervisor(t)
	t.Cleanup(func() { drainQuestions(t, r) })

	consumer := newFakeConsumer("tui")
	if err := r.RegisterQuestionConsumer(consumer); err != nil {
		t.Fatalf("RegisterQuestionConsumer: %v", err)
	}
	t.Cleanup(func() { r.UnregisterQuestionConsumer("tui") })

	type result struct {
		resp QuestionResponse
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		req := QuestionRequest{RequestID: "q1", From: "agent1"}
		resp, err := r.AskUserQuestion(context.Background(), req)
		resCh <- result{resp: resp, err: err}
	}()

	waitForDepth(t, r, 1, 2*time.Second)

	if ok := r.ResolveQuestion("q1", QuestionResponse{RequestID: "q1", Outcome: OutcomeAnswered, Note: "first"}); !ok {
		t.Fatal("first ResolveQuestion returned false, want true")
	}
	if ok := r.ResolveQuestion("q1", QuestionResponse{RequestID: "q1", Outcome: OutcomeAnswered, Note: "second"}); ok {
		t.Fatal("second ResolveQuestion returned true, want false (idempotent)")
	}

	select {
	case got := <-resCh:
		if got.err != nil {
			t.Fatalf("AskUserQuestion err = %v", got.err)
		}
		if got.resp.Note != "first" {
			t.Errorf("response.Note = %q, want %q", got.resp.Note, "first")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AskUserQuestion to return")
	}

	depth, _ := r.PeekQuestions()
	if depth != 0 {
		t.Errorf("depth = %d after resolve, want 0", depth)
	}
}

func TestQuestionQueue_CancelIdempotent(t *testing.T) {
	r, _ := newTestSupervisor(t)
	t.Cleanup(func() { drainQuestions(t, r) })

	consumer := newFakeConsumer("tui")
	if err := r.RegisterQuestionConsumer(consumer); err != nil {
		t.Fatalf("RegisterQuestionConsumer: %v", err)
	}
	t.Cleanup(func() { r.UnregisterQuestionConsumer("tui") })

	type result struct {
		resp QuestionResponse
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		req := QuestionRequest{RequestID: "q1", From: "agent1"}
		resp, err := r.AskUserQuestion(context.Background(), req)
		resCh <- result{resp: resp, err: err}
	}()

	waitForDepth(t, r, 1, 2*time.Second)

	if ok := r.CancelQuestion("q1", "user-dismissed"); !ok {
		t.Fatal("first CancelQuestion returned false, want true")
	}
	if ok := r.CancelQuestion("q1", "ignored-reason"); ok {
		t.Fatal("second CancelQuestion returned true, want false (idempotent)")
	}

	select {
	case got := <-resCh:
		if got.err != nil {
			t.Fatalf("AskUserQuestion err = %v", got.err)
		}
		if got.resp.Outcome != OutcomeSessionEnded {
			t.Errorf("response.Outcome = %q, want %q", got.resp.Outcome, OutcomeSessionEnded)
		}
		if got.resp.Note != "user-dismissed" {
			t.Errorf("response.Note = %q, want %q", got.resp.Note, "user-dismissed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AskUserQuestion to return")
	}

	cancelled := consumer.snapshotCancelled()
	if len(cancelled) != 1 {
		t.Fatalf("consumer.cancelled len = %d, want 1", len(cancelled))
	}
	if cancelled[0].Reason != "user-dismissed" || cancelled[0].RequestID != "q1" {
		t.Errorf("cancelled[0] = %+v, want {q1, user-dismissed}", cancelled[0])
	}
}

// TestQuestionQueue_CancelByAgent: contract is "CancelByAgent matches
// PendingQuestion.Req.From" — all queued questions whose Req.From equals
// the supplied agent name are cancelled with the supplied reason; others
// remain untouched.
func TestQuestionQueue_CancelByAgent(t *testing.T) {
	r, _ := newTestSupervisor(t)
	t.Cleanup(func() { drainQuestions(t, r) })

	consumer := newFakeConsumer("tui")
	if err := r.RegisterQuestionConsumer(consumer); err != nil {
		t.Fatalf("RegisterQuestionConsumer: %v", err)
	}
	t.Cleanup(func() { r.UnregisterQuestionConsumer("tui") })

	type result struct {
		id   string
		resp QuestionResponse
		err  error
	}
	resCh := make(chan result, 3)

	ask := func(id, from string) {
		go func() {
			req := QuestionRequest{RequestID: id, From: from}
			resp, err := r.AskUserQuestion(context.Background(), req)
			resCh <- result{id: id, resp: resp, err: err}
		}()
	}

	ask("q1", "alice")
	waitForDepth(t, r, 1, 2*time.Second)
	ask("q2", "bob")
	waitForDepth(t, r, 2, 2*time.Second)
	ask("q3", "alice")
	waitForDepth(t, r, 3, 2*time.Second)

	r.CancelByAgent("alice", "retired")

	// q2 should remain.
	head := waitForDepth(t, r, 1, 2*time.Second)
	if head == nil || head.Req.RequestID != "q2" {
		t.Fatalf("head after CancelByAgent(alice) = %+v, want q2", head)
	}

	// Consumer should have observed two cancellations for alice's questions.
	waitForCancelledCount(t, consumer, 2, 2*time.Second)
	cancelled := consumer.snapshotCancelled()
	if len(cancelled) != 2 {
		t.Fatalf("consumer.cancelled len = %d, want 2", len(cancelled))
	}
	gotIDs := map[string]bool{}
	for _, c := range cancelled {
		gotIDs[c.RequestID] = true
		if c.Reason != "retired" {
			t.Errorf("cancelled.Reason = %q, want retired", c.Reason)
		}
	}
	if !gotIDs["q1"] || !gotIDs["q3"] {
		t.Errorf("cancelled IDs = %v, want {q1, q3}", gotIDs)
	}

	// Drain alice's two goroutines.
	got := map[string]QuestionResponse{}
	for i := 0; i < 2; i++ {
		select {
		case res := <-resCh:
			if res.err != nil {
				t.Errorf("AskUserQuestion(%s) err = %v", res.id, res.err)
			}
			got[res.id] = res.resp
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for alice's AskUserQuestion to return")
		}
	}
	for _, id := range []string{"q1", "q3"} {
		resp, ok := got[id]
		if !ok {
			t.Errorf("missing response for %s", id)
			continue
		}
		if resp.Outcome != OutcomeAgentRetired {
			t.Errorf("%s outcome = %q, want %q", id, resp.Outcome, OutcomeAgentRetired)
		}
		if resp.Note != "retired" {
			t.Errorf("%s note = %q, want retired", id, resp.Note)
		}
	}

	// Clean up q2.
	r.CancelQuestion("q2", "cleanup")
	select {
	case <-resCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for q2 cleanup")
	}
}

func TestQuestionQueue_NoConsumerTUIUnavailable(t *testing.T) {
	r, _ := newTestSupervisor(t)
	t.Cleanup(func() { drainQuestions(t, r) })

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	startTime := time.Now()
	req := QuestionRequest{RequestID: "q1", From: "agent1"}
	resp, err := r.AskUserQuestion(ctx, req)
	elapsed := time.Since(startTime)

	if err != nil {
		t.Fatalf("AskUserQuestion err = %v, want nil", err)
	}
	if resp.Outcome != OutcomeTUIUnavailable {
		t.Errorf("Outcome = %q, want %q", resp.Outcome, OutcomeTUIUnavailable)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("returned after %v, want <100ms (should be immediate)", elapsed)
	}

	depth, _ := r.PeekQuestions()
	if depth != 0 {
		t.Errorf("depth = %d, want 0 (nothing should be queued)", depth)
	}
}

func TestQuestionQueue_MultipleConsumers(t *testing.T) {
	r, _ := newTestSupervisor(t)
	t.Cleanup(func() { drainQuestions(t, r) })

	tui := newFakeConsumer("tui")
	slack := newFakeConsumer("slack")
	if err := r.RegisterQuestionConsumer(tui); err != nil {
		t.Fatalf("RegisterQuestionConsumer(tui): %v", err)
	}
	t.Cleanup(func() { r.UnregisterQuestionConsumer("tui") })
	if err := r.RegisterQuestionConsumer(slack); err != nil {
		t.Fatalf("RegisterQuestionConsumer(slack): %v", err)
	}
	t.Cleanup(func() { r.UnregisterQuestionConsumer("slack") })

	type result struct {
		resp QuestionResponse
		err  error
	}
	resCh := make(chan result, 2)

	// q1 — enqueue and cancel; both consumers should be notified of both.
	go func() {
		req := QuestionRequest{RequestID: "q1", From: "agent1"}
		resp, err := r.AskUserQuestion(context.Background(), req)
		resCh <- result{resp: resp, err: err}
	}()
	waitForEnqueuedCount(t, tui, 1, 2*time.Second)
	waitForEnqueuedCount(t, slack, 1, 2*time.Second)

	r.CancelQuestion("q1", "test")
	<-resCh

	waitForCancelledCount(t, tui, 1, 2*time.Second)
	waitForCancelledCount(t, slack, 1, 2*time.Second)

	// q2 — enqueue and resolve; cancelled should NOT grow.
	go func() {
		req := QuestionRequest{RequestID: "q2", From: "agent1"}
		resp, err := r.AskUserQuestion(context.Background(), req)
		resCh <- result{resp: resp, err: err}
	}()
	waitForEnqueuedCount(t, tui, 2, 2*time.Second)
	waitForEnqueuedCount(t, slack, 2, 2*time.Second)

	r.ResolveQuestion("q2", QuestionResponse{RequestID: "q2", Outcome: OutcomeAnswered})
	<-resCh

	if got := len(tui.snapshotCancelled()); got != 1 {
		t.Errorf("tui cancelled after resolve = %d, want 1 (unchanged)", got)
	}
	if got := len(slack.snapshotCancelled()); got != 1 {
		t.Errorf("slack cancelled after resolve = %d, want 1 (unchanged)", got)
	}
}

func TestQuestionQueue_ResolveRace(t *testing.T) {
	r, _ := newTestSupervisor(t)
	t.Cleanup(func() { drainQuestions(t, r) })

	consumer := newFakeConsumer("tui")
	if err := r.RegisterQuestionConsumer(consumer); err != nil {
		t.Fatalf("RegisterQuestionConsumer: %v", err)
	}
	t.Cleanup(func() { r.UnregisterQuestionConsumer("tui") })

	type result struct {
		resp QuestionResponse
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		req := QuestionRequest{RequestID: "q1", From: "agent1"}
		resp, err := r.AskUserQuestion(context.Background(), req)
		resCh <- result{resp: resp, err: err}
	}()
	waitForDepth(t, r, 1, 2*time.Second)

	start := make(chan struct{})
	var trueCount int32
	var falseCount int32
	var wg sync.WaitGroup

	// outcomes carries each resolver's (note, ok) so we can identify which
	// note belongs to the winner (ok==true) and assert that exact note is
	// the one the blocked AskUserQuestion caller received.
	type outcome struct {
		note string
		ok   bool
	}
	outcomes := make(chan outcome, 2)

	notes := []string{"winner-A", "winner-B"}
	wg.Add(2)
	for _, note := range notes {
		note := note
		go func() {
			defer wg.Done()
			<-start
			ok := r.ResolveQuestion("q1", QuestionResponse{
				RequestID: "q1",
				Outcome:   OutcomeAnswered,
				Note:      note,
			})
			if ok {
				atomic.AddInt32(&trueCount, 1)
			} else {
				atomic.AddInt32(&falseCount, 1)
			}
			outcomes <- outcome{note: note, ok: ok}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	if trueCount != 1 {
		t.Errorf("trueCount = %d, want 1", trueCount)
	}
	if falseCount != 1 {
		t.Errorf("falseCount = %d, want 1", falseCount)
	}

	// Identify the winner — the resolver whose ok==true. The blocked
	// AskUserQuestion caller MUST observe this exact note.
	var winningNote string
	winners := 0
	for o := range outcomes {
		if o.ok {
			winningNote = o.note
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly 1 resolver to win, got %d", winners)
	}

	select {
	case got := <-resCh:
		if got.err != nil {
			t.Fatalf("AskUserQuestion err = %v", got.err)
		}
		if got.resp.Note != winningNote {
			t.Errorf("response.Note = %q, want winning resolver's note %q", got.resp.Note, winningNote)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AskUserQuestion to return")
	}
}
