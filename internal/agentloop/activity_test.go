package agentloop

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// countingReadSeeker wraps an io.ReadSeeker and tracks total bytes read.
// This seam is intentional: tests use readActivityTailFrom (the lower-level
// entry point that accepts an io.ReadSeeker) to assert byte-level read
// bounds without depending on the OS filesystem. Production callers use
// ReadActivityTail(path, n) which opens the file and delegates here.
type countingReadSeeker struct {
	rs   io.ReadSeeker
	read int64
}

func (c *countingReadSeeker) Read(p []byte) (int, error) {
	n, err := c.rs.Read(p)
	c.read += int64(n)
	return n, err
}

func (c *countingReadSeeker) Seek(o int64, w int) (int64, error) { return c.rs.Seek(o, w) }

// marshalEntryLine encodes an ActivityEntry as one NDJSON line (with '\n').
func marshalEntryLine(t *testing.T, e ActivityEntry) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(b, '\n')
}

func TestReadActivityTail_MissingFile(t *testing.T) {
	got, err := ReadActivityTail(filepath.Join(t.TempDir(), "missing.ndjson"), 10)
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestReadActivityTail_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadActivityTail(path, 10)
	if err != nil {
		t.Fatalf("empty file should not error, got %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestReadActivityTail_SingleEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	now := fixedNow()
	e := ActivityEntry{TS: now(), Kind: "system", Summary: "only"}
	if err := os.WriteFile(path, marshalEntryLine(t, e), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadActivityTail(path, 3)
	if err != nil {
		t.Fatalf("ReadActivityTail: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "only" {
		t.Errorf("got %+v, want 1 entry with Summary=only", got)
	}
}

func TestReadActivityTail_MultiEntry_OldestFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	var buf bytes.Buffer
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		e := ActivityEntry{TS: base.Add(time.Duration(i) * time.Second), Kind: "system", Summary: "s" + string(rune('0'+i))}
		buf.Write(marshalEntryLine(t, e))
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadActivityTail(path, 3)
	if err != nil {
		t.Fatalf("ReadActivityTail: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	wantSummaries := []string{"s2", "s3", "s4"}
	for i, e := range got {
		if e.Summary != wantSummaries[i] {
			t.Errorf("got[%d].Summary = %q, want %q", i, e.Summary, wantSummaries[i])
		}
	}
}

func TestReadActivityTail_PartialTrailingLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	now := fixedNow()
	var buf bytes.Buffer
	buf.Write(marshalEntryLine(t, ActivityEntry{TS: now(), Kind: "system", Summary: "first"}))
	buf.Write(marshalEntryLine(t, ActivityEntry{TS: now().Add(time.Second), Kind: "system", Summary: "second"}))
	// Malformed partial trailing line with NO trailing newline.
	buf.WriteString(`{"kind":"broken","summa`)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadActivityTail(path, 5)
	if err != nil {
		t.Fatalf("ReadActivityTail: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (malformed trailing skipped)", len(got))
	}
	if got[0].Summary != "first" || got[1].Summary != "second" {
		t.Errorf("got %+v, want [first second]", got)
	}
}

func TestReadActivityTail_TrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	var buf bytes.Buffer
	now := fixedNow()
	buf.Write(marshalEntryLine(t, ActivityEntry{TS: now(), Kind: "system", Summary: "a"}))
	buf.Write(marshalEntryLine(t, ActivityEntry{TS: now().Add(time.Second), Kind: "system", Summary: "b"}))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadActivityTail(path, 5)
	if err != nil {
		t.Fatalf("ReadActivityTail: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	wantSummaries := []string{"a", "b"}
	for i, e := range got {
		if e.Summary != wantSummaries[i] {
			t.Errorf("entry[%d].Summary = %q, want %q", i, e.Summary, wantSummaries[i])
		}
	}
}

func TestReadActivityTail_NLargerThanFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	var buf bytes.Buffer
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	wantSummaries := []string{"a", "b", "c"}
	for i, s := range wantSummaries {
		buf.Write(marshalEntryLine(t, ActivityEntry{TS: base.Add(time.Duration(i) * time.Second), Kind: "system", Summary: s}))
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadActivityTail(path, 10)
	if err != nil {
		t.Fatalf("ReadActivityTail: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	for i, e := range got {
		if e.Summary != wantSummaries[i] {
			t.Errorf("got[%d].Summary = %q, want %q (oldest-first)", i, e.Summary, wantSummaries[i])
		}
	}
}

func TestReadActivityTail_NoTrailingNewline_ValidLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	now := fixedNow()
	var buf bytes.Buffer
	buf.Write(marshalEntryLine(t, ActivityEntry{TS: now(), Kind: "system", Summary: "a"}))
	// Last line: valid JSON, NO trailing newline.
	b, _ := json.Marshal(ActivityEntry{TS: now().Add(time.Second), Kind: "system", Summary: "b"})
	buf.Write(b)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadActivityTail(path, 5)
	if err != nil {
		t.Fatalf("ReadActivityTail: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[1].Summary != "b" {
		t.Errorf("last entry Summary = %q, want b", got[1].Summary)
	}
}

func TestReadActivityTail_MalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	content := "not json\n" +
		`{"kind":"ok","summary":"valid"}` + "\n" +
		"{broken\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadActivityTail(path, 10)
	if err != nil {
		t.Fatalf("ReadActivityTail: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "ok" {
		t.Errorf("got %+v, want 1 valid entry", got)
	}
}

func TestReadActivityTail_NZeroOrNegative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	now := fixedNow()
	if err := os.WriteFile(path, marshalEntryLine(t, ActivityEntry{TS: now(), Kind: "system", Summary: "x"}), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, -1, -100} {
		got, err := ReadActivityTail(path, n)
		if err != nil {
			t.Errorf("n=%d: unexpected err %v", n, err)
		}
		if got != nil {
			t.Errorf("n=%d: got %+v, want nil", n, got)
		}
	}
}

func TestReadActivityTail_ChunkBoundary(t *testing.T) {
	var buf bytes.Buffer
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		e := ActivityEntry{TS: base.Add(time.Duration(i) * time.Second), Kind: "system", Summary: "entry-" + string(rune('0'+i))}
		buf.Write(marshalEntryLine(t, e))
	}
	if buf.Len() <= 16 {
		t.Fatalf("test setup: buffer too small (%d bytes)", buf.Len())
	}
	rs := bytes.NewReader(buf.Bytes())
	got, err := readActivityTailFrom(rs, 4, 16)
	if err != nil {
		t.Fatalf("readActivityTailFrom: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4", len(got))
	}
	wantSummaries := []string{"entry-4", "entry-5", "entry-6", "entry-7"}
	for i, e := range got {
		if e.Summary != wantSummaries[i] {
			t.Errorf("got[%d].Summary = %q, want %q", i, e.Summary, wantSummaries[i])
		}
	}
}

func TestReadActivityTail_BoundedReadFor1MBFile(t *testing.T) {
	var buf bytes.Buffer
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	var lastSummary string
	for i := 0; buf.Len() < 1_048_576; i++ {
		lastSummary = "summary-line-" + strings.Repeat("x", 50) + "-" + string(rune('a'+(i%26))) + "-" + string(rune('0'+(i%10)))
		e := ActivityEntry{TS: base.Add(time.Duration(i) * time.Second), Kind: "system", Summary: lastSummary}
		buf.Write(marshalEntryLine(t, e))
	}
	if buf.Len() < 1_048_576 {
		t.Fatalf("test setup: buf size %d < 1MB", buf.Len())
	}
	counter := &countingReadSeeker{rs: bytes.NewReader(buf.Bytes())}
	got, err := readActivityTailFrom(counter, 1, 4096)
	if err != nil {
		t.Fatalf("readActivityTailFrom: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Summary != lastSummary {
		t.Errorf("got Summary=%q, want %q", got[0].Summary, lastSummary)
	}
	if counter.read <= 0 {
		t.Errorf("read %d bytes, want > 0 (must actually read tail data)", counter.read)
	}
	if counter.read >= 8192 {
		t.Errorf("read %d bytes, want < 8192 (bounded tail read)", counter.read)
	}
}

// fixedNow returns a closure producing a fixed timestamp for reproducibility.
func fixedNow() func() time.Time {
	t := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func TestActivityRing_AppendAndTail(t *testing.T) {
	r := NewActivityRing(3, nil)
	now := fixedNow()

	for i, kind := range []string{"a", "b", "c", "d"} {
		r.Append(ActivityEntry{TS: now().Add(time.Duration(i) * time.Second), Kind: kind, Summary: kind})
	}

	got := r.Tail(10)
	if len(got) != 3 {
		t.Fatalf("Tail(10) len = %d, want 3 (bounded by capacity)", len(got))
	}
	wantKinds := []string{"b", "c", "d"}
	for i, e := range got {
		if e.Kind != wantKinds[i] {
			t.Errorf("Tail[%d].Kind = %q, want %q", i, e.Kind, wantKinds[i])
		}
	}

	tail2 := r.Tail(2)
	if len(tail2) != 2 || tail2[0].Kind != "c" || tail2[1].Kind != "d" {
		t.Errorf("Tail(2) = %+v, want [c d]", tail2)
	}
}

func TestActivityRing_TailZeroOrNegative(t *testing.T) {
	r := NewActivityRing(10, nil)
	r.Append(ActivityEntry{Kind: "x"})
	if got := r.Tail(0); len(got) != 0 {
		t.Errorf("Tail(0) len = %d, want 0", len(got))
	}
	if got := r.Tail(-5); len(got) != 0 {
		t.Errorf("Tail(-5) len = %d, want 0", len(got))
	}
}

func TestActivityRing_ThreadSafe(t *testing.T) {
	r := NewActivityRing(100, nil)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r.Append(ActivityEntry{Kind: "k"})
				_ = r.Tail(10)
			}
		}(i)
	}
	wg.Wait()
	// If we get here without the race detector complaining, we're good.
	if len(r.Tail(1000)) != 100 {
		t.Errorf("after 500 appends, len = %d, want 100 (cap)", len(r.Tail(1000)))
	}
}

func TestActivityRing_WritesNDJSON(t *testing.T) {
	var buf bytes.Buffer
	r := NewActivityRing(10, &buf)
	now := fixedNow()
	r.Append(ActivityEntry{TS: now(), Kind: "assistant_text", Summary: "hello"})
	r.Append(ActivityEntry{TS: now(), Kind: "tool_use", Summary: "Read()", Tool: "Read"})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), buf.String())
	}
	var e ActivityEntry
	if err := json.Unmarshal([]byte(lines[1]), &e); err != nil {
		t.Fatalf("line2 not valid JSON: %v", err)
	}
	if e.Kind != "tool_use" || e.Tool != "Read" {
		t.Errorf("got %+v, want Kind=tool_use Tool=Read", e)
	}
}

