# D4 execution report — the `docs/` cut

**Author:** flux (engineer) · **Date:** 2026-08-06 · **Branch:** `dmotles/docs-restructure-d4`
**Work order:** `docs-directory.md` (cipher, branch `dmotles/docs-audit-docs-dir`, commit `b75df17`), as corrected by `DECISION.md` §3.2/§3.3 and by `pre-archive-triage.md` (pulse, branch `dmotles/docs-triage-unactioned`).
**Scope guard:** `CLAUDE.md` and `.claude/skills/` were not touched. No build, no `make validate`, no e2e, no sandbox was invoked by me; the pre-commit hook ran `make validate` on each commit, which is the intended path.

---

## What happened, in one table

| | before | after |
|---|---:|---:|
| files under `docs/` | 144 | 80 |
| **live corpus** (excl. `archive/`, `audits/`) | 144 | **27** |
| lines, live corpus | 39,778 | **8,882** |
| quarantined to `docs/archive/` | — | 52 |
| deleted | — | 65 |
| top-level directories | 12 | 6 |

Five commits, each answering one question:

| commit | what |
|---|---|
| `docs: merge docs/design/ into docs/designs/` | 20 renames + 22 inbound citations across 17 non-docs files |
| `docs(archive): quarantine 52 superseded documents` | 52 renames + 36 inbound citations across 28 files |
| `docs: delete 65 documents superseded by the tree` | 65 deletions + 2 source-comment de-links |
| `docs: relocate the KEEP set and repair every link the cut broke` | 8 renames + 46 link repairs |
| `docs: add docs/README.md` | the index and the entry rule |

---

## 1. The classification was re-derived, not transcribed — and it did not match its own headline

**The correct split is 26 KEEP / 53 ARCHIVE / 65 DELETE, not the 24 / 55 / 65 in the work order's headline.**

Derived mechanically from Appendix B rather than from the prose:

| bucket | derivation | count |
|---|---:|---:|
| KEEP | `LIVE` + `LIVE-PARTIAL` + `UPDATE` + `REWRITE` + `DESIGN-ONLY` | 26 |
| ARCHIVE | `ARCHIVE` (48) + `SUPERSEDED` (5) | 53 |
| DELETE | 39 individual rows + 26 `m13-phase1-evidence/` files | 65 |
| | | **144** |

`SUPERSEDED` is applied to five hub docs (02, 05, 06, 08, 12) and **is not defined in Appendix B's own bucket key**. Reading it as ARCHIVE is the only reading under which the totals close, and it matches all five verdict texts ("Never re-scoped", "still specifies OIDC…"). DELETE = 65 matches the headline exactly, which localises the error to the 24/55 prose rather than to the table.

Reconciliation against the tree was exact in both directions: no file under `docs/` is absent from Appendix B, and no Appendix B row is absent from the tree.

**This is worth recording as a finding, not just a discrepancy.** §5 of the work order rotted *relative to its own appendix, inside a single document, written in one sitting* — and separately omits two KEEP files (`mcp-notifications-coverage.md`, `qum-991-foreign-content-guard/decision.md`) from its destination table. There was no elapsed time for anything to go stale. It is the enumeration failure the audit is about, occurring in the audit's own output. pulse hit the same wall independently and also refused to paper over it.

**Consequence for anyone reading that document later: derive from Appendix B, never from §5.**

---

## 2. What was deliberately kept, against the classification

### `docs/research/open-source-readiness/03-security-audit.md` — held live, not archived

Classified ARCHIVE. **Kept in place.** It and `docs/designs/hub/security-privacy.md` are the only two files in the repo that state the security trust model, and QUM-1138 exists to write that model down durably *before* either is cut. Archiving first destroys information rather than reorganising it. It carries a HELD-LIVE banner saying so, and it leaves `docs/research/` — and that directory disappears — when QUM-1138 lands.

`security-privacy.md` needed no exception: its verdict is LIVE, so it moved with the rest of `hub/` and stays live.

The same banner **visibly strikes** the document's own headline claim that "no agent name validation exists anywhere in the codebase". It does exist — `internal/agent/validate.go` → `ValidateName`, allow-list regex plus a 64-char cap, 11+ non-test call sites, committed `30fd7fe` on 2026-04-06, **QUM-161 Done**. The claim survived four months because every probe searched for `ValidateAgentName`, *the identifier the document proposes*, which never shipped. QUM-1128 was filed on that false premise and is canceled. Struck visibly rather than silently edited, per this project's convention: the correction is more instructive than the finding was.

### `13-implementation-plan.md` — not renamed to `hub-status.md`

