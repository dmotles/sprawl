package tui

// QUM-671 — TUI rewrite S1 (unwired).
//
// ChatList is the future replacement for the ViewportModel's `messages` slice
// + renderMessages walk. S2+ wires it in via a dual-append shim alongside the
// existing viewport; S6 deletes the old surface. See
// docs/designs/tui-structural-rewrite-plan.md §3.
//
// Contract notes the next slice owner inherits:
//   - No AppendStatus/AppendError/AppendBanner here. Those are S5
//     "contract violators" routed elsewhere (status bar / overlays). Their
//     omission is the in-code enforcement plan §3 S5 + §4.4 promise.
//   - A future Reset([]Item) (or Reset([]MessageEntry) per
//     qum-669-viewport-wedge-recovery.md §3) belongs in S3 alongside its
//     wiring use-site. Adding it here without a consumer invites bit-rot.
//   - Width-0 guard (plan §5 resolved Q7): Render no-ops until SetSize is
//     called with width > 0. SetSize is the only mutator of the width field,
//     so a zero sentinel is sufficient.

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// cachedRender captures one envelope's last rendered output for a specific
// (width, expanded) pairing. expanded is meaningless for non-Expandable items
// and is always false in their cache key.
type cachedRender struct {
	width    int
	expanded bool
	out      string
}

// itemEnvelope wraps an Item with its per-(width, expanded) render cache.
// The cache is nil until first Render at known width, and is bypassed
// entirely while the item is not Finished.
type itemEnvelope struct {
	item  Item
	cache *cachedRender
}

// ChatList is the per-agent ordered list of Items. Owns the renderer +
// theme passed to each Item via the shared itemRenderCtx.
type ChatList struct {
	ctx           itemRenderCtx
	items         []*itemEnvelope
	width         int
	toolsExpanded bool

	// pendingTools is the count of in-flight ToolCallItems (Finished()==false).
	// Drives Idle() so the S3 View() switch can fall back to vp.View() while
	// any tool is mid-flight. QUM-673.
	pendingTools int
	// streamingAssistant tracks whether the trailing item is an in-flight
	// AssistantTextItem (set on first chunk, cleared on Finalize). Drives
	// Idle() so the S3 View() switch falls back to vp.View() mid-stream.
	streamingAssistant bool

	// activeAgents / lastActiveAgent mirror ViewportModel's depth/parent
	// inference for live-path tool calls without an explicit parent_tool_use_id
	// (QUM-386 heuristic fallback). Replicated here so AppendToolCallWithHeader
	// produces identical Depth + ParentToolID values to the legacy path.
	activeAgents    map[string]bool
	lastActiveAgent string
}

// NewChatList constructs an empty ChatList bound to the given theme.
// Width starts at 0; Render is a no-op until SetSize is called.
func NewChatList(theme *Theme) *ChatList {
	return &ChatList{
		ctx: itemRenderCtx{
			theme:    theme,
			renderer: NewMarkdownRenderer(80),
		},
	}
}

// Len returns the number of items in the list.
func (c *ChatList) Len() int { return len(c.items) }

// Items returns the unwrapped Item slice in append-order. The returned slice
// is a fresh copy so callers (selection-mode yank, debug inspection) cannot
// mutate ChatList's internal envelope ordering. Render envelopes are NOT
// exposed — the contract is per-Item access only. QUM-676.
func (c *ChatList) Items() []Item {
	out := make([]Item, len(c.items))
	for i, env := range c.items {
		out[i] = env.item
	}
	return out
}

// Width returns the most recently applied content width (0 if SetSize has
// not been called yet).
func (c *ChatList) Width() int { return c.width }

// ToolInputsExpanded reports the current global expand-tool-inputs state.
// (Mirrors the QUM-335 viewport flag; the wiring slice will lift the global
// across all per-agent ChatLists.)
func (c *ChatList) ToolInputsExpanded() bool { return c.toolsExpanded }

