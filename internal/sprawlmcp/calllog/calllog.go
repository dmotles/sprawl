// Package calllog provides per-MCP-call observability via a JSONL call log
// and an in-flight registry. See QUM-494.
package calllog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Rotation policy for mcp-calls.jsonl. Applied only at Open (boot time) so
// a single MCP call's start/checkpoint/end lines never split across files —
// see QUM-502. Tuneable here (no config plumbing — YAGNI).
//
//nolint:gochecknoglobals // package-level tunables; overridden in tests via export_test.go.
var (
	maxLogBytes  int64 = 64 * 1024 * 1024 // rotate when current file exceeds 64 MiB
	maxRotations       = 3                // keep .1, .2, .3; drop older
)

// CallState captures the live state of an in-flight MCP call.
type CallState struct {
	CallID        string    `json:"call_id"`
	Tool          string    `json:"tool"`
	Caller        string    `json:"caller"`
	CurrentStep   string    `json:"current_step"`
	StartedAt     time.Time `json:"started_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

// fileSyncer is the file-like interface the Logger uses for JSONL output.
type fileSyncer interface {
	Write(p []byte) (int, error)
	Sync() error
	Close() error
}

// syncCounter wraps an *os.File and increments an int pointer on every Sync.
type syncCounter struct {
	f       *os.File
	counter *int
}

func (s *syncCounter) Write(p []byte) (int, error) { return s.f.Write(p) }
func (s *syncCounter) Sync() error {
	if s.counter != nil {
		*s.counter++
	}
	return s.f.Sync()
}
func (s *syncCounter) Close() error { return s.f.Close() }

// Logger writes per-call structured logs to JSONL and maintains an
// in-flight registry on disk.
type Logger struct {
	mu      sync.Mutex
	noop    bool
	file    fileSyncer
	logPath string
	regPath string
	active  map[string]*CallState
	now     func() time.Time
	newID   func() string
	closed  bool

	// tickReq, when non-nil, replaces the time.Ticker-driven heartbeat. The
	// test seam sends a chan to request a tick and waits on it for completion.
	tickReq chan chan struct{}
	stop    chan struct{}
	stopped chan struct{}
}

// Open creates the logs/ and runtime/ directories under sprawlRoot/.sprawl/
// and returns a Logger that appends to mcp-calls.jsonl. It also starts a
// background heartbeat goroutine that writes the in-flight registry.
func Open(sprawlRoot string) (*Logger, error) {
	l, err := openInternal(sprawlRoot, internalOptions{})
	if err != nil {
		return nil, err
	}
	go l.runHeartbeat()
	return l, nil
}

type internalOptions struct {
	Now         func() time.Time
	NewID       func() string
	SyncCounter *int
}

func openInternal(sprawlRoot string, opts internalOptions) (*Logger, error) {
	logsDir := filepath.Join(sprawlRoot, ".sprawl", "logs")
	runtimeDir := filepath.Join(sprawlRoot, ".sprawl", "runtime")
	if err := os.MkdirAll(logsDir, 0o755); err != nil { //nolint:gosec // G301: world-readable .sprawl dir is intentional
		return nil, fmt.Errorf("mkdir logs: %w", err)
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil { //nolint:gosec // G301: world-readable .sprawl dir is intentional
		return nil, fmt.Errorf("mkdir runtime: %w", err)
	}

	logPath := filepath.Join(logsDir, "mcp-calls.jsonl")
	if err := rotateIfNeeded(logPath); err != nil {
		return nil, fmt.Errorf("rotate log: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G302: world-readable log file is intentional
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}

	var fs fileSyncer = f
	if opts.SyncCounter != nil {
		fs = &syncCounter{f: f, counter: opts.SyncCounter}
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newID := opts.NewID
	if newID == nil {
		newID = defaultNewID
	}

	l := &Logger{
		file:    fs,
		logPath: logPath,
		regPath: filepath.Join(runtimeDir, "in-flight.json"),
		active:  map[string]*CallState{},
		now:     now,
		newID:   newID,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	return l, nil
}

// rotateIfNeeded renames logPath to logPath.1 when it exceeds maxLogBytes,
// shifting existing .1→.2…→.N within a maxRotations-deep ring and dropping
// anything older. Called only from openInternal so rotation never bisects an
// in-flight MCP call (QUM-502).
func rotateIfNeeded(logPath string) error {
	info, err := os.Stat(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() <= maxLogBytes {
		return nil
	}
	// Drop oldest generation, then shift each remaining .i → .i+1.
	oldest := fmt.Sprintf("%s.%d", logPath, maxRotations)
	if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove oldest: %w", err)
	}
	for i := maxRotations - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", logPath, i)
		dst := fmt.Sprintf("%s.%d", logPath, i+1)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("rotate %s: %w", src, err)
		}
	}
	if err := os.Rename(logPath, logPath+".1"); err != nil {
		return fmt.Errorf("rotate current: %w", err)
	}
	return nil
}

func defaultNewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// fallback deterministic-ish — better than panic
		return fmt.Sprintf("err%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// NewNoop returns a Logger whose methods are all no-ops.
func NewNoop() *Logger {
	return &Logger{noop: true}
}

// Close stops the heartbeat goroutine, flushes the registry, and closes
// the JSONL file. Idempotent.
func (l *Logger) Close() error {
	if l == nil || l.noop {
		return nil
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	stop := l.stop
	stopped := l.stopped
	file := l.file
	l.mu.Unlock()

	if stop != nil {
		close(stop)
	}
	if stopped != nil {
		<-stopped
	}

	if file != nil {
		_ = file.Sync()
		return file.Close()
	}
	return nil
}

// Begin records a start event.
func (l *Logger) Begin(ctx context.Context, tool, caller string, args any) (context.Context, string) {
	if l == nil || l.noop {
		return ctx, ""
	}
	id := l.newID()
	t := l.now()

	rec := map[string]any{
		"ts":      t.UTC().Format(time.RFC3339Nano),
		"call_id": id,
		"phase":   "start",
		"tool":    tool,
		"caller":  caller,
		"args":    args,
	}
	l.writeLine(rec)

	l.mu.Lock()
	l.active[id] = &CallState{
		CallID:        id,
		Tool:          tool,
		Caller:        caller,
		StartedAt:     t,
		LastHeartbeat: t,
	}
	l.mu.Unlock()

	l.writeRegistry()

	return WithCallID(ctx, id), id
}

// Checkpoint records a checkpoint event for an in-flight call.
func (l *Logger) Checkpoint(callID, step string, kv ...any) {
	if l == nil || l.noop {
		return
	}
	t := l.now()

	kvMap, err := kvToMap(kv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "calllog: checkpoint kv error: %v\n", err)
	}

	rec := map[string]any{
		"ts":      t.UTC().Format(time.RFC3339Nano),
		"call_id": callID,
		"phase":   "checkpoint",
		"step":    step,
	}
	if kvMap != nil {
		rec["kv"] = kvMap
	}
	l.writeLine(rec)

	l.mu.Lock()
	if cs, ok := l.active[callID]; ok {
		cs.CurrentStep = step
		cs.LastHeartbeat = t
	}
	l.mu.Unlock()

	l.writeRegistry()
}

// End records a terminal event for a call.
func (l *Logger) End(callID, status, errMsg string) {
	if l == nil || l.noop {
		return
	}
	t := l.now()

	l.mu.Lock()
	cs := l.active[callID]
	delete(l.active, callID)
	l.mu.Unlock()

	var dur float64
	if cs != nil {
		dur = t.Sub(cs.StartedAt).Seconds()
	}

	rec := map[string]any{
		"ts":         t.UTC().Format(time.RFC3339Nano),
		"call_id":    callID,
		"phase":      "end",
		"status":     status,
		"error":      errMsg,
		"duration_s": dur,
	}
	l.writeLine(rec)
	l.writeRegistry()
}

// CheckpointFn returns a closure that calls Checkpoint with callID pre-bound.
func (l *Logger) CheckpointFn(callID string) func(step string, kv ...any) {
	return func(step string, kv ...any) {
		l.Checkpoint(callID, step, kv...)
	}
}

func (l *Logger) writeLine(rec map[string]any) {
	data, err := json.Marshal(rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "calllog: marshal error: %v\n", err)
		return
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil || l.closed {
		return
	}
	if _, err := l.file.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "calllog: write error: %v\n", err)
		return
	}
	if err := l.file.Sync(); err != nil {
		fmt.Fprintf(os.Stderr, "calllog: sync error: %v\n", err)
	}
}

func (l *Logger) snapshotCalls() []CallState {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]CallState, 0, len(l.active))
	for _, cs := range l.active {
		out = append(out, *cs)
	}
	return out
}

func (l *Logger) writeRegistry() {
	if l == nil || l.noop || l.regPath == "" {
		return
	}
	calls := l.snapshotCalls()
	if calls == nil {
		calls = []CallState{}
	}
	payload := map[string]any{
		"ts":    l.now().UTC().Format(time.RFC3339Nano),
		"calls": calls,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "calllog: registry marshal: %v\n", err)
		return
	}
	tmp := l.regPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644) //nolint:gosec // G302: world-readable registry file is intentional
	if err != nil {
		fmt.Fprintf(os.Stderr, "calllog: registry open: %v\n", err)
		return
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "calllog: registry write: %v\n", err)
		return
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "calllog: registry sync: %v\n", err)
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "calllog: registry close: %v\n", err)
		return
	}
	if err := os.Rename(tmp, l.regPath); err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "calllog: registry rename: %v\n", err)
	}
}

// runHeartbeat is the test/production heartbeat goroutine. When tickReq
// is non-nil it is driven manually; otherwise it uses a 1s ticker.
func (l *Logger) runHeartbeat() {
	defer close(l.stopped)
	if l.tickReq != nil {
		for {
			select {
			case <-l.stop:
				return
			case done := <-l.tickReq:
				l.tickOnce()
				close(done)
			}
		}
	}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.tickOnce()
		}
	}
}

func (l *Logger) tickOnce() {
	t := l.now()
	l.mu.Lock()
	for _, cs := range l.active {
		cs.LastHeartbeat = t
	}
	l.mu.Unlock()
	l.writeRegistry()
}

func kvToMap(kv []any) (map[string]any, error) {
	if len(kv) == 0 {
		return nil, nil
	}
	if len(kv)%2 != 0 {
		return nil, fmt.Errorf("odd kv length: %d", len(kv))
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return m, fmt.Errorf("kv key at %d not a string: %T", i, kv[i])
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

// callIDKey is the unexported context key used by WithCallID/CallID.
type callIDKey struct{}

// WithCallID returns a derived context that carries the given call_id.
func WithCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callIDKey{}, id)
}

// CallID extracts a call_id from ctx (empty string if none).
func CallID(ctx context.Context) string {
	if v, ok := ctx.Value(callIDKey{}).(string); ok {
		return v
	}
	return ""
}
