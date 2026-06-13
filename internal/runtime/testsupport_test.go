package runtime

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
)

// scriptedTransport is an in-memory backend.ManagedTransport. Sends are
// captured on sendCh; Recv yields frames pushed via feed(). It models the
// stream-json wire well enough to drive a real backend.Session (QUM-817: this
// is now the canonical runtime-test fixture for the stdin-write input path —
// it survived the deletion of the TurnLoop tests).
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
