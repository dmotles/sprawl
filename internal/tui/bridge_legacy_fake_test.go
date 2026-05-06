package tui

import (
	"context"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/protocol"
)

// BridgeSession is the interface that the bridge uses to interact with the
// host session. This matches the methods on host.Session that the bridge needs.
type BridgeSession interface {
	Initialize(ctx context.Context) error
	SendUserMessage(ctx context.Context, prompt string) (<-chan *protocol.Message, error)
	Interrupt(ctx context.Context) error
	Close() error
}

// BridgeDelegate is the abstract behavior a Bridge wraps. The legacy
// BridgeSession-backed implementation lives below in legacyBridgeDelegate;
// the unified-runtime path (QUM-399) adapts internal/tuiruntime.TUIAdapter to
// this interface.
type BridgeDelegate interface {
	Initialize() tea.Cmd
	SendMessage(text string) tea.Cmd
	WaitForEvent() tea.Cmd
	Interrupt() tea.Cmd
	Close() error
	SessionID() string
	// IsContinuous reports whether the delegate's event stream is continuous
	// (autonomous events flow without a user turn). Legacy delegates return
	// false; the unified-runtime delegate returns true. The AppModel uses
	// this to decide when to keep WaitForEvent running across turn
	// boundaries.
	IsContinuous() bool
}

// Bridge adapts a host session into Bubble Tea commands and messages.
// It converts protocol events from the session into tea.Msg types
// that the TUI model can handle.
//
// Bridge is a thin wrapper around a BridgeDelegate. The legacy session-backed
// behavior is preserved by NewBridge (which constructs a legacyBridgeDelegate
// internally). The unified-runtime path uses NewBridgeFromDelegate.
type Bridge struct {
	delegate BridgeDelegate
}

// NewBridge creates a new Bridge wrapping the given session. Preserves the
// legacy per-turn event lifecycle.
func NewBridge(ctx context.Context, session BridgeSession) *Bridge {
	return &Bridge{
		delegate: &legacyBridgeDelegate{
			session: session,
			ctx:     ctx,
		},
	}
}

// NewBridgeFromDelegate wraps an arbitrary BridgeDelegate. Used by the
// unified-runtime path (QUM-399) to plug a TUIAdapter into the existing
// AppModel without changing call sites.
func NewBridgeFromDelegate(d BridgeDelegate) *Bridge {
	return &Bridge{delegate: d}
}

// SetSessionID stores the Claude session ID for this bridge so the TUI can
// display it (e.g. in the status bar) after Initialize. For the legacy
// delegate this stores into its sessionID field; for non-legacy delegates
// this is a no-op (the unified path's SessionID() already delegates to the
// runtime).
func (b *Bridge) SetSessionID(id string) {
	if legacy, ok := b.delegate.(*legacyBridgeDelegate); ok {
		legacy.sessionID = id
	}
}

// SessionID returns the underlying delegate's session ID.
func (b *Bridge) SessionID() string { return b.delegate.SessionID() }

// IsContinuous reports whether the delegate produces autonomous events
// outside of a user turn. See BridgeDelegate.
func (b *Bridge) IsContinuous() bool { return b.delegate.IsContinuous() }

// Initialize returns a tea.Cmd that initializes the session.
func (b *Bridge) Initialize() tea.Cmd { return b.delegate.Initialize() }

// SendMessage returns a tea.Cmd that sends a user message to the session.
func (b *Bridge) SendMessage(text string) tea.Cmd { return b.delegate.SendMessage(text) }

// WaitForEvent returns a tea.Cmd that reads the next event from the session.
func (b *Bridge) WaitForEvent() tea.Cmd { return b.delegate.WaitForEvent() }

// Interrupt returns a tea.Cmd that sends an interrupt request to the session.
func (b *Bridge) Interrupt() tea.Cmd { return b.delegate.Interrupt() }

// Close shuts down the bridge by closing the underlying delegate.
func (b *Bridge) Close() error { return b.delegate.Close() }

// legacyBridgeDelegate is the original BridgeSession-backed implementation,
// preserved unchanged behaviorally so existing tests and the legacy enter.go
// path keep working.
type legacyBridgeDelegate struct {
	session   BridgeSession
	ctx       context.Context
	events    <-chan *protocol.Message
	sessionID string
}

func (l *legacyBridgeDelegate) Initialize() tea.Cmd {
	return func() tea.Msg {
		if err := l.session.Initialize(l.ctx); err != nil {
			return SessionErrorMsg{Err: fmt.Errorf("initializing session: %w", err)}
		}
		return SessionInitializedMsg{}
	}
}

func (l *legacyBridgeDelegate) SendMessage(text string) tea.Cmd {
	return func() tea.Msg {
		events, err := l.session.SendUserMessage(l.ctx, text)
		if err != nil {
			return SessionErrorMsg{Err: fmt.Errorf("sending message: %w", err)}
		}
		l.events = events
		return UserMessageSentMsg{}
	}
}

func (l *legacyBridgeDelegate) WaitForEvent() tea.Cmd {
	return func() tea.Msg {
		if l.events == nil {
			return SessionErrorMsg{Err: fmt.Errorf("no active event stream")}
		}

		select {
		case msg, ok := <-l.events:
			if !ok {
				return SessionErrorMsg{Err: io.EOF}
			}
			result := MapProtocolMessage(msg)
			if result == nil {
				// Unknown message type — skip and wait for next
				return l.WaitForEvent()()
			}
			return result
		case <-l.ctx.Done():
			return SessionErrorMsg{Err: l.ctx.Err()}
		}
	}
}

func (l *legacyBridgeDelegate) Interrupt() tea.Cmd {
	return func() tea.Msg {
		err := l.session.Interrupt(l.ctx)
		return InterruptResultMsg{Err: err}
	}
}

func (l *legacyBridgeDelegate) Close() error       { return l.session.Close() }
func (l *legacyBridgeDelegate) SessionID() string  { return l.sessionID }
func (l *legacyBridgeDelegate) IsContinuous() bool { return false }
