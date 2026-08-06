# Memory-store probe — deliberate falsification sweep of the root agent's two memory stores

**Scope.** Both agent memory stores, probed against the tree at `main` = `3d92e2c`
(`v0.5.4-27-g3d92e2c`). Read-and-report only: nothing in either store was edited, moved,
or deleted, and no Linear issues were filed. No builds, no `make`, no e2e, no sandboxes —
`git`, `grep`, `find`, and file reads only.

**Store naming used throughout.** *Store A* = the project's own agent memory
(`persistent.md`, `timeline.md`, `timeline.md.legacy-stalled`, `sessions/`).
*Store B* = the harness's project auto-memory (a refreshed `persistent_knowledge.md`,
an index file, and five write-once topic files). Both stores live outside the repo and
are not checked in. Claims below are **paraphrased**; verbatim quotation is kept to the
minimum needed to identify a line, and no personal, employer, or session identifiers,
absolute home paths, or user quotations appear in this file.

---

## 1. Verdict

| | Store A | Store B | total |
|---|---|---|---|
| Standing present-tense claims extracted | 31 | 56 | 87 |
| — of those, checkable against the tree | 26 | 45 | 71 |
| **Falsified as written** | **5** | **11** | **16** |
| Unverifiable-as-stated | 5 | 12 | 17 |
| Verified (with stated control) | 21 | 34 | 55 |

**Headline: roughly one standing present-tense claim in five is wrong as written.**
The rate is not uniform — see §5 for the denominator and §6 for where it concentrates.

Nine of the sixteen falsifications are **over-compression**, not invention: a true
observation restated with its scope, its causal attribution, or its currency widened.
That is the shape the brief predicted, and it is the shape that survives review, because
the sentence reads as correct to anyone who already knows the true version.

The two most consequential are called out first below.

---

## 2. Falsified

Ordered by consequence. "Would promote" marks claims that a pending decision to lift
these stores into checked-in documentation would ship verbatim to every agent.

