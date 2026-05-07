// internal/memory/context_test.go — red-phase tests for QUM-517 cutover.
// The new BuildContextBlob:
//   - renders a "## Project Arc" section produced by an injectable arc
//     summarizer (WithArcSummarizer);
//   - appends the canonical footer pointing at timeline.md and the per-session
//     handoff files;
//   - renders a SINGLE-SENTENCE inbox summary (no per-message enumeration);
//   - does NOT enumerate the session timeline or recent sessions; both the
//     "## Session Timeline" and "## Recent Sessions" sections are gone.
//   - still surfaces persistent.md content under "## Persistent Knowledge".
package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/messages"
)

// canonicalFooter is the literal footer string that BuildContextBlob must
// append. Embedded backticks make this awkward in Go raw strings, so we
// build it by concatenation.
var canonicalFooter = "Read `.sprawl/memory/timeline.md` for the full session index. " +
	"Read `.sprawl/memory/sessions/<id>.md` for the full handoff of any session."

func stubArc(summary string) BuildOption {
	return WithArcSummarizer(func(_ context.Context, _ string) (string, error) {
		return summary, nil
	})
}

// emptyDeps returns the option set for a context blob built against an empty
// project: no agents, no messages, no persistent knowledge, with the arc
// summarizer stubbed.
func emptyDeps(arc string) []BuildOption {
	return []BuildOption{
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) { return nil, nil }),
		WithPersistentKnowledgeReader(func(string) (string, error) { return "", nil }),
		stubArc(arc),
	}
}

// ---------------------------------------------------------------------
// RendersArcSection
// ---------------------------------------------------------------------
func TestBuildContextBlob_RendersArcSection(t *testing.T) {
	stub := "Project began with timeline regen, then unified into append-only memory.\n"
	blob, err := BuildContextBlob("fake-root", "weave", emptyDeps(stub)...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}

	if !strings.Contains(blob, "## Project Arc") {
		t.Error("expected blob to contain '## Project Arc' header")
	}
	if !strings.Contains(blob, "Project began with timeline regen") {
		t.Errorf("expected blob to contain stub arc summary; got:\n%s", blob)
	}

	// Header must come BEFORE its content.
	idxHeader := strings.Index(blob, "## Project Arc")
	idxContent := strings.Index(blob, "Project began with timeline regen")
	if idxHeader < 0 || idxContent < 0 || idxHeader > idxContent {
		t.Errorf("'## Project Arc' header must precede its content (header=%d content=%d)", idxHeader, idxContent)
	}
}

// ---------------------------------------------------------------------
// AppendsCanonicalFooter
// ---------------------------------------------------------------------
func TestBuildContextBlob_AppendsCanonicalFooter(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "weave", emptyDeps("arc\n")...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	if !strings.Contains(blob, canonicalFooter) {
		t.Errorf("expected blob to contain canonical footer\nwant substring: %q\ngot:\n%s", canonicalFooter, blob)
	}
}

// ---------------------------------------------------------------------
// NoTimelineSectionEnumerated
// ---------------------------------------------------------------------
func TestBuildContextBlob_NoTimelineSectionEnumerated(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "weave", emptyDeps("arc\n")...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	if strings.Contains(blob, "## Session Timeline") {
		t.Errorf("blob should NOT contain '## Session Timeline' header in the new model:\n%s", blob)
	}
	// No row matching TimelineRowRE should appear in the body — there's
	// nothing to render rows from in this test (no session/timeline lister
	// is even consulted by the new BuildContextBlob).
	for _, line := range strings.Split(blob, "\n") {
		if TimelineRowRE.MatchString(line) {
			t.Errorf("blob should not enumerate timeline rows; found %q", line)
		}
	}
}

