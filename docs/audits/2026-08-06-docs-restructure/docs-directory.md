# `docs/` restructure — audit and decision document

**Author:** cipher (researcher) · **Date:** 2026-08-06 · **Base:** `dmotles/docs-audit-docs-dir` off `main`
**Surface:** all 144 files under `docs/` (39,778 lines). Read-only audit; no file outside this one was touched.
**Method:** 6 parallel classifiers, one per subtree, each required to spot-verify central factual claims by grepping the tree. All headline numbers below were independently re-derived by me from the tree, not taken from the classifiers.

---

## The decision, in one table

| | files | lines | what happens |
|---|---:|---:|---|
| **KEEP** (live truth, some with edits) | **24** | **7,712** | Becomes the new `docs/`. |
| **ARCHIVE** (true once, dated + quarantined) | **55** | **19,588** | Moves to `docs/archive/`, one-way door, nothing links in. |
| **DELETE** | **65** | **12,478** | Gone. Recoverable from git. |
| total | 144 | 39,778 | |

**45% of files and 31% of lines die outright.** Only **17% of `docs/` survives as truth.** The surviving corpus is 7,712 lines — small enough that an agent can be pointed at all of it.

The single most consequential number: **after the cut, the dangling-reference rate falls from 22% to 4%** — which is what makes the recurrence-prevention check in §5 enforceable at all. The cut is not just hygiene; it is the precondition for the gate.

---

## 1. The headline measurement: 22% of `docs/` code references point at files that do not exist

**Method, stated precisely so it can be reproduced and trusted.**

- **What counted as a code-path ref:** a regex match for `(internal|cmd|scripts|web/src)/…\.(go|sh|ts|tsx)` in any tracked file under `docs/`. Prose mentions of a *package* (`internal/tui`) do not count; only paths naming a concrete file with an extension do. This deliberately excludes symbol names, which are far more numerous and much harder to adjudicate.
- **Denominator:** **distinct** refs per file (a doc citing `session.go` nine times counts once), summed across files. **688 distinct refs across 91 docs.** The other 53 files cite no code paths at all.
- **Resolution:** a ref "dangles" if `os.path.exists()` is false at this commit. **152 of 688 dangle → 22%.**
- **Renames vs deletions — the part that makes the number trustworthy.** I re-checked every dangling ref against `git log --all --name-only`. **131 of 152 refs name a path that existed at some point and no longer does** (a real deletion or rename — either way the doc is stale). The remaining **21 never existed in repo history** — illustrative or proposed paths like `internal/foo.go`. So the number is not inflated by typos: **86% of dangling refs are genuine rot.**

**Worked instance.** `internal/runtime/turnloop.go` was deleted by QUM-817. It is still named in **13 docs**. `ForceInterruptForDelivery` (deleted by QUM-821) survives in 4; `internal/tui/bridge.go` in 5. Across a list of 19 symbols and files that CLAUDE.md itself documents as deleted, **19 docs carry ~100 live-sounding mentions**.

Two caveats I want stated rather than buried, because they cut against my own conclusion:

- **Refs resolving is necessary, not sufficient.** 35 docs have a 0% dangling rate and are still not live truth — a file existing says nothing about whether the doc's claims about it hold. `docs/designs/chatlist-invariants.md` has a perfect ref score and is dangerously incomplete (it has *zero* mentions of the pending zone that now lives inside `ChatList`, including inside the `Reset` method it specifies as an invariant). The 22% is a floor on rot, not a measure of it.
- **The git-date staleness signal is corrupted and should not be used.** 17 files show `2026-07-14` as "last touched"; that commit is `56d6b91`, a one-word employer-name `sed` — 51 insertions across 17 files. Another 18 show `2026-06-10` from a rebase squash. **Roughly a quarter of `docs/` has a last-modified date that reflects no content change.** Any future tooling that sorts by git date will mis-rank these.

---

## 2. The most dangerous stale doc

### `docs/designs/messaging-overhaul.md` — 731 lines, header says **"Status: Implemented"**, and it is cited from **7 production source files**

It wins on a specific combination no other doc matches:

1. **It asserts, rather than merely aging.** "Implemented (post-MCP migration)" is a positive claim that the described surface is in the binary today.
2. **The surface it specifies was deleted.** Its §4.2 gives complete tool schemas for `send_async(to, subject, body, reply_to, tags)`, `send_interrupt`, and `message`. All three were collapsed into `send_message` by QUM-550. They now appear in the codebase **only as banned strings in a regression test** (`internal/sprawlmcp/tool_description_sync_test.go:70`). The real call is `send_message(to, body, interrupt, wake_if_offline)`.
3. **It is copy-pasteable and the wrong answer is plausible.** The two surfaces are similar enough that a wrong edit lifted from §4.2 survives a skim of the diff.
4. **It looks maintained** — 7 commits, last touched 2026-07-14, more recently than `unified-runtime.md`, which is the doc that actually records the deletion. Two docs one directory apart contradict each other, and the wrong one is longer, newer-looking, and topic-titled.
5. **Seven production files point at it**: `internal/state/state.go`, `internal/supervisor/real.go`, `internal/supervisor/supervisor.go`, `internal/agentops/report.go`, `internal/agentloop/activity.go`, `internal/agentloop/queue.go`, `internal/tui/theme.go`. An agent reading `real.go` is *routed here by the source*.

This is the QUM-1111 mechanism precisely: an authoritative-looking artifact, reachable from the code, describing a surface that was replaced.

**Runners-up**, each the best-in-class of a distinct failure mode:

- **`docs/design/hub/02-components.md`** — *wrong while looking reconciled*. It was touched on 2026-07-23 in the "9 hub docs rewritten wire-log-authoritative" commit, but only 1 of its 15 sections was actually rewritten. §§1.4–1.6, 2.2, 2.4 still specify OIDC relying-party auth, a lease/fence registry, and fence tokens on every uplink frame — all explicitly listed as **cut** by `01` §8 and `07` §0 in the same directory. I confirmed `proto/hub/v1/hub.proto` contains **zero** occurrences of fence or lease. It is the most-linked doc in its subtree (8 siblings cite it, mostly citing the dead sections).
- **`docs/reference/claude-code-host-protocol.md`** — *authoritative and silently incomplete*. 1,151 lines, the largest file in `docs/`, added 2026-04-13 and **never updated since**. What it says is true. What it omits is the problem: it never mentions `isReplay` (which appears in **40 Go files**), `task_notification` (14), `cancel_async_message` (9), `compact_boundary` (7), or `stream_event` (3). Its size and "standalone implementation guide" framing invite exactly the over-trust its scope cannot support. **Keep with a scope banner** pointing at `internal/protocol/types.go` as the authority for sprawl's own frames.
- **`docs/research/qum-1061-child-drain-inflight-asymmetry.md`** — *false within one day of landing*. Committed 2026-08-06; its titular verdict ("only the weave drain filters `InFlightSystemEntryIDs`") was already closed by QUM-1066, which made the filter unconditional in `readInboxSnapshot` (`internal/supervisor/drain.go:352`). Recency is doing persuasive work the content no longer earns. **This one file is the whole argument for §5**: age is not the risk factor.

