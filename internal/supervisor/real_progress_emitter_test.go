package supervisor

import (
	"context"
	"sync"
	"testing"

	"github.com/dmotles/sprawl/internal/sprawlmcp/calllog"
)

// QUM-497: Real.composeCheckpoint must fan out per-call agentops checkpoints
// to BOTH the JSONL call log (when present) AND the host TUI's progress
// emitter. This guards the seam used by merge.validate-line.

type recordedProgress struct {
	mu     sync.Mutex
	events []progressEvent
}

type progressEvent struct {
	callID, step, tail string
}

func (r *recordedProgress) push(callID, step, tail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, progressEvent{callID, step, tail})
}

func (r *recordedProgress) snap() []progressEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]progressEvent, len(r.events))
	copy(out, r.events)
	return out
}

func TestComposeCheckpoint_FansOutToProgressEmitter(t *testing.T) {
	r := &Real{}
	rec := &recordedProgress{}
	r.SetProgressEmitter(rec.push)

	cp := r.composeCheckpoint("call-xyz")
	if cp == nil {
		t.Fatal("composeCheckpoint returned nil with emitter set")
	}
	cp("merge.validate-line", "line", "PASS internal/foo")
	cp("merge.validate-started", "cmd", "make validate")

	got := rec.snap()
	if len(got) != 2 {
		t.Fatalf("expected 2 progress events, got %d: %+v", len(got), got)
	}
	if got[0] != (progressEvent{"call-xyz", "merge.validate-line", "PASS internal/foo"}) {
		t.Errorf("validate-line event: %+v", got[0])
	}
	if got[1].callID != "call-xyz" || got[1].step != "merge.validate-started" || got[1].tail != "" {
		t.Errorf("validate-started event: %+v", got[1])
	}
}

func TestComposeCheckpoint_NilEmitter_NilLogger_ReturnsNil(t *testing.T) {
	r := &Real{}
	if cp := r.composeCheckpoint("any"); cp != nil {
		t.Errorf("expected nil checkpoint when no logger or emitter set")
	}
}

func TestComposeCheckpoint_LoggerOnly_NoProgressFanout(t *testing.T) {
	r := &Real{}
	r.logger = calllog.NewNoop() // logger present, but noop
	r.SetProgressEmitter(nil)

	// composeCheckpoint with empty id returns nil (logger needs an id).
	if cp := r.composeCheckpoint(""); cp != nil {
		t.Errorf("composeCheckpoint(\"\") with noop logger should be nil")
	}
}

func TestComposeCheckpoint_EmptyCallID_SkipsEmitter(t *testing.T) {
	r := &Real{}
	rec := &recordedProgress{}
	r.SetProgressEmitter(rec.push)
	cp := r.composeCheckpoint("")
	if cp == nil {
		t.Fatal("composeCheckpoint should not be nil when emitter is set")
	}
	cp("merge.validate-line", "line", "PASS")
	if got := rec.snap(); len(got) != 0 {
		t.Errorf("emitter should not fire when callID is empty (no MCP call context); got %+v", got)
	}
}

// TestExtractKVLine guards the helper that pulls the "line" value out of the
// flat checkpoint kv slice — used to surface validate-line tails in progress
// events.
func TestExtractKVLine(t *testing.T) {
	if got := extractKVLine([]any{"line", "hello"}); got != "hello" {
		t.Errorf("simple kv: %q", got)
	}
	if got := extractKVLine([]any{"cmd", "make", "line", "PASS"}); got != "PASS" {
		t.Errorf("multi kv: %q", got)
	}
	if got := extractKVLine([]any{"cmd", "make"}); got != "" {
		t.Errorf("missing line: %q", got)
	}
	if got := extractKVLine(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
	// non-string value type → treat as missing
	if got := extractKVLine([]any{"line", 42}); got != "" {
		t.Errorf("non-string val: %q", got)
	}
	// odd length should not panic
	if got := extractKVLine([]any{"line"}); got != "" {
		t.Errorf("odd length: %q", got)
	}
}

// Pure-context sanity: SetProgressEmitter accepts and is idempotent.
func TestSetProgressEmitter_NilClears(t *testing.T) {
	r := &Real{}
	rec := &recordedProgress{}
	r.SetProgressEmitter(rec.push)
	r.SetProgressEmitter(nil)
	cp := r.composeCheckpoint("id")
	if cp != nil {
		t.Errorf("after clearing emitter (and no logger), composeCheckpoint should be nil")
	}
	_ = context.Background() // keep import for future ctx-driven extensions
}
