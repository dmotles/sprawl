package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
)

// newTestDrainer builds a rootDrainer backed by an in-memory queue so tests
// don't touch disk. entries is the initial pending set; callers can re-assign
// d.peek to simulate dynamic enqueues.
func newTestDrainer(t *testing.T, entries []agentloop.Entry, sendErr error) (*rootDrainer, *[]string, *[]string) {
	t.Helper()
	sent := []string{}
	delivered := []string{}
	pending := append([]agentloop.Entry(nil), entries...)

	d := &rootDrainer{
		sprawlRoot: "/tmp/test-sprawl",
		rootName:   "weave",
		sendPrompt: func(prompt string) error {
			if sendErr != nil {
				return sendErr
			}
			sent = append(sent, prompt)
			return nil
		},
		peek: func(string, string) ([]agentloop.Entry, error) {
			return append([]agentloop.Entry(nil), pending...), nil
		},
		markDelivered: func(_, _, id string) error {
			delivered = append(delivered, id)
			// Simulate moving to delivered by removing from pending.
			next := pending[:0]
			for _, e := range pending {
				if e.ID != id {
					next = append(next, e)
				}
			}
			pending = next
			return nil
		},
		interval: 10 * time.Millisecond,
		logW:     &bytes.Buffer{},
	}
	return d, &sent, &delivered
}

func TestRootDrainer_NoPending_NoSend(t *testing.T) {
	d, sent, delivered := newTestDrainer(t, nil, nil)
	n, err := d.runOnce()
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 drained, got %d", n)
	}
	if len(*sent) != 0 || len(*delivered) != 0 {
		t.Errorf("expected no sends/delivers, got sent=%v delivered=%v", *sent, *delivered)
	}
}

func TestRootDrainer_AsyncEntries_RendersFrameAndMarksDelivered(t *testing.T) {
	entries := []agentloop.Entry{
		{ID: "a1", Class: agentloop.ClassAsync, From: "ghost", Subject: "hello", Body: "body-a1"},
		{ID: "a2", Class: agentloop.ClassAsync, From: "ghost", Subject: "again", Body: "body-a2"},
	}
	d, sent, delivered := newTestDrainer(t, entries, nil)

	n, err := d.runOnce()
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 drained, got %d", n)
	}
	if len(*sent) != 1 {
		t.Fatalf("expected 1 send (bundled async frame), got %d: %v", len(*sent), *sent)
	}
	for _, needle := range []string{
		"[inbox] You received 2 message(s)",
		"body-a1",
		"body-a2",
	} {
		if !strings.Contains((*sent)[0], needle) {
			t.Errorf("send missing %q, got:\n%s", needle, (*sent)[0])
		}
	}
	if len(*delivered) != 2 {
		t.Errorf("expected 2 markDelivered calls, got %d", len(*delivered))
	}
}

func TestRootDrainer_InterruptBeforeAsync(t *testing.T) {
	entries := []agentloop.Entry{
		{ID: "a1", Class: agentloop.ClassAsync, From: "x", Subject: "async", Body: "async-body"},
		{ID: "i1", Class: agentloop.ClassInterrupt, From: "weave", Subject: "stop", Body: "interrupt-body"},
	}
	d, sent, _ := newTestDrainer(t, entries, nil)

	if _, err := d.runOnce(); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if len(*sent) != 2 {
		t.Fatalf("expected 2 sends (interrupt + async), got %d", len(*sent))
	}
	if !strings.Contains((*sent)[0], "[interrupt]") {
		t.Errorf("expected first send to be interrupt frame, got:\n%s", (*sent)[0])
	}
	if !strings.Contains((*sent)[1], "[inbox]") {
		t.Errorf("expected second send to be async inbox frame, got:\n%s", (*sent)[1])
	}
}

func TestRootDrainer_SendFails_DoesNotMarkDelivered(t *testing.T) {
	entries := []agentloop.Entry{
		{ID: "a1", Class: agentloop.ClassAsync, From: "x", Subject: "s", Body: "b"},
	}
	d, sent, delivered := newTestDrainer(t, entries, errors.New("tmux boom"))
	_, err := d.runOnce()
	if err == nil {
		t.Fatal("expected error from send")
	}
	if len(*sent) != 0 {
		t.Errorf("sent should be empty when sendErr is stubbed, got %v", *sent)
	}
	if len(*delivered) != 0 {
		t.Errorf("expected no markDelivered on send failure, got %d", len(*delivered))
	}
}

func TestRootDrainer_RunLoop_CancelStopsPoller(t *testing.T) {
	d, _, _ := newTestDrainer(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.run(ctx); close(done) }()

	// Let one tick fire.
	time.Sleep(25 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("drain goroutine did not exit after cancel")
	}
}

func TestStartRootDrainLoop_WaitUnblocksAfterCancel(t *testing.T) {
	// Use a tmux path that will not resolve — the goroutine still must exit
	// cleanly on ctx cancel because runOnce's peek is against a sprawlRoot
	// that has no queue dir (returns empty).
	ctx, cancel := context.WithCancel(context.Background())
	wait := startRootDrainLoop(ctx, "/tmp/nonexistent-root", "weave", "/nope/tmux", "s", "w", &bytes.Buffer{})
	cancel()
	done := make(chan struct{})
	go func() { wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("wait did not return after cancel")
	}
}
