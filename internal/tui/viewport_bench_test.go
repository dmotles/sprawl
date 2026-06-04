package tui

import (
	"fmt"
	"testing"
)

// BenchmarkViewportModel_RenderAndUpdate_LongConversation measures
// renderAndUpdate cost on a 200-entry viewport (100 assistant + 100 tool).
// Runs two sub-benchmarks: "no-cache" (disableRenderCacheForTest=true) and
// "with-cache". Ratio should be ≥10x per QUM-667 acceptance criteria.
func BenchmarkViewportModel_RenderAndUpdate_LongConversation(b *testing.B) {
	build := func() *ViewportModel {
		theme := NewTheme("colour212")
		m := NewViewportModel(&theme)
		m.SetSize(120, 40)
		for i := 0; i < 100; i++ {
			m.AppendAssistantChunk(fmt.Sprintf("Message %d with **markdown** and a `code` span.\n\nSecond paragraph here.", i))
			m.FinalizeAssistantMessage()
			tid := fmt.Sprintf("t%d", i)
			m.AppendToolCall("Bash", tid, true, "ls", "ls -la /tmp")
			m.MarkToolResult(tid, "ok\nline2\nline3", false)
		}
		return &m
	}
	for _, cacheOn := range []bool{false, true} {
		name := "no-cache"
		if cacheOn {
			name = "with-cache"
		}
		b.Run(name, func(b *testing.B) {
			m := build()
			m.disableRenderCacheForTest = !cacheOn
			// Prime
			m.renderAndUpdate()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.renderAndUpdate()
			}
		})
	}
}
