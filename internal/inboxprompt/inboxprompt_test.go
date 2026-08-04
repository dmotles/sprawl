// Package inboxprompt holds the inbox/interrupt prompt-formatter that both
// the legacy agentloop child harness and the unified-runtime supervisor path
// use to render pending queue entries into a turn prompt. QUM-555 slimmed the
// frames to one `<system-notification>` line per entry — no inlined body,
// no footer prose.
package inboxprompt_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/messages"
)

func TestBuildQueueFlushPrompt_Empty(t *testing.T) {
	if got := inboxprompt.BuildQueueFlushPrompt(nil); got != "" {
		t.Errorf("expected empty prompt for nil entries, got %q", got)
	}
	if got := inboxprompt.BuildQueueFlushPrompt([]inboxprompt.Entry{}); got != "" {
		t.Errorf("expected empty prompt for empty entries, got %q", got)
	}
}

// TestBuildQueueFlushPrompt_SingleEntry pins the exact one-line shape per
// QUM-556: `<system-notification>From $FROM — mcp__sprawl__messages_read(id=$SHORT_ID)</system-notification>\n`.
// No subject, no body, no footer prose. The fully-qualified MCP tool name
// is the primary pattern-match anchor for the recipient agent.
func TestBuildQueueFlushPrompt_SingleEntry(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "uuid-1", ShortID: "abc", Class: inboxprompt.ClassAsync,
		From: "child-alpha", Subject: "status", Body: "all green",
		Tags: []string{"fyi"},
	}}
	got := inboxprompt.BuildQueueFlushPrompt(entries)
	want := "<system-notification type=\"message\">From child-alpha — mcp__sprawl__messages_read(id=abc)</system-notification>\n"
	if got != want {
		t.Errorf("BuildQueueFlushPrompt mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildQueueFlushPrompt_SingleEntry_FallsBackToID covers entries whose
// ShortID is empty (legacy enqueues): the line must cite Entry.ID instead.
func TestBuildQueueFlushPrompt_SingleEntry_FallsBackToID(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "uuid-foo", ShortID: "", Class: inboxprompt.ClassAsync,
		From: "weave", Body: "hello",
	}}
	got := inboxprompt.BuildQueueFlushPrompt(entries)
	want := "<system-notification type=\"message\">From weave — mcp__sprawl__messages_read(id=uuid-foo)</system-notification>\n"
	if got != want {
		t.Errorf("BuildQueueFlushPrompt fallback mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildQueueFlushPrompt_Multiple pins N entries → N lines, one per entry.
func TestBuildQueueFlushPrompt_Multiple(t *testing.T) {
	entries := []inboxprompt.Entry{
		{ID: "u1", ShortID: "s1", From: "a", Body: "b1"},
		{ID: "u2", ShortID: "s2", From: "b", Body: "b2"},
		{ID: "u3", ShortID: "s3", From: "c", Body: "b3"},
	}
	got := inboxprompt.BuildQueueFlushPrompt(entries)
	want := "<system-notification type=\"message\">From a — mcp__sprawl__messages_read(id=s1)</system-notification>\n" +
		"<system-notification type=\"message\">From b — mcp__sprawl__messages_read(id=s2)</system-notification>\n" +
		"<system-notification type=\"message\">From c — mcp__sprawl__messages_read(id=s3)</system-notification>\n"
	if got != want {
		t.Errorf("BuildQueueFlushPrompt multi mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildQueueFlushPrompt_NoBodyInlined guards against any reintroduction
// of the verbose pre-QUM-555 frame. Body text, subject, tags, and the legacy
// footer must NOT appear in the output.
func TestBuildQueueFlushPrompt_NoBodyInlined(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "u1", ShortID: "s1", From: "a",
		Subject: "secret-subject", Body: "secret-body-content",
		Tags: []string{"secret-tag"},
	}}
	got := inboxprompt.BuildQueueFlushPrompt(entries)
	for _, banned := range []string{
		"secret-subject", "secret-body-content", "secret-tag",
		"Continue your current work", "[inbox]", "subject:",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("BuildQueueFlushPrompt leaked %q in output: %q", banned, got)
		}
	}
}

// TestBuildQueueFlushPrompt_NamesMCPTool is the QUM-556 regression guard:
// the rendered line MUST contain the fully-qualified MCP tool name
// `mcp__sprawl__messages_read` with the id in function-call shape, and MUST
// NOT use the bare verb "Read " (which was ambiguous with the legacy CLI
// form and triggered the wrong path).
func TestBuildQueueFlushPrompt_NamesMCPTool(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "u1", ShortID: "abc", From: "weave", Body: "hi",
	}}
	got := inboxprompt.BuildQueueFlushPrompt(entries)
	if !strings.Contains(got, "mcp__sprawl__messages_read(id=abc)") {
		t.Errorf("queue flush missing MCP tool citation: %q", got)
	}
	if strings.Contains(got, "Read abc") {
		t.Errorf("queue flush still uses bare 'Read' verb (QUM-556 regression): %q", got)
	}
}

// TestBuildInterruptFlushPrompt_NamesMCPTool — QUM-556 regression guard for
// the interrupt path. Same anchors as the async path.
func TestBuildInterruptFlushPrompt_NamesMCPTool(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "u1", ShortID: "xyz", Class: inboxprompt.ClassInterrupt,
		From: "weave", Body: "stop",
	}}
	got := inboxprompt.BuildInterruptFlushPrompt(entries)
	if !strings.Contains(got, "mcp__sprawl__messages_read(id=xyz)") {
		t.Errorf("interrupt flush missing MCP tool citation: %q", got)
	}
	if strings.Contains(got, "Read xyz") {
		t.Errorf("interrupt flush still uses bare 'Read' verb (QUM-556 regression): %q", got)
	}
}

