# `DESCRIPTION.md` and the real always-loaded budget

*Read-only audit, 2026-08-06, signal. Source measured at `fb6be47`; `docs/` shape
read from `flux`'s `dmotles/docs-restructure-d4`. Every count below is a
measurement at a named commit, not an invariant — re-derive before citing.*

## Verdict

**`DESCRIPTION.md` should not exist as a mandatory prelude.** It is the
least-reliable document in the repo and it sits at the highest privilege level:
of **33 discrete checkable claims, 13 verify true, 12 are false, and 8 are
materially incomplete** — roughly **40% survive intact**. Two of the false ones
describe *safety properties the code does not have* (the name-pool ceiling,
QUM-1132) and *capability restrictions that are not enforced* (managers and
researchers are told they cannot edit code; no tool denial exists — only root's
is real). One describes a runtime that was torn out (`tmux`), and one describes a
signalling mechanism that does not exist (Claude Code hooks).

**And the budget was wrong a third time.** The task briefed 963 (CLAUDE.md 768 +
DESCRIPTION.md 195). The measured surface an agent unavoidably carries at turn
zero is **1040 lines**, because `CLAUDE.local.md` is injected **twice** (root +
worktree copy, 21 lines each), the user-global `~/.claude/CLAUDE.md` (28 lines)
loads **despite** `.claude/settings.json` listing it in `claudeMdExcludes`, and
`MEMORY.md` (7) rides along. That is the same error one layer out: 963 was itself
a countable proxy — *"the files we decided to count"* — for the property we
wanted, *"what does an agent unavoidably read before doing anything."*

**This is the audit's closing example, and it is better than the one we already
had.** The line count was a countable proxy for the property we actually cared
about, and nobody checked, because it was the number everyone had agreed to.
Note the shape: the number was not *guessed*. It was correctly measured — of the
wrong set. Arithmetic review cannot catch that, which is exactly why `flux`'s new
`docs/README.md` states the corollary *"name the property, not a countable proxy
for it"* as a placement rule. Two independent workstreams arrived at that
sentence on the same day, from opposite ends.

## The combined budget

**In-tree always-loaded ≤ 250 lines.**

| surface | measured `fb6be47` | target | note |
|---|---:|---:|---|
| `CLAUDE.md` | 768 | **≤ 205** | absorbs ~12 lines of orientation from `DESCRIPTION.md` |
| `DESCRIPTION.md` | 195 | **0** | not a prelude; see below |
| `CLAUDE.local.md` | 21 | **≤ 25** | *injected twice* — real cost is 2× |
| **in-tree total** | **984** | **≤ 250** | |
| out-of-tree, not ours to budget | 35 | — | `~/.claude/CLAUDE.md` 28 + `MEMORY.md` 7 |
| **actually injected** | **1040** | **~285** | |

**The criterion that produces 250, bottom-up:**

> **Preload only what an agent can violate before it would think to look it up.**

That is a real distinction, not a vibe. `git add -A`, committing to `main`,
`rm -rf /tmp/*`, the terminology rule, the mandatory-e2e obligation, the
assertion-can-fail rule — an agent breaks these *in the act of working*, and a
breadcrumb arrives too late. Everything explanatory — what a manager is for, why
the system converges, what the MCP tools are called — is consulted *when needed*,
and the harness already surfaces it on demand (tool schemas are self-describing;
skills load by name). Applying that filter to CLAUDE.md's own sections is D1's
job and I have not done it; 205 is the budget it should be held to, and it is
generous on purpose.

**Why 250 rather than 150.** 150 is achievable only by cutting hazard text that
is load-bearing — the QUM-989 `git add -A` rationale, the squash-rebase recovery,
the row-derivation rule. Each of those exists because it was learned expensively,
and each describes a failure an agent commits *before* it would consult anything.
A tidy 150 that drops one of them trades a documentation metric for a real
regression. 250 is where the "violate-before-lookup" filter actually lands, and
it is a 73% reduction from 1040 with every cut line given a destination below.

