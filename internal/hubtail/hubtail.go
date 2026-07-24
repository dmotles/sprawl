// Package hubtail follows the durable seq'd wire log written by
// internal/backend/claude (QUM-902) and ships each frame to the hub via the
// generated PushWireLog uplink (QUM-907). It is the HOST side of the wire-log
// uplink (QUM-908).
//
// Design contract:
//   - The wire `seq` is carried VERBATIM end to end — the tailer never
//     re-numbers. It uses transcript.DecodeEnvelopes so it frames exactly as the
//     shared reassembly parser does (it does not re-implement framing). This
//     assumes the current frame-oriented writer regime, where one NDJSON
//     envelope is exactly one complete '\n'-terminated frame; a legacy
//     chunk-oriented file (which needs cross-record reassembly) would
//     under-assemble, but `sprawl enter` only ever produces the frame-oriented
//     form.
//   - Resilient: the in-memory cursor is the last-shipped seq. A batch RPC that
//     fails leaves the cursor un-advanced, so the next poll re-ships from the
//     same point — delivery is at-least-once and the hub is expected to dedupe
//     by (session_id, seq). On a session-id change the cursor resets to 0.
//   - Best-effort: every error is swallowed/returned without ever touching the
//     live session. The tailer only reads the durable log.
package hubtail

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/dmotles/sprawl/internal/hub/gen/hub/v1"
	"github.com/dmotles/sprawl/internal/transcript"
)

// Default batching / polling bounds.
const (
	defaultMaxBatch      = 512
	defaultMaxBatchBytes = 3 << 20 // 3 MiB, headroom under connect's 4 MiB default
	defaultPollInterval  = time.Second
)

// Pusher is the subset of the generated hub client the tailer needs. The real
// hubv1connect.HubServiceClient satisfies it.
type Pusher interface {
	PushWireLog(context.Context, *connect.Request[hubv1.PushWireLogRequest]) (*connect.Response[hubv1.PushWireLogResponse], error)
}

// Config holds the static shipping parameters. Zero values for MaxBatch,
// MaxBatchBytes and PollInterval fall back to package defaults; a nil Log
// discards.
type Config struct {
	HostID        string
	RunID         string
	Bearer        string
	MaxBatch      int
	MaxBatchBytes int
	PollInterval  time.Duration
	Log           io.Writer
}

// Tailer ships a single live session's wire log to the hub, re-targeting when
// the session id changes. It is not safe for concurrent use; Run owns it.
type Tailer struct {
	client Pusher
	cfg    Config

	sessionID string // the session the cursor belongs to
	cursor    int64  // last-shipped seq for sessionID
}

// New builds a Tailer, applying defaults for unset Config bounds.
func New(client Pusher, cfg Config) *Tailer {
	if cfg.MaxBatch <= 0 {
		cfg.MaxBatch = defaultMaxBatch
	}
	if cfg.MaxBatchBytes <= 0 {
		cfg.MaxBatchBytes = defaultMaxBatchBytes
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	return &Tailer{client: client, cfg: cfg}
}

// Cursor returns the last-shipped seq for the current session.
func (t *Tailer) Cursor() int64 { return t.cursor }

// Run polls at PollInterval, resolving the live session id and its wire-log
// path each tick and shipping any new frames. Errors are logged and swallowed —
// a hub outage never affects the live session. Returns when ctx is canceled.
func (t *Tailer) Run(ctx context.Context, resolve func() (string, error), pathFor func(string) string) {
	ticker := time.NewTicker(t.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			id, err := resolve()
			if err != nil {
				t.logf("hub tail: resolve session failed: %v", err)
				continue
			}
			if id == "" {
				continue // session not started yet
			}
			if _, err := t.ShipSession(ctx, id, pathFor(id)); err != nil {
				t.logf("hub tail: ship failed (will retry): %v", err)
			}
		}
	}
}

// ShipSession decodes the wire log at path and ships every frame with seq beyond
// the cursor to the hub, advancing the cursor per successfully-shipped batch. A
// missing/empty file is a clean no-op. If sessionID differs from the tailer's
// current session the cursor resets to 0 (re-target). It returns the number of
// frames shipped and the first RPC error that halted shipping (cursor left at
// the last succeeded seq so the next call resumes).
func (t *Tailer) ShipSession(ctx context.Context, sessionID, path string) (int, error) {
	if sessionID == "" {
		return 0, nil
	}
	if sessionID != t.sessionID {
		t.sessionID = sessionID
		t.cursor = 0
	}

	envs, err := decodeFile(path)
	if err != nil {
		return 0, err
	}

	pending := t.pending(envs)
	if len(pending) == 0 {
		return 0, nil
	}

	shipped := 0
	for i := 0; i < len(pending); {
		batch, next := t.nextBatch(pending, i)
		oversize := len(batch) == 1 && len(batch[0].Raw) > t.cfg.MaxBatchBytes
		lastSeq := *batch[len(batch)-1].Seq

		if err := t.push(ctx, t.cursor, batch); err != nil {
			if oversize && isPermanentSizeError(err) {
				// A single frame the hub PERMANENTLY rejects for size can never
				// succeed on retry: skip it and advance so the tail does not
				// wedge forever. A transient error (unavailable/timeout) on the
				// same frame falls through to the retry path — it may still be
				// shippable, and dropping it would violate the no-gaps contract.
				t.logf("hub tail: skipping un-shippable oversize frame seq=%d (%d bytes): %v",
					lastSeq, len(batch[0].Raw), err)
				t.cursor = lastSeq
				i = next
				continue
			}
			return shipped, err
		}
		t.cursor = lastSeq
		shipped += len(batch)
		i = next
	}
	return shipped, nil
}

