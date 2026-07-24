package hubtail

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
)

// fakePusher records every PushWireLog request (even failed ones) and can be
// scripted to fail its first failN calls with err.
type fakePusher struct {
	mu    sync.Mutex
	reqs  []*hubv1.PushWireLogRequest
	auth  []string // Authorization header seen per call
	failN int
	err   error
	calls chan struct{} // signaled (non-blocking) after each call, for Run tests
}

func (f *fakePusher) PushWireLog(_ context.Context, req *connect.Request[hubv1.PushWireLogRequest]) (*connect.Response[hubv1.PushWireLogResponse], error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req.Msg)
	f.auth = append(f.auth, req.Header().Get("Authorization"))
	fail := f.failN > 0
	if fail {
		f.failN--
	}
	f.mu.Unlock()
	if f.calls != nil {
		select {
		case f.calls <- struct{}{}:
		default:
		}
	}
	if fail {
		return nil, f.err
	}
	return connect.NewResponse(&hubv1.PushWireLogResponse{}), nil
}

func (f *fakePusher) requests() []*hubv1.PushWireLogRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*hubv1.PushWireLogRequest, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// allFrames flattens every WireFrame from every recorded request in call order.
func (f *fakePusher) allFrames() []*hubv1.WireFrame {
	var out []*hubv1.WireFrame
	for _, r := range f.requests() {
		out = append(out, r.GetFrames()...)
	}
	return out
}

type env struct {
	ts  string
	dir string
	seq int64
	raw string
}

func marshalEnv(t *testing.T, e env) []byte {
	t.Helper()
	line, err := json.Marshal(struct {
		TS  string `json:"ts"`
		Dir string `json:"dir"`
		Seq int64  `json:"seq"`
		Raw string `json:"raw"`
	}{e.ts, e.dir, e.seq, e.raw})
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

// writeLog writes envs as frame-oriented NDJSON matching the wire-log writer.
func writeLog(t *testing.T, path string, envs ...env) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range envs {
		b.Write(marshalEnv(t, e))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendLog(t *testing.T, path string, envs ...env) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range envs {
		if _, err := f.Write(marshalEnv(t, e)); err != nil {
			t.Fatal(err)
		}
	}
}

func newTestTailer(client Pusher, cfg Config) *Tailer {
	if cfg.HostID == "" {
		cfg.HostID = "host_x"
	}
	if cfg.RunID == "" {
		cfg.RunID = "run_x"
	}
	return New(client, cfg)
}

// TestShipSession_ShipsVerbatimSeq: frames ship in file order with VERBATIM,
// non-1-based, non-contiguous seqs (never renumbered), direction mapped, kind
// DATA, ts converted to epoch-ms, from_seq starts at 0, auth header set.
func TestShipSession_ShipsVerbatimSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess1.ndjson")
	writeLog(t, path,
		env{"2026-07-24T00:00:00.500Z", "out", 5, "a\n"},
		env{"2026-07-24T00:00:01.000Z", "in", 8, "b\n"},
		env{"2026-07-24T00:00:02.000Z", "out", 13, "c\n"},
	)
	fake := &fakePusher{}
	tl := newTestTailer(fake, Config{HostID: "host_h", RunID: "run_r", Bearer: "tok123"})

	n, err := tl.ShipSession(context.Background(), "sess1", path)
	if err != nil {
		t.Fatalf("ShipSession err: %v", err)
	}
	if n != 3 {
		t.Fatalf("shipped = %d, want 3", n)
	}
	frames := fake.allFrames()
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	// VERBATIM seq: the exact source seqs (5,8,13), NOT a renumbered 1,2,3.
	wantSeq := []int64{5, 8, 13}
	wantRaw := []string{"a\n", "b\n", "c\n"}
	wantDir := []hubv1.WireDirection{
		hubv1.WireDirection_WIRE_DIRECTION_OUT,
		hubv1.WireDirection_WIRE_DIRECTION_IN,
		hubv1.WireDirection_WIRE_DIRECTION_OUT,
	}
	for i, f := range frames {
		if f.GetSeq() != wantSeq[i] {
			t.Errorf("frame %d seq = %d, want %d (verbatim, not renumbered)", i, f.GetSeq(), wantSeq[i])
		}
		if f.GetRaw() != wantRaw[i] {
			t.Errorf("frame %d raw = %q, want %q", i, f.GetRaw(), wantRaw[i])
		}
		if f.GetDirection() != wantDir[i] {
			t.Errorf("frame %d dir = %v, want %v", i, f.GetDirection(), wantDir[i])
		}
		if f.GetKind() != hubv1.WireFrameKind_WIRE_FRAME_KIND_DATA {
			t.Errorf("frame %d kind = %v, want DATA", i, f.GetKind())
		}
	}
	// ts_unix_ms is the exact epoch-ms of the envelope ts (catches sec-vs-ms
	// unit bugs and a now()-fallback).
	wantMS := time.Date(2026, 7, 24, 0, 0, 0, 500*int(time.Millisecond), time.UTC).UnixMilli()
	if got := frames[0].GetTsUnixMs(); got != wantMS {
		t.Errorf("frame 0 ts_unix_ms = %d, want %d", got, wantMS)
	}
	// First request identity + from_seq.
	reqs := fake.requests()
	first := reqs[0]
	if first.GetFromSeq() != 0 {
		t.Errorf("first from_seq = %d, want 0", first.GetFromSeq())
	}
	if first.GetHostId() != "host_h" || first.GetRunId() != "run_r" || first.GetSessionId() != "sess1" {
		t.Errorf("identity = (%q,%q,%q)", first.GetHostId(), first.GetRunId(), first.GetSessionId())
	}
	if fake.auth[0] != "Bearer tok123" {
		t.Errorf("auth = %q, want %q", fake.auth[0], "Bearer tok123")
	}
	if tl.Cursor() != 13 {
		t.Errorf("cursor = %d, want 13", tl.Cursor())
	}
}