---

## 3. Is `docs/research/` core product truth?

**No. It is an archive, and it has always been one — it says so itself, in a README nobody reads.**

`docs/research/README.md` (written 2026-04-07, never updated) opens: *"These are historical research documents from Sprawl's development… preserved here for reference and context."* The intent was right on day one. Then:

- it indexes **6 of 100 files** — 94% incomplete;
- **2 of those 6 don't exist** (`fresh-eyes-audit.md`, `stream-json-prototype/`);
- **nothing anywhere links to it.** Across the whole repo, only **3 of 144 docs** are reachable from `CLAUDE.md`, `CLAUDE.local.md`, or `.claude/skills/`.

So the archive was never labelled *at the point of use*. An agent does not read `docs/research/README.md`; it greps `docs/` and lands mid-file in a confident, present-tense write-up.

**The verification result — the number the brief asked for.** Of 100 `docs/research/` files, the classifiers nominated LIVE TRUTH candidates and I required each to survive contact with the tree. **6 survived: 6%.** And of those 6, three are executable shell scripts that are simply misfiled (they belong in `scripts/`). **Only 3 of 100 research documents are prose that describes how the system works today** — and one of those (`teardown-unbounded-wait-audit.md`) is misfiled as research when it is really an invariant record.

The failure mode is concentrated and nameable: **8 docs are written in present-tense "Current State" / "Current Architecture" / "Ground Truth" voice with no supersession banner.** That framing is what converts a harmless old file into a trap. `tmux-elimination-research.md` opens with *"Tmux is not dead in sprawl. It remains the process container for ALL child agents"* — `internal/tmux/` does not exist.

**One thing the archive cost us, concretely.** `open-source-readiness/03-security-audit.md` filed a **CRITICAL: agent-name path traversal, no validation anywhere**. I verified: `rg 'func [Vv]alidateAgentName'` returns **zero hits**. That finding has been sitting unfixed in an unread archive for four months. Archiving without triage is how a real bug becomes invisible. **This should be a Linear issue before anything is moved.**

---

## 4. `docs/design/` vs `docs/designs/` — an accident, not a taxonomy

There is **no purpose distinction**. Verified from history:

- `docs/designs/` (plural) is the original, established **2026-03-30**, grown continuously through today.
- `docs/design/` (singular) was created **2026-07-01** by the hub design push and **has only ever contained `hub/`** — I confirmed this by diffing every add-commit under `docs/design/`: outside `hub/`, the set is empty.
- **Zero cross-links in either direction.** No `docs/README.md` exists to disambiguate. `docs/design/hub/README.md` presents itself as an index with no awareness that a sibling `designs/` exists.

Someone typed the singular, and nothing pushed back. The practical harm: `rg docs/design` matches both, and newcomers guess.

**Resolution: fold `docs/design/hub/` into `docs/designs/hub/` and delete the singular directory.** The hub subtree is the better-organised of the two (numbered, with an index); it is the plural directory that lacks one. Note the hub docs also carry the single funniest evidence that nobody reads this corpus: `04-authentication.md` ends with a literal committed `</content></invoke>` tool-call artifact, sitting there since 2026-07-22.

**Hub build state, since the docs won't tell you:** Phase 0 complete, Phase 1 shipped read-only. Auth, registry, uplink and read-only fan-out are real (7 RPCs in the proto). Everything bidirectional or memory-related is aspirational — no downlink, no memory sync, `AppendStream` is memStore-only. Design-for-unbuilt-code is a legitimate category and should be **labelled**, not deleted.

---

## 5. Proposed target structure

Five directories. Each has a stated purpose and an entry criterion.

```
docs/
  README.md          -- the index. CLAUDE.md points here. See below.
  architecture/      -- how a subsystem works TODAY. Entry: describes a live system,
                        not an incident or a decision. Must name a code owner path.
  designs/           -- accepted designs, incl. hub/. Entry: a design that is built
                        or committed-to-build. Status banner REQUIRED (built/partial/design-only).
  guides/            -- procedural how-to for tasks done from the repo. Entry: a human
                        or agent runs these steps. If it is a *sometimes* task -> make it a SKILL instead.
  archive/           -- one-way door. Dated, quarantined, never linked from live docs.
                        Entry: was true once. Exit: none. Nothing here is authority.
```

**Where the 24 survivors land:**

| destination | contents |
|---|---|
| `architecture/` | `memory.md` (the anchor — see below), `teardown-invariants.md` (promoted out of `research/`) |
| `designs/` | `merge-engine.md`, `chatlist-invariants.md` *(must be updated for the pending zone first)*, `qum-669-viewport-wedge-recovery.md`, and `designs/hub/` (12 files + a rewritten `hub-status.md`) |
| `guides/` | `memory-commands.md` (renamed from `dev/commands.md` — it covers 4 of ~25 commands and the current name implies completeness it does not have), `smoke-test-memory.md` |
| `reference/` (kept inside `architecture/`) | `claude-code-host-protocol.md` **+ scope banner** |
| unchanged, but **moved out of `docs/`** | the 3 `qum-991/repro-*.sh` scripts → `scripts/`; the 2 hub `evidence/*.txt` → out of the design corpus |

**Directories that disappear entirely:** `research/`, `design/` (singular), `design-notes/`, `dev/`, `forensics/` (→ `archive/`), `handoffs/`, `testing/`, `todo/`. Six of these are single-file directories; none earns a top-level slot.

### `docs/architecture/memory.md` is the model — and the anchor

It is the strongest document in the entire surface and the only one describing a **system** rather than an incident. I verified every load-bearing symbol it names — `TimelineRowRE`, `BuildContextBlob`, `WithArcSummarizer`, `UpdatePersistentKnowledge`, `runConsolidationPipeline`, `NewCLIInvoker`, `AppendSessionWithOptions`, all four `sprawl memory` subcommands — **all present**. It explains *why* (the 3-tier model replaced LLM distillation because that pipeline destabilised as session count grew) and it correctly cross-links its own predecessor as forensics rather than absorbing it. That cross-link pattern is exactly the live/archive boundary this restructure should generalise.

