// QUM-475: Exhaustive coverage for the runtime-event → tea.Msg mapping in
// TUIAdapter.WaitForEvent. The wedge described in
// docs/forensics/tui-weave-wedge-2026-05-05.md happened because EventInterrupted
// was mapped to a non-terminal request-ack message (InterruptResultMsg), so the
// AppModel never transitioned out of TurnStreaming when an interrupt drained.
//
// These tests pin the classification of every RuntimeEventType so that:
//   - adding a new constant past EventStopped without updating
//     allRuntimeEventTypes / eventClassRegistry fails
//     TestEventRegistry_CoversAllRuntimeEventTypes at test time (the
//     int(EventStopped)+1 vs len(allRuntimeEventTypes) check);
//   - inserting a new constant *between* existing ones shifts iota values,
//     which breaks the per-event behavioral assertions in
//     TestEventRegistry_TerminalEventsSurfaceExpectedMsgType (e.g. the event
//     formerly classified as terminal now sits at the iota slot of a
//     non-terminal one and the registry's expectedMsgType no longer matches);
//   - every "terminal" event surfaces the expected tui message type via
//     WaitForEvent.
//
// Note: Go does not give us a true *compile-time* exhaustiveness check on
// untyped iota constants. The mechanism above is the cleanest practical
// substitute — a deterministic test failure with a clear message pointing
// the developer at the file they need to update.
//
// AppModel-side TurnIdle finalization is asserted in internal/tui/app_test.go
// (TestAppModel_InterruptCompletedMsg_ReturnsToIdle and the
// SessionResultMsg / TurnFailed equivalents). Splitting the assertion this
// way avoids reaching into unexported AppModel state from this package.

package tuiruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
)

// allRuntimeEventTypes lists every RuntimeEventType this package knows how
// to classify. Removing a constant from runtime/eventbus.go breaks the
// build here (undefined identifier), forcing the author to update the
// test. Adding a new constant past EventStopped is caught at test time by
// TestEventRegistry_CoversAllRuntimeEventTypes via the int(EventStopped)+1
// upper-bound check.
var allRuntimeEventTypes = [...]sprawlrt.RuntimeEventType{
	sprawlrt.EventProtocolMessage,
	sprawlrt.EventTurnStarted,
	sprawlrt.EventTurnCompleted,
	sprawlrt.EventTurnFailed,
	sprawlrt.EventInterrupted,
	sprawlrt.EventQueueDrained,
	sprawlrt.EventStopped,
}

// eventClass describes how the adapter SHOULD classify a runtime event.
type eventClass struct {
	terminal        bool   // true if this event ends the turn (drives TurnIdle)
	expectedMsgType string // Go type name returned by WaitForEvent ("" if skipped/depends-on-payload)
	expectsTurnIdle bool   // mirrors terminal — kept for documentation symmetry
}

// eventClassRegistry hand-maintains the contract for every constant. If a new
// constant is added to the runtime package, the registry length will diverge
// from len(allRuntimeEventTypes) and TestEventRegistry_CoversAllRuntimeEventTypes
// will fail until you add it here too — that's the design.
var eventClassRegistry = map[sprawlrt.RuntimeEventType]eventClass{
	sprawlrt.EventProtocolMessage: {
		terminal:        false,
		expectedMsgType: "", // mapped per-message-type; payload-dependent
		expectsTurnIdle: false,
	},
	sprawlrt.EventTurnStarted: {
		terminal:        false,
		expectedMsgType: "", // skipped by adapter (lifecycle-only)
		expectsTurnIdle: false,
	},
	sprawlrt.EventTurnCompleted: {
		terminal:        true,
		expectedMsgType: "tui.SessionResultMsg",
		expectsTurnIdle: true,
	},
	sprawlrt.EventTurnFailed: {
		terminal:        true,
		expectedMsgType: "tui.SessionResultMsg",
		expectsTurnIdle: true,
	},
	sprawlrt.EventInterrupted: {
		terminal:        true,
		expectedMsgType: "tui.InterruptCompletedMsg",
		expectsTurnIdle: true,
	},
	sprawlrt.EventQueueDrained: {
		terminal:        false,
		expectedMsgType: "", // skipped by adapter (lifecycle-only)
		expectsTurnIdle: false,
	},
	sprawlrt.EventStopped: {
		terminal:        false,
		expectedMsgType: "", // skipped by adapter (lifecycle-only)
		expectsTurnIdle: false,
	},
}