func TestBuildInterruptFlushPrompt_Empty(t *testing.T) {
	if got := inboxprompt.BuildInterruptFlushPrompt(nil); got != "" {
		t.Errorf("expected empty prompt for nil entries, got %q", got)
	}
}

// TestBuildInterruptFlushPrompt_SingleEntry pins the interrupt-line shape:
// same tag, prefixed inside with `[interrupt] `.
func TestBuildInterruptFlushPrompt_SingleEntry(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "uuid-int-1", ShortID: "xyz", Class: inboxprompt.ClassInterrupt,
		From: "weave", Subject: "stop", Body: "reprioritize",
		Tags: []string{"resume_hint:writing tests"},
	}}
	got := inboxprompt.BuildInterruptFlushPrompt(entries)
	want := "<system-notification type=\"message\" interrupt=\"true\">[interrupt] From weave — mcp__sprawl__messages_read(id=xyz)</system-notification>\n"
	if got != want {
		t.Errorf("BuildInterruptFlushPrompt mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildInterruptFlushPrompt_SingleEntry_FallsBackToID covers entries
// without a ShortID; the interrupt line falls back to Entry.ID.
func TestBuildInterruptFlushPrompt_SingleEntry_FallsBackToID(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "uuid-bar", ShortID: "", Class: inboxprompt.ClassInterrupt,
		From: "weave", Body: "stop",
	}}
	got := inboxprompt.BuildInterruptFlushPrompt(entries)
	want := "<system-notification type=\"message\" interrupt=\"true\">[interrupt] From weave — mcp__sprawl__messages_read(id=uuid-bar)</system-notification>\n"
	if got != want {
		t.Errorf("BuildInterruptFlushPrompt fallback mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildInterruptFlushPrompt_Multiple pins N interrupt entries → N lines.
func TestBuildInterruptFlushPrompt_Multiple(t *testing.T) {
	entries := []inboxprompt.Entry{
		{ID: "u1", ShortID: "s1", From: "a", Body: "b1"},
		{ID: "u2", ShortID: "s2", From: "b", Body: "b2"},
	}
	got := inboxprompt.BuildInterruptFlushPrompt(entries)
	want := "<system-notification type=\"message\" interrupt=\"true\">[interrupt] From a — mcp__sprawl__messages_read(id=s1)</system-notification>\n" +
		"<system-notification type=\"message\" interrupt=\"true\">[interrupt] From b — mcp__sprawl__messages_read(id=s2)</system-notification>\n"
	if got != want {
		t.Errorf("BuildInterruptFlushPrompt multi mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildInterruptFlushPrompt_NoBodyInlined guards against any reintroduction
// of the verbose pre-QUM-555 interrupt frame.
func TestBuildInterruptFlushPrompt_NoBodyInlined(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "u1", ShortID: "s1", From: "weave",
		Subject: "secret-subject", Body: "secret-body-content",
		Tags: []string{"resume_hint:secret-hint"},
	}}
	got := inboxprompt.BuildInterruptFlushPrompt(entries)
	for _, banned := range []string{
		"secret-subject", "secret-body-content", "secret-hint",
		"After reading, decide", "has injected", "your previous task",
		"Subject:", "resume the interrupted",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("BuildInterruptFlushPrompt leaked %q in output: %q", banned, got)
		}
	}
}

// --- QUM-559: BuildStatusNotification tests ---
//
// The new formatter renders a single `<system-notification>` line per status
// report, distinct from the message-queue formatters above. The line has the
// shape:
//   <system-notification>$AGENT changed status to $STATE: $SUMMARY</system-notification>\n
// No body inlining, no `mcp__sprawl__messages_read` citation (this is a status
// channel, not a mail channel).

// TestBuildStatusNotification_Shape pins the exact wire format for each of
// the four canonical report states.
func TestBuildStatusNotification_Shape(t *testing.T) {
	cases := []struct {
		state string
		want  string
	}{
		{
			state: "working",
			want:  "<system-notification type=\"status_change\">finn changed status to working: doing X</system-notification>\n",
		},
		{
			state: "blocked",
			want:  "<system-notification type=\"status_change\">finn changed status to blocked: doing X</system-notification>\n",
		},
		{
			state: "complete",
			want:  "<system-notification type=\"status_change\">finn changed status to complete: doing X</system-notification>\n",
		},
		{
			state: "failure",
			want:  "<system-notification type=\"status_change\">finn changed status to failure: doing X</system-notification>\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			got := inboxprompt.BuildStatusNotification("finn", tc.state, "doing X")
			if got != tc.want {
				t.Errorf("BuildStatusNotification(%q) mismatch\n got: %q\nwant: %q", tc.state, got, tc.want)
			}
		})
	}
}

// TestBuildStatusNotification_NoToolCitation guards against copy-paste from
// the queue-flush formatter: the status notification is its own channel and
// must NOT cite `mcp__sprawl__messages_read` (no maildir entry exists for
// reports after QUM-559).
func TestBuildStatusNotification_NoToolCitation(t *testing.T) {
	got := inboxprompt.BuildStatusNotification("finn", "working", "doing X")
	if strings.Contains(got, "mcp__sprawl__messages_read") {
		t.Errorf("status notification must not cite mcp__sprawl__messages_read: %q", got)
	}
	// And no maildir id= shape either.
	if strings.Contains(got, "id=") {
		t.Errorf("status notification must not include id= citation: %q", got)
	}
}

// TestBuildStatusNotification_FailureBlockedSubstrings — the QUM-557
// TUI color-coder triggers on the literal substrings " to failure: " and
// " to blocked: " in the rendered line. Pin those substrings so any future
// rewording of the formatter must update the color-coder in lockstep.
func TestBuildStatusNotification_FailureBlockedSubstrings(t *testing.T) {
	failureLine := inboxprompt.BuildStatusNotification("finn", "failure", "oops")
	if !strings.Contains(failureLine, " to failure: ") {
		t.Errorf("failure status missing ' to failure: ' substring: %q", failureLine)
	}
	blockedLine := inboxprompt.BuildStatusNotification("finn", "blocked", "waiting")
	if !strings.Contains(blockedLine, " to blocked: ") {
		t.Errorf("blocked status missing ' to blocked: ' substring: %q", blockedLine)
	}
}

func TestSplitByClass(t *testing.T) {
	entries := []inboxprompt.Entry{
		{ID: "1", Class: inboxprompt.ClassAsync},
		{ID: "2", Class: inboxprompt.ClassInterrupt},
		{ID: "3", Class: inboxprompt.ClassAsync},
		{ID: "4", Class: inboxprompt.ClassInterrupt},
	}
	interrupts, asyncs := inboxprompt.SplitByClass(entries)
	if len(interrupts) != 2 || interrupts[0].ID != "2" || interrupts[1].ID != "4" {
		t.Errorf("unexpected interrupts: %+v", interrupts)
	}
	if len(asyncs) != 2 || asyncs[0].ID != "1" || asyncs[1].ID != "3" {
		t.Errorf("unexpected asyncs: %+v", asyncs)
	}
}

func TestSplitByClass_Empty(t *testing.T) {
	interrupts, asyncs := inboxprompt.SplitByClass(nil)
	if interrupts != nil || asyncs != nil {
		t.Errorf("expected nil slices for nil input, got %+v / %+v", interrupts, asyncs)
	}
}

// TestBuildPromptsMatchGolden pins the byte-exact wire format of the
// slim QUM-555 frames against committed golden files. Fixture covers two
// async entries (one with ShortID, one with empty ShortID to exercise the
// fallback path) and two interrupt entries (likewise).
func TestBuildPromptsMatchGolden(t *testing.T) {
	asyncs := []inboxprompt.Entry{
		{
			ID:      "uuid-async-1",
			ShortID: "sh1",
			Class:   inboxprompt.ClassAsync,
			From:    "child-alpha",
			Subject: "status",
			Body:    "all green",
			Tags:    []string{"fyi", "status"},
		},
		{
			ID:      "uuid-async-2",
			ShortID: "",
			Class:   inboxprompt.ClassAsync,
			From:    "child-beta",
			Subject: "ping",
			Body:    "hi",
		},
	}
	interrupts := []inboxprompt.Entry{
		{
			ID:      "uuid-int-1",
			ShortID: "si1",
			Class:   inboxprompt.ClassInterrupt,
			From:    "weave",
			Subject: "stop",
			Body:    "reprioritize now",
			Tags:    []string{"resume_hint:writing tests"},
		},
		{
			ID:      "uuid-int-2",
			ShortID: "",
			Class:   inboxprompt.ClassInterrupt,
			From:    "ratz",
			Subject: "urgent",
			Body:    "halt",
		},
	}

	cases := []struct {
		name   string
		golden string
		got    string
	}{
		{"queue_flush", "queue_flush.golden", inboxprompt.BuildQueueFlushPrompt(asyncs)},
		{"interrupt_flush", "interrupt_flush.golden", inboxprompt.BuildInterruptFlushPrompt(interrupts)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join("testdata", tc.golden)
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v", path, err)
			}
			if tc.got != string(want) {
				t.Errorf("%s: byte mismatch with %s\n--- got (%d bytes) ---\n%s\n--- want (%d bytes) ---\n%s",
					tc.name, path, len(tc.got), tc.got, len(want), string(want))
			}
		})
	}
}