func TestRecordMessage_AssistantText(t *testing.T) {
	r := NewActivityRing(10, nil)
	now := fixedNow()
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello world"}]}}`)
	msg := &protocol.Message{Type: "assistant", Raw: raw}

	r.RecordMessage(msg, now)

	entries := r.Tail(10)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Kind != "assistant_text" {
		t.Errorf("Kind = %q, want assistant_text", e.Kind)
	}
	if !strings.Contains(e.Summary, "hello world") {
		t.Errorf("Summary = %q, want to contain 'hello world'", e.Summary)
	}
}

func TestRecordMessage_ToolUse(t *testing.T) {
	r := NewActivityRing(10, nil)
	now := fixedNow()
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}`)
	msg := &protocol.Message{Type: "assistant", Raw: raw}

	r.RecordMessage(msg, now)

	entries := r.Tail(10)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Kind != "tool_use" {
		t.Errorf("Kind = %q, want tool_use", e.Kind)
	}
	if e.Tool != "Bash" {
		t.Errorf("Tool = %q, want Bash", e.Tool)
	}
	if !strings.Contains(e.Summary, "ls -la") {
		t.Errorf("Summary = %q, want to contain 'ls -la'", e.Summary)
	}
}

func TestRecordMessage_Result(t *testing.T) {
	r := NewActivityRing(10, nil)
	now := fixedNow()
	raw := []byte(`{"type":"result","stop_reason":"end_turn","num_turns":3,"is_error":false}`)
	msg := &protocol.Message{Type: "result", Raw: raw}

	r.RecordMessage(msg, now)

	entries := r.Tail(10)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Kind != "result" {
		t.Errorf("Kind = %q, want result", e.Kind)
	}
	if !strings.Contains(e.Summary, "end_turn") {
		t.Errorf("Summary = %q, want to contain 'end_turn'", e.Summary)
	}
}

func TestRecordMessage_IgnoresUser(t *testing.T) {
	r := NewActivityRing(10, nil)
	now := fixedNow()
	msg := &protocol.Message{Type: "user", Raw: []byte(`{"type":"user"}`)}
	r.RecordMessage(msg, now)
	if got := r.Tail(10); len(got) != 0 {
		t.Errorf("user message should be ignored, got %d entries", len(got))
	}
}

func TestRecordMessage_MultipleBlocks(t *testing.T) {
	r := NewActivityRing(10, nil)
	now := fixedNow()
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"text","text":"thinking"},` +
		`{"type":"tool_use","name":"Grep","input":{"pattern":"foo"}}` +
		`]}}`)
	msg := &protocol.Message{Type: "assistant", Raw: raw}
	r.RecordMessage(msg, now)
	entries := r.Tail(10)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Kind != "assistant_text" || entries[1].Kind != "tool_use" {
		t.Errorf("kinds = %q, %q — want assistant_text, tool_use", entries[0].Kind, entries[1].Kind)
	}
}

