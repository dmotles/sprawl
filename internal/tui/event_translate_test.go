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

// QUM-838: a now-write (send-all-now) gets no isReplay echo, so the runtime
// publishes EventUserMessageSent (UUID + Prompt) to register its bubble in the
// pending zone. It must translate to UserMessageSentMsg{UUID, Text} so the
// existing zone-add reducer tracks it before its consume settle relocates it.
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
		sprawlrt.EventTurnStarted,
		sprawlrt.EventQueueDrained,
		sprawlrt.EventStopped,
	} {
		got := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{Type: evType}, InterruptedAsResult)
		if got != nil {
			t.Errorf("event %v: expected nil (skip), got %T %+v", evType, got, got)
		}
	}
}