| # | Claim (paraphrased) | Store / file | What is actually true | Evidence | Positive control | Would promote |
|---|---|---|---|---|---|---|
| F1 | `report_status({state: complete\|failure})` **halts** the agent, so it must be the **last** action. | A · persistent.md; B · persistent_knowledge.md | Terminal reports schedule teardown **deferred to the end of the current turn** (`StopAfterTurn`, 10s runaway cap), so a follow-on `send_message` or trailing text in the same turn **survives by design** (QUM-866). `complete` and `failure` are *identical* on the teardown decision — the implied asymmetry does not exist. Neither is permanent: both are revivable; only `retire`/`kill` terminate. | `internal/supervisor/real.go:2112` (`teardown := complete \|\| failure`), `:2115-2126` (goroutine + `StopAfterTurn`, not `Stop`); `internal/supervisor/runtime.go:688-706`, `:727-770`. | The same read surfaced the contrasting `working`/`blocked` branch (no teardown), so the probe distinguishes teardown from no-teardown rather than reporting it everywhere. | ✅ |
| F2 | `interrupt=true` on `send_message` is rejected **when the caller is a child of the recipient**. | A · persistent.md; B · persistent_knowledge.md | The rule is: the caller must be a **strict ancestor** of the recipient (any depth ≤16). Child→parent is one instance of a much broader gate — **siblings, peers, unrelated agents and self are all rejected too**. Stating the child→parent case as *the* rule implies sibling interrupts are permitted. They are not. | `internal/supervisor/real.go:1817-1834` (three ordered rejections), `isAncestor` at `:1960-1979`. Gate is evaluated against the original `to`, not the route-up target (`:1827-1829`). | The same read produced the *accepting* path (`ClassInterrupt`, `:1881`), so the probe is not only able to see rejections. | ✅ |
| F3 | `make validate` "found **9 real races** across 2 packages". | A · persistent.md | The repo's own documentation was written specifically to retire this figure: *a race count is run-dependent — do not quote a bare total.* Pre-fix one package reported **8** (six writer races plus two from an append), the other reported **3 in three of four runs and 2 in the fourth**. The "9" traces to a commit subject line whose figure omits two of the reports. Correct form: *"races in two packages, count varying by run."* "2 packages", the +23% cost figure (99.0s→122.2s) and the production-defect description are all verified. | `CLAUDE.md` QUM-972 section (the run-dependence warning, the per-package figures, and the naming of the superseded commit-message figure); `Makefile` `validate`/`test-race`. | The probe distinguishes: the bare `test:` target exists in the Makefile and is visibly *not* a `validate` dependency, so the wiring check can go negative. | ✅ |
| F4 | Claude model ID reference: **opus = `claude-opus-4-8`**. | A · persistent.md | `claude-opus-4-8` is **Claude Opus 4.8 — a different, older model**. The current Opus is `claude-opus-5`. Store B states this correctly; Store A does not, so the two stores disagree and A is the wrong one. Store A's sonnet/haiku/fable IDs and its context-window claims are all correct. Note the repo itself never uses full model IDs (it uses the aliases `opus`/`sonnet`/`haiku`/`fable`), and `internal/rootinit/tools_test.go:126` lists `claude-opus-4-8` among strings the spawn validator must **reject**. | Harness model reference (Opus 5 = `claude-opus-5`; Opus 4.8 = `claude-opus-4-8`, a separate row); `internal/rootinit/tools.go:80` `ValidSpawnModels`. | The same reference returns the *correct* value for the three IDs Store A got right, and lists `claude-opus-4-8` as a real-but-different model — so the probe distinguishes "not a model" from "wrong model". | ✅ |
| F5 | A plan file is indexed as an **active, unimplemented** plan, and states **"Pieces 2 and 3 remain unstarted."** | B · index + topic file | **Piece 3 has shipped in full.** All three of its parts are on `main`: the two drain implementations were unified (`internal/supervisor/drain.go`; `runDrain` at `:436`, with `weave_handle.go:248` and `runtime_launcher.go:597` now one-line wrappers), the channel-policy table exists as the `drainPolicy` struct (`drain.go:54`, `:101`, `:143`), and the heartbeat was **deleted outright** rather than made an observer. Only Piece 2 (per-turn uuid ack attribution) is genuinely unstarted. An implementer sent to this plan would rebuild work already on `main`. | Commits `56c3184` (drain unification) and `5072151` (heartbeat delete) on `main`; no `heartbeat.go` under `internal/supervisor/`. | The same `git log` grep *does* return hits for the shipped issue numbers, so it is not silently empty; and the Piece-2 branches are confirmed **not** ancestors of `main`, so the probe reports "unstarted" where that is true. | ✅ |
| F6 | `messages_list` has **no working `unread_only` filter**. | B · persistent_knowledge.md | The parameter is **`filter`**, enum `[all, unread, read, archived, status]`, and `unread` **is implemented and works**. The capability exists under a different name. (Independently re-derived from schema → MCP handler → supervisor → library; not a re-run of the earlier query.) The adjacent claim — **no default limit** — is correct and is the real hazard: the default is unbounded, clamped at 500, with no offset or cursor. | `internal/sprawlmcp/tools.go:263-278` (schema); `internal/messages/messages.go:674-676` (`unread` → `dirs=["new"]`); `internal/supervisor/real.go:2362-2369`, `:2372-2373`. | The same grep surfaced filter values that *do* exist, including an asymmetry nobody claimed (below), so a working `unread` could not have been missed. | ✅ |
| F7 | `retire` **unconditionally** `RemoveAll`s the agent directory. | A · persistent.md; B · persistent_knowledge.md | The **conclusion is right** (that path is not durable) but the mechanism and the modifier are wrong. The `RemoveAll` in `retire.go` targets only the agent's `logs` subdirectory and its failure is a *warning*. The whole-directory wipe is `state.DeleteAgent`. And it is **not unconditional** — several earlier gates return before it (uncommitted changes in a non-subagent worktree without `force`, mutually-exclusive flags, agent-not-found, merge-ownership checks, merge failure, unmerged commits, cascading children). Messages are *archived* rather than deleted, so the mailbox is a partial exception. | `internal/agent/retire.go:54-57` (logs only, warn), `:81` → `internal/state/state.go:350-360` (`DeleteAgent`, fatal); gates at `retire.go:38-42` and in `internal/agentops/retire.go`. | The `RemoveAll` grep returned positives at three separate sites, so it can find deletion sites; it is not silent by construction. | ⚠️ conclusion only |
| F8 | Committing on `main` runs the full `make validate` via the pre-commit hook — **budget over 2 minutes or it gets SIGTERMed mid-run**. | B · persistent_knowledge.md | The "full `make validate`" half is **verified**. The "2 minutes / SIGTERM" half is **not a property of the hook or of any repo script** — there is no timeout or kill mechanism anywhere in `scripts/`. It is an inference about an *external caller's* timeout (most plausibly the agent harness's own default command timeout), stated as a repo mechanism. As "budget generously, the caller may time out" it is sound; as written it misattributes the cause, which is what makes it un-actionable. | `scripts/pre-commit` in full (guards, `unset GIT_*`, `make validate`; no timeout/SIGTERM/kill); the only "~2min" figure in the tree describes the *race run's duration*, not a deadline. | The same grep pattern hits in other repo scripts (session-kill calls in the e2e harnesses), so zero hits here is a real negative rather than an inert pattern. | ❌ |
| F9 | The index file describes the refreshed knowledge file as having **21 items**. | B · index | It has **20**. | Bullet count of the refreshed file. | The same counter returns **7** on the index file itself, matching its 7 links. | ❌ |
| F10 | The rebrand note names the current **root agent as `neo`**, and instructs that any surviving old-brand reference is a bug. | B · topic file | The root agent is **`weave`**, and has been since a later rename (`d22e4ef`). The note was accurate on the day it was written; carrying it forward as the *current* identity is the over-compression. The zero-stale-references half is essentially right — exactly **one** hit survives, a deliberate historical reference in a forensics doc, and **zero** in Go files. | `cmd/enter.go:538` (`rootName = "weave"`), `:450`; the rename commit; single-hit grep across `main`. | The same grep engine returns 12 hits for the current brand name in `README.md`, so it is not silently empty. | ⚠️ |
| F11 | The publish-readiness note states, in the present tense, that **three manual steps remain**: tag `v0.1.0` and push, set a social-preview image, and flip the repo public. | B · topic file | **Two of the three are long done.** `v0.1.0` is tagged (dated the same day the note was written), and the repo is **public**. `main` is at `v0.5.4-27-g3d92e2c` with 40+ tags merged. Only the social-preview image is unsettled. Note the index line pointing at this file is *correct* ("released, repo public") — the index contradicts the body it summarises, and the index is the accurate one. | `git tag`, `git describe --tags main`; `gh repo view --json visibility` → `PUBLIC`. | `git tag` returns both `v0.1.0` and `v0.1.1` and the current series, so the probe can see tags that exist; a genuinely untagged release would have shown as absent. | ❌ |

