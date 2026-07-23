// Package transcript reconstructs Claude protocol frames from a sprawl wire-log
// NDJSON file and accumulates multi-frame assistant messages.
//
// It reads both the current frame-oriented writer output (one envelope per
// newline-delimited frame, carrying a `seq`) and legacy chunk-oriented files
// (one envelope per io.TeeReader read chunk, no `seq`) transparently. The
// reconstruction rule is the audit's B3 fix: CONCATENATE all same-direction
// `raw` strings, THEN split on '\n'. Because every frame retains its trailing
// delimiter in `raw`, concatenation reproduces the original per-direction byte
// stream for both file regimes, so a frame split across chunk records
// reassembles to 100% with 0 lost.
//
// In stream-json mode one assistant message is emitted as MULTIPLE frames that
// share one message.id, each carrying ONE content block (thinking, then text,
// then each tool_use). This package ACCUMULATES all content blocks across
// frames with the same message.id — it must never dedupe-by-id, which would
// keep only the final tool_use and silently drop the text.
//
// This is a standalone, tested parser. It is deliberately NOT wired into the
// TUI render / replay path — that retirement is a later gated slice.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"

	"github.com/dmotles/sprawl/internal/protocol"
)

// Envelope is one decoded NDJSON line. Seq is nil for a legacy seq-less line.
type Envelope struct {
	TS  string
	Dir string
	Seq *int64
	Raw string
}

// Frame is one reconstructed protocol frame in a single direction. On a parse
// failure Msg is nil and ParseErr is set; parsing of other frames continues.
type Frame struct {
	Dir      string
	Seq      int64 // first contributing record's seq; 0 if that record was seq-less
	Msg      *protocol.Message
	Raw      []byte // the frame's bytes, whitespace-trimmed (delimiter + surrounding space removed)
	ParseErr error
}

// AssistantMessageAccum accumulates ALL content blocks across every frame
// sharing one message.id. Blocks are appended in arrival order and never
// deduped.
type AssistantMessageAccum struct {
	ID         string
	Role       string
	Model      string
	Blocks     []json.RawMessage
	FrameCount int
	StopReason string
}

// Transcript is the parsed result: reconstructed frames (per-direction order
// preserved, directions in first-appearance order) and accumulated assistant
// messages (in first-seen message.id order).
type Transcript struct {
	Frames   []Frame
	Messages []*AssistantMessageAccum
}

// DecodeEnvelopes decodes the NDJSON envelope stream line by line. Each
// envelope is exactly one physical '\n'-terminated line (the writer's
// json.Marshal escapes any newline inside raw), so line-delimited decoding is
// exact. It is best-effort: any line that fails to parse — a blank fence line
// or a torn crash remnant that may sit in the MIDDLE of the file after a
// resume — is skipped so that valid envelopes on both sides remain reachable.
// A streaming json.Decoder cannot do this: it aborts at the first malformed
// value and would silently drop everything after a mid-file torn line.
//
// There is no per-line size ceiling here (large base64 tool-result frames are
// legitimate); a runaway line grows memory, consistent with the wire log being
// an already-bounded, trusted local artifact.
func DecodeEnvelopes(r io.Reader) ([]Envelope, error) {
	br := bufio.NewReader(r)
	var out []Envelope
	for {
		line, rerr := br.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var raw struct {
				TS  string `json:"ts"`
				Dir string `json:"dir"`
				Seq *int64 `json:"seq"`
				Raw string `json:"raw"`
			}
			if json.Unmarshal(trimmed, &raw) == nil {
				out = append(out, Envelope{TS: raw.TS, Dir: raw.Dir, Seq: raw.Seq, Raw: raw.Raw})
			}
		}
		if rerr != nil {
			break
		}
	}
	return out, nil
}

// Parse decodes the wire-log envelopes from r and reconstructs the transcript.
func Parse(r io.Reader) (*Transcript, error) {
	envs, err := DecodeEnvelopes(r)
	if err != nil {
		return nil, err
	}
	return assemble(envs), nil
}

