package supervisor

import (
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
)

// QUM-602: the Real supervisor must expose a SetBackendFaultEmitter seam
// (mirroring SetProgressEmitter) that the TUI installs, and a per-runtime
// fault-subscriber goroutine that fans EventBackendFaulted events from the
// runtime's EventBus out to that emitter (mirroring runActivitySubscriber).

type recordedFault struct {
	agent, class, reason, nextAction string
}

type recordedFaults struct {
	mu     sync.Mutex
	events []recordedFault
}

func (r *recordedFaults) push(agent, class, reason, nextAction string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedFault{agent, class, reason, nextAction})
}

func (r *recordedFaults) snap() []recordedFault {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedFault, len(r.events))
	copy(out, r.events)
	return out
}

func (r *recordedFaults) waitFor(n int, d time.Duration) []recordedFault {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		got := r.snap()
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return r.snap()
}

func TestRealSetBackendFaultEmitter_StoresFn(t *testing.T) {
	r := &Real{}
	rec := &recordedFaults{}

	// Install + clear must both be idempotent and panic-free, mirroring
	// SetProgressEmitter contract (QUM-497).
	r.SetBackendFaultEmitter(rec.push)
	r.SetBackendFaultEmitter(nil)
	r.SetBackendFaultEmitter(rec.push)
}

func TestRunFaultSubscriber_ForwardsEventBackendFaultedToEmitter(t *testing.T) {
	bus := runtimepkg.NewEventBus()
	rec := &recordedFaults{}

	stop := runFaultSubscriber(bus, "alice", rec.push, "test-fault")
	defer stop()

	// Unrelated events must be ignored.
	bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnStarted})
	bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventQueueDrained})

	// Fault event must be forwarded with the agent name + class + hint
	// preserved, and reason populated from the underlying error.
	bus.Publish(runtimepkg.RuntimeEvent{
		Type:            runtimepkg.EventBackendFaulted,
		FaultClass:      "HangTimeout",
		FaultNextAction: "hint",
		Error:           backend.ErrHangTimeout,
	})

	got := rec.waitFor(1, time.Second)
	if len(got) != 1 {
		t.Fatalf("emitter received %d events, want 1; got=%+v", len(got), got)
	}
	ev := got[0]
	if ev.agent != "alice" {
		t.Errorf("agent = %q, want %q", ev.agent, "alice")
	}
	if ev.class != "HangTimeout" {
		t.Errorf("class = %q, want %q", ev.class, "HangTimeout")
	}
	if ev.nextAction != "hint" {
		t.Errorf("nextAction = %q, want %q", ev.nextAction, "hint")
	}
	if ev.reason == "" || ev.reason != backend.ErrHangTimeout.Error() {
		t.Errorf("reason = %q, want %q", ev.reason, backend.ErrHangTimeout.Error())
	}
}

func TestRunFaultSubscriber_IgnoresUnrelatedEvents(t *testing.T) {
	bus := runtimepkg.NewEventBus()
	rec := &recordedFaults{}

	stop := runFaultSubscriber(bus, "bob", rec.push, "ignore-fault")
	defer stop()

	bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventInterrupted})
	bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnCompleted})
	bus.Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventStopped})

	// Give the goroutine a chance to (incorrectly) emit.
	time.Sleep(50 * time.Millisecond)
	if got := rec.snap(); len(got) != 0 {
		t.Fatalf("unrelated events leaked through fault subscriber: %+v", got)
	}
}