// TestShipSession_ResumeNoDupesOnAppend: after shipping 5,8,13, appending 21,34
// and re-shipping sends ONLY 21,34 with from_seq==13 (no gaps, no dupes).
func TestShipSession_ResumeNoDupesOnAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.ndjson")
	writeLog(t, path,
		env{"2026-07-24T00:00:00Z", "out", 5, "a\n"},
		env{"2026-07-24T00:00:00Z", "in", 8, "b\n"},
		env{"2026-07-24T00:00:00Z", "out", 13, "c\n"},
	)
	fake := &fakePusher{}
	tl := newTestTailer(fake, Config{})
	if _, err := tl.ShipSession(context.Background(), "s", path); err != nil {
		t.Fatal(err)
	}
	appendLog(t, path,
		env{"2026-07-24T00:00:00Z", "in", 21, "d\n"},
		env{"2026-07-24T00:00:00Z", "out", 34, "e\n"},
	)
	n, err := tl.ShipSession(context.Background(), "s", path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("second ship n = %d, want 2", n)
	}
	// Total frames across all requests must be exactly 5,8,13,21,34 once (no dupes).
	frames := fake.allFrames()
	wantAll := []int64{5, 8, 13, 21, 34}
	if len(frames) != len(wantAll) {
		t.Fatalf("total frames = %d, want %d", len(frames), len(wantAll))
	}
	for i, f := range frames {
		if f.GetSeq() != wantAll[i] {
			t.Fatalf("frame %d seq = %d, want %d", i, f.GetSeq(), wantAll[i])
		}
	}
	// The resume batch (the second ShipSession's single request) must declare
	// from_seq == 13. Assert unconditionally on the last request.
	reqs := fake.requests()
	last := reqs[len(reqs)-1]
	if last.GetFromSeq() != 13 {
		t.Errorf("resume batch from_seq = %d, want 13", last.GetFromSeq())
	}
	if len(last.GetFrames()) != 2 || last.GetFrames()[0].GetSeq() != 21 {
		t.Errorf("resume batch frames = %v, want [21,34]", last.GetFrames())
	}
}

