# Skills layer: dead MCP API fix

Date: 2026-08-06 · Branch: `dmotles/fix-skills-dead-api`

## What I verified

The reported defect is real, and verified against source rather than the report.

`.claude/skills/linear-issues/SKILL.md` documented three MCP messaging tools that
do not exist:

- `send_async({to, subject, body})`
- `send_interrupt({to, subject, body, resume_hint?})`
- `message(...)`, described as a deprecated alias for `send_async`

The live tool is `send_message`, declared in `internal/sprawlmcp/tools.go`
(`to`, `body`, `interrupt`, `wake_if_offline`; `to` and `body` required).
`internal/sprawlmcp/tool_description_sync_test.go` bans the strings `send_async`
and `send_interrupt` from `internal/agent/prompt_mode.go`, which is the only
place they survive. The skill never mentioned `send_message` at all.

One additional defect of the same class, not in the original report: the skill
documented a `detail:` argument on `report_status`. The live schema declares
exactly `state` and `summary`.

`peek` was documented correctly and was left alone apart from the dead
`send_async` cross-reference in its bullet.

`.agents/skills/linear-issues/SKILL.md` needed **no** mirrored edit — it is a
short pointer stub that delegates to the `.claude` copy and contains none of the
dead API. This is worth stating because the `.agents/` mirror otherwise invites
the assumption.

## What I changed

- `.claude/skills/linear-issues/SKILL.md` — removed the `detail:` line from the
  `report_status` block; replaced the four messaging bullets with two
  (`send_message`, `peek`). Section headers and surrounding prose unchanged; no
  restructuring, per the pending consolidation decision.

  The replacement text deliberately states the tool's *purpose* and points at
  the MCP schema for arguments, rather than restating a signature that will
  drift again. That is the failure mode this task exists to fix.

- `cmd/skills_dead_api_test.go` (new) — a regression guard, because prose
  asserting properties of live code with nothing keeping it true is exactly how
  this rotted. It scans every `SKILL.md` under both `.claude/skills` and
  `.agents/skills` and asserts:
  - no reference to a removed MCP tool name;
  - no argument documented in a `report_status(` call block that is absent from
    the live schema (the block is delimited by brace balance, not a fixed
    window, and the valid-argument set is extracted from `tools.go` rather than
    hardcoded — so a future `notes:` fails the same way `detail:` did);
  - the ban list is two-sided: a banned name that is *declared* again in
    `tools.go` fails the test, so the guard cannot silently outlive the removal;
  - any doc with a Messaging Tools section names `send_message`;
  - per-root assertion-count floors, so a moved directory fails instead of
    passing vacuously.

  It lives in `cmd/` rather than `internal/sprawlmcp/` on purpose: the
  CLAUDE.md touched-file matrix contains an `internal/sprawlmcp/*.go` glob (the
  `handoff` row), and under the "when in doubt, include it" rule a file there
  would owe an e2e row. There is no `cmd/*.go` glob, so this file set owes zero
  e2e rows. It also sits beside `cmd/skills_sync_test.go` and reuses its
  `repoRootFromTest` / `listSkillNames` helpers.

### Assertion evidence

Red-first, against the unfixed tree:

```
--- FAIL: TestSkillsMatchLiveMCPSurface
    .claude/skills/linear-issues/SKILL.md:190: report_status has no "detail" argument
    .claude/skills/linear-issues/SKILL.md:202: references removed MCP tool "send_async"
    .claude/skills/linear-issues/SKILL.md:205: references removed MCP tool "send_interrupt"
    .claude/skills/linear-issues/SKILL.md:210: references removed MCP tool "send_async"
    .claude/skills/linear-issues/SKILL.md:211: references removed MCP tool "send_async"
    .claude/skills/linear-issues/SKILL.md:211: references removed MCP tool "message("
--- FAIL: TestSkillsDocumentingMessagingNameSendMessage
    .claude/skills/linear-issues/SKILL.md has a Messaging Tools section that never names `send_message`
```

