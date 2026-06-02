# TUI Redesign — Design Research

**Status:** research / design notes
**Author:** ghost (research agent)
**Date:** 2026-06-02
**Branch:** `dmotles/tui-redesign-research`

## Purpose

`sprawl enter` is being redesigned around the chat-style spike on
`dmotles/tui-chat-spike` (Bubble Tea v2 altscreen, single-column,
sticky header + sticky input, app-owned message list with
`Expandable` items). This document inventories every behavior of the
production TUI in `internal/tui/`, maps each behavior to the new
model, and surfaces the open design decisions and risks that the
spike did not address.

It is **research only** — no code lands from this doc. Where a
question has an obvious answer I just state it; depth is reserved
for the genuinely hard problems (multi-agent observation, modal
hierarchy under altscreen, question-modal lifecycle, recover/handoff
under a chat layout).

The spike already established the *render* model. What this doc
designs is **everything around it**.

---

## 1. Current TUI — behavior inventory

The production TUI is ~28 source files in `internal/tui/` plus
`cmd/enter.go` and `cmd/enter_notify.go`. The full message-type
catalog (≈50 `*Msg` types) and `Update()` branch list (≈85 branches)
is too long to reproduce inline; see the appendix-equivalent in the
ghost findings directory or just read `messages.go` and `app.go`'s
`Update`. Below I group the surface area into ~25 distinct behaviors
and tag each with a disposition under the chat redesign.

Legend: **PORT** = clean port, render only changes. **DECIDE** =
needs design call below. **DROP** = goes away under the new model.

| # | Behavior | Where today | Disposition |
|---|---|---|---|
| 1 | Transcript rendering (user / assistant / tool / system / banner / error) | `viewport.go`, `render.go` | **PORT** — already what the spike does, just wired to real events |
| 2 | Auto-scroll lock at bottom + "↓ new content" hint when scrolled up | `viewport.go` | **PORT** |
| 3 | Tool call lifecycle (pending spinner → success/failure glyph → result) | `app.go` spinner + `tool_header.go` | **PORT** — spike already has `toolCallItem` with ⚙/⠿/✗ |
| 4 | Expand/collapse tool call inputs/outputs | Ctrl+O (global flag), `viewport.go` | **PORT** — spike's `Expandable` + Ctrl+O is the target |
| 5 | Thinking-block rendering | TurnThinking state in status bar (text not shown) | **DECIDE** — promote to a collapsed `thinkingItem` (spike already has it) |
| 6 | Nested / sub-agent tool calls (Depth>0 indented under parent Agent call) | `viewport.go` QUM-379/386 | **PORT** with care — indent rule survives |
| 7 | Tree panel (agent list, status dots, unread badges, cost tags, FAULT badge) | `tree.go` | **DECIDE** — collapses into the 3-line header (multi-agent question §3.1) |
| 8 | Activity panel (tool-call tail for observed agent) | `activity.go`, `activity_stream.go` | **DECIDE** — sidebar dies; see §3.2 |
| 9 | Status bar (cost, tokens, session ID, turn state, version, agent count, pending MCP ops, validate pill, question pill) | `statusbar.go` | **PORT** — already 1 line, fits spike model |
| 10 | Short-help / hint strip above status bar | `shorthelp.go` | **PORT** |
| 11 | Command palette `Ctrl+/` (commands + agent switcher) | `palette.go` | **DECIDE** — keep palette but reconsider what it dispatches; see §3.5 |
| 12 | Help modal (F1 / ?) | `help.go` | **DECIDE** — see §3.5 |
| 13 | Error dialog (session-fatal: [r]estart / [q]uit) | `error_dialog.go` | **DECIDE** — see §3.5 |
| 14 | Confirm dialog (Ctrl+C quit confirmation) | `confirm.go` | **DECIDE** — see §3.5 |
| 15 | Question modal (`ask_user_question` MCP, multi-question, hard/soft dismiss) | `question.go` | **DECIDE** — hardest modal; see §3.5 |
| 16 | Validate popup (merge validate output, sticky on failure) | `validate_popup.go` | **DECIDE** — see §3.5 |
| 17 | Reverse-i-search of input history (Ctrl+R) | `app.go:2400+`, `history.go` | **PORT** — note: this is *input-history* search, **not** scrollback search (see §3.9) |
| 18 | Bracketed-paste handling + Enter lookahead debounce | `app.go` pasteLookaheadMsg, `inputcoalesce/` | **PORT** — orthogonal to layout |
| 19 | Input history (.sprawl/input-history, 1000 entries, ↑/↓ when cursor at edges) | `history.go` | **PORT** |
| 20 | Pending-submit queue (single slot; "⏸ queued: …" suffix when mid-turn) | `app.go` pendingSubmit | **PORT** |
| 21 | Esc → interrupt turn; second Esc / queue cancel | `app.go` Update | **PORT** |
| 22 | Restart / handoff banner ("Session restarting (…)…", "Recovered N agents", "Backend recovered") | banner.go, AppendStatus | **PORT** — inline system messages in the chat |
| 23 | Backend fault sticker on tree + recovery banner | `tree.go`, `app.go` BackendFault* | **DECIDE** — tree collapses; needs new affordance §3.7 |
| 24 | Child agent observation (Ctrl+N/P cycle, switch transcript) | `app.go` cycleAgent, `child_stream.go` | **DECIDE** — biggest open question §3.1 |
| 25 | Notifications (inbox arrival, message-from-child, MCP-op-in-flight, cost/token updates) | InboxArrivalMsg, MCPCall* | **DECIDE** — surface routing §3.6 |
| 26 | Mouse capture + Ctrl+_ selection-mode toggle | `app.go`, `selection.go` (QUM-617) | **DECIDE** — see §3.10 |
| 27 | Viewport "select mode" `v`/`j`/`k`/`y` for yanking raw markdown | `selection.go` (QUM-281) | **DECIDE** — see §3.10 |
| 28 | Panel focus cycling (Tab / Shift+Tab between tree/viewport/input) | `app.go` activePanel | **DROP** — single column has nothing to cycle |
| 29 | Inbox drain on idle / prompt injection ("system-notification" wrappers) | `app.go` InboxDrainMsg, `inboxprompt/` | **PORT** — rendered as a system message |
| 30 | View cache (avoid re-render of unchanged panels during paste bursts) | `view_cache.go` (QUM-451) | **PORT** with simpler scope (only the viewport caches) |