// TestShipSession_RetryFromCursorOnError: an RPC failure leaves the cursor
// un-advanced; the next ShipSession re-ships from the same from_seq (resume).
func TestShipSession_RetryFromCursorOnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.ndjson")
	writeLog(t, path,
		env{"2026-07-24T00:00:00Z", "out", 5, "a\n"},
		env{"2026-07-24T00:00:00Z", "in", 8, "b\n"},
		env{"2026-07-24T00:00:00Z", "out", 13, "c\n"},
	)
	fake := &fakePusher{failN: 1, err: connect.NewError(connect.CodeUnavailable, os.ErrDeadlineExceeded)}
	tl := newTestTailer(fake, Config{})

	n, err := tl.ShipSession(context.Background(), "s", path)
	if err == nil {
		t.Fatal("expected error on first ship")
	}
	if n != 0 {
		t.Errorf("shipped on failed ship = %d, want 0", n)
	}
	if tl.Cursor() != 0 {
		t.Errorf("cursor after failure = %d, want 0 (un-advanced)", tl.Cursor())
	}

	// Retry succeeds and resumes from from_seq==0 with all three frames.
	n, err = tl.ShipSession(context.Background(), "s", path)
	if err != nil {
		t.Fatalf("retry err: %v", err)
	}
	if n != 3 {
		t.Fatalf("retry shipped = %d, want 3", n)
	}
	if tl.Cursor() != 13 {
		t.Errorf("cursor after retry = %d, want 13", tl.Cursor())
	}
	reqs := fake.requests()
	last := reqs[len(reqs)-1]
	if last.GetFromSeq() != 0 {
		t.Errorf("retry from_seq = %d, want 0", last.GetFromSeq())
	}
}

// TestShipSession_MissingFileNoOp: a not-yet-created wire log is a clean no-op.
func TestShipSession_MissingFileNoOp(t *testing.T) {
	fake := &fakePusher{}
	tl := newTestTailer(fake, Config{})
	n, err := tl.ShipSession(context.Background(), "s", filepath.Join(t.TempDir(), "nope.ndjson"))
	if err != nil {
		t.Fatalf("missing file should be no-op, got err: %v", err)
	}
	if n != 0 {
		t.Errorf("shipped = %d, want 0", n)
	}
	if len(fake.requests()) != 0 {
		t.Errorf("made %d requests, want 0", len(fake.requests()))
	}
}

// TestShipSession_EmptyFileNoOp: a present but empty wire log ships nothing.
func TestShipSession_EmptyFileNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.ndjson")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakePusher{}
	tl := newTestTailer(fake, Config{})
	n, err := tl.ShipSession(context.Background(), "s", path)
	if err != nil {
		t.Fatalf("empty file should be no-op, got err: %v", err)
	}
	if n != 0 || len(fake.requests()) != 0 {
		t.Errorf("empty file shipped %d frames in %d requests, want 0/0", n, len(fake.requests()))
	}
}

// TestShipSession_SessionChangeResetsCursor: a changed session id re-targets the
// tailer and resets the cursor to 0.
func TestShipSession_SessionChangeResetsCursor(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "A.ndjson")
	pathB := filepath.Join(dir, "B.ndjson")
	writeLog(t, pathA,
		env{"2026-07-24T00:00:00Z", "out", 3, "a\n"},
		env{"2026-07-24T00:00:00Z", "in", 7, "b\n"},
	)
	writeLog(t, pathB,
		env{"2026-07-24T00:00:00Z", "out", 2, "x\n"},
		env{"2026-07-24T00:00:00Z", "in", 4, "y\n"},
		env{"2026-07-24T00:00:00Z", "out", 9, "z\n"},
	)
	fake := &fakePusher{}
	tl := newTestTailer(fake, Config{})
	if _, err := tl.ShipSession(context.Background(), "A", pathA); err != nil {
		t.Fatal(err)
	}
	if tl.Cursor() != 7 {
		t.Fatalf("cursor after A = %d, want 7", tl.Cursor())
	}
	n, err := tl.ShipSession(context.Background(), "B", pathB)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("session B shipped = %d, want 3", n)
	}
	if tl.Cursor() != 9 {
		t.Errorf("cursor after B = %d, want 9", tl.Cursor())
	}
	// The B batch must be under session id "B" with from_seq 0 (cursor reset).
	reqs := fake.requests()
	last := reqs[len(reqs)-1]
	if last.GetSessionId() != "B" || last.GetFromSeq() != 0 {
		t.Errorf("B batch session=%q from_seq=%d, want (B,0)", last.GetSessionId(), last.GetFromSeq())
	}
}