### Where CLAUDE.md's evicted narrative lands

CLAUDE.md is 768 lines; three sections are 65% of it (**Commit guard 249, Validating Changes 151, Build & Test 100**). All three are narrative-heavy and belong in `docs/`, with CLAUDE.md keeping only the rule and a pointer:

| CLAUDE.md content | lands at | CLAUDE.md keeps |
|---|---|---|
| Commit guard: two-hook design, squash-merge recovery (QUM-1083), wrong-tree recovery | `docs/guides/git-hygiene.md` | "Never `git add -A`; stage explicit paths. See `docs/guides/git-hygiene.md`." |
| Lifecycle model (QUM-786) | `docs/architecture/agent-lifecycle.md` | the `IsTerminal` = `{retired, retiring}` rule + pointer |
| Race-detection guarantees (QUM-972) | `docs/architecture/testing-guarantees.md` | "`validate` runs the suite under `-race`. Scope + cost: see doc." |
| e2e matrix rationale + the touched-file table | `docs/guides/e2e-matrix.md` | **the derivation *rule*, never a row list** |

That last row matters and is the one I'd defend hardest. The table is prose that rots when code moves (QUM-1084 relocated 443 lines out from under its gates). Moving it to `docs/` does not fix that — but it does stop the rot being paid for on every single turn, and it puts the table somewhere a referential-integrity check can reach it.

### Does `docs/` need a README? Yes — it is the missing piece

There is **no `docs/README.md` today**, and `README.md` never mentions `docs/`. That is *why* the archive was never labelled at the point of use. It should be short and contain exactly four things:

1. **A one-line purpose + entry criterion per directory** (the table above).
2. **The authority order**, stated explicitly: code > `CLAUDE.md` > `docs/architecture` > `docs/designs` > `archive` (**never authority**).
3. **The archive warning**, phrased for an agent that arrived by grep: *"If you are reading a file under `docs/archive/`, it was true once and is not now. Verify against the tree before acting."*
4. **What is deliberately NOT here** — Linear is the tracker (no `todo/`); `.sprawl/incidents/` holds machine-generated snapshots, `docs/archive/` holds human analysis; agent findings go in `.sprawl/agents/<name>/findings/`, which is gitignored for this reason.

---

## 6. The entry rule that stops recurrence

forge asked me to converge with `query` on **one** referential-integrity check across all tracked markdown rather than two competing ones. I agree, and I'd go further: **the check is the whole mechanism, and front-matter is a distant second.**

### Why not front-matter alone

A `status:`/`date:` header is honest but **passive** — it tells a reader a doc *might* be stale without telling them it *is*, and it decays exactly like the prose it heads. This surface already proves it: `docs/design-notes/tab-cycling-audit.md` carries an exemplary supersession banner, and **the banner itself is now wrong** (it claims QUM-695 removed `activePanel`; `activePanel` is still in the tree). `docs/research/README.md` declared the whole directory historical in April and then rotted. Self-description does not survive contact with time. Front-matter is worth having — but as *metadata for the check*, not as the control.

### The check: dangling code-path references in tracked markdown

**Rule.** For every tracked `*.md`, extract references matching `(internal|cmd|scripts|web/src)/….(go|sh|ts|tsx)`. Fail if the path does not exist. Exempt anything under `docs/archive/` (that is what the one-way door *buys* you) and anything inside a fenced code block or marked with a `<!-- illustrative -->` comment.

**Cost and false-positive rate, measured on the current tree** — this is the part that decides whether it's adoptable:

| | refs | dangling | rate |
|---|---:|---:|---:|
| `docs/` today (all 144 files) | 688 | 152 | **22%** — unenforceable, 56 files would fail |
| the **DELETE** set (65 files) | 226 | 77 | 34% |
| the **KEEP** set (24 files) | 52 | **2** | **4%** |

**This is the key finding of §5. The gate is impossible today and trivial the day after the cut lands.** On the surviving corpus it produces **2 failures, both in `qum-991/decision.md`, and both are illustrative paths** (`internal/foo.go` and a proposed-but-unbuilt `scripts/test-guard-foreign-content.sh`) — i.e. genuine false positives, each fixable with one inline-code annotation. **Zero false negatives on the KEEP set**, because all 131 genuinely-deleted refs live in files being cut or archived.

So the sequencing is the recommendation: **cut first, then gate.** Landing the gate before the cut means 56 failing files and an immediate `|| true`. Landing it after means a green gate that stays green.

**Cost:** one shell script, no build, no `claude`, runs in well under a second on 113 files. It slots into `make validate` beside `leak-scan` and `gitignore-classes`. Per this repo's own rules it needs an **assertion-count floor** so a run that finds zero files to check exits non-zero rather than green — the exact `0 passed / 0 failed / exit 0` failure class documented under QUM-997.

**Convergence with `query`'s proposal.** `query` independently proposed banning `file.go:NNN` line cites in CLAUDE.md. That is the same class and should be the **same script, one severity tier stricter**: a bare path must *exist*; a `path:NNN` line cite is banned outright, because it is unverifiable by construction and rots on any edit above the cited line. `docs/research/qum-334-bridge-bleed.md` has a 30-row line-number table against a file that no longer exists; the hub docs and several research docs are full of them. One script, two rules, applied to every tracked `.md` including `CLAUDE.md`.

### The second rule, from `query`'s enumeration heuristic

`query`'s finding — **rot enters via enumeration, not age**; the detector is *a count followed by a list of code entities* ("three files", "both", "all 11", "the only") — applies directly here, and I tested it against my own conclusions as a falsification check rather than a confirmation:

- **It independently validates my two strongest LIVE picks.** `docs/architecture/memory.md` and `docs/designs/merge-engine.md` both score **zero** enumerations. My best-verified docs are also the ones structurally incapable of this rot.
- **It correctly flags my highest-risk keeps.** The top of the KEEP set by enumeration count is the `qum-991` bundle (7–8 hits) — whose enumerations are counts *over the current guard set*, which will be wrong the moment the proposed guard is built. `hub/13-implementation-plan.md` and `hub/04-authentication.md` (3 each) are next, both already flagged for rewrite.
- **It agrees with the classifiers on the worst offender**: `architecture-simplification-audit-2026-05-20.md` scores 10 and is being archived.

