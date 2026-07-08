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
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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
	// RawMarkdown returns the copy-for-selection payload for this item.
	// Selection-mode yank concatenates per-item RawMarkdown() outputs to
	// build the user-visible payload. Form is item-type-specific:
	//   - UserItem:               blockquoted (> prefix) lines
	//   - AssistantTextItem:      verbatim markdown source
	//   - ToolCallItem:           "<!-- tool: name (input) -->" HTML comment
	//   - ThinkingItem:           empty (transient count marker, no payload)
	//   - SystemNotificationItem: envelope body verbatim
	//   - AutoTriggerItem:        synthetic "auto-continued — …" marker
	// QUM-676: introduced when selection.go migrated off the legacy
	// MessageEntry-based AssembleRawMarkdown.
	RawMarkdown() string
}

// Expandable is implemented by items whose rendering depends on a
// per-item expanded flag (currently ToolCallItem only — QUM-677 S7 pivot
// removed ThinkingItem from this set; the marker has no body to expand).
// ChatList's global SetToolInputsExpanded fan-out calls SetExpanded on
// every Expandable in every agent's list — preserves the QUM-335
// "expand all" semantics locked by plan §3 S1.
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

// toolSpinnerFrames is the per-item braille pulse used to animate in-flight
// ToolCallItem rows (QUM-732 revisit of QUM-674 Q6). The frames are advanced
// by toolTickMsg deliveries scheduled via tea.Tick from each pending item's
// Update method — there is intentionally NO global ticker (the QUM-336
// subsystem was removed by QUM-674 S4 and is not reintroduced).
var toolSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// toolSpinnerInterval is the cadence at which each pending ToolCallItem
// advances its frame. ~10 fps lands inside the AC's 80-120ms window.
const toolSpinnerInterval = 100 * time.Millisecond

// toolTickMsg is delivered by tea.Tick to a single pending ToolCallItem.
// ToolID scopes routing; ChatList.Update forwards only to the matching item.
// This msg is LOCAL to the TUI — it never flows through the runtime EventBus
// (internal/tuiruntime/tuiadapter.go only translates RuntimeEvents), so it
// cannot trip QUM-669 gap-detect / EventDropDetectedMsg.
type toolTickMsg struct{ ToolID string }

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
//
// QUM-832: a UserItem held in the ChatList pending zone (queued, not yet echoed)
// carries pending=true and renders DIM; ZoneSettle flips it to false so it
// brightens to normal styling when it settles into the committed transcript.
// Committed bubbles (AppendUser) default to pending=false (bright).
type UserItem struct {
	ctx     *itemRenderCtx
	text    string
	pending bool
	// attachments, when non-empty, renders one 📎 chip line per file ABOVE the
	// prompt text (QUM-860). Presentation only — the image bytes travel in the
	// wire MessageParam.Blocks, never here.
	attachments []AttachmentChip
}

// NewUserItem constructs a UserItem with the given content. Defaults to the
// committed (bright) styling; the pending-zone path flips it via SetPending.
func NewUserItem(ctx *itemRenderCtx, text string) *UserItem {
	return &UserItem{ctx: ctx, text: text}
}

// NewUserItemWithAttachments constructs a UserItem that renders attachment chip
// lines above the prompt text (QUM-860). Defaults to committed (bright) styling.
func NewUserItemWithAttachments(ctx *itemRenderCtx, text string, chips []AttachmentChip) *UserItem {
	return &UserItem{ctx: ctx, text: text, attachments: chips}
}

// Text returns the user-typed text body. Used by tests to assert content
// without re-parsing the rendered output.
func (i *UserItem) Text() string { return i.text }

// SetPending toggles the dim (pending) vs bright (committed) styling. Callers
// that flip a cached item must invalidate the owning envelope's render cache —
// pending is not part of the (width, expanded) cache key. QUM-832.
func (i *UserItem) SetPending(pending bool) { i.pending = pending }

