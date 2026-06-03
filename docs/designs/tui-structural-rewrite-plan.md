# TUI Structural Rewrite — Vertical-Slice Plan

**Status:** rev 2 — open questions resolved, empirical evidence
folded in; ready to file as Linear issues.
**Author:** ghost
**Date:** 2026-06-03 (rev 2)
**Branch:** `dmotles/tui-structural-rewrite-planning`
**Successor to:** `docs/designs/tui-redesign-research.md` (rev 3) — Phase 3
of that plan is what this document re-decomposes after the QUM-650/658/659
cutover was reverted.

**Rev 2 changes:**
- All 7 open questions resolved by dmotles; §5 reworked from
  "Open questions" to "Resolved decisions."
- trace's QUM-667 forensics on dmotles' qba session empirically
  confirm §2.2's per-item-cache hypothesis (`renderAndUpdate` 50–
  200ms / call; sprawl enter 24.9% CPU idle). Folded into risks
  §4.1, S0 scope compressed, tactical pre-fix added (see §3 S0+
  preamble and §7).
- S7 (ThinkingItem promotion) elevated from optional to required.

> **Scope.** This doc plans the Item-based render architecture only. The
> visual chassis (QUM-661, QUM-664), wordmark header (QUM-646), orbital tree
> (QUM-657), activity pane removal (QUM-648), and glamour markdown are
> already in production and are **not** slice goals. Modal migration,
> toast subsystem, and lifecycle (handoff overlay, recovery toast) are
> downstream arcs and are **not** scoped here.

---

## 1. Subtraction inventory — what's already done

Counting these as done so the slice arc does not re-describe them as work:

| Capability | Lives at | Status |
|---|---|---|
| Terminal-native background, no panel borders | `internal/tui/theme.go`, `internal/tui/view_cache.go` | shipped (QUM-661) |
| Spike palette, user `›` chevron, input `▌` gutter, inline placeholder, multi-row growth | `internal/tui/theme.go`, `internal/tui/app.go` input render | shipped (QUM-664) |
| SPRAWL wordmark header | `internal/tui/banner.go` | shipped (QUM-646) |
| Orbital-pill agent tree in header | `internal/tui/tree_orbital.go` | shipped (QUM-657) |
| Activity pane removed | (deleted files) | shipped (QUM-648) |
| Glamour markdown for assistant text | `internal/tui/viewport.go:669` (`m.renderer.Render`), `internal/tui/render.go` | shipped |
| Ctrl+O expand-tool-inputs (global toggle) | `internal/tui/app.go:493-504`, `internal/tui/viewport.go:438` (`SetToolInputsExpanded`) | shipped (QUM-335) |
| Per-agent buffers + backfill epoch + nested-Agent depth | `internal/tui/app.go` `agentBuffers`, `seenToolIDs`; `viewport.go` `renderAgentContainer` | shipped (QUM-334/379/386/439/479) |
| System-notification envelope strip (one-per-call peel) | `internal/tui/messages.go` `stripSystemNotificationTag`, `viewport.go:491` `AppendSystemNotification` | shipped (QUM-557/562) |
| Mandatory-test e2e matrix | `scripts/e2e-matrix.sh`, table in `CLAUDE.md` | shipped |

**What this rewrite is NOT:**

- Not a glamour port — already done.
- Not a Ctrl+O semantics change. **Locked: keep "expand all" global
  behavior.** The spike's "toggle most-recent expandable only" model is
  rejected. Per-item expanded state is fine *internally*, but the user-
  facing key still flips every tool-call item at once.
- Not modal migration (palette / question / help / error / confirm /
  validate). Tracked in the redesign research doc §3.5 as a separate
  arc.
- Not toast subsystem or lifecycle rerouting. Tracked in research §3.6 /
  §3.7 + §5.5 issues.
- Not mouse-capture removal (research §3.10).
- Not Ctrl+R persistence (research §3.9).
- Not multi-agent UX rework — Ctrl+N/P + observed-agent input-hide
  already works; the rewrite preserves it verbatim.

The rewrite's job is purely the **render-model substrate** — the thing
Phase 3 tried to land in one shot and could not.

---

## 2. Architectural target

### 2.1 The Item interface

Modeled on the spike (`internal/tuichat/items.go`) but adapted to
production realities (per-agent buffers, nested Agent containers,
streaming chunks, system notifications, banners that must NOT render).

```go
// Item is one row in an agent's transcript.
type Item interface {
    // Render returns the wrapped, styled multi-line string for the given
    // content width. Width-stable: callers cache by width.
    Render(width int) string

    // Finished reports whether this item will ever change again. Finished
    // items are safe to memoize indefinitely. In-flight items (streaming
    // assistant text, pending tool call) return false until terminal.
    Finished() bool
}

// Expandable: implemented by ToolCallItem (and ThinkingItem if we promote
// it). The global Ctrl+O fan-out calls SetExpanded on every expandable
// in every agent's list — preserves QUM-335 semantics.
type Expandable interface {
    SetExpanded(bool)
    IsExpanded() bool
}
```

Concrete item types — minimum set to cover today's `MessageType` enum
(`viewport.go:67-130`):