---

## 2. Anchor: what the spike already gives us

From `internal/tuichat/` on `dmotles/tui-chat-spike`:

- Altscreen Bubble Tea v2 model with `header (3) | viewport | gap (1) | input (1) | status (1)`.
- `Item` interface with concrete types `userItem`, `assistantTextItem`, `thinkingItem`, `toolCallItem` (`items.go:14`).
- `Expandable` interface (`items.go:8`) implemented by `thinkingItem` and `toolCallItem`; Ctrl+O walks the list in reverse and toggles the most recent (`chat.go:264-274`).
- Glamour markdown renderer for assistant text, cached + rebuilt on width change (`chat.go:112-138`).
- SPRAWL wordmark with per-rune cyan→purple gradient via lipgloss truecolor (`header.go:78-93`); narrow-fallback breadcrumb when `width < 70`.
- A *mock* agent tree drawn on the right of the header (`header.go:32-47`, `fakeTree()`).
- `startTurnCmd` is a single goroutine that unmarshals `protocol.Message` JSON and converts to items. **There is no event-bus integration, no supervisor, no modals, no observability of other agents.**

This is the floor. Everything in §3 below is the gap between the
spike and a real replacement for `internal/tui/`.

---

## 3. Design decisions

### 3.1 Multi-agent observation

**Current.** Ctrl+N / Ctrl+P cycle `observedAgent`. When observing a
non-root agent (`observedAgent != rootAgent`) the input panel is
**hidden** (`app.go:1872`), the activity panel switches to that
agent's tail, and the viewport shows that agent's transcript via
the unified `ChildStreamAdapter`. The user can also `/switch
<agent>` from the palette or click into the tree.

**Crucial constraint that the prompt mis-stated.** The user
**cannot send messages to a child agent from the TUI today**.
Children receive messages only via weave's MCP `send_message` tool.
The TUI is observe-only when looking at a child. That removes a
whole open question — there is no UX to port for "address a child".

**Options for the new model:**

| Option | Pros | Cons |
|---|---|---|
| (a) **Separate transcripts** + slash-cmd `/observe <name>` + Ctrl+N/P cycle (status-quo behavior, new render) | Familiar; matches today's mental model; keeps weave's transcript clean; agent-specific tool-call rendering already implemented per-viewport | Mode-switching is invisible from the chat scrollback; no "where did weave come from" affordance |
| (b) Interleaved single transcript with `[finn]` prefix per message | Simpler model; user sees everything in flight; no mode | Drowns weave; tool-call expand/collapse semantics get awkward across agents; cost rendering ambiguous |
| (c) Tabs at top of viewport | Discoverable | Adds a UI primitive the spike doesn't have; gets ugly with N>4 agents |
| (d) Modal switcher | Already exists (palette `/switch`) | Loses ambient awareness |