// Render draws the chevron-prefixed user prompt block (QUM-664 styling), dimmed
// when pending (QUM-832). When the turn carries attachments (QUM-860), one 📎
// chip line per file is drawn above the prompt body, sharing the same
// pending/bright style so it dims and brightens with the bubble.
func (i *UserItem) Render(width int) string {
	if width <= 0 {
		return ""
	}
	var parts []string
	if len(i.attachments) > 0 {
		parts = append(parts, renderAttachmentChips(i.ctx.theme, i.attachments, width, i.pending))
	}
	// Suppress the prompt block entirely when the text is empty and there is at
	// least one chip, so an attach-only turn doesn't render a dangling chevron.
	if i.text != "" || len(i.attachments) == 0 {
		parts = append(parts, renderUserPromptBlock(i.ctx.theme, i.text, width, i.pending))
	}
	return strings.Join(parts, "\n")
}

// Finished always returns true: user turns are immutable on creation.
func (i *UserItem) Finished() bool { return true }

// RawMarkdown returns the blockquoted form of the user message body, matching
// the legacy AssembleRawMarkdown user-branch (`> line1\n> line2…`).
func (i *UserItem) RawMarkdown() string {
	lines := strings.Split(i.text, "\n")
	for j, ln := range lines {
		lines[j] = "> " + ln
	}
	return strings.Join(lines, "\n")
}

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
//
// QUM-691: enforce the "per-item content has no leading or trailing blank"
// contract — the outer ChatList loop owns inter-item spacing. Glamour
// prepends a leading blank line to its output; strip it so a first
// assistant item (or any same-type predecessor) does not produce an extra
// blank line. The MarkdownRenderer already trims trailing newlines.
func (i *AssistantTextItem) Render(width int) string {
	if width <= 0 {
		return ""
	}
	i.ctx.renderer.SetWidth(width)
	out := strings.TrimLeft(i.ctx.renderer.Render(i.text), "\n")
	if !i.finished {
		out += itemsStreamingCursor
	}
	return out
}

// Finished returns true only after Finalize has been called.
func (i *AssistantTextItem) Finished() bool { return i.finished }

// RawMarkdown returns the verbatim markdown source so yanked content can be
// re-pasted as markdown (fenced code blocks survive untouched).
func (i *AssistantTextItem) RawMarkdown() string { return i.text }

// ---------------------------------------------------------------------------
// ThinkingItem
// ---------------------------------------------------------------------------

// ThinkingItem is a transient "model is thinking" marker. QUM-677 S7 pivot:
// Claude/Opus redacts thinking-block bodies server-side (only the wire
// `signature` field is populated), so the marker carries a count of
// consecutive thinking blocks in the current turn instead of any text.
// `ChatList.AppendThinking` coalesces consecutive arrivals into one trailing
// marker; the marker is dropped automatically when any non-thinking append
// (assistant text, tool call, etc.) lands. Not Expandable — there is no
// body to expand.
type ThinkingItem struct {
	ctx   *itemRenderCtx
	count int
}

// NewThinkingItem constructs a fresh marker with count=1.
func NewThinkingItem(ctx *itemRenderCtx) *ThinkingItem {
	return &ThinkingItem{ctx: ctx, count: 1}
}

// Bump increments the block counter. The owning envelope's cache must be
// invalidated by the caller (ChatList.AppendThinking handles that).
func (i *ThinkingItem) Bump() { i.count++ }

// Count returns the current run-length of consecutive thinking blocks.
func (i *ThinkingItem) Count() int { return i.count }

// Render produces the dim/italic marker line, e.g. `✻ thinking… (3 blocks)`.
func (i *ThinkingItem) Render(width int) string {
	if width <= 0 {
		return ""
	}
	style := i.ctx.theme.ThinkingText
	unit := "blocks"
	if i.count == 1 {
		unit = "block"
	}
	return style.Render(fmt.Sprintf("✻ thinking… (%d %s)", i.count, unit))
}

// Finished is always true: each marker update is self-contained and safe to
// cache between counter bumps (every bump invalidates the envelope).
func (i *ThinkingItem) Finished() bool { return true }

// RawMarkdown returns "" — the marker has no yankable payload (the
// underlying thinking-block bodies are redacted at the wire layer).
func (i *ThinkingItem) RawMarkdown() string { return "" }

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
	frame        int  // current spinner frame index; advances on toolTickMsg
	ticking      bool // an in-flight tea.Tick has been armed for this item
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

