# CLAUDE.md audit — Part A (lines 1–353)

**Auditor:** `query` (researcher) · **Date:** 2026-08-06 · **Branch:** `dmotles/docs-audit-claudemd-a`
**Surface:** `CLAUDE.md` lines 1–353 — preamble, Terminology, Lifecycle model, Build & Test,
race-detector guarantee, Commit guard, reference-transaction backstop, wrong-tree-commit recovery,
squash-merge recovery. **353 lines / ~2,794 words** (~36% of the 768-line file).

**Recommendation: cut 353 lines to ~14. Everything else moves to `docs/` or a skill, and three
paragraphs should be deleted outright because a test already enforces them.**

---

## 1. Verdict

My surface supports the thesis, and it supports it in the specific way the thesis predicts: the
harm is not that the prose is bad — it is uniformly well-argued — but that **it is a census of code
that nothing keeps current**, and the census has already rotted in three places. The most
instructive rot is the `atomicDuration` paragraph: it names exactly three files that carry the
type. There are **four**. The fourth (`internal/supervisor/weave_handle.go:87`) landed eight months
of commits ago under QUM-925, and it landed *correctly* — the author followed the convention. Only
the doc's inventory broke. That is the whole argument in one artifact: the rule was worth writing,
the list of who obeys it was not, and the list is the part that decayed.

The second finding is larger in line count. **35 of my 353 lines (the entire Lifecycle model
section) restate behaviour that `TestIsTerminal` and seven other test files already pin.** Prose
that duplicates a test is strictly worse than the test: it costs every agent every turn, and when
it diverges the test is right and the prose is what agents read.

Where I disagree with a maximal reading of the thesis: **the Terminology block (8 lines) earns its
keep outright and I would not cut a word of it**, and the squash-merge recovery section, though it
is the single biggest cut by line count, must be *moved rather than deleted* — its central claim
(that both natural correctness checks lie, in opposite directions) is expensive, verified, and not
recoverable from the code. The failure was never that this content was written down. It was that
"written down" and "in CLAUDE.md" were treated as the same thing.

---

## 2. Rotted claims found

Every factual claim in my surface was checked against the tree at this commit. Five are wrong or
materially incomplete. Ranked by how likely each is to mislead an agent into a bad action.

| # | Claim | Where | Reality (verified) | Evidence |
|---|---|---|---|---|
| **R1** | `atomicDuration` is "currently duplicated … in `internal/backend/session.go`, `internal/rootinit/consolidating_lock.go`, and `internal/merge/runtests.go`" — and the follow-on paragraph does arithmetic over that set ("Two of those **three**…") | L124–139 | **Four** definitions exist. The fourth is `internal/supervisor/weave_handle.go:87`, plus consumers in `runtime_launcher.go` and `drain.go`. The "two were fixes, the third was prevention" accounting is now a statement about a set that no longer exists. | `grep -rn "^type atomicDuration" --include=*.go .` → 4 hits (`rootinit/consolidating_lock.go:57`, `supervisor/weave_handle.go:87`, `merge/runtests.go:33`, `backend/session.go:58`). Added by `664ff74` (QUM-925); CLAUDE.md's list last touched in `d680db1`, before it. |
| **R2** | `make validate` runs "build + proto-check + fmt-check + lint + test-race-gate + test-race + wirelog-helpers-unit + e2e-matrix-unit + gitignore-classes + leak-scan" | L51–56 | The real prerequisite list contains **`test-e2e-lockwait-unit`**, which the doc omits — between `test-wirelog-helpers-unit` and `test-e2e-matrix-unit`. An agent reasoning about what validate covers will under-count it. | `Makefile:4`: `validate: build proto-check fmt-check lint test-race-gate test-race test-wirelog-helpers-unit test-e2e-lockwait-unit test-e2e-matrix-unit test-gitignore-classes leak-scan`. Added by `efd82ec` (QUM-948). |
| **R3** | "The pre-commit hook … runs `scripts/guard-main-commit` **before** `make validate`" — presented as the hook's full behaviour, with an explicit ordering claim | L150–151 | The hook runs **three** things: `guard-main-commit`, then **`guard-employer-leak` (QUM-872)**, then `unset GIT_*`, then `make validate`. The employer-leak guard is invisible in this section. Since this is a **public repo** and that guard is the leak defence, an agent debugging a hook rejection has no idea what rejected it. | `scripts/pre-commit` — `"$here/guard-main-commit"`, `"$here/guard-employer-leak"`, `make validate`. |
| **R4** | The `"agent %q not found"` error is cited to `internal/agent/retire.go:82` | L39 | `internal/agent/retire.go` **contains no "not found" string at all** (87 lines; `grep -n "not found"` → no match). Line 82 is the `state.DeleteAgent` call — the *cause*, not the error site. The string is emitted from `internal/supervisor/real.go` (six sites) and `internal/agentops/{merge,retire,kill}.go`. A precise-looking `file:line` citation that does not contain what it is cited for. | `grep -n "not found" internal/agent/retire.go` → empty; `sed -n 82p` → `if err := state.DeleteAgent(...)`. |
| **R5** | The ref guard "keys strictly on `refs/heads/main`"; the commit guard protects "branch `main`" | L191, L152 | Both guards are **parameterised**: `protected="${1:-main}"`. Behaviour matches the doc *at the default*, so this is not yet a live defect — but the doc states as a hard invariant something the script deliberately made configurable, and a future call site with an argument makes the doc silently false. | `scripts/guard-main-commit:27`, `scripts/guard-main-ref` (`$protected`). |

