# ChatList Contract Invariants

**Status:** rev 1 — extracted from shipped code (S1–S4 on `main`).
**Author:** ghost (QUM-683)
**Date:** 2026-06-04
**Branch:** `dmotles/qum-683-chatlist-invariants-doc`
**Tracks:** QUM-683
**Companion to:** `docs/designs/tui-structural-rewrite-plan.md` (the arc plan)
and `docs/designs/qum-669-viewport-wedge-recovery.md` (the portable seam).

This doc is the spec the S5 (QUM-675) implementer reads before splitting
contract violators off the ChatList stream and the spec the S6 (QUM-676)
implementer reads before deleting `viewport.go`. Invariants are pulled from
the shipped S1–S4 code on `main` (commits `8e28ddd`, `fb9f1ac`, `619ce46`,
`ec6828a` / squash `e42ad5c`); citations are `file:line` against the tree at
S4 merge.

---

## 1. What ChatList is

`ChatList` (`internal/tui/chatlist.go:42-63`) is the per-`AgentBuffer`
ordered list of `Item` envelopes that owns the in-flight render cache. As
of S4 it is the render source for the chat region in all cases —
`chatRegionContent` pipes `cl.Render(width)` into `vp.SetContentExternal`
and only falls back to `vp.View()` when `cl.Len()==0` or
`vp.HasContractViolators()` returns true
(`internal/tui/app.go:2118-2126`).

`Item` (`internal/tui/items.go:30-38`) is the row interface; `Expandable`
(`items.go:45-48`) is the per-item-expand subtype. Concrete item types
shipped today: `UserItem`, `AssistantTextItem`, `ThinkingItem`,
`ToolCallItem`, `SystemNotificationItem`, `AutoTriggerItem`
(`items.go:79-440`).

---

## 2. Acceptance invariants (what ChatList accepts)

The following message classes have a corresponding `Append*` (or `Reset`
translation case) and are first-class items:

| Class | Append entry point | Item type | `Finished()` rule |
|---|---|---|---|
| User turn | `AppendUser` (`chatlist.go:119`) | `UserItem` | always true on creation (`items.go:98`) |
| Assistant streaming text | `AppendAssistantChunk` / `FinalizeAssistantMessage` (`chatlist.go:127`, `:144`) | `AssistantTextItem` | true only after `Finalize` (`items.go:151`) |
| Model thinking block | `AppendThinking` (`chatlist.go:157`) | `ThinkingItem` | always true on creation (`items.go:194`) |
| Tool call (incl. nested Agent / Task children) | `AppendToolCall` / `AppendToolCallWithHeader` / `MarkToolResult` (`chatlist.go:167`, `:186`, `:217`) | `ToolCallItem` | true once result lands (`items.go:276`) |
| System notification envelope | `AppendSystemNotification` (`chatlist.go:267`) — one item per peeled `<system-notification>` envelope | `SystemNotificationItem` | always true on creation (`items.go:412`) |
| Auto-trigger header (harness-initiated turn) | `AppendAutoTrigger` (`chatlist.go:282`) | `AutoTriggerItem` | always true on creation (`items.go:440`) |

ThinkingItem is a present-day item type by construction even though S7
(QUM-677) is what actually routes thinking blocks into the chat list from
the wire path. S5 should treat `ThinkingItem` as a normal in-stream item.

---

## 3. Rejection invariants (what ChatList does NOT accept)

The following classes have **no** entry point on `ChatList` — by design.
They are the "contract violators" S5 will route to the status bar / γ
overlay / tree-badge surfaces (rewrite plan §3 S5):

- **`MessageStatus`** — "Session restarting…", "interrupt sent",
  "Recovered N agents", resync banner, etc.
- **`MessageError`** — transport / session faults.
- **`MessageBanner`** — session banners / handoff banners.
- **`MessageSystem`** — legacy mail-glyph "system spoke, not the user"
  injection (QUM-338).

The omission is enforced **structurally**: there is no `AppendStatus` /
`AppendError` / `AppendBanner` / `AppendSystemMessage` method on
`ChatList`. The intent is called out in two places in the source:

- `chatlist.go:11-13` ("Contract notes the next slice owner inherits…
  No AppendStatus/AppendError/AppendBanner here. Those are S5 contract
  violators routed elsewhere.")
- `items.go:14-16` ("The Item set deliberately omits status/error/banner:
  those are S5 contract-violators routed to the status bar / overlays.")

`Reset` (`chatlist.go:306-352`) silently drops these classes on
transcript backfill (`default` branch at `:341-344`) — the legacy `vp`
path still surfaces them, and `chatRegionContent`'s fallback to
`vp.View()` when `vp.HasContractViolators()` is true
(`viewport.go:496-504`, `app.go:2118-2126`) is what keeps the user from
losing them during the S2–S5 dual-append window.

`AppendSystemNotification` on the **raw-text fallback** path (no envelope
present) is also a deliberate drop on the ChatList side
(`chatlist.go:255-263`): the legacy `vp.AppendSystemNotification`
(`viewport.go:573-603`) falls through to `AppendSystemMessage`, which
creates a `MessageSystem` entry — a contract violator that trips
`HasContractViolators` and routes the chat region through `vp.View()`.
Surfacing it again on the `cl` side would diverge from `vp` and
double-render after S5. **S5 must preserve this asymmetry** until the
inbox-drain raw-fallback path is fully replaced or rerouted to a status
surface.

---

## 4. Ordering & mutability invariants

1. **Append-only with one mutating tail.** All `Append*` methods push to
   the end of `c.items`. The only in-place mutations are:
   - `AppendAssistantChunk` extends the **trailing** `AssistantTextItem`'s
     text when that item is the last one and `Finished()==false`
     (`chatlist.go:128-140`, `items.go:122-127`).
   - `MarkToolResult` walks newest→oldest for a matching `ToolID` and
     flips `pending=false` (`chatlist.go:217-249`).
   - `FinalizeAssistantMessage` freezes the trailing in-flight assistant
     item (`chatlist.go:144-152`).
   - `SetToolInputsExpanded` calls `SetExpanded` on every `Expandable`
     and invalidates its cache (`chatlist.go:105-116`).

   No method reorders existing items. No method deletes items (except
   `Reset`, which replaces wholesale).

2. **Single in-flight assistant item.** At most one trailing
   `AssistantTextItem` is in flight at any time (`streamingAssistant`
   flag at `chatlist.go:54-56`). `AppendAssistantChunk` either extends
   that item or starts a new one if the trailing item is not an
   in-flight assistant.

3. **Tool-result lookup is newest-first by `ToolID`.** Mirrors
   `viewport.MarkToolResult` so the new model matches the legacy
   sequence-of-tool-results semantics exactly (`chatlist.go:221-228`).

4. **Depth / parent inference is identical to legacy.** QUM-386
   heuristic is replicated verbatim: explicit `parent_tool_use_id` wins;
   otherwise a non-`Agent` call inside any in-flight `Agent` gets
   `Depth=1` attributed to `lastActiveAgent` (`chatlist.go:192-199`).
   `activeAgents` bookkeeping mirrors `vp`'s post-completion cleanup
   (`chatlist.go:236-245`).

5. **`Reset` is replay-shaped, not edit-shaped.** `Reset(entries)`
   wipes the items slice, all in-flight bookkeeping, and the `Agent`
   nesting state (`chatlist.go:306-311`), then re-plays each entry
   through the same `Append*` calls a live wire-path event would
   produce. **It always ends in a clean state**: the trailing assistant
   is force-finalized at `:350-351` to avoid sticking `cl` in
   `streamingAssistant=true` after a transcript-replay (L2 from the B4
   handoff). `Reset` is the load-bearing entry point — five paths call
   it today (preload, restart, resync, waiting-banner, child transcript
   per the B4 handoff §S4).

6. **Backfill epoch is upstream of ChatList.** The `AgentSelectedMsg`
   epoch lives in `app.go`; it gates which events reach which buffer's
   ChatList. Item construction happens after the gate. Don't rewire
   the epoch in S5/S6 (rewrite plan §4.6).

---

## 5. Render invariants

1. **Width-stable render.** Items must produce byte-identical output for
   the same `(width, expanded)` pairing (`items.go:30-38`). This is the
   load-bearing assumption behind the per-item cache.

2. **Width-0 guard.** `Render(width<=0)` returns `""`. Every item type
   checks `width <= 0` at the top of `Render`; `ChatList.Render` checks
   both the caller's `width` and its own stored width (`chatlist.go:364-374`,
   plan §5 resolved Q7). The cache must not be populated at width 0.

3. **Cache key.** `(width, expanded)`. Cache hits only when
   `env.item.Finished() && cache.width == width && cache.expanded ==
   currentExpanded` (`chatlist.go:378-391`). Misses on width change are
   lazy: `SetSize` does not eagerly invalidate (`:89-99`).

4. **Unfinished items are never cached.** `renderEnvelope` writes the
   cache only when `env.item.Finished()` (`chatlist.go:385-387`); the
   `AssistantTextItem` mid-stream is rebuilt on every `Render` (cheap
   because it is one item).

5. **Trailing newline per item.** `Render` writes `"\n"` after every
   item's output (`chatlist.go:371-373`). Matches the legacy
   `renderMessages` separator convention. Visual-parity gate.

6. **Global expand-tool-inputs (`SetToolInputsExpanded`) is fan-out.**
   QUM-335 "expand all" semantics are locked: every `Expandable` in
   every agent's list flips together (`chatlist.go:105-116`, plan §3 S1
   resolved Q6). Single-item per-item-expand state exists internally
   but is not user-driven today.