// ParseFile is Parse over a file path.
func ParseFile(path string) (*Transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// dirBuf holds one direction's concatenated bytes plus a parallel record of
// each contributing envelope's end offset and seq, so a reconstructed frame can
// be attributed to the seq of the first record that contributed bytes to it.
type dirBuf struct {
	data    []byte
	segEnds []int
	segSeqs []int64
}

func assemble(envs []Envelope) *Transcript {
	var dirOrder []string
	seen := make(map[string]bool)
	bufs := make(map[string]*dirBuf)

	for _, e := range envs {
		b := bufs[e.Dir]
		if b == nil {
			b = &dirBuf{}
			bufs[e.Dir] = b
			dirOrder = append(dirOrder, e.Dir)
			seen[e.Dir] = true
		}
		b.data = append(b.data, e.Raw...)
		var s int64
		if e.Seq != nil {
			s = *e.Seq
		}
		b.segEnds = append(b.segEnds, len(b.data))
		b.segSeqs = append(b.segSeqs, s)
	}

	var frames []Frame
	for _, dir := range dirOrder {
		frames = append(frames, framesForDir(dir, bufs[dir])...)
	}
	return &Transcript{Frames: frames, Messages: accumulate(frames)}
}

// framesForDir splits one direction's concatenated bytes on '\n' into frames,
// dropping empty/whitespace-only segments and attributing each frame's seq to
// the first envelope that contributed its starting byte.
func framesForDir(dir string, b *dirBuf) []Frame {
	var frames []Frame
	data := b.data
	n := len(data)
	segIdx := 0
	pos := 0
	for pos < n {
		nl := bytes.IndexByte(data[pos:], '\n')
		var end int
		var content []byte
		if nl < 0 {
			end = n
			content = data[pos:end]
		} else {
			end = pos + nl + 1
			content = data[pos : end-1] // exclude the delimiter
		}
		// First envelope contributing the byte at `pos`.
		for segIdx < len(b.segEnds) && b.segEnds[segIdx] <= pos {
			segIdx++
		}
		var seq int64
		if segIdx < len(b.segSeqs) {
			seq = b.segSeqs[segIdx]
		}
		if trimmed := bytes.TrimSpace(content); len(trimmed) > 0 {
			frames = append(frames, buildFrame(dir, seq, trimmed))
		}
		pos = end
	}
	return frames
}

// buildFrame parses one reconstructed frame's bytes into a protocol.Message.
func buildFrame(dir string, seq int64, frame []byte) Frame {
	raw := append([]byte(nil), frame...)
	var msg protocol.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return Frame{Dir: dir, Seq: seq, Raw: raw, ParseErr: err}
	}
	msg.Raw = json.RawMessage(raw)
	return Frame{Dir: dir, Seq: seq, Raw: raw, Msg: &msg}
}

// accumulate walks frames in order and merges assistant frames by message.id,
// appending every frame's content blocks (never dedupe-by-id).
func accumulate(frames []Frame) []*AssistantMessageAccum {
	var msgs []*AssistantMessageAccum
	byID := make(map[string]*AssistantMessageAccum)
	for i := range frames {
		f := &frames[i]
		if f.Msg == nil || f.Msg.Type != "assistant" {
			continue
		}
		var am protocol.AssistantMessage
		if err := protocol.ParseAs(f.Msg, &am); err != nil {
			continue
		}
		var inner struct {
			ID         string            `json:"id"`
			Role       string            `json:"role"`
			Model      string            `json:"model"`
			Content    []json.RawMessage `json:"content"`
			StopReason string            `json:"stop_reason"`
		}
		if err := json.Unmarshal(am.Content, &inner); err != nil {
			continue
		}
		acc := byID[inner.ID]
		if acc == nil {
			acc = &AssistantMessageAccum{ID: inner.ID, Role: inner.Role}
			byID[inner.ID] = acc
			msgs = append(msgs, acc)
		}
		acc.Blocks = append(acc.Blocks, inner.Content...)
		acc.FrameCount++
		if inner.Model != "" {
			acc.Model = inner.Model
		}
		if inner.StopReason != "" {
			acc.StopReason = inner.StopReason
		}
	}
	return msgs
}