**Non-rot, verified correct** (recorded so the next auditor need not redo it): `IsTerminal` really
does return true only for `{retired, retiring}` (`internal/state/state.go`); the
`stopped → {complete, faulted}` migration keyed on `LastReportState` is exactly as described
(`state.go:229`); `AgentState.Subagent` exists (`state.go:108`); `agentops.AssertNotOnMain` is wired
into both `Real.RecoverAgents` and `Real.Wake` (`real.go:1074`, `real.go:1252`);
`worktree.verifyWorktreeHEAD` exists and rejects detached HEAD; `make hooks` installs **both** hooks
and does rely on `.git` being a directory; `.sprawl/config.yaml` `worktree.setup` symlinks both
guards idempotently; the delegate/send_message `wake_if_offline` gate and its error string are as
described; commit `4db5057`'s message quotes "9 races (backend 3, rootinit 6)" verbatim, so the
correction paragraph is accurate.

**One irony worth a sentence.** CLAUDE.md line 3 sends every agent to `DESCRIPTION.md`;
`DESCRIPTION.md:9` uses "sub-agents" in exactly the loose sense the Terminology section forbids.
The rule is right; the enforcement is nowhere.

---

## 3. Ranked cut list

Ranked by lines saved per unit of risk. Total: **353 → ~14 lines (−339, −96%).**

### Cut 1 — "Recovering a downstream branch after a squash-merge (QUM-1083)" · L237–353 · **−117 lines**

The largest single block in my surface, and 117 lines of pure recovery procedure that applies to
**one agent, occasionally, after a specific mistake**. Every other agent pays for it on every turn.

*What is lost:* nothing, if it moves. This content is genuinely expensive — the observation that
`git branch --contains` and a clean `git rebase` **both** lie, in opposite directions, is not
derivable from the code and cost a real incident to learn. It must survive.

*Where it goes:* `.claude/skills/git-recovery/SKILL.md`. This is the textbook skill shape: long,
procedural, needed on demand, and the agent that needs it knows it needs it (it is staring at a
conflict). Loading it costs a turn; carrying it costs every turn.

*Carve-out:* the final ~8 lines — "**Check that the question the command answers is the question
you are claiming**" — are not git content. They are a general epistemic rule about asymmetric
relations verified in the convenient direction, and they generalise well beyond
`--is-ancestor`. That paragraph belongs in `.claude/skills/testing-practices` alongside the existing
Assertion Rigor material, not in a git skill and not in CLAUDE.md.

### Cut 2 — "What `make validate` guarantees about data races (QUM-972)" · L76–147 · **−72 lines**