**Non-negotiable in the split:** `DESCRIPTION.md` goes to **0**, and the saving is
not reinvested. It is the file with the worst measured accuracy in the always-loaded
set; keeping any of it at that privilege level is the defect, independent of size.

## What `DESCRIPTION.md` is for — settled

**Its orientation role is real but tiny, and its explanatory role is already
served, twice, by more authoritative surfaces.**

1. **The runtime prompt already tells every agent what it needs.** The prompts
   compiled into `internal/agent/` deliver each agent its identity, type, family,
   parent, worktree, branch, reporting rules, and scope limits *at spawn*. I can
   verify this from the inside: my own system prompt for this task contained my
   name, my parent, my branch, my read-only constraint, and my reporting
   contract — none of it from `DESCRIPTION.md`. Sections **Agent Types**,
   **Agent Lifecycle**, **The Rules**, and **Agent Families** (80 of 195 lines)
   are therefore a *second, unversioned, drifting copy* of something Go source
   already emits authoritatively. When they disagree, the Go wins and the
   document is a bug — and they do disagree, on four counts.
2. **The MCP surface is self-describing.** Sections **Interface**, **Agent MCP
   Tools**, **Messaging**, **Reporting** (39 lines) restate tool schemas the
   agent receives from the server. This is precisely `flux`'s entry rule: a
   document that enumerates an interface's members rots the day it lands. It has
   — it lists 14 tools where 18 exist.
3. **`docs/README.md` already routes the remainder.** Its `guides/` row says a
   task done only sometimes should be a skill, "loaded on demand instead of read
   every turn." Orientation read once per human, never per turn, is the same
   case.

So: **a ~12-line "what this system is" block at the top of `CLAUDE.md`** (the
Gibson name, root/manager/IC shape, seed → decompose → converge, agents are
dormant-and-reusable not disposable), and a breadcrumb to one rewritten
`docs/architecture/orchestration-model.md` for anyone who wants the *why*. That
is genuinely worth saying — a reader who does not know the system converges will
misread half of CLAUDE.md — and it is worth 12 lines, not 195.

## Per-item disposition

Six lenses: **D**eclarative · **P**rocedural · **C**ontextual · **T**acit ·
**M**eta-cognitive · **A**gent-behavior.

| § (lines) | lens | verdict | destination / what is lost |
|---|---|---|---|
| Title + tagline (1–3) | D | **keep ~2** | → `CLAUDE.md` header. Cheapest orientation in the file. |
| Why (5–11) | D | **cut to 1 clause** | → `docs/architecture/orchestration-model.md`. Lost: the Conway framing. Acceptable — it changes no action an agent takes. |
| What / Seeding / The Root (13–23) | D | **keep ~4** | → `CLAUDE.md`. "Root can't edit code, spawns and messages" is load-bearing and *enforced*; keep it, drop the prose. |
| Agent Identity (25–29) | D+T | **rewrite, then `docs/`** | Contains QUM-1132: a safety ceiling the code lacks. Nothing an agent must know at turn zero — its name arrives in its prompt. |
| Agent Lifecycle (31–44) | D | **keep 1 line, rest cut** | → `CLAUDE.md`: "agents are dormant and reusable, resumed on the same session id" — this *does* change behaviour (reuse vs respawn). The wake/work/sleep mechanics are superseded by CLAUDE.md's own QUM-786 lifecycle contract, which is authoritative and disagrees. |
| Agent Types (46–87, **42 lines**) | D+A | **cut** | Superseded by the compiled prompts + CLAUDE.md:538. Lost: the capability table — which is *wrong* on 3 of 8 cells. Cutting a wrong table loses nothing. |
| Agent Families (89–97) | D | **cut to the enum** | `family ∈ {product, engineering, qa}` is already in the spawn schema the caller reads. |
| The Rules (99–113) | A+C | **cut** | → `docs/architecture/orchestration-model.md`. Every rule is delivered per-role at spawn, where it is actionable and type-correct; here it is generic and stale (rule 5 is false — engineers host subagents). |
| The Forcing Function (115–119) | D | **cut** | → same. Pure rationale. |
| Interface / MCP Tools / Messaging / Reporting (121–171, **51 lines**) | D+P | **cut entirely** | The tool schemas are the authority and are delivered at runtime. Lost: nothing. This is the enumeration `docs/README.md`'s entry rule forbids re-filing anywhere live. |
| Signaling (173–175) | D | **cut** | **False.** Delivery is `WakeForDelivery` → `runDrain` → stdin injection; sprawl's only hooks are *git* hooks. If the mechanism needs a home it is `docs/architecture/`, written from `internal/supervisor/drain.go`. |
| Name (177–179) | D | **cut to the tagline** | Duplicates line 3. |
| Platform (181–185) | D | **cut** | Contains the `tmux` falsehood. The worktree-per-agent fact survives — it is already in CLAUDE.md. |
| Future / Enhancements (187–195) | C | **cut** | All four items are stale in the same direction: two are built, two are half-wired. A roadmap in an always-loaded file is the highest-decay content there is. Linear is the tracker. |