// TestShipSession_BoundedBatchByCount: MaxBatch caps frames per RPC; from_seq
// advances per batch, carrying verbatim seqs.
func TestShipSession_BoundedBatchByCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.ndjson")
	writeLog(t, path,
		env{"2026-07-24T00:00:00Z", "out", 5, "a\n"},
		env{"2026-07-24T00:00:00Z", "in", 8, "b\n"},
		env{"2026-07-24T00:00:00Z", "out", 13, "c\n"},
		env{"2026-07-24T00:00:00Z", "in", 21, "d\n"},
		env{"2026-07-24T00:00:00Z", "out", 34, "e\n"},
	)
	fake := &fakePusher{}
	tl := newTestTailer(fake, Config{MaxBatch: 2})
	n, err := tl.ShipSession(context.Background(), "s", path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("shipped = %d, want 5", n)
	}
	reqs := fake.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3 (2+2+1)", len(reqs))
	}
	wantSizes := []int{2, 2, 1}
	wantFromSeq := []int64{0, 8, 21} // last seq of the preceding batch
	for i, r := range reqs {
		if len(r.GetFrames()) != wantSizes[i] {
			t.Errorf("batch %d size = %d, want %d", i, len(r.GetFrames()), wantSizes[i])
		}
		if r.GetFromSeq() != wantFromSeq[i] {
			t.Errorf("batch %d from_seq = %d, want %d", i, r.GetFromSeq(), wantFromSeq[i])
		}
	}
}

// TestShipSession_BoundedBatchByBytes: cumulative raw bytes cap splits batches.
// The byte accounting meters len(raw) only (documented in the implementation).
func TestShipSession_BoundedBatchByBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.ndjson")
	big := strings.Repeat("x", 30) + "\n" // 31 bytes of raw
	writeLog(t, path,
		env{"2026-07-24T00:00:00Z", "out", 5, big},
		env{"2026-07-24T00:00:00Z", "in", 8, big},
		env{"2026-07-24T00:00:00Z", "out", 13, big},
	)
	fake := &fakePusher{}
	// Cap ~ two frames' worth of raw bytes (2*31=62 <= 65 < 3*31=93).
	tl := newTestTailer(fake, Config{MaxBatchBytes: 65})
	n, err := tl.ShipSession(context.Background(), "s", path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("shipped = %d, want 3", n)
	}
	reqs := fake.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (byte-capped)", len(reqs))
	}
	if len(reqs[0].GetFrames()) != 2 || len(reqs[1].GetFrames()) != 1 {
		t.Errorf("byte-cap batch sizes = %d,%d, want 2,1", len(reqs[0].GetFrames()), len(reqs[1].GetFrames()))
	}
}

// TestShipSession_OversizeFrameSkippedOnFailure: a single frame larger than the
// byte cap is shipped alone; if that RPC fails it is logged, skipped, and the
// cursor advances past it so the tail never wedges.
func TestShipSession_OversizeFrameSkippedOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.ndjson")
	oversize := strings.Repeat("z", 100) + "\n"
	writeLog(t, path,
		env{"2026-07-24T00:00:00Z", "out", 5, oversize},
		env{"2026-07-24T00:00:00Z", "in", 8, "b\n"},
	)
	// Fail only the first call (the oversize frame); the follow-on normal frame
	// then succeeds.
	fake := &fakePusher{failN: 1, err: connect.NewError(connect.CodeResourceExhausted, os.ErrInvalid)}
	tl := newTestTailer(fake, Config{MaxBatchBytes: 10})
	n, err := tl.ShipSession(context.Background(), "s", path)
	if err != nil {
		t.Fatalf("oversize skip should not surface an error, got: %v", err)
	}
	if n != 1 {
		t.Fatalf("shipped = %d, want 1 (only seq 8)", n)
	}
	if tl.Cursor() != 8 {
		t.Errorf("cursor = %d, want 8 (advanced past skipped oversize)", tl.Cursor())
	}
	// The oversize frame must have been sent ALONE in its own (failed) request
	// before the follow-on normal frame's request.
	reqs := fake.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (oversize-alone + normal)", len(reqs))
	}
	if len(reqs[0].GetFrames()) != 1 || reqs[0].GetFrames()[0].GetSeq() != 5 {
		t.Errorf("first request = %v, want [seq 5] alone", reqs[0].GetFrames())
	}
	if len(reqs[1].GetFrames()) != 1 || reqs[1].GetFrames()[0].GetSeq() != 8 {
		t.Errorf("second request = %v, want [seq 8]", reqs[1].GetFrames())
	}
}

