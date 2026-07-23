package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

// wireLogLine mirrors the on-disk NDJSON shape: one JSON object per line with
// a timestamp, a direction tag, a monotonic seq, and the reconstructed frame
// bytes. As of the frame-oriented writer (wire-log-becomes-authoritative
// epic) each record holds ONE newline-delimited protocol frame rather than one
// io.TeeReader read chunk, and carries a `seq` counter. Legacy seq-less files
// stay readable (Seq is absent → decodes as 0).
type wireLogLine struct {
	TS  json.RawMessage `json:"ts"`
	Dir string          `json:"dir"`
	Seq int64           `json:"seq"`
	Raw string          `json:"raw"`
}

// readWireLogLines reads path and parses every non-empty line as a wireLogLine.
func readWireLogLines(t *testing.T, path string) []wireLogLine {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wire log %s: %v", path, err)
	}
	var lines []wireLogLine
	for _, raw := range splitNDJSON(data) {
		var l wireLogLine
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", string(raw), err)
		}
		lines = append(lines, l)
	}
	return lines
}

// splitNDJSON splits on '\n' between top-level JSON objects. Because raw
// payloads may themselves contain embedded newlines inside a JSON string, we
// cannot naively split on every '\n'; instead we scan for balanced lines by
// attempting to decode with a streaming json.Decoder.
func splitNDJSON(data []byte) [][]byte {
	var out [][]byte
	dec := json.NewDecoder(newByteReader(data))
	for {
		var m json.RawMessage
		if err := dec.Decode(&m); err != nil {
			break
		}
		out = append(out, m)
	}
	return out
}

type byteReader struct {
	b []byte
	i int
}

