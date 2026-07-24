package hub

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
)

// defaultHeartbeatInterval is the on-stream DATA-heartbeat cadence. QUM-871
// found that a periodic DATA frame (NOT an HTTP/2 PING) keeps the browser
// server-stream alive past the Azure Container Apps / Envoy L7 idle timeout.
const defaultHeartbeatInterval = 20 * time.Second

// sessionKey identifies one wire-log stream. All three components are carried
// on both the uplink and the subscribe request, so keying on the full triple is
// unambiguous (a session_id alone could collide across hosts/runs). It is a
// comparable struct so it can be a map key without delimiter-injection games.
type sessionKey struct {
	hostID    string
	runID     string
	sessionID string
}

// frameSink is the write side a subscriber exposes. It is satisfied for free by
// *connect.ServerStream[hubv1.SubscribeWireLogResponse], so the connect-free
// Fanout core needs no connect dependency and stays unit-testable.
type frameSink interface {
	Send(*hubv1.SubscribeWireLogResponse) error
}

// subscriber is one live-tail registration. notify is a capacity-1 coalescing
// wake channel: Push does a non-blocking send onto it, and the subscriber's own
// goroutine drains the shared backlog when woken. A pending wake already means
// "new frames to drain," so a full channel is simply skipped.
type subscriber struct {
	notify chan struct{}
}

// sessionState is one session's in-memory backlog plus its live subscribers.
// The backlog is unbounded (QUM-909 P1: durable sink deferred, cold-join must
// replay full from seq 0) and append-only in ascending-seq order.
type sessionState struct {
	mu      sync.Mutex
	frames  []*hubv1.WireFrame
	lastSeq int64
	subs    map[*subscriber]struct{}
}

// Fanout is the connect-free hub wire-log core: a per-session in-memory backlog
// plus a subscriber registry with replay and live broadcast. Safe for
// concurrent use.
//
// Broadcast model: subscribers read the single shared backlog at their own
// pace, woken by a coalesced signal. There is no per-subscriber frame queue, so
// a slow browser cannot block ingest, starve peers, or force eviction — it just
// lags. Send is only ever called from a subscriber's own Subscribe goroutine,
// honoring connect's "Send is not safe for concurrent use" contract.
type Fanout struct {
	mu       sync.Mutex
	sessions map[sessionKey]*sessionState

	// heartbeatInterval and now are injectable for deterministic tests; the
	// constructor sets production defaults.
	heartbeatInterval time.Duration
	now               func() int64

	// log records forward-gap warnings. Defaults to a discard logger; the server
	// wires in the hub logger so gaps surface in hubd logs.
	log *slog.Logger
}