**One further falsification is carried, not re-derived.** The claim that a disk-full
failure originated in Go build-cache pressure was falsified earlier the same day by
another agent (the filesystem named had ample space; a different device was full). It is
counted in the totals but I did not independently reproduce it, and I say so rather than
inheriting someone else's result as my own — a re-run of another agent's query is a copy,
not a control.

### Incidental defects surfaced that no claim mentions

Not part of the brief, recorded because they were found while probing and would otherwise
be lost:

1. **`filter:"sent"` is rejected at the MCP boundary** although the underlying library
   supports it, and the boundary's error message names a set that disagrees with the
   library's own (`internal/supervisor/real.go:2362-2369` vs `internal/messages/messages.go:685-686`).
2. **`send_message(interrupt=true)` targeting the root agent is non-preemptive** — the
   interrupt-class priority is `now` for children but `next` for the root, a deliberate
   and documented locked asymmetry (`internal/supervisor/drain.go:100-127`). Anyone
   reading "interrupt=true preempts" will be wrong for exactly one recipient.
3. **A delegated task can be silently lost with only a warning**: the task write is
   bounded by a timeout, and a timeout leaves the task marked in-progress with its
   notification undelivered and never re-fed, because the loop skips non-`queued` tasks
   (`internal/supervisor/runtime_launcher.go:553-566`).

