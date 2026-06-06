# M15 Phase Relevance Review — Post-B4 Structural Rewrite

**Date:** 2026-06-06
**Reviewer:** ghost (research agent)
**Scope:** 6 backlog M15 issues (QUM-649, 651, 652, 653, 654, 655) reviewed against current `main` after the B4 structural-rewrite arc (QUM-670…677, plus 692/695) landed.

## TL;DR

| Issue | Verdict | One-liner |
|---|---|---|
| QUM-649 — Toast subsystem | **MODIFY** | `transientLabel` (S5) already provides single-slot ephemeral status; toasts now only needed for *sticky condition-based* + *stacking* cases (faults, MCP slow, recovery). Scope shrinks ~40%. |
| QUM-651 — Lifecycle (handoff overlay, recovery toast, interrupt UX) | **MODIFY** | "Remove inline banners" goal is **done** via S5 transientLabel routing. Remaining real work: handoff progress overlay modal + "standby for restart" agent state. Scope shrinks ~60%. |
| QUM-652 — Modal migration | **MODIFY → fold into QUM-655** | All 6 modals already render as floating overlays in a single-column layout. Reduce to a documentation/audit sweep; absorb into Phase 9 cleanup. |
| QUM-653 — Remove mouse capture + keyboard scroll | **KEEP** | Mouse capture is *still on*; `selectionMode`/`Ctrl-_` toggle still present; help text still mentions it. Keyboard scroll already wired in ChatRegion. Pure deletion task — small. |
| QUM-654 — Ctrl+R reverse-search | **CANCEL** | Implemented by **QUM-410** (`internal/tui/history.go` + `app.go` `searchActive`/`searchOverlay`). Minor spec deltas (cap 1000 vs 10k; mode 0644 vs 0600) → file a small follow-up if desired. |
| QUM-655 — Phase 9 cleanup | **KEEP, re-scope blockers** | Cleanup is still wanted, but its blocked-by list is now mostly satisfied or modified. Drop QUM-654 from blockers; treat QUM-652 as absorbed; QUM-693 (ViewportModel facade deletion) lives separately. |

---

## Methodology

For each issue I (a) fetched the Linear description via `mcp__linear__get_issue`, (b) grepped current `main` for the symbols / strings / files the spec calls out, and (c) compared what the B4 arc absorbed against what the spec still demands.

Key reference points in current code:

- `internal/tui/statusbar.go:80-95, 236-249` — `transientLabel` field + `SetTransientLabel` setter (the S5 single sink).
- `internal/tui/viewport.go:1-25` — surviving ~423 LOC compatibility facade over ChatRegion/ChatList (QUM-693 covers its removal).
- `internal/tui/chatregion.go`, `chatlist.go` — sole render path; owns PgUp/PgDn/Home/End scroll.
- `internal/tui/app.go:1953-1957` — `mouseMode()` returns `tea.MouseModeCellMotion` (mouse capture *still on*).
- `internal/tui/app.go:283-288, 516-526` + `statusbar.go:39-42, 119-121, 214` — `selectionMode` + `Ctrl+_/Ctrl+/` toggle still wired.
- `internal/tui/help.go:34-46` — Ctrl+_, Ctrl+R both listed.
- `internal/tui/history.go:1-46` — input history; file at `<root>/.sprawl/input-history`; cap 1000; perm 0644.
- `internal/tui/app.go:2384-2462` — `handleSearchKey`, `searchOverlay`, `refreshSearchMatch` (QUM-410).
- `internal/tui/palette.go`, `help.go`, `error_dialog.go`, `confirm.go`, `question.go`, `validate_popup.go` — all render as floating bordered overlays via `lipgloss.Place`/`Border`.

---

## QUM-649 — Toast notification subsystem with dual dismissal

**Verdict: MODIFY (scope shrink ~40%)**

### Evidence from current main