// --- QUM-730: heartbeat / liveness_check notification --------------------

// TestBuildHeartbeatNotification_VerbatimBody pins the exact wire string the
// supervisor heartbeat injects into a stalled child's next-turn prompt. The
// body is intentionally chatty (it shows up in the child's transcript) so any
// future tweak to the wording must update this golden in lockstep.
//
// The verbatim body — including the trailing newline — is:
//
//	<system-notification type="liveness_check">This is an automated liveness check from the sprawl system. If there's no work to do just ignore this message. If you're still waiting on something or you were in the middle of something, please continue your work.</system-notification>\n
func TestBuildHeartbeatNotification_VerbatimBody(t *testing.T) {
	want := `<system-notification type="liveness_check">This is an automated liveness check from the sprawl system. If there's no work to do just ignore this message. If you're still waiting on something or you were in the middle of something, please continue your work.</system-notification>` + "\n"
	got := inboxprompt.BuildHeartbeatNotification()
	if got != want {
		t.Errorf("BuildHeartbeatNotification mismatch\n got: %q\nwant: %q", got, want)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("BuildHeartbeatNotification must end with newline: %q", got)
	}
	if !strings.Contains(got, `type="liveness_check"`) {
		t.Errorf("must carry type=\"liveness_check\" attribute: %q", got)
	}
}

