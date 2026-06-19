// Shared EventBus → tea.Msg translation used by both the per-child
// ChildStreamAdapter (in this package) and the bridge tuiruntime.TUIAdapter
// (which already imports this package for tea.Msg types). Living in `tui`
// avoids re-introducing an import cycle while collapsing the previously
// duplicated switch into one routine. (QUM-446)

package tui

import (
	tea "charm.land/bubbletea/v2"

	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
)

// TranslateRuntimeEvent converts a RuntimeEvent into the tea.Msg the caller's
// WaitForEvent loop should return. A nil result means "skip / continue to the
// next event" — i.e. lifecycle-only events (EventTurnStarted, EventQueueDrained,
// EventStopped), protocol messages that map to nil, and the protocol "result"
// envelope (which is intentionally dropped here because EventTurnCompleted
// already drives the terminal SessionResultMsg).
//
// EventInterrupted is delegated to interruptedFn because the two adapters
// translate it differently: the child viewport adapter surfaces
// InterruptResultMsg{Err: nil}, while the bridge adapter surfaces
// InterruptCompletedMsg populated from ev.Result. interruptedFn must not be
// nil.
func TranslateRuntimeEvent(ev sprawlrt.RuntimeEvent, interruptedFn func(sprawlrt.RuntimeEvent) tea.Msg) tea.Msg {
	switch ev.Type {
	case sprawlrt.EventProtocolMessage:
		if ev.Message == nil {
			return nil
		}
		// Drop protocol "result" messages here. The terminal SessionResultMsg
		// is emitted from EventTurnCompleted/EventTurnFailed/EventInterrupted;
		// surfacing the protocol-result mapping as well would yield a
		// duplicate SessionResultMsg per turn. (QUM-436 Item 1)
		if ev.Message.Type == "result" {
			return nil
		}
		return MapProtocolMessage(ev.Message)
	case sprawlrt.EventTurnCompleted:
		if ev.Result == nil {
			return SessionResultMsg{}
		}
		return SessionResultMsg{
			Result:       ev.Result.Result,
			IsError:      ev.Result.IsError,
			DurationMs:   ev.Result.DurationMs,
			NumTurns:     ev.Result.NumTurns,
			TotalCostUsd: ev.Result.TotalCostUsd,
		}
	case sprawlrt.EventTurnFailed:
		var errStr string
		if ev.Error != nil {
			errStr = ev.Error.Error()
		}
		return SessionResultMsg{IsError: true, Result: errStr}
	case sprawlrt.EventInterrupted:
		return interruptedFn(ev)
	case sprawlrt.EventUserMessageSent:
		// QUM-838: a now-write (send-all-now) publishes this so the TUI tracks the
		// coalesced message's fresh uuid in the pending zone (ZoneAddUser) before
		// its consume settle relocates it into the committed transcript.
		return UserMessageSentMsg{UUID: ev.UUID, Text: ev.Prompt}
	case sprawlrt.EventUserMessageConsumed:
		return UserMessageConsumedMsg{UUID: ev.UUID}
	case sprawlrt.EventUserMessageCancelled:
		return UserMessageCancelledMsg{UUID: ev.UUID}
	case sprawlrt.EventTurnStarted, sprawlrt.EventQueueDrained, sprawlrt.EventStopped:
		return nil
	default:
		return nil
	}
}

// InterruptedAsResult is the EventInterrupted handler used by the per-child
// ChildStreamAdapter — it surfaces a bare InterruptResultMsg with no error.
func InterruptedAsResult(_ sprawlrt.RuntimeEvent) tea.Msg {
	return InterruptResultMsg{Err: nil}
}

// InterruptedAsCompleted is the EventInterrupted handler used by the bridge
// TUIAdapter — it surfaces InterruptCompletedMsg populated from ev.Result.
func InterruptedAsCompleted(ev sprawlrt.RuntimeEvent) tea.Msg {
	if ev.Result == nil {
		return InterruptCompletedMsg{}
	}
	return InterruptCompletedMsg{
		Result:       ev.Result.Result,
		DurationMs:   ev.Result.DurationMs,
		NumTurns:     ev.Result.NumTurns,
		TotalCostUsd: ev.Result.TotalCostUsd,
	}
}
