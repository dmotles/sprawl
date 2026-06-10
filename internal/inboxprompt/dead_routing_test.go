// QUM-725: tests for WrapForDeadTarget. RED phase — the function does not
// exist yet; this file pins the exact templated wrapper text the implementer
// must produce so the route-up notification format cannot drift.
package inboxprompt

import (
	"strings"
	"testing"
)

func TestWrapForDeadTarget_SingleDead_ExactTemplate(t *testing.T) {
	got := WrapForDeadTarget("alice", "engineer", []string{"engineer"}, "hi")
	want := "This message was sent to engineer but engineer is dead. Originating sender: alice. Original body:\n\nhi"
	if got != want {
		t.Errorf("WrapForDeadTarget single-dead\n got: %q\nwant: %q", got, want)
	}
}

func TestWrapForDeadTarget_MultiHop_EnumeratesNames(t *testing.T) {
	got := WrapForDeadTarget("alice", "engineer", []string{"engineer", "manager"}, "hi")
	want := "This message was sent to engineer but engineer, manager are dead. Originating sender: alice. Original body:\n\nhi"
	if got != want {
		t.Errorf("WrapForDeadTarget multi-hop\n got: %q\nwant: %q", got, want)
	}
}

func TestWrapForDeadTarget_ThreeHop_EnumeratesNames(t *testing.T) {
	got := WrapForDeadTarget("alice", "engineer", []string{"engineer", "manager", "foreman"}, "hi")
	want := "This message was sent to engineer but engineer, manager, foreman are dead. Originating sender: alice. Original body:\n\nhi"
	if got != want {
		t.Errorf("WrapForDeadTarget 3-hop\n got: %q\nwant: %q", got, want)
	}
}

func TestWrapForDeadTarget_EmptyBody_IncludesOriginalBodyMarker(t *testing.T) {
	got := WrapForDeadTarget("alice", "engineer", []string{"engineer"}, "")
	// The "Original body:" marker MUST still be present even with empty body,
	// followed by the blank-line separator. Otherwise the recipient cannot
	// tell the wrapper apart from a routed message with body == wrapper-text.
	if !strings.Contains(got, "Original body:\n\n") {
		t.Errorf("WrapForDeadTarget empty-body must still emit `Original body:\\n\\n`; got: %q", got)
	}
	// Empty body trails the marker with nothing.
	want := "This message was sent to engineer but engineer is dead. Originating sender: alice. Original body:\n\n"
	if got != want {
		t.Errorf("WrapForDeadTarget empty-body\n got: %q\nwant: %q", got, want)
	}
}

func TestWrapForDeadTarget_EmptySender_UsesUnknownPlaceholder(t *testing.T) {
	// Pin behavior: when the caller doesn't know the originating sender (e.g.
	// supervisor-injected route-up where caller identity is absent), the
	// wrapper substitutes "unknown" rather than producing "Originating sender:
	// . Original body:" with a stray period.
	got := WrapForDeadTarget("", "engineer", []string{"engineer"}, "hi")
	want := "This message was sent to engineer but engineer is dead. Originating sender: unknown. Original body:\n\nhi"
	if got != want {
		t.Errorf("WrapForDeadTarget empty-sender\n got: %q\nwant: %q", got, want)
	}
}

func TestWrapForDeadTarget_PreservesBodyVerbatim(t *testing.T) {
	body := "line1\nline2\n```code```\n\ttabbed\n"
	got := WrapForDeadTarget("alice", "engineer", []string{"engineer"}, body)
	// Body MUST appear verbatim after the "Original body:\n\n" separator.
	idx := strings.Index(got, "Original body:\n\n")
	if idx < 0 {
		t.Fatalf("missing `Original body:\\n\\n` marker; got: %q", got)
	}
	suffix := got[idx+len("Original body:\n\n"):]
	if suffix != body {
		t.Errorf("body not preserved verbatim\n got: %q\nwant: %q", suffix, body)
	}
}
