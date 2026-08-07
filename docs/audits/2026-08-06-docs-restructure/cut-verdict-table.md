# QUM-1155 — section-by-section cut verdict table

**Everything from the `# QUM-1155 cut verdict table

Original: `git show c7093cc:CLAUDE.md`, sha256 `228ffaee340f2322c11c25a1a7310c207f69596b39e38cdf99b82233d1eaffae`, 938 lines, 750 non-blank, 188 blocks.

Verdicts: 188/188 accounted (135 by byte-match against the destination, 53 by recorded manual verdict). Unaccounted: 0. Stale overrides: 0.

Every non-blank line of the original is inside exactly one row, asserted mechanically above, so a block cannot be dropped from this enumeration without the generator failing.

**6 blocks carry a `+corrected` verdict** — relocated AND deliberately changed at the destination because the original text was wrong. That is a different claim from "moved", so it is spelled differently. This figure is derived from the verdicts below, not typed: a count asserted in prose in front of the thing it describes is the exact artifact this restructure exists to retire.

| lines | first line | verdict | basis |
|---|---|---|---|
| 1-1 | # CLAUDE.md | retained | byte-match against the destination |
| 3-3 | Read `DESCRIPTION.md` for project context. This file covers how to work in this codebase. | retained:reworded | The DESCRIPTION.md pointer. Rewritten so it is a pointer rather than a mandated read: the budget resolver flagged the original as a read-instruction violation, and @-importing DESCRIPTION.md would add 195 lines to the surface being bounded. |
| 5-5 | ## Terminology | retained | byte-match against the destination |
| 7-9 | - **agent** — a sprawl-spawned process with its own worktree and its own Claude session. | retained | byte-match against the destination |
| 11-11 | These three are distinct. "Sub-agent" must never refer to a Claude Agent-tool spawn — use  | retained | byte-match against the destination |
| 13-13 | ## Lifecycle model (QUM-786) | moved:sprawl-internals | byte-match against the destination |
| 15-17 | Authoritative rules for agent Status / `IsTerminal` / wake plumbing. If you | moved:sprawl-internals | byte-match against the destination |
| 19-43 | - `StatusComplete` ("complete") is the **resting state after `state:complete`** | moved:sprawl-internals | byte-match against the destination |
| 45-46 | Touched-file matrix-row mapping for these set-sites lives in the table | moved:sprawl-internals | The lifecycle cross-reference. Deliberately REWRITTEN at the destination to cite the e2e-matrix skill by path instead of CLAUDE.md's own '## Validating Changes' table, which no longer exists. Pinned by TestSprawlInternalsSkillRewritesLifecycleCrossReference, which asserts both halves. |
| 48-48 | ## Build & Test | moved:sprawl-internals | byte-match against the destination |
| 50-67 | ```bash | moved:sprawl-internals | byte-match against the destination |
| 69-70 | make test-wirelog-helpers-unit   # bash+jq unit tests for the e2e rows' wire-log | moved:sprawl-internals | byte-match against the destination |
| 72-74 | scripts/smoke-test-memory.sh   # integration test for weave memory system | moved:sprawl-internals | byte-match against the destination |
| 76-76 | ### What `make validate` guarantees about data races (QUM-972) | moved:testing-practices | byte-match against the destination |
| 78-84 | **It runs the whole unit suite under the race detector.** Until QUM-972 it did | moved:testing-practices | byte-match against the destination |
| 86-94 | **A race count is run-dependent — do not quote a bare total.** The detector | moved:testing-practices | byte-match against the destination |
| 96-96 | State the guarantee accurately, because it is narrower than "no races exist": | moved:testing-practices | byte-match against the destination |
| 98-104 | * **Covered** — every package under `./...`, on the code paths the unit tests | moved:testing-practices | byte-match against the destination |
| 106-112 | Cost, measured on a 4-core host with warm build caches (`-count=1`): `go test | moved:testing-practices | byte-match against the destination |
| 114-122 | `-race` needs cgo and a C toolchain. That fails **loudly** — the build is | moved:testing-practices | byte-match against the destination |
| 124-131 | **Repo-wide convention for duration test tunables:** a duration knob that | moved:testing-practices | byte-match against the destination |
| 133-139 | Two of those three were **fixes**; the third was **prevention**, and the | moved:testing-practices | byte-match against the destination |
| 141-146 | Snapshotting the var at goroutine entry does **not** fix it — the snapshot read | moved:testing-practices | byte-match against the destination |
| 148-148 | ## Commit guard (QUM-808) | moved:git-recovery | 'Commit guard (QUM-808)' heading, re-levelled to '## Guards: what stops you landing on `main` (QUM-808)'. Body auto-matched. |
| 150-153 | The pre-commit hook (`scripts/pre-commit`, installed via `make hooks`) runs | moved:git-recovery | byte-match against the destination |
| 155-158 | - `weave` (the root agent) — allowed to commit to `main`. | moved:git-recovery | byte-match against the destination |
| 160-163 | Because git worktrees share the common `.git/hooks` directory, the guard fires | moved:git-recovery | byte-match against the destination |
| 165-175 | **Installation.** The hook is **auto-installed on every agent worktree | moved:git-recovery | byte-match against the destination |
| 177-177 | ### Reference-transaction backstop (QUM-837) | moved:git-recovery | byte-match against the destination |
| 179-186 | The pre-commit guard above is **skippable by `git commit --no-verify`**, and | moved:git-recovery | byte-match against the destination |
| 188-200 | - **Identity semantics are identical to `guard-main-commit`**: `weave` (root) | moved:git-recovery | byte-match against the destination |
| 202-208 | Sprawl also enforces a **hook-independent** defense in depth: a non-root agent | moved:git-recovery | byte-match against the destination |
| 210-210 | ### Safe recovery from a wrong-tree commit on `main` | moved:git-recovery | Heading, re-levelled to '## A commit landed on `main` by mistake'. |
| 212-215 | If a commit ever lands on `main` by mistake, **do NOT have an agent run | moved:git-recovery+corrected | Wrong-tree recovery preamble. The destination deliberately REPLACES the `--soft` advice: in the main checkout HEAD *is* main, so --soft (and a bare update-ref) leaves the stray tree staged, to be silently re-landed by the next commit. The skill prescribes --mixed and says in as many words that the CLAUDE.md wording was wrong and must not be restored. The `reset --hard` prohibition survives intact. |
| 217-232 | 1. **Identify** the stray commit: `git -C <main-checkout> log --oneline -1 main`. | moved:git-recovery+corrected | The numbered recovery steps, reformatted into an annotated bash block and corrected per the entry above. Step 3 is now `reset --mixed`, and the verification changed from 'confirm status is clean' (unachievable) to an argument-order-checked `merge-base --is-ancestor <stray-sha> main`. |
| 234-235 | The guard makes this recovery a rare exception, not a routine: agents are | moved:git-recovery | 'the guard makes this recovery a rare exception' — the claim survives as the skill's framing of the guards section; the sentence itself was not carried. |
| 237-237 | ### Recovering a downstream branch after a squash-merge (QUM-1083) | moved:git-recovery | Heading, re-levelled; the QUM-1083 body auto-matched. |
| 239-245 | **The precondition.** Squash-merging a base branch to `main` replaces its | moved:git-recovery | 'The precondition'; verified de-wrapped ('Squash-merging a base branch to `main` replaces its commits'). |
| 247-257 | **Both natural checks lie, in opposite directions.** `git branch --contains | moved:git-recovery | 'Both natural checks lie, in opposite directions' — present under its own heading, reflowed. Verified de-wrapped: 'skipped previously applied commit' and the patch-id reasoning are both there. |
| 259-262 | **Prevent, don't recover.** When two branches share a base, either **merge the | moved:git-recovery | 'Prevent, don't recover'. Verified de-wrapped: 'When two branches share a base, either **merge the dependent one first**' is present verbatim. |
| 264-264 | **Step 1 — gate on the base being content-equivalent.** | moved:git-recovery | 'Step 1' bold lead, re-levelled to a '### Step 1 — gate on the base being content-equivalent' heading. |
| 266-268 | ```bash | moved:git-recovery | byte-match against the destination |
| 270-271 | If it is not empty, **stop**: the squash changed content, and the downstream | moved:git-recovery | byte-match against the destination |
| 273-273 | **Step 2 — cherry-pick the delta; do not rebase.** | moved:git-recovery | 'Step 2' bold lead, re-levelled to a heading. |
| 275-278 | ```bash | moved:git-recovery | byte-match against the destination |
| 280-283 | The range excludes the already-landed commits **by construction**, where a | moved:git-recovery | byte-match against the destination |
| 285-293 | **A conflict here does not mean step 1 failed.** Step 1 only establishes that | moved:git-recovery | byte-match against the destination |
| 295-295 | **Step 3 — verify the delta, not the absence of conflicts.** | moved:git-recovery | 'Step 3' bold lead, re-levelled to a heading. |
| 297-298 | If you branched off the squash commit and `main` has not moved since, the tree | moved:git-recovery | byte-match against the destination |
| 300-303 | ```bash | moved:git-recovery | byte-match against the destination |
| 305-307 | If `main` has advanced past the squash, that diff reports `main`'s later | moved:git-recovery | The main-has-advanced caveat; verified de-wrapped ('that diff reports `main`'s later commits'). |
| 309-312 | ```bash | moved:git-recovery | byte-match against the destination |
| 314-316 | Raw line counts here are dominated by blob `index` and `@@` header lines; to | moved:git-recovery | byte-match against the destination |
| 318-321 | **A clean cherry-pick is not evidence of an identical tree.** The wrong range | moved:git-recovery | 'A clean cherry-pick is not evidence of an identical tree'. Verified de-wrapped: 'The wrong range exits **0** with content silently missing' is present verbatim. |
| 323-323 | **Step 4 — commit, then check the parent.** | moved:git-recovery | 'Step 4' bold lead, re-levelled to a heading. |
| 325-328 | ```bash | moved:git-recovery | byte-match against the destination |
| 330-333 | **Run the parent check after committing, not before** — until you commit, the | moved:git-recovery | byte-match against the destination |
| 335-338 | **"Tree matches" is necessary and not sufficient.** A branch built on the | moved:git-recovery | byte-match against the destination |
| 340-348 | **Check that the question the command answers is the question you are | moved:git-recovery | 'Check that the question the command answers is the question you are claiming' — promoted to its own heading; body verified de-wrapped. |
| 350-352 | Finally, retire the original: once `<my-branch>-rebased` passes both checks, | moved:git-recovery | byte-match against the destination |
| 354-354 | ### The merge engine mutates the parent once, forward-only (QUM-1087) | moved:git-recovery | Heading, re-levelled; the QUM-1087 body auto-matched. |
| 356-362 | `sprawl merge` (and the `retire` MCP tool with `merge: true`, which routes | moved:git-recovery | byte-match against the destination |
| 364-364 | Two consequences worth knowing before reading the code: | moved:git-recovery | byte-match against the destination |
| 366-378 | * **`--ff-only` exiting 0 does not mean the parent moved.** It exits 0 without | moved:git-recovery | byte-match against the destination |
| 380-393 | **Accepted cost, recorded so it is not re-litigated: the agent's intermediate | moved:git-recovery | byte-match against the destination |
| 395-395 | ### Pre-merge recovery refs (QUM-1090) | moved:git-recovery | Heading, re-levelled; the QUM-1090 body auto-matched. |
| 397-399 | Every non-noop, non-dry-run `merge` writes two refs **before its first | moved:git-recovery | byte-match against the destination |
| 401-404 | ``` | moved:git-recovery | byte-match against the destination |
| 406-407 | Unlike reflog entries these survive `git gc`, survive branch deletion at | moved:git-recovery | byte-match against the destination |
| 409-412 | ```bash | moved:git-recovery | byte-match against the destination |
| 414-418 | **Both siblings matter, and a check that only looks at `/agent` is wrong.** | moved:git-recovery | byte-match against the destination |
| 420-433 | **The `/parent` ref survives QUM-1087, on a different argument.** After | moved:git-recovery | byte-match against the destination |
| 435-441 | `refs/sprawl/premerge/` is owned **exclusively** by this mechanism, so | moved:git-recovery | The refs/sprawl/premerge/ ownership rule; verified de-wrapped ('owned **exclusively** by this mechanism'). Reflowed and extended with the rescue/ and manual/ namespaces. |
| 443-446 | `sprawl gc` prunes these after `--premerge-retention-days` (default 14), | moved:git-recovery | The `sprawl gc` retention rule; present at the destination with 'these' reworded to 'those'. --premerge-retention-days, the 14-day default, ageing by the ref-name timestamp, and never-prune-an-unparseable-name all survive. |
| 448-448 | ### Never overwrite the thing that tells you where you were | moved:git-recovery | Heading, re-levelled to '## Never overwrite the thing that tells you where you were'. |
| 450-451 | One rule, four surfaces. It is worth seeing them as one, because each looks | moved:git-recovery | byte-match against the destination |
| 453-465 | 1. **Operator procedure** — when relocating a ref, create the replacement | moved:git-recovery | byte-match against the destination |
| 467-476 | Point 4 was demonstrated live, by accident, on the very merge the mechanism | moved:git-recovery | byte-match against the destination |
| 478-489 | The scope of that hazard is wider than "has anyone branched from it". | moved:git-recovery | byte-match against the destination |
| 491-492 | The timestamp in that name is **millisecond** precision, and the first live | moved:git-recovery | byte-match against the destination |
| 494-497 | ``` | moved:git-recovery | byte-match against the destination |
| 499-506 | Two merges of the same agent **83 milliseconds apart, inside the same | moved:git-recovery | byte-match against the destination |
| 508-510 | The incident is the point. Compressed to a maxim the rule reads as obvious | moved:git-recovery | byte-match against the destination |
| 512-512 | ### Never `git add -A` (QUM-989) | moved:git-recovery | Heading, re-levelled to '## Staging: never `git add -A` (QUM-989)'. |
| 514-515 | **Standing rule: stage explicit paths only.** Never `git add -A`, `git add .`, | moved:git-recovery | byte-match against the destination |
| 517-526 | The reason is specific to this repo, not general tidiness. Agent worktrees sit | moved:git-recovery | byte-match against the destination |
| 528-531 | The two `main` guards above do not help here: this is a **correct-branch, | moved:git-recovery | 'The two `main` guards do not help here' — the correct-branch/correct-identity/foreign-content point; verified de-wrapped. |
| 533-537 | `.gitignore` is a backstop, not the control — except for the binary artifact | moved:git-recovery | byte-match against the destination |
| 539-543 | ```bash | moved:git-recovery | byte-match against the destination |
| 545-547 | When the change is large, `git add -u` is the sanctioned shortcut: it stages | moved:git-recovery | byte-match against the destination |
| 549-551 | Explicit paths also fail *loudly* rather than silently: `git add` on an ignored | moved:git-recovery | byte-match against the destination |
| 553-553 | If an untracked file surprises you, do not stage it — find out what wrote it. | moved:git-recovery | byte-match against the destination |
| 555-555 | ## Install | moved:sprawl-internals | byte-match against the destination |
| 557-557 | > **Warning:** Do not run `make install` unless your agent identity is `weave` or the user | moved:sprawl-internals | byte-match against the destination |
| 559-559 | ## Running `claude` from agent bash subshells (QUM-518) | moved:e2e-testing-sandboxing | byte-match against the destination |
| 561-564 | When an agent invokes `claude -p ...` from a Bash tool subshell, Claude Code | moved:e2e-testing-sandboxing | byte-match against the destination |
| 566-566 | **Setup (one-time, host side):** | moved:e2e-testing-sandboxing | byte-match against the destination |
| 568-568 | 1. Create `.env` at the repo root containing your auth token(s): | moved:e2e-testing-sandboxing | byte-match against the destination |
| 570-573 | ``` | moved:e2e-testing-sandboxing | byte-match against the destination |
| 575-575 | Then `chmod 0600 .env`. **`.env` is gitignored — never commit it.** | moved:e2e-testing-sandboxing | byte-match against the destination |
| 577-577 | 2. Launch sprawl with the shim as `$SPRAWL_CLAUDE`: | moved:e2e-testing-sandboxing | byte-match against the destination |
| 579-581 | ```bash | moved:e2e-testing-sandboxing | byte-match against the destination |
| 583-587 | `scripts/run-claude` sources `$SPRAWL_ROOT/.env` (falling back to the script's | moved:e2e-testing-sandboxing | byte-match against the destination |
| 589-591 | `internal/agent/claude.go` honors `$SPRAWL_CLAUDE`: if set, it is used | moved:e2e-testing-sandboxing | byte-match against the destination |
| 593-593 | ## tmux safety (QUM-325) | retained:reworded | 'tmux safety' heading; the prohibition became a Prohibitions bullet. |
| 595-597 | > **Never run bare `tmux kill-server`.** Sandbox scripts now use a dedicated tmux socket v | retained:reworded+corrected | 'Never run bare tmux kill-server' is RETAINED as a prohibition with a pointer to e2e-testing-sandboxing. The block's `_stmux kill-session -t $SPRAWL_NAMESPACE` recommendation is deliberately NOT carried forward: the namespace names the socket, not the session, so the command cannot work. Dropped by the sandbox slice for the same reason; carrying it into the always-loaded surface would propagate a broken command. |
| 599-599 | ## `/tmp` hygiene — hard rules | moved:e2e-testing-sandboxing | byte-match against the destination |
| 601-602 | Sandbox roots live under `/tmp`, but `/tmp` is **shared** with other agents and | moved:e2e-testing-sandboxing | byte-match against the destination |
| 604-617 | - **Never `rm -rf` a broad `/tmp` glob** (`/tmp/*`, `/tmp/sprawl-*`, `$TMPDIR/*`, | moved:e2e-testing-sandboxing | byte-match against the destination |
| 619-619 | ## Text selection in `sprawl enter` (QUM-653 / QUM-731) | moved:tui-testing | byte-match against the destination |
| 621-624 | The TUI captures the mouse so the scroll wheel scrolls the chat viewport | moved:tui-testing | byte-match against the destination |
| 626-632 | * **Shift+drag** — most terminals (xterm.js / coder web terminal, gnome- | moved:tui-testing | byte-match against the destination |
| 634-634 | Scroll inside the TUI: | moved:tui-testing | byte-match against the destination |
| 636-644 | * **Mouse wheel** — scrolls the observed chat viewport up/down (suppressed | moved:tui-testing | byte-match against the destination |
| 646-646 | ### Incident snapshot hotkey (QUM-728) | moved:tui-testing | byte-match against the destination |
| 648-654 | Press `Ctrl+\` to write a forensic bundle to | moved:tui-testing | byte-match against the destination |
| 656-656 | ### Runtime pprof toggle (QUM-678 / QUM-934) | moved:tui-testing | byte-match against the destination |
| 658-661 | `--pprof <addr>` (or `SPRAWL_PPROF_ADDR`) exposes `net/http/pprof` at launch. | moved:tui-testing | byte-match against the destination |
| 663-664 | Bind-failure policy differs by **provenance**, deliberately — don't merge the | moved:tui-testing | byte-match against the destination |
| 666-671 | * **Explicitly configured** (`--pprof` / `SPRAWL_PPROF_ADDR` / an explicit arg): | moved:tui-testing | byte-match against the destination |
| 673-680 | While the listener is up, its **bound address is written to | moved:tui-testing | byte-match against the destination |
| 682-682 | ## Project Configuration | moved:sprawl-internals | byte-match against the destination |
| 684-684 | Sprawl reads `.sprawl/config.yaml` for project-level settings: | moved:sprawl-internals | byte-match against the destination |
| 686-688 | ```yaml | moved:sprawl-internals | byte-match against the destination |
| 690-695 | Since QUM-1087 this is **not** post-merge validation: the engine rebases the | moved:sprawl-internals | byte-match against the destination |
| 697-697 | ## Repo Layout | moved:sprawl-internals | byte-match against the destination |
| 699-705 | - `cmd/` — CLI commands (cobra). Each command has its own file + test file. | moved:sprawl-internals | byte-match against the destination |
| 707-707 | ## Meta: Developing Sprawl Inside Sprawl | retained:reworded | 'Meta: Developing Sprawl Inside Sprawl' heading; folded into the opening paragraph. |
| 709-709 | This repo IS Sprawl. The `.sprawl/` directory at the repo root stores agent state and work | retained:reworded | 'This repo IS Sprawl' orientation; condensed into the opening paragraph, including the do-not-touch-.sprawl rule. |
| 711-711 | ## Code Patterns | moved:sprawl-internals | byte-match against the destination |
| 713-713 | **Dependency injection**: Commands use a `deps` struct to inject interfaces for external d | moved:sprawl-internals | byte-match against the destination |
| 715-715 | **Tests required**: Every file in `cmd/` and `internal/` has a corresponding `_test.go`. K | retained:corrected | 'Tests required'. Retained as a bullet under Tests and assertions, but NOT verbatim: the original is a FALSE CENSUS — 34 of 215 non-test .go files under cmd/ + internal/ (generated files excluded, measured 2026-08-07) have no sibling _test.go. Restated as a requirement on NEW files, matching the correction the testing-practices slice made for the same reason. A requirement about future behaviour cannot rot the way a count of current files does. This was very nearly relocated verbatim into the always-loaded surface, which would have promoted a narrow falsehood to the most-read sentence in the repo. |
| 717-717 | **Every new assertion must demonstrate it CAN fail** — a negative control, a mutation, or  | retained:reworded | 'Every new assertion must demonstrate it CAN fail'. Kept as a one-liner naming all three demonstrations; the long form is in testing-practices. |
| 719-719 | **A watched failure proves the instrument works, not that it measures the right thing.** R | moved:testing-practices | byte-match against the destination |
| 721-722 | * An assertion written against the derived squash message's trailer block was watched fail | moved:testing-practices | byte-match against the destination |
| 724-724 | So after watching red, state separately **what the assertion would let through**, and pref | moved:testing-practices | byte-match against the destination |
| 726-726 | **No fallback branch may silently succeed (QUM-997).** Any validation or test script must  | retained:reworded | 'No fallback branch may silently succeed'. Kept as a one-liner including the 77-not-0 skip rule and the assertion-count floor. |
| 728-728 | **Read `/go-cli-best-practices` before writing or modifying Go code** — it covers cobra pa | moved:sprawl-internals | byte-match against the destination |
| 730-730 | **Read `/cli-ux-best-practices` before adding or modifying any CLI command's behavior** —  | moved:sprawl-internals | byte-match against the destination |
| 733-733 | ## Public vs Private Repo Hygiene | retained:reworded | 'Public vs Private Repo Hygiene' heading, kept as '## Public vs private repo hygiene'. |
| 735-735 | Before any commit, merge, or PR, determine whether the current repo is public or private: | retained:reworded | The determine-public-or-private instruction; retained condensed. |
| 737-738 | - `git remote get-url origin` → if hosted on a public namespace (github.com/<user-or-org>/ | retained:reworded | The visibility-probe commands and the default-to-PUBLIC rule; retained condensed, both commands kept. |
| 740-744 | For PUBLIC repos: | retained:reworded | The PUBLIC-repo prohibition list; retained near-verbatim as one paragraph, including the findings/ destination and the forensic-artifact caveat. |
| 746-747 | For PRIVATE repos: | retained:reworded | The PRIVATE-repo rule; retained as one clause. |
| 749-750 | This applies to all agents (engineers, researchers, QA, managers). Reviewers must flag sus | retained:reworded | 'applies to all agents' + the reviewer duty; retained. |
| 752-752 | This project tracks work in Linear. See `CLAUDE.local.md` for workspace-specific configura | retained:reworded | Linear is the tracker + CLAUDE.local.md holds the config; retained. |
| 754-754 | When creating, managing, or querying issues, **invoke the `/linear-issues` skill via the S | retained:reworded | 'invoke /linear-issues before creating an issue'; retained. |
| 756-759 | **Issue lifecycle** — if you are working on a Linear issue: | retained:reworded | The three-step issue lifecycle (In Progress + comment / log as you go / Done with summary); retained compressed into one sentence. |
| 761-761 | ## Spawning Agents | retained:reworded | 'Spawning Agents' heading; folded into the Linear section. |
| 763-763 | When spawning an agent to work on a Linear issue, keep the prompt short. Point the agent a | retained:reworded | 'keep the prompt short, point the agent at the issue'; retained. |
| 765-765 | The issue is the source of truth. The agent can read it via Linear MCP tools (`get_issue`) | retained:reworded | 'the issue is the source of truth'; retained. |
| 767-767 | ## Session Handoff | retained:reworded | 'Session Handoff' heading; the handoff skill is a skills-index entry. |
| 769-769 | At the end of a session, use `/handoff` to persist context for the next session. It guides | retained:reworded | 'use /handoff at the end of a session'; retained as a skills-index entry marked weave-only. |
| 771-771 | ## Sandbox Testing | retained:reworded | 'Sandbox Testing' heading; became the e2e-testing-sandboxing skills-index entry. |
| 773-773 | Use the `/e2e-testing-sandboxing` skill for the full setup, inspection, and cleanup workfl | retained:reworded | 'use the /e2e-testing-sandboxing skill'; retained as a skills-index entry and in Build & validate. |
| 775-778 | ```bash | moved:e2e-testing-sandboxing+corrected | The sandbox quick-start block (`make build` + `eval "$(bash scripts/sprawl-test-env.sh)"`). Present at the destination and deliberately CORRECTED: the relative-path form here fails from inside a .sprawl/worktrees/ path — the script refuses by design — so the skill spells it `cd /tmp` first and invokes by absolute path, and says so explicitly. |
| 780-780 | ## Linting & Formatting | moved:sprawl-internals | byte-match against the destination |
| 782-782 | This project uses [golangci-lint v2](https://golangci-lint.run/) with `gofumpt` formatting | moved:sprawl-internals | byte-match against the destination |
| 784-786 | * **All code must pass** `make validate` before committing. The pre-commit hook enforces t | moved:sprawl-internals | byte-match against the destination |
| 788-788 | ## Validating Changes | retained:reworded | 'Validating Changes' heading; folded into '## Build & validate'. |
| 790-794 | 1. `make validate` — full pipeline: build, fmt-check, lint, test | retained:corrected | Validate-pipeline item list, lines 788..793 — OUTSIDE the 794..938 byte-identity range wave 1 evidenced, and in no skill. Item 4's mandate ('TUI validation is mandatory for all TUI-related changes') is RETAINED verbatim as a mandate; items 2 and 3 are retained as the smoke-test and sandbox pointers. Item 1 is deliberately NOT carried forward verbatim because it is FALSE: it describes validate as 'build, fmt-check, lint, test' when Makefile:4 runs proto-check and the gate suites too, and runs test-race, not test — which since QUM-972 is the whole point. Replaced by a pointer to the Makefile as authoritative. Item 5 is the union-rule lead-in and is inside the hashed range in e2e-matrix. |
| 796-796 | **Derive the row set from the table; never from a list someone handed you (QUM-1081).** Th | moved:e2e-matrix | byte-match against the destination |
| 798-798 | Corollaries of the union rule, each of which is enough on its own to produce a wrong row s | moved:e2e-matrix | byte-match against the destination |
| 800-804 | * **The obligation is a property of the commit, not of one file in it.** Derive over every | moved:e2e-matrix | byte-match against the destination |
| 806-806 | Deriving mechanically also makes a *gap* in the table a checkable claim about the table ra | moved:e2e-matrix | byte-match against the destination |
| 808-808 | **Writing an issue or a brief? State the rule, not the row list.** An implementer cannot t | moved:e2e-matrix | byte-match against the destination |
| 810-810 | **Multi-row invocation is supported (QUM-947).** Several rows in the table below instruct  | moved:e2e-matrix | byte-match against the destination |
| 812-814 | ```bash | moved:e2e-matrix | byte-match against the destination |
| 816-816 | The summary reports `passed/requested`, where the denominator is **the number of rows you  | moved:e2e-matrix | byte-match against the destination |
| 818-818 | > **Reading older transcripts:** before QUM-947 the driver silently discarded every argume | moved:e2e-matrix | byte-match against the destination |
| 820-820 | The driver's own arg parsing, fail-fast validation, and summary arithmetic are unit-tested | moved:e2e-matrix | byte-match against the destination |
| 822-822 | All rows require a real, **authenticated** `claude` binary on PATH. `SPRAWL_E2E_SKIP_NO_CL | moved:e2e-matrix | byte-match against the destination |
| 824-824 | **The gate keys on presence only — it never probes auth.** All 11 `needs_claude` gates rea | moved:e2e-matrix | byte-match against the destination |
| 826-830 | \| claude state \| gate fires? \| `SPRAWL_E2E_SKIP_NO_CLAUDE` \| outcome \| | moved:e2e-matrix | byte-match against the destination |
| 832-832 | The middle state is a **misdiagnosis hazard, not a false green**: the row fails with a Ses | moved:e2e-matrix+repointed | The 'Not logged in' misdiagnosis paragraph. Breakage R1: it said 'see the run-claude shim and .env **above**', where 'above' meant CLAUDE.md's QUM-518 auth section, which went to a DIFFERENT skill — so the sentence telling a misdiagnosing agent how to fix auth pointed at nothing. Repointed by path at e2e-testing-sandboxing. Recorded in that skill's provenance header. |
| 834-834 | **Skip accounting (QUM-952).** A skipped row is reported as `SKIP <row>`, never `PASS`, an | moved:e2e-matrix | byte-match against the destination |
| 836-839 | ``` | moved:e2e-matrix | byte-match against the destination |
| 841-841 | The first line is the QUM-947 contract and is unchanged — `passed` means *actually execute | moved:e2e-matrix | byte-match against the destination |
| 843-843 | Driver exit codes: `0` every requested row executed and passed · `1` ≥1 row failed (domina | moved:e2e-matrix | byte-match against the destination |
| 845-845 | **A skipped row does not discharge a mandatory-gate obligation.** If the touched-file tabl | moved:e2e-matrix | byte-match against the destination |
| 847-847 | Two known remaining gaps (tracked as QUM-970 and QUM-969), so a green run is not over-read | moved:e2e-matrix | byte-match against the destination |
| 849-849 | > **Reading older transcripts:** before QUM-952 a skipped row was reported as `PASS` and e | moved:e2e-matrix | byte-match against the destination |
| 851-865 | **Relaunch waits for `weave.lock`, it does not sleep (QUM-948).** Every | moved:e2e-matrix | byte-match against the destination |
| 867-872 | **This table is prose, so a refactor that moves code between files silently | moved:e2e-matrix | byte-match against the destination |
| 874-878 | **And when you audit this table, audit the category, not the predicted | moved:e2e-matrix | byte-match against the destination |
| 880-887 | When the glob check above turns your file up, note **a glob hit means | moved:e2e-matrix | byte-match against the destination |
| 889-905 | Two cautions when counting rows this way. **A document that cites a count over | moved:e2e-matrix+repointed | The self-falsifying-count paragraph. Breakage R2: 'these paragraphs live inside the corpus they describe' named the wrong corpus after the move, and the recommended `grep -E '^   \| ' CLAUDE.md` returns zero rows post-cut while looking like an answer. Both halves repointed at the skill file. Recorded in that skill's provenance header. |
| 907-938 | \| files touched \| matrix row \| guards \| | moved:e2e-matrix | byte-match against the destination |
