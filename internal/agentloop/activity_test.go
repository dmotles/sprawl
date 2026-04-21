package agentloop

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

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