The work order proposes the rename. Its verdict is REWRITE and the rename is contingent on the rewrite. Renaming without rewriting produces a file whose **name asserts currency its content does not have** — which is this audit's central thesis applied to a filename, and strictly worse than an honest stale name. Left as-is; the rewrite is a follow-up.

### The three `qum-991/repro-*.sh` scripts — not moved to `scripts/`

§5 proposes relocating them. They moved with `decision.md` into `docs/designs/qum-991-foreign-content-guard/` instead. They are evidence *for* an open decision, not repo tooling; `scripts/` in this repo carries the implicature that its contents are runnable and gate-able, and the work order itself says one of them "must NOT be gated as-is" and another should be moved "**and** gated". Gating means a Makefile target and a QUM-997 assertion-floor review — real work, and outside D4. The promotion should ride with the QUM-991 decision, which is still open.

---

## 3. The one thing that stopped me

**pulse finding 9 — the `NOTICE` file and SPDX headers, from `01-licensing.md`, which is in the DELETE set and is covered by none of QUM-1134/1136/1137/1138.**

Escalated to forge before the delete commit; forge is filing it. Recorded here so the reasoning survives: filing beats declining because a decline written into *this* document is a decision recorded in an artifact that is itself a future cleanup candidate — the exact mechanism that buried these findings the first time. A Linear issue outlives `docs/`.

The doc's Action Items list five boxes. Three shipped (LICENSE, README link, copyright holder). Two were never done **and never declined**. This is pulse's mechanism exactly: *a readiness checklist rots into a DELETE verdict precisely because most of it succeeded, and the verdict then discards the minority that failed.*

**Findings 5–8 of that sweep are also uncovered by any Linear issue** — `multiSelect` silently ignored, the unbounded activity-ring write, the opposite probe defaults, the unclamped `searchOverlay`. They did **not** block the cut, because all four live in files that were **archived**, not deleted, so they remain greppable at `docs/archive/research/`. Naming them here anyway, because a cut that quietly made four known-live findings harder to find while claiming to prevent exactly that would be self-defeating.

---

## 4. Positive controls

Every "nothing references this" claim below was run with a control proving the probe *can* return a positive. A search that finds nothing and a search that cannot find anything look identical.

| claim | probe | control |
|---|---|---|
| No non-docs file still cites `docs/design/hub/` | `git grep -n 'docs/design/hub' -- ':!docs'` → only `CHANGELOG.md` | the same probe returned **22 hits across 17 files** immediately before the rewrite |
| No live non-docs file cites an archived doc | `git grep -nF -f <52 old paths> -- ':!docs'` → 1 hit | returned **37** before the rewrite; the surviving hit is `.claude/skills/`, which this task may not touch |
| Nothing outside the archive cites a deleted doc | `git grep -nF -f <65 paths> -- ':!docs/archive'` → 0 | returned **3** before repair; and the same `-F` probe over the KEEP set returns hits in 10 files, so it is live |
| The live corpus has no dangling internal links | link-resolver over all 27 live files → 1 | an injected bad markdown link **and** an injected bad backticked path were both reported; the surviving hit is `docs/security-model.md`, a *proposed* path in Priority Action #2 that correctly does not exist and that QUM-1138 tracks creating |
| The hub sibling links were all repaired | count of links to 02/05/06/08/12 | **41 before, 0 after**, and all 41 new `../../archive/hub/` targets resolve on disk |
| `make validate` never read a path this cut moved | read `scripts/test-gitignore-classes.sh` | `stage_against()` does `printf 'fixture\n' > "$r/$f"` into a `mktemp` scratch repo — the fixtures are synthetic and the script never touches the real tree. **I did not run `make validate` myself to claim this**; it is established by reading the harness. |

Two false-negative traps specific to these probes, both hit and avoided: `git grep -f` treats `.` in a path as a regex metacharacter (use `-F`), and a bare `docs/design` matches `docs/designs` as a prefix (anchor on the trailing slash).

---

## 5. Collateral outside `docs/` — 58 citations across 45 files

Not scope creep; these are the cut's own breakage. Every edit is a comment or prose string. **No code changed.**

- **22 refs / 17 files** repointed for the `design/` → `designs/` merge. This includes `proto/hub/v1/hub.proto` and the four checked-in **generated** files that carry its comment verbatim (`hub.pb.go`, `hub.connect.go`, `hub_pb.ts`, `hub_connect.ts`), edited to exactly what regeneration would emit.
- **36 refs / 28 files** repointed to `docs/archive/…` — Go source and test comments, an e2e spec header, and the `deploy/hub/**` terraform READMEs.
- **2 source comments** de-linked from deleted docs.
- **1 fixture path** in `scripts/test-gitignore-classes.sh`.