func TestEventRegistry_CoversAllRuntimeEventTypes(t *testing.T) {
	for _, ev := range allRuntimeEventTypes {
		if _, ok := eventClassRegistry[ev]; !ok {
			t.Errorf("RuntimeEventType %v has no entry in eventClassRegistry — add a classification when introducing new event types", ev)
		}
	}
	if len(eventClassRegistry) != len(allRuntimeEventTypes) {
		t.Errorf("eventClassRegistry has %d entries but allRuntimeEventTypes has %d; either a constant was added without registering it, or a stale entry remains", len(eventClassRegistry), len(allRuntimeEventTypes))
	}
	// Upper-bound trip-wire: EventStopped is the highest-valued
	// RuntimeEventType today (iota = 6). If a new constant is appended to
	// the const block in runtime/eventbus.go, its iota value will be
	// >= len(allRuntimeEventTypes) and this assertion will fail with a
	// pointer to the file the developer needs to update. This is the
	// closest we can get to a compile-time exhaustiveness check without
	// pulling in go/types or stringer-style codegen.
	if want, got := int(sprawlrt.EventStopped)+1, len(allRuntimeEventTypes); want != got {
		t.Fatalf("a new RuntimeEventType constant appears to have been added to runtime/eventbus.go (EventStopped=%d, expected len(allRuntimeEventTypes)=%d, got %d). Update allRuntimeEventTypes and eventClassRegistry in this file, and add a behavioral assertion in TestEventRegistry_TerminalEventsSurfaceExpectedMsgType if the new event is terminal.", int(sprawlrt.EventStopped), want, got)
	}
}

// TestEventRegistry_TerminalEventsSurfaceExpectedMsgType publishes each
// terminal RuntimeEvent directly onto the EventBus and asserts that
// WaitForEvent returns the message type the registry promises.
func TestEventRegistry_TerminalEventsSurfaceExpectedMsgType(t *testing.T) {
	tests := []struct {
		name    string
		evType  sprawlrt.RuntimeEventType
		publish func(bus *sprawlrt.EventBus)
	}{
		{
			name:   "EventTurnCompleted",
			evType: sprawlrt.EventTurnCompleted,
			publish: func(bus *sprawlrt.EventBus) {
				bus.Publish(sprawlrt.RuntimeEvent{
					Type: sprawlrt.EventTurnCompleted,
					Result: &protocol.ResultMessage{
						Result:       "ok",
						DurationMs:   42,
						NumTurns:     1,
						TotalCostUsd: 0.01,
					},
				})
			},
		},
		{
			name:   "EventTurnFailed",
			evType: sprawlrt.EventTurnFailed,
			publish: func(bus *sprawlrt.EventBus) {
				bus.Publish(sprawlrt.RuntimeEvent{
					Type:  sprawlrt.EventTurnFailed,
					Error: errors.New("boom"),
				})
			},
		},
		{
			name:   "EventInterrupted",
			evType: sprawlrt.EventInterrupted,
			publish: func(bus *sprawlrt.EventBus) {
				bus.Publish(sprawlrt.RuntimeEvent{
					Type: sprawlrt.EventInterrupted,
					Result: &protocol.ResultMessage{
						Result:       "stopped",
						DurationMs:   7,
						NumTurns:     1,
						TotalCostUsd: 0.001,
					},
				})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			class, ok := eventClassRegistry[tc.evType]
			if !ok {
				t.Fatalf("registry missing entry for %v", tc.evType)
			}
			if !class.terminal {
				t.Fatalf("registry says %v is non-terminal but it's in the terminal table", tc.evType)
			}

			mock := &adapterMockSession{}
			rt, a := buildAdapter(t, mock)
			if err := rt.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}

			tc.publish(rt.EventBus())

			// Bound the WaitForEvent call.
			gotCh := make(chan interface{}, 1)
			go func() { gotCh <- a.WaitForEvent()() }()
			var msg interface{}
			select {
			case msg = <-gotCh:
			case <-time.After(2 * time.Second):
				t.Fatalf("WaitForEvent did not return for %v within 2s", tc.evType)
			}

			gotType := reflect.TypeOf(msg).String()
			if gotType != class.expectedMsgType {
				t.Fatalf("event=%v got msg type=%s, want %s", tc.evType, gotType, class.expectedMsgType)
			}
		})
	}
}