Assertions with no natural red got mutation controls, each run and observed:

| mutation | observed |
|---|---|
| add `send_message` to the ban list | FAIL `"send_message" is declared as a live tool … remove it from bannedMCPTools` |
| point a skill root at a directory with no skill subdirs | FAIL `scanned 0 skill files under .claude/skills` |
| replace brace-anchoring with a fixed 2-line window | 0 `detail` violations — i.e. it **misses** the live defect, which is why the anchoring is load-bearing |

The `message(` ban and the `send_message(` non-match, the long-block case, and
the block-boundary case are covered by table cases in `TestScanSkillDoc`;
`TestToolNameDeclRE` pins that the ban-list oracle keys on a tool *declaration*
rather than on a mention inside another tool's description prose.

### Review round (second commit)

An independent review of the first commit found four real defects, all in the
guard rather than the prose. All are fixed, each with a control that was
observed failing:

| defect | fix | control observed |
|---|---|---|
| **High.** `liveReportStatusProps` delimited report_status's schema by scanning forward to the next `"required"` *anywhere in the file*. Remove report_status's own `"required"` and the match ran into the following tool, silently admitting `agent` / `no_validate` as valid arguments — the exact silent-wrong-set failure the guard exists to prevent. | Delimit by brace balance over report_status's own literal, then scope the property scan to the inside of its `"properties"` sub-block. | Differential: with `"required"` deleted from tools.go, the new extraction returns `{state, summary}` (pass) while the old one FAILs with `contains "agent"` / `contains "no_validate"`. |
| **High.** The leak control asserted the extracted set lacked `to`/`body` — `send_message`'s properties, declared *earlier* in the file. Since the scan only ever ran forward, that assertion could not fail under any mutation. | Assert against the forward neighbour (`agent`, `no_validate`) as well as the container key `properties`. | Same differential as above — the control now fires. |
| **Medium.** Braces inside string literals broke the skill-side block walk in both directions: `summary: "{x"` ran it to EOF and misattributed later prose; `state: "a}b"` closed it a line early and **missed** a real bad argument. | Strip quoted strings before counting braces; bound the walk and report an unterminated block loudly instead of scanning to EOF. | Removing the strip FAILs both new table cases; removing the bound FAILs the unterminated-block case with a misattributed `detail:` at line 51. |
| **Medium.** `schemaPropertyRE` also matched the container key, so `properties` itself counted as a valid report_status argument. | Take only depth-1 keys inside the properties sub-block. | Covered by the leak control's `properties` entry. |
| **Low.** `message(` matched English prose ("the commit `message(s)` must be clear"). | Require a backtick, since skills write tool names in backticks. | Reverting to the loose form FAILs the new prose table case. |
| **Low.** The table test passed the production ban list rather than a fixture. | Local fixture. | n/a — hygiene. |

The reviewer independently re-derived the e2e-matrix row set for this file set
and confirmed zero rows are owed. It also flagged that the first commit
message justified the test's location partly by which validation gate it would
avoid; the honest reason is that it reuses `cmd/skills_sync_test.go`'s helpers
and is not a test of `internal/sprawlmcp` behaviour, and that is what should
have led.

### Repo-wide check: is any agent anywhere still told to call a dead tool?

The property is not "does `send_async` appear in `.claude/skills/`" — it is
"does any shipped instruction reach an agent naming a tool that does not
exist." Agent prompts are Go string constants in `internal/agent/`, compiled
into the binary, and invisible to a markdown grep.

Probe: `git grep -n -E 'send_async|send_interrupt'` over the whole tracked
tree, and again scoped to `internal/agent/**`.

Result: **zero** hits under `internal/agent/`. Positive control, same probe
and same path scope against a live tool name, finds 42 `send_message`
references across `prompt_mode.go`, `prompt_child_sections.go`,
`wake_prompts.go` and the golden files — so the probe demonstrably reaches the
compiled prompt text.

