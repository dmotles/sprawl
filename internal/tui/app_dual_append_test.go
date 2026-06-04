package tui

// QUM-672 S2 — dual-append shadow invariants.
//
// The dual-append shim mirrors every user-typed turn into both the legacy
// ViewportModel (the live-render path) and the new ChatList (a silent
// shadow). These tests pin the invariant so future slices can't drift the
// two stores apart while we lean on the shim.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestAgentBuffer_AppendUser_MirrorsToViewportAndChatList asserts the unit-
// level invariant: AgentBuffer.AppendUser appends to vp (as MessageUser)
// AND to cl in lockstep.
func TestAgentBuffer_AppendUser_MirrorsToViewportAndChatList(t *testing.T) {
	theme := NewTheme("colour212")
	buf := &AgentBuffer{vp: NewViewportModel(&theme), cl: NewChatList(&theme)}

	inputs := []string{"hello", "second\nwith newline", "third"}
	for _, in := range inputs {
		buf.AppendUser(in)
	}

	var userMsgs []MessageEntry
	for _, e := range buf.vp.GetMessages() {
		if e.Type == MessageUser {
			userMsgs = append(userMsgs, e)
		}
	}
	if len(userMsgs) != len(inputs) {
		t.Fatalf("vp user-message count = %d, want %d", len(userMsgs), len(inputs))
	}
	for i, in := range inputs {
		if userMsgs[i].Content != in {
			t.Errorf("vp user msg %d content = %q, want %q", i, userMsgs[i].Content, in)
		}
	}

	if got := buf.cl.Len(); got != len(inputs) {
		t.Fatalf("cl.Len() = %d, want %d (lockstep with vp)", got, len(inputs))
	}
}

// TestAgentBuffer_AppendUser_RenderParity verifies that the ChatList side of
// the dual-append shim renders identically to a reference ChatList fed the
// same user-text sequence at the same width. This is the
// "Render(width) for user-only state matches what we'd expect" gate from
// QUM-672's per-slice validation list — independent of whether the shadow
// is wired to display yet.
func TestAgentBuffer_AppendUser_RenderParity(t *testing.T) {
	theme := NewTheme("colour212")
	buf := &AgentBuffer{vp: NewViewportModel(&theme), cl: NewChatList(&theme)}
	ref := NewChatList(&theme)

	const width = 80
	buf.cl.SetSize(width)
	ref.SetSize(width)

	inputs := []string{"hello", "wrap me a multi-line\nprompt body", "trailing"}
	for _, in := range inputs {
		buf.AppendUser(in)
		ref.AppendUser(in)
	}

	got := buf.cl.Render(width)
	want := ref.Render(width)
	if got != want {
		t.Errorf("cl.Render parity mismatch\n got: %q\nwant: %q", got, want)
	}
	// Sanity: the rendered string must surface every input.
	for _, in := range inputs {
		// Multi-line inputs are split into per-line chevron blocks, so the
		// first line is sufficient as a containment witness.
		head := strings.SplitN(in, "\n", 2)[0]
		if !strings.Contains(got, head) {
			t.Errorf("Render output missing user input %q\n%s", head, got)
		}
	}
}

// TestAppModel_SubmitMsg_DualAppendUserMessage exercises the production wire-
// up: SubmitMsg must route the user-typed turn through both the legacy
// viewport and the ChatList shadow on the root buffer. This is the slice's
// proof-of-wiring: the only production AppendUserMessage call site must use
// the dual-append helper.
func TestAppModel_SubmitMsg_DualAppendUserMessage(t *testing.T) {
	mock := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, mock)

	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	_, _ = app.Update(SubmitMsg{Text: "hello claude"})

	buf, ok := app.agentBuffers[app.rootAgent]
	if !ok {
		t.Fatalf("root buffer missing after SubmitMsg")
	}

	// vp side — the live-render path must show the user message.
	found := false
	for _, e := range buf.vp.GetMessages() {
		if e.Type == MessageUser && e.Content == "hello claude" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("vp missing user message after SubmitMsg")
	}

	// cl side — the shadow must have exactly one user item appended.
	if got := buf.cl.Len(); got != 1 {
		t.Errorf("cl.Len() = %d after SubmitMsg, want 1", got)
	}

	// Render parity at the production-sized width — the shadow's render must
	// surface the user text. (Production renders via vp; this just confirms
	// the shadow's state is queryable and contains what we appended.)
	if w := buf.cl.Width(); w > 0 {
		out := buf.cl.Render(w)
		if !strings.Contains(out, "hello claude") {
			t.Errorf("cl.Render at width %d missing user text; got: %q", w, out)
		}
	} else {
		t.Errorf("cl.Width() = 0 after WindowSizeMsg — SetSize wiring missing")
	}
}

// TestPreloadTranscript_MirrorsToChatList asserts the cold-boot resume path
// routes its entries through AgentBuffer.SetMessages so cl mirrors the
// resumed transcript alongside vp. Pre-fix this path called vp.SetMessages
// directly, leaving cl empty + Idle. QUM-673 post-review fix.
func TestPreloadTranscript_MirrorsToChatList(t *testing.T) {
	app := newBleedApp(t)

	entries := []MessageEntry{
		{Type: MessageUser, Content: "hi there", Complete: true},
		{Type: MessageAssistant, Content: "hello back", Complete: true},
	}
	app.PreloadTranscript(entries)

	buf := app.rootBuf()
	// vp side — the resumed entries land in the legacy store.
	if got := len(buf.vp.GetMessages()); got < 2 {
		t.Errorf("vp got %d entries after PreloadTranscript, want >=2", got)
	}
	// cl side — the shadow must mirror the entries (user + assistant = 2 items).
	if got := buf.cl.Len(); got != 2 {
		t.Errorf("cl.Len() = %d after PreloadTranscript, want 2 (user + assistant)", got)
	}
	if !buf.cl.Idle() {
		t.Errorf("cl must be Idle after finished transcript Reset")
	}
}

// TestSessionRestart_ResetsChatListWhenClearing asserts the
// preserved-status-only restart-clear path (in RestartCompleteMsg) routes
// through AgentBuffer.SetMessages so cl is Reset alongside vp. The status
// entries are skipped by ChatList.Reset (contract violators) so cl ends up
// empty + Idle. Pre-fix this path called root.SetMessages directly, leaving
// cl holding the prior session's items. QUM-673 post-review fix.
func TestSessionRestart_ResetsChatListWhenClearing(t *testing.T) {
	app := newBleedApp(t)

	// Seed cl + vp with a prior-session transcript.
	buf := app.rootBuf()
	buf.AppendUser("prior session message")
	buf.AppendAssistantChunk("prior session reply")
	buf.FinalizeAssistantMessage()
	if buf.cl.Len() == 0 {
		t.Fatalf("cl seed failed; expected items after AppendUser/AppendAssistantChunk")
	}

	// Simulate the restart-clear pass: build a preserved-status-only set and
	// route it through buf.SetMessages exactly as RestartCompleteMsg does.
	var preserved []MessageEntry
	for _, e := range buf.vp.GetMessages() {
		if e.Type == MessageStatus {
			preserved = append(preserved, e)
		}
	}
	preserved = append(preserved, MessageEntry{
		Type: MessageStatus, Content: "consolidation banner", Complete: true,
	})
	buf.SetMessages(preserved)

	if got := buf.cl.Len(); got != 0 {
		t.Errorf("cl.Len() = %d after restart-clear, want 0 (status entries are skipped)", got)
	}
	if !buf.cl.Idle() {
		t.Errorf("cl must be Idle after restart-clear Reset to empty")
	}
}