---

## 3. Unverifiable-as-stated

A real category, not a soft falsification. Each of these is unfit to promote **regardless
of whether it happens to be true**, because a reader cannot check it as written.

| Claim (paraphrased) | Store | Why it cannot be checked |
|---|---|---|
| "This failure class was seen ~11 times in one session" (Store A) / "~12 times" (Store B) | A, B | An unreproducible tally over a past session, and **the two stores give different numbers for what reads as the same phenomenon** — so at most one is right and nothing in the tree adjudicates. No such figure appears anywhere in the repo. |
| "0 early settles across 761 wire logs"; "12.3% of turns ack ≥2, max 27" | B | Corpus-dated measurements. The corpus has since grown (852 log files across 46 directories today, against 761 then), so a re-run answers a *different* question and can neither confirm nor falsify the figure. Also a countable proxy: "761 wire logs" is a file count, not a turn count. |
| "123 identical notifications, 38,673 bytes, one stdin frame" | B | A past-runtime measurement. It is corroborated verbatim in the relevant commit body — but by the **same author**, so that is corroboration, not independent confirmation. The producing mechanism was deleted by that very commit, so it is no longer re-derivable. |
| "`messages_list` can dump 4000+ lines / 99K+ chars" | B | A report of one past run against one mailbox. Arithmetically consistent with the schema (≈9 lines per summary, no message body, ~440 messages under the 500 clamp) but not observable from the tree. |
| The canonical framing is "coined by" a named agent | B | The framing itself **is** in `CLAUDE.md` (verified, §4). The attribution is not, and authorship of a phrase is not a tree property. |
| `[1m]` is "Claude Code's long-context alias suffix, passed through verbatim" | A, B | "Passed through verbatim" is verified in-repo. What the suffix *means* to the CLI is upstream behaviour with no referent in this tree — and it is worth re-examining, since the models it is applied to are now natively long-context, which would make the suffix vestigial. |
| "delegate and async send_message don't order against each other" | A, B | The **premise** is verified (`later` vs `next`, §4). What that implies for interleaving is a claim about the upstream CLI's queue scheduler, which is not in this tree. |
| Named issue numbers attached to past incidents (double-delivery, silent loss, ordering) | A, B | Provenance pointers to a tracker, not findings. Out of reach of a read-only tree audit; report them as citations, never as evidence. |
| "Spec-first saves 30+ min in agent ramp-up"; wave sizing; "manager-wrapped has proven clean across ~10-issue epics" | A, B | Unfalsifiable process estimates. Reasonable heuristics; not facts. |
| "Live testing is the gold standard" | A, B | A value judgement with no truth-condition in a repo. What *is* checkable holds: a written mandate exists — but scoped to **TUI code**, not the broader "new interactive features", and it gates *reporting done*, which is upstream of marking an issue Done. The phrase "gold standard" is not the repo's language anywhere in this context; its single repo-wide hit is about an unrelated subject. |
| Personal working-style claims about the user | A, B | About a person, not the tree. Out of scope for this method; not counted in the denominator. |
| The rescue-ref count stated in the most recent session file | A | Was 16; is 21 now. Live agents create these continuously, so the number is a moving target by construction — a claim about a moment, not a standing fact. |
| "CLAUDE.md went 784→768 lines" | A | The **768 endpoint is verified**. The 784 start point cannot be recovered: the same session rewrote the history that would contain it. A claim whose own session destroyed its referent. |

---

## 4. Verified

Terse, with the control that makes each negative trustworthy.

