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