Tree-wide, the remaining hits are all either regression tests that name the
strings *in order to ban them* (`internal/sprawlmcp/tool_description_sync_test.go`,
`server_sendmessage_test.go`, and this new guard), explanatory comments, or
historical records under `docs/` and `CHANGELOG.md` that correctly narrate the
removal. None instructs an agent to call anything.

## What I found and deliberately left alone

Swept the other six `.claude/skills/` for the same defect class — dead MCP
tools, dead CLI subcommands, dead scripts, dead make targets, dead
`SPRAWL_*` env vars, dead repo paths.

**Clean:** no other skill references a removed MCP tool. All script paths,
make targets, and env vars resolve. No skill invents a CLI subcommand; the
`sprawl init` references in `testing-practices` and `go-cli-best-practices` are
*correct* — one narrates a historical bug, the other documents the removal
(QUM-346) and cites the live regression guard `cmd/init_removed_test.go`.

**Ambiguous, left for a decision (not fixed):** roughly nineteen "real example"
citations in `go-cli-best-practices/SKILL.md` and `testing-practices/SKILL.md`
point at files deleted when those operations became MCP-only —
`cmd/retire.go`, `cmd/spawn.go`, `cmd/messages.go`, `cmd/report.go` and their
tests. The *pattern* each teaches is still valid (the receiving
`internal/agentops/` halves all exist), so only the `cmd/` half of each
citation is dead. Repointing them means choosing replacement examples across
two large skills, which is squarely the restructuring this task was scoped to
avoid. `linear-issues/SKILL.md` has two of the same kind in its
issue-writing guidance (`cmd/spawn.go`, `internal/tmux/session.go` as
throwaway "be specific" examples).

This is a different defect class — stale citation, not broken API — and it
argues for a second, separate guard ("every repo path cited in a skill
exists"), which would go red immediately and so must ship with its fix.

## Job 2 — the three invoked-but-absent skill names

| skill | verdict |
|---|---|
| `code-review` | Harness-supplied, bundled in the Claude Code binary (`~/.local/share/claude/versions/…`). Also present as a marketplace plugin at `~/.claude/plugins/marketplaces/claude-plugins-official/plugins/code-review/`, which is blocklisted. Never existed in this repo. |
| `claude-api` | Harness-supplied global skill, bundled in the Claude Code binary with its reference tree. `~/.claude/skills/` does not exist. Never existed in this repo. |
| `issue-execution-rigor` | **A live repo skill** — but under `.agents/skills/`, not `.claude/skills/`. Not deleted; nothing has ever been deleted under `.claude/skills/`. |

`git log --all --diff-filter=D -- '.claude/skills/**' '.agents/skills/**'` is
empty: there have been zero skill deletions in the history. So none of the
three is a deleted repo skill, and nothing needs recovering.

`issue-execution-rigor` originates in `4bffad8` (2026-04-28, "docs: add Codex
issue execution rigor workflow"), later modified in `ea7edbd` and `cb0ba6b`
(2026-06-10). It is invoked because `AGENTS.md` instructs agents to load
`.agents/skills/issue-execution-rigor/SKILL.md` by path. It is a phase-gated
plan → red → green → validate → reflect → closeout workflow, scoped to
non-trivial Linear issues.

Incidental finding: `.agents/` is not a faithful mirror of `.claude/`. All six
commonly-named skills differ between the trees; `.claude/skills/handoff/` has no
`.agents/` counterpart, and `.agents/skills/{issue-execution-rigor,
commit-message-hygiene}/` have no `.claude/` counterpart. The `.agents/` copies
were last touched 2026-06-10 while `.claude/skills/testing-practices/SKILL.md`
has been edited heavily since. `cmd/skills_sync_test.go` enforces *existence*
of a counterpart, not currency of its content.
