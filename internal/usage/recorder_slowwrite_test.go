package usage

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
)

// slowWriter sleeps `delay` per Write to simulate a stalled disk. It records
// the number of writes observed so the test can assert progress.
type slowWriter struct {
	delay  time.Duration
	writes int64
}

func (s *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(s.delay)
	atomic.AddInt64(&s.writes, 1)
	return len(p), nil
}

func (s *slowWriter) Close() error { return nil }

// TestRecorder_SlowWriteDoesNotBlockPublisher exercises the EventBus
// subscriber pattern: a slow Recorder must NOT throttle the publisher beyond
// the subscriber buffer slop. The EventBus drops on full buffer (see
// eventbus.go: Publish returns promptly), so publisher latency stays bounded.
//
// Drives 5 turns through a Recorder whose underlying writer sleeps 100ms per
// write. Wires the Recorder via the production EventBus subscriber pattern
// (Subscribe + goroutine -> Handle). Asserts EventBus.Publish returns in
// well under the cumulative subscriber write latency.
func TestRecorder_SlowWriteDoesNotBlockPublisher(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	sw := &slowWriter{delay: 100 * time.Millisecond}
	rec, err := NewRecorder(tmp, "finn",
		WithWriterFactory(func(path string) (io.WriteCloser, error) {
			_ = path
			return sw, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer rec.Close()

	bus := runtime.NewEventBus()
	ch, cancel := bus.Subscribe(32)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			rec.Handle(ev)
		}
	}()

	const turns = 5
	sessionID := "sess-slow"
	publishStart := time.Now()
	for i := 0; i < turns; i++ {
		bus.Publish(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
		bus.Publish(turnCompletedEvent(sessionID, 0.001))
	}
	publishElapsed := time.Since(publishStart)

	// 10 publishes; if Publish blocked on the slow writer, this would take
	// well over 500ms. The 32-buffer subscriber absorbs the burst. Allow a
	// generous ceiling for CI scheduler noise but well under the cumulative
	// subscriber latency (5 * 2 * 100ms = 1s).
	if publishElapsed > 300*time.Millisecond {
		t.Errorf("EventBus.Publish blocked for %v across %d turns; expected the subscriber buffer to absorb the burst", publishElapsed, turns)
	}

	// Drain: wait for the subscriber goroutine to flush all pending events
	// through the slow writer. After draining we MUST observe one write per
	// completed turn (proves the slow writer was actually wired — guards
	// against a no-op Handle stub making this test trivially pass).
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("subscriber goroutine did not exit after cancel")
	}
	if got := atomic.LoadInt64(&sw.writes); got < int64(turns) {
		t.Errorf("slowWriter observed %d writes, want >= %d (one per completed turn); Recorder is not wiring the injected writer factory", got, turns)
	}
}