// RawMarkdown emits the legacy HTML-comment form so selection yank stays
// source-compatible with pre-S6 transcripts: `<!-- tool: name (input) -->`
// when an input summary is present, `<!-- tool: name -->` otherwise.
func (i *ToolCallItem) RawMarkdown() string {
	if i.input != "" {
		return fmt.Sprintf("<!-- tool: %s (%s) -->", i.name, i.input)
	}
	return fmt.Sprintf("<!-- tool: %s -->", i.name)
}

// Name returns the tool name.
func (i *ToolCallItem) Name() string { return i.name }

// Pending reports whether the tool call is still in flight.
func (i *ToolCallItem) Pending() bool { return i.pending }

// Failed reports whether the tool call result indicated an error.
func (i *ToolCallItem) Failed() bool { return i.failed }

// Result returns the result body recorded by MarkResult.
func (i *ToolCallItem) Result() string { return i.result }

// Input returns the (possibly truncated) input summary.
func (i *ToolCallItem) Input() string { return i.input }

// InputFull returns the full input payload retained for the expanded body.
func (i *ToolCallItem) InputFull() string { return i.inputFull }

// Depth returns the indentation depth (0 = top-level, 1 = nested inside an
// Agent container, etc.).
func (i *ToolCallItem) Depth() int { return i.depth }

// ParentToolID returns the parent Agent tool_use_id when Depth > 0.
func (i *ToolCallItem) ParentToolID() string { return i.parentToolID }

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
	frame := ""
	if i.pending {
		frame = toolSpinnerFrames[i.frame%len(toolSpinnerFrames)]
	}
	return toolIndicator(i.ctx.theme, i.pending, i.failed, frame, pendingToolGlyph)
}

// StartTickCmd returns a tea.Cmd that fires the next toolTickMsg for this
// item after toolSpinnerInterval. Returns nil when the item is not pending
// or a tick is already in flight (idempotent — safe to call from
// ChatList.PendingToolTickCmds on every append batch).
func (i *ToolCallItem) StartTickCmd() tea.Cmd {
	if !i.pending || i.ticking {
		return nil
	}
	i.ticking = true
	id := i.toolID
	return tea.Tick(toolSpinnerInterval, func(time.Time) tea.Msg {
		return toolTickMsg{ToolID: id}
	})
}

// ResetTicking clears the in-flight tick flag without modifying frame state.
// Called on observed-agent switches so the newly-observed pane's pending
// items can be re-armed by PendingToolTickCmds — any tick chain previously
// armed for this item has either already terminated (delivered to the now-
// previous observed pane and dead-ended) or will dead-end on next delivery,
// so resetting the flag is safe and necessary to avoid a frozen spinner
// after a switch-away-then-back (QUM-732).
func (i *ToolCallItem) ResetTicking() { i.ticking = false }

// Update advances the spinner frame on a toolTickMsg destined for this item
// and returns the next-tick cmd while still pending. Returns nil when the
// item has resolved — the tick cmd chain naturally terminates, so no
// goroutine / cmd leak. Returns nil for mis-routed toolTickMsg (different
// ToolID), letting ChatList.Update treat them as no-ops.
func (i *ToolCallItem) Update(msg tea.Msg) tea.Cmd {
	tm, ok := msg.(toolTickMsg)
	if !ok || tm.ToolID != i.toolID {
		return nil
	}
	if !i.pending {
		i.ticking = false
		return nil
	}
	i.frame = (i.frame + 1) % len(toolSpinnerFrames)
	return tea.Tick(toolSpinnerInterval, func(time.Time) tea.Msg {
		return toolTickMsg{ToolID: i.toolID}
	})
}

