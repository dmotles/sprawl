package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// wireLogLine mirrors the on-disk NDJSON shape: one JSON object per line with
// a timestamp, a direction tag, and the verbatim raw payload.
type wireLogLine struct {
	TS  json.RawMessage `json:"ts"`
	Dir string          `json:"dir"`
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

func TestWireLog_RoundTripVerbatimCapture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}

	payloads := []string{
		`{"type":"init"}`,
		"{\"type\":\"text\"}\n", // trailing newline must be preserved
		`{"partial`,             // non-JSON / partial fragment captured verbatim
	}
	out := w.dirWriter("out")
	for _, p := range payloads {
		if _, err := out.Write([]byte(p)); err != nil {
			t.Fatalf("Write(%q): %v", p, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readWireLogLines(t, path)
	if len(lines) != len(payloads) {
		t.Fatalf("got %d lines, want %d: %+v", len(lines), len(payloads), lines)
	}
	for i, l := range lines {
		if l.Dir != "out" {
			t.Errorf("line %d dir = %q, want out", i, l.Dir)
		}
		if l.Raw != payloads[i] {
			t.Errorf("line %d raw = %q, want %q (verbatim)", i, l.Raw, payloads[i])
		}
		if len(l.TS) == 0 {
			t.Errorf("line %d missing ts field", i)
		}
	}
}

func TestWireLog_WriteReturnsFullCountAndNoError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.ndjson")
	w, err := newWireLog(path)
	if err != nil {
		t.Fatalf("newWireLog: %v", err)
	}
	defer w.Close()

	p := []byte(`{"type":"result"}`)
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

	p := []byte(`{"type":"after-close"}`)
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

func TestWireLog_ConcurrentWritesProduceCompleteLines(t *testing.T) {
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
			payload := []byte(`{"type":"msg","seq":` + itoa(i) + `}`)
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
		t.Fatalf("got %d complete JSON lines, want %d (torn/interleaved writes?)", len(lines), total)
	}
	for i, l := range lines {
		if l.Dir != "in" && l.Dir != "out" {
			t.Errorf("line %d dir = %q, want in|out", i, l.Dir)
		}
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