**Recommendation: (a) — separate transcripts, Ctrl+N/P cycle,
`/observe <name>` palette command.** This is the lowest-disruption
mapping, preserves the per-agent viewport state we already have
(seen-tool-ids, nesting state, backfill epoch), and the spike's
3-line header is the ambient affordance for "which other agents
exist + their state". The header tree must be **real**, not the
spike's `fakeTree()`, and it must highlight the observed agent
distinctly (e.g. underline + arrow on the observed node). When
observing a child, the input row swaps to a dim "observing <name> —
Ctrl+N back to weave" hint (no textinput, matches current behavior).

### 3.2 Inline child activity (the dead sidebar)

The activity sidebar dies with the multi-column layout. Three
candidates for where the data goes:

1. **Drop it.** Tool calls of the observed agent already render in
   that agent's transcript when you `Ctrl+N` to them. The sidebar
   today is largely a duplicate of what the viewport shows.
2. **Header expansion.** Below the 3-line header, expose a 1-line
   "currently: <tool name> · 12s" per non-idle agent. Cheap, ambient.
3. **Inline as collapsed tool blocks in weave's transcript** when a
   child agent is doing something noteworthy (delegated via
   `spawn`/`delegate`).

**Recommendation: drop the panel + add option (2) as a header
embellishment.** The header tree already shows status dots
(working / blocked / done / failure). Append the most-recent
tool-name + elapsed for each non-idle non-weave agent as a one-line
status under the tree. Users who want full activity can `Ctrl+N`
into that agent. Option (3) is wrong: it conflates weave's
transcript with what children are doing and breaks the
chat-as-conversation metaphor.

### 3.3 Sending a message to a child

Already answered above: **users do not address children directly
today and we should not start.** If we ever want this:

- `/send <name> <msg>` palette command is the cleanest fit (matches
  existing `/handoff` prompt-injection pattern).
- `@finn …` prefix is a footgun (collides with markdown, GitHub
  mentions, future autocomplete).
- Modal is overkill.

But this is a non-goal for the redesign — keep parity with today.

### 3.4 MCP tool calls weave makes (`spawn`, `merge`, `retire`,
`send_message`, `delegate`, `ask_user_question`)

These already render today as collapsed tool blocks in weave's
viewport — same machinery as any other tool call. The spike's
`toolCallItem` collapses to a one-liner and expands on Ctrl+O.
**Disposition: keep as-is, no special styling.** The header tree
already shows the side-effects (new agent appears, fault badge,
status dot transitions) so the user gets ambient feedback without
us needing a bespoke "weave spawned finn" banner.

Exceptions:

- `ask_user_question` is special: its tool-call result is gated on
  the question modal closing. The tool-call entry should render in
  the transcript but its "result" body should not duplicate the
  user's answer back into the chat — the question modal already
  consumed that interaction. Today this works; preserve.
- `handoff` is a *prompt-injection* template (palette
  `InjectPromptMsg`), not a render. Keep that path.

### 3.5 Modals under altscreen

This is the chewiest area. We are already in altscreen, so we
*can* paint overlay modals — but every overlay we draw fights the
"chat = one column" thesis of the redesign. Three options per
modal:

(α) **Inline block in the transcript** that the user interacts
with then it collapses to a normal item.
(β) **Grow the sticky region** at the bottom temporarily (replace
the 1-line input with an N-line form).
(γ) **Centered altscreen overlay** as today.

| Modal | Recommendation | Why |
|---|---|---|
| Help (F1/?) | **γ overlay** | Stateless reference; doesn't mutate session; overlay is what every TUI does and what users expect |
| Palette (Ctrl+/) | **β grow sticky region** | The palette already replaces the input semantically; expanding the sticky input box into a filtered list is more honest than a free-floating box |
| Error dialog (session-fatal) | **γ overlay** | Blocks all interaction; centered modal is the only safe affordance |
| Confirm (Ctrl+C quit) | **α inline** as a "[y/n] Quit?" system message at the bottom of the scrollback, keyboard-focused | The dialog is one question with two answers; pinning it inline keeps history honest about "the user attempted to quit at T" |
| Question (`ask_user_question`) | **β grow sticky region** | This is the hardest one. It's interactive (cursor + multi-pick + custom text + per-question tabs), it can be soft-hidden + reopened (Ctrl-Q), and there's a hard-cancel path that unwedges the MCP call. Trying to do α (inline) breaks Ctrl-Q reopen semantics — once it scrolls away you'd be hunting through scrollback. Trying to do γ (overlay) is fine but loses the "you have a pending question" ambient affordance. Growing the sticky region keeps it both visible and dismissible. Soft-hide collapses back to 1-line input + "🔔 pending question — Ctrl-Q" hint in the short-help strip (already the pattern today). |
| Validate popup | **α inline** for the streamed output, plus an existing minimized **status-bar pill** for short cases | Validate output is *log content*. It belongs in the scrollback as a tool-call-like block. On failure, sticky-pin a header strip "validate failed — Ctrl-V to view" until the user dismisses. |

