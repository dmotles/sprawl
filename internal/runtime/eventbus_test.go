package runtime

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// recvOrTimeout pulls a single event from ch or fails the test after d.
func recvOrTimeout(t *testing.T, ch <-chan RuntimeEvent, d time.Duration) RuntimeEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed unexpectedly")
		}
		return ev
	case <-time.After(d):
		t.Fatalf("timed out waiting for event after %s", d)
	}
	return RuntimeEvent{}
}

func TestEventBus_SingleSubscriberReceivesEvent(t *testing.T) {
	bus := NewEventBus()
	ch, unsub := bus.Subscribe(4)
	defer unsub()

	bus.Publish(RuntimeEvent{Type: EventTurnStarted, Prompt: "hi"})

	got := recvOrTimeout(t, ch, 2*time.Second)
	if got.Type != EventTurnStarted {
		t.Errorf("Type = %v, want EventTurnStarted", got.Type)
	}
	if got.Prompt != "hi" {
		t.Errorf("Prompt = %q, want %q", got.Prompt, "hi")
	}
}

func TestEventBus_MultipleSubscribersAllReceive(t *testing.T) {
	bus := NewEventBus()
	chs := make([]<-chan RuntimeEvent, 0, 3)
	for i := 0; i < 3; i++ {
		c, unsub := bus.Subscribe(4)
		defer unsub()
		chs = append(chs, c)
	}

	want := RuntimeEvent{Type: EventQueueDrained}
	bus.Publish(want)

	for i, c := range chs {
		got := recvOrTimeout(t, c, 2*time.Second)
		if got.Type != want.Type {
			t.Errorf("subscriber %d: Type = %v, want %v", i, got.Type, want.Type)
		}
	}
}

func TestEventBus_SlowSubscriberDropsWithoutBlockingOthers(t *testing.T) {
	bus := NewEventBus()

	slow, unsubA := bus.Subscribe(1)
	defer unsubA()
	fast, unsubB := bus.Subscribe(128)
	defer unsubB()

	const N = 64

	// Drain fast in the background so Publish never blocks on it.
	var fastCount int32
	doneCount := make(chan struct{})
	go func() {
		deadline := time.After(2 * time.Second)
		for {
			select {
			case <-fast:
				if atomic.AddInt32(&fastCount, 1) == N {
					close(doneCount)
					return
				}
			case <-deadline:
				return
			}
		}
	}()

	// Publish must complete in well under our timeout, even though slow can't keep up.
	publishDone := make(chan struct{})
	go func() {
		for i := 0; i < N; i++ {
			bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
		}
		close(publishDone)
	}()

	select {
	case <-publishDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on slow subscriber")
	}

	select {
	case <-doneCount:
	case <-time.After(2 * time.Second):
		t.Fatalf("fast subscriber only received %d/%d", atomic.LoadInt32(&fastCount), N)
	}

	// Slow subscriber should have received at least one but fewer than N.
	// We can't strictly assert drops without knowing the impl; just drain non-blockingly.
	drained := 0
drain:
	for {
		select {
		case <-slow:
			drained++
		default:
			break drain
		}
	}
	if drained >= N {
		t.Errorf("slow subscriber received %d events; expected drops with buffer=1 and N=%d", drained, N)
	}
}

func TestEventBus_UnsubscribeClosesChannel(t *testing.T) {
	bus := NewEventBus()
	ch, unsub := bus.Subscribe(4)

	unsub()

	done := make(chan struct{})
	go func() {
		//nolint:revive // intentional drain loop until channel close
		for range ch {
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("range over channel did not terminate after unsubscribe")
	}
}

func TestEventBus_UnsubscribeIdempotent(t *testing.T) {
	bus := NewEventBus()
	_, unsub := bus.Subscribe(1)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second unsubscribe panicked: %v", r)
		}
	}()

	unsub()
	unsub()
}