| Item type | Replaces today's | Finished() rule |
|---|---|---|
| `UserItem` | `MessageUser` | true on creation |
| `AssistantTextItem` | `MessageAssistant` | true once `FinalizeAssistantMessage` fires (`viewport.go:302`) |
| `ThinkingItem` | (currently not in viewport — status-bar only) | true on receive |
| `ToolCallItem` | `MessageToolCall` (incl. Agent containers + nested) | true once `MarkToolResult` fires AND parent turn done |
| `SystemNotificationItem` | `MessageSystemNotification` | true on receive (envelope already stripped) |
| `AutoTriggerItem` | `MessageAutoTrigger` (a kind of system-injected user content per research §3.6) | true on receive |

**What is NOT in the Item set** (the viewport contract in code): no
`BannerItem`; no `StatusItem` for system meta ("session restarting…",
"interrupt sent", "backend recovered") — routed to status-bar /
toasts / tree badges; no `ErrorItem` for transport faults — becomes
γ overlay or toast per research §3.7.

The "viewport contract enforcement in code" win is: **AppendBanner,
AppendStatus, AppendError no longer exist on the ChatList API.** A test
asserts the API surface. Any future code path that wants to surface
system meta to the user has to go through a non-viewport channel.

### 2.2 Per-item render cache

The cache is the perf win. Today's `renderMessages`
(`viewport.go:638-720+`) rebuilds the full conversation string on every
`renderAndUpdate()`, which fires on every append, every tool result,
every spinner frame, every resize. For a 200-message conversation with
glamour markdown rendering every assistant block, that's the root cause
of long-conversation slowdown.

Cache shape:

```go
type cachedRender struct {
    width    int
    expanded bool  // for Expandable items
    out      string
}

type itemEnvelope struct {
    item  Item
    cache *cachedRender  // nil until first Render at known width
}
```

Rules:

1. On `Render(width)`, the envelope checks `cache.width == width &&
   cache.expanded == currentExpanded`. Hit → return `cache.out`.
2. Miss → call `item.Render(width)`, store the result.
3. **If `item.Finished() == false`, skip caching entirely.** Only
   finished items cache.
4. On `Expandable.SetExpanded`, invalidate that envelope's cache.
5. On `ChatList.SetWidth(w)`, do not invalidate — the next Render call
   per item will miss and rebuild lazily.

Net effect: in a session with 100 finished messages and 1 streaming
assistant block, a per-keypress re-render rebuilds **one** item's
string instead of 100. Glamour runs once per finished assistant block,
not once per frame.

**Edge case — assistant streaming.** `AssistantTextItem` is not
finished until `FinalizeAssistantMessage`. While streaming, each
`AppendAssistantChunk` re-renders that item (uncached) but every other
item hits the cache. This matches the spike's "render in-flight, cache
finished" intuition without ever introducing a "live region" concept.

### 2.3 Interaction with existing plumbing

The Item model replaces the *innards* of `ViewportModel`. The
**external surface** that `app.go` calls is what determines whether
slices are bounded.

Today (`viewport.go`): `AppendUserMessage`, `AppendAssistantChunk`,
`FinalizeAssistantMessage`, `AppendToolCall`, `AppendToolCallWithHeader`,
`MarkToolResult`, `AppendSystemMessage`, `AppendSystemNotification`,
`AppendAutoTrigger`, `AppendStatus`, `AppendError`, `AppendBanner`,
`SetSpinnerFrame`, `SetToolInputsExpanded`, `SetSize`, … (≈20 methods).

The rewrite **preserves the verb-set that maps cleanly to Items**
(`AppendUserMessage`, `AppendAssistantChunk`, `FinalizeAssistantMessage`,
`AppendToolCall*`, `MarkToolResult`, `AppendSystemNotification`,
`AppendAutoTrigger`, `SetToolInputsExpanded`, `SetSize`) and **routes
the contract-violating ones away** (`AppendStatus`, `AppendError`,
`AppendBanner`, `SetSpinnerFrame`). The routing of contract violators
happens slice-by-slice (not all at once) — see §3.

**Per-agent buffers.** Today `app.go` stores `agentBuffers
map[string]*AgentBuffer`, each holding a `vp *ViewportModel`. The
rewrite swaps `vp *ViewportModel` for `cl *ChatList` (new) inside
`AgentBuffer`. The map shape, the backfill epoch (QUM-439/479), and
the `seenToolIDs` dedupe (QUM-334) are kept verbatim.

**Backfill epoch.** Untouched. `child_stream.go`, `event_translate.go`,
`replay.go`, `protocol_mapping.go` — all kept verbatim. The translation
target is the ChatList API instead of the ViewportModel API; that's
the only change.

**Messages package.** `internal/tui/messages.go` types
(`HandoffRequestedMsg`, `BackendFaultMsg`, `QuestionsAvailableMsg`,
`DismissQuestionMsg{Hard}`, etc.) are not modified by this arc.
Mandatory-test rows survive because the contract types are stable.

### 2.4 Anti-pattern that broke Phase 3

`eb72cba` (canceled QUM-659 cutover) bundled in one PR: delete
`viewport.go` (1121) + `viewport_test.go` (2606) +
`viewport_lifecycle_test.go` (294) + selection files; rewire ≈80
`app.go` call sites; drop the spinner subsystem; drop the global
`toolInputsExpanded` flag (which is now re-locked, so this was wrong
anyway); drop select-mode; rewrite ≈11 test files; migrate 4 e2e
scripts. ≈4000 LOC of churn in one PR. The slice arc below
decomposes the same end-state into 6-7 daily-driver-shippable steps.

