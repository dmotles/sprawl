# Lost Commits Audit — 2026-04-21 repo nuke

**Author:** ghost (researcher agent)
**Date:** 2026-04-21
**Event:** `/home/coder/sprawl` was deleted and re-cloned at **2026-04-21T06:40:34Z**.
**Current main tip:** `9298045` (`finn: Fixed data race in cmd/agentloop.go …`).
**Remote state at audit time:** `origin` has only `refs/heads/main`. No feature branches survived on the remote.
**Reflog state:** Post-clone reflog shows only the clone event — the pre-wipe reflog did not survive.

## Summary

- **15 unique feature branches** had commits authored in agent worktrees over the recent work window (inspected jsonl sessions modified since 2026-04-18).
- **14 of those branches are already squash-merged into `origin/main`** — the original per-branch commit SHAs are gone (branches were never pushed; merge was squash), but **the content is preserved** in main's history under different SHAs. No action required.
- **1 branch is genuinely lost**: `dmotles/enter-ctrlc-resume` @ `4ac1b17` (finn, 9-file fix to clean-shutdown behavior in `sprawl enter`). This commit was made at 06:36:34Z — **~4 minutes before the wipe** — and was never merged or pushed.
- **Severity: LOW–MEDIUM.** Only one commit of real work is gone. It is **fully reconstructible** from the surviving Claude session jsonl (every `Edit` call is preserved in the transcript, and the commit message is captured verbatim).

No `git push` calls were issued from any agent worktree session. This is expected: agent worktrees never push; weave integrates via `sprawl merge` (squash to main) and pushes tags/main from the root session.

## Per-branch breakdown

Legend: `✅ merged` = content is on `origin/main` via squash, so the original branch SHA is not recoverable but the work is preserved. `❌ LOST` = branch SHA missing from repo AND content not merged to main.

| Branch | Branch SHA(s) | Author | Subject | Status | Main squash SHA |
|---|---|---|---|---|---|
| `dmotles/fix-agentloop-stdout-race` | `e18a653` | finn | fix(cmd): serialize timestampWriter.Write with mutex to fix data race | ✅ merged | `9298045` (main tip) |
| `dmotles/scrub-linear-refs-from-prompts` | `1369442` | finn | Scrub hardcoded Linear references from shipped prompts | ✅ merged | `1a57380` |
| `dmotles/linear-ripout-research` | `80bdc26` | ghost | research: concrete edit plan for Linear→GitHub migration | ✅ merged | `488c52d` |
| `dmotles/tui-session-boundary` | `2dc75d1` | finn | tui: make session boundary visible on restart | ✅ merged | `a6af6df` |
| `dmotles/qum-265-command-palette` | `a613703` | finn | QUM-265: TUI command palette with /exit, /help, /handoff | ✅ merged | `27c6a42` |
| `dmotles/qum-263-sprawl-handoff-mcp` | `0da0dcc` | finn | QUM-263 add handoff MCP tool | ✅ merged | `cc22c73` |
| `dmotles/qum-262-supervisor-methods` | `b4e4f82` | finn | feat(supervisor): implement Spawn/Merge/Retire/Kill via agentops | ✅ merged | `01cabe5` |
| `dmotles/qum-264-transcript-replay` | `f95a213` | ratz | QUM-264: replay Claude session JSONL into TUI viewport on resume | ✅ merged | `a24daec` |
| `dmotles/qum-259-phase4-tui-handoff-restart` | `98cc35b` | finn | QUM-259 Phase 4: Wire TUI handoff + in-process restart | ✅ merged | `8f8d5bc` |
| `dmotles/qum-257-phase3-tui-prepare` | `18847e3` | finn | QUM-257 Phase 3: wire TUI sprawl enter through rootinit.Prepare | ✅ merged | `75eaa18` |
| `dmotles/qum-256-phase2-weave-lock` | `4225042`, `721068e` | finn | QUM-256: weave.lock flock (+ gofrs/flock refactor) | ✅ merged | `20634ff` |
| `dmotles/qum-255-phase15-resume-by-default` | `a0c6226` | finn | QUM-255 Phase 1.5 — resume-by-default in rootinit.Prepare | ✅ merged | `b894764` |
| `dmotles/qum-254-phase1-rootinit` | `2576566`, `f466225` | finn | QUM-254: Extract internal/rootinit/ (+ smoke-test rebrand fix) | ✅ merged | `23705de` |
| `dmotles/qum-252-tui-weave-unification-research` | `a14c37a` | ghost | docs(design): add TUI/tmux weave init unification plan | ✅ merged | `1ffa1d8` |
| **`dmotles/enter-ctrlc-resume`** | **`4ac1b17`** | **finn** | **fix(enter): ctrl+c no longer triggers handoff-finalize or kills agents** | **❌ LOST** | — |