// --- QUM-1064: status_change last-wins-per-agent coalescing ---------------

// statusSeed is one status_change envelope to write in a test fixture.
type statusSeed struct{ from, state, summary string }

// seedStatusChanges writes the given envelopes into recipient's maildir under
// root, one second apart on a backdated clock.
//
// The messages.NowFunc override is load-bearing, not cosmetic: DrainStatusChange
// sorts by Timestamp parsed as RFC3339 (second resolution) with a non-stable
// sort.Slice, so envelopes written within the same wall-clock second have an
// arbitrary relative order — and under last-wins coalescing that decides which
// payload survives. Stepping the clock makes "newest" well-defined.
//
// Mutates the process-global messages.NowFunc, so callers must not t.Parallel().
func seedStatusChanges(t *testing.T, root, recipient string, seeds []statusSeed) {
	t.Helper()
	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	orig := messages.NowFunc
	t.Cleanup(func() { messages.NowFunc = orig })
	for i, s := range seeds {
		messages.NowFunc = func() time.Time { return base.Add(time.Duration(i) * time.Second) }
		if _, err := messages.SendStatusChange(root, s.from, recipient, messages.StatusChangePayload{
			State:   s.state,
			Summary: s.summary,
		}); err != nil {
			t.Fatalf("seeding status envelope %d (%s/%s): %v", i, s.from, s.summary, err)
		}
	}
}

