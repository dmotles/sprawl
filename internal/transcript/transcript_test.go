package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testEnv is the on-disk envelope shape used to build fixtures. Seq is a
// pointer so a nil Seq (omitempty) reproduces a legacy seq-less line.
type testEnv struct {
	TS  string `json:"ts"`
	Dir string `json:"dir"`
	Seq *int64 `json:"seq,omitempty"`
	Raw string `json:"raw"`
}

func seqPtr(i int64) *int64 { return &i }

// ndjson marshals envelopes to NDJSON (one JSON object per physical line).
func ndjson(t *testing.T, envs ...testEnv) string {
	t.Helper()
	var b strings.Builder
	for _, e := range envs {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}

// blockTypes extracts the "type" of each accumulated content block.
func blockTypes(t *testing.T, blocks []json.RawMessage) []string {
	t.Helper()
	var out []string
	for _, blk := range blocks {
		var v struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(blk, &v); err != nil {
			t.Fatalf("unmarshal block %q: %v", string(blk), err)
		}
		out = append(out, v.Type)
	}
	return out
}

// T1: an OLD chunk-oriented file where a single logical frame is split across
// multiple same-direction records must reconstruct to 100% of frames with 0
// unparseable — the audit's B3 concatenate-then-split gotcha.
func TestParse_OldChunkFileMidFrameSplitsFullyRecovered(t *testing.T) {
	// One assistant frame split across two records, plus a clean result frame.
	content := ndjson(t,
		testEnv{TS: "t0", Dir: "out", Raw: `{"type":"assis`},
		testEnv{TS: "t1", Dir: "out", Raw: `tant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n"},
		testEnv{TS: "t2", Dir: "out", Raw: `{"type":"result","subtype":"success"}` + "\n"},
	)

	tr, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tr.Frames) != 2 {
		t.Fatalf("got %d frames, want 2 (concat-then-split): %+v", len(tr.Frames), tr.Frames)
	}
	for i, f := range tr.Frames {
		if f.ParseErr != nil {
			t.Errorf("frame %d ParseErr = %v, want nil (100%% recovery)", i, f.ParseErr)
		}
		if f.Msg == nil {
			t.Errorf("frame %d Msg = nil, want parsed message", i)
		}
	}
	if tr.Frames[0].Msg.Type != "assistant" {
		t.Errorf("frame 0 type = %q, want assistant", tr.Frames[0].Msg.Type)
	}
	if tr.Frames[1].Msg.Type != "result" {
		t.Errorf("frame 1 type = %q, want result", tr.Frames[1].Msg.Type)
	}
}

// T2: one message.id emitted as multiple frames (thinking, then text, then
// tool_use) MUST have ALL content blocks accumulated. A dedupe-by-id
// implementation keeps only the last frame and fails this test.
func TestParse_AssistantBlocksAccumulatedAcrossFrames_NoDedupeById(t *testing.T) {
	f1 := `{"type":"assistant","message":{"id":"m1","role":"assistant","model":"claude-x","content":[{"type":"thinking","thinking":"pondering"}]}}`
	f2 := `{"type":"assistant","message":{"id":"m1","role":"assistant","model":"claude-x","content":[{"type":"text","text":"here you go"}]}}`
	f3 := `{"type":"assistant","message":{"id":"m1","role":"assistant","model":"claude-x","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{}}]}}`
	content := ndjson(t,
		testEnv{TS: "t0", Dir: "out", Seq: seqPtr(1), Raw: f1 + "\n"},
		testEnv{TS: "t1", Dir: "out", Seq: seqPtr(2), Raw: f2 + "\n"},
		testEnv{TS: "t2", Dir: "out", Seq: seqPtr(3), Raw: f3 + "\n"},
	)

	tr, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tr.Messages) != 1 {
		t.Fatalf("got %d accumulated messages, want 1 (same message.id): %+v", len(tr.Messages), tr.Messages)
	}
	m := tr.Messages[0]
	if m.ID != "m1" {
		t.Errorf("message id = %q, want m1", m.ID)
	}
	if m.FrameCount != 3 {
		t.Errorf("FrameCount = %d, want 3", m.FrameCount)
	}
	if len(m.Blocks) != 3 {
		t.Fatalf("got %d accumulated blocks, want 3 (dedupe-by-id would drop text+thinking): %v", len(m.Blocks), blockTypes(t, m.Blocks))
	}
	// Blocks must be accumulated in arrival order (reconstruction fidelity).
	got := blockTypes(t, m.Blocks)
	want := []string{"thinking", "text", "tool_use"}
	if len(got) != len(want) {
		t.Fatalf("block types = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("block %d type = %q, want %q (order preserved)", i, got[i], want[i])
		}
	}
	if m.Model != "claude-x" {
		t.Errorf("Model = %q, want claude-x", m.Model)
	}
}

// TestParse_DistinctMessageIdsAreSeparated proves the accumulation grouping key
// actually SEPARATES different message.id values. An implementation that lumps
// all assistant frames into a single accumulated message passes every
// single-id test but fails this one.
func TestParse_DistinctMessageIdsAreSeparated(t *testing.T) {
	content := ndjson(t,
		testEnv{TS: "t0", Dir: "out", Seq: seqPtr(1), Raw: `{"type":"assistant","message":{"id":"m1","content":[{"type":"thinking","thinking":"x"}]}}` + "\n"},
		testEnv{TS: "t1", Dir: "out", Seq: seqPtr(2), Raw: `{"type":"assistant","message":{"id":"m2","content":[{"type":"text","text":"y"}]}}` + "\n"},
		testEnv{TS: "t2", Dir: "out", Seq: seqPtr(3), Raw: `{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"z"}]}}` + "\n"},
	)

	tr, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tr.Messages) != 2 {
		t.Fatalf("got %d accumulated messages, want 2 distinct ids", len(tr.Messages))
	}
	// First-seen order: m1 then m2.
	if tr.Messages[0].ID != "m1" || tr.Messages[1].ID != "m2" {
		t.Fatalf("message ids = [%q,%q], want [m1,m2] (first-seen order)", tr.Messages[0].ID, tr.Messages[1].ID)
	}
	if tr.Messages[0].FrameCount != 2 || len(tr.Messages[0].Blocks) != 2 {
		t.Errorf("m1 FrameCount=%d Blocks=%d, want 2 and 2", tr.Messages[0].FrameCount, len(tr.Messages[0].Blocks))
	}
	if tr.Messages[1].FrameCount != 1 || len(tr.Messages[1].Blocks) != 1 {
		t.Errorf("m2 FrameCount=%d Blocks=%d, want 1 and 1", tr.Messages[1].FrameCount, len(tr.Messages[1].Blocks))
	}
}

// TestParse_SameDirectionConcatWithInterleavedOtherDirection locks the
// "concatenate SAME-direction raw" rule. An "out" frame split across two "out"
// records with an "in" record interleaved between them must still reassemble
// cleanly; a naive impl that concatenates ALL raw globally corrupts the frame.
func TestParse_SameDirectionConcatWithInterleavedOtherDirection(t *testing.T) {
	content := ndjson(t,
		testEnv{TS: "t0", Dir: "out", Seq: seqPtr(1), Raw: `{"type":"assis`},
		testEnv{TS: "t1", Dir: "in", Seq: seqPtr(2), Raw: `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"},
		testEnv{TS: "t2", Dir: "out", Seq: seqPtr(3), Raw: `tant","message":{"id":"m1","content":[{"type":"text","text":"ok"}]}}` + "\n"},
	)

	tr, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tr.Frames) != 2 {
		t.Fatalf("got %d frames, want 2 (one per direction): %+v", len(tr.Frames), tr.Frames)
	}
	var sawAssistant, sawUser bool
	for _, f := range tr.Frames {
		if f.ParseErr != nil {
			t.Fatalf("frame dir=%s ParseErr = %v, want nil (same-direction reassembly)", f.Dir, f.ParseErr)
		}
		switch f.Msg.Type {
		case "assistant":
			sawAssistant = true
			if f.Dir != "out" {
				t.Errorf("assistant frame dir = %q, want out", f.Dir)
			}
		case "user":
			sawUser = true
			if f.Dir != "in" {
				t.Errorf("user frame dir = %q, want in", f.Dir)
			}
		}
	}
	if !sawAssistant || !sawUser {
		t.Errorf("missing frames: assistant=%v user=%v", sawAssistant, sawUser)
	}
}

// TestParse_EmptySegmentsDropped pins the empty-frame policy: adjacent newlines
// produce empty segments after concat+split, which must be dropped (mirroring
// protocol.Reader skipping blank lines), never surfaced as empty frames.
func TestParse_EmptySegmentsDropped(t *testing.T) {
	content := ndjson(t,
		testEnv{TS: "t0", Dir: "out", Seq: seqPtr(1), Raw: `{"type":"result"}` + "\n\n" + `{"type":"system","subtype":"init"}` + "\n"},
	)

	tr, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tr.Frames) != 2 {
		t.Fatalf("got %d frames, want 2 (empty segment dropped)", len(tr.Frames))
	}
}

// TestParse_MultiRecordFrameSeqProvenance pins Frame.Seq for a frame
// reassembled from multiple records to the FIRST contributing record's seq.
func TestParse_MultiRecordFrameSeqProvenance(t *testing.T) {
	content := ndjson(t,
		testEnv{TS: "t0", Dir: "out", Seq: seqPtr(7), Raw: `{"type":"assis`},
		testEnv{TS: "t1", Dir: "out", Seq: seqPtr(8), Raw: `tant","message":{"id":"m1","content":[{"type":"text","text":"ok"}]}}` + "\n"},
	)

	tr, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tr.Frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(tr.Frames))
	}
	if tr.Frames[0].Seq != 7 {
		t.Errorf("Frame.Seq = %d, want 7 (first contributing record)", tr.Frames[0].Seq)
	}
}

