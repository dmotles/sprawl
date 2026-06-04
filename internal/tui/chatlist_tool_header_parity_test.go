package tui

// QUM-674 S4 — tool-header byte-parity between the legacy ViewportModel.
// renderToolCall path and the new ToolCallItem.renderBox/renderNested path.
//
// The Phase 3 (QUM-659) cutover broke Bash rendering by porting the spike's
// summarize() verbatim instead of using the production formatToolHeader. S4
// brings the lifecycle into ChatList; this gate prevents that class of
// regression by asserting byte-for-byte equivalence on a fixed input matrix
// covering every tool the production renderer special-cases.
//
// Scope:
//   - depth=0 finished state for Bash, Read, Edit, Write, Glob, Grep, Task
//     (renderBox vs renderToolCall). Spinner divergence in the pending state
//     is intentional per Q6=option-a; finished state has no spinner glyph so
//     parity is exact.
//   - depth=1 nested rendering for Agent + a non-Agent child (renderNested
//     on cl vs renderNestedToolCall on vp).

import (
	"encoding/json"
	"strings"
	"testing"
)

// toolHeaderCase pairs a tool name with raw input JSON and a stable tool_use_id.
type toolHeaderCase struct {
	name   string
	toolID string
	raw    string // JSON input as it would arrive on the wire
}

func toolHeaderCases() []toolHeaderCase {
	return []toolHeaderCase{
		{"Bash", "tu_bash", `{"command":"ls -la /tmp","description":"list /tmp","timeout":5000}`},
		{"Read", "tu_read", `{"file_path":"/etc/hosts","offset":10,"limit":20}`},
		{"Edit", "tu_edit", `{"file_path":"/tmp/foo.txt","old_string":"a","new_string":"b","replace_all":true}`},
		{"Write", "tu_write", `{"file_path":"/tmp/out.txt","content":"hello"}`},
		{"Glob", "tu_glob", `{"pattern":"**/*.go","path":"/repo"}`},
		{"Grep", "tu_grep", `{"pattern":"TODO","path":"/repo","glob":"*.go","output_mode":"content","-i":true}`},
		{"Task", "tu_task", `{"description":"do thing","subagent_type":"general-purpose","prompt":"a long prompt"}`},
	}
}

// renderToolViaViewport drives a single-entry ViewportModel through
// renderMessages and returns the assembled bytes. Mirrors what the legacy
// render path would emit for the entry. spinnerFrame is left empty so the
// pending fallback is the production default ("⠋"); the test cases use
// the finished state where the indicator is ✓ regardless.
func renderToolViaViewport(t *testing.T, theme *Theme, entry MessageEntry, width int) string {
	t.Helper()
	vp := NewViewportModel(theme)
	vp.SetSize(width, 10)
	// Disable both cache layers so the render walk produces fresh bytes.
	vp.disableRenderCacheForTest = true
	vp.SetMessages([]MessageEntry{entry})
	return vp.renderMessages()
}

// renderToolViaChatList drives a single-entry ChatList through Render and
// returns the assembled bytes. Constructs the entry through the production
// AppendToolCallWithHeader entry point so the depth/parent heuristic exercises
// the same path used by app.go.
func renderToolViaChatList(t *testing.T, theme *Theme, entry MessageEntry, width int) string {
	t.Helper()
	cl := NewChatList(theme)
	cl.SetSize(width)
	cl.AppendToolCallWithHeader(entry.Content, entry.ToolID, entry.Approved,
		entry.ToolInput, entry.ToolInputFull, entry.HeaderArg, entry.HeaderParams,
		entry.ParentToolID)
	if !entry.Pending {
		cl.MarkToolResult(entry.ToolID, entry.Result, entry.Failed)
	}
	return cl.Render(width)
}