---

## 3. Vertical-slice arc

Naming convention: **S<n>** in this doc; Linear issues to be filed
after review. All slices land on `main` via normal PR; no parallel
tree.

**Preamble — QUM-667 tactical pre-fix.** trace's qba forensics
empirically confirm `renderAndUpdate` + glamour is the hot path
(§4.1, §Empirical Evidence). dmotles is suffering on a 6-day session
*today*. Decision: a **tactical pre-fix lands as a standalone PR
before S1 opens** — a per-`MessageEntry` render cache *inside the
existing `ViewportModel`*, keyed by `(width, message-hash)`. Skip the
glamour rebuild on cache hit. Estimated < 200 LOC, no architecture
change, no contract change.

The tactical fix is **not** part of the slice arc — it ships
independently. S3 will reimplement the same cache inside ChatList;
S6 deletes the tactical code along with `ViewportModel`. Justification
for the redundancy: relief now outweighs the cost of cleanup later;
S6 deletes `viewport.go` wholesale anyway, so the tactical cache
deletion is mechanical.

### S0 — Perf baseline confirmation (compressed scope)

**Scope.** ~30 min of pprof against a qba-equivalent session
(snapshot from trace if available; otherwise reproduce on dmotles'
real session). Confirm the QUM-667 hot path is still present
*after the tactical pre-fix*, capture **a baseline number** that
S3's `recover-live` gate compares against, and look for any
*other* unexpected hot spot before committing to the arc.

This is no longer an open-ended investigation — trace's evidence
already validated the central hypothesis (CPU 24.9% idle,
`renderAndUpdate` 50–200ms/call, O(N) glamour rebuild). S0 is a
sanity check + a measurement reference, not a discovery exercise.

Concrete commands:

```bash
make build
# Reproduce on a long-history session. Hold a key in the input field
# to force renderAndUpdate ticks. Capture cpu + mem profiles.
go tool pprof -top cpu.prof
go tool pprof -list 'renderMessages' cpu.prof
go tool pprof -list 'glamour' cpu.prof
```

**Daily-driver guarantee.** N/A — no code merges.

**Output.** A short findings note (≤ 1 page) under
`.sprawl/agents/ghost/findings/` capturing the baseline number that
S3 compares against, plus a yes/no on "any unexpected hot spot."

**Bail-out gate.** If a major hot spot appears that the arc doesn't
address (e.g. supervisor event dispatch), pause and replan. With
QUM-667 already in flight as the tactical pre-fix, the arc's
remaining justification is contract-in-code + per-item
addressability (per resolved Q5), which still hold.

**Budget.** ≤ 0.5 day.

### S1 — Introduce Item interface + ChatList wrapper, unwired

**Scope.** Add `internal/tui/items.go` and `internal/tui/chatlist.go`
(names match the canceled Phase 3 checkpoint for forensic continuity).
Define the Item interface, the per-item cache envelope, the item types
that map 1:1 to today's `MessageType`. Implement `ChatList.SetSize`,
`ChatList.Append`, `ChatList.Render`. **Do not** wire to `app.go`.
**Do not** delete or modify `viewport.go`.

The package compiles, tests pass, the new types are dead code on disk.

**Files touched.** New `items.go` (≤250 LOC), `chatlist.go` (≤200
LOC), `items_test.go`, `chatlist_test.go` (unit tests: wrap, prefix,
cache hit/miss, Render width, Finished gating).

**Daily-driver guarantee.** The binary is bit-for-bit unchanged in
behavior. No new wires.

**Validation gates.** `make validate`. Mandatory e2e: none triggered
(no files in the matrix table are touched).

**LOC budget.** ≤ 500 LOC including tests.

**Bail-out.** Easy. Delete the two new files; the rest of the codebase
is untouched.

### S2 — Wire ChatList alongside ViewportModel for ONE consumer path

**Scope.** Pick the simplest verb (`AppendUserMessage`) and make
`AgentBuffer` hold *both* `vp *ViewportModel` and `cl *ChatList`.
Mirror the append: every call site of `vp.AppendUserMessage` also calls
`cl.AppendUser`. **Continue to render from `vp`** — `cl` is a shadow
that exists only to prove the data flow.

Add a hidden debug invariant: in tests, after every append, assert
that `cl.Len() == vp.Len()` and that the rendered widths agree on the
user-message rows. (Goldens optional.)

**Why this slice exists.** Phase 3 fused "introduce ChatList" + "wire
it" + "delete ViewportModel" into one cutover. This slice proves the
wiring shape on a trivial verb before dangerous verbs come on line.

**Files touched.** `internal/tui/app.go` (add `cl` to `AgentBuffer`,
mirror `AppendUserMessage`), `internal/tui/chatlist.go`
(`AppendUser`, `Len`), shadow-mirror invariant unit test.

**Daily-driver guarantee.** Production rendering still flows through
`vp`. `cl` is a silent shadow.

