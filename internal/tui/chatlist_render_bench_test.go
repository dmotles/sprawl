package tui

// QUM-933 perf artifact. Sizes the per-frame render cost of a tool-heavy
// transcript, which is what the settle chokepoint fixes.
//
// The measured shape is the incident's: a backlog of assistant text blocks
// separated by resolved tool calls, plus ONE still-pending tool row. The
// pending row matters — ChatList.Update(toolTickMsg) calls invalidate() every
// toolSpinnerInterval (100ms) and Idle() is false while pendingTools > 0, so
// the outer Render cache is bypassed and every one of those ~10 frames/sec
// re-walks all envelopes. Before the fix each intermediate assistant block was
// permanently unfinished and therefore permanently uncacheable, so each frame
// re-ran the full goldmark+glamour pipeline over the whole backlog.
//
// Measured on a quiet 4-core arm64 box, interleaved parent/fix ×3:
//   parent (5f025a4^): 51.3 / 50.7 / 52.1 ms per frame
//   with the fix:       0.49 / 0.58 / 0.61 ms per frame   (~88x)
// At the pending row's 10 fps that is ~0.5 core of sustained burn versus
// ~0.5%, which is the reported "109% CPU with all agents idle".
//
// Not a gate (Go benchmarks have no pass/fail); the correctness guards live in
// chatlist_settle_test.go, whose glamour-call assertion pins the same property
// deterministically.
//
// Kept out of chatlist_longhist_bench_test.go on purpose: that file's trio is
// anchored on the committed 1500-frame QUM-674 fixture and its cache-hit /
// cold / steady-state shapes. This one is a synthetic orphan backlog measured
// on the spinner-tick repaint path, which is a different question.

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkChatList_Render_OrphanBacklog_SpinnerTick(b *testing.B) {
	para := strings.Repeat("This is a sentence of prose in an assistant message block. ", 25)
	body := func(i int) string {
		return fmt.Sprintf("## Step %d\n\n%s\n\n- point one with `code`\n- point two\n\n%s\n", i, para, para)
	}
	cl := newTestChatList()
	cl.SetSize(120)
	for i := 0; i < 22; i++ {
		cl.AppendAssistantChunk(body(i))
		id := fmt.Sprintf("t%d", i)
		cl.AppendToolCallWithHeader("Bash", id, true, "{}", "{}", "echo", nil, "")
		cl.MarkToolResult(id, "ok", false)
	}
	cl.AppendToolCallWithHeader("Bash", "pending", true, "{}", "{}", "sleep 120", nil, "")
	cl.Render(120)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Exactly what a spinner tick does: bump the revision, then repaint.
		cl.Update(toolTickMsg{ToolID: "pending"})
		cl.Render(120)
	}
}