// NewFanout builds a Fanout with the production heartbeat cadence and a real
// unix-millisecond clock. Logging is discarded until a caller sets log.
func NewFanout() *Fanout {
	return &Fanout{
		sessions:          make(map[sessionKey]*sessionState),
		heartbeatInterval: defaultHeartbeatInterval,
		now:               func() int64 { return time.Now().UnixMilli() },
		log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// session returns the state for key, creating it on first use. Held only
// briefly; per-session work uses sessionState.mu, never Fanout.mu.
func (f *Fanout) session(key sessionKey) *sessionState {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.sessions[key]
	if st == nil {
		st = &sessionState{subs: make(map[*subscriber]struct{})}
		f.sessions[key] = st
	}
	return st
}

// Push appends the given frames to the session backlog, idempotent by seq: a
// frame whose seq is <= the highest stored seq is dropped (a re-uplink after a
// browser reconnect replays overlapping frames). Frames are expected in
// ascending seq order within a batch, and the host is expected to deliver a
// session's batches in seq order (single in-flight uplink per session — QUM-908
// owns that contract). Out-of-order or backward batches are treated as
// idempotent re-uplinks; the hub does not reorder or buffer (P1).
//
// Push returns true when it detects a FORWARD gap — the first newly-stored seq
// skips past lastSeq+1, so frames are missing from the backlog — and logs a
// warning so the gap is observable. Subscribers are woken non-blockingly.
func (f *Fanout) Push(key sessionKey, frames []*hubv1.WireFrame) (gap bool) {
	if len(frames) == 0 {
		return false
	}
	st := f.session(key)

	st.mu.Lock()
	defer st.mu.Unlock()
	oldLast := st.lastSeq
	appended := false
	var firstStored int64
	for _, fr := range frames {
		if fr.GetSeq() <= st.lastSeq {
			continue // idempotent: already stored
		}
		if !appended {
			firstStored = fr.GetSeq()
		}
		st.frames = append(st.frames, fr)
		st.lastSeq = fr.GetSeq()
		appended = true
	}
	if !appended {
		return false
	}
	// A forward gap only makes sense once the session has a prior high-water
	// mark; the first batch (oldLast == 0) is the stream origin, not a gap.
	if oldLast > 0 && firstStored > oldLast+1 {
		gap = true
		f.log.Warn("wire-log forward gap: frames missing from backlog",
			"host_id", key.hostID, "run_id", key.runID, "session_id", key.sessionID,
			"expected_seq", oldLast+1, "got_seq", firstStored)
	}
	for sub := range st.subs {
		select {
		case sub.notify <- struct{}{}:
		default: // a wake is already pending; coalesce
		}
	}
	return gap
}

// Subscribe replays backlog frames with seq > fromSeq (fromSeq=0 ⇒ full replay
// from the beginning), then live-tails newly pushed frames, emitting a
// HEARTBEAT frame every heartbeatInterval. It blocks until ctx is cancelled
// (returns nil) or a sink Send fails (returns that error), deregistering the
// subscriber on the way out.
func (f *Fanout) Subscribe(ctx context.Context, key sessionKey, fromSeq int64, sink frameSink) error {
	st := f.session(key)
	sub := &subscriber{notify: make(chan struct{}, 1)}

	// Register before computing the replay start index so any frame pushed
	// concurrently either lands in the initial drain or triggers a wake we then
	// service — never lost, never duplicated.
	st.mu.Lock()
	st.subs[sub] = struct{}{}
	// Backlog is seq-ascending and append-only, so the first index past fromSeq
	// is a stable cursor into the slice.
	idx := 0
	for idx < len(st.frames) && st.frames[idx].GetSeq() <= fromSeq {
		idx++
	}
	st.mu.Unlock()

	defer func() {
		st.mu.Lock()
		delete(st.subs, sub)
		st.mu.Unlock()
	}()

	ticker := time.NewTicker(f.heartbeatInterval)
	defer ticker.Stop()

	for {
		st.mu.Lock()
		batch := append([]*hubv1.WireFrame(nil), st.frames[idx:]...)
		idx = len(st.frames)
		st.mu.Unlock()

		for _, fr := range batch {
			if err := sink.Send(&hubv1.SubscribeWireLogResponse{Frame: fr}); err != nil {
				return err
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-sub.notify:
			// New frames appended; loop to drain them.
		case <-ticker.C:
			hb := &hubv1.SubscribeWireLogResponse{Frame: &hubv1.WireFrame{
				Kind:     hubv1.WireFrameKind_WIRE_FRAME_KIND_HEARTBEAT,
				TsUnixMs: f.now(),
			}}
			if err := sink.Send(hb); err != nil {
				return err
			}
		}
	}
}

// DebugSnapshot returns a JSON-friendly view of live fan-out state for
// /debug/state: one streams entry per session (backlog frame count + last seq)
// and one connections entry per session (live subscriber count). Both slices
// are non-nil so an idle hub renders as [] rather than null.
func (f *Fanout) DebugSnapshot() (streams, connections []map[string]any) {
	f.mu.Lock()
	keys := make([]sessionKey, 0, len(f.sessions))
	states := make([]*sessionState, 0, len(f.sessions))
	for k, st := range f.sessions {
		keys = append(keys, k)
		states = append(states, st)
	}
	f.mu.Unlock()

	streams = make([]map[string]any, 0, len(keys))
	connections = make([]map[string]any, 0, len(keys))
	for i, k := range keys {
		st := states[i]
		st.mu.Lock()
		frameCount := len(st.frames)
		lastSeq := st.lastSeq
		subCount := len(st.subs)
		st.mu.Unlock()

		streams = append(streams, map[string]any{
			"host_id":     k.hostID,
			"run_id":      k.runID,
			"session_id":  k.sessionID,
			"frame_count": frameCount,
			"last_seq":    lastSeq,
		})
		connections = append(connections, map[string]any{
			"host_id":     k.hostID,
			"run_id":      k.runID,
			"session_id":  k.sessionID,
			"subscribers": subCount,
		})
	}
	return streams, connections
}