Source comments get the bare `archive/` path with no `(archived)` suffix, deliberately: in a grep hit the path segment *is* the signal, and the label would be noise in a code comment. In prose docs the suffix is required, because a rendered link hides the path.

**A gap worth knowing independently of this task:** `make proto-check` runs `buf lint` and `buf format --diff --exit-code` and **never diffs generated output against the proto**. Checked-in generated files can therefore drift from their source with a fully green `validate`. Flagged upward; not fixed here.

---

## 6. Known-broken, deliberately not fixed

| what | why not | route |
|---|---|---|
| `.claude/skills/e2e-testing-sandboxing/SKILL.md:175` cites `docs/research/qum-458-e2e-leak-analysis.md`, now archived | this task is forbidden to touch `.claude/skills/` | **D5** |
| `scripts/e2e-tests/hub-e2e.sh:14` cites `13-p1-local-e2e-and-manual-walkthrough.md` | **pre-existing** dangler — that file exists under neither spelling and never did. Carried through the rename unchanged, not introduced here | follow-up |
| `.gitignore` still negates `docs/research/m13-phase1-evidence/ec6-live-handoff-stderr.log`, now deleted | the negation is inert, but removing it changes what `test-gitignore-classes.sh` asserts about the broad-`*.log` tradeoff — a test-semantics change needing its own review | follow-up |
| `CLAUDE.local.md` advertises `docs/todo/punchlist.md`, now deleted | untracked private file; not mine to edit | forge |
| `13-implementation-plan.md` carries no status marks though Phase 0 + Phase 1 shipped | the REWRITE is its own task | follow-up |
| `chatlist-invariants.md` has zero mentions of the QUM-833/925 pending zone now inside `ChatList` | content correction, not placement; the classification flags it as UPDATE | follow-up |

The work order's §5 also proposes moving the two hub `evidence/qum-911/*.txt` files out of `docs/`. They are classified ARCHIVE, so they were simply archived — no new decision about where non-docs evidence lives was needed.

---

## 7. The entry rule (work-order item 4)

**The referential-integrity checker was NOT built.** It is D3, a separate decision, gated on this cut landing. The measured case for it is unchanged and now actionable: the dangling rate on the surviving corpus is low enough that the gate is enforceable, which it was not before.

The cheapest enforceable version — the one that needs no tooling — went into `docs/README.md` as a **placement rule** an author applies to their own draft:

> If a document enumerates call sites, lists the implementers of an interface, or mirrors a directory tree, it belongs in `archive/` the day it lands.

with two corollaries (*say what is gone, not what is there*; *name the property, not a countable proxy for it*), a required date-and-status line stated explicitly as metadata rather than as the control, and a warning not to judge staleness by git date — roughly a quarter of this tree carried a last-modified date from a one-word `sed` or a rebase squash, reflecting no content change.

**The rule is applied to its own authors**, which is the part that makes it credible: the audit documents that produced this restructure enumerate a 144-file directory tree, so they belong in `archive/`, not `audits/`. `docs/README.md` says so in those terms.

`docs/README.md` deliberately contains **no counts and no file rosters**. "There are N design docs" is the construction that rots the moment someone complies with the structure — and §5 of the work order is the proof, having rotted against its own appendix with zero elapsed time.

---

## 8. Two things in the work order I believe are wrong

Both were escalated and approved before execution.

1. **"Nothing links into `archive/`"** is contradicted by the audit's own exemplar. `architecture/memory.md` links to archived forensics, and the audit *praises* that link as "exactly the live/archive boundary this restructure should generalise". Implemented as **"a live doc may link into `archive/` only with an explicit `(archived)` label"**. An absolute rule contradicted by its own model document gets broken immediately and then ignored generally.
2. **The `hub-status.md` rename** — see §2.

---

## 9. Where the source documents live

`docs-directory.md` (cipher) and `pre-archive-triage.md` (pulse) were **not imported into this branch**, to avoid a duplicate-file conflict if their own branches are merged separately. They remain on `dmotles/docs-audit-docs-dir` (`b75df17`) and `dmotles/docs-triage-unactioned`.

**Outstanding obligation for whoever lands cipher's document:** it still asserts the withdrawn path-traversal CRITICAL in **two** places — §3's closing paragraph and the Appendix B row for `03-security-audit.md`, the latter with *"Promote to Linear before archiving."* Both must be struck before that document lands anywhere someone will act on it. That is exactly how QUM-1128 came to be filed the first time. **A withdrawal is not done when the finding is struck; it is done when every downstream reference to it is struck.** The corrected statement now lives in the HELD-LIVE banner on `03-security-audit.md` in this tree.

By the entry rule above, both documents belong in `docs/archive/` when they land, not in `docs/audits/`.