**Validation gates.** `make validate`; mandatory e2e: `notify-tui`
(touches `app.go`), `drain-row-inject` (touches `app.go` if the user-
message path is in scope — yes it is).

**LOC budget.** ≤ 300 LOC.

**Bail-out.** Revert app.go diff + delete chatlist.go additions.

### S3 — Flip rendering to ChatList for finished items only; keep streaming/tools on vp

**Scope.** In `View()` for the chat region, call `cl.Render(width)`
instead of `vp.View()`, but **only when no item is in-flight** —
i.e. no streaming assistant block, no pending tool call. When there
is, fall back to `vp.View()`. Maintain dual append (still mirroring).

To make this work, the dual append must be expanded from S2's
user-only mirror to cover all non-system verbs: assistant chunks
(append + finalize), tool calls (append + result), system
notifications, auto-triggers. Banner/Status/Error stay vp-only —
they're contract violators that S5 will route away.

The cache contract pays off here: switching observed agent (Ctrl+N)
no longer re-runs glamour on N assistant blocks; the per-item caches
populate lazily on first Render at the new width.

**Why this shape.** It lets us validate the cache + finished-render
path on the dominant case (steady state, scrolling, switching agents)
without yet handling the harder streaming/tool lifecycle in the new
model. Streaming + tools stay on the battle-tested vp path until S4.

**Files touched.** `app.go` (`View()`, observe-agent path, Ctrl-O
fan-out iterates both `vp` and `cl`); `chatlist.go` (full Append
surface for non-system verbs: `AppendAssistantChunk`,
`FinalizeAssistantMessage`, `AppendToolCall*`, `MarkToolResult`,
`AppendSystemNotification`, `AppendAutoTrigger`,
`SetToolInputsExpanded`); new `chatlist_render_test.go` (cache hit
on second Render same width; miss on width change; invalidate on
SetExpanded); `viewport_test.go` (dual-append counter adjustments).

**Daily-driver guarantee.** Steady-state UX is the new model. Active
streaming + active tool call fall back to the proven old rendering, so
even if the new model has a streaming bug we don't yet know about,
the user sees correct output during the actual interaction.

**Validation gates.** `make validate`. Mandatory e2e:
- `notify-tui` — child status, banners surface (banners still come
  through vp; unchanged).
- `drain-row-inject` — system-notification wrappers strip correctly
  (both vp and cl must render the stripped body the same way).
- `recover-live` — replay path doesn't regress on startup time. **This
  is the slice where the original Phase 3 regression would re-surface
  if it re-surfaces.** Hard gate: measure startup time before and
  after; if it grows by >2s on a recover-live session, slice is held.

**LOC budget.** ≤ 600 LOC including test churn. The single biggest
slice; if it gets bigger than this, split it (e.g. assistant-only S3a,
tool-call-only S3b).

**Bail-out.** Revert the `View()` switch and keep the dual-append
shim. Recovery cost is one PR.

### S4 — Move streaming + tool-call lifecycle to ChatList

**Scope.** Drop the "streaming → fall back to vp" branch from S3.
ChatList renders in all cases. `AssistantTextItem` rebuilds on every
chunk (uncached; cheap because it's a single item). `ToolCallItem`
rebuilds while `Finished() == false`; flips to finished + cacheable
when `MarkToolResult` fires.

This is where the spinner question lands. Today the spinner is global
(`SetSpinnerFrame` ticks every pending tool). The rewrite has two
options:

- **(a) per-item spinner glyph** — `ToolCallItem.Render` uses ⚙/⠿/✗
  per the spike. No global ticker. Loses the synchronized pulse.
- **(b) global ticker still drives a per-item invalidate** — keep
  today's behavior but stop using `SetSpinnerFrame` to mutate
  `MessageEntry`; the tick invalidates the in-flight tool-call
  item's cache and lets `Render` pick the next glyph.

Recommend (a) for simplicity. Decision deferred to slice owner;
visible difference is purely cosmetic.

**Files touched.** `app.go` (remove streaming-fallback in `View()`;
spinner subsystem deleted (a) or rewired (b)); `chatlist.go`
(`ToolCallItem.Finished()` logic); `items.go` (per-item glyph if
(a)); extend `chatlist_render_test.go` (streaming-chunk uncached
path; tool lifecycle pending → done).

**Daily-driver guarantee.** All transcript content now flows through
ChatList. ViewportModel is still alive as the dual-append target but
its rendering is no longer used.

**Validation gates.** `make validate`. Mandatory e2e:
- `notify-tui`, `drain-row-inject` — as before.
- `recover-live` — startup time still gated.
- **TUI manual test (/tui-testing)** — required: long session, scroll
  back, switch observed agent, watch a tool call from start to
  finish. Bash tool rendering specifically (Phase 3 broke this) —
  verify `summarize("Bash", input)` produces the same one-liner as
  today's `formatToolHeader`.

**LOC budget.** ≤ 500 LOC.

**Bail-out.** Re-add the fallback branch from S3. Recovery one PR.

### S5 — Route contract violators out of the chat list

**Scope.** Remove `AppendStatus`, `AppendError`, `AppendBanner` from
the ChatList API entirely. Find every call site and reroute:

- `AppendStatus("Session restarting…")` → goes to nothing yet. **For
  this arc, route to the status bar's transient text field** (today's
  `statusbar.go` already has the surface). Toast subsystem (separate
  arc per research §5.5) supersedes this later — this slice just
  removes the viewport bleed.
