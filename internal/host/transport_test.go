package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

func TestTransport_SendWritesNDJSON(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	tr := newTestPipeTransport(t, "", &buf)

	ctx := context.Background()
	msg := map[string]string{"type": "user", "content": "hello"}
	if err := tr.Send(ctx, msg); err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	output := buf.String()
	if !strings.HasSuffix(output, "\n") {
		t.Error("Send() output does not end with newline")
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got["type"] != "user" {
		t.Errorf("type = %q, want %q", got["type"], "user")
	}
	if got["content"] != "hello" {
		t.Errorf("content = %q, want %q", got["content"], "hello")
	}
}

func TestTransport_RecvReturnsMessages(t *testing.T) {
	input := `{"type":"system","subtype":"init","session_id":"s1"}` + "\n"
	tr := newTestPipeTransport(t, input, &bytes.Buffer{})

	ctx := context.Background()
	msg, err := tr.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv() error: %v", err)
	}
	if msg == nil {
		t.Fatal("Recv() returned nil")
	}
	if msg.Type != "system" {
		t.Errorf("Type = %q, want %q", msg.Type, "system")
	}
	if msg.Subtype != "init" {
		t.Errorf("Subtype = %q, want %q", msg.Subtype, "init")
	}
}

func TestTransport_RecvRespectsContextCancellation(t *testing.T) {
	// Use an empty reader that will block - simulate by using a pipe we never write to
	pr, _ := io.Pipe()
	tr := &PipeTransport{
		reader: protocol.NewReader(pr),
		writer: protocol.NewWriter(&bytes.Buffer{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := tr.Recv(ctx)
	if err == nil {
		t.Fatal("Recv() expected context error, got nil")
	}
	if ctx.Err() == nil {
		t.Error("expected context to be cancelled")
	}
}

func TestTransport_RecvReturnsEOFOnClose(t *testing.T) {
	// Empty input - reader returns EOF immediately
	tr := newTestPipeTransport(t, "", &bytes.Buffer{})

	ctx := context.Background()
	_, err := tr.Recv(ctx)
	if err == nil {
		t.Fatal("Recv() expected error on empty stream, got nil")
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("Recv() error = %v, want io.EOF", err)
	}
}

func TestTransport_CloseIsIdempotent(t *testing.T) {
	tr := newTestPipeTransport(t, "", &bytes.Buffer{})

	if err := tr.Close(); err != nil {
		t.Fatalf("first Close() error: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close() error: %v", err)
	}
}

// PipeTransport is a Transport backed by in-memory reader/writer for testing.
// The real implementation will wrap a subprocess; this tests the protocol layer.
type PipeTransport struct {
	reader *protocol.Reader
	writer *protocol.Writer
	closed bool
}

func (p *PipeTransport) Send(ctx context.Context, msg any) error {
	return p.writer.WriteJSON(msg)
}

func (p *PipeTransport) Recv(ctx context.Context) (*protocol.Message, error) {
	type result struct {
		msg *protocol.Message
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := p.reader.Next()
		ch <- result{msg, err}
	}()
	select {
	case r := <-ch:
		return r.msg, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *PipeTransport) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	return p.writer.Close()
}

func newTestPipeTransport(t *testing.T, input string, output *bytes.Buffer) *PipeTransport {
	t.Helper()
	return &PipeTransport{
		reader: protocol.NewReader(strings.NewReader(input)),
		writer: protocol.NewWriter(output),
	}
}