So the entry rule is: **a doc that enumerates call sites, lists implementers of an interface, or mirrors a directory tree is archive the day it lands** — it cannot be maintained as live truth, and it should not be filed as such. That is a *placement* rule, cheap to apply at review time, and it needs no tooling.

### Delete vs quarantine — arguing the boundary rather than defaulting to cut

forge is right that `docs/` differs from `CLAUDE.md` in a way that should change the default. `CLAUDE.md` is paid for on **every turn**, so its cost is tokens and cutting is nearly always correct. `docs/` is paid for only when an agent greps it, so the cost is **being misled** — and a *visibly dated, quarantined* doc imposes near-zero cost while retaining the "how we got here" value the brief explicitly wants.

So I did **not** default to cut. The boundary I applied:

**ARCHIVE (55 files) when the doc retains explanatory value that the code cannot supply.** The clearest case is `notification-injection-race-2026-05-14.md`: its code map is entirely dead, but its *diagnosis* is the causal record of why QUM-817 rewrote turn ownership — and that "why" is not recoverable from `SetFrameRouter`'s comments. Same for `unified-runtime.md`, `tui-structural-rewrite-plan.md`, and the three `forensics/` files. This is the majority verdict, and deliberately so: **archive is 55 files to delete's 65.**

**DELETE (65 files) only when one of four things is true**, because in these cases quarantine buys nothing and still costs a grep hit:

1. **Zero surviving referent** — the subject no longer exists in any form, so there is no "how we got here" to preserve, only a map of a deleted machine (`realtime-message-injection.md`, `qum-570-startturn-caller-map.md`, `manager-wake-loss`).
2. **Recoverable from git** — completed deletion audits are changelogs (`cli-deletion-deadcode-audit`).
3. **Duplicated by a live authority** — CLAUDE.md, a skill, or the shipped code already says it, better (`qum-617-text-selection`, `claude-stream-json-protocol`).
4. **Not a document** — raw captures and transcripts. `m13-phase1-evidence/` is 26 files with only **11 distinct contents**: 15 are literal byte-duplicates. This class is the most clear-cut in the audit, and it has already grown *machinery* to protect itself — `.gitignore` carries a permanent negation (`!docs/research/m13-phase1-evidence/ec6-live-handoff-stderr.log`) whose only purpose is to keep a 10-line April stderr log tracked. That is the ratchet made visible.

**Two things must happen before anything moves**, or archiving destroys value:

- **Triage the archive for live findings.** The unfixed CRITICAL path-traversal finding must become a Linear issue first. Archiving it as-is buries a real bug deeper.
- **Fix the inbound source links.** 20 docs are cited from `.go`/`.sh` files. Moving or deleting them silently breaks those comments — and this rot is already live in *both* directions: `scripts/test-gitignore-classes.sh` cites `docs/findings-summary.md` and `scripts/e2e-tests/hub-e2e.sh` cites `docs/design/hub/13-p1-local-e2e-and-manual-walkthrough.md`. **Neither file exists.** The referential-integrity check in §5 should therefore run in both directions: markdown → code, and code → markdown.

---

## Ranked recommendation

| # | action | buys |
|---|---|---|
| 1 | **Delete the 65-file DELETE set** (12,478 lines) | Removes every doc that would actively mislead a grep. Drops dangling refs 22% → 4%. |
| 2 | **Re-banner `messaging-overhaul.md` and fix the 7 source citations** | Kills the single most dangerous artifact — and it is reachable *from production code*. |
| 3 | **File the path-traversal CRITICAL in Linear** | A real unfixed bug, invisible for 4 months. Costs 10 minutes. |
| 4 | **Move 55 files to `docs/archive/`, one-way door** | Preserves "how we got here" at near-zero misleading-cost. |
| 5 | **Add `docs/README.md` + point CLAUDE.md at it** | The missing piece: labels the archive *at the point of use*, which is where every past attempt failed. |
| 6 | **Land the referential-integrity check** (one script, both directions, with an assertion floor) | Makes rot fail CI instead of failing an agent. Only viable *after* step 1. |
| 7 | **Fold `docs/design/` into `docs/designs/`; update `chatlist-invariants.md`** | Ends the accidental fork; closes the worst live-doc omission. |

Steps 1–3 are independently valuable and can land today. Step 6 depends on step 1.

---

## Appendix A — reflections

**Surprising.** That the archive intent was correct from day one and still failed — `docs/research/README.md` labelled the whole directory historical in April 2026. The lesson is that labelling at the *directory* level does nothing when agents arrive by grep. Quarantine has to be structural (a path segment an agent can see in the result) rather than declarative.

Also surprising: the strongest correlate of a doc being live truth was not recency. `qum-1061` was committed **yesterday and is already false**, while `architecture/memory.md` (last real edit weeks ago) is perfect. What distinguishes them is exactly `query`'s heuristic — one enumerates, the other describes a system.

**Open questions.** (a) I classified `docs/designs/hub/` largely on build-state; whether the unbuilt hub design is still the *intended* design is a product question I can't answer from the tree — if Phase 2/3 has been abandoned, another ~2,000 lines become archive. (b) I did not verify the 55 archive-bound docs as rigorously as the KEEP set; some may be DELETE. That asymmetry is deliberate — the cost of over-archiving is low — but it means the 55/65 split is softer than the 24.

**What I'd do next.** Prototype the referential-integrity script and run it against `CLAUDE.md` and `.claude/skills/` too, to give `query` and the skills auditor a shared measured baseline rather than three separate estimates. Second, measure the *symbol*-level dangling rate (I only measured file paths, which is the conservative floor); a sample suggested it is materially worse, and it would sharpen the "necessary but not sufficient" caveat in §1.

---

## Appendix B — all 144 files