- `AppendBanner(...)` — banners largely come from session lifecycle
  events. Same treatment: status-bar transient text.
- `AppendError(...)` — for transport / session faults, escalate to
  the existing γ overlay error dialog (already exists; the bleed-
  into-viewport copy is what's removed).
- `BackendFaultMsg` text — already surfaces on the tree badge today.
  Remove any inline viewport copy of the same.

Keep the dual append for these *into* `vp` only briefly during the
PR review window, then in the same slice rip out the vp calls. After
S5, `vp` no longer receives any contract-violating append.

**Optional contract test.**

```go
// In chatlist_contract_test.go:
//   - assert ChatList type does not expose AppendStatus, AppendError,
//     AppendBanner (reflection check).
//   - assert no Item type renders to a string containing certain
//     banner prefixes ("― Session restarting", "ERROR:", etc).
```

This is the slice that delivers the "viewport contract enforcement in
code" win.

**Files touched.** `app.go` (every AppendStatus/Banner/Error call
site reroutes); `statusbar.go` (add transient-text field if
missing); `viewport.go` (those Append* methods removed or
unexported).

**Daily-driver guarantee.** No banners in the viewport. The user
sees the same information surface as today, just routed differently.

**Validation gates.** `make validate`. Mandatory e2e:
- `notify-tui` — child status badge stays on the tree (no viewport
  copy).
- `handoff` — handoff status text appears on the status bar, not
  the viewport. **This is sensitive** — `HandoffRequestedMsg` /
  `SessionRestartingMsg` are the matrix-row keys. If the row asserts
  on a specific banner string in the viewport, the assertion needs
  to move to the status-bar string. Coordinate.
- `recover-live` — "Recovered N agents" no longer in viewport.
- `idle-interrupt-inject` — interrupt acknowledgement no longer in
  viewport.

**LOC budget.** ≤ 400 LOC mostly deletions.

**Bail-out.** Possible by un-deleting the routes. Costlier than S2-S4
bail-outs because matrix-row test assertions may have moved.

### S6 — Delete the dual-append shim + ViewportModel rendering surface

**Scope.** Now that ChatList is the renderer for everything and no
contract violators flow into vp, `vp.AppendUserMessage` etc. have
become dead code. Delete `ViewportModel`'s rendering helpers
(`renderMessages`, `renderAgentContainer`, `renderNestedToolCall`,
`renderToolCall`, `renderUserPromptBlock`) and the `messages`
`[]MessageEntry` slice. Delete `viewport.go` if nothing else lives
in it; delete `view_cache.go`'s now-unused per-panel cache if
applicable.

Goldens regenerate (already mostly regenerated in S3/S4; this is
the cleanup pass).

**Files touched.** `viewport.go` (major deletion — includes the
QUM-667 tactical pre-fix cache; deleted along with the file),
`viewport_test.go` (major deletion), `viewport_lifecycle_test.go`
(likely full deletion), `view_cache.go` (narrow or delete),
`render.go` (fold remaining helpers into items).

**Daily-driver guarantee.** No user-visible change. This slice is
cleanup.

**Validation gates.** `make validate`. Run **every** mandatory e2e
row (`make test-e2e-matrix`) — this is the slice where regressions
that crept in but were masked by the dual-append shim would surface.

**LOC budget.** Net **deletion** of ~1500-2500 LOC.

**Bail-out.** Hardest to bail out of. By design — this slice is the
"point of no return" for the architecture. The earlier slices were
the bail-out windows.

### S7 — ThinkingItem promotion (user-visible)

**Scope.** Promote `thinking` content blocks to `ThinkingItem` in the
chat list. Today the model's thinking text is consumed in the
status bar's `TurnThinking` state but never rendered in the
viewport. After S7, thinking blocks appear in-line in the
transcript, **collapsed by default** with the spike's
`✻ thinking (preview · ^O to expand)` shape (per
`internal/tuichat/items.go:49-56`). Respects global Ctrl+O fan-out
(QUM-335) like any other Expandable item.

**Viewport contract check.** Thinking content IS agent-emitted — it
is the model's own output token stream, just on a different content
type. It satisfies the §3.6 viewport stream contract (research
doc): "things the agent responded with." This is NOT a contract
violation; it's a previously-hidden agent-emitted surface being
made visible.

**Frame-handler delta.** In the assistant-frame path (today's
`event_translate.go` / `protocol_mapping.go`), `thinking` blocks
currently fall through to status-bar bookkeeping. S7 routes them
into `ChatList.AppendThinking(text)` instead, producing a
`ThinkingItem` envelope. Status-bar `TurnThinking` state is kept
(it's how the user knows the model is thinking *before* any text
streams) but its text payload role transfers to the item.

**Files touched.** `chatlist.go` (`AppendThinking`), `items.go`
(`ThinkingItem` with `Finished()` true on append + Expandable),
`event_translate.go` / `protocol_mapping.go` (route `thinking`
blocks to ChatList), `app.go` (drop the status-bar text payload
once routed), tests.

**Daily-driver guarantee.** Thinking text becomes visible. New
user-facing surface; manual eyeball required.

**Validation gates.** `make validate`. Matrix rows: none of the
listed rows touch thinking blocks directly, so no row gates this.
**Manual TUI gate required**: drive a prompt that produces a long
thinking block; verify collapsed render; verify Ctrl+O expands;
verify a second Ctrl+O collapses (global fan-out).

**LOC budget.** ≤ 300 LOC including tests.

**Bail-out.** Trivial — revert the routing change and the new
type. The status-bar fallback continues to work.

---

## 4. Risks list

### 4.1 The Phase 3 ~1-min startup regression + general per-event slowdown

**Empirical evidence (QUM-667, trace 2026-06-03).** On dmotles' qba
session (6-day history): `sprawl enter` sits at 24.9% CPU **idle**;
`ViewportModel.renderAndUpdate` (`internal/tui/viewport.go:628`)
takes 50–200ms per call; called on every event AND every spinner
tick; rebuilds the full conversation string through glamour every
time. This **empirically confirms** the §2.2 hypothesis about
O(N) glamour rebuild being the dominant cost.

The Phase 3 ~1-min startup regression is the same shape, amplified
by replay's bulk hydration calling `renderAndUpdate` once per
appended entry → quadratic in transcript length on startup. Phase 3
had no pprof at the time, so this was guesswork; trace's evidence
now makes it concrete.

**Mitigation, layered:**

- **Tactical pre-fix (QUM-667), ships standalone before S1.** A
  per-`MessageEntry` render cache inside `ViewportModel`, keyed by
  `(width, message-hash)`. Skip the glamour rebuild on cache hit.
  Same cache shape S3 will move into ChatList; lives in viewport.go
  until S6 deletes the file. ≤ 200 LOC. Immediate relief.
- **Structural fix (S3 + per-item cache contract).** Items don't
  `Render` until something asks them to; finished items cache;
  width-stable. Even the bulk-hydration startup path stops being
  quadratic because nothing calls Render during replay.
- **Width-0 guard (per resolved Q7).** `ChatList.Render` no-ops
  until `SetSize` is called. Prevents cache filling with garbage
  at width 0 before the first `WindowSizeMsg`.
- **Startup-time gate at S3** (`recover-live` row). Hard fail if
  startup grows by >2s vs the S0 baseline.

### 4.2 Tool-call rendering correctness (bash specifically)

**Phase 3 failure.** Bash tool rendering broke — likely because the
spike's `summarize("Bash", input)` only handles a `command` key and
gives a different output shape than today's
`formatToolHeader` / `renderToolCall` pair.

**Mitigation.**
- **S4 manual TUI test gate.** Required: drive a real bash tool call
  end to end, compare header rendering against a golden screenshot
  from today's main.
- **Item parity test.** In `chatlist_render_test.go`, for each known
  tool name (Bash, Read, Edit, Write, Glob, Grep, Agent + nested
  Agent depth, Task tool), assert the rendered header byte-for-byte
  matches the legacy `renderToolCall` output on a fixed input. This
  is a porting checklist disguised as a test.
- Don't port the spike's `summarize` verbatim. The production
  `formatToolHeader` (see `internal/tui/render.go` /
  `tool_header.go`) is the source of truth.

### 4.3 Interrupt path

**Phase 3 failure.** Interrupt broke — likely because the cutover
deleted the "Sending interrupt…" banner path AND the underlying
`Esc → Supervisor.Interrupt` wiring was tangled with the banner-append
code. With the banner code gone, the wiring went with it.

**Mitigation.** The interrupt **wiring** (Esc → supervisor) lives in
`app.go`'s key handler; the **rendering** (banner) lives in
`viewport.go`. Two different code paths. S5 touches only the
rendering side — `app.go` Esc handling stays as-is; only the
post-handler `AppendStatus("interrupt sent")` line reroutes. The
`idle-interrupt-inject` matrix row asserts on supervisor-level
delivery, not the banner string, so it passes. Manual gate on S5:
Esc mid-stream on root and on an observed child; status-bar ack;
stream cancels; next response renders.

### 4.4 Viewport contract drift (banners creeping back in)

**Risk.** Future PRs (unrelated to this arc) add a new banner type to
the viewport because it's the easiest place to put it. Six months
later the contract is mush again.

**Mitigation.**
- **S5 deletes the methods.** `ChatList` exposes no
  `AppendBanner` / `AppendStatus` / `AppendError`. A future PR that
  wants to "just add a banner" has to add a new method to ChatList,
  which will trip code review.
- **Optional contract test** in `chatlist_contract_test.go` — pin the
  exposed method set via reflection. If a new method appears, the
  test fails and the PR has to justify.
- **CLAUDE.md note**: add a section "Viewport contract" referencing
  research doc §3.6 so reviewers have a citable rule.

### 4.5 Per-agent cache memory bloat

**New risk introduced by the rewrite.** Every finished item caches
its rendered string per width. For a long-running weave with 10
agents × 200 messages each, that's 2000 cached strings. Probably fine
(strings are small, glamour output rarely > a few KB per block) but
not free.

**Mitigation.**
- Per-item cache stores **one** entry (last width). On width change
  the old entry is overwritten, not appended. Memory is O(items).
- If S0 pprof shows memory pressure at large N, add an LRU cap as
  an S7 follow-up.

### 4.6 Backfill epoch / agent-switch race

Today's epoch (`AgentSelectedMsg` increments; stale `ChildStreamMsg`
events drop). Easy to break if rewiring `app.go`.