func TestEventBus_PublishWithZeroSubscribersIsNoOp(t *testing.T) {
	bus := NewEventBus()

	done := make(chan struct{})
	go func() {
		bus.Publish(RuntimeEvent{Type: EventStopped})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish with zero subscribers blocked")
	}
}

func TestEventBus_ConcurrentPublishSubscribeUnsubscribe(t *testing.T) {
	bus := NewEventBus()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Publishers.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					bus.Publish(RuntimeEvent{Type: EventTurnCompleted})
				}
			}
		}()
	}

	// Subscriber churn.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					ch, unsub := bus.Subscribe(8)
					// drain a few, ignoring drops
					timeout := time.After(20 * time.Millisecond)
				loop:
					for {
						select {
						case <-ch:
						case <-timeout:
							break loop
						}
					}
					unsub()
				}
			}
		}()
	}

	time.Sleep(300 * time.Millisecond)
	close(stop)

	doneAll := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneAll)
	}()

	select {
	case <-doneAll:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent goroutines did not return in time")
	}
}

func TestEventBus_EventPayloadFieldsPreserved(t *testing.T) {
	bus := NewEventBus()
	ch, unsub := bus.Subscribe(1)
	defer unsub()

	msg := &protocol.Message{Type: "assistant", UUID: "u-1", SessionID: "s-1"}
	res := &protocol.ResultMessage{Type: "result", Subtype: "success", DurationMs: 42}
	wantErr := errors.New("boom")

	want := RuntimeEvent{
		Type:    EventTurnFailed,
		Message: msg,
		Prompt:  "do the thing",
		Result:  res,
		Error:   wantErr,
	}
	bus.Publish(want)

	got := recvOrTimeout(t, ch, 2*time.Second)
	if got.Type != want.Type {
		t.Errorf("Type = %v, want %v", got.Type, want.Type)
	}
	if got.Message != msg {
		t.Errorf("Message pointer not preserved")
	}
	if got.Prompt != want.Prompt {
		t.Errorf("Prompt = %q, want %q", got.Prompt, want.Prompt)
	}
	if got.Result != res {
		t.Errorf("Result pointer not preserved")
	}
	if !errors.Is(got.Error, wantErr) {
		t.Errorf("Error = %v, want %v", got.Error, wantErr)
	}
}

// --- QUM-472: dropped-event telemetry ---------------------------------------
//
// The following tests drive the telemetry API for tracking events that are
// dropped due to slow subscribers. The fix shape (Option 1) is:
//
//   - SubscribeNamed(name, buffer) tags a subscriber for telemetry.
//   - Subscribe(buffer) keeps its existing signature; internally it delegates
//     to SubscribeNamed("", buffer).
//   - DroppedCounts() returns map[string]uint64 keyed by subscriber name when
//     the name is non-empty, else by a stable synthetic key fmt.Sprintf("#%d", id).
//   - On the FIRST drop encountered (process-wide, or per-bus — impl's choice
//     so long as it doesn't spam) a slog.LevelWarn record is emitted whose
//     message references the eventbus drop and whose attrs include the
//     subscriber name.
//   - Unsubscribing drops the subscriber's entry from DroppedCounts (the
//     counter is owned by the subscriber registration; this is intentional —
//     callers that want to retain final counts can read DroppedCounts before
//     calling unsub).

func TestEventBus_DroppedCountsRecordsOverflow(t *testing.T) {
	bus := NewEventBus()
	_, unsub := bus.SubscribeNamed("slow", 1)
	defer unsub()

	for i := 0; i < 10; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}

	// buffer=1 admits the first publish; the remaining 9 drop because no one
	// drains the channel. Go's buffered-channel + non-blocking-select semantics
	// make this exact.
	got := bus.DroppedCounts()["slow"]
	if got != 9 {
		t.Errorf("DroppedCounts()[%q] = %d, want 9", "slow", got)
	}
}