// ---------------------------------------------------------------------
// NoRecentSessionsSection
// ---------------------------------------------------------------------
func TestBuildContextBlob_NoRecentSessionsSection(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "weave", emptyDeps("arc\n")...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	if strings.Contains(blob, "## Recent Sessions") {
		t.Errorf("blob should NOT contain '## Recent Sessions' header in the new model:\n%s", blob)
	}
}

// ---------------------------------------------------------------------
// InboxCompact_WithMessages — single-sentence summary, no per-msg detail
// ---------------------------------------------------------------------
func TestBuildContextBlob_InboxCompact_WithMessages(t *testing.T) {
	msgs := []*messages.Message{
		{From: "alice", Subject: "build failed", Timestamp: "2026-05-07T11:00:00Z"},
		{From: "bob", Subject: "review pls", Timestamp: "2026-05-07T11:05:00Z"},
		{From: "carol", Subject: "merge conflict", Timestamp: "2026-05-07T11:10:00Z"},
	}

	opts := []BuildOption{
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) { return msgs, nil }),
		WithPersistentKnowledgeReader(func(string) (string, error) { return "", nil }),
		stubArc("arc\n"),
	}

	blob, err := BuildContextBlob("fake-root", "weave", opts...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}

	wantSentence := "3 messages in inbox. Recommend archiving stale messages when possible."
	if !strings.Contains(blob, wantSentence) {
		t.Errorf("expected compact inbox sentence %q in blob; got:\n%s", wantSentence, blob)
	}

	// Must NOT enumerate individual messages (sender/subject pairs).
	for _, m := range msgs {
		if strings.Contains(blob, m.Subject) {
			t.Errorf("blob enumerates message subject %q; should be compact only", m.Subject)
		}
		if strings.Contains(blob, "From "+m.From) {
			t.Errorf("blob enumerates message sender %q; should be compact only", m.From)
		}
	}
}

// ---------------------------------------------------------------------
// InboxEmpty
// ---------------------------------------------------------------------
func TestBuildContextBlob_InboxEmpty(t *testing.T) {
	blob, err := BuildContextBlob("fake-root", "weave", emptyDeps("arc\n")...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	if strings.Contains(blob, "messages in inbox") {
		t.Errorf("blob should NOT mention 'messages in inbox' when inbox is empty:\n%s", blob)
	}
}

// ---------------------------------------------------------------------
// PersistentKnowledgeStillRendered
// ---------------------------------------------------------------------
func TestBuildContextBlob_PersistentKnowledgeStillRendered(t *testing.T) {
	pk := "Project uses cobra for CLI.\nAlways run make validate before commit."
	opts := []BuildOption{
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) { return nil, nil }),
		WithPersistentKnowledgeReader(func(string) (string, error) { return pk, nil }),
		stubArc("arc\n"),
	}

	blob, err := BuildContextBlob("fake-root", "weave", opts...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	if !strings.Contains(blob, "## Persistent Knowledge") {
		t.Error("expected blob to contain '## Persistent Knowledge' header")
	}
	if !strings.Contains(blob, "Project uses cobra for CLI.") {
		t.Error("expected persistent knowledge content to be rendered")
	}
	if !strings.Contains(blob, "Always run make validate before commit.") {
		t.Error("expected second persistent knowledge line to be rendered")
	}
}

// ---------------------------------------------------------------------
// QUM-521: Last Session block — verbatim render of most recent SEALED session.
// ---------------------------------------------------------------------

// stubSessionLister returns a BuildOption injecting a sessionLister that
// returns the given sessions/bodies regardless of n. Sessions are oldest-first
// to mirror ListRecentSessions semantics.
func stubSessionLister(sessions []Session, bodies []string) BuildOption {
	return WithSessionLister(func(_ string, _ int) ([]Session, []string, error) {
		return sessions, bodies, nil
	})
}

func stubLastSessionID(id string) BuildOption {
	return WithLastSessionIDReader(func(_ string) (string, error) { return id, nil })
}