Net: **~12 lines to `CLAUDE.md`**, one rewritten `docs/architecture/` page for the
*why*, and 51 lines of interface enumeration deleted outright rather than
relocated.

## Evidence

### Claim verification — 33 claims, 13 true

Every probe below ran with a stated positive control; a search that finds nothing
and a search that *cannot* find anything are indistinguishable.

**False (12).** Cited by source line:

* **L29 name-pool ceiling** — `"no more agents can be spawned"` appears nowhere in
  the tree; `AllocateName` is an unbounded `for i := 1; ; i++` with per-type
  fallback prefixes (`runner`/`decker`/`fixer`/`inspector`) and a warn-only log
  past `2*len(pool)`. *Control:* the same grep over `names.go` returns its real
  error, `unknown agent type %q`. Filed as **QUM-1132** — cited here as evidence
  about the file's reliability, not re-filed.
* **L27 "~50 names"** — 60 at `fb6be47` (19/14/13/14), and the pool is
  **partitioned by type**, which the file does not say.
* **L50–55 table, "Manager / Researcher — can edit code: No"** — no enforcement
  exists. `rootinit.DisallowedTools` denies `Edit`/`Write`/`NotebookEdit` to
  **root only**; `ChildDisallowedTools` is harness-tied no-op tools and contains
  no editor. *Control:* the same file shows root's denial, so the probe finds
  denials when they exist. A capability matrix that is prompt convention wearing
  a table's clothes is the most dangerous line in the file.
* **L46/L50 "four types"** — spawnable types are `engineer, researcher, manager,
  qa`; **`qa` is absent from the file entirely** while `tester`/`code-merger`
  appear in `agentops.ValidTypes`. Root is not a spawnable type.
* **L80/L86/L111 "leaf nodes — cannot spawn"** — false since QUM-709: engineers,
  researchers and qa may host `subagent: true` children (depth cap 3).
* **L23 "the only one the user interacts with directly"** — `ask_user_question`'s
  server-side gate admits `Type=="manager"` as well as root.
* **L175 Signaling via Claude Code hooks** — `internal/hooks` installs *git*
  guards (QUM-808/837/872). *Control:* the grep that returns zero Claude-Code hook
  wiring returns `WakeForDelivery`/`runDrain` for the real path.
* **L184 "child agents run in tmux sessions"** — `internal/claude/launch.go:5`:
  stream-json subprocess mode is *"the only launch mode left after the tmux
  teardown."*
* **L44 `--json` output mode** — the flag is `--output-format`; `--resume` is
  correct.
* **L193 "Automatic .env copying — not yet implemented"** — implemented in
  `.sprawl/config.yaml` `worktree.setup`.
* **L194 "Web UI — not yet implemented"** — `web/` (vite/TS) and `cmd/hubd` exist,
  with a `hub-e2e` matrix row.
* **L9 "spawn sub-agents"** — violates CLAUDE.md's own Terminology section, which
  forbids the loose sense. Notable less as an error than as a measurement: the
  file was not re-read when that rule landed.