// countStatusOnDisk reports how many status_change envelopes are sitting in
// recipient's maildir. messages.List is non-destructive, so it can be called
// on both sides of the drain.
func countStatusOnDisk(t *testing.T, root, recipient string) int {
	t.Helper()
	msgs, err := messages.List(root, recipient, "status")
	if err != nil {
		t.Fatalf("listing status envelopes for %s: %v", recipient, err)
	}
	return len(msgs)
}

// assertSeededOnDisk is the positive control: it proves the fixture actually
// landed n status envelopes before the destructive drain runs. Without it, a
// broken seed yields 0 envelopes → 0 lines, and a "collapsed to one line"
// assertion is satisfiable vacuously by an empty maildir.
func assertSeededOnDisk(t *testing.T, root, recipient string, n int) {
	t.Helper()
	pre, err := messages.List(root, recipient, "status")
	if err != nil {
		t.Fatalf("positive control: pre-drain List: %v", err)
	}
	if len(pre) != n {
		t.Fatalf("positive control failed: seeded %d status envelopes but %d are on disk pre-drain; "+
			"a post-drain line count would prove nothing", n, len(pre))
	}
}

// TestDrainStatusChangeLines_CoalescesPerAgentLastWins is the primary red-first
// test for QUM-1064: five envelopes from one agent must render one line
// carrying the newest state and summary.
//
// Note this is NOT already covered by the 7639e0f frame dedup: those payloads
// all differ, so boundSystemFrame's byte-identical collapse cannot touch them.
func TestDrainStatusChangeLines_CoalescesPerAgentLastWins(t *testing.T) {
	root := t.TempDir()
	seedStatusChanges(t, root, "alice", []statusSeed{
		{"bob", "working", "s0"},
		{"bob", "working", "s1"},
		{"bob", "working", "s2"},
		{"bob", "working", "s3"},
		{"bob", "complete", "s4"},
	})
	assertSeededOnDisk(t, root, "alice", 5)

	lines := inboxprompt.DrainStatusChangeLines(root, "alice")
	if len(lines) != 1 {
		t.Fatalf("expected 5 envelopes from one agent to coalesce to 1 line, got %d: %q", len(lines), lines)
	}
	want := inboxprompt.BuildStatusNotification("bob", "complete", "s4")
	if lines[0] != want {
		t.Errorf("survivor line mismatch\n got: %q\nwant: %q", lines[0], want)
	}
	// The superseded envelopes must be gone from disk, not merely omitted from
	// this batch: a coalescer that peeks and then drains only the survivor
	// satisfies every line-count assertion above while leaving the backlog to
	// re-render on the next turn — the exact failure QUM-1064 exists to fix.
	if left := countStatusOnDisk(t, root, "alice"); left != 0 {
		t.Errorf("coalesced-away envelopes left on disk: %d remain after the drain, want 0", left)
	}
}

