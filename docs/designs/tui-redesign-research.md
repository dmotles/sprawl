# TUI Redesign — Design Research

**Status:** research / design notes — **decisions locked 2026-06-02** (see §3 LOCKED markers)
**Author:** ghost (research agent)
**Date:** 2026-06-02 (rev 3: header tree style locked)
**Branch:** `dmotles/tui-redesign-research`

## Revision history

- **rev 1** (2026-06-02): Initial inventory + recommendations.
- **rev 2** (2026-06-02): Locked decisions after dmotles review.
  Also folds three weave confirmations: toasts support **both**
  condition-based and user-dismiss; Ctrl+R input history is
  **persistent** across sessions with concrete spec (~10k entries,
  consecutive-dedup, mode 0600); **no `ask_user_question` bug
  audit** — Linear confirmed all 11 AUQ issues are Done.
- **rev 3** (2026-06-02): **Header tree layout locked to
  `orbital-pill`** (§3.1) — roots anchored left, children fan right
  as tokens, grandchildren as `↳` tokens; selected agent rendered
  as reverse-video cyan pill. Reference impl:
  `internal/tuichat/header.go::treeOrbitalCore` @
  `dmotles/tui-chat-spike` commit `5245510`. Tree collapse policy
  reclassified as implementation detail (orbital degrades
  gracefully at width).
  Sections marked **LOCKED** are final. Sections marked **OPEN** are
  pending a downstream input.
  Notable changes from rev 1:
  - §3.2 activity panel: **dropped entirely**, no preserved alternatives.
  - §3.6 notifications: rewritten around **toast subsystem** +
    **viewport stream contract**.
  - §3.7 lifecycle: rewritten around **overlay modal for handoff**
    + **toast for recovery/interrupt/fault**.
  - §3.10 selection: **no mouse capture in v1** — terminal/tmux own
    selection. Drops both Ctrl+_ toggle and v/j/k/y mode.
  - §5 migration: locked to **incremental in-place (5b)**, no
    parallel `internal/tui2/` tree.
  - New §5.5: discrete infrastructure pieces to file as Linear issues.

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

### 3.1 Multi-agent observation — **LOCKED**

**Decision:** option (a) — separate per-agent transcripts;
Ctrl+N / Ctrl+P cycle the observed agent; `/observe <name>` palette
command for explicit jumps. When observing a non-root agent the
input row swaps to a dim "observing &lt;name&gt; — Ctrl+N back to
weave" hint (no textinput, matches today's `app.go:1872` behavior).

**Constraint already established by today's code:** the user
**cannot send messages to a child agent from the TUI** — children
only receive messages via weave's MCP `send_message`. Preserve that
contract (see §3.3).

**Header tree layout — LOCKED: `orbital-pill`.** Picked by dmotles
from ratz's `sprawl chat --tree=<style>` visual spike.

- **Layout: orbital.** Roots anchored on the left
  (`weave ──●`, `tower ──●`); children fan to the right as tokens;
  grandchildren rendered as `↳`-prefixed tokens.
- **Selected-agent marker: reverse-video cyan pill.** Same
  treatment as the spike's minimal-glyphs variant.
- **Reference implementation:** `internal/tuichat/header.go` on
  `dmotles/tui-chat-spike`, commit `5245510`, function
  `treeOrbitalCore` (the pill marker lives in the same file).
  Implementer ports the rendering logic from there.

Considered + rejected: (b) interleaved single transcript with
`[finn]` prefixes (drowns weave, tool-call expand/collapse gets
ambiguous across agents); (c) tabs (extra primitive, ugly at N>4);
(d) modal-only switcher (loses ambient awareness — the header tree
is the ambient signal).

### 3.2 Activity panel — **LOCKED: DROPPED**

The activity panel goes away entirely in the new TUI. No header
embellishment, no inline-in-weave-transcript, no replacement
affordance. Users who want to see what a child is doing
`Ctrl+N` to that child's transcript.

`activity.ndjson` on disk continues to be written for debugging
and post-hoc inspection; the UI just doesn't surface it.

`internal/tui/activity.go` and `internal/tui/activity_stream.go`
are deleted as part of Phase 1 of the migration (§5).

### 3.3 Sending a message to a child — **LOCKED**

