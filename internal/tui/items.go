package tui

// QUM-671 — TUI rewrite S1 (unwired).
//
// This file introduces the new render model that S3+ will wire into AppModel.
// Nothing here is reachable from production code today — by design. See
// docs/designs/tui-structural-rewrite-plan.md §3 S1 for slice scope and §2.1
// for the architectural target.
//
// Contract callouts that the rest of the arc inherits from here:
//   - Item.Render is width-stable: callers (ChatList) cache by (width, expanded).
//   - Item.Finished signals whether the item will ever mutate again. Caching
//     of unfinished items is forbidden (ChatList enforces).
//   - The Item set deliberately omits status/error/banner: those are S5
//     contract-violators routed to the status bar / overlays (see plan §3 S5
//     and qum-669-viewport-wedge-recovery.md §3).
//   - Spinner animation is per-item-static (resolved Q6, plan §5). S1 picks a
//     fixed pending glyph rather than plumbing a global frame parameter
//     because S4 deletes the global ticker.

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Item is one row in an agent's transcript. Implementations must be
// width-stable: Render(w) called twice with the same w (and the same
// expanded state where applicable) must produce byte-identical output.
type Item interface {
	// Render returns the wrapped, styled multi-line string for the given
	// content width. width <= 0 must return "" (caller hasn't been sized).
	Render(width int) string
	// Finished reports whether this item will ever change again. Finished
	// items are safe to memoize indefinitely. In-flight items (streaming
	// assistant text, pending tool call) return false until terminal.
	Finished() bool
}

// Expandable is implemented by items whose rendering depends on a
// per-item expanded flag (ToolCallItem, ThinkingItem). ChatList's
// global SetToolInputsExpanded fan-out calls SetExpanded on every
// Expandable in every agent's list — preserves the QUM-335 "expand all"
// semantics locked by plan §3 S1.
type Expandable interface {
	SetExpanded(bool)
	IsExpanded() bool
}

// itemRenderCtx is the small bundle of shared state every item needs at
// render time. ChatList owns the canonical instance and hands a pointer to
// each item it constructs so theme/renderer swaps propagate.
type itemRenderCtx struct {
	theme    *Theme
	renderer *MarkdownRenderer
}

// pendingToolGlyph is the static glyph rendered in place of a spinner frame
// while a ToolCallItem is in flight. Plan §3 S4 + §5 Q6 resolved the global
// spinner subsystem out of existence; for S1 we render a fixed glyph.
const pendingToolGlyph = "⠿"

// streamingCursor is the trailing marker drawn after an in-flight
// AssistantTextItem. Matches viewport.StreamingCursor by intent but kept as
// a separate constant so the new and legacy renderers can diverge without
// risk.
const itemsStreamingCursor = "▍"

// nestedToolCallItemIndent mirrors viewport.nestedToolCallIndent so depth>0
// nested rendering keeps QUM-379 visual parity. Kept local to avoid coupling
// the unwired S1 file to legacy internals.
const nestedToolCallItemIndent = 2

// ---------------------------------------------------------------------------
// UserItem
// ---------------------------------------------------------------------------

// UserItem is a single user-typed turn. Always Finished.
type UserItem struct {
	ctx  *itemRenderCtx
	text string
}

// NewUserItem constructs a UserItem with the given content.
func NewUserItem(ctx *itemRenderCtx, text string) *UserItem {
	return &UserItem{ctx: ctx, text: text}
}

// Render draws the chevron-prefixed user prompt block (QUM-664 styling).
func (i *UserItem) Render(width int) string {
	if width <= 0 {
		return ""
	}
	return renderUserPromptBlock(i.ctx.theme, i.text)
}

// Finished always returns true: user turns are immutable on creation.
func (i *UserItem) Finished() bool { return true }

// ---------------------------------------------------------------------------
// AssistantTextItem
// ---------------------------------------------------------------------------