// renderBox renders the top-level (depth==0) tool-call row as a clean inline
// header — `<glyph> <ToolName>(<command preview>)` — with no `┌ │ └` box
// chrome (QUM-796 #1/#2). Output (expanded input + result preview) indents
// two spaces under the header. Glyph + name keep the accent color; the
// command preview renders in NormalText (the historical light grey).
func (i *ToolCallItem) renderBox(width int) string {
	var sb strings.Builder
	displayName := FormatToolDisplayName(i.name)
	mainArg := i.headerArg
	if mainArg == "" && i.headerParams == nil {
		mainArg = i.input
	}
	// Defensive: neutralize control chars (newlines, tabs, CR, raw ESC) so
	// the header stays a single, un-corrupted styled row.
	mainArg = sanitizeHeaderArg(mainArg)
	// Width-budget: indicator (1 cell) + " " before the name, plus the "()"
	// wrapping the preview when present.
	const fixed = 2 // indicator + " "
	nameCells := ansi.StringWidth(displayName)
	if nameCells > width-2 {
		displayName = ansi.Truncate(displayName, width-2, "…")
		nameCells = ansi.StringWidth(displayName)
	}
	if mainArg != "" {
		budget := width - fixed - nameCells - 2 // 2 for "(" and ")"
		if budget < 1 {
			budget = 1
		}
		mainArg = ansi.Truncate(mainArg, budget, "…")
	}
	sb.WriteString(i.indicator())
	sb.WriteString(i.ctx.theme.AccentText.Bold(true).Render(" " + displayName))
	if mainArg != "" {
		sb.WriteString(i.ctx.theme.NormalText.Render("(" + mainArg + ")"))
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

// Content returns the peeled envelope body.
func (i *SystemNotificationItem) Content() string { return i.content }

// Interrupt reports whether the envelope was marked as an interrupt.
func (i *SystemNotificationItem) Interrupt() bool { return i.interrupt }

// NotificationType returns the envelope's type attribute (kind).
func (i *SystemNotificationItem) NotificationType() string { return i.notificationType }

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

// RawMarkdown returns the envelope body verbatim so peer-agent message
// contents surface in yanked output.
func (i *SystemNotificationItem) RawMarkdown() string { return i.content }

// ---------------------------------------------------------------------------
// AutoTriggerItem
// ---------------------------------------------------------------------------

// AutoTriggerItem renders the synthetic "why this turn happened" header for
// autonomous (harness-initiated) turns (QUM-634). Always Finished on
// creation.
type AutoTriggerItem struct {
	ctx *itemRenderCtx
}

// NewAutoTriggerItem constructs a finished auto-trigger header.
func NewAutoTriggerItem(ctx *itemRenderCtx) *AutoTriggerItem {
	return &AutoTriggerItem{ctx: ctx}
}

// Render produces the single styled indicator line. QUM-855 suppressed the
// completed-sidechain result body that used to be stuffed into this marker
// (dumping it verbatim bypassed the glamour pass and surfaced raw
// `##`/`**`/backtick walls in flat SystemText purple). QUM-857 removed the
// body-carrying state entirely — the item never held anything but this fixed
// cue (the sidechain result reaches the model context via the Agent tool), so
// only the styled marker is rendered.
func (i *AutoTriggerItem) Render(width int) string {
	if width <= 0 {
		return ""
	}
	return i.ctx.theme.SystemText.Render("↻ auto-continued")
}

// Finished always returns true.
func (i *AutoTriggerItem) Finished() bool { return true }

// RawMarkdown surfaces the synthetic auto-trigger marker so yanked output
// reflects the "why this turn happened" cue the user saw on screen. The
// suppressed summary body is intentionally not yankable (QUM-855).
func (i *AutoTriggerItem) RawMarkdown() string {
	return "↻ auto-continued"
}

// ---------------------------------------------------------------------------
// CompactBannerItem
// ---------------------------------------------------------------------------

// CompactBannerItem renders the first-party context-compaction banner shown
// when the backend emits a compact_boundary frame (QUM-865). The text is
// pre-formatted by the AppModel reducer (e.g. "🗜 context compacted · 236k→9k
// tok · manual"). Always Finished on creation — the boundary is a settled fact.
type CompactBannerItem struct {
	ctx  *itemRenderCtx
	text string
}

// NewCompactBannerItem constructs a finished compaction banner item.
func NewCompactBannerItem(ctx *itemRenderCtx, text string) *CompactBannerItem {
	return &CompactBannerItem{ctx: ctx, text: text}
}

// Render produces the single styled banner line.
func (i *CompactBannerItem) Render(width int) string {
	if width <= 0 {
		return ""
	}
	return i.ctx.theme.SystemText.Render(i.text)
}

// Finished always returns true.
func (i *CompactBannerItem) Finished() bool { return true }

// RawMarkdown surfaces the banner text so yanked output reflects what the user
// saw on screen.
func (i *CompactBannerItem) RawMarkdown() string { return i.text }

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------
