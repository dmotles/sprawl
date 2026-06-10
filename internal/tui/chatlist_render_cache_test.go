package tui

// QUM-769 — outer ChatList.Render cache.
//
// The per-envelope cache (itemEnvelope.cache) memoizes finished-item Render
// output. The outer Render walk still rebuilds the concatenated string on
// every call — ~10ms / ~70MB for a 1500-envelope steady-state chat. This
// test file pins the new outer-Render cache: hits when nothing has changed,
// misses on every mutator path.
//
// Probe convention: ChatList exposes a private `renderBuilds` counter
// incremented inside buildRender (the cache-miss branch). Tests in-package
// can assert it without binding to a public surface.

import "testing"

// renderBuildsBaseline returns renderBuilds after one cold render so the
// per-test delta starts from 1 build.
func renderBuildsBaseline(t *testing.T, cl *ChatList, width int) int {
	t.Helper()
	cl.Render(width)
	return cl.renderBuilds
}

func TestChatList_RenderCache_HitOnSecondCallSameWidth(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("hello")
	cl.AppendUser("world")
	first := cl.Render(80)
	builds1 := cl.renderBuilds
	second := cl.Render(80)
	builds2 := cl.renderBuilds
	if second != first {
		t.Errorf("cached render diverged from first call")
	}
	if builds2 != builds1 {
		t.Errorf("renderBuilds incremented on cache hit: %d -> %d", builds1, builds2)
	}
}

func TestChatList_RenderCache_InvalidatesOnWidthChange(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("hello")
	cl.Render(80)
	baseline := cl.renderBuilds
	cl.SetSize(40)
	cl.Render(40)
	if cl.renderBuilds == baseline {
		t.Errorf("width change did not trigger rebuild (renderBuilds=%d unchanged)", baseline)
	}
}

func TestChatList_RenderCache_InvalidatesOnAppendUser(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("first")
	baseline := renderBuildsBaseline(t, cl, 80)
	cl.AppendUser("second")
	cl.Render(80)
	if cl.renderBuilds == baseline {
		t.Errorf("AppendUser did not invalidate outer Render cache")
	}
}

func TestChatList_RenderCache_InvalidatesOnAppendToolCall(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("hi")
	baseline := renderBuildsBaseline(t, cl, 80)
	cl.AppendToolCallWithHeader("Read", "t1", true, "{}", "{}", "foo", nil, "")
	cl.Render(80)
	if cl.renderBuilds == baseline {
		t.Errorf("AppendToolCall did not invalidate outer Render cache")
	}
}

func TestChatList_RenderCache_InvalidatesOnMarkToolResult(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Read", "t1", true, "{}", "{}", "foo", nil, "")
	baseline := renderBuildsBaseline(t, cl, 80)
	cl.MarkToolResult("t1", "ok", false)
	cl.Render(80)
	if cl.renderBuilds == baseline {
		t.Errorf("MarkToolResult did not invalidate outer Render cache")
	}
}

func TestChatList_RenderCache_InvalidatesOnExpandToggle(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Read", "t1", true, "{}", "{}", "foo", nil, "")
	cl.MarkToolResult("t1", "ok", false)
	baseline := renderBuildsBaseline(t, cl, 80)
	cl.SetToolInputsExpanded(true)
	cl.Render(80)
	if cl.renderBuilds == baseline {
		t.Errorf("SetToolInputsExpanded did not invalidate outer Render cache")
	}
}

func TestChatList_RenderCache_InvalidatesOnReset(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("hi")
	baseline := renderBuildsBaseline(t, cl, 80)
	cl.Reset([]MessageEntry{{Type: MessageUser, Content: "fresh"}})
	cl.Render(80)
	if cl.renderBuilds == baseline {
		t.Errorf("Reset did not invalidate outer Render cache")
	}
}

func TestChatList_RenderCache_StreamingBypassesOuterCache(t *testing.T) {
	// While an assistant is streaming, every Render must walk the items so
	// chunk-by-chunk text appears live. Outer cache must not short-circuit.
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("hel")
	cl.Render(80)
	baseline := cl.renderBuilds
	cl.Render(80)
	if cl.renderBuilds == baseline {
		t.Errorf("streaming Render hit outer cache; chunk updates would be lost")
	}
}

func TestChatList_RenderCache_InvalidatesOnFinalizeAssistant(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("hello")
	cl.Render(80)
	cl.FinalizeAssistantMessage()
	// After finalize, Render must rebuild once (was streaming, now idle and
	// cache is empty). Then a second Render must be a cache hit.
	cl.Render(80)
	baseline := cl.renderBuilds
	cl.Render(80)
	if cl.renderBuilds != baseline {
		t.Errorf("post-finalize cached Render rebuilt unexpectedly: %d -> %d", baseline, cl.renderBuilds)
	}
}

func TestChatList_RenderCache_RenderBuildsCountIsOneAfterRepeatedCalls(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	for i := 0; i < 5; i++ {
		cl.AppendUser("x")
	}
	cl.Render(80)
	baseline := cl.renderBuilds
	for i := 0; i < 10; i++ {
		cl.Render(80)
	}
	if cl.renderBuilds != baseline {
		t.Errorf("repeated steady-state Render rebuilt %d extra times", cl.renderBuilds-baseline)
	}
}
