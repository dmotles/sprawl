<!--
DRAFT — proposed replacement for CLAUDE.md. NOT the live file. Do not copy this
header. Everything between BEGIN BODY and END BODY is the proposed file; the
appendix below END BODY is review material and does not ship. See
`description-md-audit.md` and this document's appendix for the coverage mapping
and the destinations that must exist before this can land.
-->

<!-- BEGIN BODY -->
# CLAUDE.md

Sprawl orchestrates Claude Code agents to work a goal in parallel. You talk to
one root agent; it spawns managers, which decompose work and spawn engineers,
researchers, and QA agents. Work flows down as tasks and back up as merges, and
the system converges because decomposition bottoms out — at some point the only
thing left is to make the change. Agents are **dormant and reusable, not
disposable**: an agent that finished a task can be woken with its full context,
so prefer reusing the agent that already has the background over spawning a
fresh one.

**This repo is Sprawl.** The `.sprawl/` directory at the root holds live agent
state and worktrees — if you are an agent working here, you are running inside
the system you are changing. Do not touch anything under `.sprawl/` unless that
is your task. Read `docs/README.md` before acting on anything you found in
`docs/`.

## Terminology

- **agent** — a sprawl-spawned process with its own worktree and its own Claude
  session.
- **sub-agent** — a sprawl-spawned process that shares its parent's worktree.
- **sidechain** — a Claude in-process `Agent`-tool spawn (Explore, Plan, Oracle,
  test-critic).

These three are distinct. "Sub-agent" must never refer to a Claude Agent-tool
spawn — use "sidechain".

## Hard rules

These are the rules you can break *before* you would think to look anything up.
Everything else in this file is a pointer.

- **Never `git add -A`, `git add .`, or `git commit -a`.** Stage explicit paths,
  or `git add -u` for a large change to tracked files. Agent worktrees share a
  filesystem with other agents' scratch output and with tooling that writes
  files nobody named in advance, so `-A` makes your commit a function of someone
  else's hygiene. Review `git diff --cached` before every commit. If an
  untracked file surprises you, do not stage it — find out what wrote it.
- **Never commit to `main`** unless you are the root agent. Hooks enforce this
  and will tell you what to do; you do not need to have pre-read them.
- **Never `git reset --hard` on `main`.** It can destroy the root agent's
  uncommitted work. If a commit landed on `main` by mistake, stop and ask the
  root agent — recovery is re-homing the commit and moving the ref, never a
  hard reset.
- **Never `rm -rf` a broad `/tmp` glob.** Other agents' live sandboxes are
  there. Delete only a path you created, after asserting it matches the prefix
  you expect. **Never touch `/tmp/coder-script-data`** — it is host tooling
  state, and breaking it makes end-to-end rows skip silently rather than fail.
- **Do not run `make install`** unless you are the root agent or were asked to.
  Use `make build` and exercise the local `./sprawl` binary against a temporary
  root instead.
- **This repo is public.** Never commit anything naming or describing an
  employer's internal systems, hosts, customers, or topology. A commit-time
  guard catches listed terms; it cannot catch prose that *describes* an internal
  system without naming one, so that judgement is yours. Forensic artifacts
  captured from real systems default to `.sprawl/agents/<name>/findings/`, which
  is not tracked. When in doubt, ask.

## Build and validate

`make validate` is the gate, and the pre-commit hook runs it for you. `make
build` builds the binary; `make fmt` fixes formatting. Anything else, read the
`Makefile` — it is authoritative and one `grep` away.

A green `validate` means no failure was *observed* on the paths the unit suite
drives. It does not cover the end-to-end harnesses, anything behind a build tag,
or a concurrent path no test exercises.

## Mandatory end-to-end tests

`make validate` does not exercise the live supervisor or TUI. Those are covered
by end-to-end rows, and touching the code a row guards obligates you to run it.

- **Derive the obligation from every path in your diff, not from a list someone
  handed you.** Take the union. A row you were told to skip and a row you
  verified does not apply are indistinguishable to your reviewer.
- **Over-running costs a CI slot; under-running ships the defect and comes back
  green either way.** When unsure, include the row.