// TestShipSession_OversizeFrameTransientErrorNotDropped: a TRANSIENT error on an
// oversize-alone frame must NOT skip/drop it (that would break the no-gaps AC for
// large-but-shippable frames) — the error surfaces and the cursor stays put so
// the next ShipSession retries the same frame.
func TestShipSession_OversizeFrameTransientErrorNotDropped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.ndjson")
	oversize := strings.Repeat("z", 100) + "\n"
	writeLog(t, path,
		env{"2026-07-24T00:00:00Z", "out", 5, oversize},
		env{"2026-07-24T00:00:00Z", "in", 8, "b\n"},
	)
	fake := &fakePusher{failN: 1, err: connect.NewError(connect.CodeUnavailable, os.ErrDeadlineExceeded)}
	tl := newTestTailer(fake, Config{MaxBatchBytes: 10})
	n, err := tl.ShipSession(context.Background(), "s", path)
	if err == nil {
		t.Fatal("transient error on oversize frame must surface, not be swallowed as a skip")
	}
	if n != 0 {
		t.Errorf("shipped = %d, want 0 (nothing succeeded)", n)
	}
	if tl.Cursor() != 0 {
		t.Errorf("cursor = %d, want 0 (oversize frame NOT dropped on transient error)", tl.Cursor())
	}

	// Retry (fake now healthy) ships both frames, verbatim, no drop.
	n, err = tl.ShipSession(context.Background(), "s", path)
	if err != nil {
		t.Fatalf("retry err: %v", err)
	}
	if n != 2 {
		t.Fatalf("retry shipped = %d, want 2", n)
	}
	if tl.Cursor() != 8 {
		t.Errorf("cursor after retry = %d, want 8", tl.Cursor())
	}
}

// TestRun_ShipsThenStopsOnCancel: Run polls, ships via the resolver, and returns
// promptly when the context is canceled.
func TestRun_ShipsThenStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "sess.ndjson"),
		env{"2026-07-24T00:00:00Z", "out", 5, "a\n"},
		env{"2026-07-24T00:00:00Z", "in", 8, "b\n"},
	)
	fake := &fakePusher{calls: make(chan struct{}, 8)}
	tl := newTestTailer(fake, Config{PollInterval: 5 * time.Millisecond})

	resolve := func() (string, error) { return "sess", nil }
	pathFor := func(id string) string { return filepath.Join(dir, id+".ndjson") }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tl.Run(ctx, resolve, pathFor)
		close(done)
	}()

	select {
	case <-fake.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("Run never shipped")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
	if len(fake.requests()) == 0 {
		t.Fatal("expected at least one shipped batch")
	}
}

// TestRun_SwallowsErrorsAndKeepsPolling: a permanently-failing hub must never
// panic the Run loop; it keeps polling (multiple attempts) and still stops on
// cancel — the "live session unaffected by hub outage" AC.
func TestRun_SwallowsErrorsAndKeepsPolling(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "sess.ndjson"),
		env{"2026-07-24T00:00:00Z", "out", 5, "a\n"},
	)
	fake := &fakePusher{
		calls: make(chan struct{}, 16),
		failN: 1 << 30, // effectively always fail
		err:   connect.NewError(connect.CodeUnavailable, os.ErrDeadlineExceeded),
	}
	tl := newTestTailer(fake, Config{PollInterval: 5 * time.Millisecond})
	resolve := func() (string, error) { return "sess", nil }
	pathFor := func(id string) string { return filepath.Join(dir, id+".ndjson") }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tl.Run(ctx, resolve, pathFor)
		close(done)
	}()

	// Observe at least two polling attempts despite every RPC failing.
	for i := 0; i < 2; i++ {
		select {
		case <-fake.calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("Run stopped polling after %d failed attempts", i)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}