// SetSize updates the content width. Width-0 (anything <= 0) is treated as
// "not sized yet"; subsequent Render calls will no-op until a positive width
// arrives. On a real width change we do NOT eagerly invalidate caches: each
// envelope's cache miss-detects on (width, expanded) mismatch and rebuilds
// lazily on demand.
func (c *ChatList) SetSize(width int) {
	if width <= 0 {
		c.width = 0
		return
	}
	c.width = width
}

// SetToolInputsExpanded propagates a new global expand-tool-inputs state to
// every Expandable item in the list. On change, the matching envelopes'
// caches are invalidated so the next Render produces fresh output.
// (Plan §3 S1 + resolved Q6: "expand all" semantics survive the rewrite.)
func (c *ChatList) SetToolInputsExpanded(expanded bool) {
	if c.toolsExpanded == expanded {
		return
	}
	c.toolsExpanded = expanded
	for _, env := range c.items {
		if ex, ok := env.item.(Expandable); ok {
			ex.SetExpanded(expanded)
			env.cache = nil
		}
	}
}

// AppendUser appends a new UserItem.
func (c *ChatList) AppendUser(text string) {
	c.dropTrailingThinkingMarker()
	c.items = append(c.items, &itemEnvelope{item: NewUserItem(&c.ctx, text)})
}

// AppendAssistantChunk appends a streaming chunk to the trailing
// AssistantTextItem if it exists and is in flight; otherwise it starts a new
// in-flight assistant item. Mirrors viewport.AppendAssistantChunk's mutate-
// or-append semantics so the new model behaves identically to the legacy.
func (c *ChatList) AppendAssistantChunk(text string) {
	if n := len(c.items); n > 0 {
		if a, ok := c.items[n-1].item.(*AssistantTextItem); ok && !a.Finished() {
			a.AppendChunk(text)
			// Streaming item bypasses cache via Finished()==false; nothing to
			// invalidate, but null out defensively to be explicit.
			c.items[n-1].cache = nil
			c.streamingAssistant = true
			return
		}
	}
	c.dropTrailingThinkingMarker()
	c.items = append(c.items, &itemEnvelope{item: NewAssistantTextItem(&c.ctx, text)})
	c.streamingAssistant = true
}

// FinalizeAssistantMessage marks the trailing AssistantTextItem (if any) as
// finished, allowing its render to be cached on next Render.
func (c *ChatList) FinalizeAssistantMessage() {
	if n := len(c.items); n > 0 {
		if a, ok := c.items[n-1].item.(*AssistantTextItem); ok && !a.Finished() {
			a.Finalize()
			c.items[n-1].cache = nil
		}
	}
	c.streamingAssistant = false
}

// AppendThinking coalesces consecutive thinking arrivals into a single
// trailing ThinkingItem marker. QUM-677 S7: thinking-block bodies are
// redacted server-side, so the marker carries a count instead of text. The
// marker is dropped on the next non-thinking append (see
// dropTrailingThinkingMarker) — its purpose is purely the transient "model
// is currently thinking" indicator.
func (c *ChatList) AppendThinking() {
	if n := len(c.items); n > 0 {
		if t, ok := c.items[n-1].item.(*ThinkingItem); ok {
			t.Bump()
			c.items[n-1].cache = nil
			return
		}
	}
	c.items = append(c.items, &itemEnvelope{item: NewThinkingItem(&c.ctx)})
}

// dropTrailingThinkingMarker removes a trailing ThinkingItem if one is
// present. Called by every non-thinking Append* verb — once real content
// arrives, the transient marker has served its purpose.
func (c *ChatList) dropTrailingThinkingMarker() {
	if n := len(c.items); n > 0 {
		if _, ok := c.items[n-1].item.(*ThinkingItem); ok {
			c.items = c.items[:n-1]
		}
	}
}

// AppendToolCall appends a pending ToolCallItem. The item inherits the
// current global expand state so it renders with the right body shape on
// its first paint. Bumps the pendingTools counter so Idle() reports false
// until MarkToolResult lands.
func (c *ChatList) AppendToolCall(spec ToolCallSpec) {
	c.dropTrailingThinkingMarker()
	item := NewToolCallItem(&c.ctx, spec)
	item.SetExpanded(c.toolsExpanded)
	c.items = append(c.items, &itemEnvelope{item: item})
	c.pendingTools++
	if spec.Name == "Agent" && spec.ToolID != "" {
		if c.activeAgents == nil {
			c.activeAgents = make(map[string]bool)
		}
		c.activeAgents[spec.ToolID] = true
		c.lastActiveAgent = spec.ToolID
	}
}