// T3: seq on a new frame-oriented file with multiple system/init frames (a
// resumed session) must be strictly increasing and continuous per frame.
func TestParse_SeqMonotonicAndContinuous(t *testing.T) {
	content := ndjson(t,
		testEnv{TS: "t0", Dir: "out", Seq: seqPtr(1), Raw: `{"type":"system","subtype":"init"}` + "\n"},
		testEnv{TS: "t1", Dir: "out", Seq: seqPtr(2), Raw: `{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"a"}]}}` + "\n"},
		testEnv{TS: "t2", Dir: "out", Seq: seqPtr(3), Raw: `{"type":"system","subtype":"init"}` + "\n"},
		testEnv{TS: "t3", Dir: "out", Seq: seqPtr(4), Raw: `{"type":"result"}` + "\n"},
	)

	tr, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tr.Frames) != 4 {
		t.Fatalf("got %d frames, want 4", len(tr.Frames))
	}
	for i, f := range tr.Frames {
		if want := int64(i + 1); f.Seq != want {
			t.Errorf("frame %d seq = %d, want %d (monotonic+continuous)", i, f.Seq, want)
		}
	}
}

// T4: a malformed frame among valid ones is isolated to Frame.ParseErr and does
// NOT abort parsing of the remaining valid frames.
func TestParse_MalformedFrameIsolatedDoesNotAbort(t *testing.T) {
	content := ndjson(t,
		testEnv{TS: "t0", Dir: "out", Seq: seqPtr(1), Raw: `{"type":"result"}` + "\n"},
		testEnv{TS: "t1", Dir: "out", Seq: seqPtr(2), Raw: `{this is not json` + "\n"},
		testEnv{TS: "t2", Dir: "out", Seq: seqPtr(3), Raw: `{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"ok"}]}}` + "\n"},
	)

	tr, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tr.Frames) != 3 {
		t.Fatalf("got %d frames, want 3 (malformed frame must not abort)", len(tr.Frames))
	}
	if tr.Frames[0].ParseErr != nil {
		t.Errorf("frame 0 ParseErr = %v, want nil", tr.Frames[0].ParseErr)
	}
	if tr.Frames[1].ParseErr == nil {
		t.Errorf("frame 1 ParseErr = nil, want non-nil (malformed)")
	}
	if tr.Frames[2].ParseErr != nil {
		t.Errorf("frame 2 ParseErr = %v, want nil", tr.Frames[2].ParseErr)
	}
	// The valid assistant frame after the bad one must still accumulate.
	if len(tr.Messages) != 1 {
		t.Errorf("got %d messages, want 1 (valid frame after malformed still parsed)", len(tr.Messages))
	}
}

