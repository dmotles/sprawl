package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
)

// fakeSink implements frameSink for deterministic unit tests. Send pushes the
// response's frame onto ch. If sendErr is set, Send returns it without
// enqueuing.
type fakeSink struct {
	ch      chan *hubv1.WireFrame
	sendErr error
}

func newFakeSink(buf int) *fakeSink {
	return &fakeSink{ch: make(chan *hubv1.WireFrame, buf)}
}

func (s *fakeSink) Send(resp *hubv1.SubscribeWireLogResponse) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	s.ch <- resp.GetFrame()
	return nil
}

// recvFrame reads one frame from ch or fails after a short timeout.
func recvFrame(t *testing.T, ch <-chan *hubv1.WireFrame) *hubv1.WireFrame {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a frame")
		return nil
	}
}

// noFrameWithin asserts no frame arrives on ch within d.
func noFrameWithin(t *testing.T, ch <-chan *hubv1.WireFrame, d time.Duration) {
	t.Helper()
	select {
	case f := <-ch:
		t.Fatalf("unexpected frame seq=%d", f.GetSeq())
	case <-time.After(d):
	}
}

// waitSubscribers blocks until the given session reports exactly want live
// subscribers, or fails. This replaces sleep-based registration ordering: it
// deterministically establishes that a subscriber goroutine has registered
// before the test pushes, so live-tail (not replay) is what gets exercised.
func waitSubscribers(t *testing.T, f *Fanout, sessionID string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, connections := f.DebugSnapshot()
		for _, c := range connections {
			if c["session_id"] == sessionID {
				if n, _ := c["subscribers"].(int); n == want {
					return
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session %q never reached %d subscribers", sessionID, want)
}

func dataFrame(seq int64) *hubv1.WireFrame {
	return &hubv1.WireFrame{
		Seq:       seq,
		Direction: hubv1.WireDirection_WIRE_DIRECTION_OUT,
		Kind:      hubv1.WireFrameKind_WIRE_FRAME_KIND_DATA,
		Raw:       "frame",
		TsUnixMs:  seq,
	}
}

func dataFrames(seqs ...int64) []*hubv1.WireFrame {
	out := make([]*hubv1.WireFrame, 0, len(seqs))
	for _, s := range seqs {
		out = append(out, dataFrame(s))
	}
	return out
}

var testKey = sessionKey{hostID: "h", runID: "r", sessionID: "s"}

// TestFanout_PushStoresAndIdempotent proves frames are stored keyed by seq and
// that re-pushing seqs <= lastSeq is a no-op (no duplicate storage), verified by
// a fresh full-replay subscriber seeing each seq exactly once.
func TestFanout_PushStoresAndIdempotent(t *testing.T) {
	f := NewFanout()
	f.Push(testKey, dataFrames(1, 2, 3))
	f.Push(testKey, dataFrames(2, 3)) // dup — must be dropped
	f.Push(testKey, dataFrames(3))    // dup — must be dropped

	sink := newFakeSink(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.Subscribe(ctx, testKey, 0, sink) }()

	got := []int64{
		recvFrame(t, sink.ch).GetSeq(),
		recvFrame(t, sink.ch).GetSeq(),
		recvFrame(t, sink.ch).GetSeq(),
	}
	want := []int64{1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("replay[%d] = %d, want %d (got %v)", i, got[i], want[i], got)
		}
	}
	// No 4th data frame (heartbeats excluded by default 20s cadence).
	noFrameWithin(t, sink.ch, 100*time.Millisecond)
}

// TestFanout_ForwardGapDetected: Push reports (and logs) a forward gap when a
// batch's first newly-stored seq skips past lastSeq+1, so a real backlog gap is
// observable. A contiguous append, a cold start from a non-zero seq, and an
// overlapping re-uplink must NOT be flagged.
func TestFanout_ForwardGapDetected(t *testing.T) {
	f := NewFanout()
	if gap := f.Push(testKey, dataFrames(1, 2)); gap {
		t.Fatal("contiguous initial push flagged a gap")
	}
	if gap := f.Push(testKey, dataFrames(3)); gap {
		t.Fatal("contiguous follow-on push flagged a gap")
	}
	if gap := f.Push(testKey, dataFrames(6, 7)); !gap {
		t.Fatal("forward gap (missing 4,5) not detected")
	}
	// An overlapping/backward re-uplink stores nothing new — not a gap.
	if gap := f.Push(testKey, dataFrames(5, 6, 7)); gap {
		t.Fatal("overlapping re-uplink flagged a gap")
	}

	// Cold start from a non-zero seq (e.g. host resumes after a hub restart) is
	// the stream origin, not a gap.
	cold := NewFanout()
	if gap := cold.Push(testKey, dataFrames(500, 501)); gap {
		t.Fatal("cold start from seq 500 wrongly flagged as a gap")
	}
}

// TestFanout_ReplayFromZero: FromSeq=0 replays the whole backlog in seq order.
func TestFanout_ReplayFromZero(t *testing.T) {
	f := NewFanout()
	f.Push(testKey, dataFrames(1, 2, 3))

	sink := newFakeSink(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.Subscribe(ctx, testKey, 0, sink) }()

	for _, want := range []int64{1, 2, 3} {
		if got := recvFrame(t, sink.ch).GetSeq(); got != want {
			t.Fatalf("replay seq = %d, want %d", got, want)
		}
	}
}

// TestFanout_ReplayDelta: FromSeq=N replays only frames with seq > N.
func TestFanout_ReplayDelta(t *testing.T) {
	f := NewFanout()
	f.Push(testKey, dataFrames(1, 2, 3, 4))

	sink := newFakeSink(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.Subscribe(ctx, testKey, 2, sink) }()

	for _, want := range []int64{3, 4} {
		if got := recvFrame(t, sink.ch).GetSeq(); got != want {
			t.Fatalf("delta replay seq = %d, want %d", got, want)
		}
	}
	noFrameWithin(t, sink.ch, 100*time.Millisecond)
}

// TestFanout_LiveTailAfterReplay: a subscriber on an empty session receives
// frames pushed after it connected.
func TestFanout_LiveTailAfterReplay(t *testing.T) {
	f := NewFanout()
	sink := newFakeSink(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.Subscribe(ctx, testKey, 0, sink) }()

	// Deterministically wait for registration so the backlog is provably empty
	// at subscribe time — this exercises the LIVE-tail path, not replay.
	waitSubscribers(t, f, "s", 1)
	f.Push(testKey, dataFrames(1))
	if got := recvFrame(t, sink.ch).GetSeq(); got != 1 {
		t.Fatalf("live seq = %d, want 1", got)
	}
}

// TestFanout_ConcurrentSubscribersAllReceive (AC5): every live frame reaches
// every subscriber on the session.
func TestFanout_ConcurrentSubscribersAllReceive(t *testing.T) {
	f := NewFanout()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sinks := []*fakeSink{newFakeSink(8), newFakeSink(8), newFakeSink(8)}
	for _, s := range sinks {
		go func() { _ = f.Subscribe(ctx, testKey, 0, s) }()
	}
	// All three registered (backlog empty) before the push → live fan-out path.
	waitSubscribers(t, f, "s", 3)
	f.Push(testKey, dataFrames(1, 2))

	for i, s := range sinks {
		for _, want := range []int64{1, 2} {
			if got := recvFrame(t, s.ch).GetSeq(); got != want {
				t.Fatalf("subscriber %d seq = %d, want %d", i, got, want)
			}
		}
	}
}

// TestFanout_ReplayLiveBoundaryNoDupNoGap: a push racing with a new
// subscription must yield each seq exactly once across the replay->live seam.
func TestFanout_ReplayLiveBoundaryNoDupNoGap(t *testing.T) {
	f := NewFanout()
	f.Push(testKey, dataFrames(1, 2, 3))

	sink := newFakeSink(16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.Subscribe(ctx, testKey, 0, sink) }()
	// Race a dup (3) + a new frame (4) against registration.
	f.Push(testKey, dataFrames(3, 4))

	for _, want := range []int64{1, 2, 3, 4} {
		if got := recvFrame(t, sink.ch).GetSeq(); got != want {
			t.Fatalf("boundary seq = %d, want %d", got, want)
		}
	}
	noFrameWithin(t, sink.ch, 100*time.Millisecond)
}

// TestFanout_Heartbeat (AC2): idle subscribers get on-stream HEARTBEAT frames,
// re-stamped from the clock each beat, carrying no seq, at the configured
// cadence (asserted via an elapsed lower bound so a broken back-to-back emit is
// caught while remaining robust to slow machines).
func TestFanout_Heartbeat(t *testing.T) {
	const interval = 20 * time.Millisecond
	f := NewFanout()
	f.heartbeatInterval = interval
	// Advancing clock: each call returns a strictly larger value so we can prove
	// every beat re-stamps ts rather than reusing a fixed value.
	var clock int64
	f.now = func() int64 { clock += 1000; return clock }

	sink := newFakeSink(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	go func() { _ = f.Subscribe(ctx, testKey, 0, sink) }()

	var lastTs int64
	for i := 0; i < 2; i++ {
		hb := recvFrame(t, sink.ch)
		if hb.GetKind() != hubv1.WireFrameKind_WIRE_FRAME_KIND_HEARTBEAT {
			t.Fatalf("heartbeat %d kind = %v, want HEARTBEAT", i, hb.GetKind())
		}
		if hb.GetSeq() != 0 {
			t.Fatalf("heartbeat %d seq = %d, want 0 (not a backlog entry)", i, hb.GetSeq())
		}
		if hb.GetTsUnixMs() <= lastTs {
			t.Fatalf("heartbeat %d ts = %d, not advancing past %d (clock not re-read)", i, hb.GetTsUnixMs(), lastTs)
		}
		lastTs = hb.GetTsUnixMs()
	}
	// Two beats at `interval` cadence cannot arrive in under one interval; a
	// broken emitter firing back-to-back would finish near-instantly.
	if elapsed := time.Since(start); elapsed < interval {
		t.Fatalf("2 heartbeats took %v, want >= %v (cadence not honored)", elapsed, interval)
	}
}

// TestFanout_SubscribeReturnsOnCtxCancel: cancelling ctx returns nil and
// deregisters the subscriber.
func TestFanout_SubscribeReturnsOnCtxCancel(t *testing.T) {
	f := NewFanout()
	sink := newFakeSink(8)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- f.Subscribe(ctx, testKey, 0, sink) }()
	waitSubscribers(t, f, "s", 1)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Subscribe returned %v on ctx cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after ctx cancel")
	}

	_, connections := f.DebugSnapshot()
	for _, c := range connections {
		if n, _ := c["subscribers"].(int); n != 0 {
			t.Fatalf("subscribers = %d after cancel, want 0", n)
		}
	}
}

// TestFanout_SlowSubscriberDoesNotBlockIngestOrPeers: a subscriber that reads
// slowly must not block Push nor starve a healthy co-subscriber. The shared
// backlog + coalesced wakeup means the slow reader simply lags; every frame is
// still delivered to it (unbounded backlog, no eviction, no drops).
func TestFanout_SlowSubscriberDoesNotBlockIngestOrPeers(t *testing.T) {
	f := NewFanout()

	// Healthy subscriber has ample buffer; slow subscriber's sink buffer is tiny
	// so its send loop lags well behind ingest.
	healthy := newFakeSink(128)
	slow := newFakeSink(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.Subscribe(ctx, testKey, 0, healthy) }()
	go func() { _ = f.Subscribe(ctx, testKey, 0, slow) }()
	waitSubscribers(t, f, "s", 2)

	// Push must never block regardless of the slow reader.
	const n = 50
	for i := int64(1); i <= n; i++ {
		f.Push(testKey, dataFrames(i))
	}

	// Healthy subscriber receives every frame in order, unaffected by the peer.
	for i := int64(1); i <= n; i++ {
		if got := recvFrame(t, healthy.ch).GetSeq(); got != i {
			t.Fatalf("healthy seq = %d, want %d", got, i)
		}
	}

	// The slow subscriber still eventually receives every frame in order — no
	// drops, no gaps.
	for i := int64(1); i <= n; i++ {
		if got := recvFrame(t, slow.ch).GetSeq(); got != i {
			t.Fatalf("slow seq = %d, want %d", got, i)
		}
	}
}

// TestFanout_SendErrorDeregisters: a Send error during REPLAY is surfaced from
// Subscribe and the subscriber is torn down.
func TestFanout_SendErrorDeregisters(t *testing.T) {
	f := NewFanout()
	wantErr := errors.New("boom")
	sink := &fakeSink{ch: make(chan *hubv1.WireFrame, 8), sendErr: wantErr}
	f.Push(testKey, dataFrames(1))

	err := f.Subscribe(context.Background(), testKey, 0, sink)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Subscribe err = %v, want %v", err, wantErr)
	}
	_, connections := f.DebugSnapshot()
	for _, c := range connections {
		if n, _ := c["subscribers"].(int); n != 0 {
			t.Fatalf("subscribers = %d after send error, want 0 (deregistered)", n)
		}
	}
}

