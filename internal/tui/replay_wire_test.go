package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// wireEnv is one on-disk wire-log envelope for test fixtures. It mirrors the
// writer's shape in internal/backend/claude/wirelog.go: one JSON object per
// physical line, carrying a direction, a monotonic seq, and the raw frame
// bytes (delimiter retained inside raw).
type wireEnv struct {
	TS  string `json:"ts"`
	Dir string `json:"dir"`
	Seq int64  `json:"seq"`
	Raw string `json:"raw"`
}

// writeWireEnvelopes writes explicit envelopes to a .ndjson wire-log file and
// returns the path. Use this when a test needs to control direction, seq, or
// frame-splitting precisely.
func writeWireEnvelopes(t *testing.T, envs []wireEnv) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.ndjson")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range envs {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode envelope: %v", err)
		}
	}
	return path
}

// wireEnvelopeBytes wraps each conversation-record JSON string as a single
// "out" wire envelope (seq starting at 1, raw = record + "\n") and returns the
// concatenated NDJSON bytes. Used by fixtures that must write to a specific
// resolved wire-log path rather than a temp file.
func wireEnvelopeBytes(t *testing.T, records []string) []byte {
	t.Helper()
	var out []byte
	for i, r := range records {
		env, err := json.Marshal(wireEnv{
			TS:  "2026-07-24T00:00:00.000000000Z",
			Dir: "out",
			Seq: int64(i + 1),
			Raw: r + "\n",
		})
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		out = append(out, env...)
		out = append(out, '\n')
	}
	return out
}

// writeWireLog wraps each conversation-record JSON string as a single "out"
// wire envelope (seq starting at 1, raw = record + "\n") and writes the
// resulting .ndjson wire log. This is the wire-log analogue of writeJSONL: the
// "out" direction of a real wire log carries Claude's stdout conversation
// stream, whose record shape is identical to the retired Claude-JSONL shape.
func writeWireLog(t *testing.T, records []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.ndjson")
	if err := os.WriteFile(path, wireEnvelopeBytes(t, records), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoadTranscriptFromWire_BasicUserAssistant(t *testing.T) {
	path := writeWireLog(t, []string{
		`{"type":"user","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi there"}]}}`,
	})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	if entries[0].Type != MessageUser || entries[0].Content != "hello" {
		t.Errorf("entry[0] = %+v, want user 'hello'", entries[0])
	}
	if entries[1].Type != MessageAssistant || entries[1].Content != "hi there" {
		t.Errorf("entry[1] = %+v, want assistant 'hi there'", entries[1])
	}
}

// TestLoadTranscriptFromWire_ReplayPromptRendersAsUser proves the real typed
// prompt — which appears on the wire "out" direction ONLY as an isReplay echo
// carrying the string content — renders as a normal user bubble.
func TestLoadTranscriptFromWire_ReplayPromptRendersAsUser(t *testing.T) {
	path := writeWireLog(t, []string{
		`{"type":"user","message":{"role":"user","content":"do the thing"},"uuid":"u1","isReplay":true}`,
	})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Type != MessageUser || entries[0].Content != "do the thing" {
		t.Fatalf("got %+v, want single user 'do the thing'", entries)
	}
}

// TestLoadTranscriptFromWire_DroppedLiveFrameReappears is the phantom-message
// golden: the wire log has a seq gap (frame seq 3 was dropped on the live
// render seam), but the persisted assistant frame at seq 4 must still
// rehydrate because rehydration re-reads the authoritative on-disk wire log.
func TestLoadTranscriptFromWire_DroppedLiveFrameReappears(t *testing.T) {
	path := writeWireEnvelopes(t, []wireEnv{
		{TS: "2026-07-24T00:00:00.000000000Z", Dir: "out", Seq: 1, Raw: `{"type":"user","message":{"role":"user","content":"q"}}` + "\n"},
		{TS: "2026-07-24T00:00:00.000000000Z", Dir: "out", Seq: 2, Raw: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}` + "\n"},
		// seq 3 intentionally absent — dropped on the live seam.
		{TS: "2026-07-24T00:00:00.000000000Z", Dir: "out", Seq: 4, Raw: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"recovered"}]}}` + "\n"},
	})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Assert the FULL ordered set rehydrates — not just that "recovered"
	// exists — so a spurious drop or reorder of the surviving frames also
	// fails, proving the read is a faithful re-read of the on-disk log.
	want := []struct {
		typ MessageType
		txt string
	}{
		{MessageUser, "q"},
		{MessageAssistant, "first"},
		{MessageAssistant, "recovered"},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i, w := range want {
		if entries[i].Type != w.typ || entries[i].Content != w.txt {
			t.Fatalf("entry[%d] = %+v, want {%v %q}", i, entries[i], w.typ, w.txt)
		}
	}
}

// TestLoadTranscriptFromWire_TornMiddleLineSkipped mirrors DecodeEnvelopes'
// best-effort contract: a torn/garbage envelope in the middle is skipped while
// valid frames on both sides survive.
func TestLoadTranscriptFromWire_TornMiddleLineSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.ndjson")
	content := `{"ts":"t","dir":"out","seq":1,"raw":"{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"before\"}}\n"}` + "\n" +
		`{"ts":"t","dir":"out",gar` + "\n" +
		`{"ts":"t","dir":"out","seq":3,"raw":"{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"after\"}]}}\n"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 || entries[0].Content != "before" || entries[1].Content != "after" {
		t.Fatalf("got %+v, want before/after with torn middle skipped", entries)
	}
}

// TestLoadTranscriptFromWire_MultiFrameAssistantSplitRaw exercises the
// transcript parser's concat-then-split reassembly: one assistant protocol
// frame split across TWO out envelopes (raws concatenate to the full frame +
// '\n') must reassemble into one MessageAssistant entry.
func TestLoadTranscriptFromWire_MultiFrameAssistantSplitRaw(t *testing.T) {
	full := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"split message"}]}}`
	half := len(full) / 2
	// A frame split mid-way across envelopes only occurs in the LEGACY
	// chunk-oriented writer, whose envelopes carry no seq (decode as 0). The
	// current frame-oriented writer buffers per direction until '\n', so it
	// never emits a partial-frame envelope. Model the legacy regime here.
	path := writeWireEnvelopes(t, []wireEnv{
		{TS: "2026-07-24T00:00:00.000000000Z", Dir: "out", Seq: 0, Raw: full[:half]},
		{TS: "2026-07-24T00:00:00.000000000Z", Dir: "out", Seq: 0, Raw: full[half:] + "\n"},
	})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Type != MessageAssistant || entries[0].Content != "split message" {
		t.Fatalf("got %+v, want single assistant 'split message'", entries)
	}
}

// TestLoadTranscriptFromWire_MultiFrameAssistantOneBlockPerFrame reproduces the
// stream-json shape where one assistant message.id is emitted as MULTIPLE
// frames each with ONE content block (thinking, then text, then tool_use).
// Iterating out-frames in order and emitting a MessageEntry per block must
// yield: thinking skipped, then MessageAssistant, then MessageToolCall.
func TestLoadTranscriptFromWire_MultiFrameAssistantOneBlockPerFrame(t *testing.T) {
	path := writeWireLog(t, []string{
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","content":[{"type":"thinking","thinking":"hmm"}]}}`,
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","content":[{"type":"text","text":"answer"}]}}`,
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","content":[{"type":"tool_use","id":"tool_1","name":"Read","input":{"file_path":"/x"}}]}}`,
	})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (thinking skipped): %+v", len(entries), entries)
	}
	if entries[0].Type != MessageAssistant || entries[0].Content != "answer" {
		t.Errorf("entry[0] = %+v, want assistant 'answer'", entries[0])
	}
	if entries[1].Type != MessageToolCall || entries[1].ToolID != "tool_1" {
		t.Errorf("entry[1] = %+v, want tool call tool_1", entries[1])
	}
}