func TestEventBus_AnonymousSubscriberTrackedWithSyntheticKey(t *testing.T) {
	bus := NewEventBus()
	_, unsub := bus.Subscribe(1)
	defer unsub()

	for i := 0; i < 5; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}

	counts := bus.DroppedCounts()
	syntheticKeyRE := regexp.MustCompile(`^#\d+$`)

	var matched []string
	for k := range counts {
		if syntheticKeyRE.MatchString(k) {
			matched = append(matched, k)
		}
	}
	if len(matched) != 1 {
		t.Fatalf("DroppedCounts() = %v, want exactly one synthetic key matching %q", counts, syntheticKeyRE)
	}
	if got := counts[matched[0]]; got != 4 {
		t.Errorf("DroppedCounts()[%q] = %d, want 4", matched[0], got)
	}
}

func TestEventBus_DroppedCountsZeroWhenDrained(t *testing.T) {
	bus := NewEventBus()
	ch, unsub := bus.SubscribeNamed("fast", 4)
	defer unsub()

	const N = 100
	done := make(chan struct{})
	var received int32
	go func() {
		for {
			select {
			case <-ch:
				if atomic.AddInt32(&received, 1) == N {
					close(done)
					return
				}
			case <-time.After(2 * time.Second):
				return
			}
		}
	}()

	for i := 0; i < N; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("fast subscriber only received %d/%d", atomic.LoadInt32(&received), N)
	}

	if got := bus.DroppedCounts()["fast"]; got != 0 {
		t.Errorf("DroppedCounts()[%q] = %d, want 0", "fast", got)
	}
}

func TestEventBus_DroppedCountsIsolatedPerSubscriber(t *testing.T) {
	bus := NewEventBus()

	_, unsubSlow := bus.SubscribeNamed("slow", 1)
	defer unsubSlow()
	fast, unsubFast := bus.SubscribeNamed("fast", 128)
	defer unsubFast()

	const N = 64

	var fastCount int32
	doneFast := make(chan struct{})
	go func() {
		deadline := time.After(2 * time.Second)
		for {
			select {
			case <-fast:
				if atomic.AddInt32(&fastCount, 1) == N {
					close(doneFast)
					return
				}
			case <-deadline:
				return
			}
		}
	}()

	for i := 0; i < N; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}

	select {
	case <-doneFast:
	case <-time.After(2 * time.Second):
		t.Fatalf("fast subscriber only received %d/%d", atomic.LoadInt32(&fastCount), N)
	}

	counts := bus.DroppedCounts()
	if counts["slow"] == 0 {
		t.Errorf("DroppedCounts()[%q] = 0, want > 0", "slow")
	}
	if counts["fast"] != 0 {
		t.Errorf("DroppedCounts()[%q] = %d, want 0", "fast", counts["fast"])
	}
}

func TestEventBus_DroppedCountsAfterUnsubscribeIsRemoved(t *testing.T) {
	// Document: unsubscribe drops the counter. Read DroppedCounts before unsub
	// if you want to retain final counts.
	bus := NewEventBus()
	_, unsub := bus.SubscribeNamed("slow", 1)

	for i := 0; i < 5; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}
	if bus.DroppedCounts()["slow"] == 0 {
		t.Fatalf("expected drops to be recorded before unsubscribe")
	}

	unsub()

	if _, ok := bus.DroppedCounts()["slow"]; ok {
		t.Errorf("DroppedCounts() still contains key %q after unsubscribe", "slow")
	}
}

// captureHandler is a minimal in-memory slog.Handler that records every
// Record it receives. Used to assert warn-once behavior.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