Six paragraphs, of which **one sentence is a live rule** and the rest is the story of QUM-972.

Line by line: the "until QUM-972 it did not" history (L78–84) is Linear's job. The race-count
variance paragraph (L86–94) spends nine lines correcting the arithmetic in a **commit message**
that no agent will ever read. The cost measurement (L106–112) documents a decision already made and
already implemented. The `-race`-needs-cgo paragraph (L114–122) explains why `test-race-gate`
exists, to agents who cannot change that it exists. R1 lives here.

*What survives:* the `atomicDuration` **rule** — "a duration knob production reads from a goroutine
and tests override must be a synchronised seam, never a plain package var" — and its sharpest
corollary, that snapshotting at goroutine entry does not fix it because the snapshot read *is* the
racing access. Roughly three sentences.

*What is lost:* the covered/not-covered breakdown of what a green `validate` proves. That is real,
and it goes to `docs/dev/validate.md` rather than dying.

*Where it goes:* rule → `.claude/skills/go-cli-best-practices`. History + cost + scope →
`docs/dev/validate.md`. **The file census does not go anywhere — delete it** (see §5, M1).

### Cut 3 — "Lifecycle model (QUM-786)" · L13–47 · **−35 lines**

**This is the highest-confidence cut in my surface and the one I would make first**, because unlike
the others it is not a judgement call about cost/benefit — the content is already enforced.

Nine bullets specifying `IsTerminal`, the `StatusStopped` migration, and the delegate/wake gate
matrix. All of it is pinned by tests: `internal/state/state_test.go:561` `TestIsTerminal` is a table
test over the exact status set, and the migration + complete-lifecycle behaviour is covered by
`internal/state/state_test.go`, `internal/agentops/{terminal_error,retire,merge}_test.go`, and
`internal/supervisor/{runtime_status_complete,runtime_durable_fault,real_recover_agents}_test.go`.

Prose duplicating a passing test is strictly negative value: it costs every turn, and on divergence
the test is authoritative while the prose is what gets read. R4 is the divergence, already begun.

*What is lost:* the *rationale* — "permanent termination is a deliberate parent action, never a
side effect of reporting complete." That sentence is design intent, not behaviour, and no test
carries it.

*Where it goes:* rationale → `docs/architecture/agent-lifecycle.md`. The nine behavioural bullets →
**deleted**, superseded by `TestIsTerminal`'s table.

### Cut 4 — Commit guard + reference-transaction backstop · L148–209 · **−62 lines**

Two sections, ~62 lines, describing **hooks that are auto-installed and that print their own
remediation on rejection**. `guard-main-commit` emits a full "To fix:" block naming the exact
`git rev-parse` command to run; `guard-main-ref` emits "Only weave may advance 'main'". An agent
that trips these does not need to have pre-read 62 lines — it needs the error message, which it
gets.

The installation mechanics (L165–175, L196–200) are the clearest "enforced by tooling, therefore
needs no human-readable rule" case in the file: `.sprawl/config.yaml`'s `worktree.setup` does it
automatically on every worktree creation. The prose exists so a human can audit the mechanism,
which is a `docs/` need, not a per-turn need. R3 and R5 both live here.

*What is lost:* one operational instruction with no automated home — **weave should run
`make hooks` once in the main checkout**, because the auto-install only fires on worktree creation.
That is one line and it is the only part of these 62 lines that changes anyone's behaviour.

*Where it goes:* the `make hooks` instruction → one line in CLAUDE.md (weave-facing). Everything
else → `docs/dev/commit-guards.md`.

### Cut 5 — "Safe recovery from a wrong-tree commit on `main`" · L210–236 · **−27 lines**

A three-step recovery for an event the two guards above are specifically designed to prevent. It is
recovery-from-a-prevented-failure: the least-likely-to-fire content in my surface, carried on every
turn.

*What is lost:* nothing, if moved — and one sentence should be kept in CLAUDE.md, because it is a
**prohibition on a plausible reflex**, not a procedure. An agent told "a commit landed on main" will
reach for `git reset --hard`, which can destroy weave's uncommitted work. That is worth one line;
the remaining 26 are not.