// TestLoadTranscriptFromWire_IgnoresInDirection asserts stdin ("in") frames
// never produce entries — only the "out" conversation stream is rendered.
func TestLoadTranscriptFromWire_IgnoresInDirection(t *testing.T) {
	path := writeWireEnvelopes(t, []wireEnv{
		{TS: "2026-07-24T00:00:00.000000000Z", Dir: "in", Seq: 1, Raw: `{"type":"user","message":{"role":"user","content":"sent on stdin"}}` + "\n"},
		{TS: "2026-07-24T00:00:00.000000000Z", Dir: "out", Seq: 2, Raw: `{"type":"user","message":{"role":"user","content":"echoed on stdout"},"isReplay":true}` + "\n"},
	})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "echoed on stdout" {
		t.Fatalf("got %+v, want only the out-direction echo", entries)
	}
}

// TestLoadTranscriptFromWire_SkipsSidechain asserts that on the root replay
// path (includeSidechain=false via LoadTranscript), a frame carrying a
// parent_tool_use_id — the wire-native marker of sidechain activity, since the
// wire has no top-level isSidechain field — is skipped.
func TestLoadTranscriptFromWire_SkipsSidechain(t *testing.T) {
	path := writeWireLog(t, []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"main"}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"inner"}]},"parent_tool_use_id":"toolu_outer"}`,
	})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "main" {
		t.Fatalf("got %+v, want only main-convo entry (sidechain skipped)", entries)
	}
}

// TestLoadChildTranscriptFromWire_SidechainLinkageFromParentToolID is the
// sidechain-linkage test for the child-tail path (includeSidechain=true). Two
// parallel Agent tool calls; each inner sidechain tool_use carries an explicit
// parent_tool_use_id that must drive ParentToolID/Depth — NOT the agentStack
// heuristic, which would misattribute parallel sidechains.
func TestLoadChildTranscriptFromWire_SidechainLinkageFromParentToolID(t *testing.T) {
	// QUM-928: sidechain frames are suppressed by default, so this
	// invariant (explicit parent_tool_use_id attribution with concurrent
	// sidechains — never a last-agent fallback) is now reachable only via
	// the debug hatch. Retained because that code path still ships.
	withSidechainVisible(t, true)
	path := writeWireLog(t, []string{
		// Two Agent tool calls in the main conversation.
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"agentA","name":"Agent","input":{}}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"agentB","name":"Agent","input":{}}]},"parent_tool_use_id":null}`,
		// Inner sidechain activity, interleaved, each tagged with its wire parent.
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"inB","name":"Read","input":{"file_path":"/b"}}]},"parent_tool_use_id":"agentB"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"inA","name":"Read","input":{"file_path":"/a"}}]},"parent_tool_use_id":"agentA"}`,
	})
	entries, err := LoadChildTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byID := map[string]MessageEntry{}
	for _, e := range entries {
		if e.Type == MessageToolCall {
			byID[e.ToolID] = e
		}
	}
	if e, ok := byID["inA"]; !ok || e.ParentToolID != "agentA" {
		t.Errorf("inA parent = %q (found=%v), want agentA", e.ParentToolID, ok)
	}
	if e, ok := byID["inB"]; !ok || e.ParentToolID != "agentB" {
		t.Errorf("inB parent = %q (found=%v), want agentB", e.ParentToolID, ok)
	}
	if e := byID["inA"]; e.Depth < 1 {
		t.Errorf("inA depth = %d, want >=1", e.Depth)
	}
}

// TestLoadChildTranscriptFromWire_RendersTimestamplessAssistant guards the
// removal of the QUM-331 `since` filter: the vast majority of assistant
// out-frames carry NO top-level timestamp, so a timestamp-based filter would
// drop them. The child tail must render them.
func TestLoadChildTranscriptFromWire_RendersTimestamplessAssistant(t *testing.T) {
	path := writeWireLog(t, []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"no timestamp here"}]}}`,
	})
	entries, err := LoadChildTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "no timestamp here" {
		t.Fatalf("got %+v, want the timestampless assistant message", entries)
	}
}