// AppendToolCallWithHeader matches viewport.AppendToolCallWithHeader's
// signature so AgentBuffer can fan out without per-call translation. Replicates
// the QUM-386 depth/parent inference: an explicit parent_tool_use_id is
// authoritative; otherwise a non-Agent call inside any in-flight Agent gets
// Depth=1 attributed to the most recent Agent.
func (c *ChatList) AppendToolCallWithHeader(name, toolID string, approved bool,
	input, fullInput, headerArg string, headerParams []KVPair,
	parentToolUseID string,
) {
	depth := 0
	parentID := ""
	switch {
	case parentToolUseID != "":
		parentID = parentToolUseID
		depth = 1
	case len(c.activeAgents) > 0 && name != "Agent":
		depth = 1
		parentID = c.lastActiveAgent
	}
	c.AppendToolCall(ToolCallSpec{
		Name:         name,
		ToolID:       toolID,
		Approved:     approved,
		Input:        input,
		InputFull:    fullInput,
		HeaderArg:    headerArg,
		HeaderParams: headerParams,
		Depth:        depth,
		ParentToolID: parentID,
	})
}

// MarkToolResult walks newest→oldest for a ToolCallItem with the matching
// ToolID and flips it to finished. Returns true if a match was found.
// Mirrors viewport.MarkToolResult semantics so the wiring slice can swap the
// destination without behavior change. Decrements pendingTools on match.
func (c *ChatList) MarkToolResult(toolID, content string, isError bool) bool {
	if toolID == "" {
		return false
	}
	for n := len(c.items) - 1; n >= 0; n-- {
		t, ok := c.items[n].item.(*ToolCallItem)
		if !ok {
			continue
		}
		if t.ToolID() != toolID {
			continue
		}
		wasPending := !t.Finished()
		t.MarkResult(content, isError)
		c.items[n].cache = nil
		if wasPending && c.pendingTools > 0 {
			c.pendingTools--
		}
		// QUM-386 mirror: remove completed Agent from active set.
		if c.activeAgents[toolID] {
			delete(c.activeAgents, toolID)
			if c.lastActiveAgent == toolID {
				c.lastActiveAgent = ""
				for id := range c.activeAgents {
					c.lastActiveAgent = id
					break
				}
			}
		}
		return true
	}
	return false
}

// Update routes a toolTickMsg to the matching pending ToolCallItem and
// returns its follow-up cmd. Returns nil if no item matches (e.g. the row
// was removed) or if the matching item has resolved — the cmd chain dies
// naturally, satisfying AC #3 (no leak).
func (c *ChatList) Update(msg tea.Msg) tea.Cmd {
	tm, ok := msg.(toolTickMsg)
	if !ok {
		return nil
	}
	for _, env := range c.items {
		t, ok := env.item.(*ToolCallItem)
		if !ok || t.ToolID() != tm.ToolID {
			continue
		}
		cmd := t.Update(tm)
		// Cache invalidation: pending items already bypass the envelope
		// cache (Finished()==false short-circuit in renderEnvelope), so
		// mutating frame requires no cache nil-out. Keep it explicit anyway
		// for safety against future caching of in-flight items.
		env.cache = nil
		return cmd
	}
	return nil
}

// ResetPendingToolTicking clears the in-flight tick flag on every pending
// ToolCallItem in this list. Called by AppModel on observed-agent switches
// before re-arming with PendingToolTickCmds — otherwise an item whose tick
// chain was orphaned by a previous switch (its tick was delivered to the
// then-observed pane, found no match, dead-ended) would never re-arm,
// leaving the spinner frozen for the remainder of the tool call (QUM-732).
func (c *ChatList) ResetPendingToolTicking() {
	for _, env := range c.items {
		if t, ok := env.item.(*ToolCallItem); ok {
			t.ResetTicking()
		}
	}
}