7. **`ToolInputsExpanded` is per-`ChatList` state inherited at append.**
   New `ToolCallItem`s and `ThinkingItem`s inherit the current global
   flag on append (`chatlist.go:159-160`, `:169`). The S3 wiring slice
   lifted the global across all per-agent ChatLists; S5 should not
   change this.

---

## 6. The `Idle()` invariant — load-bearing through S5

`ChatList.Idle()` returns `pendingTools == 0 && !streamingAssistant`
(`chatlist.go:290-292`). S3 used it to switch `View()` between
`cl.Render` and `vp.View()`; S4 dropped the switch (now ChatList always
renders) but `Idle()` survives because S5's reroute decisions and any
future wedge-recovery logic want to know "is this surface in flight."

**Required invariants for `Idle()` to be trustworthy:**

- `streamingAssistant` flips true on the first `AppendAssistantChunk`
  and false on `FinalizeAssistantMessage` **or** `Reset`
  (`chatlist.go:134`, `:151`, `:350-351`).
- `pendingTools` is incremented in `AppendToolCall` and decremented in
  `MarkToolResult` **only when the matched item was actually in flight**
  (`chatlist.go:171`, `:229-234`). Double-finalize is a no-op.
- `Reset` zeroes both counters before replaying (`chatlist.go:308-309`).

Any S5 routing logic that fires "the user is idle, surface a banner"
must consult `cl.Idle()`, not `vp` state — `vp`'s pending state will
diverge after S6.

**Hazard (L1 from B4 handoff):** `AgentBuffer.MarkToolResult` today only
reports `vp`'s lookup result. If `vp` and `cl` diverge — e.g. an event
reaches `vp` but not `cl` — `cl.pendingTools` could stay > 0 while `vp`
clears, leaving `cl` not-Idle indefinitely. S4 was supposed to fold an
invariant test in but per the handoff this is still residue; S5 should
verify the invariant holds after the violator-split shrinks the divergence
surface.

---

## 7. The dual-append shim — what it guarantees during S2–S5

The shim's contract (rewrite plan §3 S2–S4, B4 handoff §S5):

- Every wire-path verb that maps to an `Item` is mirrored to both `vp`
  and `cl` (the S2 user-only shim was expanded in S3 to cover
  assistant chunks, tool-call lifecycle, system-notification envelopes,
  and auto-trigger headers).
- Contract violators (`AppendStatus`, `AppendError`, `AppendBanner`,
  `AppendSystemMessage`) flow into `vp` only; `cl` has no method to
  receive them.
- `chatRegionContent` (`app.go:2118-2126`) reads from `cl.Render` when
  `cl.Len() > 0` and `!vp.HasContractViolators()`; otherwise falls back
  to `vp.View()`. **The fallback is the user-visible guarantee that
  banners/status survive S2–S5 without rerouting work.**
- Per-agent buffer shape (`agentBuffers map[string]*AgentBuffer`,
  backfill epoch QUM-439/479, `seenToolIDs` QUM-334) is unchanged.

S6 deletes the shim by deleting `vp`'s rendering surface; S5 must finish
rerouting violators **before** S6 lands, or `chatRegionContent`'s
fallback will start dropping visible content.

---

## 8. QUM-669 resync semantics — what ChatList must preserve

The QUM-669 design (`docs/designs/qum-669-viewport-wedge-recovery.md`
§3 portable-seam table) calls out the renderer-facing surface as the
**single swappable layer** between `vp` and `cl`:

```
Renderer (the one swappable surface):
  today:   ViewportModel.SetMessages([]MessageEntry)
  future:  ChatList.Reset([]MessageEntry)      ← S3 landed this
  Sink:    AppendStatus(text string)           ← S5 reroutes this away
```

`Reset` is already shipped on ChatList (`chatlist.go:306-352`) and is
treated as a first-class non-violator operation per the QUM-669 §3
"Compatibility with the rewrite plan" callout. **The S6 deletion must
preserve the following:**

1. **`Reset(entries)` is the resync sink.** Whatever post-S6 owner of
   the chat-region content surface is must expose a single-call
   transcript-replace operation with the same semantics as today's
   `ChatList.Reset` — wipes all bookkeeping, replays through `Append*`,
   force-finalizes the trailing assistant. The resync command in
   `app.go` calls `vp.SetMessages(entries)` today (QUM-669 §2.4); S6
   replaces that call with `cl.Reset(entries)` (or the post-S6
   equivalent).
2. **Resync banner is a status-bar segment, not a ChatList item.** The
   QUM-669 design routes the "✓ resynced — recovered N events" line to a
   transient status-bar text field (§2.6), not to the in-flow viewport.
   This is **already** S5-compliant; the only S6 work is to delete the
   legacy `vp.AppendStatus` fallback path. S6 must NOT reintroduce a
   `ChatList.AppendStatus`-shaped method.
3. **Drop-detection seq stamping is upstream of ChatList.** The
   `EventBus` `Seq` field and `TUIAdapter` gap detection
   (`internal/runtime/eventbus.go`, `internal/tuiruntime/tuiadapter.go`)
   are unaffected by the S6 deletion. ChatList does not see Seq.
4. **Ctrl+L resync trigger is in `app.go`.** Survives S6 unchanged.
5. **`HasContractViolators` goes away with `vp`.** Once S5 has emptied
   the violator classes out of `vp`'s message stream, the
   `chatRegionContent` branch is dead. S6 deletes it together with
   `vp.SetContentExternal` and the `appliedContent` cache.
6. **Spinner ticker is already gone.** S4 deleted the global spinner
   subsystem; tool-call items render a static `pendingToolGlyph`
   (`items.go:61`) until result. Wedge-exit (QUM-669 §2.7) is no longer
   gated on cancelling a ticker; it depends only on
   `cl.MarkToolResult` flipping `pending=false` (or `Reset` replacing
   the slice wholesale).

**Invariants QUM-669 explicitly asks ChatList to honor going forward:**

- A single in-viewport `MessageStatus` line on `normal → dropped` and
  `recovered`. S5 reroutes this to the status bar — the QUM-669 design
  already anticipates this and treats it as the S5-compliant target.
- Reset-after-resync must clear any in-flight `Pending: true` tool
  entries so quit-during-wedge is unblocked. ChatList's `Reset` already
  does this by construction (replays entries, finalizes the trailing
  assistant; replayed `MessageToolCall` entries with `Pending=false`
  call `MarkToolResult`, the ones with `Pending=true` re-enter the
  pending state which is the correct behavior on a faithful replay).

---

## 9. Non-invariants — things that look stable but can change

Listed so S5 / S6 implementers do not write tests against these:

1. **Pending-tool glyph (`⠿`).** `items.go:61` defines it as a constant
   that S4 picked when the global spinner subsystem went away
   (rewrite plan §3 S4 resolved Q6). Could change to `⚙` / any frame
   without violating the contract. Synchronized-pulse spinner pulse is
   *not* part of the contract.
2. **Streaming cursor (`▍`).** `items.go:67` — kept separate from
   `viewport.StreamingCursor` so the two renderers can diverge.
3. **Box-drawing characters / nested indent.** `┌`, `└`, `│`, the
   `nestedToolCallItemIndent = 2` constant (`items.go:72`) are layout
   choices, not contract.
4. **Cache shape.** Today's `cachedRender{width, expanded, out}` single
   slot per envelope can become an LRU of N slots without affecting
   external behavior (rewrite plan §4.5).
5. **Markdown renderer wrap width.** `ctx.renderer.SetWidth(width)` is
   called on every assistant render (`items.go:142`). The render is
   stable for a given `(width, text)` pair but the renderer instance is
   shared and resized in place. Don't depend on per-call width-isolation
   if you reuse the renderer outside ChatList.
6. **Agent-container rendering inside ChatList.** Today nested Agent
   children render as flat `Depth>0` items (`items.go:212-215`,
   resolved Q3 in the plan doc). The container-style render that
   `viewport.renderAgentContainer` does is **not** ported; if S5 or a
   later slice decides to bring back the container shape, that's a
   render-only change that does not touch the Item contract.