No such feature today; none in scope for v1. The TUI is
observe-only when looking at a child. If we ever add it, the
palette `/send <name> <msg>` slot is reserved — but this is
not v1 work.

### 3.4 MCP tool calls weave makes — **LOCKED**

Tools like `spawn`, `merge`, `retire`, `send_message`, `delegate`,
`ask_user_question` render as **collapsed tool-call items** in
weave's transcript — identical to any other tool call. The spike's
`toolCallItem` collapses to a one-liner and expands on Ctrl+O. No
special styling. The header tree already shows the side-effects
(new agent appears, fault badge, status-dot transitions).

**Important nuance — `/handoff` slash-command vs `handoff` MCP
tool call (do not conflate):**

- **`/handoff`** is a **user-facing slash command** in the palette.
  It dispatches a `InjectPromptMsg` that injects a prompt-template
  string *as if the user typed it*. The user sees it render as
  their own message in the transcript (`userItem`-style). It is
  not a tool call.
- **`handoff`** (no slash) is the **MCP tool weave invokes** as the
  actual handoff mechanism — wired through
  `internal/rootinit/postrun.go`, `Supervisor.HandoffRequested()`.
  This renders as a tool-call item in weave's transcript, just
  like any other MCP tool.

Both paths must exist; both must render correctly. They are
different actors (user vs weave) with different render rules.