- **No `toast`/`Toast` symbol exists** in `internal/tui/` — `grep -i toast` returns zero matches.
- The status-bar `transientLabel` field (S5/QUM-675, `statusbar.go:80-95`) is a *single-slot* last-write-wins sink used today for:
  - "Interrupting…" / "Interrupt failed: …" / "Interrupt sent — waiting for turn to end" (`app.go:628, 1086, 1088`)
  - "/handoff dispatched — see output below" (`app.go:696`)
  - "Session restarting (%s)…" / "queued message dropped due to session restart" (`app.go:943, 951`)
  - "Consolidation failed: …" / "Consolidation complete (%ds)" (`app.go:1000, 1002`)
  - "[startup] resumed %d agents" / "[startup] resumed %d agents (%d failed)" (`app.go:1247-1249`)
  - "backend recovered on %s" (`app.go:1218`)
  - "✓ resynced — recovered %d events from session log" (`app.go:1473`)
  - "Completed in %dms, cost $%.4f" / "Interrupted (%dms)" (`app.go:864, 889`)
  - Inbox banners, gap-detected warnings, etc.
- All of those used to be inline banners in the viewport; S5 routed them OUT of the chat list (the "contract violators" rerouting). `transientLabel` is now the canonical sink.

### What the original spec assumed vs. what we now have

The original spec assumed *no* status surface existed and toasts had to handle:
1. one-shot status (Completed/Interrupted/recover-result)
2. sticky condition-based dismissal (fault / MCP slow / handoff progress)
3. stacking
4. user-dismiss chord

(1) is already covered by `transientLabel`. What we genuinely still lack: **(2) sticky condition-based items**, **(3) concurrent stacking**, and **(4) a user-dismiss key**.

### Scope-edit recommendations

- **Remove from spec:** the implicit requirement that toasts replace simple one-shot status text. `transientLabel` keeps that job; toasts coexist alongside it. State in the issue: "Toasts do **not** replace `statusBar.SetTransientLabel`; toasts are for sticky / multi-concurrent / user-dismissible surfaces."
- **Add to spec:**
  - An explicit migration list of which existing `SetTransientLabel` call-sites in `app.go` *should* become toasts vs. stay (suggested toasts: backend-fault, MCP-slow warning, multi-agent recover summary; suggested stay: completion timing, /handoff dispatched).
  - A clear "modal > toast > transientLabel > chat-list" stacking order in `View()` composition.
- **Reorder API:** the `Toast.DismissOn` contract should include the existing `transientLabel` semantics (last-write-wins) as a degenerate case, so the underlying primitive can power both surfaces if we ever consolidate.

### Dependencies

- Original blockers (none) still accurate.
- Original blocks: QUM-651, QUM-655 — still accurate but QUM-651's reliance on toasts is smaller (see below).

### Estimated size / risk

- Original spec ~1 eng-day.
- Modified spec ~0.5 eng-day. Risk low — purely additive overlay layer; no existing surface gets ripped out.

---

## QUM-651 — Lifecycle: handoff overlay, recovery toast, interrupt UX, standby-restart state

**Verdict: MODIFY (scope shrink ~60%)**

### Evidence from current main

| Sub-goal | Status |
|---|---|
| Remove inline "Session restarting…" banner from viewport | **DONE** — S5 routes MessageStatus/Banner/Error/System out of chat list; restart status now shows via `statusBar.SetTransientLabel("Session restarting (%s)…", reason)` (`app.go:943`). |
| Remove inline interrupt status | **DONE** — interrupt status now shows via `SetTransientLabel("Interrupting…")` and `"Interrupt sent — waiting for turn to end"` (`app.go:628, 1088`). |
| Remove inline recover banner | **DONE** — `AgentsResumedMsg` handler routes "[startup] resumed N agents" to `SetTransientLabel` (`app.go:1238-1249`); per-agent recover via `"backend recovered on %s"` (`app.go:1218`). |
| Floating handoff overlay modal with progress | **NOT DONE** — handoff dispatch sets `transientLabel("/handoff dispatched — see output below")` and emits `SessionRestartingMsg`, but there is no animated progress modal. |
| New "standby for restart" agent state | **NOT DONE** — `grep -ri standby internal/` returns no Go hits; only mentioned in design docs. |
| Condition-based dismissal for fault / MCP-slow toast | **NOT DONE** — depends on QUM-649. |

### Scope-edit recommendations

Reduce the issue to two crisp deliverables:

1. **Handoff overlay modal** — floating bordered modal that animates through the "consolidating sessions … writing handoff … restarting …" phases. Replaces the current `transientLabel("Session restarting …")` for handoff-initiated restarts. Note: `transientLabel` is already adequate for *non-handoff* restart paths; keep it for those.
2. **"Standby for restart" agent state** — add to `internal/state/state.go`, set on `handoff` MCP tool return, cleared on restart begin or cancel. Propagate to TUI for an `[STANDBY]` adornment in the tree pill / status indicator.

Move the rest:
- Recovery toast → covered by QUM-649's migration list (recover events are an obvious "promote to toast" target).
- Interrupt UX → already done via transientLabel; remove from this issue.
- Terminal-error toast → covered by QUM-649.
- Sub-agent rendering decision → already settled by B4 (S7/QUM-677 promoted ThinkingItem; tool-call rendering is now per-Item). The "Task tool" branch can be dropped from this issue.

### Dependencies

- **Blocked-by:** drop QUM-650 (already done — viewport is gone, ChatList sole path). Keep QUM-649 only if the recovery toast / fault toast wiring is part of this issue's deliverable; otherwise this issue no longer blocks on QUM-649.
- **Blocks:** QUM-655 — still accurate.

### Estimated size / risk

- Original ~1.5 eng-day.
- Modified ~0.7 eng-day. Risk: medium for the "standby for restart" state plumbing (touches supervisor + state + tree liveness — exercise the `recover-live` and `handoff` e2e matrix rows).

---

## QUM-652 — Modal migration: palette, help, validate-popup, error, confirm

**Verdict: MODIFY → fold into QUM-655**

### Evidence from current main

All 6 modals already render as floating bordered overlays in the single-column layout. Specifically:

- `internal/tui/help.go:73-81` — `lipgloss.NewStyle().Border(RoundedBorder())…` then `lipgloss.Place(width, height, Center, Center, box)`. Already approach (b).
- `internal/tui/palette.go` — `PaletteModel` is described in the source as "floating centered command palette overlay" (palette.go:25-28). Already approach (b).
- `internal/tui/question.go` — overlay, gated by `showQuestion` (app.go:159-167).
- `internal/tui/validate_popup.go` — small overlay with state machine; *already* "non-fullscreen near-trigger, auto-dismiss on completion" per spec — done by QUM-588.
- `internal/tui/error_dialog.go`, `confirm.go` — both bordered overlays.

There is no surviving multi-pane modal positioning logic; `app.go`'s overlay composition routes all of these via the View() `overlay` slot.

### Scope-edit recommendations