Verification performed: `git cat-file -e <sha>` for every branch SHA above — all return "missing" (confirms squash-merge path, not recoverable by SHA). Cross-reference against the current `git log --all --oneline` confirms the 14 ✅ rows by subject/author match and the single ❌ row has no corresponding commit on main.

## The one lost commit, in detail

**Commit:** `dmotles/enter-ctrlc-resume 4ac1b17` — *fix(enter): ctrl+c no longer triggers handoff-finalize or kills agents*
**Author:** finn
**Session:** `~/.claude/projects/-home-coder-sprawl--sprawl-worktrees-finn/dac9ea08-c407-4fc9-baa3-c4cfae5d7914.jsonl`
**Committed at:** 2026-04-21T06:36:34.465Z (4 min 0 sec before the wipe)
**Session ended at:** 06:39:58Z (ran out during E2E testing; all subsequent tool calls were `Bash`/`Read`/`Glob` for validation — no further `Edit`/`Write`, so nothing uncommitted was lost)
**Diffstat:** 9 files changed, 155 insertions(+), 60 deletions(-)

**Files modified** (all reconstructible from the `Edit` tool calls in the session jsonl between 06:32:34Z and 06:35:54Z):

- `cmd/enter.go`
- `cmd/enter_test.go`
- `internal/rootinit/deps.go`
- `internal/rootinit/init.go`
- `internal/rootinit/init_test.go`
- `internal/rootinit/postrun.go`
- `internal/rootinit/postrun_test.go`
- `internal/rootinit/spinner.go`
- `internal/rootinit/spinner_test.go`

**Commit message (verbatim from jsonl):**

```
fix(enter): ctrl+c no longer triggers handoff-finalize or kills agents

Punchlist #4 + #5. Ctrl+C on `sprawl enter` was running
rootinit.FinalizeHandoff on clean shutdown, which clears last-session-id
when a handoff-signal file is present — breaking resume-by-default
(QUM-255) on the next launch. It was also unconditionally killing every
child agent, surprising users who expect `sprawl enter` to be a
detachable UI.

Changes:
- cmd/enter.go runEnter: drop the clean-shutdown FinalizeHandoff call
  and the agent-killing loop. Keep sup.Shutdown() (noop for Real).
  /handoff path still runs finalize via makeRestartFunc — unchanged.
- rootinit: add Deps.LogPrefix (default "[root-loop]") and thread it
  through postrun.go, init.go, and the spinner so TUI-mode messages
  read "[enter] ..." instead of "[root-loop] ..." after the alt-screen
  tears down.
- cmd/enter.go newSession/finalize deps set LogPrefix="[enter]".
- Tests: TestEnter_CleanShutdown_DoesNotKillAgents asserts zero kills;
  TestEnter_ShutdownPath_DoesNotCallFinalizeOnCleanExit asserts finalize
  isn't invoked; new rootinit tests cover the TUI-mode prefix.
```

Referenced as "Punchlist #4 + #5" — there is no `punchlist.md` on current main, and the file was not touched in this session. The referenced punchlist lives in an earlier weave session's memory; see *Gaps* below.

## Recommended recovery actions

1. **Re-run finn against the same task.** Spawn a new finn engineer with a prompt pointing at the finn session jsonl and the commit message above; finn can replay the 9 `Edit` operations deterministically. This is the cleanest path — no squash-merge provenance weirdness, tests included, and finn already demonstrated the design works (`make validate` passed before the commit).
   - Concretely: `cp` the jsonl to `.sprawl/agents/<finn>/reference.jsonl` and tell finn: *"recreate commit 4ac1b17 on branch `dmotles/enter-ctrlc-resume`; the full edit sequence is in this transcript between timestamps 06:32:34Z and 06:35:54Z. Commit message attached below."*
