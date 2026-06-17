package tui

import (
	"sync"

	tea "charm.land/bubbletea/v2"
)

// fakeSessionBackend is the unified-runtime-style SessionBackend test double
// used across TUI tests (QUM-490). It replaces the legacy `tui.Bridge` +
// `mockSession`/`continuousFakeDelegate` combo deleted with this issue.
//
// AppModel only consumes its session via the SessionBackend interface, so a
// single fake covers every prior usage:
//   - Initialize/SendMessage/WaitForEvent/Interrupt/Close return inert
//     tea.Cmds whose msgs the caller may queue via QueueMsg.
//   - Counters expose how many times each method was called for assertions.
//   - Configurable errors (initErr/sendErr/interruptErr/closeErr) flow into
//     SessionErrorMsg / InterruptResultMsg respectively.
//   - sessionID + isContinuous mirror the legacy Bridge.SetSessionID and
//     continuousFakeDelegate.IsContinuous knobs.
//
// The zero value of fakeSessionBackend is NOT ready to use; construct via
// newFakeSessionBackend() to ensure the embedded queue channel is initialized.
type fakeSessionBackend struct {
	mu sync.Mutex

	initCalls                int
	sendCalls                int
	waitCalls                int
	interruptCalls           int
	closeCalls               int
	interruptAndSendCalls    int
	lastInterruptAndSendText string
	recallCalls              int
	sendAllNowCalls          int

	initErr             error
	sendErr             error
	interruptErr        error
	closeErr            error
	interruptAndSendErr error
	recallText          string
	recallErr           error
	sendAllNowErr       error

	sessionID    string
	isContinuous bool

	// queued is the FIFO of pre-staged tea.Msgs returned by WaitForEvent in
	// order. Tests that don't need event delivery can ignore this entirely;
	// when the queue is empty WaitForEvent returns the configured
	// waitDefault (defaults to nil, which the AppModel reducer drops).
	queued      []tea.Msg
	waitDefault tea.Msg

	// closeCalled / interruptCalled mirror the legacy mockSession field
	// names so migrated assertions read naturally. Tests run on a single
	// goroutine; mu protects the rest of the struct, but these are exposed
	// directly for assertion ergonomics (`if mock.closeCalled { ... }`).
	closeCalled     bool
	interruptCalled bool
}

// newFakeSessionBackend returns a SessionBackend test double in its default
// (legacy-bridge-equivalent) configuration: not continuous, empty session ID,
// every method returns success.
func newFakeSessionBackend() *fakeSessionBackend {
	return &fakeSessionBackend{}
}

// SetSessionID stores a session ID (mirrors *Bridge.SetSessionID).
func (f *fakeSessionBackend) SetSessionID(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionID = id
}

// SetContinuous toggles the IsContinuous() return value. Use true to model
// the unified-runtime adapter; false (default) models the legacy bridge.
func (f *fakeSessionBackend) SetContinuous(c bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.isContinuous = c
}

// QueueMsg enqueues a tea.Msg to be returned by the next WaitForEvent call.
// FIFO order. When the queue drains, WaitForEvent returns the configured
// waitDefault (nil by default).
func (f *fakeSessionBackend) QueueMsg(msg tea.Msg) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued = append(f.queued, msg)
}

// SetWaitDefault changes the message returned by WaitForEvent when the
// queue is empty. Use SessionErrorMsg{Err: io.EOF} to simulate session
// teardown.
func (f *fakeSessionBackend) SetWaitDefault(msg tea.Msg) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitDefault = msg
}

// --- SessionBackend implementation ---

func (f *fakeSessionBackend) Initialize() tea.Cmd {
	f.mu.Lock()
	f.initCalls++
	err := f.initErr
	f.mu.Unlock()
	return func() tea.Msg {
		if err != nil {
			return SessionErrorMsg{Err: err}
		}
		return SessionInitializedMsg{}
	}
}

func (f *fakeSessionBackend) SendMessage(_ string) tea.Cmd {
	f.mu.Lock()
	f.sendCalls++
	err := f.sendErr
	f.mu.Unlock()
	return func() tea.Msg {
		if err != nil {
			return SessionErrorMsg{Err: err}
		}
		return UserMessageSentMsg{}
	}
}

func (f *fakeSessionBackend) WaitForEvent() tea.Cmd {
	f.mu.Lock()
	f.waitCalls++
	var next tea.Msg
	if len(f.queued) > 0 {
		next = f.queued[0]
		f.queued = f.queued[1:]
	} else {
		next = f.waitDefault
	}
	f.mu.Unlock()
	return func() tea.Msg { return next }
}

func (f *fakeSessionBackend) Interrupt() tea.Cmd {
	f.mu.Lock()
	f.interruptCalls++
	f.interruptCalled = true
	err := f.interruptErr
	f.mu.Unlock()
	return func() tea.Msg { return InterruptResultMsg{Err: err} }
}

// InterruptAndSend records the call and returns a cmd that emits an
// InterruptResultMsg carrying the configured interruptAndSendErr. The TUI
// expects the adapter to be responsible for enqueuing `text` as the next
// prompt regardless of whether the preempt itself succeeded — this fake
// mirrors that contract by recording the text unconditionally.
func (f *fakeSessionBackend) InterruptAndSend(text string) tea.Cmd {
	f.mu.Lock()
	f.interruptAndSendCalls++
	f.lastInterruptAndSendText = text
	err := f.interruptAndSendErr
	f.mu.Unlock()
	return func() tea.Msg { return InterruptResultMsg{Err: err} }
}

func (f *fakeSessionBackend) Recall() tea.Cmd {
	f.mu.Lock()
	f.recallCalls++
	text := f.recallText
	err := f.recallErr
	f.mu.Unlock()
	return func() tea.Msg { return PromptsRecalledMsg{Text: text, Err: err} }
}

func (f *fakeSessionBackend) SendAllNow() tea.Cmd {
	f.mu.Lock()
	f.sendAllNowCalls++
	err := f.sendAllNowErr
	f.mu.Unlock()
	return func() tea.Msg { return SendAllNowResultMsg{Err: err} }
}

func (f *fakeSessionBackend) Close() error {
	f.mu.Lock()
	f.closeCalls++
	f.closeCalled = true
	err := f.closeErr
	f.mu.Unlock()
	return err
}

func (f *fakeSessionBackend) SessionID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionID
}

func (f *fakeSessionBackend) IsContinuous() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.isContinuous
}

// Compile-time check: fakeSessionBackend implements SessionBackend.
var _ SessionBackend = (*fakeSessionBackend)(nil)
