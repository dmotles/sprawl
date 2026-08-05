// Unit coverage for the shared EventBus → tea.Msg translation used by both
// the per-child ChildStreamAdapter and the bridge tuiruntime.TUIAdapter
// (QUM-446). Behavioral assertions against the adapters themselves live in
// internal/tuiruntime/event_mapping_exhaustive_test.go and
// internal/tui/adapter_eof_isolation_test.go — these tests pin the helper's
// pure-function semantics so a refactor to either adapter cannot quietly
// drift the per-event mapping.

package tui

import (
	"errors"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/protocol"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
)

func TestTranslateRuntimeEvent_ProtocolMessage_NilSkipped(t *testing.T) {
	got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: nil,
	}, InterruptedAsResult)
	if got != nil {
		t.Fatalf("expected nil (skip) for nil Message, got %T %+v", got, got)
	}
}

func TestTranslateRuntimeEvent_ProtocolMessage_ResultEnvelopeSkipped(t *testing.T) {
	got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: &protocol.Message{Type: "result"},
	}, InterruptedAsResult)
	if got != nil {
		t.Fatalf("expected nil (skip) for protocol result envelope, got %T %+v", got, got)
	}
}

func TestTranslateRuntimeEvent_TurnCompleted_PopulatesSessionResultMsg(t *testing.T) {
	ev := sprawlrt.RuntimeEvent{
		Type: sprawlrt.EventTurnCompleted,
		Result: &protocol.ResultMessage{
			Result:       "ok",
			IsError:      false,
			DurationMs:   42,
			NumTurns:     2,
			TotalCostUsd: 0.5,
		},
	}
	got := TranslateRuntimeEvent(ev, InterruptedAsResult)
	want := SessionResultMsg{
		Result:       "ok",
		IsError:      false,
		DurationMs:   42,
		NumTurns:     2,
		TotalCostUsd: 0.5,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTranslateRuntimeEvent_TurnCompleted_NilResultYieldsZeroValue(t *testing.T) {
	got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{
		Type: sprawlrt.EventTurnCompleted,
	}, InterruptedAsResult)
	if _, ok := got.(SessionResultMsg); !ok {
		t.Fatalf("expected SessionResultMsg, got %T", got)
	}
}

func TestTranslateRuntimeEvent_TurnFailed_SurfaceErrorString(t *testing.T) {
	got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{
		Type:  sprawlrt.EventTurnFailed,
		Error: errors.New("boom"),
	}, InterruptedAsResult)
	msg, ok := got.(SessionResultMsg)
	if !ok {
		t.Fatalf("expected SessionResultMsg, got %T", got)
	}
	if !msg.IsError || msg.Result != "boom" {
		t.Fatalf("got %#v, want IsError=true Result=\"boom\"", msg)
	}
}

func TestTranslateRuntimeEvent_TurnFailed_NilErrorEmptyString(t *testing.T) {
	got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{
		Type: sprawlrt.EventTurnFailed,
	}, InterruptedAsResult)
	msg, ok := got.(SessionResultMsg)
	if !ok {
		t.Fatalf("expected SessionResultMsg, got %T", got)
	}
	if !msg.IsError || msg.Result != "" {
		t.Fatalf("got %#v, want IsError=true Result=\"\"", msg)
	}
}

func TestTranslateRuntimeEvent_Interrupted_DelegatesToCallback_AsResult(t *testing.T) {
	got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{
		Type: sprawlrt.EventInterrupted,
		Result: &protocol.ResultMessage{
			Result: "stopped",
		},
	}, InterruptedAsResult)
	msg, ok := got.(InterruptResultMsg)
	if !ok {
		t.Fatalf("expected InterruptResultMsg (child semantics), got %T", got)
	}
	if msg.Err != nil {
		t.Fatalf("expected nil Err, got %v", msg.Err)
	}
}