7. **`SystemNotificationItem` style/glyph.** Uses
   `notificationGlyphAndStyle` via a stub `MessageEntry`
   (`items.go:404-408`). Both the stub bridge and the glyph table can
   move without touching ChatList.
8. **`Idle()`'s exact predicate.** Today: no streaming assistant + no
   pending tools. If S7 introduces a `ThinkingItem` in-flight state
   (which it does not — thinking blocks arrive whole today), `Idle()`
   would need to learn about it. S5 should not depend on the exact
   predicate; it should call `Idle()`.

---

## 10. What contract violators are NOT (S5 reroute spec)

For the S5 implementer (QUM-675): the chat-region stream is for **agent
output** — text the model produced, tool calls the model invoked, and
envelopes the supervisor injected as quasi-user-role content the model
will reply to. The following are NOT agent output and must NOT land in
ChatList:

| Class | What it is | S5 reroute target |
|---|---|---|
| Session lifecycle banners | "Session restarting…", "Backend recovered", "Recovered N agents" | Status-bar transient text |
| Interrupt acknowledgement | "Interrupt sent" | Status-bar transient text |
| Transport / fault errors | `BackendFaultMsg` text, MCP errors | γ overlay (existing error dialog) + tree badge |
| Validation pill | Build/test gate progress | Existing `SetValidatePill` segment |
| Inbox-drain raw-text fallback | Text without `<system-notification>` envelope | Stays on `vp` until inbox-drain itself is fixed to always wrap |
| QUM-669 resync banner | "✓ resynced — recovered N events" | Status-bar transient text (see §8) |
| Drop-telemetry chip | First-drop tick | Status-bar `SetEventDrops` segment (already shipped, QUM-681) |
| Handoff overlay | `HandoffRequestedMsg` body | Existing modal stack |

Everything in this table either has an existing non-viewport surface
**today** or has one being built in the wedge-recovery / messaging
arcs. None of them need a `ChatList.Append*` method.

**The S5 contract test** (rewrite plan §3 S5, §4.4) should assert via
reflection that `ChatList`'s exported method set contains no
`AppendStatus`, `AppendBanner`, `AppendError`, or `AppendSystemMessage`.
That test pins the contract for future contributors.

---

## 11. Recommended issue-body amendments

### QUM-675 (S5) — add to "Implementation Details"

> Before designing the violator-stream split, read
> `docs/designs/chatlist-invariants.md`. In particular:
>
> - §3 enumerates the four classes ChatList already rejects by
>   construction; your work is to actually re-route the call sites that
>   land on `vp.AppendStatus` / `vp.AppendBanner` / `vp.AppendError` /
>   `vp.AppendSystemMessage` (the raw-text fallback at
>   `viewport.go:573-603`) into the status-bar / overlay / tree-badge
>   surfaces in §10's table.
> - §6 names `cl.Idle()` as the load-bearing predicate for "the chat
>   is idle, surface a banner" decisions. Use it.
> - §10 is the spec for the violator stream. Each row in the table is a
>   reroute target; the test gate is the reflection-based contract test
>   from rewrite-plan §3 S5.
> - §3 documents the asymmetric drop of the raw-text
>   `AppendSystemNotification` fallback path on the `cl` side. Preserve
>   that asymmetry until the inbox-drain wrap-up is in place — do not
>   "fix" it by adding a `cl` method.

### QUM-676 (S6) — add to "Implementation Details"

> Before designing the `viewport.go` deletion, read
> `docs/designs/chatlist-invariants.md` §7 (the dual-append shim
> contract you are ending) and §8 (the QUM-669 resync semantics you
> are inheriting). In particular:
>
> - The `chatRegionContent` `vp.HasContractViolators()` fallback at
>   `app.go:2118-2126` is the only thing keeping the user from losing
>   banners during S2–S5; verify S5 has finished its reroute before
>   merging S6. If you can grep for `MessageStatus`/`MessageError`/
>   `MessageBanner`/`MessageSystem` outside test files and the
>   to-be-deleted `viewport.go` and get zero hits, S5 is done.
> - Replace the resync-path call `vp.SetMessages(entries)` with
>   `cl.Reset(entries)` (or the post-S6 equivalent). `cl.Reset` already
>   force-finalizes the trailing assistant (`chatlist.go:350-351`) and
>   clears `pendingTools` (`:308-309`), so the QUM-669 wedge-exit
>   invariant (§2.7) holds without an extra `setTurnState` poke beyond
>   what `app.go` already does on `EventDropDetectedMsg`.
> - `vp.SetContentExternal` / `appliedContent` / `HasContractViolators`
>   all become dead code along with `renderMessages`. Delete them.
> - The QUM-667 tactical per-`MessageEntry` cache inside `vp` dies with
>   the file — mechanical cleanup, no separate de-shim PR needed
>   (rewrite plan §7).