// TestEventBus_FirstDropWarns asserts the first-drop warn still fires under
// the new rate+burst-limited policy (QUM-681). The exact emission count is
// no longer pinned to one — under bursty drop conditions the policy may emit
// additional warns when the burst threshold is crossed — but at minimum the
// first drop must always produce a warn record carrying the subscriber name.
func TestEventBus_FirstDropWarns(t *testing.T) {
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	bus := NewEventBus()
	_, unsub := bus.SubscribeNamed("slow", 1)
	defer unsub()

	for i := 0; i < 50; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	var warns []slog.Record
	for _, r := range h.records {
		if r.Level == slog.LevelWarn {
			warns = append(warns, r)
		}
	}
	if len(warns) < 1 {
		t.Fatalf("got %d warn records, want at least 1", len(warns))
	}

	rec := warns[0]
	if !strings.Contains(strings.ToLower(rec.Message), "drop") ||
		!strings.Contains(strings.ToLower(rec.Message), "eventbus") {
		t.Errorf("warn message = %q, want it to mention 'eventbus' and 'drop'", rec.Message)
	}

	var foundName bool
	rec.Attrs(func(a slog.Attr) bool {
		if strings.Contains(strings.ToLower(a.Key), "name") ||
			strings.Contains(strings.ToLower(a.Key), "subscriber") {
			if a.Value.String() == "slow" {
				foundName = true
				return false
			}
		}
		// Some impls may use key "slow" directly — accept any attr whose value is "slow".
		if a.Value.String() == "slow" {
			foundName = true
			return false
		}
		return true
	})
	if !foundName {
		t.Errorf("warn record attrs did not include subscriber name %q; record=%+v", "slow", rec)
	}
}

// --- QUM-681: rate+burst-limited drop warn + telemetry snapshot ----------

// installCaptureHandler swaps slog.Default with a captureHandler for the
// duration of the test and returns the handler so the test can inspect
// emitted records.
func installCaptureHandler(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func warnRecords(h *captureHandler) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Level == slog.LevelWarn {
			out = append(out, r.Clone())
		}
	}
	return out
}

// TestEventBus_DropWarn_BurstThresholdEmitsMultiple verifies that with a
// frozen clock (so the interval gate never fires) the burst-count gate
// emits multiple warns when many drops accumulate — but far fewer warns
// than there are drops.
func TestEventBus_DropWarn_BurstThresholdEmitsMultiple(t *testing.T) {
	h := installCaptureHandler(t)

	bus := NewEventBus()
	t0 := time.Unix(1_700_000_000, 0)
	bus.setNow(func() time.Time { return t0 })

	_, unsub := bus.SubscribeNamed("slow", 1)
	defer unsub()

	const N = 25
	for i := 0; i < N; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}

	warns := warnRecords(h)
	if len(warns) < 2 {
		t.Fatalf("got %d warns, want >= 2 (burst threshold should fire multiple times for %d drops)", len(warns), N)
	}
	if len(warns) >= N {
		t.Fatalf("got %d warns for %d publishes, want strictly fewer (rate-limited)", len(warns), N)
	}
}

// TestEventBus_DropWarn_IntervalRateLimited verifies the time-axis gate:
// with the burst threshold uncrossed, a second warn fires only after
// dropWarnInterval has elapsed (per the frozen, then advanced, clock).
func TestEventBus_DropWarn_IntervalRateLimited(t *testing.T) {
	h := installCaptureHandler(t)

	bus := NewEventBus()
	now := time.Unix(1_700_000_000, 0)
	bus.setNow(func() time.Time { return now })

	_, unsub := bus.SubscribeNamed("slow", 1)
	defer unsub()

	// First burst: 5 publishes -> 4 drops. Burst threshold (10) not crossed;
	// only the first-drop warn should fire.
	for i := 0; i < 5; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}
	got := len(warnRecords(h))
	if got != 1 {
		t.Fatalf("after first burst got %d warns, want 1", got)
	}

	// Advance the clock past dropWarnInterval and emit another small burst.
	now = now.Add(dropWarnInterval + time.Second)
	for i := 0; i < 5; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}
	got = len(warnRecords(h))
	if got != 2 {
		t.Fatalf("after interval-elapsed second burst got %d warns, want 2", got)
	}
}