func TestTranslateRuntimeEvent_Interrupted_DelegatesToCallback_AsCompleted(t *testing.T) {
	ev := sprawlrt.RuntimeEvent{
		Type: sprawlrt.EventInterrupted,
		Result: &protocol.ResultMessage{
			Result:       "stopped",
			DurationMs:   7,
			NumTurns:     1,
			TotalCostUsd: 0.001,
		},
	}
	got := TranslateRuntimeEvent(ev, InterruptedAsCompleted)
	want := InterruptCompletedMsg{
		Result:       "stopped",
		DurationMs:   7,
		NumTurns:     1,
		TotalCostUsd: 0.001,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTranslateRuntimeEvent_Interrupted_NilResult_AsCompleted(t *testing.T) {
	got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{
		Type: sprawlrt.EventInterrupted,
	}, InterruptedAsCompleted)
	if _, ok := got.(InterruptCompletedMsg); !ok {
		t.Fatalf("expected InterruptCompletedMsg, got %T", got)
	}
}

func TestTranslateRuntimeEvent_UserMessageConsumed(t *testing.T) {
	got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{
		Type: sprawlrt.EventUserMessageConsumed,
		UUID: "uuid-1",
	}, InterruptedAsResult)
	want := UserMessageConsumedMsg{UUID: "uuid-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTranslateRuntimeEvent_UserMessageCancelled(t *testing.T) {
	got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{
		Type: sprawlrt.EventUserMessageCancelled,
		UUID: "uuid-2",
	}, InterruptedAsResult)
	want := UserMessageCancelledMsg{UUID: "uuid-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// QUM-838: on a now-write (send-all-now) the runtime publishes
// EventUserMessageSent (UUID + Prompt) to register its bubble in the pending zone
// BEFORE any consume settle can reach it. It must translate to
// UserMessageSentMsg{UUID, Text} so the existing zone-add reducer tracks it and
// the later settle relocates rather than no-ops. (QUM-1068: the original
// rationale here was "a now-write gets no isReplay echo" — measured false; the
// ordering requirement is what makes this event load-bearing.)
func TestTranslateRuntimeEvent_UserMessageSent(t *testing.T) {
	got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{
		Type:   sprawlrt.EventUserMessageSent,
		UUID:   "now-1",
		Prompt: "AAA\nBBB",
	}, InterruptedAsResult)
	want := UserMessageSentMsg{UUID: "now-1", Text: "AAA\nBBB"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestTranslateRuntimeEvent_LifecycleEventsSkipped(t *testing.T) {
	for _, evType := range []sprawlrt.RuntimeEventType{
		sprawlrt.EventQueueDrained,
		sprawlrt.EventStopped,
	} {
		got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{Type: evType}, InterruptedAsResult)
		if got != nil {
			t.Errorf("event %v: expected nil (skip), got %T %+v", evType, got, got)
		}
	}
}

// QUM-927 rework: EventBackendFaulted deliberately has NO case here, and that
// decision is load-bearing enough to pin.
//
// Faults reach the TUI by a different, agent-NAMED route: the supervisor's
// per-runtime runFaultSubscriber (internal/supervisor/runtime_launcher.go:152)
// → Real.dispatchFault → cmd/enter.go's SetBackendFaultEmitter →
// BackendFaultMsg{Agent,...}. This translator cannot participate in that route:
// RuntimeEvent carries FaultClass/FaultNextAction but no agent identity, so the
// only BackendFaultMsg it could build is one keyed on "" — which poisons
// AppModel.faults[""] and renders a blank-agent " faulted: ..." toast. And
// because this function is shared by BOTH the per-child ChildStreamAdapter and
// the root bridge, adding a case here also duplicates every child fault that
// the supervisor path already surfaces correctly.
//
// The root pane's fault surface is instead the turn-terminal event: a genuine
// backend fault publishes EventTurnFailed (gated in unified.go's
// SetTerminalErrorHandler closure on a live turn — phase OR frame-level, which
// is exactly what QUM-927's rework widened), and EventTurnFailed already maps to
// SessionResultMsg{IsError} → the "Session Error" dialog with [r] restart.
//
// Adding a case here is therefore a plausible-looking WRONG fix for QUM-927: it
// makes the boundary-fault symptom disappear while leaving the runtime gate
// unfixed, and introduces double-surfacing in exchange. This test exists so that
// wrong fix fails a test instead of passing the suite. The separate gap — a
// backend fault while the root session is genuinely idle (no turn open at all,
// so no EventTurnFailed) — is real, pre-existing, and NOT fixed by adding a case
// here; it needs a root-side fault subscriber and is tracked as QUM-964.
func TestTranslateRuntimeEvent_BackendFaultedHasNoCase(t *testing.T) {
	// Both handlers, because this must hold for the per-child adapter and the root
	// bridge alike — they differ only in interruptedFn.
	for name, fn := range map[string]func(sprawlrt.RuntimeEvent) tea.Msg{
		"InterruptedAsResult":    InterruptedAsResult,
		"InterruptedAsCompleted": InterruptedAsCompleted,
	} {
		got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{
			Type:            sprawlrt.EventBackendFaulted,
			Error:           errors.New("backend: claude subprocess exited unexpectedly"),
			FaultClass:      "SubprocessExited",
			FaultNextAction: "wake",
		}, fn)
		if got != nil {
			t.Errorf("[%s] EventBackendFaulted must translate to nil (faults route via the agent-named "+
				"BackendFaultMsg supervisor path, and the root fault surface is EventTurnFailed); got %T %+v", name, got, got)
		}
	}
}

// QUM-858: EventTurnStarted is no longer a skipped lifecycle event — it maps to
// TurnStartedMsg so the TUI can light the in-turn indicator during the
// pre-content window of a freshly-opened turn.
func TestTranslateRuntimeEvent_TurnStartedMapsToMsg(t *testing.T) {
	got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{Type: sprawlrt.EventTurnStarted}, InterruptedAsResult)
	if _, ok := got.(TurnStartedMsg); !ok {
		t.Errorf("EventTurnStarted: got %T %+v, want TurnStartedMsg", got, got)
	}
}