func TestRedactSummary_ScrubsSecrets(t *testing.T) {
	cases := []struct {
		in   string
		want string // substring that should appear
		bad  string // substring that MUST NOT appear
	}{
		{
			in:   `{"Authorization":"Bearer sk-abc123xyz","path":"/foo"}`,
			want: "REDACTED",
			bad:  "sk-abc123xyz",
		},
		{
			in:   `{"api_key":"secret-key-value","other":"ok"}`,
			want: "REDACTED",
			bad:  "secret-key-value",
		},
		{
			in:   `{"ANTHROPIC_API_KEY":"whatever"}`,
			want: "REDACTED",
			bad:  "whatever",
		},
		{
			in:   `{"x-api-key":"hunter2"}`,
			want: "REDACTED",
			bad:  "hunter2",
		},
		{
			in:   `{"password":"p@ss","user":"bob"}`,
			want: "REDACTED",
			bad:  "p@ss",
		},
	}
	for _, c := range cases {
		got := RedactSummary(c.in)
		if !strings.Contains(got, c.want) {
			t.Errorf("RedactSummary(%q) = %q; want contains %q", c.in, got, c.want)
		}
		if strings.Contains(got, c.bad) {
			t.Errorf("RedactSummary(%q) = %q; must not contain %q", c.in, got, c.bad)
		}
	}
}