// PendingToolTickCmds returns a batched tea.Cmd that arms a tick for every
// pending ToolCallItem that doesn't already have one in flight. Idempotent:
// calling it repeatedly only arms ticks on freshly-appended items. Returns
// nil when nothing needs arming, so the caller can append it safely to a
// cmd batch.
func (c *ChatList) PendingToolTickCmds() tea.Cmd {
	var cmds []tea.Cmd
	for _, env := range c.items {
		t, ok := env.item.(*ToolCallItem)
		if !ok {
			continue
		}
		if cmd := t.StartTickCmd(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// AppendSystemNotification peels one or more `<system-notification>`
// envelopes off the input and appends one SystemNotificationItem per
// envelope.
//
// L3 alignment (QUM-674 / QUM-693): when the input contains NO envelope at
// all, the raw text is intentionally dropped from cl — untagged inbox
// banners route to the statusbar transient label, not the chat region.
// Trailing residue after the last envelope is similarly dropped.
//
// Mirrors viewport.AppendSystemNotification's peel-loop (QUM-557/562/574)
// for the envelope path — the drain-row-inject matrix row's expectations
// are inherited via this contract.
func (c *ChatList) AppendSystemNotification(text string) {
	rest := text
	appended := false
	for {
		stripped, notifType, isInterrupt, remaining, ok := stripSystemNotificationTag(rest)
		if !ok {
			break
		}
		if !appended {
			c.dropTrailingThinkingMarker()
			appended = true
		}
		c.items = append(c.items, &itemEnvelope{
			item: NewSystemNotificationItem(&c.ctx, stripped, notifType, isInterrupt),
		})
		rest = remaining
	}
}

// AppendAutoTrigger appends a finished AutoTriggerItem.
func (c *ChatList) AppendAutoTrigger(summary string) {
	c.dropTrailingThinkingMarker()
	c.items = append(c.items, &itemEnvelope{item: NewAutoTriggerItem(&c.ctx, summary)})
}

// HasPendingAssistant reports whether the trailing item is an in-flight
// AssistantTextItem (set on first chunk, cleared on Finalize).
func (c *ChatList) HasPendingAssistant() bool { return c.streamingAssistant }

// HasPendingToolCall reports whether at least one ToolCallItem is still in
// flight (Finished()==false). Mirrors the legacy ViewportModel helper.
func (c *ChatList) HasPendingToolCall() bool { return c.pendingTools > 0 }

// Idle reports whether the list has no in-flight items: no streaming
// AssistantTextItem and no pending ToolCallItem. The S3 View() switch uses
// this to decide whether to render the chat region via ChatList (idle) or
// fall back to the legacy ViewportModel (in-flight). QUM-673.
func (c *ChatList) Idle() bool {
	return c.pendingTools == 0 && !c.streamingAssistant
}

// Reset replaces the items slice from a transcript-backfill snapshot
// (ChildTranscriptMsg / PreloadTranscript path). Translates each
// MessageEntry into the matching Append* so the resulting items list is
// equivalent to what the live-path Append calls would produce.
//
// Notes:
//   - Status / Error / Banner / System (legacy mail-glyph) entries have no
//     ChatList item type — those are S5 contract-violators routed to the
//     status bar / overlays. Reset silently skips them; View() falls back
//     to vp.View() when ChatList is empty so the user still sees them.
//   - Tool calls finalize via MarkToolResult so depth/agent bookkeeping
//     matches the live path.
func (c *ChatList) Reset(entries []MessageEntry) {
	c.items = nil
	c.pendingTools = 0
	c.streamingAssistant = false
	c.activeAgents = nil
	c.lastActiveAgent = ""
	for _, e := range entries {
		switch e.Type {
		case MessageUser:
			c.AppendUser(e.Content)
		case MessageAssistant:
			c.AppendAssistantChunk(e.Content)
			if e.Complete {
				c.FinalizeAssistantMessage()
			}
		case MessageToolCall:
			c.AppendToolCallWithHeader(e.Content, e.ToolID, e.Approved,
				e.ToolInput, e.ToolInputFull, e.HeaderArg, e.HeaderParams,
				e.ParentToolID)
			if !e.Pending {
				c.MarkToolResult(e.ToolID, e.Result, e.Failed)
			}
		case MessageSystemNotification:
			// Single-envelope: build the item directly (the Append peel-loop
			// expects raw text containing the envelope, which the backfilled
			// MessageEntry has already stripped).
			notifType := e.NotificationType
			if notifType == "" {
				notifType = NotificationKindMessage
			}
			c.items = append(c.items, &itemEnvelope{
				item: NewSystemNotificationItem(&c.ctx, e.Content, notifType, e.Interrupt),
			})
		case MessageAutoTrigger:
			c.AppendAutoTrigger(e.Content)
		default:
			// Status / error / banner / system entries: skip per the ChatList
			// contract — these surfaces route to the statusbar transient
			// label / γ overlay / tree badge instead.
		}
	}
	// L2 (QUM-674): Reset is a snapshot-replay entry point (preload /
	// restart / resync / waiting-banner / child transcript). A transcript
	// with a trailing Complete=false assistant entry would otherwise leave
	// streamingAssistant=true, sticking cl in not-Idle indefinitely. Force-
	// finalize the trailing assistant so Reset always lands in a clean state.
	c.FinalizeAssistantMessage()
}

// Render walks every envelope, hitting per-item caches keyed by
// (width, expanded) when the item is Finished, and bypassing the cache
// otherwise. Each item is followed by a trailing "\n", matching the
// legacy renderMessages walk's separator convention (which writes "\n"
// after every block, not just between them). Matching the trailing-
// newline produces visual parity with vp.View() for the matrix-row gate.
//
// QUM-691: the outer loop also owns inter-item separators. Between two
// items whose Go-types differ, an additional "\n" is inserted before the
// current item so a single blank line appears between them. Consecutive
// items of the same type are joined with no extra blank. No leading blank
// before the first item and no trailing blank after the last item. The
// per-item Render contract is "no leading or trailing blank" — items.go
// enforces that for AssistantTextItem; other items render without leading
// or trailing newlines.
//
// Width-0 guard: returns "" if SetSize has not been called (width == 0).
// Per plan §5 Q7 this prevents the cache filling with garbage at width 0
// before the first WindowSizeMsg arrives.
func (c *ChatList) Render(width int) string {
	if width <= 0 || c.width <= 0 {
		return ""
	}
	var sb strings.Builder
	var prevType string
	for idx, env := range c.items {
		curType := itemTypeKey(env.item)
		if idx > 0 && curType != prevType {
			sb.WriteString("\n")
		}
		sb.WriteString(c.renderEnvelope(env, width))
		sb.WriteString("\n")
		prevType = curType
	}
	return sb.String()
}

// itemTypeKey returns a string identifier for an Item's concrete type,
// used by Render to detect transitions between distinct item types for
// inter-item blank-line insertion (QUM-691).
func itemTypeKey(it Item) string {
	switch it.(type) {
	case *UserItem:
		return "user"
	case *AssistantTextItem:
		return "assistant"
	case *ThinkingItem:
		return "thinking"
	case *ToolCallItem:
		return "tool"
	case *SystemNotificationItem:
		return "notification"
	case *AutoTriggerItem:
		return "auto"
	default:
		return "other"
	}
}

// renderEnvelope returns the rendered output for one envelope, consulting
// and updating the cache as appropriate.
func (c *ChatList) renderEnvelope(env *itemEnvelope, width int) string {
	expanded := envelopeExpanded(env)
	if env.item.Finished() && env.cache != nil &&
		env.cache.width == width && env.cache.expanded == expanded {
		return env.cache.out
	}
	out := env.item.Render(width)
	if env.item.Finished() {
		env.cache = &cachedRender{width: width, expanded: expanded, out: out}
	} else {
		env.cache = nil
	}
	return out
}

// envelopeExpanded extracts the Expandable state for cache keying. Non-
// Expandable items always key as false.
func envelopeExpanded(env *itemEnvelope) bool {
	if ex, ok := env.item.(Expandable); ok {
		return ex.IsExpanded()
	}
	return false
}
