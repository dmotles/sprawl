package tui

// QUM-671 — chat render model.
//
// ChatList is the sole chat render source: it owns the item list and the
// per-(width, expanded) render cache that ViewportModel/ChatRegion display.
// (The pre-S6 ViewportModel `messages` slice + renderMessages walk and the
// migration-era dual-append shim are gone — see
// docs/designs/tui-structural-rewrite-plan.md §3.)
//
// Contract notes:
//   - No AppendStatus/AppendError/AppendBanner here. Those "contract
//     violators" are routed elsewhere (status bar / overlays) per the
//     in-code enforcement plan §3 S5 + §4.4.
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

// renderCacheEntry memoizes the outer ChatList.Render walk so a steady-
// state Render (no mutations between calls) returns in O(1). QUM-769.
type renderCacheEntry struct {
	width    int
	revision uint64
	out      string
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

	// QUM-769 outer Render cache. revision is bumped by invalidate() on every
	// observable state change; renderCache hits while revision and width are
	// unchanged AND the list is Idle (streaming bypasses so chunk-by-chunk
	// text repaints live). renderBuilds is a same-package instrumentation
	// counter incremented inside the cache-miss path; tests assert on it.
	revision     uint64
	renderCache  *renderCacheEntry
	renderBuilds int

	// zone holds uuid-keyed inbound frames that have been written to the CLI
	// stdin but not yet acknowledged (isReplay echo). It is a separate slice —
	// NOT part of items — so eager pending renders never disturb the
	// assistant-chunk coalescing invariant (trailing items entry is the
	// in-flight assistant). buildRender appends the zone after items so it reads
	// as the inline transcript tail. QUM-833.
	zone *pendingZone
}

// invalidate marks the outer Render cache dirty. Called by every mutator
// that changes the rendered output. Bumping a monotonic counter is cheaper
// than recomputing a fingerprint and harder to forget than per-mutator
// cache nilling.
func (c *ChatList) invalidate() {
	c.revision++
}

// Revision returns the current observable-state counter. ChatRegion uses
// this as a cheap cache fingerprint so it can skip vp.SetContent + vp.View
// when nothing under it has changed. QUM-769.
func (c *ChatList) Revision() uint64 { return c.revision }

// NewChatList constructs an empty ChatList bound to the given theme.
// Width starts at 0; Render is a no-op until SetSize is called.
func NewChatList(theme *Theme) *ChatList {
	return &ChatList{
		ctx: itemRenderCtx{
			theme:    theme,
			renderer: NewMarkdownRenderer(80),
		},
		zone: newPendingZone(),
	}
}

// Len returns the number of items in the list.
func (c *ChatList) Len() int { return len(c.items) }

// ZoneLen returns the number of pending-zone content items (user + system) held
// before their CLI echo. The new-content scroll indicator's content-detection
// must count these too, not just committed items — otherwise a freshly
// submitted prompt or first-frame drained notification arriving while the user
// is scrolled up gets no "new content below" cue. It counts items (not zone
// entries) so it is unit-consistent with Len(): a settle relocating one N-item
// entry into N committed items nets to zero and never spuriously flips the
// indicator. QUM-856.
func (c *ChatList) ZoneLen() int { return c.zone.itemCount() }

// Empty reports whether the list has no renderable content — neither committed
// items nor pending-zone entries. The emptiness gate for placeholder-vs-content
// must use this, not Len(), so a fresh-session pending prompt (held in the zone
// before its CLI echo) is not hidden behind the placeholder. QUM-854.
func (c *ChatList) Empty() bool { return len(c.items) == 0 && c.zone.len() == 0 }

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
		if c.width != 0 {
			c.invalidate()
		}
		c.width = 0
		return
	}
	if c.width != width {
		c.invalidate()
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
	c.invalidate()
}

// AppendUser appends a new UserItem.
func (c *ChatList) AppendUser(text string) {
	c.dropTrailingThinkingMarker()
	c.items = append(c.items, &itemEnvelope{item: NewUserItem(&c.ctx, text)})
	c.invalidate()
}

// AppendUserWithAttachments appends a committed UserItem carrying attachment
// chips (QUM-860). Used by the uuid-less bridge path (legacy/tests) that emits
// no consume ack, so there is no pending-zone settle to relocate.
func (c *ChatList) AppendUserWithAttachments(text string, chips []AttachmentChip) {
	c.dropTrailingThinkingMarker()
	c.items = append(c.items, &itemEnvelope{item: NewUserItemWithAttachments(&c.ctx, text, chips)})
	c.invalidate()
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
			c.invalidate()
			return
		}
	}
	c.dropTrailingThinkingMarker()
	c.items = append(c.items, &itemEnvelope{item: NewAssistantTextItem(&c.ctx, text)})
	c.streamingAssistant = true
	c.invalidate()
}