// AssistantTextItem holds the streaming assistant text for one turn. It
// mutates in place as chunks arrive (AppendChunk) and freezes when the turn
// finalizes (Finalize). Finished() returns false while streaming so ChatList
// bypasses the cache for the in-flight item — every other item still hits.
type AssistantTextItem struct {
	ctx      *itemRenderCtx
	text     string
	finished bool
}

// NewAssistantTextItem constructs an in-flight assistant item seeded with
// the first chunk of streamed text.
func NewAssistantTextItem(ctx *itemRenderCtx, text string) *AssistantTextItem {
	return &AssistantTextItem{ctx: ctx, text: text}
}

// AppendChunk appends another chunk to the streaming text buffer. No-op once
// the item is finished.
func (i *AssistantTextItem) AppendChunk(text string) {
	if i.finished {
		return
	}
	i.text += text
}

// Finalize freezes the item, allowing ChatList to cache its render.
func (i *AssistantTextItem) Finalize() { i.finished = true }

// Text returns the current accumulated text (used by tests; tracking copy
// kept simple — no defensive clone since strings are immutable in Go).
func (i *AssistantTextItem) Text() string { return i.text }

// Render glamour-renders the markdown and appends a streaming cursor when
// in flight. Width parameter selects the MarkdownRenderer's wrap width.
func (i *AssistantTextItem) Render(width int) string {
	if width <= 0 {
		return ""
	}
	i.ctx.renderer.SetWidth(width)
	out := i.ctx.renderer.Render(i.text)
	if !i.finished {
		out += itemsStreamingCursor
	}
	return out
}

// Finished returns true only after Finalize has been called.
func (i *AssistantTextItem) Finished() bool { return i.finished }

// ---------------------------------------------------------------------------
// ThinkingItem
// ---------------------------------------------------------------------------

// ThinkingItem renders a model-emitted thinking block (plan §3 S7 promotes
// this to a first-class viewport row). Collapsed by default; Ctrl+O fan-out
// (via Expandable) flips it open. Always Finished on creation — thinking
// blocks arrive whole, unlike assistant text which streams.
type ThinkingItem struct {
	ctx      *itemRenderCtx
	text     string
	expanded bool
}

// NewThinkingItem constructs a finished ThinkingItem from the model's
// thinking content block text.
func NewThinkingItem(ctx *itemRenderCtx, text string) *ThinkingItem {
	return &ThinkingItem{ctx: ctx, text: text}
}

// Render produces the collapsed teaser when not expanded, or the full text
// (wrapped) when expanded.
func (i *ThinkingItem) Render(width int) string {
	if width <= 0 {
		return ""
	}
	style := i.ctx.theme.SystemText
	if !i.expanded {
		preview := firstNonBlankLine(i.text)
		budget := width - 4 // "✻ " + " · ^O" trailing hint reserve
		if budget > 0 {
			preview = ansi.Truncate(preview, budget, "…")
		}
		return style.Render("✻ thinking ") + style.Render(preview) +
			style.Render(" · ^O to expand")
	}
	body := formatSystemMessage(i.text, width)
	return style.Render("✻ thinking") + "\n" + style.Render(body)
}

// Finished is always true: thinking blocks arrive whole.
func (i *ThinkingItem) Finished() bool { return true }

// SetExpanded flips the expanded flag.
func (i *ThinkingItem) SetExpanded(v bool) { i.expanded = v }

// IsExpanded reports the current expanded state.
func (i *ThinkingItem) IsExpanded() bool { return i.expanded }

// ---------------------------------------------------------------------------
// ToolCallItem
// ---------------------------------------------------------------------------