// TestEventBus_DropWarn_SingleBlip verifies a one-event blip emits exactly
// one warn and that quiescence past dropClearInterval does not produce more.
func TestEventBus_DropWarn_SingleBlip(t *testing.T) {
	h := installCaptureHandler(t)

	bus := NewEventBus()
	now := time.Unix(1_700_000_000, 0)
	bus.setNow(func() time.Time { return now })

	_, unsub := bus.SubscribeNamed("slow", 1)
	defer unsub()

	// 2 publishes -> 1 drop.
	for i := 0; i < 2; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}
	got := len(warnRecords(h))
	if got != 1 {
		t.Fatalf("blip got %d warns, want exactly 1", got)
	}

	// Advance past clear interval; no further drops occur, so no further warns.
	now = now.Add(dropClearInterval + time.Second)
	got = len(warnRecords(h))
	if got != 1 {
		t.Fatalf("after quiescent advance got %d warns, want still 1", got)
	}
}

// TestEventBus_DropTelemetry_Snapshot verifies the structured snapshot API
// returns cumulative count and last-drop timestamp anchored to the bus clock.
func TestEventBus_DropTelemetry_Snapshot(t *testing.T) {
	bus := NewEventBus()
	t0 := time.Unix(1_700_000_000, 0)
	bus.setNow(func() time.Time { return t0 })

	_, unsub := bus.SubscribeNamed("slow", 1)
	defer unsub()

	for i := 0; i < 10; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}

	snap := bus.DropTelemetry()
	tel, ok := snap["slow"]
	if !ok {
		t.Fatalf("DropTelemetry() missing key %q; snapshot=%v", "slow", snap)
	}
	if tel.Cumulative != 9 {
		t.Errorf("DropTelemetry()[%q].Cumulative = %d, want 9", "slow", tel.Cumulative)
	}
	if !tel.LastDropAt.Equal(t0) {
		t.Errorf("DropTelemetry()[%q].LastDropAt = %v, want %v", "slow", tel.LastDropAt, t0)
	}
}

// --- QUM-669: publisher-side Seq stamping + subscriber-gap detection -----
//
// Per docs/designs/qum-669-viewport-wedge-recovery.md §2.1, the EventBus
// stamps each RuntimeEvent with a monotonic 1-indexed Seq number BEFORE
// fanning out to subscribers. This makes drops detectable purely from a gap
// in received seq numbers on each subscriber, with no cross-subscriber
// bookkeeping. CurrentSeq() exposes the last-stamped seq for callers (e.g.
// the TUIAdapter's lastSeq baseline after an Observe swap).

// TestEventBus_PublishStampsMonotonicSeq verifies the publisher-side
// seq-stamping invariant: every received RuntimeEvent carries a Seq value
// that is 1-indexed and monotonically increasing across the publish stream,
// and CurrentSeq() reflects the last-stamped value.
func TestEventBus_PublishStampsMonotonicSeq(t *testing.T) {
	bus := NewEventBus()
	ch, unsub := bus.SubscribeNamed("seq-probe", 64)
	defer unsub()

	const N = 5
	for i := 0; i < N; i++ {
		bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	}

	for i := 1; i <= N; i++ {
		ev := recvOrTimeout(t, ch, 2*time.Second)
		if ev.Seq != uint64(i) {
			t.Errorf("event %d: Seq = %d, want %d", i, ev.Seq, i)
		}
	}

	if got := bus.CurrentSeq(); got != uint64(N) {
		t.Errorf("CurrentSeq() = %d, want %d", got, N)
	}
}

// TestEventBus_CurrentSeq_FreshBusIsZero verifies that a freshly-constructed
// bus reports CurrentSeq() == 0 before any Publish has stamped a sequence.
// QUM-669.
func TestEventBus_CurrentSeq_FreshBusIsZero(t *testing.T) {
	bus := NewEventBus()
	if got := bus.CurrentSeq(); got != 0 {
		t.Errorf("CurrentSeq() on fresh bus = %d, want 0", got)
	}
}