**Mitigation.** Don't rewire the epoch. The epoch lives in `app.go`
and gates which events get dispatched to *which* AgentBuffer's
ChatList. The dispatch site is unchanged. Item construction happens
after the epoch gate.

### 4.7 QUM-666 MCP schema drift — not this arc

For completeness only: trace's forensics also surfaced
**QUM-666** — MCP tool schema drift where `peek` uses `agent` and
`retire/merge/kill/recover` use `agent_name`. This caused the "MCP
tools are broken" perception that triggered the qba forensic
exercise but is **not in the TUI arc's scope** — fix lives in
`internal/sprawlmcp/`. Listed here so reviewers don't conflate.

---

## 5. Resolved decisions (formerly: open questions)

All seven open questions from rev 1 were resolved by dmotles
during the rev-1 review pass. Kept here as history.

1. **Package boundary.** **Resolved: `internal/tui/`.** Smaller
   surface, fewer import cycles, matches Phase 3 forensic
   continuity (items.go/chatlist.go file names from QUM-658).
   Rejected `internal/tui/chatitems/` — would force a structural
   "items are pure data" enforcement, but the cost (more package
   plumbing) outweighs the discipline win.

2. **Streaming buffer ownership.** **Resolved: (a) mutating
   append.** `AssistantTextItem` owns a `text string` appended to
   by `ChatList.AppendAssistantChunk`. Matches today's
   `viewport.go:289` (`AppendAssistantChunk`) behavior verbatim,
   no supervisor changes required.