// ToolCallItem renders a tool invocation row. Depth==0 renders the
// box-drawing block (┌ … └); Depth>0 renders the compact nested form for
// items inside Agent containers (QUM-379 visual parity).
//
// Agent-container children rendering (the parent-renders-its-pending-
// children form from viewport.renderAgentContainer) is intentionally NOT
// implemented at the item layer: it requires cross-item visibility and will
// live at the ChatList layer when S3 wiring brings the data flow. For S1,
// "Agent" tool calls render the same as any other tool — adequate for the
// unwired surface.
type ToolCallItem struct {
	ctx          *itemRenderCtx
	name         string
	toolID       string
	approved     bool
	input        string
	inputFull    string
	headerArg    string
	headerParams []KVPair
	depth        int
	parentToolID string
	pending      bool
	failed       bool
	result       string
	expanded     bool
}

// ToolCallSpec captures the constructor inputs for a ToolCallItem so the
// argument list stays manageable.
type ToolCallSpec struct {
	Name         string
	ToolID       string
	Approved     bool
	Input        string
	InputFull    string
	HeaderArg    string
	HeaderParams []KVPair
	Depth        int
	ParentToolID string
}

// NewToolCallItem constructs a pending ToolCallItem.
func NewToolCallItem(ctx *itemRenderCtx, spec ToolCallSpec) *ToolCallItem {
	return &ToolCallItem{
		ctx:          ctx,
		name:         spec.Name,
		toolID:       spec.ToolID,
		approved:     spec.Approved,
		input:        spec.Input,
		inputFull:    spec.InputFull,
		headerArg:    spec.HeaderArg,
		headerParams: spec.HeaderParams,
		depth:        spec.Depth,
		parentToolID: spec.ParentToolID,
		pending:      true,
	}
}

// ToolID returns the protocol tool_use_id (used by ChatList.MarkToolResult
// to locate the matching item when a result event arrives).
func (i *ToolCallItem) ToolID() string { return i.toolID }

// MarkResult flips the item to its finished state with the given result.
func (i *ToolCallItem) MarkResult(content string, isError bool) {
	i.pending = false
	i.failed = isError
	i.result = content
}

// Finished is true once the result has landed.
func (i *ToolCallItem) Finished() bool { return !i.pending }

// SetExpanded toggles the per-item expand flag (mirrors global Ctrl+O).
func (i *ToolCallItem) SetExpanded(v bool) { i.expanded = v }

// IsExpanded reports the current expand state.
func (i *ToolCallItem) IsExpanded() bool { return i.expanded }

// Render dispatches to the depth-appropriate form.
func (i *ToolCallItem) Render(width int) string {
	if width <= 0 {
		return ""
	}
	if i.depth > 0 {
		return i.renderNested(width)
	}
	return i.renderBox(width)
}

func (i *ToolCallItem) indicator() string {
	return toolIndicator(i.ctx.theme, i.pending, i.failed, "", pendingToolGlyph)
}

func (i *ToolCallItem) renderBox(width int) string {
	var sb strings.Builder
	displayName := FormatToolDisplayName(i.name)
	mainArg := i.headerArg
	if mainArg == "" && i.headerParams == nil {
		mainArg = i.input
	}
	paramsStr := RenderKVPairs(i.headerParams)
	// Width-budget per viewport.renderToolCall.
	const fixed = 4 // "┌ " + indicator + " "
	nameCells := ansi.StringWidth(displayName)
	budget := width - fixed - nameCells
	if budget < 1 {
		budget = 1
	}
	if mainArg != "" {
		budget--
	}
	if paramsStr != "" {
		remaining := budget - ansi.StringWidth(paramsStr) - 1
		if remaining < MinMainArgCells {
			paramsStr = ""
		} else {
			mainArg = ansi.Truncate(mainArg, remaining, "…")
		}
	}
	if paramsStr == "" && mainArg != "" {
		mainArg = ansi.Truncate(mainArg, budget, "…")
	}
	if nameCells > width-3 {
		displayName = ansi.Truncate(displayName, width-3, "…")
	}
	sb.WriteString(i.ctx.theme.AccentText.Render("┌ "))
	sb.WriteString(i.indicator())
	sb.WriteString(i.ctx.theme.AccentText.Bold(true).Render(" " + displayName))
	if mainArg != "" {
		sb.WriteString(i.ctx.theme.NormalText.Render(" " + mainArg))
	}
	if paramsStr != "" {
		sb.WriteString(i.ctx.theme.NormalText.Render(" " + paramsStr))
	}
	if i.expanded {
		body := i.inputFull
		if body == "" {
			body = i.input
		}
		sb.WriteString(renderToolInputBody(i.ctx.theme, body, width))
	}
	if !i.pending && i.result != "" {
		sb.WriteString(renderResultPreviewLines(i.ctx.theme, i.result, i.failed, i.expanded, width))
	}
	sb.WriteString("\n")
	sb.WriteString(i.ctx.theme.AccentText.Render("└"))
	return sb.String()
}