- **A skipped row validates nothing.** Skips exit non-zero on purpose. Say
  "skipped", never cite a green-looking run that skipped.
- Rows are discovered from the tree: `bash scripts/e2e-matrix.sh --list`. Run
  several at once by naming them; `make test-e2e-matrix-<row>` takes exactly
  one.
- Rows need a real, authenticated `claude` on `PATH`. A row that fails with
  `Not logged in` is an auth problem, not a product regression, and hiding
  `claude` to force a skip buys nothing.

## Tests and assertions

Read `/testing-practices` before writing or reviewing tests. Two rules from it
are worth stating here because you can violate them while writing the test,
before you would think to open a skill:

- **Every new assertion must demonstrate that it CAN fail** — by a negative
  control, a mutation, or a red-first run — and you must record which one you
  used and what it printed. An assertion nobody has watched fail is a claim, not
  a check.
- **No fallback branch may silently succeed.** A check that cannot fail is worse
  than no check, because it is indistinguishable from a working one. A harness
  that aggregates its own results needs a floor that fails a zero-assertion run;
  a skip on an unmet precondition must exit 77, never 0.

Two habits that would have prevented most of the false findings this repo has
had to retract:

- **Name the property before you name the probe.** Write the sentence you intend
  to publish, then choose the search. "Is this behaviour tested" and "does a
  file with this name exist" are different questions, and only one of them is
  usually what you meant.
- **Before trusting a negative result, prove the probe can return a positive
  one.** A search that finds nothing and a search that *cannot* find anything
  look identical.

## Writing code

- Read `/go-cli-best-practices` before writing Go, and `/cli-ux-best-practices`
  before changing any command's behaviour or output. Every command must tell the
  calling agent what to do next.
- Keep behaviour tested. The unit of the rule is the **behaviour**, not a file
  named after the file under test — tests routinely live in a differently-named
  file in the same package, and reading the rule as `foo.go → foo_test.go`
  overstates gaps by a wide margin.
- A duration knob that production reads from a goroutine and tests override must
  be a synchronised seam, never a plain package var. Snapshotting it at
  goroutine entry does not fix it — the snapshot read *is* the racing access.

## Documentation you write

- **Say what is gone, not what is there.** Claims of absence stay true; claims of
  presence expire silently.
- **Do not enumerate code entities.** A count followed by a list of files,
  symbols, or call sites is made wrong by the next person who *complies* with the
  rule it states. This is how nearly all of this file's predecessor rotted.
- Cite a section or a quoted phrase, never `file.go:NNN`. Line citations survive
  the code moving out from under them and point confidently at the wrong thing.
- If it explains rather than constrains, it belongs in `docs/` or a skill, not
  here. Read `docs/README.md` for what goes where.

## Working an issue

Work is tracked in Linear. **Invoke the `/linear-issues` skill before creating or
updating an issue** — do not rely on remembered conventions; it defines required
fields that are easy to miss. Set the issue In Progress when you pick it up,
comment as you go so the thread is a living log, and mark it Done only after
validation is complete. `CLAUDE.local.md` holds the workspace's team, project,
and branch prefix.

When you spawn an agent to work an issue, point it at the issue and keep the
prompt short. The issue is the source of truth; do not paste its contents.

## When `claude` fails with `Not logged in`

Claude Code strips the auth token from Bash subshells, so an inner `claude`
cannot log in. Launch sprawl with `scripts/run-claude` as `$SPRAWL_CLAUDE`; it
re-hydrates the token from a root `.env` that worktree setup copies for you.
This is the fix — a skip flag is not.

## Skills

Loaded on demand, and better than anything this file could restate:
`/testing-practices`, `/go-cli-best-practices`, `/cli-ux-best-practices`,
`/e2e-testing-sandboxing`, `/tui-testing`, `/linear-issues`, `/handoff`
(root agent only).

## Project configuration

`.sprawl/config.yaml` holds project settings, including the validation command
and the worktree setup hook that installs the commit guards and copies local
config into every new worktree. `sprawl config` reads and writes it.
<!-- END BODY -->

---

# Appendix — review material (does not ship)

## A. Coverage: every section of today's `CLAUDE.md`

Sections are named by heading, not by line range, because the live file is
contended and line numbers would be wrong by the time you read this.

