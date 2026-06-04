# B4 Manager Handoff — TUI Structural Rewrite Arc

**Author:** citadel (B4 manager, 2026-06-03 to 2026-06-04)
**Audience:** next B4 manager picking up the arc post-checkpoint.
**Read alongside:** `docs/designs/tui-structural-rewrite-plan.md`, the most recent slice's Linear comment, and the §0 "Resolved Decisions" section of the plan doc.

---

## Status

**Done (3 + 1 prep):** S0 (QUM-670), S1 (QUM-671), S2 (QUM-672), S3 (QUM-673 + QUM-684 prep refactor folded in).

**Next:** S4 (QUM-674, streaming items), then S5 (QUM-675, contract violators), S6 (QUM-676, delete viewport.go shim), S7 (QUM-677, ThinkingItem).

**Checkpoint discipline (new cadence per weave 2026-06-04):** one slice → main → `make install` → restart → next B4 manager spawn. No more stacking. You will be retired after your one slice. Plan accordingly.

**Integration branch state at retirement:** `dmotles/b4-tui-rewrite-arc` is rebased on main + carries S0+S1+S2+S3+QUM-684. After weave pulls to main, your fork will start from main + the new commits already there.

---

## Slice-specific gotchas (S4–S7)

### S4 (QUM-674): streaming items
- **Must fold in this residue from S3 (per S3 scope deferral):**
  - **M1 (load-bearing):** add `BenchmarkChatList_Render_LongHistory_*` to anchor the ChatList path directly. Today only the legacy-viewport-side bench exists; gating S4+ without a ChatList-direct bench is shaky.
  - **L2:** `Reset` with `Complete=false` assistant entry can stick cl `streamingAssistant=true`. Conservative fix: always Finalize during Reset.
  - **L1:** `AgentBuffer.MarkToolResult` only reports vp's lookup result; if vp/cl diverge cl stays not-Idle. Add invariant test.
  - **L3:** raw-text fallback in `AppendSystemNotification` surfaces text cl shows but vp's `HasContractViolators` would mask. Reconcile.
- **`Reset` is now load-bearing**: called from 5 paths (ChildTranscriptMsg + preload + restart + resync + waiting-banner). A single bug here cascades. Be very deliberate about any change touching Reset.
- **`vp.SetContentExternal(cl.Render)`** is the chat-region routing seam introduced in S3. The side-effect-inside-getter shape of `chatRegionContent` is fragile — `viewport.go` comment flags it for S6 cleanup. Don't refactor in S4 unless you have to; just be aware.

### S5 (QUM-675): contract violators / overlays
- This slice splits banners/status/error/overlay items out of the canonical ChatList stream. Plan-doc §3 S5 enumerates the verbs.
- **Hazard:** QUM-683 (ChatList contract invariants doc) is still open. finn S1 inferred invariants from §3 + addendum; that was enough for S2/S3 but S5 is where the contract gets stress-tested. Recommend a pre-S5 task: have a researcher (or fold into S5 dispatch) write the invariants section to `docs/designs/tui-structural-rewrite-plan.md` first. **forge — who originally committed to writing it — is retired.** No one to chase.
- The dual-append shim is still load-bearing through S5. Don't delete it until S6.