// makeEntry builds the MessageEntry the production wire would yield for a
// tool call: input/fullInput synthesized from raw JSON via FormatToolHeader.
func makeEntry(t *testing.T, tc toolHeaderCase, pending bool, depth int) MessageEntry {
	t.Helper()
	headerArg, headerParams := FormatToolHeader(tc.name, json.RawMessage(tc.raw))
	var prettyBuf any
	_ = json.Unmarshal([]byte(tc.raw), &prettyBuf)
	fullJSON, _ := json.MarshalIndent(prettyBuf, "", "  ")
	entry := MessageEntry{
		Type:          MessageToolCall,
		Content:       tc.name,
		Complete:      true,
		Approved:      true,
		ToolID:        tc.toolID,
		ToolInput:     headerArg,
		ToolInputFull: string(fullJSON),
		HeaderArg:     headerArg,
		HeaderParams:  headerParams,
		Pending:       pending,
		Depth:         depth,
	}
	if !pending {
		entry.Result = "operation succeeded"
		entry.Failed = false
	}
	return entry
}

// TestToolHeaderParity_FinishedDepth0 asserts byte-for-byte parity between
// vp.renderToolCall and cl.ToolCallItem.renderBox for every special-cased
// tool name in the finished state at depth=0.
func TestToolHeaderParity_FinishedDepth0(t *testing.T) {
	theme := NewTheme("colour212")
	for _, tc := range toolHeaderCases() {
		t.Run(tc.name, func(t *testing.T) {
			entry := makeEntry(t, tc, false, 0)
			const width = 100
			vpOut := renderToolViaViewport(t, &theme, entry, width)
			clOut := renderToolViaChatList(t, &theme, entry, width)
			if vpOut != clOut {
				t.Errorf("byte parity mismatch for %s (depth=0, finished)\n vp: %q\n cl: %q",
					tc.name, vpOut, clOut)
			}
		})
	}
}

// TestToolHeaderParity_FinishedNestedDepth1 asserts parity for nested (depth=1)
// rendering of a non-Agent tool child. Both render via renderNested*. Agent
// itself is excluded because vp dispatches Agent unconditionally to the
// container form (renderAgentContainer) regardless of depth — that
// cross-item rendering form lives at the ChatList wiring layer, not in the
// item itself (see items.go ToolCallItem comment). Container/nested-Agent
// parity is acknowledged-divergent for S4.
//
// Both renderers are invoked through their unexported nested helpers so the
// test bypasses the messages-slice dispatch quirks (vp skips entries with
// a non-empty ParentToolID under the "rendered inside parent container"
// assumption; we want to assert the nested-render byte shape itself).
func TestToolHeaderParity_FinishedNestedDepth1(t *testing.T) {
	theme := NewTheme("colour212")
	for _, tc := range toolHeaderCases() {
		t.Run(tc.name, func(t *testing.T) {
			entry := makeEntry(t, tc, false, 1)
			const width = 100

			// vp side: call renderNestedToolCall on a sized vp directly.
			vp := NewViewportModel(&theme)
			vp.SetSize(width, 10)
			var vpSB strings.Builder
			vp.renderNestedToolCall(&vpSB, entry)
			vpOut := vpSB.String()

			// cl side: build a depth-1 ToolCallItem, mark it finished, render.
			ctx := itemRenderCtx{theme: &theme, renderer: NewMarkdownRenderer(width)}
			item := NewToolCallItem(&ctx, ToolCallSpec{
				Name:         entry.Content,
				ToolID:       entry.ToolID,
				Approved:     entry.Approved,
				Input:        entry.ToolInput,
				InputFull:    entry.ToolInputFull,
				HeaderArg:    entry.HeaderArg,
				HeaderParams: entry.HeaderParams,
				Depth:        1,
			})
			item.MarkResult(entry.Result, entry.Failed)
			clOut := item.Render(width)

			if vpOut != clOut {
				t.Errorf("byte parity mismatch for %s (depth=1, finished)\n vp: %q\n cl: %q",
					tc.name, vpOut, clOut)
			}
		})
	}
}