| today's section | disposition | destination |
|---|---|---|
| Preamble (`Read DESCRIPTION.md`) | **compressed** | Orientation paragraph, drawn from `DESCRIPTION.md` per `description-md-audit.md`; the mandatory read of `DESCRIPTION.md` is dropped |
| Terminology | **kept verbatim** | — (unrottable: it defines our vocabulary) |
| Lifecycle model | **cut** | Behaviour is pinned by tests; design intent → `docs/architecture/` |
| Build & Test (target census) | **compressed to 3 targets** | Rest → the `Makefile`, which is authoritative |
| What `make validate` guarantees about data races | **compressed to 2 sentences + 1 rule** | History, cost, and covered/not-covered scope → `docs/guides/` |
| Commit guard | **compressed to a prohibition** | Mechanics and identity semantics → `docs/guides/` |
| Reference-transaction backstop | **cut** | → `docs/guides/`; the guards print their own remediation |
| Safe recovery from a wrong-tree commit | **compressed to a prohibition** | Procedure → a `git-recovery` skill |
| Recovering after a squash-merge | **moved** | → a `git-recovery` skill. **Must not be lost** — that both natural checks lie in opposite directions is not derivable from the code |
| ″ (its closing epistemic rule) | **moved** | → `/testing-practices`, beside Assertion Rigor; it is not git content |
| Never `git add -A` | **compressed to the prohibition** | Incident narrative → `docs/guides/` |
| Install warning | **kept, compressed** | — |
| Running `claude` from agent bash subshells | **compressed to the diagnostic** | One-time host setup → `docs/guides/` |
| tmux safety | **moved** | → `/e2e-testing-sandboxing`, which does not currently carry the socket guidance |
| `/tmp` hygiene | **compressed to 2 prohibitions** | Harness-author patterns → `/e2e-testing-sandboxing` |
| Text selection in `sprawl enter` | **cut** | The in-product help modal owns keybindings; make it authoritative and test it |
| Incident snapshot hotkey | **cut** | Every bundle writes its own legend at capture time |
| Runtime pprof toggle | **cut** | Rationale already sits on the code; operator facts → `docs/guides/` |
| Project Configuration | **compressed to a pointer** | Key reference → generated from the config struct tags |
| Repo Layout | **cut** | Inventory of packages; regenerate or drop |
| Code Patterns — DI | **cut** | `/go-cli-best-practices` has it, fuller |
| Code Patterns — assertion rigor | **compressed to the rule** | `/testing-practices` § Assertion Rigor |
| Code Patterns — non-asserting fallback | **compressed to the rule** | `/testing-practices` § The non-asserting fallback |
| Code Patterns — skill pointers | **kept** | Skills block |
| Public vs Private Repo Hygiene | **compressed to the judgement residue** | The mechanical case is enforced at commit time |
| Linear Issue Tracking | **compressed** | Lifecycle detail → `/linear-issues` |
| Spawning Agents | **kept, compressed** | — |
| Session Handoff | **cut** | `/handoff` is discoverable in the skill list; every non-root agent pays for it today and can never use it |
| Sandbox Testing | **cut to the skill pointer** | `/e2e-testing-sandboxing` |
| Linting & Formatting | **cut** | The hook refuses the commit; no rule needed |
| Validating Changes items 1–4 | **compressed** | Build/validate + skills blocks |
| Validating Changes item 5 — e2e preamble + table | **compressed to the rule + a live discovery command** | Mechanism and per-row tacit notes → an `e2e-matrix` skill; gates → machine-readable per-row manifests |
| Makefile / race-gate row | **cut** | It is a `make` target, not an e2e row |
| — *(new)* | **added** | "Documentation you write" — the enumeration ban and the absence/presence rule, which is the audit's own output and had no home |

Nothing in the table is unaccounted for. Three entries are the ones a reviewer
should push on, because each loses something real if its destination is not
built: the squash-merge recovery, the tmux socket guidance, and the e2e row
manifests.

## B. Breadcrumbs, checked against `flux`'s tree

Every pointer the body makes, and whether its target exists **now** on
`dmotles/docs-restructure-d4`.