- Close this issue as effectively done, OR re-scope to a one-paragraph audit folded into QUM-655's "documentation" sweep:
  - For each modal, add a 2-3 line code comment explaining its chosen approach (overlay vs inline) per the design doc taxonomy.
  - Ensure `View()` composition orders modals **above** any future toasts (per QUM-649's deliverable). This is a 1-line invariant test in `app_test.go`.

### Dependencies

- Blocked-by QUM-650 — already done.
- Blocks QUM-655 — keep, but folding makes this trivial.

### Estimated size / risk

- ~0.1 eng-day if folded into QUM-655. Risk near-zero.

---

## QUM-653 — Remove mouse capture + add keyboard scroll bindings

**Verdict: KEEP (small, well-scoped deletion task)**

### Evidence from current main

Mouse capture is **still on**:

```
internal/tui/app.go:1953-1957
func (m AppModel) mouseMode() tea.MouseMode {
    if m.selectionMode { return tea.MouseModeNone }
    return tea.MouseModeCellMotion
}
```

The `Ctrl+_` (and `Ctrl+/`) selection-mode toggle is still wired:

- `app.go:283-288` — `selectionMode` field.
- `app.go:516-526` — `Code: '_', Mod: ModCtrl` handler.
- `statusbar.go:39-42, 119-121, 214` — `selectionMode` field, "-- SELECT (mouse capture off) — Ctrl-/ to resume --" left-segment, `SetSelectionMode`.
- `help.go:35` — `"Ctrl+_ (or Ctrl+/)"` row.
- `CLAUDE.md` "Text selection in `sprawl enter` (QUM-617)" section — describes this behavior.

Keyboard scroll is **already wired** in ChatRegion (per QUM-650/676 — see `chatregion.go`, referenced by `app.go` and `help.go` lists PgUp/PgDn).

### Scope-edit recommendations

The original spec is still accurate. Concrete edits to the affected files (essentially a delete-only diff):

- `app.go:1953-1957` — replace body with `return tea.MouseModeNone`. Delete `mouseMode()` entirely if call sites can be inlined.
- `app.go:283-288` — remove `selectionMode` field.
- `app.go:516-526` — remove the `Code: '_', Mod: ModCtrl` branch.
- `statusbar.go:39-42` — remove `selectionMode` field.
- `statusbar.go:119-121` — remove the SELECT banner branch.
- `statusbar.go:214` — remove `SetSelectionMode`.
- `help.go:35` — remove Ctrl+_ row.
- `CLAUDE.md` — rewrite the "Text selection in `sprawl enter`" section per Phase 7 spec.
- Remove the corresponding test cases in `app_test.go:1825-1898` (TestAppModel_View_EnablesMouseCellMotion, ...StillSetWhenTooSmall, MouseModeToggle, selectionMode-related, etc.).

### Dependencies

- Blocked-by QUM-650 — already done.
- Blocks QUM-655 — keep.

### Estimated size / risk

- ~0.3 eng-day. Risk low. Run full `make test-e2e-matrix` since touch is broad; in particular the `notify-tui` row checks input/View() rendering.
- One subtle concern: with mouse capture off, mouse-wheel events fall through to the terminal. Verify in tmux + bare ssh terminal that this is acceptable for the chat-list reader experience (PgUp/PgDn are documented; users may still reach for the wheel reflexively).

---

## QUM-654 — Ctrl+R persistent input reverse-search across sessions

**Verdict: CANCEL (already implemented as QUM-410)**

### Evidence from current main

The feature exists end-to-end:

- `internal/tui/history.go` — full implementation. `NewHistory(sprawlRoot)` → file at `<sprawlRoot>/.sprawl/input-history`. JSON-encoded lines, capped at `defaultHistoryCap = 1000`. Consecutive-duplicate and empty-string dedup (see header comment).
- `internal/tui/app.go:240-244, 415-425, 554-555, 2384-2462` — `searchActive`, `searchQuery`, `searchMatchIdx`, `searchPriorInput`. `handleSearchKey`: Ctrl+R cycles next-older; Enter accepts; Esc/Ctrl+C cancel-restore; Backspace shrinks.
- `app.go:2454-2462` — `searchOverlay()` renders `"(reverse-i-search)`<q>': <match>"` — the exact bash style the spec asks for.
- `app_history_test.go` — `TestCtrlR_StateMachine` and friends already cover the state machine.
- `help.go:44-45` — "Ctrl+R" / "Esc (search)" rows already in the help overlay.
- `app_resync_test.go:273-300` — `TestAppModel_CtrlL_DoesNotConflictWithReverseSearch` verifies precedence.

### Spec-vs-implementation deltas

| Spec | Implementation | Action |
|---|---|---|
| Retention ~10k entries | `defaultHistoryCap = 1000` | If 10k is desired, bump and ship as a 1-line follow-up. |
| File mode 0600 | `historyFilePerm = 0o644` | If 0600 is desired, tighten in a follow-up. Justifiable since input prompts can contain sensitive text. |
| `.sprawl/memory/input-history` or `.sprawl/input-history` | `<root>/.sprawl/input-history` | Matches one of the spec choices. ✓ |
| `.gitignore` entry | unverified | Check; `.sprawl/` itself is typically gitignored, so likely fine. |

### Recommendation

- **Close QUM-654 as duplicate of QUM-410.** Link them in Linear.
- **File one small follow-up issue** (low priority) covering: bump cap to 10k, tighten file mode to 0600, confirm `.gitignore` covers it. Probably ~0.1 eng-day.

### Dependencies

- Blocked-by QUM-650 — already done.
- Blocks QUM-655 — once closed, drop from QUM-655's blockers list.

### Estimated size / risk

- Original ~1 eng-day → 0.

---

## QUM-655 — Phase 9 cleanup: delete deprecated paths and finalize redesign

**Verdict: KEEP (re-scope blockers and remove items already absorbed)**

### Evidence from current main

The deletion checklist in the spec is partially obsolete or already done by B4:

| Spec deletion target | Current state |
|---|---|
| Leftover `activity.go` / `activity_stream.go` stubs | Files do not exist in `ls internal/tui/`. **Done.** |
| Old `internal/tui/viewport.go` | **Still present** as a ~423 LOC compatibility facade — but **owned by QUM-693**, not this issue. |
| Pre-Phase-7 mouse capture handlers | **Still present** — to be removed by QUM-653. |
| Pre-Phase-5 inline lifecycle banner code | **Done** by S5 (see QUM-651 evidence). |
| Pre-Phase-6 multi-pane modal positioning | **Done** — see QUM-652 evidence. |
| Unused `*Msg` types | Likely some remain; audit needed. |

### Scope-edit recommendations

- **Update blocked-by list:** drop QUM-654 (closed). Keep QUM-649, QUM-651, QUM-653. Add explicit *non-blocker* note for QUM-693 (parallel facade deletion).
- **Fold QUM-652** into this issue's documentation sweep (per QUM-652 verdict above).
- **Add new bullet to the deletion checklist:** "Once QUM-693 ships, drop the `internal/tui/viewport.go` facade and the legacy test seams it exposed (`AppendMessage*`, `GetMessages`, `SetMessages` etc.)."
- **Add new bullet to the reflection sweep:** "Walk B4 arc issues (QUM-670…677, QUM-692, QUM-695) — their commenters may have flagged residual cleanup items."

### Dependencies

- Updated blocked-by: QUM-649, QUM-651, QUM-653 (and conceptually QUM-693 if we want viewport.go gone before final close).

### Estimated size / risk

- Original ~1 eng-day → ~0.6 eng-day (less scope; B4 already swept a lot). Risk low.

---

## Cross-cutting observations

1. **`transientLabel` quietly absorbed half of M15.** The S5 routing decision (QUM-675) effectively delivered the lifecycle-banner-removal goal of QUM-651 *and* gave us a single-slot status surface that softens the urgency of QUM-649's toast subsystem. Worth noting in the M15 milestone description so the remaining issues read coherently.
2. **QUM-410 → QUM-654 duplicate.** A direct duplicate that nobody linked at filing time. Worth a habit of cross-referencing existing implementations during M15 backlog scrubs.
3. **Modal migration was a non-event.** The spike-validation requirement assumed modals would visually break in the new layout; the actual transition just worked because the modals were already using `lipgloss.Place(…, Center, Center, …)` overlays. Save the scoping cycles next milestone by sanity-checking the assumption up front.
4. **Mouse-wheel UX after capture removal.** When QUM-653 ships, mouse-wheel events fall through to the terminal/tmux. For sprawl-the-product this is the right call, but it's a *behavior* change worth a one-line release note alongside the change.

## Open questions / quick-experiment proposals

- **Q1:** Does anyone currently rely on the `Ctrl+_` selection toggle in daily use? If so, the QUM-653 deletion is a behavior regression for them. Quick check: `git log --all --oneline -- internal/tui` for references; ask dmotles.
- **Q2:** Should `transientLabel` and Toast share an implementation, or stay separate primitives? Worth a 30-min spike inside QUM-649 to prototype both options before committing.
- **Q3:** Is "standby for restart" needed on the `AgentState` enum, or can the TUI infer it from `handoff_dispatched_at` + `restart_started_at` timestamps already plumbed through? A quick read of `internal/state/state.go` would settle this before QUM-651 starts.

## Reflection (what surprised me / what I'd dig into next)

- **Surprise #1:** how completely `transientLabel` ate the lifecycle-banner removal work. I expected QUM-651 to be 80% real work; it's more like 40%.
- **Surprise #2:** QUM-654 is a direct duplicate of an issue (QUM-410) that's been shipped — the backlog wasn't audited against the existing codebase before being filed.
- **Surprise #3:** modal migration was effectively complete *before* B4 even started — the spike-validation worry was over-cautious.
- **Open question I'd chase with more time:** is the `viewport.go` facade actually safe to delete *now* (QUM-693 timing) — i.e. how many test files would need rewriting? A `grep -rl "ViewportModel\|AppendMessage\|GetMessages" internal/tui/*_test.go | wc -l` would scope that quickly.
- **Open question #2:** the design doc's "load-bearing viewport contract" is currently enforced by routing rules (S5). If we add toasts, we need an explicit doc bullet that toasts don't violate the contract (they're agent-visible UI for things the agent can't see). Worth writing into `chatlist-invariants.md`.