// baseOpts builds an option set with empty inbox, no PK, stub arc,
// plus the supplied extra options applied last (so they override).
func baseOpts(extra ...BuildOption) []BuildOption {
	o := []BuildOption{
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) { return nil, nil }),
		WithPersistentKnowledgeReader(func(string) (string, error) { return "", nil }),
		stubArc("arc\n"),
	}
	return append(o, extra...)
}

func TestBuildContextBlob_LastSession_OmittedWhenNoSealedSessions(t *testing.T) {
	opts := baseOpts(
		stubSessionLister(nil, nil),
		stubLastSessionID(""),
	)
	blob, err := BuildContextBlob("fake-root", "weave", opts...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	if strings.Contains(blob, "## Last Session") {
		t.Errorf("blob must NOT contain '## Last Session' when no sealed sessions exist; got:\n%s", blob)
	}
}

func TestBuildContextBlob_LastSession_RendersSingleSealed(t *testing.T) {
	ts := time.Date(2026, 5, 7, 12, 30, 0, 0, time.UTC)
	sessions := []Session{{SessionID: "abc", Timestamp: ts}}
	bodies := []string{"hello body\n"}
	opts := baseOpts(
		stubSessionLister(sessions, bodies),
		stubLastSessionID(""),
	)
	blob, err := BuildContextBlob("fake-root", "weave", opts...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	if !strings.Contains(blob, "## Last Session") {
		t.Errorf("expected '## Last Session' header in blob:\n%s", blob)
	}
	wantHeader := "### Session: abc (" + ts.Format(time.RFC3339) + ")"
	if !strings.Contains(blob, wantHeader) {
		t.Errorf("expected session header %q in blob:\n%s", wantHeader, blob)
	}
	if !strings.Contains(blob, "hello body") {
		t.Errorf("expected verbatim session body in blob:\n%s", blob)
	}
	if got := strings.Count(blob, "### Session:"); got != 1 {
		t.Errorf("expected exactly 1 '### Session:' occurrence, got %d:\n%s", got, blob)
	}
}

func TestBuildContextBlob_LastSession_OnlyNewestOfMultiple(t *testing.T) {
	t1 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	sessions := []Session{
		{SessionID: "s1", Timestamp: t1},
		{SessionID: "s2", Timestamp: t2},
		{SessionID: "s3", Timestamp: t3},
	}
	bodies := []string{"b1", "b2", "b3"}
	opts := baseOpts(
		stubSessionLister(sessions, bodies),
		stubLastSessionID(""),
	)
	blob, err := BuildContextBlob("fake-root", "weave", opts...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	if !strings.Contains(blob, "### Session: s3") {
		t.Errorf("expected newest session s3 to be rendered:\n%s", blob)
	}
	if !strings.Contains(blob, "b3") {
		t.Errorf("expected newest body 'b3' to be rendered:\n%s", blob)
	}
	for _, banned := range []string{"### Session: s1", "### Session: s2", "b1", "b2"} {
		if strings.Contains(blob, banned) {
			t.Errorf("blob contains older session content %q; only newest sealed should render:\n%s", banned, blob)
		}
	}
}

func TestBuildContextBlob_LastSession_LiveEqualsNewest_NoSealedRendered(t *testing.T) {
	ts := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	sessions := []Session{{SessionID: "X", Timestamp: ts}}
	bodies := []string{"live body"}
	opts := baseOpts(
		stubSessionLister(sessions, bodies),
		stubLastSessionID("X"),
	)
	blob, err := BuildContextBlob("fake-root", "weave", opts...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	if strings.Contains(blob, "## Last Session") {
		t.Errorf("blob must NOT include '## Last Session' when only session is live:\n%s", blob)
	}
	if strings.Contains(blob, "live body") {
		t.Errorf("blob must NOT include live session body:\n%s", blob)
	}
}

func TestBuildContextBlob_LastSession_LiveEqualsNewest_FallsBackToPrevious(t *testing.T) {
	tA := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	tB := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	sessions := []Session{
		{SessionID: "A", Timestamp: tA},
		{SessionID: "B", Timestamp: tB},
	}
	bodies := []string{"body-A", "body-B"}
	opts := baseOpts(
		stubSessionLister(sessions, bodies),
		stubLastSessionID("B"),
	)
	blob, err := BuildContextBlob("fake-root", "weave", opts...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	if !strings.Contains(blob, "## Last Session") {
		t.Errorf("expected '## Last Session' to render previous sealed session A:\n%s", blob)
	}
	if !strings.Contains(blob, "### Session: A") {
		t.Errorf("expected '### Session: A' header (B is live):\n%s", blob)
	}
	if !strings.Contains(blob, "body-A") {
		t.Errorf("expected body-A in blob:\n%s", blob)
	}
	if strings.Contains(blob, "### Session: B") {
		t.Errorf("blob must NOT include live session B header:\n%s", blob)
	}
	if strings.Contains(blob, "body-B") {
		t.Errorf("blob must NOT include live session B body:\n%s", blob)
	}
}

func TestBuildContextBlob_LastSession_PlacementBeforeInbox(t *testing.T) {
	ts := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	sessions := []Session{{SessionID: "abc", Timestamp: ts}}
	bodies := []string{"sealed-body"}
	msgs := []*messages.Message{
		{From: "alice", Subject: "build failed", Timestamp: "2026-05-07T11:00:00Z"},
	}
	opts := []BuildOption{
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) { return msgs, nil }),
		WithPersistentKnowledgeReader(func(string) (string, error) { return "", nil }),
		stubArc("arc\n"),
		stubSessionLister(sessions, bodies),
		stubLastSessionID(""),
	}
	blob, err := BuildContextBlob("fake-root", "weave", opts...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	idxFooter := strings.Index(blob, canonicalContextFooter)
	idxLast := strings.Index(blob, "## Last Session")
	idxInbox := strings.Index(blob, "messages in inbox")
	if idxFooter < 0 || idxLast < 0 || idxInbox < 0 {
		t.Fatalf("missing required section: footer=%d last=%d inbox=%d\n%s", idxFooter, idxLast, idxInbox, blob)
	}
	if idxFooter >= idxLast || idxLast >= idxInbox {
		t.Errorf("expected footer < ## Last Session < inbox sentence; got footer=%d last=%d inbox=%d\n%s",
			idxFooter, idxLast, idxInbox, blob)
	}
}

func TestBuildContextBlob_LastSession_PlacementBetweenFooterAndPersistent(t *testing.T) {
	ts := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	sessions := []Session{{SessionID: "abc", Timestamp: ts}}
	bodies := []string{"sealed-body"}
	opts := []BuildOption{
		WithMessageLister(func(string, string, string) ([]*messages.Message, error) { return nil, nil }),
		WithPersistentKnowledgeReader(func(string) (string, error) {
			return "PK content here.", nil
		}),
		stubArc("arc\n"),
		stubSessionLister(sessions, bodies),
		stubLastSessionID(""),
	}
	blob, err := BuildContextBlob("fake-root", "weave", opts...)
	if err != nil {
		t.Fatalf("BuildContextBlob: %v", err)
	}
	idxFooter := strings.Index(blob, canonicalContextFooter)
	idxLast := strings.Index(blob, "## Last Session")
	idxPK := strings.Index(blob, "## Persistent Knowledge")
	if idxFooter < 0 || idxLast < 0 || idxPK < 0 {
		t.Fatalf("missing required section: footer=%d last=%d pk=%d\n%s", idxFooter, idxLast, idxPK, blob)
	}
	if idxFooter >= idxLast || idxLast >= idxPK {
		t.Errorf("expected footer < ## Last Session < ## Persistent Knowledge; got footer=%d last=%d pk=%d\n%s",
			idxFooter, idxLast, idxPK, blob)
	}
}
