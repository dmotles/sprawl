package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-618: per-turn-deadline teardown regression at the runtime/integration
// layer.
//
// The TurnLoop's TurnTimeout fires on a healthy long turn. On turnCtx cancel
// the loop returns, but the backend reader runs on a DETACHED readerCtx, so
// backend.Session.currentTurn (the non-autonomous frame created by the first
// StartTurn) stays pinned. The FOLLOW-UP turn's StartTurn then returns
// backend.ErrTurnInProgress and the pending queue item is never delivered.
//
// To reproduce the REAL mechanism (not a hand-rolled approximation) this test
// wires a real backend.NewSession over an in-memory transport into the
// TurnLoop. backend is already imported by this test package, so there is no
// import cycle; option (a) from the plan is used for fidelity.

// scriptedTransport is an in-memory backend.ManagedTransport. Sends are
// captured on sendCh; Recv yields frames pushed via feed(). It models the
// stream-json wire well enough to drive a real backend.Session.
type scriptedTransport struct {
	sendCh chan any
	recvCh chan *protocol.Message
}

func newScriptedTransport() *scriptedTransport {
	return &scriptedTransport{
		sendCh: make(chan any, 100),
		recvCh: make(chan *protocol.Message, 100),
	}
}

func (s *scriptedTransport) Send(ctx context.Context, msg any) error {
	select {
	case s.sendCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *scriptedTransport) Recv(ctx context.Context) (*protocol.Message, error) {
	select {
	case msg, ok := <-s.recvCh:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *scriptedTransport) Close() error { return nil }
func (s *scriptedTransport) Wait() error  { return nil }
func (s *scriptedTransport) Kill() error  { return nil }
func (s *scriptedTransport) Pid() int     { return 0 }

// feed pushes a raw JSON frame onto the transport's receive path.
func (s *scriptedTransport) feed(t *testing.T, raw string) {
	t.Helper()
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("feed: unmarshal error: %v", err)
	}
	msg.Raw = json.RawMessage(raw)
	s.recvCh <- &msg
}

// drainOneSend pops one captured Send (e.g. a user prompt frame) within d, or
// fails the test. Used to confirm a StartTurn actually emitted its prompt.
func (s *scriptedTransport) drainOneSend(t *testing.T, d time.Duration, label string) {
	t.Helper()
	select {
	case <-s.sendCh:
	case <-time.After(d):
		t.Fatalf("timed out waiting for transport Send (%s)", label)
	}
}

const (
	scriptedInitFrame   = `{"type":"system","subtype":"init","session_id":"sess-qum618"}`
	scriptedAssistFrame = `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`
	scriptedResultFrame = `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`
)

// TestTurnLoop_PerTurnDeadline_RecoversAndDeliversPending pins the end-to-end
// contract: when a per-turn deadline fires on a busy turn, the loop must
// surface EventTurnFailed wrapping context.DeadlineExceeded AND the backend
// must clear its pinned turn so the FOLLOW-UP turn's StartTurn is accepted and
// the pending queue item gets delivered.
//
// FAILS today: the per-turn deadline cancels turnCtx, but the real backend
// reader runs on a detached readerCtx and never clears currentTurn, so the
// follow-up StartTurn returns backend.ErrTurnInProgress and
// OnQueueItemDelivered never fires for the pending item.
func TestTurnLoop_PerTurnDeadline_RecoversAndDeliversPending(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-qum618"})
	t.Cleanup(func() { _ = session.Close() })

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(256)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "first turn", EntryIDs: []string{"seq-A"}})

	var (
		cbMu      sync.Mutex
		delivered []string
	)
	loop := NewTurnLoop(TurnLoopConfig{
		Session:     session,
		Queue:       q,
		EventBus:    bus,
		TurnTimeout: 50 * time.Millisecond,
		OnQueueItemDelivered: func(item QueueItem) {
			cbMu.Lock()
			delivered = append(delivered, item.EntryIDs...)
			cbMu.Unlock()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})

	// First turn starts: drain its user prompt, then feed system:init to open
	// the frame and keep the stream BUSY (no result) so it runs past the 50ms
	// deadline.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "first-turn user prompt")
	transport.feed(t, scriptedInitFrame)

	// The per-turn deadline must surface as EventTurnFailed wrapping
	// context.DeadlineExceeded.
	ev, _ := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnFailed
	})
	if ev.Error == nil {
		t.Fatalf("EventTurnFailed.Error is nil; want an error wrapping context.DeadlineExceeded")
	}
	if !errors.Is(ev.Error, context.DeadlineExceeded) {
		t.Errorf("EventTurnFailed.Error = %v; want errors.Is(err, context.DeadlineExceeded)", ev.Error)
	}

	// Enqueue a pending follow-up item and wake the loop. The follow-up
	// StartTurn must be ACCEPTED — observed via a SECOND EventTurnStarted.
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "follow-up", EntryIDs: []string{"seq-B"}})
	q.Wake()

	// Wait for the second EventTurnStarted (the follow-up turn).
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted && ev.Prompt == "follow-up"
	})

	// Drive the follow-up turn to completion: drain its prompt, feed a
	// non-init frame (proves delivery) and a result frame.
	transport.drainOneSend(t, 2*time.Second, "follow-up user prompt")
	transport.feed(t, scriptedAssistFrame)
	transport.feed(t, scriptedResultFrame)

	// The follow-up item must be reported delivered (delivery proxy at this
	// layer). Fails today: follow-up StartTurn rejected → never delivered.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cbMu.Lock()
		ok := containsString(delivered, "seq-B")
		cbMu.Unlock()
		if ok {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	cbMu.Lock()
	got := append([]string(nil), delivered...)
	cbMu.Unlock()
	t.Fatalf("OnQueueItemDelivered never fired for pending follow-up item; delivered=%v, want to include seq-B", got)
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