Bucket key: **LIVE** = describes the system today · **LIVE-PARTIAL** = accurate but materially incomplete · **DESIGN-ONLY** = design for unbuilt code · **UPDATE**/**REWRITE** = keep, edits required · **ARCHIVE** = true once · **DELETE**.
Lens key: 1 declarative · 2 procedural · 3 contextual · 4 tacit · 5 meta-cognitive · 6 agent-behavior.

> **Do not use the "last touched" column as a staleness signal** — see §1. 17 files show `2026-07-14` from a one-word `sed` and 18 show `2026-06-10` from a rebase squash.

| path | last touched | lines | bucket | lens | verdict |
|---|---|---|---|---|---|
| `docs/architecture/memory.md` | 2026-07-14 | 156 | **LIVE** | 1,2,3 | Verified symbol-by-symbol; the only doc describing a system rather than an incident. Anchor of the new docs/. |
| `docs/design-notes/tab-cycling-audit.md` | 2026-07-14 | 189 | **ARCHIVE** | 1,3,5 | Exemplary banner - but the banner is itself stale (claims QUM-695 removed activePanel; it is still in the tree). |
| `docs/design/hub/00-overview.md` | 2026-07-23 | 155 | **LIVE** | 1,3,5 | Motivation/north-star; little falsifiable content. |
| `docs/design/hub/01-architecture.md` | 2026-07-23 | 321 | **LIVE** | 1,3,4 | Advisory active-host marker, no fence, seq-verbatim all verified. Downlink half unbuilt. |
| `docs/design/hub/02-components.md` | 2026-07-23 | 417 | **SUPERSEDED** | 1,4 | MOST DANGEROUS IN SUBTREE. Touched 07-23 so looks current; only 1 of 15 sections rewritten. Still specifies OIDC, lease/fence registry, snapshots. 8 sibling docs cite it. |
| `docs/design/hub/03-api-surfaces.md` | 2026-07-23 | 363 | **LIVE-PARTIAL** | 1,2,4,5 | L7-LB research is the best content in the set; every RPC it names is wrong (7 shipped, ~9 named don't exist). |
| `docs/design/hub/04-authentication.md` | 2026-07-22 | 383 | **LIVE** | 1,2,4 | Highest hit-rate doc. DEFECT: ends with committed `</content></invoke>` tool-call artifact. |
| `docs/design/hub/05-observability.md` | 2026-07-01 | 334 | **SUPERSEDED** | 1,2,4 | Never re-scoped. Endpoints shipped, but the /debug/state payload it documents is wrong (leases, fence, snapshot_seq). |
| `docs/design/hub/06-iac.md` | 2026-07-01 | 292 | **SUPERSEDED** | 1,2 | Never re-scoped. Layout built at deploy/hub/infra/terraform/, not the doc's path; provisions snapshots/OIDC/GC that v2 cut. |
| `docs/design/hub/07-storage-persistence.md` | 2026-07-23 | 373 | **LIVE** | 1,4 | Most accurate design doc; exact interface names match. Memory tables unbuilt. |
| `docs/design/hub/08-deployment.md` | 2026-07-01 | 382 | **SUPERSEDED** | 1,2,3 | Never re-scoped. The embedspa build tag it makes load-bearing does not exist. |
| `docs/design/hub/09-synchronization.md` | 2026-07-23 | 334 | **LIVE** | 1,3,4 | The one rule is implemented and e2e-proven. Section 5 "what was cut" is honest and correct. |
| `docs/design/hub/10-memory.md` | 2026-07-02 | 317 | **DESIGN-ONLY** | 1,3,4 | Local half verified; hub half entirely unbuilt. Label design-not-yet-built. |
| `docs/design/hub/11-frontend-stack.md` | 2026-07-23 | 261 | **LIVE** | 1,3 | React 19/Vite 6/connect-web/react-virtual confirmed; connect-query and Zustand not used. |
| `docs/design/hub/12-testability-local-dev.md` | 2026-07-23 | 498 | **SUPERSEDED** | 2,4,6 | Almost everything unbuilt: --port-file, docker-compose, 4 proposed matrix rows (1 shipped), test issuer. Tells tests to assert fence state. |
| `docs/design/hub/13-implementation-plan.md` | 2026-07-23 | 757 | **REWRITE** | 2,3,5 | Phase 0 + Phase 1 read-only shipped; doc carries NO status marks and still says run the spike first. Becomes hub-status.md. |
| `docs/design/hub/README.md` | 2026-07-02 | 66 | **LIVE** | 1,5 | Correct principles; status column marks 12 written docs as todo. |
| `docs/design/hub/attachments-multimodal.md` | 2026-07-01 | 300 | **LIVE** | 1,4 | Never re-scoped but v2-neutral. /attach shipped; browser-upload path unbuilt. |
| `docs/design/hub/evidence/qum-911/go-test.txt` | 2026-07-24 | 34 | **ARCHIVE** | - | Point-in-time go test transcript. Not product truth. |
| `docs/design/hub/evidence/qum-911/teeth-check.txt` | 2026-07-24 | 12 | **ARCHIVE** | 4 | The mutation run proving the assertion has teeth. Higher value; restate the claim in the walkthrough. |
| `docs/design/hub/qum-911-e2e-walkthrough.md` | 2026-07-24 | 178 | **LIVE** | 2,4,5 | The only hub doc both accurate and about shipped code. Model for the rest. |
| `docs/design/hub/security-privacy.md` | 2026-07-02 | 328 | **LIVE** | 1,4 | TL;DR correct v2; section 1.1/1.2 still list the cut lease/fence registry, contradicting itself. |
| `docs/designs/agent-teardown.md` | 2026-06-04 | 285 | **ARCHIVE** | 1,3 | Semantics live; every mechanism (tmux windows, respawn, --force) gone. |
| `docs/designs/agent-wrapper-loop.md` | 2026-05-14 | 451 | **DELETE** | 1,3 | Superseded banner links to a deleted file; entire env-var contract gone. |
| `docs/designs/chatlist-invariants.md` | 2026-07-14 | 495 | **UPDATE** | 1,2,4 | Invariants hold, but zero mentions of the QUM-833/925 pending zone now inside ChatList, including inside Reset. Misleads by omission. |
| `docs/designs/merge-engine.md` | 2026-08-06 | 222 | **LIVE** | 1,2,3,4 | Written 2026-08-06; every symbol resolves. Only defect: `merge_first` vs the real `merge` param. |
| `docs/designs/messaging-overhaul.md` | 2026-07-14 | 731 | **ARCHIVE** | 1,3 | MOST DANGEROUS DOC IN docs/. Status says Implemented; the tool surface it specifies was deleted by QUM-550. Cited from 7 production source files. |
| `docs/designs/parallel-agent-viewport-containers.md` | 2026-06-10 | 291 | **DELETE** | 1 | Still "Status: Proposal"; the bug, the field, and the renderer were all deleted. |
| `docs/designs/qum-669-viewport-wedge-recovery.md` | 2026-07-14 | 451 | **UPDATE** | 1,2,3,5 | Shipped and materially accurate; only header status stale. Does NOT describe the watchdog. |
| `docs/designs/tui-chassis-port-scoping.md` | 2026-06-03 | 70 | **ARCHIVE** | 2 | Spent checklist; source package internal/tuichat/ no longer exists. |
| `docs/designs/tui-redesign-research.md` | 2026-06-29 | 953 | **ARCHIVE** | 3,5,6 | Locked-decisions record; decisions shipped, opening file inventory now wrong. |
| `docs/designs/tui-structural-rewrite-plan.md` | 2026-06-04 | 911 | **ARCHIVE** | 2,3,5 | S0-S7 arc complete. Valuable as the why, useless as a map. |
| `docs/designs/unified-runtime.md` | 2026-06-29 | 698 | **ARCHIVE** | 1,3,5 | Best-maintained old doc: 4 dated amendments incl. a QUM-821/829 retraction. But retraction sits 690 lines below the claim it retracts. |
| `docs/designs/unify-tui-weave-init.md` | 2026-05-14 | 260 | **ARCHIVE** | 1,3 | Correctly self-labels superseded; both predictions shipped. |
| `docs/designs/viewport-yank.md` | 2026-07-14 | 103 | **DELETE** | 1 | Feature deleted wholesale in QUM-695; every symbol gone. |
| `docs/dev/commands.md` | 2026-05-07 | 178 | **LIVE** | 1,2 | Accurate but misnamed: covers 4 of ~25 commands. Rename to memory-commands.md. |
| `docs/forensics/lost-commits-2026-04-21.md` | 2026-04-30 | 113 | **ARCHIVE** | 3,4 | One-time post-mortem, fully resolved. Legitimately archival, no code claims to decay. |
| `docs/forensics/memory-consolidation-perf.md` | 2026-07-14 | 206 | **ARCHIVE** | 1,3,4 | Properly cross-referenced from architecture/memory.md as the why-the-old-pipeline-failed record. |
| `docs/forensics/tui-weave-wedge-2026-05-05.md` | 2026-05-06 | 163 | **ARCHIVE** | 3,4 | Correct banner; live-session forensics, no longer reproducible. |
| `docs/handoffs/b4-manager-handoff.md` | 2026-06-04 | 93 | **DELETE** | 2,3,6 | Expired agent-to-agent work order for a June arc. Competes with the live /handoff mechanism. |
| `docs/reference/claude-code-host-protocol.md` | 2026-04-13 | 1151 | **LIVE-PARTIAL** | 1,2,3 | Authoritative on the control/MCP layer; silent on isReplay, priority, task_notification, compact_boundary, stream_event, cancel_async_message. Needs a scope banner. |
| `docs/research/README.md` | 2026-04-07 | 15 | **DELETE** | 1,3 | Indexes 6 of 100 files; 2 of the 6 do not exist. Declares the dir historical then rots. |
| `docs/research/agent-resume-after-restart.md` | 2026-05-06 | 237 | **ARCHIVE** | 1,3,4 | Self-labeled historical; gap closed by RecoverAgents, evidence base deleted. |
| `docs/research/architecture-simplification-audit-2026-05-20.md` | 2026-05-20 | 877 | **ARCHIVE** | 1,3,4,5 | 4 of 5 recommendations shipped; only the fat RuntimeHandle is still open. |
| `docs/research/ask-user-question-mcp-design.md` | 2026-05-11 | 824 | **ARCHIVE** | 1,2,3 | Built essentially as specced; shipped code plus QUM-611 rework is now the source of truth. |
| `docs/research/beads-worktree-integration.md` | 2026-06-10 | 200 | **ARCHIVE** | 1,2,3 | External tool this repo no longer uses; integration code survives but dormant. |
| `docs/research/branch-hygiene-root-cause.md` | 2026-06-10 | 234 | **ARCHIVE** | 1,3,4,6 | PARTIAL-TRUTH TRAP: cmd/enter.go still literally reads CallerName "weave", so a grep will falsely confirm a fixed bug. |
| `docs/research/child-viewport-missing-tool-results.md` | 2026-04-30 | 152 | **ARCHIVE** | 1,3 | Fix landed in-place as QUM-388 in the exact code block the doc quotes. |
| `docs/research/claude-code-capabilities.md` | 2026-06-10 | 369 | **ARCHIVE** | 1,3 | External-CLI reference that drifted; recommends --bare, contradicting a sibling doc and the code. |
| `docs/research/claude-stream-json-protocol.md` | 2026-06-10 | 871 | **DELETE** | 1,2,3 | Duplicated by docs/reference/claude-code-host-protocol.md; recommends --bare, which breaks OAuth here. |
| `docs/research/cli-deletion-deadcode-audit-2026-05-18.md` | 2026-06-10 | 405 | **ARCHIVE** | 1,2,3 | Every deletion happened. Now a changelog recoverable from git log. |
| `docs/research/config-load-bug-merge-retire.md` | 2026-04-07 | 134 | **DELETE** | 1 | Entire root cause lives in cmd/retire.go, deleted in QUM-566. |
| `docs/research/context-token-counter.md` | 2026-04-30 | 279 | **ARCHIVE** | 1,2,3 | Reads as a live proposal for something that shipped as a whole package plus a /usage modal. High re-implementation risk. |
| `docs/research/go-agent-loop-integration.md` | 2026-04-07 | 609 | **ARCHIVE** | 1,2,3,4 | Approach A adopted then rearchitected twice; its concrete shape never existed in current form. |
| `docs/research/input-panel-overflow.md` | 2026-05-02 | 261 | **ARCHIVE** | 1,3 | Fix landed, pinned by app_input_overflow_test.go. Its layout recap paragraph is the salvageable part. |
| `docs/research/m13-phase1-validation-2026-04-22.md` | 2026-07-14 | 266 | **ARCHIVE** | 1,3 | Milestone gate for a completed cutover; every CLI command it gates is deleted. |
| `docs/research/m15-phase-relevance-2026-06-06.md` | 2026-06-06 | 305 | **ARCHIVE** | 1,3,5 | Backlog triage; stale in both directions. Good reflection content, zero operational value. |
| `docs/research/manager-wake-loss-2026-05-07.md` | 2026-05-07 | 439 | **DELETE** | 1,3,4 | 439 lines root-causing a race between two symbols that no longer exist. |
| `docs/research/mcp-hang-observability-design.md` | 2026-05-08 | 412 | **DELETE** | 1,2,3 | Proposal whose angles A and C shipped; its "no context, no timeout" premise is false today. |
| `docs/research/mcp-manager-callsite-bugs.md` | 2026-05-06 | 233 | **DELETE** | 1,3 | Both bugs fixed, with the doc's own issue ID cited in the fix. |
| `docs/research/mcp-notifications-coverage.md` | 2026-05-11 | 242 | **LIVE** | 1,3 | Records an EXTERNAL fact the tree cannot re-derive. Add client-version banner. |
| `docs/research/mcp-surface-audit-2026-04-22.md` | 2026-07-14 | 460 | **DELETE** | 1,3 | Asserts a countable fact - "Twelve tools" - that is off by 7. Three of its names are now blocked by a regression test. |
| `docs/research/messaging-delivery-architecture-2026-05-12.md` | 2026-05-13 | 265 | **DELETE** | 1,3,4 | Self-titled "Ground Truth"; every structural claim false. Three amendment banners make it look maintained. |
| `docs/research/notification-injection-race-2026-05-14.md` | 2026-05-14 | 133 | **ARCHIVE** | 1,3,4,5 | KEEP ON MERIT. The causal record of why QUM-817 rewrote turn ownership - not recoverable from code. Needs a dead-code-map warning. |
| `docs/research/open-source-readiness/01-licensing.md` | 2026-04-06 | 61 | **DELETE** | 1 | Opens "No LICENSE file exists". It exists. |
| `docs/research/open-source-readiness/02-secrets-scan.md` | 2026-07-14 | 99 | **ARCHIVE** | 1,3 | Self-marked resolved; names the live control (guard-employer-leak). Policy is live elsewhere; this is its origin story. |
| `docs/research/open-source-readiness/03-security-audit.md` | 2026-04-30 | 169 | **ARCHIVE** | 1,3 | CONTAINS AN UNFIXED CRITICAL: agent-name path traversal. No ValidateAgentName exists anywhere. Promote to Linear before archiving. |
| `docs/research/open-source-readiness/04-release-mechanism.md` | 2026-04-06 | 235 | **DELETE** | 1,2 | "No release automation, no .goreleaser.yaml, no workflow." All three exist. |
| `docs/research/open-source-readiness/05-cross-platform.md` | 2026-04-06 | 249 | **DELETE** | 1,3 | Audits internal/tmux/ line-by-line. That package does not exist. |
| `docs/research/open-source-readiness/06-installer-distribution.md` | 2026-04-06 | 429 | **DELETE** | 1,2 | "No install.sh, no .github/." Both exist. Largest dead file in its batch. |
| `docs/research/open-source-readiness/07-unknown-unknowns.md` | 2026-07-14 | 256 | **DELETE** | 1,3 | Built around Beads as a core concern. Tracker is Linear. Reads as an audit of a different repo. |
| `docs/research/open-source-readiness/README.md` | 2026-04-07 | 29 | **DELETE** | 3 | All "pending decisions" settled; repo is public with v0.1.1 shipped. |
| `docs/research/paste-input-ux-synergy.md` | 2026-05-04 | 410 | **ARCHIVE** | 1,3,4 | Both recommendations shipped; behaviour now lives in code. |
| `docs/research/paste-pipeline-architecture.md` | 2026-05-04 | 469 | **DELETE** | 1,3,4 | Recommends the OPPOSITE of what shipped. Reads as live guidance. |
| `docs/research/paste-render-cadence.md` | 2026-05-02 | 135 | **DELETE** | 1,3 | Shipped differently; vendored line cites unverifiable and stale. |
| `docs/research/permission-hang-forensic-2026-05-19.md` | 2026-06-10 | 354 | **ARCHIVE** | 1,3,5 | Hypothesis explicitly falsified; its central object readTurn was renamed runReader. |
| `docs/research/qum-1000-local-command-strand-design.md` | 2026-07-27 | 399 | **ARCHIVE** | 1,3,5 | Design landed; the durable correction is already absorbed into CLAUDE.md. |
| `docs/research/qum-1061-child-drain-inflight-asymmetry.md` | 2026-08-06 | 200 | **ARCHIVE** | 1,3,5 | COMMITTED 2026-08-06 AND ALREADY FALSE - QUM-1066 made the filter unconditional. Recency doing persuasive work the content no longer earns. |
| `docs/research/qum-334-bridge-bleed.md` | 2026-07-14 | 311 | **DELETE** | 1,3 | 30-row line table against internal/tui/bridge.go, which does not exist. |
| `docs/research/qum-371-scope-update-2026-05-19.md` | 2026-05-19 | 372 | **ARCHIVE** | 1,3 | Shipped. Purely a planning snapshot. |
| `docs/research/qum-386-regression-2026-05-21.md` | 2026-07-14 | 134 | **DELETE** | 1,3 | Root cause removed by QUM-914; attribution now flows through ParentToolID. |
| `docs/research/qum-432-stripped-bracketed-paste-plan.md` | 2026-05-01 | 193 | **DELETE** | 1,2,3 | Superseded twice over; heavy vendored-source line cites. |
| `docs/research/qum-458-e2e-leak-analysis.md` | 2026-05-04 | 469 | **ARCHIVE** | 1,2,3,4 | All four gaps closed; script inventory stale. |
| `docs/research/qum-462-live-verify.md` | 2026-05-06 | 163 | **DELETE** | 1,3 | A PASS verdict about a code path that no longer exists. Worse than no verification. |
| `docs/research/qum-488-delegate-wake-investigation.md` | 2026-05-06 | 223 | **DELETE** | 1,3 | Hinges on state.NextTask - zero occurrences tree-wide. |
| `docs/research/qum-549-send-interrupt-during-mcp-tool-wait.md` | 2026-05-13 | 339 | **ARCHIVE** | 1,3,5 | Correct diagnosis, fixed by QUM-552. Cited from internal/backend/session.go. |
| `docs/research/qum-552-sandbox-transcript.md` | 2026-05-13 | 228 | **DELETE** | 1,3 | Raw go test -v transcript. The tests are the durable artifact; this adds nothing. |
| `docs/research/qum-570-startturn-caller-map.md` | 2026-05-15 | 118 | **DELETE** | 1,3 | A caller map whose sole production caller is a deleted file. Mapping a deleted machine. |
| `docs/research/qum-606-recover-zombie-2026-05-20.md` | 2026-07-14 | 437 | **ARCHIVE** | 1,3,4 | Fixed, and the fix is commented in place naming the historical cause. |
| `docs/research/qum-608-paste-pipeline-deep-dive-2026-05-20.md` | 2026-06-09 | 656 | **ARCHIVE** | 1,2,3,4 | The paste doc that actually shipped; carries its own staleness banner. |
| `docs/research/qum-611-ask-question-wedge-2026-05-21.md` | 2026-05-21 | 588 | **ARCHIVE** | 1,3,4 | Fix shipped; but its single-slot pending-submit paragraph was deleted by QUM-828. |
| `docs/research/qum-615-agent-liveness-spec-2026-05-26.md` | 2026-07-14 | 578 | **ARCHIVE** | 1,3,4 | Implemented; its LOCKED decisions are superseded and contradicted by CLAUDE.md's QUM-786 lifecycle contract. |
| `docs/research/qum-617-text-selection-2026-05-21.md` | 2026-07-14 | 459 | **DELETE** | 1,2,3 | Recommends a selectionMode toggle CLAUDE.md records as deliberately retired. Live half already in CLAUDE.md. |
| `docs/research/qum-618-wedge-rootcause-2026-05-26.md` | 2026-06-10 | 156 | **ARCHIVE** | 1,3,5 | Root cause was TurnTimeout, which has zero occurrences. Will send an investigator hunting a timeout that cannot fire. |
| `docs/research/qum-619-idle-interrupt-race-2026-05-21.md` | 2026-05-21 | 465 | **DELETE** | 1,3 | Names ForceInterruptForDelivery as the bug site; deleted by QUM-821. ClassInterrupt survives, so a one-symbol spot-check falsely confirms it. |
| `docs/research/qum-670-baseline.md` | 2026-06-04 | 363 | **DELETE** | 1,2,3 | Benchmarks against a replaced render path; QUM-685 explicitly retires its anchor numbers. |
| `docs/research/qum-685-bench-investigation.md` | 2026-06-06 | 243 | **ARCHIVE** | 1,3,5 | Corrective conclusion sound; absolute ms figures two months and one host old. Re-measure, do not cite. |
| `docs/research/qum-727-design.md` | 2026-07-14 | 522 | **ARCHIVE** | 1,3,4 | Implemented differently (StopAfterTurn, not Stop). Its stopped status is retired as a write target. |
| `docs/research/qum-991-foreign-content-guard/decision.md` | 2026-07-27 | 611 | **LIVE** | 1,2,4,5 | Open, unactioned decision: the proposed guard does not exist. Needs a decision, not deletion. |
| `docs/research/qum-991-foreign-content-guard/repro-binary-blindness.sh` | 2026-07-27 | 311 | **LIVE** | 2,5 | Load-bearing evidence. Misfiled: executables belong in scripts/. Must NOT be gated as-is. |
| `docs/research/qum-991-foreign-content-guard/repro-design-probes.sh` | 2026-07-27 | 116 | **LIVE** | 2 | Measurement probe. Move to scripts/. |
| `docs/research/qum-991-foreign-content-guard/repro-hook-coverage.sh` | 2026-07-27 | 380 | **LIVE** | 2,5 | Only harness pinning CLAUDE.md's guard-main-ref claims. Move to scripts/ and gate. |
| `docs/research/realtime-message-injection.md` | 2026-04-09 | 304 | **DELETE** | 1,3 | The only doc with ZERO surviving named files. Its "Current Architecture" maps a deleted subsystem. |
| `docs/research/sandbox-notifier-leak-2026-04-22.md` | 2026-06-10 | 124 | **ARCHIVE** | 3,4 | Correctly-bannered corpse; self-declared it would self-resolve, and it did. |
| `docs/research/status-reliability-findings.md` | 2026-04-07 | 175 | **ARCHIVE** | 3,4,5 | Good incident narrative, dead code refs, no banner. |
| `docs/research/teardown-unbounded-wait-audit.md` | 2026-05-13 | 118 | **LIVE** | 1,3,4 | teardownSession/withRuntimeStopTimeout live. Misfiled as research; it is an invariant record. |
| `docs/research/tmux-elimination-research.md` | 2026-04-30 | 316 | **DELETE** | 1,2,3 | Opens present-tense "Tmux is not dead... process container for ALL child agents". Flatly false; internal/tmux does not exist. |
| `docs/research/tmux-tui-cruft-audit-2026-05-06.md` | 2026-05-06 | 418 | **DELETE** | 1,3,5 | All five findings discharged. Says "Tmux stays" - wrong. A completed punchlist presented as an inventory. |
| `docs/research/token-usage-tracking.md` | 2026-04-29 | 194 | **DELETE** | 1,3 | Asserts a gap that internal/usage/ closed. Could cause re-implementation of a shipped feature. |
| `docs/research/tui-input-disappears-with-tall-tree.md` | 2026-06-10 | 102 | **ARCHIVE** | 1,3,4 | Root-cause class still true; framed around a deleted env var and pre-rewrite line numbers. Never reproduced. |
| `docs/research/tui-parity-audit-2026-04-22.md` | 2026-04-30 | 336 | **DELETE** | 1,3,6 | Decides whether the TUI should replace a deleted command in a deleted mode. |
| `docs/research/tui-render-corruption-2026-04-22.txt` | 2026-04-30 | 45 | **DELETE** | 3 | Raw 45-line tmux pane capture. Incident-shaped, not docs-shaped. |
| `docs/research/tui-render-corruption-diagnosis-2026-04-22.md` | 2026-04-30 | 173 | **ARCHIVE** | 1,3,4 | Bug fixed by exactly its recommendation at tree.go:39. Reads as an open bug report. |
| `docs/research/unified-runtime-messaging-audit.md` | 2026-05-06 | 654 | **DELETE** | 1,3,5 | Honest banner, but 654 lines understating the decay; four more rewrites landed after the banner. |
| `docs/research/weave-session-cycling-2026-05-05.md` | 2026-06-10 | 378 | **ARCHIVE** | 1,3,4,5 | Well-bannered; genuinely good reflection section. |
| `docs/testing/m4-manager-smoke-test.md` | 2026-06-04 | 391 | **DELETE** | 2,6 | Self-admits it references completed milestones; every referenced test file is gone. A procedure that cannot fail because it cannot run. |
| `docs/testing/smoke-test-memory.md` | 2026-05-14 | 91 | **LIVE** | 2 | Script exists, procedure runs. Thin; fold next to memory architecture. |
| `docs/todo/punchlist.md` | 2026-06-10 | 61 | **DELETE** | 3,6 | Duplicate tracker. Linear is the tracker; CLAUDE.local.md still advertises this as in-flight. |
| `docs/research/m13-phase1-evidence/` **(26 files)** | 2026-04-22/30 | 1563 | **DELETE** | 3 | Raw tmux pane captures. Only 11 distinct contents — 15 files are literal byte-duplicates. `ec7-weave-system-prompt.md` is a frozen copy of generated output whose source is `internal/agent/prompt.go`. |