*Where it goes:* procedure → the same `.claude/skills/git-recovery/SKILL.md` as Cut 1 (they are one
topic). Prohibition → CLAUDE.md.

### Cut 6 — "Build & Test" command census · L48–75 · **−24 of 28 lines**

Eleven `make` targets with explanatory comments, of which agents routinely need three. The list is
a hand-maintained mirror of the `Makefile` — R2 is exactly the failure mode that guarantees. The
inline commentary ("race-gate runs BEFORE test-race on purpose…") is a defence of a decision
already made and already encoded in the prerequisite order.

*What is lost:* discoverability of the less-common targets. Acceptable: the `Makefile` is one
`grep` away and is authoritative by construction.

*Where it goes:* `make validate` / `make build` / `make fmt` stay (~4 lines). Everything else →
delete, or generate (§5, M2).

---

## 4. Retained items

Total retained in CLAUDE.md from a 353-line surface: **~14 lines.**

| Item | Lines | Destination | Reason |
|---|---|---|---|
| **Terminology: agent / sub-agent / sidechain + the "never say sub-agent for a sidechain" rule** | ~6 | **`CLAUDE.md`** (verbatim, unchanged) | The one section I would defend against any cut. It is our own vocabulary, so it **cannot rot against the code** — the definitions constitute the terms. It is universal: every agent in a three-tier system reads and writes these words. And ambiguity here is expensive in a way that compounds silently, because a mis-scoped "sub-agent" in a handoff produces a wrong worktree model in the reader's head. Six lines, zero maintenance, prevents a recurring real confusion. *Suggested addition:* fix `DESCRIPTION.md:9`, which violates it. |
| `make validate` is the gate; `make build`; `make fmt` | ~3 | **`CLAUDE.md`** | Genuinely universal and genuinely needed every turn. Name the three; point at the `Makefile` for the rest. |
| "Never `git reset --hard` on `main` — it can destroy weave's uncommitted work. Ask weave." | 1 | **`CLAUDE.md`** | A prohibition on a **plausible reflex**, which is the only category of git content that must be pre-loaded — by the time you need it you have already typed it. |
| "weave: run `make hooks` once in the main checkout" | 1 | **`CLAUDE.md`** | The single step in 62 lines of hook prose that automation does not cover. |
| Breadcrumbs: `/git-recovery`, `docs/dev/validate.md`, `docs/dev/commit-guards.md` | ~3 | **`CLAUDE.md`** | Meta-cognitive. The point of the restructure is that the content survives and is findable, not that it is resident. |
| `atomicDuration` **rule** (no file list) + the snapshot-does-not-fix-it corollary | — | **`.claude/skills/go-cli-best-practices`** | A real, non-obvious, non-rotting rule — but it binds only the agent adding a concurrency knob, so it fails the universality test for CLAUDE.md. |
| Squash-merge + wrong-tree recovery procedures | — | **`.claude/skills/git-recovery/SKILL.md`** (new) | Long, procedural, on-demand, self-identifying need. Textbook skill. |
| "Check that the question the command answers is the question you are claiming" | — | **`.claude/skills/testing-practices`** | General epistemic rule, wasted filed under git; sits naturally beside Assertion Rigor. |
| Lifecycle **design intent** ("permanent termination is a deliberate parent action") | — | **`docs/architecture/agent-lifecycle.md`** (new) | Intent, not behaviour — no test carries it, and it is what a future author needs to not re-broaden `IsTerminal`. |
| `validate` covered / not-covered scope; race-detector cost data | — | **`docs/dev/validate.md`** (new) | Core product truth about what our gate does and does not prove. Belongs in docs, not in every context window. |
| Hook mechanics, identity semantics, install paths | — | **`docs/dev/commit-guards.md`** (new) | Auditable design record for a human; automated for agents. |

---

## 5. Replacement mechanisms

Each of these replaces prose with something that **cannot silently rot**. Ordered by value.