// FinalizeAssistantMessage marks the trailing AssistantTextItem (if any) as
// finished, allowing its render to be cached on next Render.
func (c *ChatList) FinalizeAssistantMessage() {
	wasStreaming := c.streamingAssistant
	if n := len(c.items); n > 0 {
		if a, ok := c.items[n-1].item.(*AssistantTextItem); ok && !a.Finished() {
			a.Finalize()
			c.items[n-1].cache = nil
		}
	}
	c.streamingAssistant = false
	if wasStreaming {
		c.invalidate()
	}
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
			c.invalidate()
			return
		}
	}
	c.items = append(c.items, &itemEnvelope{item: NewThinkingItem(&c.ctx)})
	c.invalidate()
}

// dropTrailingThinkingMarker removes a trailing ThinkingItem if one is
// present. Called by every non-thinking Append* verb — once real content
// arrives, the transient marker has served its purpose.
func (c *ChatList) dropTrailingThinkingMarker() {
	if n := len(c.items); n > 0 {
		if _, ok := c.items[n-1].item.(*ThinkingItem); ok {
			c.items = c.items[:n-1]
			c.invalidate()
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
	c.invalidate()
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
		c.invalidate()
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
		c.invalidate()
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
	if appended {
		c.invalidate()
	}
}

// ZoneAddUser adds an eager, uuid-keyed user prompt to the pending zone. It
// renders DIM as the inline transcript tail until its consume echo settles it
// and brightens it (QUM-832). QUM-833.
func (c *ChatList) ZoneAddUser(uuid, text string) {
	item := NewUserItem(&c.ctx, text)
	item.SetPending(true)
	c.zone.add(&pendingEntry{
		uuid:  uuid,
		kind:  pendingUser,
		items: []*itemEnvelope{{item: item}},
	})
	c.invalidate()
}

// ZoneAddUserWithAttachments adds an eager, uuid-keyed user prompt carrying
// attachment chip lines to the pending zone (QUM-860). Like ZoneAddUser it
// renders DIM until its consume echo settles + brightens it; the chip metadata
// lives on the UserItem so ZoneSettle's cache-nil + pending flip brightens the
// chip alongside the bubble.
func (c *ChatList) ZoneAddUserWithAttachments(uuid, text string, chips []AttachmentChip) {
	item := NewUserItemWithAttachments(&c.ctx, text, chips)
	item.SetPending(true)
	c.zone.add(&pendingEntry{
		uuid:  uuid,
		kind:  pendingUser,
		items: []*itemEnvelope{{item: item}},
	})
	c.invalidate()
}

// ZoneAddSystem peels a (possibly stacked) system-notification frame into N
// system-styled items held as one uuid-keyed zone entry. Born final-styled —
// notifications are already-settled facts. Mirrors AppendSystemNotification's
// peel-loop but targets the zone. QUM-833.
func (c *ChatList) ZoneAddSystem(uuid, rawText string) {
	var items []*itemEnvelope
	rest := rawText
	for {
		stripped, notifType, isInterrupt, remaining, ok := stripSystemNotificationTag(rest)
		if !ok {
			break
		}
		items = append(items, &itemEnvelope{
			item: NewSystemNotificationItem(&c.ctx, stripped, notifType, isInterrupt),
		})
		rest = remaining
	}
	if len(items) == 0 {
		// QUM-833 F1: classified as system by the cheap prefix check but no
		// envelope actually peeled (a malformed `<system-notification`-prefixed
		// frame). Render it verbatim as a user bubble so the live path matches
		// replay's peelNotificationEntries (which emits an unpeelable classified
		// body as a user entry) — single-classifier convergence over the
		// malformed boundary, with no silent drop.
		c.ZoneAddUser(uuid, rawText)
		return
	}
	c.zone.add(&pendingEntry{uuid: uuid, kind: pendingSystem, items: items})
	c.invalidate()
}

// ZoneSettle relocates the pending entry for uuid out of the zone and into the
// committed transcript at the current tail (consume-ordered). Returns false if
// no entry is tracked for uuid (the restart-orphan / supervisor-write no-op,
// ghost's C9) so the caller never blind-appends. QUM-833.
func (c *ChatList) ZoneSettle(uuid string) bool {
	e := c.zone.take(uuid)
	if e == nil {
		return false
	}
	c.dropTrailingThinkingMarker()
	// QUM-832: brighten the settled entry. A pending user bubble rendered dim
	// while in the zone; on settle it joins the committed transcript with normal
	// styling. The per-envelope render cache is keyed only on (width, expanded)
	// and UserItem.Finished() is always true, so the cached dim string would be
	// served stale — nil every relocated envelope's cache to force a fresh
	// (bright) render.
	for _, env := range e.items {
		if u, ok := env.item.(*UserItem); ok {
			u.SetPending(false)
		}
		env.cache = nil
	}
	c.items = append(c.items, e.items...)
	c.invalidate()
	return true
}

// ZoneDrop removes a pending USER entry (recall / supersede). System
// notifications are never recall-droppable (LOCKED invariant 5): a drop targeting
// a system uuid is refused. Returns true only when a user entry was removed.
// QUM-833.
func (c *ChatList) ZoneDrop(uuid string) bool {
	e, ok := c.zone.byUUID[uuid]
	if !ok || e.kind != pendingUser {
		return false
	}
	c.zone.take(uuid)
	c.invalidate()
	return true
}

// ZoneUserCount returns the number of pending user-submitted prompts (system
// notifications excluded). Drives the HasQueued short-help binding. QUM-833.
func (c *ChatList) ZoneUserCount() int { return c.zone.userCount() }

// ClearZone drops every pending entry (session restart tears down the CLI
// command queue, so its outstanding projection is gone). QUM-833.
func (c *ChatList) ClearZone() {
	if c.zone.len() == 0 {
		return
	}
	c.zone.clear()
	c.invalidate()
}

// AppendAutoTrigger appends a finished AutoTriggerItem.
func (c *ChatList) AppendAutoTrigger() {
	c.dropTrailingThinkingMarker()
	c.items = append(c.items, &itemEnvelope{item: NewAutoTriggerItem(&c.ctx)})
	c.invalidate()
}

// AppendCompactBanner appends a finished first-party compaction banner
// (QUM-865). text is the pre-formatted banner line.
func (c *ChatList) AppendCompactBanner(text string) {
	c.dropTrailingThinkingMarker()
	c.items = append(c.items, &itemEnvelope{item: NewCompactBannerItem(&c.ctx, text)})
	c.invalidate()
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
	// QUM-833: a backfill snapshot (preload / restart / resync / child switch)
	// replaces the committed transcript wholesale; any un-settled pending entry
	// is stale and must not render under the fresh transcript.
	c.zone.clear()
	c.invalidate()
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
			// QUM-857: the parsed summary (e.Content) is intentionally not
			// propagated — the marker renders a fixed cue, not the body.
			c.AppendAutoTrigger()
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
	// QUM-769: while anything is streaming or a tool tick is in flight, bypass
	// the outer cache so chunk-by-chunk text + spinner frames repaint live.
	// The per-envelope cache still serves finished items inside the walk.
	if !c.Idle() {
		return c.buildRender(width)
	}
	if c.renderCache != nil && c.renderCache.width == width && c.renderCache.revision == c.revision {
		return c.renderCache.out
	}
	out := c.buildRender(width)
	c.renderCache = &renderCacheEntry{width: width, revision: c.revision, out: out}
	return out
}

// buildRender is the uncached worker: walks every envelope, applies the
// per-envelope cache, and concatenates with QUM-691 inter-type separator
// semantics. Counts invocations via renderBuilds for in-package test probes.
func (c *ChatList) buildRender(width int) string {
	c.renderBuilds++
	var sb strings.Builder
	var prevType string
	n := 0
	// render walks committed items first, then the pending zone (QUM-833) so the
	// zone reads as the inline transcript tail. The n counter spans both regions
	// so the QUM-691 inter-type blank-line rule applies across the boundary just
	// like any other type transition (no explicit separator).
	render := func(env *itemEnvelope) {
		curType := itemTypeKey(env.item)
		if n > 0 && curType != prevType {
			sb.WriteString("\n")
		}
		sb.WriteString(c.renderEnvelope(env, width))
		sb.WriteString("\n")
		prevType = curType
		n++
	}
	for _, env := range c.items {
		render(env)
	}
	for _, e := range c.zone.order {
		for _, env := range e.items {
			render(env)
		}
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