**Store A and B (shared).** The MCP server is named `sprawl` and its tool names are
unprefixed *on the wire* — with the caveat that the client namespaces them, so the name an
agent actually types **is** prefixed (control: the name grep returned 19 literal values, so
a prefixed one would have shown). Both main-integrity guards exist as described, including
the bypass-proof reference-transaction hook, and both are installed by the worktree-setup
hook (control: the same tree listing found all three scripts; a missing one would be
visibly absent) — with the nuance that the installs are warn-and-continue, so auto-install
is best-effort. `validate` really does depend on a `-race` invocation and not on the bare
`test` target. The e2e driver's exit-code contract really is **5-way** (0–4), independently
enumerated from the script's own `exit` sites and cross-checked against its header; `77` is
a sixth documented code the driver never itself returns, so "6 codes" would be wrong in the
other direction (control: the grep returned 20+ exit sites including duplicates, so it is
not under-matching). `tmux capture-pane -e` does preserve SGR and is used at exactly the two
sites that assert on attributes (control: ~20 call sites, the large majority deliberately
*without* `-e` — so the probe discriminates the two forms; stated as a blanket rule the
claim over-generalises). `schema_version` plus migrate-on-load is real in production code
(v3, `internal/state/state.go:94`, `:120-123`, `:178`), and `RegisterRootRuntime` does
persist via `state.SaveAgent` — though only when the type field is empty, i.e. a one-shot
backfill rather than a general persist-everything pattern. Spawn's `system_prompt`
**appends** under a `## Operator Instructions` header and does not replace
(`internal/supervisor/runtime_launcher.go:312-318`), and the model enum exposes the full
option set (`internal/rootinit/tools.go:80`). `internal/shlint` is absent from the tree, and
— the part that absence alone cannot establish — the "four blind spots across four review
rounds" figure is corroborated in detail, with a four-row table naming each spelling
(control: absence-of-package is trivially true and settles nothing; the content grep is the
probe that can settle it, and it returned a hit). The delegate/async priority split
(`later` vs `next`) is exactly as stated (control: the grep returned three distinct
priority values, so it discriminates). The branch prefix and tracker identifiers match the
project's own local configuration.

**Store B only.** The current model IDs and context windows are **all correct**, including
the one Store A gets wrong. The local binary is network-only with respect to the hub: DB
driver imports exist in exactly one package, whose only importers are the server tree — the
CLI has no path to it (control: the grep *does* find the DB imports where they legitimately
live, and a first malformed-pathspec attempt returned empty, a would-be false negative that
was caught and re-run). The auto-continue injection and the `pendingSubmit` branch are both
gone, with only past-tense comments and a replay-only classifier surviving (controls: the
greps return live hits elsewhere in the same files). The recall and send-all-now key
handlers are real handlers, not just help text. The wire-log corpus is at exactly the stated
path and extension — 852 files, all `.ndjson`, across 46 directories, verified without
opening one. The dedup-and-truncate cap really is inside `WriteSystemMessage`
(`internal/runtime/unified.go:815-831`), and is not vacuous: its test file carries an
explicit negative control asserting small frames are untouched. The canonical framing **is**
present in `CLAUDE.md` (see §7 — my first probe said it was absent, and was wrong). The
documented squash-then-rebase recovery procedure is present as described.

**Two of Store B's own open questions, now discharged.** The store flags a *"still-unverified
gap"*: that the forbidden-terms list used by the leak guard had never been confirmed to
contain the names it is supposed to catch. It does. The list has **exactly 7 non-blank
entries**, matching the stated count, and a boolean test confirms the employer name is among
them (positive control: the same case-insensitive test finds that name in a local file known
to contain it; negative control: a nonsense string returns 0). **No term text was read into
this report, printed, or stored** — only counts and booleans. Separately, the store's
unfiled worry that the leak guard *fails open when the terms file is absent* and *does not
scan commit messages* is **confirmed on both halves** — but the fail-open is a documented,
deliberate install-safety property stated in the script's own header, not an undiscovered
gap; and the guard scans the staged diff only, so commit messages are genuinely outside its
reach (control: the probe locates the actual input source, `git diff --cached`, rather than
merely failing to find an alternative).

**Session-file spot checks (Store A).** The most recent session file's checkable claims
mostly hold: `main` is exactly 3 commits ahead of the remote as stated; the described
rollback script exists at the stated path with a matching timestamp; the mandatory-test
matrix table is **30 rows** as claimed; the installed-binary version string matches
`git describe` exactly (control: the same command on the parent commit returns a different
string). Two do not: the matrix-row count only reproduces with a **table-scoped** probe — a
naive whole-file grep for the row pattern returns 35, because it sweeps in a second
three-space-indented table, which is precisely the countable-proxy trap; and the claim that
a new test file "gave the package its first ever gate, permanently" is **true on a branch
and not on `main`** — an accurate result stated one merge early, in the same file that
elsewhere correctly records that the branch has deliberately not landed.