// TestDrainStatusChangeLines_DistinctAgentsAllSurvive is the discriminator:
// without it, "one line" is satisfiable by a rule that keeps only the newest
// envelope overall. Green before the change too — a no-regression guard, not a
// red-first test.
func TestDrainStatusChangeLines_DistinctAgentsAllSurvive(t *testing.T) {
	root := t.TempDir()
	seedStatusChanges(t, root, "alice", []statusSeed{
		{"bob", "working", "b"},
		{"carol", "working", "c"},
		{"dave", "working", "d"},
	})
	assertSeededOnDisk(t, root, "alice", 3)

	want := []string{
		inboxprompt.BuildStatusNotification("bob", "working", "b"),
		inboxprompt.BuildStatusNotification("carol", "working", "c"),
		inboxprompt.BuildStatusNotification("dave", "working", "d"),
	}
	got := inboxprompt.DrainStatusChangeLines(root, "alice")
	if len(got) != len(want) {
		t.Fatalf("expected %d distinct agents to yield %d lines, got %d: %q", len(want), len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line[%d] mismatch\n got: %q\nwant: %q", i, got[i], want[i])
		}
	}
}

// TestDrainStatusChangeLines_SurvivorOrderIsFirstAppearance pins the ordering
// contract. QUM-1064 leaves the choice between first-appearance and
// last-appearance to the implementer but requires it be pinned; first-appearance
// is what we chose, because it gives each agent a stable slot regardless of how
// chatty it is and is byte-identical to the pre-QUM-1064 output whenever every
// sender is distinct. The sole alternative — last-appearance, which under this
// fixture's monotonic clock is identical to surviving-envelope-timestamp order —
// would emit bob, dave, carol, frank, erin, so the goldens below distinguish the
// two.
//
// Six senders in an interleaved seed make the map-iteration failure mode
// structural rather than lucky: a coalescer emitting by ranging over its map
// would have to hit 1 of 120 orderings to pass.
//
// The golden comparison pins both dimensions at once: order is first-appearance
// and content is last-wins.
func TestDrainStatusChangeLines_SurvivorOrderIsFirstAppearance(t *testing.T) {
	root := t.TempDir()
	seedStatusChanges(t, root, "alice", []statusSeed{
		{"bob", "working", "s1"},
		{"carol", "working", "s2"},
		{"bob", "working", "s3"},
		{"dave", "working", "s4"},
		{"erin", "working", "s5"},
		{"carol", "working", "s6"},
		{"frank", "working", "s7"},
		{"erin", "complete", "s8"},
	})
	assertSeededOnDisk(t, root, "alice", 8)

	want := []string{
		inboxprompt.BuildStatusNotification("bob", "working", "s3"),
		inboxprompt.BuildStatusNotification("carol", "working", "s6"),
		inboxprompt.BuildStatusNotification("dave", "working", "s4"),
		inboxprompt.BuildStatusNotification("erin", "complete", "s8"),
		inboxprompt.BuildStatusNotification("frank", "working", "s7"),
	}
	got := inboxprompt.DrainStatusChangeLines(root, "alice")
	if len(got) != len(want) {
		t.Fatalf("expected %d survivors, got %d: %q", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("survivor[%d] mismatch\n got: %q\nwant: %q\nfull output: %q", i, got[i], want[i], got)
		}
	}
}