func TestRedactSummary_PreservesSafeContent(t *testing.T) {
	in := `{"path":"/foo","command":"ls"}`
	got := RedactSummary(in)
	if !strings.Contains(got, "/foo") || !strings.Contains(got, "ls") {
		t.Errorf("RedactSummary stripped safe content: %q -> %q", in, got)
	}
}

func TestRecordMessage_RedactsToolInput(t *testing.T) {
	r := NewActivityRing(10, nil)
	now := fixedNow()
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"WebFetch","input":{"url":"https://x.com","Authorization":"Bearer secrettoken"}}]}}`)
	msg := &protocol.Message{Type: "assistant", Raw: raw}
	r.RecordMessage(msg, now)
	entries := r.Tail(10)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if strings.Contains(entries[0].Summary, "secrettoken") {
		t.Errorf("secret leaked into summary: %q", entries[0].Summary)
	}
}

func TestActivityPath(t *testing.T) {
	got := ActivityPath("/root", "ghost")
	want := filepath.Join("/root", ".sprawl", "agents", "ghost", "activity.ndjson")
	if got != want {
		t.Errorf("ActivityPath = %q, want %q", got, want)
	}
}

func TestReadActivityFile_TailLastN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	var buf bytes.Buffer
	now := fixedNow()
	for i := 0; i < 5; i++ {
		e := ActivityEntry{TS: now().Add(time.Duration(i) * time.Second), Kind: "k", Summary: "s" + string(rune('0'+i))}
		b, _ := json.Marshal(e)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadActivityFile(path, 2)
	if err != nil {
		t.Fatalf("ReadActivityFile: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].Summary != "s3" || got[1].Summary != "s4" {
		t.Errorf("got %v, want [s3 s4]", []string{got[0].Summary, got[1].Summary})
	}
}

func TestReadActivityFile_Missing(t *testing.T) {
	got, err := ReadActivityFile(filepath.Join(t.TempDir(), "nope.ndjson"), 10)
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

func TestReadActivityFile_TailBiggerThanFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	e := ActivityEntry{Kind: "k", Summary: "only"}
	b, _ := json.Marshal(e)
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadActivityFile(path, 100)
	if err != nil {
		t.Fatalf("ReadActivityFile: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "only" {
		t.Errorf("got %+v, want 1 entry 'only'", got)
	}
}

func TestReadActivityFile_SkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.ndjson")
	content := `not json
{"kind":"ok","summary":"valid"}
{broken
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadActivityFile(path, 10)
	if err != nil {
		t.Fatalf("ReadActivityFile: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "ok" {
		t.Errorf("got %+v, want 1 valid entry", got)
	}
}

func TestActivityEntry_SummaryTruncation(t *testing.T) {
	r := NewActivityRing(10, nil)
	now := fixedNow()
	longText := strings.Repeat("x", 500)
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + longText + `"}]}}`)
	msg := &protocol.Message{Type: "assistant", Raw: raw}
	r.RecordMessage(msg, now)
	entries := r.Tail(10)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry")
	}
	if len(entries[0].Summary) > maxSummaryLen {
		t.Errorf("summary len = %d, want ≤ %d", len(entries[0].Summary), maxSummaryLen)
	}
}