func newByteReader(b []byte) *byteReader { return &byteReader{b: b} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, errEOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

var errEOF = errReader("EOF")

type errReader string

func (e errReader) Error() string { return string(e) }

// TestWireLog_FrameOrientedOneRecordPerFrame asserts the writer emits ONE
// record per newline-delimited frame (not per Write chunk), that each complete
// frame's raw retains its own trailing '\n' delimiter, that seq starts at 1 and
// increases by one per record, and that a trailing partial (no newline) is
// flushed as the final record on Close rather than dropped.
func TestWireLog_FrameOrientedOneRecordPerFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}

	out := w.dirWriter("out")
	// Frame A is complete in the first write; frame B is split across the
	// first and second writes (mid-frame chunk boundary); the third write is a
	// trailing partial with no newline.
	chunks := []string{
		`{"type":"a"}` + "\n" + `{"type":`,
		`"b"}` + "\n",
		`{"partial`,
	}
	for _, c := range chunks {
		if _, err := out.Write([]byte(c)); err != nil {
			t.Fatalf("Write(%q): %v", c, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readWireLogLines(t, path)
	wantRaw := []string{
		`{"type":"a"}` + "\n",
		`{"type":"b"}` + "\n",
		`{"partial`, // trailing partial flushed on Close, no delimiter
	}
	if len(lines) != len(wantRaw) {
		t.Fatalf("got %d records, want %d (one per frame): %+v", len(lines), len(wantRaw), lines)
	}
	for i, l := range lines {
		if l.Dir != "out" {
			t.Errorf("record %d dir = %q, want out", i, l.Dir)
		}
		if l.Raw != wantRaw[i] {
			t.Errorf("record %d raw = %q, want %q", i, l.Raw, wantRaw[i])
		}
		if want := int64(i + 1); l.Seq != want {
			t.Errorf("record %d seq = %d, want %d", i, l.Seq, want)
		}
		if len(l.TS) == 0 {
			t.Errorf("record %d missing ts field", i)
		}
	}
}

// TestWireLog_PerDirectionPartialBuffering asserts that partial frames from the
// two directions are buffered SEPARATELY — a single shared buffer would splice
// mid-frame bytes from "in" into the "out" frame (and vice versa), corrupting
// output. "in" and "out" are written from different goroutines in production
// (stdout tee reader vs stdin multiwriter), so this is a real hazard.
func TestWireLog_PerDirectionPartialBuffering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	out := w.dirWriter("out")
	in := w.dirWriter("in")

	// Interleave partials from both directions, then complete each.
	if _, err := out.Write([]byte(`{"o":`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := in.Write([]byte(`{"i":`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := out.Write([]byte(`1}` + "\n")); err != nil { // completes out frame
		t.Fatalf("Write: %v", err)
	}
	if _, err := in.Write([]byte(`2}` + "\n")); err != nil { // completes in frame
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readWireLogLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("got %d records, want 2 (one per direction, no cross-contamination): %+v", len(lines), lines)
	}
	byDir := map[string]string{}
	for _, l := range lines {
		byDir[l.Dir] = l.Raw
	}
	if byDir["out"] != `{"o":1}`+"\n" {
		t.Errorf("out frame raw = %q, want %q (no in-bytes spliced)", byDir["out"], `{"o":1}`+"\n")
	}
	if byDir["in"] != `{"i":2}`+"\n" {
		t.Errorf("in frame raw = %q, want %q (no out-bytes spliced)", byDir["in"], `{"i":2}`+"\n")
	}
}

// TestWireLog_ResumeAfterTrailingPartial reopens a file whose final record is a
// delimiter-less flushed partial (as Close emits). seq recovery must scan that
// valid-but-newline-less final envelope and continue numbering from it.
func TestWireLog_ResumeAfterTrailingPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")

	w1, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	if _, err := w1.dirWriter("in").Write([]byte(`{"partial`)); err != nil { // no newline
		t.Fatalf("Write: %v", err)
	}
	if err := w1.Close(); err != nil { // flushes the partial as seq=1
		t.Fatalf("Close: %v", err)
	}

	w2, err := newWireLog(path)
	if err != nil {
		t.Fatalf("reopen newWireLog: %v", err)
	}
	if _, err := w2.dirWriter("out").Write([]byte(`{"type":"done"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readWireLogLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("got %d records, want 2", len(lines))
	}
	if lines[0].Seq != 1 || lines[1].Seq != 2 {
		t.Fatalf("seq = [%d,%d], want [1,2] (recovery past delimiter-less final line)", lines[0].Seq, lines[1].Seq)
	}
}

func TestWireLog_WriteReturnsFullCountAndNoError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	defer w.Close()

	p := []byte(`{"type":"result"}` + "\n")
	n, err := w.dirWriter("in").Write(p)
	if err != nil {
		t.Fatalf("Write err = %v, want nil (tee-safety contract)", err)
	}
	if n != len(p) {
		t.Errorf("Write n = %d, want %d (must return len(p))", n, len(p))
	}
}

func TestWireLog_WriteAfterCloseStillSatisfiesTeeContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	// Close first so the underlying file write fails on the next Write.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	p := []byte(`{"type":"after-close"}` + "\n")
	wr := w.dirWriter("out")
	// Must never panic, never short-count, never return an error — otherwise
	// io.TeeReader / io.MultiWriter would corrupt the live transport.
	n, err := wr.Write(p)
	if err != nil {
		t.Errorf("Write after close err = %v, want nil (graceful degrade)", err)
	}
	if n != len(p) {
		t.Errorf("Write after close n = %d, want %d", n, len(p))
	}
}

// TestWireLog_MalformedInputNeverErrorsOrShortWrites feeds non-JSON garbage and
// bytes with embedded newlines; the writer must always report a full,
// error-free write (tee-safety) regardless of content, and Close must succeed.
func TestWireLog_MalformedInputNeverErrorsOrShortWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}

	garbage := [][]byte{
		[]byte("not json at all\n"),
		[]byte("\x00\x01\x02 binary \n more\n"),
		[]byte(`{"unterminated`),
		[]byte("\n\n\n"),
	}
	for _, g := range garbage {
		n, werr := w.dirWriter("out").Write(g)
		if werr != nil {
			t.Errorf("Write(%q) err = %v, want nil", g, werr)
		}
		if n != len(g) {
			t.Errorf("Write(%q) n = %d, want %d", g, n, len(g))
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestWireLog_ConcurrentWritesProduceContiguousSeq drives both directions
// concurrently and asserts every record is a complete JSON line and the shared
// seq counter produces a contiguous 1..N set (no dup, no gap) — proving seq
// assignment and file writes are mutex-safe across both directions.
func TestWireLog_ConcurrentWritesProduceContiguousSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}

	const total = 50
	var wg sync.WaitGroup
	wg.Add(total)
	for i := 0; i < total; i++ {
		i := i
		go func() {
			defer wg.Done()
			dir := "in"
			if i%2 == 0 {
				dir = "out"
			}
			// Each payload is a complete newline-delimited frame.
			payload := []byte(`{"type":"msg","n":` + itoa(i) + "}\n")
			if _, err := w.dirWriter(dir).Write(payload); err != nil {
				t.Errorf("concurrent Write: %v", err)
			}
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readWireLogLines(t, path)
	if len(lines) != total {
		t.Fatalf("got %d complete JSON records, want %d (torn/interleaved writes?)", len(lines), total)
	}
	seqs := make([]int64, 0, total)
	for i, l := range lines {
		if l.Dir != "in" && l.Dir != "out" {
			t.Errorf("record %d dir = %q, want in|out", i, l.Dir)
		}
		seqs = append(seqs, l.Seq)
	}
	sort.Slice(seqs, func(a, b int) bool { return seqs[a] < seqs[b] })
	for i, s := range seqs {
		if want := int64(i + 1); s != want {
			t.Fatalf("seq set not contiguous: sorted[%d] = %d, want %d (dup or gap)", i, s, want)
		}
	}
}

// TestWireLog_SeqMonotonicAcrossResume opens a fresh log, writes frames, closes,
// then re-opens the SAME path (as a resume does under a stable sessionID),
// writes more frames, and asserts seq is strictly increasing and continuous
// across the reopen boundary — recovery-on-open must pick up the last seq.
func TestWireLog_SeqMonotonicAcrossResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")

	writeFrames := func(dir string, frames ...string) {
		w, err := newWireLog(path)
		if err != nil {
			t.Fatalf("newWireLog: %v", err)
		}
		for _, f := range frames {
			if _, err := w.dirWriter(dir).Write([]byte(f)); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	writeFrames("out", `{"type":"system","subtype":"init"}`+"\n", `{"type":"assistant"}`+"\n")
	writeFrames("out", `{"type":"system","subtype":"init"}`+"\n", `{"type":"result"}`+"\n")

	lines := readWireLogLines(t, path)
	if len(lines) != 4 {
		t.Fatalf("got %d records, want 4 across two sessions", len(lines))
	}
	for i, l := range lines {
		if want := int64(i + 1); l.Seq != want {
			t.Fatalf("record %d seq = %d, want %d (seq must continue across resume)", i, l.Seq, want)
		}
	}
}

// TestWireLog_SeqStartsAtOneOnLegacyFile asserts that appending seq'd frames
// onto a pre-existing seq-LESS (legacy chunk-oriented) file starts numbering at
// 1 and coexists with the legacy lines (backward compatibility on disk).
func TestWireLog_SeqStartsAtOneOnLegacyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	// Seed a legacy seq-less file (chunk-oriented, no seq field).
	legacy := `{"ts":"2026-01-01T00:00:00Z","dir":"out","raw":"{\"type\":\"old\"}\n"}` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}

	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	if _, err := w.dirWriter("out").Write([]byte(`{"type":"new"}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readWireLogLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("got %d records, want 2 (legacy + new)", len(lines))
	}
	if lines[0].Seq != 0 {
		t.Errorf("legacy record seq = %d, want 0 (absent)", lines[0].Seq)
	}
	if lines[1].Seq != 1 {
		t.Errorf("first new record seq = %d, want 1", lines[1].Seq)
	}
}

// TestWireLog_CloseFlushesTrailingPartial asserts a buffered partial frame with
// no trailing newline is emitted as a final record on Close (never dropped).
func TestWireLog_CloseFlushesTrailingPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	partial := `{"type":"incomplete`
	if _, err := w.dirWriter("in").Write([]byte(partial)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readWireLogLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("got %d records, want 1 (flushed partial)", len(lines))
	}
	if lines[0].Raw != partial {
		t.Errorf("raw = %q, want %q", lines[0].Raw, partial)
	}
	if lines[0].Seq != 1 {
		t.Errorf("seq = %d, want 1", lines[0].Seq)
	}
}

// lenientEnvelopes reads path, splits on physical '\n', and returns every line
// that parses as an envelope — skipping a torn/garbage line (as a robust reader
// must). Unlike readWireLogLines (json.Decoder) it does not abort at a bad line.
func lenientEnvelopes(t *testing.T, path string) []wireLogLine {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []wireLogLine
	for _, line := range bytesSplitLines(data) {
		var l wireLogLine
		if json.Unmarshal(line, &l) == nil {
			out = append(out, l)
		}
	}
	return out
}

func bytesSplitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			if seg := trimSpaceBytes(data[start:i]); len(seg) > 0 {
				out = append(out, seg)
			}
			start = i + 1
		}
	}
	if seg := trimSpaceBytes(data[start:]); len(seg) > 0 {
		out = append(out, seg)
	}
	return out
}

func trimSpaceBytes(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	for len(b) > 0 {
		c := b[len(b)-1]
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}

// TestWireLog_ResumeAfterCrashTornTail simulates a hard crash that left a
// newline-less, incomplete envelope at the file's tail. On resume the writer
// must (a) fence that torn remnant so post-crash frames stay reachable to a
// reader, and (b) recover the true max seq by scanning past the torn line so
// numbering never duplicates across resumes.
func TestWireLog_ResumeAfterCrashTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	// Two valid seq'd envelopes, then a torn remnant: incomplete JSON, no '\n'.
	seed := `{"ts":"t1","dir":"out","seq":1,"raw":"{\"a\":1}\n"}` + "\n" +
		`{"ts":"t2","dir":"out","seq":2,"raw":"{\"b\":2}\n"}` + "\n" +
		`{"ts":"t3","dir":"out","seq":3,"raw":"{\"c`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First resume: write a complete frame.
	w1, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	if _, err := w1.dirWriter("out").Write([]byte(`{"d":4}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	env := lenientEnvelopes(t, path)
	// The new frame must be reachable — i.e. it must sit on its own parseable
	// line, not spliced onto the unterminated torn remnant.
	found := false
	for _, e := range env {
		if e.Raw == `{"d":4}`+"\n" {
			found = true
		}
	}
	if !found {
		t.Fatalf("post-crash frame unreachable: torn tail not fenced. envelopes=%+v", env)
	}
	assertNoDupSeq(t, env)

	// Second resume: numbering must continue past the torn line, no duplicate.
	w2, err := newWireLog(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := w2.dirWriter("out").Write([]byte(`{"e":5}` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	env2 := lenientEnvelopes(t, path)
	assertNoDupSeq(t, env2)
	// The two written frames must carry distinct, increasing seqs.
	var seqD, seqE int64
	for _, e := range env2 {
		switch e.Raw {
		case `{"d":4}` + "\n":
			seqD = e.Seq
		case `{"e":5}` + "\n":
			seqE = e.Seq
		}
	}
	if seqD == 0 || seqE == 0 || seqE <= seqD {
		t.Fatalf("expected distinct increasing seqs for the two post-crash frames, got d=%d e=%d", seqD, seqE)
	}
}

func assertNoDupSeq(t *testing.T, env []wireLogLine) {
	t.Helper()
	seen := map[int64]bool{}
	for _, e := range env {
		if e.Seq == 0 {
			continue // legacy seq-less line
		}
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d across resumes: %+v", e.Seq, env)
		}
		seen[e.Seq] = true
	}
}

// TestWireLog_MultipleDirectionsFlushedOnClose asserts Close flushes the
// trailing partial of EVERY buffered direction, not just a hardcoded pair.
func TestWireLog_MultipleDirectionsFlushedOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	// "aux" is an unconventional direction; its trailing partial must still flush.
	if _, err := w.dirWriter("aux").Write([]byte(`{"partial-aux`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.dirWriter("out").Write([]byte(`{"partial-out`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	lines := readWireLogLines(t, path)
	got := map[string]bool{}
	for _, l := range lines {
		got[l.Dir] = true
	}
	if !got["aux"] || !got["out"] {
		t.Fatalf("Close dropped a buffered direction: got dirs %v, want aux+out", got)
	}
}

func TestWireLog_CloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close = %v, want nil (idempotent)", err)
	}
}

func TestWireLog_NewWireLogCreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog should MkdirAll parents: %v", err)
	}
	defer w.Close()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file at %s after construction: %v", path, err)
	}
}

// itoa is a tiny strconv.Itoa stand-in to avoid an extra import in the test
// that the implementer might trip over during the red phase.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