---

## 5. Base rate — denominator and method

**Unit of counting.** One atomic assertion. A single memory bullet routinely carries four
or five; each was extracted and adjudicated separately, so bullets are not the denominator.

**Population.** The prioritised standing-claim set named in the brief: Store A's
`persistent.md` (20 bullets), Store B's refreshed knowledge file (20 bullets), Store B's
index file (7 lines), and the five topic files the index links.

**Inclusion rule — the load-bearing decision.** A claim enters the denominator only if it
is (a) **present-tense and standing**, and (b) **checkable against the tree or the
harness**. Explicitly excluded, and *not* counted as errors:

- claims explicitly about the past ("X was true at session N") — the brief's first hazard;
- normative conventions ("mark the issue In Progress on pickup") — a rule cannot be false,
  only unfollowed;
- claims about a person's preferences or working style.

Claims that are standing and present-tense but *unfalsifiable as written* are counted in a
**third** column, not folded into either verified or falsified.

**Result.**

| Population | Checkable | Falsified | Rate |
|---|---|---|---|
| Store A · `persistent.md` | 26 | 5 | **19%** |
| Store B · refreshed knowledge file | 30 | 6 | **20%** |
| Store B · index file | 7 | 2 | **29%** |
| Store B · 5 write-once topic files | 12 | 5 | **42%** |
| **All standing present-tense claims** | **71** | **16** | **23%** |

**Headline for the pending decision: about one in five standing present-tense claims in
the actively-refreshed files is wrong as written, and about two in five in the
write-once satellites.** The brief predicted the base rate would exceed the incidental
discovery rate of three in one day. It does, by roughly a factor of five.

**Reproducing this.** Take the four populations above, split every bullet into atomic
assertions, drop the past-tense/normative/personal ones, and adjudicate each remainder
against `main` with a stated positive control. The one judgement call that moves the number
is how strictly "as written" is read: an over-compressed claim whose *core* is true but
whose *scope* is wrong (F1, F2, F7, F8, F10) is counted here as **falsified**, because the
sentence a reader would act on is the wrong one. Counting only outright-false claims would
put the rate near 8%. Both numbers are defensible; the higher one is the right input to a
promotion decision, because promotion ships the sentence, not the core.

**Sampling of the historical material** (reported separately, not folded into the rate
above). `timeline.md` is 86 rows, each a dated one-line session record — historical by
construction, and out of scope for a present-tense sweep. `sessions/` is 87 files; I read
the most recent in full and sampled six more spanning the full date range by taking every
15th file in modification order. Every sampled file carries a `timestamp` in its
frontmatter and a dated summary heading, i.e. the corpus is **self-dating**, which is the
structural reason it is low-yield for this method: its claims are past-tense by
construction and cannot be falsified by present state.

**`timeline.md.legacy-stalled` is dead — reportable on its own.** Its last entry is dated
**2026-04-22**, and it is a fine-grained event log, a different format from the one-row-per-
session `timeline.md` that superseded it. Its rename to `.legacy-stalled` is dated
2026-05-07 — the same day `timeline.md` records a three-tier append-only memory
re-architecture landing. It is referenced nowhere in the tree. It stalled 15 days before it
was renamed, has not been appended to in ~3.5 months, and can be archived.

---

## 6. Which store is worse, and why

**Store B, and the difference is structural, not editorial.**

Head to head on the refreshed files the two stores are indistinguishable — 19% against 20%,
inside the noise of my own inclusion judgement. And on the claims where they diverge, they
split evenly: Store A carries the wrong model ID (F4) and the retired race count (F3), which
Store B gets right or omits; Store B carries the false filter claim (F6) and the misattributed
timeout (F8), which Store A does not.

The gap is entirely in **what Store B has that Store A does not**: an index and five
write-once satellite files.

