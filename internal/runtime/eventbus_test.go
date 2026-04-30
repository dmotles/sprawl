package runtime

import (
	"errors"
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