**Modal hierarchy.** Today: help > error > confirm > palette >
question > validate. Preserve this with the caveat that β-style
modals (palette, question) live in the sticky region and γ-style
(help, error) live on top. We should never have *both* β and γ up
at the same time; if an error arrives while the palette is open,
the palette closes first (today's behavior).

### 3.6 Notifications

| Event | Today | Recommendation |
|---|---|---|
| Inbox arrival (message-from-child) | AppendStatus + tree-badge refresh | Inline system message in chat + count badge on header tree node — identical to today, just rendered differently |
| Cost/token update | Status-bar field | Status-bar field, unchanged |
| Rate-limit hit | Currently surfaces as a transport error → error dialog | Inline system message + sticky pill in status bar |
| MCP op > 60s threshold | Banner in viewport + status-bar `⏳` pill | Same |
| Backend fault on child | Tree FAULT badge + status-line banner | Header tree FAULT badge + inline system message in *weave's* transcript ("finn: backend fault: <class>"). When user `Ctrl+N`s to finn, the child viewport shows the fault context too |
| Auto-continue triggered | AppendAutoTrigger banner | Same — inline system message |

Rule of thumb: anything observable in the past tense → inline
system message. Anything in-flight or status-of-the-world →
status-bar field or header decoration. Banner-flash (toast-style)
is not needed; it doesn't fit chat ergonomics.

### 3.7 Lifecycle

| Path | Today | Recommendation |
|---|---|---|
| Handoff requested (`HandoffRequestedMsg` via `internal/rootinit/postrun.go`) | Status banner + `RestartSessionMsg` + async restart fn → `RestartCompleteMsg` reinstalls bridge | Identical state machine. Render: inline system message "Session restarting (handoff)…" → bridge closes → consolidation phase messages appear inline → new SessionBanner item on `RestartCompleteMsg`. |
| Recover ("recovered N agents", `Real.Recover` + `cancelByAgent` for QUM-611) | `AgentsResumedMsg` banner on startup | Same — inline system message at top of weave's transcript |
| In-flight turn cancel (Esc → bridge.Interrupt → `InterruptCompletedMsg`) | Status banner + finalizeTurn | Inline system "Interrupt sent — waiting for turn to end" then "Turn interrupted" |
| Backend fault recovered (`BackendFaultClearedMsg`) | Banner + tree-badge clear | Inline system message in the *child's* viewport (where the fault happened) + tree dot reverts |
| Terminal error (transport non-EOF) | Error dialog | γ overlay; user picks [r] / [q] |
| EOF (graceful session end) | Auto-restart via `SessionErrorMsg{io.EOF}` → `RestartSessionMsg` | Same; inline "Session ended — restarting…" |
| Question wedge (QUM-611) — child stuck on `ask_user_question` mid-`Recover` | `Real.Recover` proactively `cancelByAgent` then re-presents the queue | Unchanged; the TUI sees a `CancelQuestionMsg` followed by a fresh `QuestionsAvailableMsg`. Render: short flash "Question reset (recovery)" then re-show modal |
| Drain/inject (QUM-555, QUM-619) | Inbox drained on idle → injected as `system-notification` wrappers | Inline system messages, identical to today; the `<system-notification>` envelope-strip code in `messages.go` ports unchanged |

### 3.8 Resize edge cases

The spike already handles `WindowSizeMsg` by recomputing
`header=3, status=1, input=1, gap=1, chat=remaining`, but graceful
degradation is missing. Add the following chain:

1. **Width ≥ 70.** Full SPRAWL wordmark + tree on right. (Spike
   already does this.)
2. **40 ≤ Width < 70.** Wordmark falls back to the gradient
   breadcrumb (`weave → finn → radar`); tree compresses to one
   line of dots. (Spike has the breadcrumb path; tree compress
   needs work.)
3. **Width < 40.** Header collapses to 1 line: just the breadcrumb.
   No tree. Status bar drops version + agent-count.
4. **Width < 30.** Show a single screen: "Terminal too narrow
   (need ≥ 30 cols)" and stop rendering normal UI. This already
   exists in `app.go` as `tooSmall`.
5. **Height < 12.** Drop the gap line, drop the short-help strip;
   collapse header to 1 line. Today's `MinTermHeight=10` floor
   stays.
6. **Height < 10.** `tooSmall` screen.
7. **Resize mid-stream.** Glamour renderer rebuilds on width
   change (spike `chat.go:112-125`). Viewport must re-flow without
   re-running tool calls. The cached `view_cache.go` (QUM-451)
   pattern survives but its key needs to include width.

### 3.9 Search — clarification + recommendation

The prompt asked about a "searchOverlay" that lets users grep
in-app scrollback. **There is no such thing today.** The Ctrl+R
flow in `app.go:2400+` is shell-style reverse-search of the
**input history** (`history.go`), not the transcript.

So this is two separate questions:

1. **Input-history reverse-search (Ctrl+R).** Keep. It's keyboard
   ergonomics, orthogonal to layout, renders fine as a one-line
   prompt above the input.
2. **Scrollback grep — should we add it?** Probably yes,
   eventually, but **defer past the redesign cutover**. The
   altscreen model can support it cheanly later as a β-style
   sticky-region grow (`/` in viewport mode → grep prompt → next
   match scrolls). It is not load-bearing for the redesign and
   not currently a user expectation. Mark as follow-up.

### 3.10 Mouse capture / selection

Two interacting features today:

- **Mouse cell-motion capture** so the viewport sees wheel scroll.
  This blocks native terminal click-drag selection.
- **Ctrl+_ / Ctrl+/ toggle** (QUM-617) drops mouse capture so the
  user can drag-select with the host terminal. Status bar shows
  `-- SELECT (mouse capture off) --`.
- **Viewport `v`/`j`/`k`/`y` "select mode"** (QUM-281) for
  yanking raw markdown via OSC 52.

Crush's approach (per the reference): implement own selection via
OSC 52 inside the TUI, no need to drop mouse capture.

| Option | Pros | Cons |
|---|---|---|
| (a) **Keep `Ctrl+_` toggle**, drop the viewport `v`/`j`/`k`/`y` mode | Simple; matches user expectation that "the terminal does selection"; one less mode to maintain | Loses the "yank a single message as raw markdown" affordance |
| (b) Implement crush-style **own selection** via OSC 52, no capture toggle | Pretty; one consistent interaction model | Significant implementation work; OSC 52 paste-back is widely supported but copy out is not universal; mouse must do double-duty (scroll + drag-select) |
| (c) Keep **both** | Maximum flexibility | Maximum confusion |

**Recommendation: (a) for the cutover; revisit (b) post-soak.**
Ctrl+_ is well-understood, already documented in `CLAUDE.md`, and
the redesign is risky enough without taking a dependency on
broad OSC 52 copy support. The `v/j/k/y` mode is little-used per
my reading of the code; punt it. (If users complain, revisit.)

---

## 4. Edge cases & complications uncovered

While reading `messages.go`, `app.go::Update`, and the supervisor
event paths I found several non-obvious things that complicate
the redesign:

1. **The pending-submit queue is a single slot, not a queue**
   (`pendingSubmit string`, app.go ~line 200). The behavior is
   "last Enter wins". Esc cancels and — per QUM-576 — refuses to
   clobber a non-empty input. Spike has no queue at all today.
2. **`UserMessageSentMsg` is the commit barrier for inbox drain**
   (QUM-323). `commitDrainCmd` only fires after the bridge confirms
   send. If you re-architect submit you must preserve this ordering
   or you re-introduce the QUM-555 race.
3. **Backfill epoch on child streams** (QUM-439, QUM-479). Each
   `AgentSelectedMsg` increments an epoch; `ChildStreamMsg` events
   tagged with stale epochs are silently dropped. The new model
   *must* preserve this — without it, switching agents fast can
   replay old events into the new viewport.
4. **Per-agent viewport state** (`agentBuffers map`,
   `seenToolIDs`, `activeAgents`, `lastActiveAgent` — QUM-334/386).
   This is non-trivial; tool-call nesting depends on these. The
   new chat-list model needs the same shape: one `[]Item` per
   agent, not a single shared list.
5. **System-notification envelope stripping** (`stripSystemNotificationTag`,
   QUM-557/562). System messages from the inbox arrive wrapped in
   `<system-notification …>` envelopes. The stripper peels **one
   per call** and the caller loops to peel back-to-back wrappers
   so nested notifications render as separate items. Easy to break
   on a rewrite.
6. **Spinner is global, not per-tool** (QUM-336). A single
   `spinner.Model` is pushed into every viewport on each tick;
   ticking gates on `pendingToolCalls > 0`. The spike has per-item
   icons (⚙/⠿/✗) which is simpler but loses the synchronized
   pulse. Probably fine to switch to per-item.
7. **`view_cache.go` (QUM-451)** caches bordered panel renders to
   survive paste bursts. Single-column has fewer panels to cache,
   but viewport renders during paste are still expensive — keep
   the cache, narrow its key.
8. **Mouse events are routed only when no modal is up** (app.go
   ~line 600). Easy to forget when restructuring the Update.
9. **Modal-stack swallowing**: each modal's Update returns early
   for all keys except its own. Hierarchy is order-dependent in
   the switch. If we move palette/question to β (grow sticky
   region), they need a new "this region is focused" gate that
   replaces the existing showXxx checks.
10. **Auto-restart on EOF** is wired through `SessionErrorMsg` →
    `RestartSessionMsg`. If we change how transport errors surface
    we must not break this path.
11. **Validate popup is a multi-state machine** (Hidden / Queued /
    RunningHidden / RunningVisible / Minimized / Failed). On
    failure it is **sticky** until the next merge or kill. The
    redesign must carry this state through.
12. **Question modal's hard vs soft dismiss** (QUM-611):
    `DismissQuestionMsg{Hard:true}` calls
    `Supervisor.CancelQuestion` which unwedges the MCP tool call.
    Soft hide keeps drafts. Easy to get wrong; needs the same
    Hard bit through the new modal plumbing.
13. **Bracketed-paste coalescing** (`inputcoalesce/`, QUM-608).
    The `tea.NewProgram` call in `cmd/enter.go::resolveEnterDeps.runProgram`
    installs a coalescer. The new TUI's program-setup needs the
    same wiring or paste perf falls off a cliff.
14. **Question consumer registration** is the call site at
    `cmd/enter.go` that wires `QuestionsAvailableMsg` from
    supervisor → TUI. Mandatory-test-row `ask-user-question`
    covers this; do not silently delete the goroutine.
15. **Mandatory-test e2e matrix rows**: from `CLAUDE.md`, the
    following rows directly touch the TUI redesign surface:
    - `notify-tui` — covers `tree.go`, `messages.go`, `app.go`.
      Contract: child status changes surface to user. **Survives** if
      header tree real-renders status dots.
    - `handoff` — covers `HandoffRequestedMsg` /
      `SessionRestartingMsg` / `RestartSessionMsg`. **Survives** if
      we keep the message names and the post-restart bridge
      re-install.
    - `ask-user-question` — covers `internal/tui/question.go`,
      `internal/tui/app.go` question modal, `Ctrl-Q` binding,
      `View()` composition for `showQuestion`, status-bar
      pending-questions field. **At risk** — every one of these
      changes under the redesign. Plan: keep the same
      `messages.go` types (`QuestionsAvailableMsg`,
      `DismissQuestionMsg{Hard}`, `CancelQuestionMsg`,
      `QuestionAnsweredMsg`) and the same supervisor consumer
      registration; let the row pass on message-flow assertions.
    - `drain-row-inject` — covers `messages.go` / `viewport.go` /
      `cmd/enter.go` injection wiring. **Survives** if the
      system-notification stripper ports verbatim.
    - `paste-coalesce` — covers `resolveEnterDeps.runProgram`.
      **Survives** if we keep the coalescer at the program
      construction site.
16. **`unifiedHandle.ForceInterruptDelivery` + `drainPendingToQueue`**
    (QUM-619, `internal/supervisor/runtime_launcher.go`). The
    idle-recipient gate ensures interrupts only land when the
    recipient is idle. Untouched by the redesign — it lives
    below the TUI — but is the load-bearing assumption that
    inbox drains never cross-talk with mid-turn streams.

---

## 5. Migration plan

### Phase 0 — finalize this doc + open Linear epic

`QUM-???` (TBD) — TUI redesign tracking issue. Reference this doc.

### Phase 1 — promote `internal/tuichat/` to `internal/tui2/`

Move the spike onto a redesign branch (off `dmotles/tui-chat-spike`).
Replace `fakeTree()` with a real `*supervisor.Tree` adapter
(read-only). Wire `protocol.Message` unmarshal to the real
`tuiruntime.TUIAdapter` event stream. **No modals yet.** Goal:
weave-only chat works against a real session for a single root
agent. Validate manually.

### Phase 2 — multi-agent observation

Add per-agent viewport buffers (`map[string][]Item`), `ChildStreamAdapter`
integration, Ctrl+N/P cycle, observed-agent highlight in header
tree, child-mode hides input. Backfill epoch ports verbatim from
today.

### Phase 3 — modals

Port in order of independence:

1. Help (γ overlay) — easy, stateless.
2. Confirm (α inline) — small.
3. Error dialog (γ overlay).
4. Palette (β grow sticky region).
5. Validate popup (α inline + status-bar pill).
6. Question modal (β grow sticky region) — last, hardest, must
   pass `ask-user-question` matrix row.

### Phase 4 — lifecycle & notifications

Handoff, recover, fault/recovery banners, inbox drain/inject,
MCP-op pills, auto-continue. All of these are message-handler
ports — the existing `messages.go` types come along.

### Phase 5 — soak behind a flag

Ship as `sprawl enter --new` for ~1 week. Old TUI is the default.
Dogfood with the agent fleet. Fix paper-cuts.

### Phase 6 — flip default + delete old

`sprawl enter --new` becomes default; old code path becomes
`--legacy`. After another week with no regressions, delete
`internal/tui/` and rename `internal/tui2/` → `internal/tui/`.
Update `CLAUDE.md`'s mandatory-test rows to point at the new
paths.

### Why phased + flagged, not atomic

- The mandatory-test e2e matrix is the contract. Six rows touch
  TUI files. Flagged rollout lets the matrix run against *both*
  implementations for the soak.
- The TUI is the only user-facing surface; an atomic swap that
  ships a broken modal kills user trust on the worst possible
  surface.
- Agents observing other agents (Phase 2) and the question modal
  (Phase 3.6) are independently risky. Phasing them lets us back
  out one without the other.

### Why not keep the two side-by-side forever

`agentBuffers`, `viewCache`, `view_cache.go`, `selection.go`,
`tree.go`, `activity.go` are non-trivial code. Maintaining two
TUIs taxes every TUI change. Hard cutover after soak; budget two
weeks soak max.

---

## 6. Risk inventory

**Load-bearing assumption #1: the spike's `Item` interface scales
to ≥200 entries per agent without re-render cost blowing up.**
Today's viewport caches at the bordered-panel level (`view_cache.go`).
The spike renders the full list every frame. If glamour re-renders
markdown on every keypress for a 100-message scrollback the TUI
will feel sluggish. *Mitigation*: cache per-item rendered output,
key by (width, expanded-state).

**Load-bearing assumption #2: Ctrl+N/P + observed-agent input-hide
remains the multi-agent UX.** If users push back hard ("I want to
chat with finn directly") we will need to add `/send <name>` and
revisit whether children should also have an input affordance.
That's a chunk of supervisor work (`Real.SendMessage` is wired,
the TUI just doesn't surface it).

**Load-bearing assumption #3: β-modals (palette, question) inside
the sticky region won't be visually confusing.** This is the one
that scares me most. The question modal can be 10+ lines of form;
that's a lot of "input region". If users get lost we may have to
revert to γ overlays for the question modal.

**Load-bearing assumption #4: glamour markdown rendering is fast
enough at terminal width.** Today's TUI uses glamour too. Should
be fine, but worth a benchmark on 100-item transcripts.

**Other risks:**

- `ask-user-question` mandatory-test row is the highest-failure
  candidate. Allocate time to debug it during Phase 3.6.
- Selection mode change (drop `v`/`j`/`k`/`y`) may break the
  `selection_test.go` tests; need to update or delete them.
- Spike has no test coverage. The redesign branch starts from
  ~0 test coverage of `internal/tuichat/`; we should port the
  shape of tests from `internal/tui/app_*_test.go` as Phase 0.5.
- Bubble Tea v2 alpha churn — spike is on v2; production TUI is
  also on a recent v2 release. Check API drift.
- OSC 52 selection (if we revisit assumption from §3.10) has
  inconsistent terminal support; skip.
- The header tree is going to be a lot more information-dense
  than the spike's mock. At ≥6 agents the right-hand tree blob
  starts to crowd the wordmark. Plan: when `len(tree.Nodes) > N`,
  hide non-active branches behind a "…" with a hint.

---

## 7. Recommendations summary

1. Adopt the spike's render model as the floor.
2. Drop multi-pane layout; replace tree+activity sidebars with
   the 3-line header tree + per-non-idle-agent status hint.
3. Ctrl+N/P + observed-agent input-hide is the multi-agent UX,
   identical to today. Header tree highlights the observed agent.
4. β-style sticky-region growth for palette + question modal;
   γ-style overlay for help + error; α inline for confirm +
   validate.
5. Drop the viewport `v`/`j`/`k`/`y` "select mode"; keep the
   `Ctrl+_` mouse-capture toggle.
6. Drop input-panel focus-cycling (Tab/Shift+Tab); single column
   has nothing to cycle.
7. Defer scrollback search to a follow-up.
8. Phased migration behind `sprawl enter --new`; flip default
   after ~1 week soak; delete old `internal/tui/` after another.
9. Keep `messages.go` types stable across the rewrite — that's
   how the mandatory-test e2e matrix rows survive the cutover.

---

## Appendix A — file map for the redesign

Production code that the new TUI replaces or absorbs:

```
internal/tui/app.go                  → split into chat/model.go + chat/update.go
internal/tui/messages.go             → kept verbatim (contracts)
internal/tui/viewport.go             → items.go (spike model)
internal/tui/render.go               → items.go (Render method per type)
internal/tui/tree.go                 → header.go (header tree)
internal/tui/activity.go             → DELETED
internal/tui/activity_stream.go      → DELETED
internal/tui/statusbar.go            → status.go (kept shape)
internal/tui/shorthelp.go            → kept
internal/tui/question.go             → modal_question.go (β region)
internal/tui/palette.go              → modal_palette.go (β region)
internal/tui/help.go                 → modal_help.go (γ overlay)
internal/tui/error_dialog.go         → modal_error.go (γ overlay)
internal/tui/confirm.go              → inline_confirm.go (α inline)
internal/tui/validate_popup.go       → inline_validate.go (α inline + pill)
internal/tui/banner.go               → wordmark.go (header)
internal/tui/selection.go            → DELETED (drop v/j/k/y)
internal/tui/child_stream.go         → kept verbatim
internal/tui/event_translate.go      → kept verbatim
internal/tui/protocol_mapping.go     → kept verbatim
internal/tui/tool_header.go          → folded into items.go
internal/tui/history.go              → kept verbatim
internal/tui/replay.go               → kept verbatim
internal/tui/layout.go               → simplified (no tree/activity columns)
internal/tui/view_cache.go           → narrowed (per-item cache)
internal/tui/colors.go, theme.go     → kept
internal/tui/stderr_redirect.go      → kept
internal/tui/session_backend.go      → kept verbatim
cmd/enter.go                         → minor: swap NewAppModel → new constructor
cmd/enter_notify.go                  → kept verbatim
```

Test files in `internal/tui/*_test.go` need triage:

- `app_test.go`, `app_*_test.go`, `viewport_test.go`,
  `render_test.go` → rewrite against new model
- `messages_test.go`, `event_translate_test.go`,
  `protocol_mapping_test.go`, `child_stream` tests → kept
- `selection_test.go` → deleted
- `activity*_test.go` → deleted
- `tree_test.go` → rewrite for header-tree
- All modal `_test.go` files → rewrite per modal disposition

---

## Appendix B — open questions for weave / the team

1. Are we OK losing the viewport `v`/`j`/`k`/`y` yank-as-raw-markdown
   affordance? (§3.10)
2. Is the β-grown sticky-region question modal acceptable, or do
   we prefer γ-overlay for the question modal specifically?
   (§3.5)
3. Are we OK with **no** scrollback search at cutover? (§3.9)
4. Header tree at ≥6 agents — collapse policy? Hide-non-active +
   "…" hint is my proposal but it's a UX call.
5. Do we want `/send <name> <msg>` in the palette as a forward-
   looking nicety, or strictly observe-only? (§3.3)

---

## Reflections

Things that surprised me while researching:

- **The user cannot send messages to children today.** The prompt
  framed this as an open question, but reading `app.go:1872`
  showed the input is hidden when `observedAgent != rootAgent`.
  This eliminates a chunk of UX design work and clarifies the
  contract: weave is the only thing the user converses with.
- **There is no scrollback search.** The "searchOverlay" the
  prompt referenced is reverse-i-search of *input history*. The
  redesign doesn't have to preserve a feature that doesn't exist.
- **The activity panel is mostly redundant with the viewport**
  for the observed agent. Killing it is straightforward in a way
  the prompt didn't quite acknowledge.
- **The question modal's hard-vs-soft dismiss** (QUM-611) is more
  subtle than it looks. Hard cancel unwedges an in-flight MCP
  call via `Supervisor.CancelQuestion`; soft hide keeps drafts.
  The redesign must thread the `Hard` bit through the new modal
  plumbing or it will silently break recovery.

Open questions I would chase if I had more time:

- Is there a clean way to render `delegate` / `spawn` results as
  *previews of the child's first output* inline? That would give
  the user a reason to look at weave's transcript instead of
  immediately Ctrl+N'ing into the child.
- The validate popup's "sticky on failure" state is implemented
  manually as state-machine bookkeeping in `validate_popup.go`.
  Is there a more declarative way to express "this status sticks
  until {merge, kill}"?
- What does the redesign do about color-accessibility? The
  cyan→purple gradient is striking on truecolor terminals but
  degrades poorly on 8-color. The spike has no fallback.

Where I would start next: prototype the β-grown sticky-region
question modal in a throwaway branch. It is the single highest-
risk decision in this doc, and 4 hours of code would tell us
whether it feels OK or not. Everything else has clearer answers.