2. **Alternative — mechanical reconstruction.** A scripted extractor over the jsonl (`jq` for Edit tool_use blocks) can rebuild the file diffs, since `Edit` entries contain exact `old_string` / `new_string` pairs. This is lossless but you still need to validate and run `make validate`.
3. **No action for the 14 merged branches.** Content is in `origin/main`. The missing branch SHAs are cosmetic — every squash-merge in this repo discards originals by design.
4. **Verify resume-by-default still works without the fix.** Until the commit is re-landed, Ctrl+C from `sprawl enter` will (per the commit message) run `FinalizeHandoff` on clean shutdown, which can clear `last-session-id` and break the next resume. Consider warning users or avoiding Ctrl+C until the fix is back.
5. **Investigate what caused the wipe.** Out of scope for this audit, but worth an incident note — the re-clone removed unpushed branches with no warning. Consider adding a pre-wipe push hook or at least a pre-merge safety check in `sprawl retire`.

## Gaps / uncertainties

- **Punchlist referenced by the lost commit.** The message mentions "Punchlist #4 + #5". There is no `punchlist.md` in the current repo, and this session didn't touch one. It likely lived in a weave session's memory (MCP `memory` tool, not disk). I did not trace it to its source; if recovery matters, grep the pre-wipe weave jsonls (`-home-coder-sprawl/*.jsonl`) for "Punchlist" and "#4" / "#5" to find what the other items were (and whether any are also unlanded).
- **Weave tool activity is opaque to this audit.** Weave uses in-process MCP tools (`mcp__sprawl__*`) rather than raw Bash/Edit. Any decisions/state weave held about in-flight work (e.g. "merge finn's branch now") isn't surfaced by searching for `git commit`. I assumed weave's merge activity is what produced the 14 ✅ main commits; cross-checking the weave jsonls (`-home-coder-sprawl/b7427922…`, `c79aee5d…`) would harden this.
- **Other jsonl projects not exhaustively searched.** I limited scope to `-home-coder-sprawl*` slugs. Two projects I did *not* deeply inspect: `-home-coder-dendra--sprawl-worktrees-*` (old dendra rebrand predecessors — likely stale) and `-tmp-sprawl-*` (sandbox/test runs — not real work). If any agent ran from one of those against this repo, their commits would be missed. Quick spot-check suggests these are test environments, not production.
- **Non-committed work files.** Aside from the punchlist above, I found no evidence of uncommitted design docs, research notes, or scratch files sitting in agent worktrees at the time of the wipe. The `Edit`/`Write` activity in the only session cut short (dac9ea08) was all inside the commit.
- **chip / tower / zone worktrees.** Those projects exist under `~/.claude/projects/` but had no jsonl files modified since 2026-04-18 in this search window, so they contributed no commits. If the user expected work from them, that's a flag to check — though their absence from this audit is consistent with them being idle.

## Reflection

- **Surprising:** how little was actually lost. Given "repo got nuked," I expected a messier picture. The squash-merge-and-delete-branch discipline that weave has been enforcing is what saved this — 14 out of 15 branches were drained into `origin/main` before the wipe. The process works.
- **Open questions:** (1) What caused the wipe? A `rm -rf` from a stray agent? A `sprawl retire --abandon` gone wrong? (2) The mysterious "Punchlist #4 + #5" — what's on items #1-#3 and #6+, and are any of those also unlanded? (3) Is there a checkpoint/backup layer we should add so unpushed branches survive a re-clone?
- **Next steps if I had more time:** (a) Full trace of the weave session to reconstruct the punchlist and verify finn's "enter-ctrlc-resume" was indeed queued for merge, not held back for review. (b) Write a one-shot jsonl-to-patch extractor as a generic recovery tool for this team. (c) Audit `sprawl retire` and `sprawl merge` for any path that could cause a silent repo wipe.