// TestParse_TornEnvelopeLineSkipped asserts a torn/garbage envelope line in the
// MIDDLE of the file (e.g. a crash remnant fenced onto its own line) is skipped
// and the valid envelopes after it are still decoded and reconstructed — a
// streaming decoder that aborts at the first bad value would lose everything
// past the torn line.
func TestParse_TornEnvelopeLineSkipped(t *testing.T) {
	good1 := ndjson(t, testEnv{TS: "t0", Dir: "out", Seq: seqPtr(1), Raw: `{"type":"result"}` + "\n"})
	good2 := ndjson(t, testEnv{TS: "t2", Dir: "out", Seq: seqPtr(3), Raw: `{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"after"}]}}` + "\n"})
	content := good1 + `{"ts":"t1","dir":"out","seq":2,"raw":"{\"torn` + "\n" + good2

	envs, err := DecodeEnvelopes(strings.NewReader(content))
	if err != nil {
		t.Fatalf("DecodeEnvelopes: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("got %d envelopes, want 2 (torn line skipped)", len(envs))
	}

	tr, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(tr.Frames) != 2 {
		t.Fatalf("got %d frames, want 2 (recovered past torn line)", len(tr.Frames))
	}
	if len(tr.Messages) != 1 || tr.Messages[0].ID != "m1" {
		t.Fatalf("assistant after torn line not recovered: %+v", tr.Messages)
	}
}

// T5: a pre-existing seq-less chunk file (legacy) parses end-to-end: envelopes
// decode with a nil Seq, frames reconstruct, and assistant accumulation works.
func TestParse_LegacySeqlessChunkFileParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.ndjson")
	// Assistant frame split across two seq-less records + a user frame.
	content := ndjson(t,
		testEnv{TS: "t0", Dir: "out", Raw: `{"type":"assistant","message":{"id":"m9","role":"assistant","content":[{"type":"te`},
		testEnv{TS: "t1", Dir: "out", Raw: `xt","text":"legacy"}]}}` + "\n"},
	)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// DecodeEnvelopes should report Seq == nil for legacy lines.
	envs, err := DecodeEnvelopes(strings.NewReader(content))
	if err != nil {
		t.Fatalf("DecodeEnvelopes: %v", err)
	}
	for i, e := range envs {
		if e.Seq != nil {
			t.Errorf("legacy envelope %d Seq = %v, want nil", i, *e.Seq)
		}
	}

	tr, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(tr.Frames) != 1 {
		t.Fatalf("got %d frames, want 1 (reassembled from 2 chunks)", len(tr.Frames))
	}
	if tr.Frames[0].ParseErr != nil {
		t.Fatalf("frame ParseErr = %v, want nil", tr.Frames[0].ParseErr)
	}
	if len(tr.Messages) != 1 || tr.Messages[0].ID != "m9" {
		t.Fatalf("accumulation failed on legacy file: %+v", tr.Messages)
	}
	if len(tr.Messages[0].Blocks) != 1 {
		t.Errorf("got %d blocks, want 1", len(tr.Messages[0].Blocks))
	}
}