**`ask_user_question` exception (preserve today's behavior):** its
tool-call result is gated on the question modal closing. The
tool-call entry renders in the transcript but its "result" body
must **not** duplicate the user's answer back into the chat — the
question modal already consumed that interaction. Today this
works; preserve.

### 3.5 Modals under altscreen — **LOCKED (with spike-validate gate per modal)**

Placement model (three flavors):

- (α) **Inline block** in the transcript that the user interacts
  with, then collapses to a normal item.
- (β) **Grow the sticky region** at the bottom temporarily (replace
  the 1-line input with an N-line form).
- (γ) **Centered altscreen overlay** painted on top.

| Modal | LOCKED placement | Rationale |
|---|---|---|
| Help (F1/?) | **γ overlay** | Stateless reference; doesn't mutate session; users expect overlays for help |
| Palette (Ctrl+/) | **β grow sticky region** | Palette already replaces the input semantically; expanding the sticky region is more honest than a floating box |
| Error dialog (session-fatal) | **γ overlay** | Blocks all interaction; centered modal is the only safe affordance |
| Confirm (Ctrl+C quit) | **α inline** "[y/n] Quit?" system message at the bottom of scrollback, keyboard-focused | Single question, two answers; pinning inline keeps history honest |
| Question (`ask_user_question`) | **β grow sticky region** | Interactive (cursor + multi-pick + custom text + per-question tabs); supports soft-hide + Ctrl-Q reopen; α inline would break reopen (scrolls away); γ overlay loses ambient affordance |
| Validate popup | **α inline** for streamed output + small **status-bar pill** for in-flight | See also §4 tweak: validate-failure popup is a small overlay near the merge/retire trigger, **not** a fullscreen takeover, auto-dismiss on validate completion. |

**Per-modal spike-validate gate (LOCKED).** Each modal placement
gets a small visual test before its phase ships. Phase 3 (modal
migration in §5) does not advance a modal until its visual spike
has been eyeballed and signed off. Spike artifacts live alongside
ratz's visual spikes as a sibling.

**Modal hierarchy.** Today: help > error > confirm > palette >
question > validate. Preserve this with the caveat that β-style
modals (palette, question) live in the sticky region and γ-style
(help, error) live on top. We should never have *both* β and γ up
at the same time; if an error arrives while the palette is open,
the palette closes first (today's behavior).

### 3.6 Notifications — **LOCKED (new model: toasts + viewport stream contract)**

This section is rewritten from rev 1. The model is now:

**The Viewport Stream Contract (named principle, LOAD-BEARING).**
The viewport renders **only** two kinds of content:

> **(a) things sent *to* the agent**, and
> **(b) things the agent responded *with*** (including tool-call
> inputs/outputs).

Everything else — system meta, harness chatter, MCP slow-warnings,
faults, recoveries, interrupt acknowledgements, "session is
restarting" status — goes through a separate **toast** or
**overlay** layer. This contract is the design's spine; deviations
from it should be treated as bugs.

**Toast subsystem.** Toasts are overlay (non-stream) notifications
for things the *agent itself* doesn't see — i.e. for the human
operator. The subsystem is new infrastructure to be built (see
§5.5 issue list).

**Dismissal model (LOCKED, dual):** every toast supports **both**
condition-based clearing **and** user-dismiss. The two are not
exclusive — a toast persists until **either** its underlying
condition clears **or** the user explicitly dismisses it,
whichever happens first. User-dismiss is a single keybinding
(TBD, e.g. Esc while no modal is up, or a small dismiss-toast
chord) applied to the most-recent toast.

Per-toast clearing conditions:

| Toast | Cleared when (condition) | Also user-dismissible? |
|---|---|---|
| MCP slow-warning ("MCP call running > 60s: weave/spawn") | MCP call returns OR calling agent dies | yes |
| Backend fault ("finn: backend fault (oom)") | agent is recovered (BackendFaultClearedMsg) OR retired | yes |
| Interrupt issued ("interrupt sent to weave") | Harness injects the interrupt into the agent (i.e. the agent observes it and starts processing — at which point the viewport shows the agent's real response) | yes |
| Rate-limit hit | Backoff window expires OR turn completes | yes |
| Recovery startup ("recovered N agents") | **Timer-based exception** — informational, not state-bound. Auto-dismiss after ~5s. | yes (the user-dismiss is the primary affordance; the timer is a fallback) |

**Per-event mapping under the new model:**

| Event | Today's render | LOCKED render |
|---|---|---|
| Inbox arrival (message *to* weave) | AppendStatus + tree-badge | **In viewport** — it's content sent to the agent (matches contract). Unread badge on header tree for non-observed agents. |
| Cost/token update | Status-bar field | Status-bar field, unchanged |
| Rate-limit hit | Transport error → error dialog | **Toast** + status-bar pill until backoff clears |
| MCP op > 60s threshold | Banner in viewport + status-bar pill | **Toast** until MCP returns; status-bar pill unchanged |
| Backend fault on child | Tree FAULT badge + status-bar banner | **Toast** + header tree FAULT badge; toast clears on recovery |
| Backend recovered | Banner + tree-badge clear | **Toast** clears (no new toast needed) — header tree dot reverts; absence of fault is its own signal |
| Auto-continue triggered | AppendAutoTrigger banner | Currently borderline: it's *sort of* a message-to-agent (the harness injects a continuation prompt). Render as a normal user-prompt-style item in the viewport (matches contract). |
| Validate failure | Banner in viewport + sticky popup | **Small overlay near the merge/retire trigger** (per §4); auto-dismiss on completion. No fullscreen takeover. |

**Anti-pattern.** "Sending interrupt…" / "Interrupt queued" /
"Session restarting…" banners in the viewport are **out** —
they violate the contract. Those events surface as toasts or
overlays (§3.7).

**Toast subsystem is its own engineering project.** See §5.5.

### 3.7 Lifecycle — **LOCKED (rewritten around overlay + toast model)**

All paths in this section honor the §3.6 **Viewport Stream
Contract**: viewport shows what the agent sees or says;
lifecycle/system meta surfaces as overlay or toast.

**Handoff flow (LOCKED).** Introduces a new agent lifecycle state
("standby for restart") — see §5.5 issue list.

1. Weave invokes the **`handoff` MCP tool** (the actual mechanism,
   not the `/handoff` slash-command).
2. The MCP tool returns "handoff completed" to weave. Weave enters
   the new **standby for restart** lifecycle state. Weave knows
   not to start new work.
3. On weave's next yield (turn end), a **floating overlay modal**
   appears with the rest of the UI still visible underneath.
4. Modal contents: animated progress + status messages
   ("consolidating sessions…", "writing handoff…", "restarting…").
   Sourced from today's `ConsolidationPhaseMsg` /
   `ConsolidationCompleteMsg` events.
5. Modal dismisses when restart completes; the new session takes
   over rendering. No inline "Session restarting…" banner in the
   viewport (that violated the contract).

**Recovery flow (LOCKED).** Startup **toast**, *not* inline banner.
"Recovered N agents." Timer-based dismissal (§3.6 exception).

**Interrupt flow (LOCKED).**
1. User presses Esc mid-turn.
2. **Toast** appears: "interrupt sent" — acknowledges the user's
   action.
3. **Nothing in the viewport yet.** No "Interrupting…" or
   "Interrupt queued" — those are system meta and violate the
   contract.
4. When the harness injects the interrupt into the agent and the
   agent observes it, the agent's actual response renders in the
   viewport as normal stream content.
5. The toast clears when the harness completes injection.

**Terminal error / backend fault.** Toast (per §3.6). Fatal
session error still escalates to a γ-overlay error dialog with
[r]estart / [q]uit.

**EOF (graceful session end).** Auto-restart via `SessionErrorMsg{io.EOF}`
→ `RestartSessionMsg`. The transition surfaces as a brief toast
("Session ended — restarting…"); the new session banner is the
visible signal that restart succeeded.

**Sub-agents — Claude `Task` tool (NOT sprawl children).** The
Claude harness's own `Task` tool spawns sub-instances inside a
single agent's session. These are *not* sprawl agents and have no
tree presence. Implementer picks one of:

- (a) **Flatten** — render the `Task` tool plus all of its inner
  tool calls as nested entries (current behavior, today's
  QUM-379/386 nesting machinery), OR
- (b) **Opaque** — render just the `Task` tool call with no inner
  detail.

Both meet the viewport contract; pick whichever is simpler at
implementation time.

**`ask_user_question` works as currently specified.** Linear
audit confirmed no open AUQ bugs (all 11 AUQ-related issues are
Done as of rev 2). Design proceeds assuming today's behavior is
correct: tool-call item renders in the transcript; question
modal owns the interaction; result body does not duplicate the
user's answer into the chat (§3.4); soft-hide / hard-cancel work
per QUM-611.

**Question wedge (QUM-611).** When `Real.Recover` proactively
`cancelByAgent`s a wedged question, the TUI sees `CancelQuestionMsg`
followed by a fresh `QuestionsAvailableMsg`. Render: brief toast
"question reset (recovery)" then re-show modal. Unchanged from
rev 1 in behavior; just re-routed through the toast layer
instead of an inline banner.

**Drain/inject (QUM-555, QUM-619).** Inbox drained on idle →
injected as `<system-notification>` wrappers. These **stay in the
viewport** because they meet the contract: they are content sent
*to* the agent. The envelope-strip code in `messages.go` ports
verbatim.

### 3.8 Resize edge cases — **LOCKED (adopt rev 1 plan as-is)**

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

### 3.9 Search — **LOCKED**

- **Input-history reverse-search (Ctrl+R).** YES. Keep. (Note:
  this searches input history, *not* viewport content — the rev 1
  doc clarified that there is no scrollback search today.)
- **Viewport grep.** NO. Just let the user scroll. Not in scope
  for v1; not a follow-up either unless users explicitly ask.

**Persistence (LOCKED): persistent across sessions.** Not
session-bound. Match shell muscle memory.

Concrete spec:

- **On-disk location.** `.sprawl/input-history` (already where
  today's code writes) or under `.sprawl/memory/` — implementer's
  choice between the two; either is sensible. Mode 0600.
- **Append-only** semantics. New entries appended; old entries
  stay until trim.
- **Retention: ~10k entries**, trimmed in a rolling window from
  the head (oldest dropped first when the cap is exceeded).
- **De-duplicate consecutive duplicates.** If the user submits
  the same string twice in a row, only one entry lands. (Non-
  consecutive duplicates are kept; matches shell behavior.)
- Load path must be **stable across session restarts** so handoff
  loops preserve history.

File this as its own issue (§5.5).

### 3.10 Mouse capture / selection — **LOCKED**

**v1 has NO mouse capture at all.** Terminal/tmux own mouse
entirely. Reason: the primary developer SSHs from a laptop and
wants clipboard to live in the local viewer's OS, not on the
remote host. Mouse capture inverts that and is a frequent
papercut.

Consequences:

- The Ctrl+_ / Ctrl+/ selection-mode toggle (QUM-617) is **removed**
  — there's nothing to toggle.
- The viewport `v`/`j`/`k`/`y` own-selection mode (QUM-281) is
  **removed**.
- The TUI provides **keyboard scroll shortcuts** for the viewport:
  PgUp / PgDn / Home / End / Up / Down (and ergonomic alternatives
  on terminals that consume those). Wheel scroll is handed off to
  the terminal emulator natively.
- Crush-style OSC 52 own-selection is **future-maybe, not v1**.
  Same SSH downside; can be revisited if the keyboard scroll
  story turns out to be insufficient.

`internal/tui/selection.go` is deleted as part of Phase 1.
`CLAUDE.md`'s "Text selection in `sprawl enter` (QUM-617)" section
will need to be updated to reflect the new model when v1 ships
(file note for handoff).

---

## 4. Edge cases & complications uncovered

While reading `messages.go`, `app.go::Update`, and the supervisor
event paths I found several non-obvious things that complicate
the redesign:

1. **The pending-submit queue is a single slot, not a queue**
   (`pendingSubmit string`, app.go ~line 200). The behavior is
   "last Enter wins". Esc cancels and — per QUM-576 — refuses to
   clobber a non-empty input. Spike has no queue at all today.
   **v1 keeps single-slot.** Render the pending submit as a
   hover-above-prompt indicator (the muted "⏸ queued: …" pattern
   today, just rendered cleanly above the input row). A real
   multi-message queue is future scope, not v1.
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
   **Copy/paste perf is an explicit regression-watch item.** It
   took deliberate work to get right (QUM-451, QUM-608); v1 must
   not regress it. Validate via the `paste-coalesce` mandatory-test
   matrix row before flipping default.
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
    **v1 placement (per §3.5/§3.6):** small overlay near the
    merge/retire trigger; auto-dismiss on validate completion;
    **not** a fullscreen takeover. Failure stickiness is preserved
    until the next merge/kill.
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

## 5. Migration plan — **LOCKED: incremental in-place (5b)**

Rev 1 proposed two paths: (5a) promote `internal/tuichat/` to a
parallel `internal/tui2/`, soak behind `sprawl enter --new`, then
flip; or (5b) refactor `internal/tui/` in place, phase by phase,
shipping each phase as the default.

**Decision: (5b) incremental in-place.** Rationale:

- Each phase is small, observable, and **daily-driver-testable**
  the moment it merges.
- **Bail-out** is possible at any phase — partial progress is
  partial value.
- (5a) concentrates soak risk at cutover day; (5b) distributes it.
- "I'm using it daily" is a stronger forcing function than
  "we'll cut over when X is done."
- Slower in calendar time, but probably **faster to a stable
  daily-driver-quality TUI** because each phase is shipped + dogfooded.

### Locked phase order

Each phase is a Linear issue + branch + PR. Each phase must:

- pass `make validate`,
- pass the relevant mandatory-test e2e matrix rows (see CLAUDE.md
  table),
- be visually signed off (per-modal spike gate in §3.5 applies to
  modal phases),
- be daily-driven by dmotles for ≥48h before the next phase opens
  a PR.

| # | Phase | Notes |
|---|---|---|
| 1 | **Rip out activity pane + simplify tree** | Smallest, safest. Deletes `activity.go`, `activity_stream.go`, drops the column from `layout.go`, simplifies `tree.go`. No new behavior — pure reduction. Matrix row at risk: `notify-tui` (must still surface child status changes via tree). |
| 2 | **Port wordmark header into existing TUI** | Already tracked as **QUM-646**. Bring the spike's 3-line SPRAWL wordmark + cyan→purple gradient + `orbital-pill` header tree (§3.1) into `internal/tui/banner.go` / `tree.go`. Tree rendering ports from `internal/tuichat/header.go::treeOrbitalCore` @ `dmotles/tui-chat-spike` commit `5245510`. |
| 3 | **Replace viewport with chat-style list + Expandable items (Ctrl+O)** | The biggest single change. `viewport.go` + `render.go` → item-list model from the spike. Ctrl+O expand/collapse. Glamour rendering ports. Per-agent buffers preserved. Backfill epoch preserved. Matrix rows at risk: most TUI-touching rows. |
| 4 | **Toast notification subsystem (new infra)** | New file(s): `toast.go`. Condition-based dismissal subscribing to lifecycle events. No user-visible change until phase 5 wires events into it. See §5.5 issue. |
| 5 | **Lifecycle pieces: handoff overlay, recovery toast, interrupt UX** | Wire `HandoffRequestedMsg` to the new overlay modal (§3.7). Wire `BackendFaultMsg` / `MCPCallStartedMsg` / recovery to toasts. Remove "Sending interrupt…" inline banner. Matrix rows: `handoff`, `recover-live`, `idle-interrupt-inject` all sensitive here. Introduces "standby for restart" agent state — see §5.5 issue. |
| 6 | **Modal migration: palette, help, validate-popup, error, confirm, question** | Each modal moves to its locked placement (§3.5) with a small visual spike before the PR opens. Order within phase: help → confirm → error → palette → validate → question (hardest last). The `ask-user-question` mandatory-test row will likely flake until question modal lands; coordinate. |
| 7 | **Mouse capture removal + keyboard scroll bindings** | Delete `selection.go`, remove Ctrl+_ / Ctrl+/ handlers, wire PgUp / PgDn / Home / End / arrow keys for viewport scroll. Update `CLAUDE.md` text-selection section. |
| 8 | **Ctrl+R input reverse-search (re-spec)** | Already exists today; phase 8 is "do we change anything?" — likely keep it; just decide persistent-vs-session-scoped (§3.9 — leaning persistent). See §5.5 issue. |
| 9 | **Cleanup: delete deprecated code paths** | Mop-up. Anything not deleted in earlier phases (dead message types, unused state fields) goes here. Update `CLAUDE.md` mandatory-test row paths if files moved. |

**Order may shift if a dependency forces it.** For example, if the
toast subsystem (phase 4) blocks lifecycle work (phase 5), 4
*must* land first. If the wordmark header (phase 2) can land before
or after the activity-pane removal (phase 1) without conflict,
order is flexible. The constraint is: **phase 5 (lifecycle) cannot
land before phase 4 (toast)**, and **phase 6 (modals) should not
land before phase 3 (chat-style list)** because confirm/validate
need the inline-item model.

### What does NOT happen under 5b

- No `internal/tui2/` parallel tree. No `sprawl enter --new` flag.
  No "soak then flip" cutover day.
- No giant atomic rewrite PR. Every phase is a normal PR.
- No long-lived feature branch. Everything merges to main as it
  lands.

### Risk vs rev 1

(5a) had two safety nets (flag + parallel tree). (5b) has fewer,
so each phase needs more discipline:

- **Mandatory-test e2e matrix rows are the safety net.** Every PR
  in this plan must run them.
- **Per-modal visual spike (§3.5)** is the second net for modals
  specifically.
- **48h daily-drive** is the third net — paper-cuts surface before
  the next phase opens.

---

## 5.5 Discrete infrastructure pieces — file as separate Linear issues

These are pieces of work surfaced by the redesign that warrant
their own tracking issues (do not file the issues yet; this is
just the list).

1. **Toast notification subsystem.**
   New infra. Condition-based dismissal subscribing to lifecycle
   events (MCP-op return, agent recovery, etc.). One timer-based
   exception: recovery startup toast. Renders as overlay
   (non-stream). See §3.6.
2. **"Standby for restart" agent lifecycle state.**
   New agent state introduced by the handoff flow (§3.7). Agent
   enters standby after the `handoff` MCP tool returns; exits
   when restart completes. Used to gate the handoff overlay
   modal and prevent new turns from starting mid-restart.
3. **Ctrl+R input reverse-search (persistence).**
   Exists today as a per-session affordance; this issue makes it
   **persistent across sessions** per §3.9. Concrete spec:
   `.sprawl/input-history` (or `.sprawl/memory/`), mode 0600,
   append-only, ~10k rolling retention, de-duplicate consecutive
   duplicates, load path stable across session restarts.
_(rev 2 dropped: a conditional "ask_user_question bug audit" entry.
weave confirmed no open AUQ bugs; design assumes today's behavior
is correct.)_

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
remains the multi-agent UX (LOCKED §3.1).** If users push back
hard ("I want to chat with finn directly") we will need to add
`/send <name>` and revisit. Out of v1 scope.

**Load-bearing assumption #3: β-modals (palette, question) inside
the sticky region won't be visually confusing.** The question
modal can be 10+ lines of form; that's a lot of "input region".
**Mitigation locked in §3.5: per-modal spike-validate gate before
each modal phase ships.** If the visual spike for the question
modal feels wrong, fall back to γ overlay for that modal
specifically.

**Load-bearing assumption #5 (NEW, rev 2): the viewport stream
contract (§3.6) is enforceable.** The model assumes every
event/message can be cleanly bucketed as "agent-visible content"
vs "system meta." If any event sits ambiguously between the two
(today's `AutoContinueMsg` and inbox-drain `<system-notification>`
wrappers are the borderline cases) we may end up either
violating the contract or rendering important information only
in toasts the user might miss. *Mitigation*: enumerate every
`*Msg` type in `messages.go` against the contract during phase 4
or 5 and document the bucket explicitly.

**Load-bearing assumption #6 (NEW, rev 2): condition-based toast
dismissal is implementable for every state-bound toast.** This
requires each toast's clearing condition to actually fire as an
observable event. MCP-call-end is fine; "calling agent dies" is
fine (we have agent-state transitions). But "rate-limit window
expires" may need a new timer/event we don't currently emit.
*Mitigation*: validate per-toast during phase 4.

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
- The header tree is information-dense at high agent counts. The
  `orbital-pill` layout (§3.1, rev 3) degrades gracefully at width
  per ratz's spike notes; collapse policy when overflow does
  occur is implementer's call (e.g. hide non-active branches
  behind a "…" hint).

---

## 7. Locked decisions summary

1. Adopt the spike's render model as the floor.
2. **Drop the activity panel entirely** (§3.2). No replacement.
3. **Drop the multi-column layout**; the `orbital-pill` header
   tree (§3.1) is the only multi-agent affordance.
4. **Ctrl+N/P + observed-agent input-hide** is the multi-agent UX
   (§3.1). Observed agent rendered with reverse-video cyan pill.
5. **`/handoff` slash-command (user-injected prompt template) is
   distinct from `handoff` MCP tool call (weave's mechanism)** —
   render differently (§3.4).
6. **Modal placements (§3.5)**: β grow-sticky for palette +
   question, γ overlay for help + error, α inline for confirm +
   validate. Per-modal spike-validate gate before shipping.
7. **Viewport stream contract (§3.6, LOAD-BEARING)**: viewport
   only renders agent-visible content. Everything else is toast
   or overlay.
8. **Toast subsystem** with **dual dismissal** — condition-based
   *and* user-dismiss (whichever fires first). One timer
   exception: recovery startup toast. New infra (§5.5 issue).
9. **Handoff is a floating overlay modal** that appears at
   weave's next yield after the `handoff` MCP returns (§3.7).
   Introduces "standby for restart" agent state (§5.5 issue).
10. **Interrupt UX**: toast acknowledges Esc; viewport stays
    quiet until the harness injects the interrupt and the agent
    responds for real (§3.7).
11. **NO mouse capture in v1** (§3.10). Drop Ctrl+_ toggle, drop
    `v`/`j`/`k`/`y`. Keyboard scroll shortcuts only.
12. **Ctrl+R input reverse-search** preserved, **persistent**
    across sessions; `.sprawl/input-history` (or `.sprawl/memory/`),
    mode 0600, append-only, ~10k rolling retention, dedupe
    consecutive duplicates (§3.9; §5.5 issue).
13. **No viewport grep / scrollback search**, not even as future
    scope unless users ask (§3.9).
14. **Migration: incremental in-place (5b)**. 9-phase order
    locked in §5. No `internal/tui2/`, no `--new` flag, no
    parallel tree.
15. **Keep `messages.go` types stable across phases** — that's
    how the mandatory-test e2e matrix rows survive (§4 item 15).

### OPEN (after rev 2)

_(rev 3: header tree style + collapse policy are now resolved —
`orbital-pill` layout locked, see §3.1. No design-level opens
remain at the layout layer.)_

---

## Appendix A — file map for the redesign

Refactor targets in `internal/tui/` (in-place per §5b — no
`internal/tui2/`):

```
internal/tui/app.go                  → keep file; refactor Update/View phase-by-phase
internal/tui/messages.go             → KEPT VERBATIM (contracts; mandatory-test safety net)
internal/tui/viewport.go             → rewritten as item-list model (phase 3)
internal/tui/render.go               → folded into per-item Render methods (phase 3)
internal/tui/tree.go                 → header tree (phase 1 + 2)
internal/tui/activity.go             → DELETED (phase 1)
internal/tui/activity_stream.go      → DELETED (phase 1)
internal/tui/activity_stream_test.go → DELETED (phase 1)
internal/tui/activity_test.go        → DELETED (phase 1)
internal/tui/statusbar.go            → kept shape; drop sidebar-related fields
internal/tui/shorthelp.go            → kept
internal/tui/question.go             → β grow-sticky modal (phase 6)
internal/tui/palette.go              → β grow-sticky modal (phase 6)
internal/tui/help.go                 → γ overlay modal (phase 6)
internal/tui/error_dialog.go         → γ overlay modal (phase 6)
internal/tui/confirm.go              → α inline (phase 6)
internal/tui/validate_popup.go       → α inline + pill, small-overlay-near-trigger (phase 6)
internal/tui/banner.go               → wordmark (phase 2, QUM-646)
internal/tui/selection.go            → DELETED (phase 7)
internal/tui/selection_test.go       → DELETED (phase 7)
internal/tui/child_stream.go         → kept verbatim
internal/tui/event_translate.go      → kept verbatim
internal/tui/protocol_mapping.go     → kept verbatim
internal/tui/tool_header.go          → folded into items (phase 3)
internal/tui/history.go              → kept; re-spec for persistent (phase 8)
internal/tui/replay.go               → kept verbatim
internal/tui/layout.go               → simplified (phase 1: no activity col)
internal/tui/view_cache.go           → narrowed (per-item cache, phase 3)
internal/tui/colors.go, theme.go     → kept
internal/tui/stderr_redirect.go      → kept
internal/tui/session_backend.go      → kept verbatim
internal/tui/toast.go                → NEW (phase 4)
cmd/enter.go                         → keep; minor wiring per phase
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

## Appendix B — remaining open questions (rev 2)

Most rev-1 open questions are locked. As of rev 3 the header-tree
layout + collapse policy are also locked (orbital-pill, §3.1).
What remains:

1. **User-dismiss-toast keybinding** — Esc-when-no-modal? A
   dedicated chord? Implementation-detail pick, not a design
   question (§3.6).
2. **Header tree collapse policy at high agent count** — the
   orbital layout degrades gracefully at width per ratz's notes,
   so collapse policy is now an **implementation detail** rather
   than an open design question. Implementer picks at code time.

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

## Reflections — rev 2 (post-locking)

What I learned from the locking pass:

- **The viewport stream contract (§3.6) is the most important
  idea in the rewrite.** Once I had it written down I could see
  rev 1's confusion: rev 1 wanted to inline-render lifecycle
  banners *and* keep them as ambient affordances — which meant
  the viewport became a mish-mash of "what the agent saw" plus
  "what the harness was doing." The contract makes the split
  load-bearing and tells us exactly what goes where. Future TUI
  decisions can be checked against it.
- **Toast subsystem is more than I expected.** I had pictured a
  one-off helper. Condition-based dismissal subscribed to
  lifecycle events is a real engineering project — it needs
  agent-state subscription, MCP-call-state subscription, and a
  small render layer with its own z-ordering vs modals. That's
  why it's its own §5.5 issue.
- **"Standby for restart" is a real new agent state**, not just
  a TUI flag. The agent has to know not to start work; the
  supervisor has to know not to feed it work; the TUI uses the
  state to decide when to show the overlay. Cross-cutting.
- **Dropping mouse capture entirely** removes a surprising amount
  of code (selection.go, the Ctrl+_ toggle, the v/j/k/y mode,
  parts of `view_cache.go`'s key, the cell-motion enable in
  `cmd/enter.go`). Phase 7 is going to feel great.
- **Incremental in-place (5b) is the right call.** Writing out
  the 9-phase order made it obvious — each phase is a normal-
  sized PR, each is shippable, each lands a real improvement.
  The (5a) parallel-tree-with-flag plan was me over-engineering
  the safety net.

What I'd still chase if I had time:

- Enumerate every `*Msg` type in `messages.go` against the
  viewport stream contract (§3.6) and tag each as "viewport / toast
  / overlay / status-bar." That catalog would make phase 3 + 4 +
  5 mechanical. It's the next thing I'd produce.
- Visual sketches of the handoff overlay modal — what does
  "consolidating sessions…" actually look like rendered? The
  animated progress could be a spinner or a phase indicator; the
  choice affects how cluttered the overlay feels.