3. **Task tool nesting.** **Resolved: (a) flatten.** Task tool's
   inner tool calls render as nested `ToolCallItem`s with
   `Depth > 0` — preserves today's QUM-379/386 behavior and
   existing test expectations. Rejected: (b) opaque, (c)
   expandable-with-inner-calls. (c) was a tempting middle ground
   ghost surfaced in rev 1 reflections, but (a) is the lowest-risk
   port.

4. **ThinkingItem promotion.** **Resolved: required, S7 is no
   longer optional.** dmotles wants thinking content visible
   in-chat. ThinkingItem is **agent-emitted content** (model's own
   token stream, just a different content-block type) so it
   **satisfies the viewport stream contract** — not a contract
   violation. See S7 scope for details and the contract-check
   call-out.

5. **S0 stop condition.** **Resolved: keep going with the arc.**
   Even if pprof had shown perf wasn't the dominant win, the arc's
   remaining justifications — viewport contract enforced in code
   and per-item addressability — stand on their own. Moot now that
   QUM-667 empirically confirmed the hypothesis (§4.1), but the
   decision matters if a future S0-equivalent finds nothing.

6. **Spinner.** **Resolved: (a) per-item glyph.** `ToolCallItem`
   renders ⚙ / ⠿ / ✗ per the spike. Global spinner subsystem
   deleted in S4. Loses synchronized pulse; dmotles accepts.

7. **Width-0 first-frame.** **Resolved: yes, guard.**
   `ChatList.Render` no-ops until `SetSize` is called.
   Encoded in S1's API contract; trivial. Prevents the cache from
   filling with garbage at width 0 before the first
   `WindowSizeMsg`.

---

## 6. Validation strategy

### Per-slice gates (table)

| Slice | `make validate` | matrix rows | manual / new tests |
|---|---|---|---|
| S0 | n/a | n/a | ~30-min pprof confirmation against qba-equivalent; baseline number for S3 gate; "any other unexpected hot spot" yes/no |
| S1 | required | none (no matrix-listed file touched) | unit tests for items / cache |
| S2 | required | `notify-tui`, `drain-row-inject` | shadow-mirror invariant unit test |
| S3 | required | `notify-tui`, `drain-row-inject`, `recover-live` (**startup-time gate**) | render-cache hit/miss tests; manual: long-session scroll, agent switch |
| S4 | required | `notify-tui`, `drain-row-inject`, `recover-live`, `idle-interrupt-inject` | **manual TUI: full tool lifecycle, Bash specifically**; tool-header parity test |
| S5 | required | `notify-tui`, `handoff`, `recover-live`, `idle-interrupt-inject` | manual TUI: Esc interrupt; contract-test (reflection) on ChatList API |
| S6 | required | **all matrix rows** (`make test-e2e-matrix`) | regenerate goldens |
| S7 | required | none (thinking blocks not exercised by listed rows) | **manual TUI**: prompt that produces long thinking block; collapsed render; Ctrl+O expand/collapse |

### End-to-end correctness after the whole arc

After S6 the TUI must:

1. Render a fresh session correctly: user prompt → assistant stream →
   tool call (pending → done) → assistant continuation.
2. Render a long session (100+ messages) without per-keystroke lag.
   Gate: keypress-to-first-paint within S0 baseline + 5ms tolerance.
3. Render a bash tool call header that matches today's main visually
   (golden compare).
4. Handle a nested Agent / Task call with `Depth > 0` indentation
   matching QUM-379/386 behavior.