| breadcrumb | target | exists? |
|---|---|---|
| `docs/README.md` (×2) | the new index | **yes** |
| `Makefile` | — | **yes** |
| `scripts/e2e-matrix.sh --list` | row discovery | **yes** — `--list` is a supported whole-invocation mode |
| `make test-e2e-matrix-<row>` | — | **yes** |
| `scripts/run-claude` / `$SPRAWL_CLAUDE` | — | **yes** |
| `.sprawl/config.yaml`, `sprawl config` | — | **yes** |
| `CLAUDE.local.md` | workspace config | **yes** (untracked; copied per worktree) |
| `/testing-practices`, `/go-cli-best-practices`, `/cli-ux-best-practices`, `/e2e-testing-sandboxing`, `/tui-testing`, `/linear-issues`, `/handoff` | skills | **yes** — all seven |
| `.sprawl/agents/<name>/findings/` | — | **yes** (untracked by design) |

**The body contains no pointer to a file that does not exist.** That is
deliberate and it is the constraint that shaped several wordings: where the
sub-audits proposed sending content to `docs/dev/…`, that directory does not
exist and is not one of the seven `flux` settled on. Those destinations are
listed in §C as preconditions instead of being written into the draft as
danglers, since shipping a dangling breadcrumb is the defect this restructure
exists to fix.

## C. Preconditions — must exist before this draft lands

| what | why | who |
|---|---|---|
| A `git-recovery` skill | Holds the squash-merge and wrong-tree recovery procedures. Until it exists, cutting them from `CLAUDE.md` destroys them. **Blocking.** | D1/D2 owner |
| The `/testing-practices` addition | The "question the command answers" rule needs its new home before its old one is cut. **Blocking.** | D1 owner |
| `/e2e-testing-sandboxing` gains the tmux socket guidance | It is the one place `CLAUDE.md` holds unique content on this topic. **Blocking.** | D2 owner |
| An `e2e-matrix` skill + per-row gate manifests | The draft's e2e section states the rule and points at live row discovery, so it is *safe* without these — but the per-row tacit notes ("this row is the only live coverage of X") have no home until they exist. **Not blocking; lossy.** | D2 |
| `docs/guides/` pages for validate scope, commit guards, host setup, debugging, git hygiene | Long-form material the body drops. Route to `guides/` (procedural, run from this repo), **not** the `docs/dev/` the sub-audits proposed — that directory does not exist in the restructured tree. **Not blocking.** | D4 follow-up |
| `docs/architecture/` page for lifecycle design intent and the orchestration model | Absorbs what `DESCRIPTION.md` should keep. **Not blocking.** | D1 follow-up |

## D. Corrections applied from `DECISION.md` §§3.1–3.5

Recorded because the detail documents are not self-correcting and a later reader
will otherwise re-import the withdrawn versions.

- **`drain.go` has tests** (§3.1). The sub-audit's "443 lines, no test file" and
  the "36 of 216 files untested" figure are withdrawn. The draft therefore states
  the companion-test rule as a property — *keep behaviour tested* — and says
  explicitly that the per-file reading is wrong, rather than repeating "every
  file has a `_test.go`", which is the wording defect that produced the false
  finding.
- **Agent-name validation exists** (§3.2). Nothing in the draft repeats the
  withdrawn security finding.