### S6 (QUM-676): delete viewport.go wholesale
- **forge's "portable seam" note (QUM-669):** the resync logic in `viewport.go` was deliberately written to be portable — its invariants live in QUM-669's §3 portable-seam table + addendum. When you delete `viewport.go`, the resync behavior must move into ChatList (or its container) and preserve those invariants. Read QUM-669's design notes before designing the S6 deletion.
- The chat-region side-effect-in-getter `chatRegionContent` shape (S3 reflection #1) should die with viewport.go — fix the routing seam at the same time.
- `vp.SetContentExternal` becomes dead code; delete its caller graph too.

### S7 (QUM-677): ThinkingItem
- Promoted from optional to required per the arc handoff. User-visible thinking blocks.
- Independent of S6 in principle but practically wants to land last so the contract is stable.

---

## Dispatch-prompt patterns that worked

- **Point at the Linear issue first.** Don't restate the slice contract in the prompt. Tell the engineer to read QUM-67N + the plan doc + the prior slice's commits + the S0 research doc, in that order.
- **List folded prep work explicitly.** If a backlog issue (e.g., QUM-684) is being absorbed into the slice, name it, link it, and tell the engineer its acceptance criteria are part of this slice's contract. Mark the backlog issue Done when its AC is met as a sub-step.
- **Bench gates as numbers.** S1 baselines: View ≤50 ms/op, Cold ≤1380 ms/op, Cached ≤1370 ms/op (the +30% gates). Include them verbatim — don't make the engineer derive them. The plan-doc's "≤2s startup regression" gate has a concrete proxy: the bench delta above + recover-live's `WAIT_FOR_PATTERN_ELAPSED` line (S1 added that instrumentation in `scripts/lib/e2e-common.sh`).
- **Mandatory validation list:** `make validate` + the e2e matrix rows whose guard files the slice touches + bench rerun + live install + dmotles eyeball. Spell out which install path (`/tmp/sprawl-qum6NN-vN/sprawl`).
- **Leave Linear In Progress until dmotles eyeballs.** Engineer flips to Done only after merge — manager (you) handles the merge + Linear Done after eyeball signoff.
- **Be explicit about retired peers.** finn (S1+S2+S3 implementer) and forge (B2 manager / QUM-669 author) are both retired. If your dispatch implies "ask finn/forge about X," they can't. Reference code directly.

---

## Verification heuristics (more than CLAUDE.md)

1. **Always re-run at least one e2e row yourself in the engineer's worktree** before merging, even if the engineer reports green. notify-tui caught a stale install once (S2 — finn's install binary was from a pre-final commit). Use `make test-e2e-matrix-<row>` in background, do other prep in parallel.
2. **Check the install binary's version string against the engineer's HEAD commit.** Run `/tmp/sprawl-qum6NN-vN/sprawl version` and confirm the commit hash matches. If it's stale, rebuild + reinstall yourself before pinging weave.
3. **Read the engineer's commit log, not just their report.** Multiple commits per slice are fine; what matters is whether the last commit aligns with the dispatched scope. Post-review commits (S3 had one — `a957e5d`) are common and good.
4. **Bench numbers in the completion message — verify in context.** If they list S1 baselines from memory and the numbers differ from yours, ask. QUM-685 (Cached bench discrepancy) is open precisely because that check caught a 700× anomaly.
5. **For eyeball-skip decisions:** valid only when the slice is unwired-by-construction (S1 case). Anything that touches a visible code path requires the install + dmotles ping.

---

## Pending items the next manager should track

- **QUM-685** (P3, Backlog) — `RenderMessages_LongHistory_Cached` bench reports ~1050 ms across S1/S2/S3 but original S1 report claimed ~1.49 ms. Likely either bit-rot in the originally-reported number or the bench-as-committed defeats the QUM-667 cache. **Doesn't block any slice** but the perf anchor is unreliable until resolved. Worth a 15-min `git bisect` if a researcher has cycles.
- **QUM-683** (P4, Backlog) — Document ChatList contract invariants in the plan doc. **Should land before S5 dispatches.**
- **QUM-678** (P4, Backlog) — Add `--pprof`/`SPRAWL_PPROF_ADDR` for live captures. Low-pri; surfaced during S0.
- **Tree-layer regression flagged by dmotles during S3 eyeball** — QUM-679's row-per-manager fix didn't actually fix the bug. weave is filing a new issue. Not B4's problem on the structural rewrite arc, but if it lands on `internal/tui/tree.go` or `internal/tui/app.go` in flight, it could collide with S4 streaming work. Coordinate with the tree-fix author.

---

## Recommended dispatch order for the next manager

1. **Spawn researcher for QUM-683 first** (~30 min, doc-only). Get the ChatList contract invariants written before S4 so S5's eventual implementer has a spec to read. Cheap insurance.
2. **Then dispatch S4 engineer for QUM-674** with the residue items (M1/L1/L2/L3 from S3) folded in as mandatory prep. Use the same pattern as S3: list the backlog items, name their AC, mark them done as sub-steps.
3. **Bench gate for S4:** same +30% as S3 (View ≤50, Cold ≤1380, Cached ≤1370). Plus — **load-bearing** — require the new `BenchmarkChatList_Render_LongHistory_*` to land and have its own initial baseline captured in the completion comment. Future slices gate against that.
4. **Per-slice validation:** `make validate` + notify-tui + drain-row-inject + recover-live + new ChatList bench. S4 is the first slice where you can drop the legacy-viewport bench from the gate (streaming routing moves cleanly into ChatList).
5. **Install path:** `/tmp/sprawl-qum674-v1/sprawl`. Dmotles eyeball mandatory — first slice where streaming items render through ChatList live.

---

## Tacit context

- **finn was the implementer for S1, S2, and S3.** Sprawl recycled the name across spawns; the agent has no shared memory across retirements. Don't assume continuity in the engineer.
- **dmotles is on a live session and watching.** Daily-driver guarantee is real. If a slice ships a visible regression, dmotles will notice within hours.
- **weave is the integration gatekeeper to main.** Don't pull to main yourself. Pull-to-main is weave's call, after your in-tree merge and dmotles signoff.
- **The dual-append shim is the architectural load-bearing element through S2–S5.** Don't be tempted to delete it early. S6 deletes it intentionally.