// TestFanout_LiveSendErrorDeregisters: a Send error on the LIVE-tail path (a
// frame pushed after registration) is surfaced and deregisters the subscriber.
func TestFanout_LiveSendErrorDeregisters(t *testing.T) {
	f := NewFanout()
	wantErr := errors.New("live-boom")
	// Empty session: no replay, so the only Send is for the live frame.
	sink := &fakeSink{ch: make(chan *hubv1.WireFrame, 8), sendErr: wantErr}

	done := make(chan error, 1)
	go func() { done <- f.Subscribe(context.Background(), testKey, 0, sink) }()
	waitSubscribers(t, f, "s", 1)
	f.Push(testKey, dataFrames(1))

	select {
	case err := <-done:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Subscribe err = %v, want %v", err, wantErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after live send error")
	}
	_, connections := f.DebugSnapshot()
	for _, c := range connections {
		if n, _ := c["subscribers"].(int); n != 0 {
			t.Fatalf("subscribers = %d after live send error, want 0", n)
		}
	}
}

// TestFanout_DebugSnapshot (AC4): stream/connection state is reported per
// session with frame counts, last seq, and subscriber counts.
func TestFanout_DebugSnapshot(t *testing.T) {
	f := NewFanout()
	keyA := sessionKey{hostID: "h", runID: "r", sessionID: "a"}
	keyB := sessionKey{hostID: "h", runID: "r", sessionID: "b"}
	f.Push(keyA, dataFrames(1, 2))
	f.Push(keyB, dataFrames(1, 2, 3))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = f.Subscribe(ctx, keyA, 0, newFakeSink(8)) }()
	go func() { _ = f.Subscribe(ctx, keyA, 0, newFakeSink(8)) }()
	waitSubscribers(t, f, "a", 2)

	streams, connections := f.DebugSnapshot()
	if len(streams) != 2 {
		t.Fatalf("streams = %d, want 2", len(streams))
	}
	if len(connections) != 2 {
		t.Fatalf("connections = %d, want 2", len(connections))
	}

	for _, s := range streams {
		if s["session_id"] == "a" {
			if fc, _ := s["frame_count"].(int); fc != 2 {
				t.Errorf("stream a frame_count = %v, want 2", s["frame_count"])
			}
			if ls, _ := s["last_seq"].(int64); ls != 2 {
				t.Errorf("stream a last_seq = %v, want 2", s["last_seq"])
			}
		}
	}
	for _, c := range connections {
		if c["session_id"] == "a" {
			if n, _ := c["subscribers"].(int); n != 2 {
				t.Errorf("session a subscribers = %v, want 2", c["subscribers"])
			}
		}
	}
}