5. Switch observed agent (Ctrl+N/P) without replaying old events
   (epoch gate intact).
6. Apply Ctrl+O once, see every tool-call item in every agent's
   buffer flip to expanded; press again, all collapse.
7. Receive a `<system-notification>` envelope from inbox-drain,
   render it as a single `SystemNotificationItem` per peeled wrapper,
   no envelope tags visible.
8. Render thinking content as a collapsed `ThinkingItem`; Ctrl+O
   expands every thinking item alongside every tool item (global
   fan-out).
9. **NEVER** render: "Session restarting…", "Interrupt sent",
   "Recovered N agents", "ERROR: <transport>". Those surface
   elsewhere.
10. Pass every mandatory-test matrix row.

### Reflections on Phase 3's failure mode applied to the gates

Phase 3 nominally passed `make validate` at every checkpoint but
broke the binary in interactive use. Therefore: every slice from S3
onward has a **manual TUI gate**, not just `make validate`. The
`/tui-testing` skill exists for this reason; use it.

---

## 7. Cleanup tracking

**Standing principle (per chassis-port-scoping.md §6 precedent and
research doc §5 phase-9):** each slice ships its own cleanups inside
the same PR — dead test files, unused message fields, stale
comments. Don't accumulate a Phase 9 mop-up that turns into its own
project.

Items that **will** generate cleanups across the arc:

- **QUM-667 tactical per-`MessageEntry` cache** (ships pre-S1 inside
  `viewport.go`). Deleted along with `viewport.go` in S6. The
  cleanup is mechanical because the whole file goes away; no
  separate de-shim PR needed.
- `viewport.go` shrinks each slice; finally deletes in S6.
- `viewport_test.go` shrinks each slice; finally deletes in S6.
- `view_cache.go` — its per-panel cache may become moot once items
  cache individually. Audit at S6.
- `render.go` — folds into per-item `Render` methods over S3-S6.
- `tool_header.go` — its formatting becomes `ToolCallItem.Render`
  internals over S4.
- `messages.go` — *kept verbatim* (the mandatory-test contract).
  Any cleanup here is out of scope for this arc.

**Phase 9 (QUM-655) sweep**, if filed for this arc, scope is:
- Final golden regeneration after S6.
- CLAUDE.md edits: any matrix-row file-list updates, a new
  "Viewport contract" section, any text-selection updates that
  became applicable.
- Removal of any feature flag or shim left from S2/S3/S4 bail-out
  hooks.

---

Aggregate end-state: net **deletion** of approximately 800-1500 LOC
of legacy viewport rendering, plus ~700 LOC of new tests. The arc
subtracts code on balance.

---

## Empirical evidence + acknowledgements

This plan's §2.2 per-item-cache hypothesis was empirically validated
by **trace** on 2026-06-03 via a forensic triage of dmotles' qba
sprawl session (read-only, via the coder CLI):

- `sprawl enter` at **24.9% CPU when idle** on a 6-day session.
- `ViewportModel.renderAndUpdate` (`internal/tui/viewport.go:628`):
  **50–200ms per call**, on every event AND every spinner tick.
- Each call rebuilds the full conversation string through glamour
  — O(N) in transcript length.
- Filed as **QUM-667 [High, Improvement]**.

Two consequences for this plan:

1. The arc's perf justification is no longer hypothetical; it is the
   structural fix for a measured production hot path.
2. The tactical pre-fix (per-`MessageEntry` cache inside
   `ViewportModel`) lands as a standalone PR before S1 so dmotles
   gets relief immediately. S3 reimplements the same cache inside
   ChatList; S6 deletes the tactical code along with `viewport.go`.

Separately, trace filed **QUM-666** (MCP tool schema drift —
`agent` vs `agent_name`). Not in this arc's scope; noted in §4.7
so reviewers don't conflate.

Thanks to trace for the read-only forensic methodology and the
filings — without empirical evidence the arc was a plausible
hypothesis; with it, the design is grounded.

---

## Reflections

**Rev 1 surprises (kept):** the dual-append shim is the load-bearing
decomposition mechanism; Ctrl+O global lock is quietly important for
S3+ multi-agent cleanliness; the S5 contract reflection test is the
actual enforcement (once `AppendBanner` is gone from the type,
future contributors must add a method, which is reviewable).

**Rev 2 surprises:**

- **Empirical evidence changes the doc's posture.** §4.1 no longer
  reads "we think this might happen" but "this is happening, here's
  the fix." Designs feel different when grounded.
- **The tactical pre-fix is a real architectural decision.** "Both"
  wins because S6 deletes viewport.go wholesale anyway, but
  short-lived tactical code is normally a smell — here it's bounded
  precisely because the arc sunsets the host file.
- **ThinkingItem-as-agent-content is the right framing.** I worried
  briefly it was a contract violation (sort of "system meta") but
  the contract is about who produced the bytes — the model produces
  thinking tokens — so it lands cleanly on "agent responded with."
  The contract's strength is that it forces this question to be
  asked instead of assumed.

**Tensions for review:** status-bar transient text as the S5 reroute
target is hand-wavy (display policy needs a pass before S5 lands);
Task tool option (c) — expandable showing inner calls — is
locked-out by Q3's (a) but is the right long-term answer if Task
usage grows.