// captureSlog swaps the process-global default logger for a JSON handler over a
// buffer and restores it on cleanup. Callers must not t.Parallel().
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

// findLogRecords returns every JSON log record in buf whose "msg" equals msg.
func findLogRecords(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

const coalesceLogMsg = "inboxprompt: coalesced status_change lines"

// TestDrainStatusChangeLines_LogsWhenCoalesced pins the observability
// requirement: once lines are collapsed, the log is the only place the backlog
// depth is visible. Fields are parsed from JSON rather than substring-matched
// so a rename fails here.
func TestDrainStatusChangeLines_LogsWhenCoalesced(t *testing.T) {
	root := t.TempDir()
	seedStatusChanges(t, root, "alice", []statusSeed{
		{"bob", "working", "s0"},
		{"bob", "working", "s1"},
		{"bob", "working", "s2"},
		{"bob", "complete", "s3"},
	})
	assertSeededOnDisk(t, root, "alice", 4)

	buf := captureSlog(t)
	lines := inboxprompt.DrainStatusChangeLines(root, "alice")

	recs := findLogRecords(t, buf, coalesceLogMsg)
	if len(recs) != 1 {
		t.Fatalf("expected exactly 1 Info record %q, got %d; logs:\n%s", coalesceLogMsg, len(recs), buf.String())
	}
	rec := recs[0]
	if rec["level"] != "INFO" {
		t.Errorf("log level = %v, want INFO", rec["level"])
	}
	if rec["recipient"] != "alice" {
		t.Errorf("log recipient = %v, want alice", rec["recipient"])
	}
	// Cross-check the counts against reality rather than against the constants
	// the fixture used: a log that reports drained=4/emitted=1 while returning 4
	// lines is a lie the field assertions alone cannot catch.
	if rec["drained"] != float64(4) {
		t.Errorf("log drained = %v, want 4 (the seeded envelope count)", rec["drained"])
	}
	if rec["emitted"] != float64(len(lines)) {
		t.Errorf("log emitted = %v but the drain returned %d lines", rec["emitted"], len(lines))
	}
	if len(lines) != 1 {
		t.Errorf("expected 4 envelopes from one agent to coalesce to 1 line, got %d: %q", len(lines), lines)
	}
}

// TestDrainStatusChangeLines_NoLogWhenNothingCoalesced is the negative control
// for the test above: an unconditional log would satisfy that one.
func TestDrainStatusChangeLines_NoLogWhenNothingCoalesced(t *testing.T) {
	root := t.TempDir()
	seedStatusChanges(t, root, "alice", []statusSeed{
		{"bob", "working", "b"},
		{"carol", "working", "c"},
		{"dave", "working", "d"},
	})
	assertSeededOnDisk(t, root, "alice", 3)

	buf := captureSlog(t)
	lines := inboxprompt.DrainStatusChangeLines(root, "alice")
	if len(lines) != 3 {
		t.Fatalf("precondition: expected 3 lines, got %d", len(lines))
	}
	if recs := findLogRecords(t, buf, coalesceLogMsg); len(recs) != 0 {
		t.Errorf("no coalesce log expected when nothing was collapsed, got: %v", recs)
	}
}

// TestDrainStatusChangeLines_EmptyReturnsNil keeps the coalesce assertions from
// being satisfiable by an implementation that always emits one line.
func TestDrainStatusChangeLines_EmptyReturnsNil(t *testing.T) {
	if lines := inboxprompt.DrainStatusChangeLines(t.TempDir(), "alice"); lines != nil {
		t.Errorf("empty drain must return nil, got %#v", lines)
	}
}

// writeRawStatusEnvelope drops a status_change envelope onto disk with an
// arbitrary (possibly non-JSON) body. messages.SendStatusChange can only
// produce well-formed bodies, so this is the only way to exercise the
// corrupt-envelope path.
func writeRawStatusEnvelope(t *testing.T, root, recipient, from, body string, ts time.Time) {
	t.Helper()
	dir := filepath.Join(root, ".sprawl", "messages", recipient, "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	env := map[string]string{
		"id":        fmt.Sprintf("%d.%s.deadbeef", ts.UnixNano(), from),
		"from":      from,
		"to":        recipient,
		"body":      body,
		"timestamp": ts.UTC().Format(time.RFC3339),
		"type":      "status_change",
	}
	blob, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshalling raw envelope: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.%s.deadbeef.json", ts.UnixNano(), from))
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("writing raw envelope: %v", err)
	}
}

