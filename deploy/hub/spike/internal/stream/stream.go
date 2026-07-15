// Package stream holds the tiny, transport-agnostic downlink log used by the
// QUM-871 spike: an append-only sequence of frames plus the from_seq resume
// rule. Keeping this pure (no protobuf, no network) makes the reconnect "one
// rule" — zero gaps, zero dupes — offline-unit-testable.
package stream

import "sync"

// FrameKind distinguishes a keep-alive heartbeat from an application DATA frame.
type FrameKind int

const (
	KindHeartbeat FrameKind = iota
	KindData
)

// Frame is one seq'd downlink entry. Seq is a per-Log monotonic counter from 1.
type Frame struct {
	Seq      uint64
	Kind     FrameKind
	Payload  string
	TSUnixMs int64
}

// Log is an append-only, concurrency-safe frame log with monotonic sequencing.
// The server appends heartbeats + data frames and replays on reconnect.
type Log struct {
	mu     sync.Mutex
	frames []Frame
	seq    uint64
}

// Append assigns the next seq, records the frame, and returns it.
func (l *Log) Append(kind FrameKind, payload string, tsUnixMs int64) Frame {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	f := Frame{Seq: l.seq, Kind: kind, Payload: payload, TSUnixMs: tsUnixMs}
	l.frames = append(l.frames, f)
	return f
}

// Since returns a snapshot of every frame with Seq > fromSeq (the resume tail).
func (l *Log) Since(fromSeq uint64) []Frame {
	l.mu.Lock()
	defer l.mu.Unlock()
	return FramesSince(fromSeq, l.frames)
}

// FramesSince is the pure resume rule: the frames of log whose Seq > fromSeq,
// in order. log is assumed append-ordered with strictly increasing Seq, so the
// result is contiguous and starts at fromSeq+1 (zero gaps, zero dupes).
func FramesSince(fromSeq uint64, log []Frame) []Frame {
	out := make([]Frame, 0, len(log))
	for _, f := range log {
		if f.Seq > fromSeq {
			out = append(out, f)
		}
	}
	return out
}
