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

func TestEventBus_WarnsOnceOnFirstDrop(t *testing.T) {
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
	if len(warns) != 1 {
		t.Fatalf("got %d warn records, want exactly 1", len(warns))
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