---

## 12. References

- `internal/tui/chatlist.go` — ChatList implementation (S1–S4 shipped).
- `internal/tui/items.go` — Item interface + concrete types.
- `internal/tui/viewport.go:267-282` — `SetContentExternal` (the chat-
  region routing seam introduced in S3; dies in S6).
- `internal/tui/viewport.go:491-504` — `HasContractViolators` (the S5
  fallback gate; dies after S5 empties the violator classes).
- `internal/tui/viewport.go:573-603` — `AppendSystemNotification`
  raw-text fallback (the asymmetric drop documented in §3 / §10).
- `internal/tui/app.go:2118-2126` — `chatRegionContent` (the routing
  surface that ties cl + vp together).
- `docs/designs/tui-structural-rewrite-plan.md` §2.1, §3, §4.4, §5.
- `docs/designs/qum-669-viewport-wedge-recovery.md` §3 (portable seam),
  §2.4 (resync command), §2.7 (wedge-exit).
- `docs/handoffs/b4-manager-handoff.md` §S4 (Reset hazards L1/L2/L3),
  §S5 (this issue), §S6 (forge's portable-seam handoff).
- Shipped commits on `main`: `8e28ddd` (S1), `fb9f1ac` (S2), `619ce46`
  (S3 + QUM-684 helpers refactor), `ec6828a` / `e42ad5c` (S4).

---

## Reflections

**What was surprising:**

- The "drop the raw-text `AppendSystemNotification` fallback on the
  `cl` side" decision (§3, `chatlist.go:255-263`) is more load-bearing
  than the plan doc admits. The `cl`/`vp` parity test would fail if we
  were not deliberate about routing the raw text through `vp` only —
  this is a real invariant, not a stylistic choice, and S5 has to
  preserve it until inbox-drain itself stops emitting unwrapped raw
  text.
- `Reset`'s force-finalize-trailing-assistant tail (`chatlist.go:350-351`,
  the B4-handoff L2 residue) is what makes QUM-669's wedge-exit
  invariant trivially fall out at S6. The QUM-669 design had to argue
  for `setTurnState(TurnIdle)` separately; with `cl.Reset` doing the
  force-finalize, the chat-region side of wedge-exit is automatic.
- The S4 spinner deletion (replaced by a static `⠿` glyph) means
  `Idle()` is the **only** signal of in-flight state. Pre-S4 designs
  could lean on `SetSpinnerFrame` cadence to know "something's animating";
  post-S4 they cannot. S5 must read `Idle()` and trust it.

**Open questions I would investigate next with more time:**

- Whether the B4-handoff L1 hazard (`AgentBuffer.MarkToolResult` only
  reporting `vp`'s result) actually manifests in any matrix-row scenario,
  or whether the dual-append shim is so deterministic that
  `vp.pendingTools` and `cl.pendingTools` always agree. An invariant
  test would settle it.
- Whether `ToolInputsExpanded` survives `Reset` correctly across a
  resync. The field is on `ChatList`, not on individual items; `Reset`
  does not touch it (`chatlist.go:308-311`). Items re-created during
  replay inherit the current flag (`:159`, `:169`). Looks correct, but
  worth a focused test.
- Where the post-S6 owner of the resync sink lives. The QUM-669 §3 table
  shows `ChatList.Reset` as the receiver, but `app.go` is the caller —
  if S6 wants the chat-region routing fully encapsulated, a small
  `ChatRegion` wrapper that owns the `cl` + the resync command could
  fall out naturally. Out of QUM-683 scope but worth a slice if S6 grows.

**Anything dangerous surfaced:** none. The contract is consistent across
the shipped code, the rewrite plan, and the QUM-669 design. The B4
handoff's L1/L2/L3 residues are tracked but not blockers for S5; the L2
("Reset can stick streamingAssistant=true") was already fixed in S4 by
the force-finalize at `chatlist.go:350-351`.