**Materially incomplete (8).** The 14-tool list omits `toast`, `pause`, `wake`
(18 exist); `spawn`'s signature omits `subagent`/`model`/`system_prompt` and
marks `branch` required when it is not; `retire` omits `cascade` and the
refuse-by-default semantics; the 4-command CLI list omits ~15 top-level commands;
"own worktree" omits the subagent exception; the wake/work/sleep loop predates
the QUM-786 status contract; `send_message` omits `wake_if_offline`; "Future"
items are half-wired rather than absent.

**True (13).** `SPRAWL_AGENT_IDENTITY` and the no-`parent`-argument consequence;
root is an interactive session and **is** mechanically barred from editing;
`--resume` reuse of a stable session id; families `{product, engineering, qa}`
matching the spawn enum; managers squash-merge via `sprawl merge`; `report_status`
being ephemeral and non-retrievable; `status` fields; the messaging tool set and
`interrupt` semantics; the four named CLI commands all existing; worktree per
agent; issue tracking being external; the Gibson etymology.

### The budget measurement, and the probe that settles "always-loaded"

The property is *"what does an agent unavoidably read before doing anything,"* not
*"which files did we decide to count."* The instrument is the session's own
loaded-context manifest.

* **Positive control:** the worktree `CLAUDE.md`, the root `CLAUDE.local.md`, the
  worktree `CLAUDE.local.md`, `~/.claude/CLAUDE.md`, and `MEMORY.md` all appear.
  The instrument reports what is injected.
* **Negative:** `DESCRIPTION.md` does **not** appear, and neither does
  `AGENTS.md`.

That distinction matters more than the arithmetic. **`DESCRIPTION.md` is not
mechanically imported — it is an imperative a compliant agent obeys.** CLAUDE.md
line 3 says "Read `DESCRIPTION.md`" in backticks; the `@`-import form appears
nowhere in CLAUDE.md (`AGENTS.md:1`'s `See @CLAUDE.md` is the positive control for
that syntax). So the 195 lines land in context on the first turn *for agents that
comply*, and not at all for those that do not — and nothing distinguishes the two
states downstream. That is **worse** than a hard import, not better: a
nondeterministic instruction surface whose highest-privilege content is also its
least accurate. It is an argument for cutting, not for discounting the cost.

**Still-uncounted surfaces found while looking:**

* **`CLAUDE.local.md` is injected twice** — the root copy and the `worktree.setup`
  copy are both loaded (both appear in the manifest). 21 lines each; the content
  is identical, the cost is not.
* **`claudeMdExcludes` is not taking effect.** `.claude/settings.json` lists
  `~/.claude/CLAUDE.md`, and that file's contents are in this session's context
  regardless. Either the key is wrong or its scope is narrower than intended —
  worth one probe by whoever owns D1, because it is a control the repo believes
  it has.
* **`docs/todo/punchlist.md` is *not* transitively always-loaded** — the
  `CLAUDE.local.md:21` reference is prose ("See …"), not an import, and the
  manifest does not contain it. It is also **already dangling**: `flux` deleted
  the file in D4 (recorded in `d4-execution.md` §6). A breadcrumb to a deleted
  file, in an always-loaded file — cheap to fix, and it belongs to whoever owns
  `CLAUDE.local.md`, since it is untracked.
* **Control re-affirmed:** no compiled prompt in `internal/agent/` loads any file
  by path — they reference skills by name and the Skill tool only. That negative
  bounds the surface, and it held on re-probe.

## Reflections

* **Surprising:** the most dangerous line in `DESCRIPTION.md` is not a wrong fact
  but a *right-looking table*. Three of the eight capability cells assert
  enforcement that does not exist, and a table format is precisely what makes a
  reader stop checking.
* **Surprising:** the budget was wrong in the same direction twice in one day, by
  the same mechanism, at two different levels of the stack. The second one was
  found only because the first made the question askable.
* **Open:** whether `claudeMdExcludes` is misconfigured or misunderstood. I
  observed the file loaded despite the exclude; I did not determine why, and it
  is out of my scope to change.
* **Next, with more time:** apply the violate-before-lookup filter to CLAUDE.md's
  768 lines section by section and produce the actual 205-line draft, rather than
  asserting the number is reachable. I believe it is; I have not proved it, and
  the difference between those two is what this audit is about.
