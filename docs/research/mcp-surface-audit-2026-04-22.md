# MCP Surface Audit — Phase 1 Gate for tmux Deprecation

**Author:** ghost (researcher)
**Date:** 2026-04-22
**Linear:** [QUM-313](https://linear.app/qumulo-dmotles/issue/QUM-313)
**Parent:** [QUM-195](https://linear.app/qumulo-dmotles/issue/QUM-195) / M13 (TUI Cutover)
**Related:** `docs/research/tui-parity-audit-2026-04-22.md`

## Purpose

M13 Phase 2 will deprecate and remove a set of `sprawl` CLI commands. Before that is
safe, every command on that list needs an MCP equivalent (exposed by the in-process
`sprawl-ops` MCP server defined in `internal/sprawlmcp/tools.go`) that is **at least
as capable** as the CLI.

This doc answers three questions for every command in the deprecation list:

1. Does an MCP tool exist, and is it a true functional equivalent?
2. Who still calls the CLI form today? (What breaks if we delete it?)
3. What gap work is needed to clear Phase 1?

It is research-only. Gap issues are **not** filed here — weave will triage and file
from the prioritized gap list in §3.

## Phase 2 deprecation list (from M13 milestone description)

**Move to MCP-only (stateful, TUI-aware):**
`sprawl delegate`, `sprawl handoff`, `sprawl kill`, `sprawl messages`, `sprawl poke`,
`sprawl report`, `sprawl retire`, `sprawl spawn`, `sprawl status`, `sprawl tree`.

**Removed entirely:**
`sprawl init` (tmux entry point), `sprawl color` (folds into `sprawl enter --color`).

Commands that **stay** as CLI (`cleanup`, `completion`, `config`, `enter`, `help`,
`logs`, `merge`, `version`) are out of scope for this audit.

## 1. MCP tool inventory (current state of `sprawl-ops`)

Source: `internal/sprawlmcp/tools.go`. Twelve tools are defined:

| Tool | Purpose (per description) |
| --- | --- |
| `sprawl_spawn` | Spawn agent with own worktree/branch. |
| `sprawl_status` | List all agents with state/type/family/branch. |
| `sprawl_delegate` | Queue a task to an existing agent. |
| `sprawl_send_async` | Queue async message; survives crashes; returns `message_id`. |
| `sprawl_send_interrupt` | Parent→descendant mid-turn interrupt. |
| `sprawl_peek` | Inspect an agent's status + last report + N activity events. |
| `sprawl_report_status` | Child reports state to parent (`working`/`blocked`/`complete`/`failure`). |
| `sprawl_message` | **Deprecated** alias for `sprawl_send_async`. |
| `sprawl_merge` | Squash-merge agent branch (agent stays alive). |
| `sprawl_retire` | Retire agent, optionally merging or abandoning. |
| `sprawl_handoff` | Weave-only session handoff. |
| `sprawl_kill` | Emergency stop; preserves state and worktree. |

There is **no** MCP equivalent of `sprawl tree`, `sprawl poke`, `sprawl init`, or
`sprawl color` today.

## 2. Command-by-command parity matrix

Each entry lists CLI surface (flags/subcommands that matter for parity), the MCP
equivalent, and the delta. CLI surface extracted from `cmd/*.go`.

### 2.1 `sprawl spawn` → `sprawl_spawn`

**CLI surface** (`cmd/spawn.go`, `cmd/spawn_subagent.go`):

- `sprawl spawn agent --family=… --type=… --prompt=… --branch=…`
  — creates its own worktree + branch.
- `sprawl spawn subagent --family=… --type=… --prompt=…` (no `--branch`,
  inherits parent) — **currently broken per `docs/todo/punchlist.md`.**
- `--type` accepts `manager|researcher|engineer|tester|code-merger`.

**MCP surface:** `sprawl_spawn{family, type, prompt, branch}`.

- `type` enum is `engineer|researcher|manager` — missing `tester` and `code-merger`.
- No `subagent` mode (no way to spawn a subagent that shares parent's worktree).

**Delta / gap:** **MODERATE.**
1. Extend `type` enum to cover `tester` and `code-merger`, or document deliberate
   narrowing.
2. Decide fate of `spawn subagent`: punchlist flags it as broken. If we keep the
   concept, expose it in MCP (optional `branch`, or a `subagent: true` flag). If
   we drop it, note that in Phase 2 removal notes.

**Consumers of CLI:** `internal/agent/prompt_mode.go` (5 refs) + golden testdata
  — the manager/root prompt still tells agents to run `sprawl spawn agent …`.
  Prompt rewrite is already tracked in [QUM-235](https://linear.app/qumulo-dmotles/issue/QUM-235).

### 2.2 `sprawl status` → `sprawl_status` + `sprawl_peek`

**CLI surface** (`cmd/status.go`):

- No args: agent matrix (state, process, last report).
- `sprawl status <agent>`: agent detail + activity tail.
- Flags: `--json`, `--family`, `--type`, `--parent`, `--status` (filters),
  `--watch/-w` (streams activity.ndjson across all agents), `--tail=N`
  (default 50).

**MCP surface:**
- `sprawl_status{}` — no filters, no per-agent detail, no tail, no watch.
- `sprawl_peek{agent, tail}` — covers per-agent detail + last report + last N
  activity entries. No watch.

**Delta / gap:** **LOW–MODERATE.**
1. Filters (`--family/--type/--parent/--status`) are missing but trivially
   emulable client-side on structured output.
2. `--watch` has no MCP analogue. Likely not needed: the TUI already renders
   this live, and MCP callers can poll `sprawl_status` or subscribe via the TUI
   event stream. Recommend **not** re-implementing watch in MCP.
3. `--json` equivalence: MCP tools return structured results natively; confirm
   that `sprawl_status` output includes all fields the CLI `--json` emits (state,
   family, type, parent, branch, last_report, process liveness). **Verify.**

**Consumers of CLI:** `internal/agent/prompt_mode.go` (2 refs) + golden testdata;
  docs/designs/messaging-overhaul.md, docs/research/status-reliability-findings.md
  (design docs only, no runtime dependency).

### 2.3 `sprawl tree` → **no MCP equivalent** ❌

**CLI surface** (`cmd/tree.go`):

- `sprawl tree` renders agent hierarchy; `--json` for machine output; `--root=<agent>`
  to render a subtree.

**MCP surface:** none. `sprawl_status` returns a flat list; parent relationships
must be reconstructed client-side.

**Delta / gap:** **HIGH (blocker).**
Add `sprawl_tree{root?: string, json?: bool}` (or have it always return JSON —
MCP clients prefer structured data). If we decide weave/TUI is the sole consumer
of "tree", the TUI already renders the tree from state, so the question is
whether any **non-TUI** MCP caller (i.e. a child agent asking "what's the graph?")
needs this. In practice `sprawl_status` plus parent links is sufficient for most
uses; a dedicated tree tool is nice-to-have, not strictly required.

**Recommendation:** make `sprawl_status` return `parent` (if it doesn't already)
and treat tree rendering as a TUI concern. Ship a thin `sprawl_tree` MCP tool
only if agent prompts will reference it.

**Consumers of CLI:** `internal/agent/prompt_mode.go` (1 ref, help text only).
Low external reach.

### 2.4 `sprawl delegate` → `sprawl_delegate`

**CLI surface** (`cmd/delegate.go`): `sprawl delegate <agent> <task>`. Validates
that target is not killed/retired/retiring, enqueues task prompt, prints task ID.

**MCP surface:** `sprawl_delegate{agent_name, task}`. Matches CLI.

**Delta / gap:** **NONE** (parity). Confirm the MCP tool returns the task ID in
its output so callers can reference it.

**Consumers of CLI:** `internal/agent/prompt_mode.go` (5 refs) + golden testdata
(instruction text only). Prompt rewrite covered by QUM-235.

### 2.5 `sprawl messages` → `sprawl_send_async` / `sprawl_send_interrupt` (partial)

**CLI surface** (`cmd/messages.go`) — **the largest gap in the MCP surface**:

- `send <agent> <subject> <body>` — send async message.
- `broadcast <subject> <body>` — send to all active agents.
- `inbox [--all] [--new]` — show received messages (default: unread).
- `read <message-id>` — fetch full message body; prints archive hint.
- `list [all|unread|read|archived|sent]` — list with filter.
- `archive [--all] [--read] [message-id]` — single or bulk archive.
- `unread <message-id>` — mark read message as unread.
- `sent` — show sent messages.

**MCP surface:**
- `sprawl_send_async{to, subject, body, reply_to?, tags?}` — covers `send`, and
  adds threading + tags which the CLI lacks.
- `sprawl_send_interrupt{to, subject, body, resume_hint?}` — mid-turn interrupt
  (no CLI analogue; `sprawl poke` was the closest tmux primitive).
- `sprawl_message{…}` — deprecated alias; ignore.

**Missing entirely from MCP:**
1. **`inbox` / `list` / `read`** — an agent cannot read its own mailbox via MCP.
   Today child agents poll `sprawl messages inbox` (discouraged) or react to
   poke wakes. Phase 2 cannot drop the CLI until MCP exposes a mailbox read
   surface. Recommended shape:
     - `sprawl_inbox{filter?: "unread"|"read"|"archived"|"sent"|"all", limit?}` → returns message summaries.
     - `sprawl_read_message{message_id}` → returns full body; optionally marks read.
2. **`archive` / `unread`** — lifecycle transitions. Recommended:
     - `sprawl_archive_message{message_id? , all?: bool, read_only?: bool}`.
     - `sprawl_mark_unread{message_id}`.
3. **`broadcast`** — fan-out send. Either (a) add `sprawl_broadcast{subject, body, filter?}`
   or (b) treat this as a client-loop over `sprawl_status` + `sprawl_send_async`
   (simpler; documented pattern).

**Delta / gap:** **HIGH (blocker).** This is the bulk of the Phase 1 gap work.

**Consumers of CLI:** Heavy. `cmd/agentloop.go` uses the Maildir/wake protocol
internally (this is fine — it's the library, not the CLI). Agent prompts in
`internal/agent/prompt_mode.go` and `prompt_child_sections.go` reference
`sprawl messages …` extensively. Prompts need to be rewritten to use MCP tools
in Phase 2.

### 2.6 `sprawl report` → `sprawl_report_status`

**CLI surface** (`cmd/report.go`):

- `sprawl report status <msg>` → state=`working`.
- `sprawl report done <msg>` → state=`complete`.
- `sprawl report problem <msg>` → state=`failure`.

**MCP surface:** `sprawl_report_status{state, summary, detail?}` where `state ∈
{working, blocked, complete, failure}`. State enum is a **superset** of CLI
(adds `blocked`), and adds `detail` for longer-form markdown.

**Delta / gap:** **NONE** (MCP is strictly more capable).

**Consumers of CLI:** Core. Extensive references in agent prompts and in
`scripts/test-notify-e2e.sh` (QUM-310 regression guard). Prompt rewrite +
e2e test adaptation already contemplated under M13 Phase 2 items.

### 2.7 `sprawl retire` → `sprawl_retire` (+ `sprawl_merge`)

**CLI surface** (`cmd/retire.go`):

- `sprawl retire <agent>` — safe retire (refuses if unmerged commits).
- `--merge` — merge into parent branch before retiring.
- `--abandon` — discard work, delete branch.
- `--cascade` — retire agent and all descendants bottom-up.
- `--force` — skip safety checks, orphan children.
- `--yes` — acknowledge safety warnings (for dirty/live).
- `--no-validate` — skip post-merge validation.

**MCP surface:** `sprawl_retire{agent_name, merge?, abandon?}`.

**Delta / gap:** **MODERATE.**
1. No `--cascade`. Cascade is a real workflow (manager retiring subtree). Either
   expose it (`cascade?: bool`) or document "call `sprawl_retire` per child
   bottom-up". **Recommend exposing** — cascade ordering is nontrivial and the
   CLI got it right.
2. No `--force`. Escape hatch for broken state. Likely needed as `force?: bool`
   for parity.
3. No `--yes`. If MCP treats safety warnings as failures that require explicit
   override, we need a `confirm?: bool` or similar.
4. No `--no-validate` on the retire path. `sprawl_merge` already exposes
   `no_validate`; the merge branch of `sprawl_retire` should too.

**Consumers of CLI:** Agent prompts (`internal/agent/prompt_mode.go` — 11 refs),
design docs (`docs/designs/agent-teardown.md` — 8 refs). Prompt rewrite under
QUM-235.

### 2.8 `sprawl kill` → `sprawl_kill`

**CLI surface** (`cmd/kill.go`): `sprawl kill <agent> [--force]`. `--force`
switches from graceful TERM to immediate KILL.

**MCP surface:** `sprawl_kill{agent_name}`. No `--force`.

**Delta / gap:** **LOW.** Add `force?: bool`. Parity otherwise.

**Consumers of CLI:** `internal/agent/prompt_mode.go` (3 refs); design docs only.

### 2.9 `sprawl handoff` → `sprawl_handoff`

**CLI surface** (`cmd/handoff.go`): reads session summary from stdin, writes
handoff signal, saves active-agent metadata. Root-agent-only.

**MCP surface:** `sprawl_handoff{summary}`. Matches functionality.

**Delta / gap:** **NONE.**

**Consumers of CLI:** `.claude/skills/handoff/SKILL.md`, CLAUDE.md, README.md.
The `/handoff` skill currently pipes stdin into `sprawl handoff`; needs to be
rewritten to call the MCP tool. Not a gating code change but a docs/skill task.

### 2.10 `sprawl poke` → **intentionally no MCP equivalent**

**CLI surface** (`cmd/poke.go`): writes a `.poke` marker file under
`.sprawl/agents/<name>.poke` that the agent loop watches to wake.

**Usage audit (per §3 below):**
- **Zero** references to the string `"sprawl poke"` in skills, docs, prompts,
  scripts, or agent-facing instructions.
- The `.poke` marker **file** is used internally (by the messages notifier, by
  `agentloop.go`'s wake path, by retire teardown, and in tests), but nothing in
  the codebase invokes the `sprawl poke` CLI command to produce it.

**Delta / gap:** **NONE NEEDED — drop it.**

The interrupt-style semantic that `poke` gestures at is already covered by
`sprawl_send_interrupt` at a much higher level of abstraction. The `.poke` file
is an internal wake primitive and will remain as library code; the user-facing
`sprawl poke` command is dead and can be deleted in Phase 2 without an MCP
replacement.

### 2.11 `sprawl init` — **removed, no MCP equivalent needed**

`sprawl enter` replaces it as the TUI entry point. Not an MCP surface — it's a
process boot, and MCP only exists **inside** an already-running process.

**Consumers of CLI:** Heavy in scripts (`test-init-e2e.sh`), docs, CLAUDE.md,
README.md. All of that needs to be rewritten to point at `sprawl enter` as part
of Phase 2 (milestone explicitly lists "remove `test-init-e2e.sh`").

### 2.12 `sprawl color` — **removed, folds into `sprawl enter --color`**

Cosmetic. No MCP tool needed. The milestone description already specifies the
replacement path (`sprawl enter --color <name>` flag).

**Delta / gap:** Requires **`sprawl enter`** to accept a color flag/subcommand
that replicates `sprawl color set|list|rotate`. Not MCP work — a CLI
adjustment on `enter`. Flagged here for completeness; primary tracking should
be a Phase 2 task against `cmd/enter.go`.

## 3. Prioritized gap list

Ordered by how much they block Phase 2 cutover.

### P0 — blockers (must land before Phase 2 can ship)

1. **Mailbox read surface for MCP.** Add (at minimum) `sprawl_inbox` and
   `sprawl_read_message` MCP tools. Without these, child agents lose the ability
   to read messages when the CLI goes away. See §2.5.
2. **Mailbox lifecycle in MCP.** Add `sprawl_archive_message` and
   `sprawl_mark_unread`. Without archive, inboxes grow without bound. See §2.5.
3. **`sprawl_retire` cascade + force.** Add `cascade`, `force`, `confirm`,
   `no_validate` fields. Parent managers need the full CLI workflow; without
   cascade they can't cleanly tear down subtrees. See §2.7.
4. **Rewrite `internal/agent/prompt_mode.go` + golden testdata** to reference
   MCP tools instead of the outgoing CLIs. This is tracked as
   [QUM-235](https://linear.app/qumulo-dmotles/issue/QUM-235) already — surface
   here for visibility. Prompts currently advertise `sprawl spawn`, `sprawl
   delegate`, `sprawl messages`, `sprawl report`, `sprawl retire`, `sprawl
   kill`, `sprawl status`, `sprawl tree` to agents.

### P1 — parity polish (should land before Phase 2, but workaround-able)

5. **Broadcast.** Either add `sprawl_broadcast` or document "iterate
   `sprawl_status` + `sprawl_send_async`" as the recommended pattern. Prefer
   the dedicated tool for discoverability.
6. **`sprawl_kill` `force` flag.** Trivial addition.
7. **`sprawl_spawn` type enum.** Decide whether `tester` and `code-merger`
   are supported; reconcile with MCP enum.
8. **`sprawl spawn subagent`.** Resolve the broken-subagent punchlist item
   before deciding whether to surface subagent mode in MCP. If we keep it,
   expose it.
9. **`sprawl_status` output completeness.** Audit returned fields against
   `--json` output; ensure parent, last_report, liveness, branch are present so
   clients can emulate filters.

### P2 — nice-to-have / optional

10. **`sprawl_tree` MCP tool.** Low usage. Defer unless a specific MCP caller
    needs it. TUI renders its own tree from state.
11. **`/handoff` skill rewrite** to call `sprawl_handoff` MCP tool (today pipes
    stdin to CLI).
12. **`sprawl enter --color`** to replace `sprawl color`. CLI work, not MCP.

### No-op / drops

13. **Drop `sprawl poke`** entirely in Phase 2. No MCP replacement. See §2.10
    and §4.
14. **Drop `sprawl init`, `sprawl color`**. Already in milestone's "removed
    entirely" list.
15. **Delete `sprawl_message`** deprecated alias after Phase 2 lands.

## 4. `sprawl poke` audit (scope item)

The issue asks for a specific poke audit. Exhaustive search results:

- **Literal `"sprawl poke"` matches in codebase:** 0 (excluding the command's
  own implementation/tests).
- **Agent prompts:** 0 references.
- **Skills (`.claude/skills/`):** 0 references.
- **Docs (`docs/`):** 0 references to the CLI command. `messaging-overhaul.md`
  and `realtime-message-injection.md` discuss the `.poke` **file** as a wake
  primitive, not the CLI command.
- **Scripts:** 0 references (`scripts/test-notify-e2e.sh` references the
  `.poke` file path, not the CLI).
- **README.md / DESCRIPTION.md / CLAUDE.md:** 0 references.

**Recommendation:** `sprawl poke` can be dropped in Phase 2 **without an MCP
equivalent**. The `.poke` marker file semantics continue to live in
`cmd/agentloop.go` / `internal/messages` as internal wake plumbing. Mid-turn
interrupts for user-level callers are handled by `sprawl_send_interrupt`.

## 5. Migration-surface review (prompts, skills, docs, CLAUDE.md)

What needs to be rewritten when the CLIs go away. Prioritized by the amount of
content that references each command.

| Surface | Commands it references | Notes |
| --- | --- | --- |
| `internal/agent/prompt_mode.go` + `prompt_child_sections.go` + golden testdata | spawn, delegate, messages, report, retire, kill, status, tree | **Largest rewrite.** Golden test files under `internal/agent/testdata/*_tmux.golden` will be obsolete; `*_tui.golden` variants should become the only variant. Covered by [QUM-235](https://linear.app/qumulo-dmotles/issue/QUM-235). |
| `CLAUDE.md` (root) | init, report, messages | Validation section (§ "Validating Changes") hard-codes `make test-init-e2e` and `make test-notify-e2e`; both scripts go away in Phase 2. Rewrite to point at TUI-mode e2e equivalents. |
| `DESCRIPTION.md` | init, messages, report, spawn, kill | User-facing overview. Needs a pass. |
| `README.md` | init, handoff | Entry-point docs. |
| `.claude/skills/handoff/SKILL.md` | handoff | Replace CLI invocation with `sprawl_handoff` MCP tool. |
| `.claude/skills/testing-practices/SKILL.md` | spawn, retire, kill | Examples. |
| `.claude/skills/linear-issues/SKILL.md` | messages, report, spawn | Already nudges toward MCP preference. |
| `.claude/skills/go-cli-best-practices/SKILL.md` | init | Regression narrative (QUM-261). |
| `docs/designs/*.md` | all | Design docs — historical record, minimal rewrite needed beyond callouts. |
| `docs/testing/smoke-test-memory.md`, `docs/testing/m4-manager-smoke-test.md` | init, report, spawn, handoff | Test procedures. Rewrite for TUI mode. |
| `scripts/test-init-e2e.sh` | init | **Delete** per milestone. |
| `scripts/test-notify-e2e.sh` | report, messages | Rewrite or delete per milestone. |

## 6. Reflections

**Surprises.**
- The MCP surface is more complete than I expected going in. The only
  P0-blocking gap is the messaging read/archive/lifecycle surface; everything
  else is either parity polish or prompt/doc rewrites. The issue's speculation
  ("Gaps are likely in: `sprawl_tree`, `sprawl_handoff`, `sprawl_messages`…")
  turned out to be right about messages but wrong about handoff (parity
  already) and largely-correct-but-low-priority about tree.
- `sprawl poke` really is completely unreferenced. I triple-checked. It's a
  clean drop.
- `sprawl_report_status` is actually **more** capable than `sprawl report` —
  it has a `blocked` state that the CLI lacks. One of the few places MCP
  already leads.

**Open questions.**
- Does `sprawl_status` currently return `parent`, `last_report`, and liveness
  fields in its output, or only state/type/family/branch? I did not verify the
  return shape, only the input schema. Worth a quick follow-up read of
  `internal/sprawlmcp/server.go`.
- Does `sprawl_delegate` return the task ID? Same "verify server.go" item.
- Is `sprawl spawn subagent` worth saving at all, given the punchlist notes it
  as broken? If we're dropping it, we can simplify the MCP surface by not
  adding subagent support.
- Broadcast: dedicated tool vs. documented iteration pattern. Not strictly
  required for correctness — tie-breaker is "what reads better in agent
  prompts."

**What I'd investigate next with more time.**
- Read `internal/sprawlmcp/server.go` to confirm the actual return shapes of
  each tool (especially `sprawl_status` and `sprawl_delegate`), and file a
  concrete follow-up if fields are missing.
- Enumerate the exact set of prompt changes needed in `prompt_mode.go` (line
  numbers, current text, proposed replacement) as a patch plan for QUM-235.
- Spec the new mailbox MCP tools (`sprawl_inbox`, `sprawl_read_message`,
  `sprawl_archive_message`, `sprawl_mark_unread`) in enough detail that an
  implementer could just pick it up — schemas, semantics around
  marking-read-on-read, pagination shape.

## Appendix A — References

- Phase 2 deprecation list: M13 milestone description (see issue QUM-313).
- MCP tool source of truth: `internal/sprawlmcp/tools.go`.
- Parent parity audit: `docs/research/tui-parity-audit-2026-04-22.md`.
- Related Linear issues: [QUM-262](https://linear.app/qumulo-dmotles/issue/QUM-262)
  (initial MCP tool implementation), [QUM-292](https://linear.app/qumulo-dmotles/issue/QUM-292)
  (async messaging MCP tools), [QUM-235](https://linear.app/qumulo-dmotles/issue/QUM-235)
  (prompt rewrite), [QUM-305](https://linear.app/qumulo-dmotles/issue/QUM-305)
  / [QUM-309](https://linear.app/qumulo-dmotles/issue/QUM-309)
  / [QUM-310](https://linear.app/qumulo-dmotles/issue/QUM-310) (legacy messaging).