// TestDrainStatusChangeLines_CorruptEnvelopeDoesNotEraseValidStatus pins that a
// body which fails to decode is skipped rather than winning the last-wins race.
//
// Before coalescing, a corrupt envelope rendered one garbled line among N and
// the agent's real states still reached the recipient. Under last-wins a corrupt
// newest envelope would be the ONLY thing the recipient ever sees for that
// agent — a strictly worse outcome than the behaviour it replaced. Skipping the
// undecodable envelope keeps the last *valid* snapshot.
func TestDrainStatusChangeLines_CorruptEnvelopeDoesNotEraseValidStatus(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedStatusChanges(t, root, "alice", []statusSeed{{"bob", "complete", "done"}})
	// Newest envelope for bob, and undecodable.
	writeRawStatusEnvelope(t, root, "alice", "bob", "{not json", base.Add(time.Hour))
	assertSeededOnDisk(t, root, "alice", 2)

	lines := inboxprompt.DrainStatusChangeLines(root, "alice")
	want := inboxprompt.BuildStatusNotification("bob", "complete", "done")
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("corrupt newest envelope must not displace the last valid snapshot\n got: %q\nwant: [%q]", lines, want)
	}
	// The corrupt envelope is still consumed — skipping it must not leave it on
	// disk to be re-read every turn.
	if left := countStatusOnDisk(t, root, "alice"); left != 0 {
		t.Errorf("%d envelopes left on disk after the drain, want 0", left)
	}
}

// TestDrainStatusChangeLines_SameSecondBurstKeepsNewest guards the ordering
// dependency QUM-1064 made load-bearing. DrainStatusChange sorts on an RFC3339
// timestamp, which is second-resolution: a burst of reports inside one wall-clock
// second carries identical timestamps, so every one of them is a sort tie. That
// used to be cosmetic (all N lines were emitted regardless of order); under
// last-wins the tie-break decides which payload survives, and the drain is
// destructive, so the losers are gone.
//
// This cannot be made red-first: the pre-existing sort.Slice happened to leave
// this input alone. That is an accident of pdqsort and of ReadDir returning
// lexically sorted names that are also numerically ordered — not a contract —
// so DrainStatusChange now uses sort.SliceStable and this pins it.
//
// Mutation control (recorded): reversing `found` immediately before the stable
// sort in messages.DrainStatusChange makes this fail, printing survivor "s00"
// instead of "s49". So the assertion does observe the tie-break order rather
// than passing on any arrangement.
func TestDrainStatusChangeLines_SameSecondBurstKeepsNewest(t *testing.T) {
	root := t.TempDir()
	const n = 50
	orig := messages.NowFunc
	t.Cleanup(func() { messages.NowFunc = orig })
	// Every envelope shares one wall-clock second: all timestamps tie.
	frozen := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		messages.NowFunc = func() time.Time { return frozen.Add(time.Duration(i) * time.Nanosecond) }
		if _, err := messages.SendStatusChange(root, "bob", "alice", messages.StatusChangePayload{
			State:   "working",
			Summary: fmt.Sprintf("s%02d", i),
		}); err != nil {
			t.Fatalf("seeding envelope %d: %v", i, err)
		}
	}
	assertSeededOnDisk(t, root, "alice", n)

	lines := inboxprompt.DrainStatusChangeLines(root, "alice")
	want := inboxprompt.BuildStatusNotification("bob", "working", fmt.Sprintf("s%02d", n-1))
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("same-second burst must coalesce to the newest write-order envelope\n got: %q\nwant: [%q]", lines, want)
	}
}
