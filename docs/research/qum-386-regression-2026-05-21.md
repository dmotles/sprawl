# QUM-386 regression investigation — 2026-05-21

**Researcher**: ghost
**Tracker**: [QUM-386](https://linear.app/qumulo-dmotles/issue/QUM-386/fix-parallel-agent-sub-agent-rendering-in-tui-viewport) — reopened by dmotles 2026-05-21
**Branch under analysis**: `main` @ HEAD (post-QUM-577, post-QUM-481, post-QUM-419)

## TL;DR

The original QUM-386 fix (commit `4532a60`, 2026-04-30) **is still present** in the codebase. The structural data model (`MessageEntry.ParentToolID`, `ViewportModel.activeAgents`, two-pass `renderMessages`, `renderAgentContainer`) is intact, and the bridge fix in `mapAssistantMessage` still emits *all* `tool_use` blocks via `AssistantContentMsg`.

What never worked correctly on the live-stream path — and was only papered over because the original e2e test never exercised it — is **per-Agent attribution of inner sub-agent tool calls when ≥2 Agents run in parallel**. The viewport uses a single-string `lastActiveAgent` "last writer wins" heuristic, so every inner tool call streamed from any sub-agent gets attached to whichever Agent the parent assistant pushed last. That is exactly what dmotles is seeing.

This is **not** a regression in the post-2026-04-30 sense (no follow-on commit broke a previously-working path). It's a structural gap in the original fix that QUM-577 happened to surface by making the *replay* path correct — the live-vs-replay disparity is now sharp enough to be obviously wrong to a human watching live.

## Repro / symptom recap

From dmotles' 2026-05-21 comment:

> When weave fires multiple Claude Code sub-agents (via the built-in `Agent` tool) in parallel […] All tool calls / outputs from every parallel sub-agent get appended into the **last** Agent block that was opened in the viewport. […] the previously-running Agent block visually freezes — its activity stops moving — and all subsequent tool-call output accumulates under the most recent Agent block.

## Code-path map (live stream, today)

1. `internal/protocol/types.go`
   * `AssistantMessage.ParentToolUseID *string` is decoded off every assistant envelope (top-level JSONL field, not a content-block field). Same for `UserMessage`. These are the wire field that tells us "this assistant turn was emitted *inside* the sub-agent whose outer Agent tool call has ID X".
2. `internal/tui/protocol_mapping.go` → `mapAssistantMessage`
   * Decodes `protocol.AssistantMessage` (gets `.ParentToolUseID`) and then `assistantContent` (gets `.Content` blocks + `.Usage`).
   * Emits a `ToolCallMsg` per `tool_use` block (QUM-386 multi-block fix, line 79–87).
   * **Never reads `am.ParentToolUseID`.** The pointer is decoded then discarded.
3. `internal/tui/messages.go` → `ToolCallMsg`
   * Carries `ToolName`, `ToolID`, `Approved`, `Input`, `FullInput`, `HeaderArg`, `HeaderParams`. **No parent field.**
4. `internal/tui/app.go` → `AssistantContentMsg` / `ToolCallMsg` case
   * Calls `vp.AppendToolCallWithHeader(...)` (line 736 / 761 / 2689). Passes nothing about the parent.
5. `internal/tui/viewport.go` → `AppendToolCallWithHeader`
   * Computes attribution itself: `if len(m.activeAgents) > 0 && name != "Agent" { depth = 1; parentID = m.lastActiveAgent }`.
   * `m.lastActiveAgent` is a single string mutated on every Agent push (line 339), and on completion is replaced with "any other active agent" via `for id := range m.activeAgents { lastActiveAgent = id; break }` — i.e. effectively random map iteration order, not the chronological parent of the inbound block.

### The breakage, walked through

Assume the parent turn emits two `tool_use` blocks `A1` and `A2`, both `Agent`, in the same assistant message:

* `AppendToolCallWithHeader("Agent", "A1", …)` → `activeAgents = {A1}`, `lastActiveAgent = A1`. Depth 0, parentID "". OK.
* `AppendToolCallWithHeader("Agent", "A2", …)` → `activeAgents = {A1, A2}`, `lastActiveAgent = A2`. Depth 0, parentID "". OK.

Now Claude Code runs both sub-agents and starts streaming sidechain `assistant` messages:

* `{type:"assistant", parent_tool_use_id:"A1", message:{content:[{type:"tool_use", name:"Read", …}]}}` arrives.
* `mapAssistantMessage` emits `ToolCallMsg{ToolName:"Read", ToolID:…}` with **no parent**.
* `AppendToolCallWithHeader("Read", …)` runs. `len(activeAgents) == 2 && name != "Agent"` → `parentID = m.lastActiveAgent = A2`. **Wrong: should be A1.**
* Same for any sub-call from A1's sub-agent — all get attributed to whichever ID happens to be in `lastActiveAgent` when they arrive.

Empirically this lands every inner call under the most-recently-pushed Agent's container, exactly matching the user-reported symptom. When A1 finishes first, `MarkToolResult` picks "any remaining active" → A2; further calls from A2 then correctly land under A2 but A1's earlier mis-attributed children never get re-attributed.

The "previously-running Agent block visually freezes" symptom comes from the same root cause: A1's container is collapsed-or-pending in `renderAgentContainer`, but no `childrenOf[A1]` entries ever get built because every child went into `childrenOf[A2]`.

## Replay path (for contrast — `internal/tui/replay.go`)

`scanTranscriptWithSidechain` (QUM-577, May 18) does it right:

```go
isSidechain, _ := rec["isSidechain"].(bool)
wireParentToolID, _ := rec["parent_tool_use_id"].(string)
…
if isSidechain && wireParentToolID != "" {
    parentID = wireParentToolID
    if depth < 1 { depth = 1 }
}
```

This is covered by `TestLoadChildTranscript_SidechainParallelAgents_ParentToolIDFromWire` in `replay_test.go:1166` — which asserts exactly the parallel-attribution invariant that the live path violates. There is no live-stream equivalent of that test.

## Why now? Why nobody caught it on 2026-04-30

Two reasons:

1. **The original e2e test (`scripts/test-parallel-agent-viewport-e2e.sh`) never emits inner sub-agent activity.** It synthesizes:
   * One assistant message with two parallel `tool_use` blocks (the outer Agents).
   * Two `tool_result` user messages (collapsing the containers).
   * Nothing in between. No sidechain assistant messages, no inner `tool_use`, no `parent_tool_use_id`. The "two-┌-markers" assertion passes with the `lastActiveAgent` heuristic intact because nothing exercises it.
2. **QUM-577 sharpened the live/replay disparity.** Before QUM-577, `LoadChildTranscript` filtered out sidechain records entirely — the child viewport showed only the outer Agent frame. Post-QUM-577 the replay path correctly attributes per-Agent. Run the same parallel-Agent scenario, restart the TUI, and now the replay renders perfectly while the live stream looks broken. That asymmetry is dmotles' observation: "the fix never covered the live-streaming path in the same way it covered replay (cf. QUM-577 / QUM-593 split between replay and live-stream paths)".

`git log` on the touched files confirms no follow-on change between `4532a60` (QUM-386 fix) and HEAD perturbed the live attribution logic; `lastActiveAgent` has been in place untouched since 2026-04-30. The only material edits to `viewport.go` since were:

* `b7c3948` (QUM-476) — `SetMessages` rebuilds `activeAgents` from in-flight entries. Doesn't affect live attribution.
* `c729621` (QUM-419) — per-tool header fields. Unrelated.

## Recommendation

**Plumb `parent_tool_use_id` end-to-end on the live path, mirroring the replay path. Drop the `lastActiveAgent` heuristic.**

### Required changes (do NOT implement here — for the eventual fix)

1. **`internal/tui/messages.go`** — add `ParentToolUseID string` to `ToolCallMsg` (and, for symmetry, `AssistantTextMsg` — sub-agent assistant text doesn't currently nest but the field is cheap and future-proofs child-viewport attribution).
2. **`internal/tui/protocol_mapping.go`** → `mapAssistantMessage`
   * After `json.Unmarshal(msg.Raw, &am)`, deref `am.ParentToolUseID` once and stamp the resulting string onto every `ToolCallMsg` emitted for the message.
3. **`internal/tui/viewport.go`** → `AppendToolCallWithHeader`
   * Add a `parentToolUseID string` parameter. When non-empty, set `parentID = parentToolUseID` and `depth = 1` *unconditionally* (don't gate on `len(activeAgents) > 0` — sidechain assistant messages arrive after the outer Agent's `tool_result` is in flight on the wire, and there are degenerate orderings where `activeAgents` is empty when the inner call lands; the wire field is authoritative). Fall back to the existing `lastActiveAgent` heuristic only when the wire field is empty (covers unit tests + any future non-Agent nesting source).
   * `lastActiveAgent` and its book-keeping can be deleted entirely once tests are migrated; `activeAgents` (set membership) is still useful for "is this Agent in flight?" — `MarkToolResult` still needs to clear `activeAgents`.
4. **`internal/tui/app.go`** — all three `AppendToolCallWithHeader` call sites (lines 736, 761, 2689) pass `im.ParentToolUseID` through.
5. **`AppendToolCall` shim** stays as-is (passes `""`), preserving backward compatibility for tests that didn't carry the field.

### Tradeoffs

| Option | Pros | Cons |
|---|---|---|
| (a) **Wire `parent_tool_use_id` end-to-end (recommended)** | Symmetric with replay path — single source of truth. Correct for arbitrary fan-out (3+ parallel Agents). No heuristic. Trivially testable. | Touches `ToolCallMsg` signature → ~10 call sites + tests need the new field. Small backcompat cost. |
| (b) Keep `lastActiveAgent` but maintain a per-pending-Agent "last sub-agent that streamed something" map | Smaller signature delta | Still fundamentally a heuristic; requires speculative correlation (sub-agent A streams a tool_use → we guess "A is still streaming"); breaks on interleaved sub-agent output, which is exactly the case the wire field exists to disambiguate. Strictly inferior. |
| (c) Correlate purely by intra-`assistant`-message block order (parallel calls in same message → same parent) | No protocol coupling | Cannot solve the bug. Parallel sub-agent activity arrives in *separate* assistant messages (one per sub-agent turn), each with its own `parent_tool_use_id`. There's no "block order" to leverage at the parent level. |
| (d) Drop nesting entirely; render all sub-agent activity flat | Smallest diff | Undoes QUM-379 / QUM-386 visual containment — a UX regression worse than the current bug, which at least keeps the *outer* Agent rows correct. |

Recommend (a).

### Regression guards (mandatory)

* **Unit test** in `internal/tui/protocol_mapping_test.go`: feed two assistant envelopes with `parent_tool_use_id` set to `A1` and `A2` respectively, each carrying a single `tool_use` block, and assert that the resulting `ToolCallMsg.ParentToolUseID` matches. Sister of the existing `TestLoadChildTranscript_SidechainParallelAgents_ParentToolIDFromWire`.
* **Unit test** in `internal/tui/viewport_test.go`: `AppendToolCallWithHeader` with a non-empty `parentToolUseID` produces a `MessageEntry` with `ParentToolID == parentToolUseID` and `Depth == 1`, *regardless* of `activeAgents` state.
* **Extend `scripts/test-parallel-agent-viewport-e2e.sh`** (or new sister script) to interleave sidechain assistant messages with `parent_tool_use_id:"agent-tool-1"` and `parent_tool_use_id:"agent-tool-2"`, each emitting a uniquely-named inner tool (e.g. `Read alpha-file.txt` vs `Read beta-file.txt`), and assert that:
  * Each filename appears under exactly one container (string proximity check via tmux capture-pane).
  * The container "freeze" symptom does not occur (the container whose sub-agent emits earliest still shows nested activity in subsequent frames).
* This is the assertion that pre-fix would fail (every inner call lands under the second container) and post-fix would pass.

## Open questions / things I'd verify next

1. **Does Claude Code's live stream actually emit sidechain `assistant` messages on the parent stream?** I'm 95% sure yes — `protocol.AssistantMessage.ParentToolUseID` exists and is exercised by unit tests, and the live stream is the same NDJSON the JSONL replay file is written from, so structurally there's no reason for a divergence. But I did not directly tail a `claude` subprocess during a parallel-Agent run to confirm the messages reach `mapAssistantMessage`. **If they don't**, the fix becomes "load child transcript live + skip live path attribution entirely" — a much bigger change. **Recommend the implementing engineer verify with a one-off `tee` of the raw stream** before committing to fix (a).
2. **Sub-agent assistant TEXT** is emitted as `AssistantTextMsg` and currently appended to the *root* assistant message buffer via `AppendAssistantChunk`. That's likely also wrong for parallel Agents (the two sub-agents' text will interleave into one stream), but the user's report focuses on tool calls. Flag as a follow-up rather than scope-creep this fix.
3. **`AppendToolCall` shim usage** — quick audit needed to find non-test callers that still hit the no-parent shim. If any production path uses it post-fix, attribution will silently fall back to the heuristic. The new tests should fail if any production path regresses to the shim.
4. **Whether QUM-593 actually relates here.** dmotles' comment groups it with QUM-577 as "split between replay and live-stream paths". I didn't look at QUM-593 — recommend the implementing engineer check it for any wire-field handling I'm missing.

## Reflections

* **Surprising**: the bug was structurally baked into the original fix. The "fix" implemented per-Agent rendering (containers) and a per-Agent data model (`ParentToolID`) but used a heuristic for the one thing that *had* to be correct for the fix to work. The e2e test was tailored to the data-model assertions and skipped the attribution case — classic shape-of-the-test-determines-shape-of-the-fix anti-pattern.
* **Unanswered**: open question (1) above. I'm confident in the recommendation, but the implementing engineer should verify live-stream parent_tool_use_id propagation before committing to the call-graph changes.
* **If I had more time**: I'd write a 30-line standalone Go program that pipes a synthetic two-parallel-Agent stream into `mapAssistantMessage` + a no-network `ViewportModel`, dump the resulting `MessageEntry` slice, and assert by inspection that nothing about the fix changes (so as to verify my live-vs-replay diagnosis is exactly right and not slightly off in a way that would invalidate the proposed fix). The unit test in `protocol_mapping_test.go` proposed above is approximately this, written as a permanent guard.
