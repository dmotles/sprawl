# `.agents/skills/` — the gap D5's surface missed

*Read-only audit, 2026-08-06. All measurements taken at commit `fb6be47` and are
stated as measurements, not as durable facts — re-derive before citing.*

## Verdict

**Neither skill is dangerous, but one is wrong and both are dead for the harness
that does the work.**

1. **`commit-message-hygiene` is factually wrong about this repo's convention.**
   It instructs "keep issue ids out of the subject by default" and "put Linear
   references in a footer, e.g. `Refs: QUM-350`". Measured over the last 100
   commits at `fb6be47`: **85 subjects contain `QUM-NNN`, 59 begin with it**, and
   `^Refs:` appears **5 times in the last 500 commit bodies**. The skill's own
   step 2 ("check recent `git log --oneline` if you need to match local style")
   would tell an agent the opposite of what its Subject Line Rules say. Its
   ≤72-char guidance is also counterfactual here (69/100 recent subjects exceed
   it), and it never mentions the `Co-Authored-By` trailer that **173 of the last
   200** commits carry.

2. **`issue-execution-rigor` is accurate as far as it goes, but it is a stale
   *subset* of `testing-practices` on the axis that matters, and it is stricter
   on the axis that doesn't.** It mandates red-first exclusively ("Do not skip the
   red phase by writing tests after the fix"). `CLAUDE.md` and
   `.claude/skills/testing-practices/SKILL.md` § Assertion Rigor state the binding
   rule as *"every new assertion must demonstrate that it CAN fail, by one of: a
   negative control, a **mutation**, or a red-first run"* — and then explicitly
   name the hole IER walks into: *"writing the test first does not make it
   independent if you write it from the mechanism you are about to build."* An
   agent that loads IER alone satisfies its checklist, never runs a mutation,
   never adds an assertion-count floor, and believes it has met the repo standard.
   That is the live hazard. IER also predates the QUM-997 non-asserting-fallback
   rule, the QUM-952 skip-accounting rule ("a skipped row does not discharge a
   mandatory-gate obligation"), and the QUM-1081 union rule for deriving e2e rows
   — none of which it reflects. Its § Validation Floor invitation to "justify
   calling it harness drift" is the one line that actively cuts against
   `CLAUDE.md`'s stance that a control proves a failure *pre-existing*, never
   *acceptable*.

3. **Both are unreachable from Claude Code as configured here** (see Reachability
   below). They are Codex-only, referenced only from `AGENTS.md`, and the last
   200 commits carry **zero** Codex attribution against 173 Claude ones.

## Evidence

### What they are

| file | lines | lens served |
|---|---|---|
| `.agents/skills/issue-execution-rigor/SKILL.md` | 145 | **procedural** (phase gates) + **agent-behavior** (sidechain review at each gate). No declarative or contextual content. |
| `.agents/skills/commit-message-hygiene/SKILL.md` | 118 | **procedural** + **tacit** (subject/body taste, worked bad/better pairs). |

Both were added 2026-04-28 (`4bffad8`, `7c0d19e`). IER has been touched exactly
once since — `cb0ba6b`, a 10-line terminology pass. `testing-practices` has 19
commits and stands at 1933 lines. That divergence is the whole story.

### Factual claims, checked against the tree

Verified present: all six companion skills IER names (`linear-issues`,
`testing-practices`, `go-cli-best-practices`, `cli-ux-best-practices`,
`tui-testing`, `e2e-testing-sandboxing`) exist under `.claude/skills/`;
`make validate` exists; the In-Progress/pickup-comment/Done lifecycle matches
`CLAUDE.md`; "sidechain" is used in the repo's sense.
`scripts/test-tui-e2e.sh`, cited in a `commit-message-hygiene` example, exists.
No deleted-API class of error was found in either file — the failure mode here is
**omission and drift**, not a stale API.

The one claim that fails is `commit-message-hygiene`'s model of this repo's
subject-line convention, quantified above.

### Reachability — property, then probes

The property is **"can an agent in this repo actually load this file's
content?"**, not "does this string appear somewhere". Three probes, each with its
positive control stated:

* **Name discovery (Skill tool).** The available-skills listing in this session
  contains exactly the seven `.claude/skills/` entries — that is the positive
  control. Neither `.agents`-only skill appears. → **not discoverable by name.**
* **Auto-loaded context.** This session's loaded-instruction set is
  `CLAUDE.md` (global + project + worktree), `CLAUDE.local.md` ×2, and the
  user-level `MEMORY.md`; the worktree `CLAUDE.md` being present is the positive
  control. `AGENTS.md` is **absent**. → **`AGENTS.md`, and therefore both
  path-loads it carries, are not delivered to Claude Code in this
  configuration.** Stated as one observation of one harness version, not a
  property of Claude Code.
* **Textual reference (`git grep`).** Positive control: `testing-practices`
  returns 16 files. `issue-execution-rigor` and `commit-message-hygiene` each
  return only their own file, `AGENTS.md`, and `DECISION.md`. → **`AGENTS.md`
  lines 2–3 are the sole references, and both paths still resolve.**

So the answer to "is `commit-message-hygiene` referenced by anything" is **yes** —
symmetrically with IER, and by the same mechanism. The earlier framing that only
IER was path-loaded was half the picture.

### Why the two-tree arrangement lets this happen

`cmd/skills_sync_test.go` enforces `.claude/skills/*` → `.agents/skills/*`
**one-directionally**: it iterates the `.claude` set and requires a counterpart
with matching frontmatter (`handoff` whitelisted). An `.agents`-only skill is
constrained by **nothing** — no counterpart requirement, no frontmatter check, no
rot check. Combined with path-loading, that is two independent blind spots
stacked: invisible to the sync test, invisible to `.claude/skills/` enumeration,
invisible to `Skill`-tool telemetry.

## Path-loaded instruction surfaces (enumerated, `fb6be47`)

Tracked files that direct an agent to open another specific file, where that file
is neither a `.claude/skills/` skill nor `CLAUDE.md` itself:

| from | to | note |
|---|---|---|
| `AGENTS.md:2` | `.agents/skills/issue-execution-rigor/SKILL.md` | resolves; Codex-only |
| `AGENTS.md:3` | `.agents/skills/commit-message-hygiene/SKILL.md` | resolves; Codex-only |
| `AGENTS.md:1` | `CLAUDE.md` (`See @CLAUDE.md`) | resolves |
| `CLAUDE.md:3` | `DESCRIPTION.md` | resolves, 195 lines — **the largest path-loaded surface, and it is loaded by every agent** |
| `CLAUDE.md` (several) | `CLAUDE.local.md` | resolves; untracked, copied into worktrees by `.sprawl/config.yaml` `worktree.setup` |
| `CLAUDE.local.md:21` | `docs/todo/punchlist.md` | resolves |
| each of the six `.agents/skills/*` stubs | `../../../.claude/skills/<name>/SKILL.md` | resolve |

Adjacent surfaces that are *not* path-loads but are instruction content outside
both skill trees, and belong in D5's inventory:

* `.claude/agents/oracle.md` (13 lines) and `.claude/agents/test-critic.md` (15
  lines) — sidechain definitions, injected by name. `test-critic.md` gives
  test-quality guidance ("testing pyramid (unit > integration > e2e)") that
  neither cites nor agrees in emphasis with `testing-practices`, and is in
  tension with IER's "unit tests are not enough" and the repo's mandatory-e2e
  table.
* The prompt constants compiled into `internal/agent/` (`prompt.go`,
  `prompt_mode.go`, `prompt_child_sections.go`, `restart_prompt.go`,
  `wake_prompts.go`). Swept: they reference skills **by name / by the Skill
  tool** and mention `CLAUDE.md` generically. **No compiled prompt loads a file
  by path** — one clean result worth recording, since it bounds the surface.
* `CONTRIBUTING.md` and `README.md` — human-facing, no agent path-loads.
* The user-level `~/.claude/.../memory/MEMORY.md` chain — real instruction
  surface, out of tree, out of D5's reach.

## Recommendation

* **`commit-message-hygiene` → rewrite, then promote to `.claude/skills/`, or
  cut.** Its taste content is fine; its repo-specific claims are wrong and would
  actively push an agent off convention. If nobody will own re-deriving the
  convention from `git log`, cut it — a wrong style guide is worse than none.
* **`issue-execution-rigor` → fold into `.claude/skills/testing-practices` (or a
  thin `.claude/` workflow skill that *delegates* the testing sections to it).**
  The phase-gate/sidechain-review spine is genuinely additive and lives nowhere
  else; its red/validation sections are a stale restatement of doctrine that has
  moved twice since. Folding is what stops a second copy drifting again.
* **The two-tree arrangement should not survive consolidation.** Six of eight
  `.agents/` entries are 13-line pointers that exist so a second harness can
  discover the same content — that part works and costs little. The failure is
  the residue: the tree also accumulated two originals that no test constrains
  and no Claude agent can read. Recommend **`.agents/skills/` holds pointers
  only**, enforced by extending `cmd/skills_sync_test.go` with the reverse
  direction (every `.agents` entry must have a `.claude` counterpart, or be
  whitelisted with a reason). That single assertion closes the exact hole this
  audit walked into, and its absence is why the hole existed.

## Reflections

* **Surprising:** the gap was symmetric. `commit-message-hygiene` is path-loaded
  from `AGENTS.md` on the adjacent line — it was never the "nothing points at it"
  case it was framed as. The framing came from having looked at one line of a
  three-line file.
* **Surprising:** the sync test is one-directional, and that is the mechanical
  root cause. The measurement blind spot (path-loading vs. name discovery) is the
  *proximate* cause; the missing reverse assertion is why the residue could exist
  at all.
* **Open:** whether Claude Code in some other configuration or version *does*
  ingest `AGENTS.md`. My probe is one session at one commit. If it ever does,
  `commit-message-hygiene` stops being inert and starts pushing agents off the
  repo's actual subject convention — which reorders my keep/cut call from "cut is
  acceptable" to "must fix or delete now".
* **Next, with more time:** diff IER's phase gates against the compiled
  `internal/agent/` engineer prompt to see how much of the spine is already
  delivered to every agent at spawn, which would decide fold-vs-cut on evidence
  rather than judgement.