- **"The `CLAUDE.md` copy is always the stale one" does not survive** (§3.4 #4).
  The set was staleness-selected and there is a verified inverse case where the
  product is wrong and `CLAUDE.md` is right. The draft cuts duplicated content on
  the grounds that it is *duplicated*, not on a claim about which copy rots.
- **The e2e row count is 29, not 30** (§3.4). The draft states no row count at
  all, and points at live discovery instead.
- **`315/399` is an existence measure, not a truth measure** (§3.4 #3). Not cited.
- **Do not summarise — cut or point** (§4c). Applied throughout: where a skill
  owns the material, the draft keeps the imperative and drops the compression. A
  compressed copy is not a safer copy; it is one whose rot cannot be detected.
- **Prose works for rules gating a deliberate action, and fails for rules
  constraining incidental output** (§4d). This is the sorting rule behind "Hard
  rules": every entry there gates a command someone deliberately types. The
  terminology rule is the known exception — it constrains incidental output and
  is kept anyway, because it defines the vocabulary, with the caveat that it
  needs a check rather than more prose.

## E. On the line count

Measured, not estimated:

| | draft body | today's `CLAUDE.md` | change |
|---|---:|---:|---:|
| lines (`wc -l`) | **170** | 768 | −78% |
| of which blank | 32 | | |
| of which headings | 12 | | |
| content lines | **126** | | |
| words | 1,435 | 10,577 | −86% |
| characters | 8,761 | 79,984 | −89% |

**170 against a ≤205 target.** The combined in-tree always-loaded figure is
then 170 + 0 (no `DESCRIPTION.md` prelude) + the workspace config, which is
injected twice — comfortably inside the ≤250 recommendation without spending
the slack.

One thing happened while writing this section that belongs in it. I wrote "the
body measures 211 lines" **before running `wc`**, from a sense of how long the
draft felt, and then measured and found 170. Nobody would have caught it: 211
is plausible, it is in the right range, and it sits in a document arguing for
line-count discipline. It is the fourth revision of a number in this audit's
history and it has the same shape as the first three — *a figure asserted in
prose, in front of the thing it describes, with nothing that fails when it is
wrong.* Left visible rather than quietly corrected, because the correction is
more instructive than the number.

Note also what `wc -l` does and does not measure. It counts blank lines,
headings, and table pipes; it does not count tokens, and reflowing this content
to a different column width moves it without changing anything that matters. If
a number is going to be the acceptance criterion, state it over something that
does not move under reformatting — characters, or better, the resolver in §F.

What I will defend is the *content*: every line in the body is either a rule an
agent can break before it would think to look anything up, a pointer, or the
orientation needed to read the pointers. The section a reviewer should attack
first is "Tests and assertions" — it is the one place I kept a compressed copy of
skill content rather than a bare pointer, on the argument that you violate those
two rules *while writing the test*, before you would open a skill. If that
argument fails, it is worth about eight lines.

## F. The budget figure should be a script, not a sentence

The always-loaded number has been revised three times in one hour — 768, then
963, then 1040 — each revision found by checking what the previous one assumed,
each moving the same direction. That is not three mistakes; it is one artifact
type failing three times. **A budget figure written in prose is exactly the
construction this project concluded cannot be maintained**: a count, over a set
of code entities, maintained by hand, with nothing that fails when the set
changes. It is the enumeration rule applied to the audit's own headline number.

The form that can be maintained is a script that resolves the set and prints the
number, wired into `validate` with a ceiling. To be correct it must resolve:

- **`@`-imports transitively**, which are mechanical, and distinguish them from
  prose "read this file" instructions, which are not — the second class is what
  hid `DESCRIPTION.md`, and it is the harder half, because it means reading an
  imperative rather than a syntax.
- **Every copy that is actually injected**, not every distinct file. The
  workspace-local config is delivered twice, from two paths, with identical
  content and doubled cost.
- **Out-of-tree files** — the user-global instruction file and the memory file.
  These are not ours to edit, but they are part of what an agent carries, so a
  budget that omits them is measuring the wrong set again. Report them
  separately from the in-tree figure rather than silently excluding them.
- **The harness manifest as ground truth**, not the repo's beliefs about it. The
  repo currently configures an exclusion for the user-global file that does not
  appear to take effect; a resolver that reads the config would inherit that
  error, and one that reads what was actually loaded would catch it.

**Is it cheap enough? For the in-tree half, yes** — resolving `@`-imports and
counting injected copies is a small script, and it converts the fourth revision
of this number from a discovery into a test failure. **For the prose-instruction
half, no, and it should not be attempted.** Detecting "this sentence tells an
agent to go read that file" is natural-language classification, and this repo has
already built and rejected a deterministic prose parser for a structurally
identical problem, which acquired four blind spots of the same class it was built
to detect. The right split: make the script exact on the mechanical part, and
make the prose part *unnecessary* by banning the construction — an always-loaded
file may `@`-import, or it may point at on-demand material, but it may not
instruct a mandatory read of a file that is not imported. Then the resolver is
complete by construction, and the rule that makes it complete is one line in the
file it governs.