// pending returns the envelopes with a seq strictly greater than the cursor, in
// ascending seq order. Seqless (legacy) envelopes are dropped — this tailer only
// serves the seq'd frame-oriented regime.
func (t *Tailer) pending(envs []transcript.Envelope) []transcript.Envelope {
	out := make([]transcript.Envelope, 0, len(envs))
	for _, e := range envs {
		if e.Seq != nil && *e.Seq > t.cursor {
			out = append(out, e)
		}
	}
	// DecodeEnvelopes preserves file (append/seq) order; sort defensively so a
	// non-monotonic file can never produce an out-of-order or gap-mislabeled
	// batch.
	sort.SliceStable(out, func(i, j int) bool { return *out[i].Seq < *out[j].Seq })
	return out
}

// nextBatch collects a batch starting at index start, bounded by MaxBatch frames
// and MaxBatchBytes cumulative raw bytes. A single frame larger than the byte
// cap is returned alone (it cannot be split). Returns the batch and the index to
// resume from.
func (t *Tailer) nextBatch(pending []transcript.Envelope, start int) ([]transcript.Envelope, int) {
	bytes := 0
	i := start
	for i < len(pending) {
		raw := len(pending[i].Raw)
		if i > start { // batch already holds >=1 frame
			if i-start >= t.cfg.MaxBatch || bytes+raw > t.cfg.MaxBatchBytes {
				break
			}
		}
		bytes += raw
		i++
	}
	return pending[start:i], i
}

// push builds and sends one PushWireLogRequest for batch with the given
// from_seq. The bearer token is attached per-request (mirrors hub.RegisterHost).
func (t *Tailer) push(ctx context.Context, fromSeq int64, batch []transcript.Envelope) error {
	frames := make([]*hubv1.WireFrame, len(batch))
	for i, e := range batch {
		frames[i] = &hubv1.WireFrame{
			Seq:       *e.Seq,
			Direction: mapDirection(e.Dir),
			Kind:      hubv1.WireFrameKind_WIRE_FRAME_KIND_DATA,
			Raw:       e.Raw,
			TsUnixMs:  parseTS(e.TS),
		}
	}
	req := connect.NewRequest(&hubv1.PushWireLogRequest{
		HostId:    t.cfg.HostID,
		RunId:     t.cfg.RunID,
		SessionId: t.sessionID,
		Frames:    frames,
		FromSeq:   fromSeq,
	})
	if t.cfg.Bearer != "" {
		req.Header().Set("Authorization", "Bearer "+t.cfg.Bearer)
	}
	_, err := t.client.PushWireLog(ctx, req)
	return err
}

func (t *Tailer) logf(format string, args ...any) {
	fmt.Fprintf(t.cfg.Log, format+"\n", args...)
}

// decodeFile opens path and decodes its NDJSON envelopes. A missing file is a
// clean no-op ((nil, nil)) — the wire log may not exist yet.
func decodeFile(path string) ([]transcript.Envelope, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	return transcript.DecodeEnvelopes(f)
}

// isPermanentSizeError reports whether err is a hub rejection that a retry can
// never fix — a message-size / argument rejection. Everything else (transport
// unavailable, deadline exceeded, unknown) is treated as transient/retryable.
func isPermanentSizeError(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeResourceExhausted, connect.CodeInvalidArgument:
		return true
	default:
		return false
	}
}

// mapDirection maps the wire-log dir tag to the proto enum. "out" is claude
// stdout, "in" is claude stdin; anything else is UNSPECIFIED.
func mapDirection(dir string) hubv1.WireDirection {
	switch dir {
	case "in":
		return hubv1.WireDirection_WIRE_DIRECTION_IN
	case "out":
		return hubv1.WireDirection_WIRE_DIRECTION_OUT
	default:
		return hubv1.WireDirection_WIRE_DIRECTION_UNSPECIFIED
	}
}

// parseTS converts an RFC3339Nano wire-log timestamp to epoch-ms, or 0 if it
// cannot be parsed (ts_unix_ms is optional/best-effort).
func parseTS(ts string) int64 {
	if ts == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}