// --- QUM-665: ActivityRing.LastAt accessor ---

// TestActivityRing_LastAt_ZeroWhenEmpty asserts an unused ring returns the
// zero time. Callers rely on IsZero() to mean "no activity recorded".
func TestActivityRing_LastAt_ZeroWhenEmpty(t *testing.T) {
	r := NewActivityRing(10, nil)
	got := r.LastAt()
	if !got.IsZero() {
		t.Errorf("LastAt() on empty ring = %v, want zero time", got)
	}
}

// TestActivityRing_LastAt_TracksLatestAppend asserts LastAt returns the TS
// of the most-recently appended entry under normal monotonic append.
func TestActivityRing_LastAt_TracksLatestAppend(t *testing.T) {
	r := NewActivityRing(10, nil)
	t1 := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(5 * time.Second)
	r.Append(ActivityEntry{TS: t1, Kind: "system", Summary: "a"})
	r.Append(ActivityEntry{TS: t2, Kind: "system", Summary: "b"})
	got := r.LastAt()
	if !got.Equal(t2) {
		t.Errorf("LastAt() = %v, want %v (latest append)", got, t2)
	}
}

// TestActivityRing_LastAt_SurvivesEviction asserts that when the ring's
// oldest entry is evicted, LastAt still returns the most-recent TS — i.e.
// LastAt is computed from the live entries, not a separate stale field.
func TestActivityRing_LastAt_SurvivesEviction(t *testing.T) {
	r := NewActivityRing(2, nil)
	t1 := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(1 * time.Second)
	t3 := t1.Add(2 * time.Second)
	r.Append(ActivityEntry{TS: t1, Kind: "system", Summary: "a"})
	r.Append(ActivityEntry{TS: t2, Kind: "system", Summary: "b"})
	r.Append(ActivityEntry{TS: t3, Kind: "system", Summary: "c"}) // evicts t1
	got := r.LastAt()
	if !got.Equal(t3) {
		t.Errorf("LastAt() = %v, want %v (latest survives eviction)", got, t3)
	}
}

// TestActivityRing_LastAt_NotRegressedByOlderAppend defends the "max TS"
// semantic of the accessor. Even if an older entry is appended after a
// newer one (which shouldn't happen in practice but the accessor name says
// "latest"), LastAt should not regress.
func TestActivityRing_LastAt_NotRegressedByOlderAppend(t *testing.T) {
	r := NewActivityRing(10, nil)
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	older := now.Add(-1 * time.Minute)
	r.Append(ActivityEntry{TS: now, Kind: "system", Summary: "new"})
	r.Append(ActivityEntry{TS: older, Kind: "system", Summary: "old"})
	got := r.LastAt()
	if !got.Equal(now) {
		t.Errorf("LastAt() = %v, want %v (max TS, not last-appended TS)", got, now)
	}
}