// TestEventBus_MultipleSubscribersSeeSameSeqForSamePublish verifies that the
// publisher stamps Seq once per Publish call and fans the identical Seq value
// out to every subscriber. QUM-669.
func TestEventBus_MultipleSubscribersSeeSameSeqForSamePublish(t *testing.T) {
	bus := NewEventBus()
	chA, unsubA := bus.SubscribeNamed("a", 4)
	defer unsubA()
	chB, unsubB := bus.SubscribeNamed("b", 4)
	defer unsubB()

	bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
	bus.Publish(RuntimeEvent{Type: EventProtocolMessage})

	for i := 1; i <= 2; i++ {
		a := recvOrTimeout(t, chA, 2*time.Second)
		b := recvOrTimeout(t, chB, 2*time.Second)
		if a.Seq != uint64(i) {
			t.Errorf("subscriber a event %d: Seq = %d, want %d", i, a.Seq, i)
		}
		if b.Seq != uint64(i) {
			t.Errorf("subscriber b event %d: Seq = %d, want %d", i, b.Seq, i)
		}
		if a.Seq != b.Seq {
			t.Errorf("event %d: subscriber a Seq=%d, subscriber b Seq=%d; want equal",
				i, a.Seq, b.Seq)
		}
	}
}

// TestEventBus_PublishWithSeq_DeterministicGap exercises the test-only
// publishWithSeq seam to produce an observable gap in the Seq stream without
// the flaky slow-consumer race. Subscriber sees the explicit seq values in
// monotonic order with a clear gap. QUM-669.
func TestEventBus_PublishWithSeq_DeterministicGap(t *testing.T) {
	bus := NewEventBus()
	ch, unsub := bus.SubscribeNamed("probe", 16)
	defer unsub()

	bus.PublishWithSeq(RuntimeEvent{Type: EventProtocolMessage}, 1)
	bus.PublishWithSeq(RuntimeEvent{Type: EventProtocolMessage}, 2)
	bus.PublishWithSeq(RuntimeEvent{Type: EventProtocolMessage}, 10)

	wantSeqs := []uint64{1, 2, 10}
	for i, want := range wantSeqs {
		ev := recvOrTimeout(t, ch, 2*time.Second)
		if ev.Seq != want {
			t.Errorf("event %d: Seq = %d, want %d", i, ev.Seq, want)
		}
	}
}

// TestEventBus_ConcurrentPublishStampOrderMatchesDeliveryOrder guards the
// QUM-669 invariant that publishMu serializes "stamp Seq" + "fanout" so a
// subscriber observes Seq values in strictly ascending order even under
// concurrent publishers. Pre-publishMu, two goroutines could stamp Seq=N and
// Seq=N+1 but deliver them in reverse order, producing a backwards-Seq on
// the wire that the TUIAdapter would underflow into a uint64-max "missing"
// count. The drain-row-inject e2e tripped this in the wild.
func TestEventBus_ConcurrentPublishStampOrderMatchesDeliveryOrder(t *testing.T) {
	bus := NewEventBus()
	const buf = 4096
	ch, unsub := bus.SubscribeNamed("order-probe", buf)
	defer unsub()

	const workers = 8
	const perWorker = 128
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				bus.Publish(RuntimeEvent{Type: EventProtocolMessage})
			}
		}()
	}
	wg.Wait()

	const total = workers * perWorker
	var prev uint64
	for i := 0; i < total; i++ {
		ev := recvOrTimeout(t, ch, 2*time.Second)
		if ev.Seq <= prev {
			t.Fatalf("event %d: Seq = %d, prev = %d — out-of-order delivery violates the publishMu invariant", i, ev.Seq, prev)
		}
		prev = ev.Seq
	}
}

// Compile-time assertions that the required RuntimeEventType constants exist
// and are distinct values.
var _ = [...]RuntimeEventType{
	EventProtocolMessage,
	EventTurnStarted,
	EventTurnCompleted,
	EventTurnFailed,
	EventInterrupted,
	EventQueueDrained,
	EventStopped,
}