// TestLoadTranscriptFromWire_SuppressesCompactContinuation: the wire has no
// isCompactSummary flag; the giant context-compaction continuation appears as a
// synthetic user out-frame whose content begins with the known preamble. It
// must be suppressed on replay (the first-party compaction banner replaces it).
func TestLoadTranscriptFromWire_SuppressesCompactContinuation(t *testing.T) {
	// Uses the real preamble constant so the test and impl cannot drift and
	// both stay pinned to the string Claude actually emits (verified against a
	// captured wire log: weave/def3f46f… — isSynthetic:true, isReplay:false).
	content := compactContinuationPreamble + " The summary below covers the earlier portion.\n\nSummary:\n1. Primary Request"
	rec := map[string]any{
		"type":        "user",
		"message":     map[string]any{"role": "user", "content": content},
		"isSynthetic": true,
		"isReplay":    false,
	}
	blob, _ := json.Marshal(rec)
	path := writeWireLog(t, []string{
		string(blob),
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"resuming"}]}}`,
	})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Type != MessageAssistant {
		t.Fatalf("got %+v, want compact continuation suppressed (only assistant remains)", entries)
	}
}

// TestLoadTranscriptFromWire_SyntheticReplayNonPreambleRenders breaks a
// flag-combo shortcut: an (isSynthetic && isReplay) frame whose content is NOT
// the compaction preamble must still render. This forces content-prefix
// detection rather than suppressing on the flag pair alone.
func TestLoadTranscriptFromWire_SyntheticReplayNonPreambleRenders(t *testing.T) {
	path := writeWireLog(t, []string{
		`{"type":"user","message":{"role":"user","content":"a normal replayed prompt"},"isSynthetic":true,"isReplay":true}`,
	})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Type != MessageUser || entries[0].Content != "a normal replayed prompt" {
		t.Fatalf("got %+v, want the non-preamble synthetic+replay frame rendered as user", entries)
	}
}

// TestLoadTranscriptFromWire_ToolResultPatchesEntry exercises the wire-native
// tool_result shape: a synthetic user out-frame with array tool_result content
// must patch Result/Failed onto the matching prior MessageToolCall by
// tool_use_id.
func TestLoadTranscriptFromWire_ToolResultPatchesEntry(t *testing.T) {
	path := writeWireLog(t, []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/x"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file body","is_error":true}]}}`,
	})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tc *MessageEntry
	for i := range entries {
		if entries[i].Type == MessageToolCall && entries[i].ToolID == "toolu_1" {
			tc = &entries[i]
		}
	}
	if tc == nil {
		t.Fatalf("tool call toolu_1 not found: %+v", entries)
	}
	if tc.Result != "file body" || !tc.Failed {
		t.Fatalf("tool call = %+v, want Result 'file body' + Failed=true", *tc)
	}
}

// TestLoadTranscriptFromWire_SyntheticSkillFrameNotSuppressed guards against
// over-suppression: isSynthetic:true also marks skill-injection user frames,
// which must still render.
func TestLoadTranscriptFromWire_SyntheticSkillFrameNotSuppressed(t *testing.T) {
	path := writeWireLog(t, []string{
		`{"type":"user","message":{"role":"user","content":"Base directory for this skill is /x. Use it."},"isSynthetic":true}`,
	})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 || entries[0].Type != MessageUser {
		t.Fatalf("got %+v, want the synthetic skill frame rendered as user", entries)
	}
}