func (i *ToolCallItem) renderNested(width int) string {
	var sb strings.Builder
	indent := strings.Repeat(" ", i.depth*nestedToolCallItemIndent)
	const fixedCells = 4 // "│ " + indicator + " "
	budget := width - fixedCells - len(indent)
	if budget < 1 {
		budget = 1
	}
	body := i.name
	if i.input != "" {
		body += "  " + i.input
	}
	body = ansi.Truncate(body, budget, "…")
	sb.WriteString(i.ctx.theme.AccentText.Render("│ " + indent))
	sb.WriteString(i.indicator())
	sb.WriteString(i.ctx.theme.NormalText.Render(" " + body))
	return sb.String()
}

// ---------------------------------------------------------------------------
// SystemNotificationItem
// ---------------------------------------------------------------------------

// SystemNotificationItem renders one peeled `<system-notification>`
// envelope (QUM-557/562). Mirrors viewport.notificationGlyphAndStyle
// branching. Always Finished on creation — envelopes arrive whole.
type SystemNotificationItem struct {
	ctx              *itemRenderCtx
	content          string
	notificationType string
	interrupt        bool
}

// NewSystemNotificationItem constructs a finished system-notification item.
func NewSystemNotificationItem(ctx *itemRenderCtx, content, notifType string, interrupt bool) *SystemNotificationItem {
	return &SystemNotificationItem{
		ctx:              ctx,
		content:          content,
		notificationType: notifType,
		interrupt:        interrupt,
	}
}

// Render produces the accent-prefixed envelope row.
func (i *SystemNotificationItem) Render(width int) string {
	if width <= 0 {
		return ""
	}
	// Use a stand-in MessageEntry so we can reuse the shared glyph/style
	// selector verbatim without duplicating its lookup-table.
	stub := MessageEntry{NotificationType: i.notificationType, Interrupt: i.interrupt}
	glyph, style := notificationGlyphAndStyle(i.ctx.theme, stub)
	formatted := formatSystemMessage(i.content, width)
	return style.Render("│ " + glyph + " " + formatted)
}

// Finished always returns true.
func (i *SystemNotificationItem) Finished() bool { return true }

// ---------------------------------------------------------------------------
// AutoTriggerItem
// ---------------------------------------------------------------------------

// AutoTriggerItem renders the synthetic "why this turn happened" header for
// autonomous (harness-initiated) turns (QUM-634). Always Finished on
// creation.
type AutoTriggerItem struct {
	ctx     *itemRenderCtx
	summary string
}

// NewAutoTriggerItem constructs a finished auto-trigger header.
func NewAutoTriggerItem(ctx *itemRenderCtx, summary string) *AutoTriggerItem {
	return &AutoTriggerItem{ctx: ctx, summary: summary}
}

// Render produces the single-line marker.
func (i *AutoTriggerItem) Render(width int) string {
	if width <= 0 {
		return ""
	}
	return i.ctx.theme.SystemText.Render("↻ auto-continued — " + i.summary)
}

// Finished always returns true.
func (i *AutoTriggerItem) Finished() bool { return true }

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// firstNonBlankLine returns the first non-empty trimmed line in s, or "" if
// none. Used by ThinkingItem's collapsed teaser.
func firstNonBlankLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}