| | Store A | Store B |
|---|---|---|
| Tiers | 1 refreshed file (+ dated history) | 1 refreshed file, 1 index, 5 topic files |
| Refreshed each handoff | the one file | the one file **only** |
| Never refreshed | — | index + all 5 topic files |
| Falsification rate | 19% | 20% / 29% / **42%** by tier |

**Four of the five satellite files carry a falsified standing claim** (F5, F10, F11, and the
branch-convention drift below); their modification times are 100–121 days old, while the
knowledge file they sit beside is rewritten at every handoff. Nothing re-reads them, so
nothing corrects them — and each states its stale content in the **present tense**, which is
what converts an accurate historical note into a false current one. Three of the four were
accurate on the day they were written.

**The index adds a second, independent compression step and is the most error-dense surface
per line** (2 of 7). Both of its failures are characteristic of an index rather than of a
document: one is a countable that drifted (F9), and the other mislabels a plan as active and
unimplemented when two thirds of it has shipped (F5) — an error that exists *only* at the
index layer, since the plan file itself is at least internally consistent about what it
knew. Notably the index is also *right where its target is wrong* (F11): its one-line
summary contradicts the body of the file it points at, and the summary is the accurate one.
An index that can be both more and less correct than its target is not a navigation aid; it
is a second store.

**One further satellite drift, minor but illustrative.** A profile file states the branch
convention as a loose form, while both refreshed files state the tighter, issue-keyed form
for tracked work. Actual practice: 109 of 139 branches (78%) use the tight form. The
satellite is not false — it is *under-specified*, which is the failure mode a write-once
file drifts into when the real convention tightens around it.

**Consequence for the promotion decision.** The two refreshed files are the *better* halves
of their stores and still run ~20%. The satellites run ~42% and would be promoted with the
same authority. Whatever is promoted, the tiering has to be flattened first, or the
promotion will encode "never refreshed" as "checked in", which reads as *more* authoritative,
not less.

---

## 7. Method notes, including one of my own probes that failed

**Controls.** Every negative result in this report names the positive control that shows the
probe can fire. Two probes were re-derived from scratch rather than re-run from an earlier
agent's description, per the brief.

**A false negative I produced, caught, and corrected.** Checking whether the canonical
framing about asymmetric verification is present in `CLAUDE.md`, a line-anchored grep
returned **0**, and my accompanying "negative control" *also* returned 0 — which I briefly
read as confirmation. It was not: two zeros from a probe that cannot produce a one are the
same zero. The file is hard-wrapped, and the phrase spans a line break. A
whitespace-normalised re-run with a genuine positive control (a different known-present
wrapped phrase, which the line-anchored form also missed) returns **1**. The claim is
**verified**.

This is the class the stores themselves name, reproduced by the agent auditing them, while
looking for it — which is the point. The specific lesson is narrower than "be careful": a
negative control that shares the probe's defect is not a control at all, because it is
present in both the working and the broken case. Only a *positive* control — a case known to
exist that the probe must find — discriminates. The two zeros looked exactly like a correct
result.

**Public-repo handling.** Both stores contain personal and employer-adjacent material. Every
claim here is paraphrased; no store was pasted; no home paths, session identifiers, agent-to-
user quotations, or forbidden terms appear. The forbidden-terms check was deliberately
designed to emit a boolean and a count only, so that verifying a leak-detection list could
not itself become a leak. I re-read this file in full before committing it, specifically
looking for the two leak classes a prior agent found that no regex catches.

**A second failed probe, discarded rather than cited.** I also tried to scan this file
against the forbidden-terms list mechanically. It returned zero — and so did its positive
control, a file known to contain a listed term. **A scan that cannot find a term it is
pointed straight at proves nothing about a file where it finds none**, so that result is
discarded and is not part of the assurance above. What the assurance rests on instead is a
set of targeted greps that each demonstrably matched somewhere else during this audit
(employer name, home paths, user handle, session-identifier shape, cloud-resource names:
all zero here), plus the manual read-through. Recorded because a zero from a broken scan is
the exact false-green the stores warn about, and because the second control is what caught
it — the same lesson as §7's first paragraph, hit twice in one session while looking for it.