**M1 — Delete the `atomicDuration` census; let the code carry it.** *(fixes R1, permanently)*
All four definition sites already carry the rationale in doc comments — `weave_handle.go:82`,
`merge/runtests.go:21`, `rootinit/consolidating_lock.go:47`, `backend/session.go:39` — and three of
them explicitly cite "the repo-wide CLAUDE.md convention." The doc's list is a fourth, remote,
unmaintained copy of information that is already at every point of use. Delete it. If drift between
the four copies is a concern, that is a **test**, not a paragraph: a ~10-line Go test that finds
every `type atomicDuration` declaration and asserts the bodies are byte-identical. That test fails
when a fifth copy diverges; the paragraph did not fail when a fourth appeared.

**M2 — Generate the target list, or delete it.** *(fixes R2, permanently)*
The `Makefile` is authoritative. Either add a `make help` target that prints `##`-annotated targets
(one Makefile rule, ~3 lines of awk), or drop the census and point at the `Makefile`. A
hand-maintained mirror of a machine-readable file is rot with extra steps — R2 is that rot, and it
appeared within one commit of the target landing.

**M3 — Delete the lifecycle bullets; cite the test.** *(replaces Cut 3)*
`TestIsTerminal` is a table test over the exact status set. Replace 35 lines of prose with a
breadcrumb: *"Agent status semantics: see `TestIsTerminal` in `internal/state/state_test.go` and
`docs/architecture/agent-lifecycle.md`."* A test is a claim someone has watched fail — which is the
standard this repo already applies to its own assertions, and should apply to its documentation.

**M4 — Ban `file:line` citations in CLAUDE.md; lint for them.** *(fixes R4, prevents its class)*
R4 is a citation that survived the code moving out from under it, and it is the same failure class
the task brief cites for QUM-1111. A `file:line` in prose is a pointer with no referential
integrity. A ~15-line CI check can parse every `path.go:NNN` in tracked markdown and assert the
file exists and has that many lines — cheap, and it would have caught R4. Better still: cite
**symbols**, which `grep` can resolve after a move.

**M5 — Let the guards document themselves.** *(fixes R3 and R5, permanently)*
Both guards already print full remediation on rejection. Add the one missing line to
`guard-employer-leak`'s failure output (a pointer to `docs/dev/commit-guards.md`) and the 62 lines
of hook prose become redundant for agents entirely. The error message is delivered exactly when it
is needed, to exactly the agent that needs it, and it is emitted by the script — so it cannot
describe a guard that no longer behaves that way.

---

## 6. Reflections

**Surprising.** I expected the rot to be in the oldest content. It is not — the newest content rots
fastest. `atomicDuration` (R1) rotted because the *convention succeeded*: a new author correctly
adopted it, and the doc's census of adopters is what broke. The paragraph was made wrong by someone
obeying it. Likewise R2, where a target joined `validate` and the mirror-list did not follow within
the same commit. Aging is not the mechanism; **enumeration** is. Any sentence in CLAUDE.md that
counts things in the codebase is a scheduled failure.

Also surprising: the single most defensible section in my surface (Terminology) is the shortest,
carries no QUM reference, and is the only one that describes nothing about the code. That is not a
coincidence — it is the selection criterion the rewrite should use.

**Open questions.** (a) I did not measure the token cost of my 353 lines against a real turn, so
"every agent pays this forever" is argued, not quantified — a per-turn token figure would make the
cut self-evidently correct rather than merely reasoned. (b) I could not determine whether the
lifecycle bullets ever *prevented* a defect; if they did, the cut is still right but the rationale
should move to docs rather than be deleted. (c) R5 is latent, not live — someone deliberately
parameterised both guards' protected branch and I did not find the caller that motivated it.

**Next.** I would grep the whole file for the enumeration pattern — sentences containing a count
("three files", "both", "all 11", "N rows") followed by a list of code entities — and check every
one against the tree. R1 and R2 are both instances, found in a 353-line window; the remaining 415
lines are denser in exactly this construction, and the e2e table is the densest of all. My guess is
that this single grep finds most of the file's remaining rot, and that it is also the shape a CI
check could partially automate. Second, I would confirm no other doc or skill depends on the
sections I propose to move, so the relocation does not break a live breadcrumb.
