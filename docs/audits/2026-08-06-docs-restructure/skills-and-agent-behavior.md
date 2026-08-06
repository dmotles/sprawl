# Audit: the skills layer and the agent-behavior gap

**Date:** 2026-08-06 · **Auditor:** `prism` (researcher, under `forge`) · **Scope:** Lens 6
(agent behavior) and the `.claude/` on-demand layer. Read-only; no code or docs outside this
file were modified.

**On this document's own length.** It is ~10k words, which is a survey's length in an audit that
recommends cutting. That is deliberate and it is the one place the thesis does not apply: this
lives in `docs/audits/`, is read once by the people doing the restructure, and its value is the
per-claim `grep` evidence — a compressed version would be exactly the unfalsifiable prose §6.2
argues against. §0 and the three-line verdict in §5 are the read-if-nothing-else path.

**Method note.** Every claim below about the tree was verified with `grep`/`git` at this
commit and the command is cited. Nothing here rests on reading a doc that describes the code
— that is the failure mode the restructure exists to fix. Where I could not verify something,
it is labelled *unverified*.

---

## 0. The one-paragraph version

The behavior layer is real, large, and almost entirely invisible. Roughly **64 KB of
compiled-in system-prompt text** across five roles (`internal/agent/testdata/*.golden`) is
the *de facto* behavioral standard, and no agent, reviewer, or user can read it without
reading Go source. It has rotted in ways that are worse than CLAUDE.md's rot, because nobody
greps a string constant. Meanwhile **two roles that run shell commands — researcher and QA —
receive no safety guidance at all**, and the single most operationally valuable body of
behavioral knowledge in the project sits in weave's private, non-checked-in memory where no
child agent can see it.

The thesis ("default to cutting, prefer a rule to a description") holds for CLAUDE.md and
holds for the skills layer. It does **not** straightforwardly hold for the behavior layer:
the problem there is not excess, it is **absence and invisibility**. Section 5 argues that
distinction, and Section 8 pushes back on "make it a test" with a measured counter-example
already in the tree.

Ranked by what it buys us:

| # | Finding | Buys us |
|---|---|---|
| 1 | Researcher + QA prompts contain zero safety/hygiene guidance (§1.1) | Removes a live class of destructive-action and false-RED incidents |
| 2 | The findings hand-off path is broken on both ends by a relative path (§1.2) | Fixes silent loss of researcher output — the thing researchers exist to produce |
| 3 | `DESCRIPTION.md` — the first file CLAUDE.md tells you to read — contradicts a *mandatory* step of the compiled engineer prompt (§1.3) | Removes the highest-traffic rotted doc in the repo |
| 4 | ~9 durable, generally-applicable rules live only in weave's private memory (§2) | Every child agent stops re-learning them by failing |
| 5 | The entire skills layer is reachable only via CLAUDE.md prose; no child prompt names the Skill tool (§3, §6.2) | Protects the skills layer from being orphaned by the restructure |
| 6 | `testing-practices` is a second dumping ground, but a *legitimate* one (§6) | Tells us what to split rather than what to delete |
| 7 | Doc accuracy correlates with proximity to executed code (§8) | A placement rule that survives the next two years |
| 8 | Class (b): four checked-in rules are violated in-tree, and one comparable rule is not — the difference is what to keep (§2.5) | Four ~15-line greps that convert the largest rot class into a red build |

### The skills layer in three lines

1. It is worth keeping and is currently unsafe to follow — three of seven skills are built on an
   agent-facing CLI that no longer exists, and one tells agents to call three MCP tools the test
   suite asserts are absent.
2. The rot is maintenance attention, not size: five of seven untouched since May 2026, one since
   April; the one skill under active maintenance is the one whose principles are pristine.
3. `testing-practices` has CLAUDE.md's disease (85.5% is one section) but is paid on demand, so
   the fix is split-and-prune, not shrink-for-cost.

### What I am asking you to decide

Six decisions. Everything after §0 is evidence for these; nothing else needs reading to answer.

| # | Decision | My recommendation | Where |
|---|---|---|---|
| D1 | Do researcher and QA prompts get a safety section? | **Yes** — and put the shared rule in CLAUDE.md once rather than a fourth Go copy | §1.1, §4 |
| D2 | Fix the researcher findings path (absolute) + give it an `# Environment` block? | **Yes** — two one-line binary fixes; it is silently losing output today | §1.2 |
| D3 | What happens to `DESCRIPTION.md`? | Keep the philosophy in `docs/`; **delete** the type table and "ICs cannot spawn agents" rather than reword them; generate the capability matrix | §1.3 |
| D4 | Promote the ~9 durable rules out of weave's private memory? | **Yes** — the false-RED list to CLAUDE.md, the orchestration runbook to a new skill | §2.1, §4 |
| D5 | Split `testing-practices` three ways, and move the CLAUDE.md/skill duplication boundary? | **Yes** — note the duplication is *test-pinned*, so this needs an explicit call, not a quiet edit | §6.1, §6.2 |
| D6 | Spend the enforcement budget on four grep checks (dead doc-path citations first)? | **Yes** — this is the cheapest large win in the audit | §2.5.4 |

**Reading guide.** §0 is the decision. §5 and §7 are the skills verdict and bucketing. Everything
else — Parts 1 and 2 in full, §8, §9 — is appendix: per-claim evidence, kept because §6.2 argues
that unfalsifiable summary is how the current rot survived audit.

---

# Part 1 — the agent-behavior gap (the important half)

## 1.0 Where behavior actually lives: five layers, ranked by how rotted they are

I found five distinct homes for agent-behavior guidance. Sorting them by measured accuracy
produced the most useful single insight in this audit.

| Layer | Size | Who can read it | Rot found |
|---|---|---|---|
| MCP tool schemas (`internal/sprawlmcp/tools.go`, `internal/rootinit/tools.go`) | 447 + n lines | Every agent, at every turn, automatically | **None found.** `qa` is in the `spawn` enum, the subagent depth/capability gate is documented, model tiers are current |
| `CLAUDE.md` | 768 lines / 10,577 words / 80 KB | Every agent, every turn | Known: QUM-1111. Behavior sections themselves are largely accurate |
| Compiled system prompts (`internal/agent/prompt*.go`) | 64 KB across 5 goldens | Nobody — requires reading Go | 2 confirmed dead claims (§1.4), 1 major omission class (§1.1) |
| `DESCRIPTION.md` | 195 lines / 2,473 words | Every agent (CLAUDE.md line 3 orders it) | **4 confirmed contradictions**, one against mandatory behavior (§1.3) |
| weave private memory (`~/.claude/projects/.../memory/`) | 150 lines, 8 files | **weave only.** Not checked in, not in any worktree | 2 confirmed false claims about current code (§2.3) |

**The pattern: accuracy tracks (a) proximity to executed code and (b) number of readers.**
The tool schemas are exercised on every call and are clean. weave's memory is read by one
agent, greppable by none, and is the only layer where I found claims that are simply false
about the current tree. `DESCRIPTION.md` is read by everyone but *exercised* by nothing, and
it is the worst-rotted checked-in file I looked at.

This is a placement rule, not an observation. See §8.

## 1.1 FINDING 1 — researcher and QA agents receive no safety guidance whatsoever

Measured directly against the pinned golden prompts:

```
$ for s in 'Executing actions' '# System' 'Tone and style' 'Destructive-var' 'not the only agent'; do ... done
                      researcher  qa  engineer  manager
Executing actions          0       0      1        1
# System                   0       0      1        1
Tone and style             0       0      1        1
Destructive-var            0       0      1        1
not the only agent         0       0      1        1
```

(`internal/agent/testdata/{researcher,qa,engineer,manager}_tui.golden`; section membership
confirmed in `BuildResearcherPrompt` / `BuildQAPrompt`, `internal/agent/prompt.go:299` and
`:319` — both assemble only identity + reflection + role-specific + RULES.)

So a researcher and a QA agent are never told:

- to weigh reversibility and blast radius before acting;
- **that other agents are running concurrently in sibling worktrees** — "Be especially aware
  that you are likely not the only agent running";
- the destructive-var guardrail (`rm -rf "$VAR"` forbidden unless the preceding line asserts
  `$VAR` is under `/tmp/`);
- not to bypass safety checks with `--no-verify`;
- how to handle a suspected prompt injection in a tool result;
- output/tone expectations.

Two of these are not theoretical:

- **The destructive-var guardrail exists nowhere else.** `grep -n 'Destructive-var\|== /tmp/\*'
  CLAUDE.md .claude/skills/*/SKILL.md` → no matches. It is *only* in the engineer and manager
  string constants. CLAUDE.md's `## /tmp hygiene` covers adjacent ground but never states the
  assert-then-delete rule. So the rule is invisible to humans and absent for two roles.
- **QA is explicitly ordered to run `make validate`** (`qaVerificationProtocolSection`, step 4,
  `internal/agent/prompt_child_sections.go:211`) while being told nothing about concurrency.
  A machine-wide `golangci-lint` lock under concurrent `validate` runs is a *known recurring
  false-RED on this host* — recorded in weave's private memory, visible to nobody else. QA's
  entire job is issuing verdicts, and we have built the one role most likely to misattribute a
  false RED and given it the least context to recognise one.

The researcher gap is the one I can attest to first-hand: my own system prompt for this task
contains no `# Environment` block and no safety section (see §1.2).

**Recommendation.** This is the clearest case in the audit for CLAUDE.md rather than a skill:
"you are one of several concurrent agents; assert before you delete; never `--no-verify`;
don't touch what you didn't create" is universal, short, and load-bearing for every role. Put
the rule in CLAUDE.md **once**, and delete the per-role duplication of it from the engineer and
manager constants — today the same "Executing actions with care" text is maintained twice in
Go (`engineerExecutingActionsSection`, `managerExecutingActionsSection`) and differs only in
whether it says "manager" or "parent".

## 1.2 FINDING 2 — the findings hand-off is broken on both ends by an unqualified relative path

Three facts, each verified:

1. `researcherDocumentingSection` tells the researcher: *"For research reports or findings:
   write to `.sprawl/agents/<name>/findings/`"* (`prompt_child_sections.go:183`). **Relative
   path.**
2. Both consumers are told to read the same relative path: `rootVerifyingWork` — *"Researcher:
   Check `.sprawl/agents/<name>/findings/`"* (`prompt.go:188`) — and
   `managerVerificationSection` — *"check findings in `.sprawl/agents/<name>/findings/`"*
   (`prompt_child_sections.go:298`).
3. **The researcher is the only role with no `# Environment` block**, so it is never told its
   working directory:

```
$ for f in researcher qa engineer manager; do ... done
researcher  Environment=0 WorkDir=0 bytes=3282
qa          Environment=1 WorkDir=1 bytes=3513
engineer    Environment=1 WorkDir=1 bytes=12296
manager     Environment=1 WorkDir=1 bytes=18965
```

`BuildResearcherPrompt` is the one builder that does not append `envContextBlock`
(`prompt.go:299-316` vs `:255`, `:319`, `:340`).

The mechanics: the researcher's cwd is its worktree, so the relative path resolves to
`<worktree>/.sprawl/agents/<name>/findings/` — a shadow directory that is **not**
`$SPRAWL_ROOT/.sprawl/agents/<name>/`. Confirmed on this very agent: `$SPRAWL_ROOT` is
the repo root, the real agent dir `$SPRAWL_ROOT/.sprawl/agents/<name>/` contains
`SYSTEM.md`, `activity.ndjson`, `prompts/` — and the worktree has its own separate
`.sprawl/` (`config.yaml`, `incidents/`). Meanwhile the manager reads the same relative path
from *its* worktree, and weave reads it from the repo root. **Three different resolutions of
one string, none of which is guaranteed to be the writer's.**

Then `retire` removes the worktree (`internal/agent/retire.go:46`), taking the shadow findings
dir with it. This is the real mechanism behind the "findings vanish on retire" belief recorded
in weave's memory — whose stated mechanism is wrong (§2.3).

**Recommendation.** Two one-line fixes, both in the binary, neither a docs problem:
give `BuildResearcherPrompt` the same `envContextBlock` every other role gets, and interpolate
an absolute path into the findings instruction (the builder already receives everything it
needs). Then state the *convention* — "agent artifacts go in `$SPRAWL_ROOT/.sprawl/agents/
<name>/findings/`, absolute" — once, in CLAUDE.md. This is exactly the thesis's "prefer a rule
over a description," and the enforcement belongs in the prompt builder, not in prose.

## 1.3 FINDING 3 — `DESCRIPTION.md` contradicts mandatory compiled behavior

CLAUDE.md line 3: *"Read `DESCRIPTION.md` for project context."* It is therefore the
highest-traffic doc in the repo after CLAUDE.md itself. Four confirmed defects:

| `DESCRIPTION.md` says | Tree says | Severity |
|---|---|---|
| "Agents come in **four** types" (line 48) and the type table omits QA entirely (50-55) | `SupportedTypes` = `{engineer, researcher, manager, qa}`; `ValidTypes` adds `tester`, `code-merger` (`internal/agentops/spawn.go:17,23`); `BuildQAPrompt` exists and is dispatched at `internal/supervisor/runtime_launcher.go:344`; `qa` is in the `spawn` tool enum (`internal/sprawlmcp/tools.go:77`) | High |
| "**ICs do the work but cannot spawn agents**" (Rule 5, line 111) and "They are leaf nodes — they cannot spawn other agents" (lines 80, 86) | `AgentTypesAllowedToSpawnSubAgents` = `{manager, engineer, researcher, qa}` (`spawn.go:26-29`), and the engineer TDD workflow **step 5 requires** spawning a code-review sub-agent: *"spawn a code reviewer as a sprawl sub-agent … Call it directly via the sprawl MCP tool — do NOT route through your manager"* (`prompt_child_sections.go:56-62`) | **Critical** — the doc forbids what the prompt mandates |
| Manager "uses `sprawl merge` to squash-merge" (lines 68, 107) | Prompts and tool schemas use the `merge` **MCP tool**; `sprawl merge` is the human CLI | Medium |
| QA never appears as a type or a lifecycle stage | The manager prompt makes dispatching a QA pass a *precondition for reporting done* (`managerVerificationSection`, `managerIntegrationBranchSection` (a)(b)(c)) | High |

Provenance is clean: `BuildQAPrompt` landed 2026-06-10; `DESCRIPTION.md` was last touched
2026-06-04 — six days earlier — and never updated. Two months stale.

**Recommendation.** `DESCRIPTION.md`'s *philosophy* (§Why, §The Forcing Function, §Name) is
excellent and timeless — keep it, move it to `docs/`. Its *type table, rules list, and role
sections* are a description of current code structure and are exactly what the thesis says to
delete. The role/capability matrix should be **generated** from `SupportedTypes` /
`AgentTypesAllowedToSpawnSubAgents` / the spawn enum, or dropped. The "ICs cannot spawn agents"
rule should not be reworded — it should be deleted, because it is no longer the design.

## 1.4 Dead claims inside the compiled prompts

Both survive into shipped prompts and both are read by an LLM as current fact:

| Prompt text | Location | Reality |
|---|---|---|
| `QA (type: "qa", once Arc Item #2 ships)` | `prompt_mode.go:257` (manager) | `qa` shipped; in `SupportedTypes` and the spawn enum |
| Researcher usable "as a QA verifier (family=\"qa\") **until the qa type ships**" | `rootAgentTypesTemplate`, `prompt_mode.go:119` (root) | Same |

`grep -rn "Arc Item"` finds exactly two hits in the tree: this one, and CLAUDE.md line 8's
"Arc Item #3 model" — an internal planning vocabulary that no longer resolves to anything a
reader can look up. Both should go.

Also worth flagging as an inconsistency rather than rot: the QA RULES say *"Do NOT spawn
sprawl children — you are a leaf verifier"* (`prompt_mode.go:68`) while `spawn.go:27` lists
`"qa": true` in `AgentTypesAllowedToSpawnSubAgents`. Prose forbids, code permits. Harmless
today, but it is the same class of divergence and the code gate is the cheaper place to fix it.

## 1.5 Structural asymmetries in the prompt layer

Not rot, but they are behavior decisions nobody appears to have made deliberately:

| Section | root | manager | engineer | researcher | qa |
|---|---|---|---|---|---|
| Size (bytes) | 26,287 | 18,965 | 12,296 | 3,282 | 3,513 |
| Safety / executing-actions | ✓ | ✓ | ✓ | — | — |
| `# System` (prompt injection, hooks, compaction) | ✓ | ✓ | ✓ | — | — |
| `# Environment` (workdir) | n/a | ✓ | ✓ | — | ✓ |
| Reflection step | — | — | ✓ | ✓ | ✓ |
| Names the Skill tool | ✓ (2×) | — | — | — | — |
| Names `CLAUDE.md` | ✓ (1×, incidental) | — | — | — | — |

Two of these matter for the restructure:

- **The manager is the only role with no reflection step.** Managers are where decomposition
  and dispatch quality is decided, and they are the role we ask for no retrospective.
- **No child prompt names the Skill tool** (`grep -ic skill` over the goldens: engineer 1 — a
  vague "check for applicable skills" inside the reflect step — researcher 0, manager 0, qa 0).
  **No child prompt names `CLAUDE.md` at all.** So the entire on-demand layer is discoverable
  to non-root agents *only* because Claude Code injects `CLAUDE.md` automatically and CLAUDE.md
  carries imperative pointers. If the restructure shrinks CLAUDE.md and drops those five
  pointer sentences, the skills layer goes dark for every child agent. This is the single
  strongest argument for the "breadcrumbs" clause of the CLAUDE.md destination rule.

## 1.6 A live cross-layer contradiction: does `report_status(complete)` halt you?

| Layer | Claim |
|---|---|
| weave private memory | "`report_status({state: complete\|failure, summary})` **HALTS the agent — must be the LAST action**" |
| CLAUDE.md `report-then-send` row | `Real.ReportStatus` calls `rt.StopAfterTurn(...)` instead of `rt.Stop` "**so a follow-on send_message in the same turn survives**" (QUM-866) |
| Child prompts (`childReportBulletsTemplate`, `prompt_mode.go:20-24`) | Silent. Says when to call it; says nothing about teardown |

CLAUDE.md is right and weave's memory is stale post-QUM-866. My own task prompt depends on the
CLAUDE.md semantics ("Then `report_status` complete **and** send me a message"), so this is not
academic. The authoritative copy is CLAUDE.md's lifecycle contract; the correct destination for
the *agent-facing* form of it is the `report_status` **tool description**, which is the layer
every agent reads and the layer with a zero-rot record (§1.0).

---

## 2. What lives only in weave's private memory

weave's Claude-project memory directory, outside the repo — 8 files, 150 lines. Not checked in,
not present in any worktree, invisible to every child agent. Per the public-repo rule I summarize
rules only and paste nothing; user-model and employer content is characterized in §2.2 without
detail.

A rule recorded there as *feedback* is, by construction, a rule that was violated because it
was not written down. There are two such files.

### 2.1 Durable, generally-applicable, and should be checked in

| Rule (summarized) | Who needs it | Destination |
|---|---|---|
| **Four known false-RED classes on this host** — disk-full `ENOSPC` from the Go build cache (made likelier by every `validate` now being a `-race` build); a machine-wide `golangci-lint` lock when two agents run `validate` concurrently; `Not logged in` from auth stripped in Bash subshells; a `TempDir` cleanup race presenting as a FAIL with no assertion failure. Discount all four before blaming your diff — and never paper over the lock with `\|\| true` | **Every agent that runs `validate`** — i.e. engineer + QA, the two roles with the least context | CLAUDE.md (short list) — this is the highest-value transfer in the audit |
| **Agent-wedge runbook** — three signatures (endless thinking with zero tool calls; a fresh engineer wedging at startup with zero commits; MCP dead but process alive) and the response: `peek` → `kill` → re-dispatch off the committed branch. A wedged child blocks `merge` | Managers and weave | New skill: `orchestration-runbook` |
| **Verify a live agent's worktree by hashing HEAD and requiring `git status --porcelain` to be exactly 0 lines before *and* after** — verifying a running agent's tree is racy | Managers, QA | Same skill |
| **Never pipe a test/probe script through `\| head` / `\| tail`** — the pipeline's exit status becomes the tail's, masking failures behind a fallback that never fires. Use explicit `wc -l` and check `$?` | Everyone writing verification | `testing-practices` (it is the same family as the QUM-997 rule already there) |
| **Live keystroke testing is the gold standard for interactive/lifecycle features**; confirm rendering with `tmux capture-pane -p -e` (the `-e` captures ANSI) | Engineers on TUI work | Already implied by `/tui-testing`; make the "-e" detail explicit there |
| **In-memory vs on-disk divergence is a recurring bug class** — a mutation must be persisted via `state.SaveAgent`; `schema_version` + migrate-on-load is the established pattern | Engineers touching `internal/state` | CLAUDE.md one-liner or `docs/` |
| **Scratch analysis tooling does not go in the tracked public tree** — put it in gitignored `.sprawl/incidents/<date>-<topic>/` | Every agent | CLAUDE.md — it is a one-line corollary of the existing hygiene section |
| **Forensics before repro**: capture to `.sprawl/incidents/` *before* running recovery; grep `.sprawl/logs/mcp-calls.jsonl` to settle a hypothesis before spawning a fix | Managers, engineers debugging | `orchestration-runbook` skill |
| **`messages_read` on a notification's advertised short id can fail as "ambiguous prefix"**; `messages_list` has no working `unread_only` filter and can dump 99K+ chars, blowing the token limit | Every agent with an inbox | The `messages_read` / `messages_list` **tool descriptions** (§8) — and file the two defects |

### 2.2 Correctly private — leave it there

User-preference and relationship content (communication style, response-length aversion, the
"bring evidence, not agreement" stance, approval scoping for pushes and cloud applies), the
leak-scan standing policy, cloud deployment specifics, and in-flight plan state. This is
user-model and employer context; the public repo is the wrong home for all of it.

One item is a boundary case worth a decision: the **"an asymmetric relation verified in the
convenient direction and reported in the desired one"** framing has *already* been promoted to
CLAUDE.md (§QUM-1083, §Never `git add -A`). That promotion is the model this section is asking
for. The private copy is now the duplicate.

### 2.3 Two claims in memory that are false about the current tree

Worth stating because it establishes that the private layer rots hardest, which is the argument
for moving things out of it:

1. *"`retire` unconditionally `RemoveAll`s `.sprawl/agents/<name>/` (QUM-1055) — not durable
   despite docs."* **Not true of the current code.** The only `RemoveAll` in the retire path
   targets `.sprawl/agents/<name>/logs` (`internal/agent/retire.go:54-57`); state is removed via
   `state.DeleteAgent`, and `internal/agentops/retire.go` only threads the dep through. The
   *conclusion* (findings can vanish) is right; the mechanism is the worktree removal at
   `retire.go:46` combined with the relative-path bug in §1.2. A right conclusion attached to
   the wrong mechanism is worse than no note, because it sends the fix to the wrong file.
2. The `report_status`-halts claim, stale since QUM-866 (§1.6).

---

## 2.5 Two failure classes: rules with no home, and rules with no teeth

Everything above is class **(a)** — a rule that exists nowhere durable, living only in weave's
private memory or compiled into `internal/agent/` string constants. `forge` asked me to
distinguish it from class **(b)**: a rule that *is* written down, in the shared checked-in layer,
and is violated in-tree anyway because nothing enforces it. Class (b) is the more important one
for the restructure, because **it is a prediction about everything we choose to keep.**

### 2.5.1 The specimen, extended

CLAUDE.md lines 5-11 define the agent / sub-agent / sidechain distinction and state flatly:
*"'Sub-agent' must never refer to a Claude Agent-tool spawn — use 'sidechain'."* It is violated
on **line 9 of `DESCRIPTION.md`** — the file CLAUDE.md line 3 tells every agent to read first —
in exactly the forbidden loose sense ("*rules that allow agents to self-organize, spawn
sub-agents*").

Two extensions I verified, both of which sharpen the point:

- **`docs/designs/tui-redesign-research.md:352`** uses the forbidden sense with maximum clarity:
  *"Sub-agents — Claude `Task` tool (NOT sprawl children)"*. That is the precise meaning the rule
  bans, spelled out.
- **Someone already tried to enforce this by hand, and the attempt is itself violated.** Three
  documents carry a manually added grandfather header — *"Terminology note (2026-06): pre-rename
  'sub-agent' in this doc refers to what is now called 'sidechain'"*
  (`docs/designs/messaging-overhaul.md:1`, `docs/research/tui-input-disappears-with-tall-tree.md:1`,
  `docs/research/mcp-surface-audit-2026-04-22.md:1`). And `messaging-overhaul.md` then uses the
  forbidden sense three more times *below its own note* (lines 63, 439, 696), in section
  headings.

So the sequence is: state the rule → hand-patch three files → and the front-page document plus a
hand-patched document are both still wrong. **Hand enforcement does not scale to a second pass,
and nothing recorded that it had been attempted.**

### 2.5.2 A class-(b) inventory in my own surface

| Rule (all in CLAUDE.md) | Verified violations | Grep-checkable? |
|---|---|---|
| Terminology: never "sub-agent" for a sidechain (L11) | `DESCRIPTION.md:9`; `tui-redesign-research.md:352`; `messaging-overhaul.md:63,439,696` — the last three under their own grandfather note | **Yes, trivially** |
| "Every file in `cmd/` and `internal/` has a corresponding `_test.go`. **Keep it that way.**" (L552) | **36**: 4 in `cmd/` (`hub.go`, `root.go`, `weavelock.go`, `hosttest/main.go`), 32 of 189 non-test files in `internal/` | **Yes** — but see the ambiguity note below |
| tmux safety: "In scripts, always use `_stmux` (not bare `tmux`)" (L439) | **Zero in `scripts/`** — the rule is fully honored where a wrapper exists. But it is violated in the *skills that teach the workflow*: `tui-testing` uses bare `tmux new-session`/`capture-pane`/`kill-session`, `e2e-testing-sandboxing` uses bare `tmux list-sessions`/`new-session`/`respawn-window` | **Yes** — and the existing check evidently never looked at `.md` |
| `/tmp` hygiene: "assert the path is under `/tmp/`, then delete" (L441-459) | `tui-testing`'s recipe ends in a bare `rm -rf "$TEST_ROOT"` with no assertion — the exact shape of the incident that hardened the sibling skill | **Yes** (pattern-matchable) |
| **Counter-example:** "Never `git add -A`" (L354-396) | **Zero.** The only tree occurrence is `scripts/test-gitignore-classes.sh:110`, with a comment saying it is *"used deliberately — it is the hazard under test"* | Yes — and it is the one rule in this table that is actually respected |

The `_test.go` rule carries a second lesson. Does "a corresponding `_test.go`" mean per-file or
per-package? At 36 violations, nobody can tell any more, and the two readings give different
verdicts. **An unenforced rule does not merely get violated; it loses its meaning, which is what
makes it un-enforceable later.** Teeth are cheapest at the moment the rule is written.

### 2.5.3 What this does and does not do to the premise of the restructure

`forge` put it strongly: if a rule in the first 10 lines of CLAUDE.md is violated on line 9 of
`DESCRIPTION.md`, then "write it in CLAUDE.md" is demonstrably not a mechanism. I agree with the
observation and want to narrow the conclusion, because the counter-example in the table is
decisive.

**The `git add -A` ban is stated in the same document, is equally unenforced by CI, and has zero
violations.** The difference is not the document. It is that the ban is (i) one unambiguous
string, (ii) attached to a concrete recent incident with a named cost, (iii) paired with an
adjacent mechanism that makes the failure visible (`.gitignore` classes, a dedicated harness),
and (iv) about an action an agent takes *deliberately*, in one place, where it is deciding.
The terminology rule is (i) a judgement about word usage, (ii) attached to no cost the writer
feels, (iii) paired with nothing, and (iv) violated *incidentally*, in prose, while writing about
something else.

So the sharper claim is: **CLAUDE.md works for rules that gate a deliberate action and fails for
rules that constrain incidental output.** That is a usable filter for what to keep, and it is not
the same as "prose never works."

### 2.5.4 Which retained rules can get teeth cheaply

My recommendation, split by mechanism. Everything in the first two groups is a `grep` over
tracked files, runnable in the existing `make validate` alongside `gitignore-classes` and
`leak-scan`, which are already exactly this shape.

**Cheap and worth doing now (single script, ~40 lines total):**

| Rule | Check |
|---|---|
| Terminology | `git grep -inE 'sub-?agents?' -- '*.md'` minus an allowlist of the grandfathered files; fail on new hits. Fix `DESCRIPTION.md:9` first, or the check lands red |
| `_test.go` companion | Enumerate non-test `.go` files without a sibling; fail on **increase** from a pinned baseline (36 today), not on non-zero. Ratchets without a 36-file cleanup |
| Dead path citations in docs | For every `` `path/like/this.go` `` in `CLAUDE.md`, `DESCRIPTION.md`, and `.claude/**/*.md`, assert the path exists. **This single check kills the largest defect class in this entire audit** — it would have caught `cmd/retire.go` (at least a dozen citing lines across two skills — 9 in `go-cli-best-practices`, 4 in `testing-practices`, measured at `3d92e2c`), `cmd/messages.go`, `internal/shlint`, and `cmd/report.go`. Roughly 15 lines of shell |

> Auditor's note, kept deliberately: the first draft of the cell above said "~18 citations". It
> was 13. I introduced a count-followed-by-code-entities error into the section that documents
> that exact class, while writing about it — which is the strongest available evidence that the
> mitigation has to be structural (state a floor, or cite the commit) rather than a resolution to
> be careful. Both are applied above.
| Dead symbol citations | Same, for `` `Identifier` `` tokens matching a Go-symbol shape: assert `git grep -q`. Noisier; run as a warning first. Would have caught `messagesDeps`, `defaultRetireDeps`, `send_async` |
| Bare `tmux` / unasserted `rm -rf $VAR` **in `.md`** | Extend the existing script-level convention to the docs that teach it. The rule already has perfect compliance in `scripts/`; the gap is purely that nobody pointed the check at prose |
| Skill frontmatter | Assert every `.claude/skills/*/SKILL.md` opens with YAML carrying `name` + `description`. Three currently fail (§6.9) |

**Cheap, and it should be generation rather than a check:** the role/capability matrix in
`DESCRIPTION.md` and the agent-type lists inside the prompt constants (§1.3, §1.4). These are
projections of `SupportedTypes` / `AgentTypesAllowedToSpawnSubAgents` / the `spawn` enum.
Generating makes the rot impossible instead of detectable — the only unambiguously worthwhile
mechanical fix in this audit.

**Genuinely prose-only — do not try to check these:**

- The safety and blast-radius rules (§1.1). "Weigh reversibility", "you are not the only agent
  running", "investigate before deleting" are judgement calls. Their enforcement mechanism is
  the *pre-commit and reference-transaction hooks* for the cases that matter
  (`guard-main-commit`, `guard-main-ref`) — note that this is exactly the pattern: the repo did
  not write a better paragraph about not committing to `main`, it wrote a hook git cannot skip.
  Where a hook is possible, prefer it; where it is not, prose plus a per-role prompt section is
  the honest ceiling.
- The four false-RED classes (§2.1). Diagnostic knowledge, not a constraint. Nothing to check.
- Assertion rigor (§6.2). Already the subject of a 794-line attempt at mechanization that its own
  authors label a presence check (§8.3). Do not extend it.
- The public/private hygiene rule. Partially mechanized already (`guard-employer-leak`), and
  CLAUDE.md itself records that the mechanism is text-only and structurally blind to binaries.
  The teeth here are an independent auditor, not a grep.

**The asymmetry that settles the priority order:** the four doc-citation checks above are cheap,
have no false-negative cost, and each one converts a class of silent rot into a red build. The
behavioral rules cannot be checked at all. So the restructure should spend its enforcement budget
entirely on the citation checks, and spend its *editorial* budget on making the behavioral rules
short enough to be read — which is the same conclusion §4 reached from the other direction.

---

## 3. Duplication across the three layers, and which copy should win

| Content | Copies | Authoritative | Verdict |
|---|---|---|---|
| "Keep spawn prompts short — point the agent at the issue, don't repeat it" | CLAUDE.md §Spawning Agents; `managerPostDispatchBlock()` (`prompt_mode.go:234`); weave memory | **The prompt** (it is per-role and already delivered) | Cut the CLAUDE.md copy to a breadcrumb; delete the memory copy |
| Linear issue lifecycle (In Progress on pickup → comment progress → Done with summary) | CLAUDE.md §Linear Issue Tracking; `/linear-issues` skill; weave memory | **The skill** | Cut CLAUDE.md to the pointer; delete the memory copy |
| `delegate` vs `send_message` vs `report_status` decision rule | `rootDelegateVsMessages` **and** `managerDelegateVsMessages` (near-identical, ~7 lines each, maintained twice in Go); `childReportBulletsTemplate`; **and** the MCP tool descriptions, at length | **The tool descriptions** | Collapse the two Go constants into one; the tool description is the copy that cannot rot |
| Parallelism-vs-serialization | `rootParallelism` **and** `managerParallelismSection` — byte-identical but for one trailing clause | One shared constant | Deduplicate in Go |
| "Executing actions with care" | `engineerExecutingActionsSection` **and** `managerExecutingActionsSection` — differ only in "manager"/"parent" | One templated constant, extended to researcher + QA (§1.1) | Deduplicate + extend |
| Retire/merge safety ("default to safe retirement", "before retiring researchers, check for committed artifacts") | `rootMergeRetireBlock`, `rootRules`, `rootCommands`, `managerLifecycle` — **four** copies in the same package | One constant | Deduplicate |
| KISS/YAGNI | `rootKISS`, `engineerDoingTasksSection` tail, `managerToneSection` tail, weave memory | Any one | Harmless; leave |

**The biggest single duplication is not doc-to-doc — it is inside the binary.** Six behavioral
blocks are maintained in two-to-four near-identical Go string constants in one package
(`internal/agent/`), each pinned byte-for-byte by a golden test, so every edit must be made
2–4× and re-goldened 2–4×. That is the mechanism by which "until the qa type ships" survived in
two places (§1.4) and why researcher/QA got skipped when safety text was added to the other two
(§1.1). The restructure's highest-leverage change in this layer is **compose the prompts from
shared sections rather than per-role copies** — which is also the stated intent of
`prompt_mode.go`'s template idiom comment (QUM-534), only partially applied.

---

## 4. Where the surviving agent-behavior content should live

Three destinations, and I will argue the boundary rather than just assign.

**The boundary test I used: "would a *newly spawned researcher* be wrong without this?"**
Not "is it important" — importance is why everything is in CLAUDE.md today. If a rule changes
what any role does on any task, it is universal. If it only changes what one role does, it
belongs in that role's system prompt, which is already delivered for free and costs CLAUDE.md
nothing.

### → `CLAUDE.md` (target: ~25 lines from this half of the audit)

1. **Concurrency and blast radius** (~5 lines). You are one of several agents in sibling
   worktrees; assert-before-delete for any variable-driven destructive command; never
   `--no-verify`; don't kill processes you didn't start; don't touch files you didn't create.
   *Universal, currently missing for two roles, and the destructive-var rule exists nowhere a
   human can find it.*
2. **The four false-RED classes** (~6 lines, or 2 lines + a pointer). *Universal for anyone who
   runs `validate`, and the highest-value item currently trapped in private memory.*
3. **Artifact placement** (~3 lines). Findings → absolute `$SPRAWL_ROOT/.sprawl/agents/<name>/
   findings/`; scratch tooling and forensics → gitignored `.sprawl/incidents/<date>-<topic>/`,
   never the tracked tree. *Universal, and it is a rule, not a description.*
4. **Breadcrumbs** (~6 lines). One line per skill, each stating the trigger, not the contents.
   *Load-bearing: §1.5 shows no child prompt names the Skill tool, so these five sentences are
   the only thing keeping the on-demand layer alive for children.*
5. Keep §Public vs Private Repo Hygiene, compressed. *Universal and irreversible if violated.*

Everything else in CLAUDE.md's behavior sections becomes a pointer or dies:
§Spawning Agents → one line inside the breadcrumbs; §Linear Issue Tracking → pointer only;
§Session Handoff → pointer only; §Meta: Developing Sprawl Inside Sprawl → keep (2 lines,
genuinely universal, genuinely surprising to a new agent).

### → `docs/`

- `DESCRIPTION.md`'s philosophy — §Why, §The Forcing Function, §Name, the manager judgment
  principle. Timeless, explains how we got here, nobody needs it per-turn.
- The **role/capability matrix**, either generated from `spawn.go` or deleted (§1.3).
- The behavior-layer architecture: which of the five layers owns what, and the accuracy
  ordering in §1.0. This audit's §8 is the seed of that document.

Note for whoever lands this: `docs/` already has `architecture/`, `design/`, **and**
`designs/`, plus 113 files / 267k words. Adding to it without a taxonomy pass just moves the
problem.

### → `.claude/skills/`

- **New: `orchestration-runbook`** — the wedge runbook, live-worktree verification, forensics-
  before-repro, merge/retire hygiene, wave sizing. Loaded by managers and weave only; too
  situational for CLAUDE.md, too operational for `docs/`. This is where most of §2.1 lands.
- Additions to `testing-practices` (the `| head` masking rule) and `/tui-testing` (the
  `capture-pane -e` detail).

### → the system prompt / tool descriptions (a fourth destination the three-bucket rule omits)

The three-destination rule has a gap: **role-specific behavior already delivered by the binary
should stay in the binary, and per-tool semantics belong in the tool description.** Neither is
CLAUDE.md, `docs/`, or a skill. Concretely: the TDD workflow stays in the engineer prompt; the
QA verification protocol stays in the QA prompt; `report_status` teardown semantics (§1.6),
the `messages_read` ambiguous-prefix trap, and the delegate-vs-message decision rule belong in
the tool descriptions — the one layer with a zero-rot record. I recommend adding this as an
explicit fourth destination, otherwise the restructure will pull compiled behavior into
CLAUDE.md and make the per-turn tax worse.

---

# Part 2 — the `.claude/` on-demand layer

## 5. Verdict in three lines

1. **The skills layer is worth keeping and is currently unsafe to follow.** Three of the seven
   skills are built on an agent-facing CLI surface that no longer exists, and one of them tells
   agents to call three MCP tools that the test suite actively asserts are absent.
2. **The rot is a maintenance-attention problem, not a size problem.** Five of seven skills have
   not been touched since May 2026 (one since April); the single skill under active maintenance,
   `testing-practices`, is also the only one whose *principles* sections are pristine.
3. **`testing-practices` is a second dumping ground with the same disease as CLAUDE.md — but
   unlike CLAUDE.md it is paid for only on demand, so the correct response is to split and
   prune it, not to shrink it for cost reasons.**

## 6. The skills inventory

Sizes, last touch, and the summary verdict. "Dead claims" counts distinct verified-dead
assertions about the tree, not instances.

| Skill | Lines | Last touched | Frontmatter | Dead claims | Verdict |
|---|---|---|---|---|---|
| `testing-practices` | 1933 | **2026-08-06** | **none** | ~20 | Split (§6.1) |
| `go-cli-best-practices` | 446 | 2026-05-11 | **none** | ~10 | Rewrite or retire (§6.3) |
| `cli-ux-best-practices` | 311 | 2026-05-14 | **none** | 6 | Trim + repoint (§6.4) |
| `linear-issues` | 212 | 2026-05-14 | best of the seven | 3 (all in one block) | Amputate one section (§6.5) |
| `e2e-testing-sandboxing` | 180 | 2026-05-14 | good | 4 | Fix examples (§6.6) |
| `tui-testing` | 111 | **2026-04-13** | adequate | 7 | Gut Part B (§6.7) |
| `handoff` | 69 | 2026-05-14 | good | **0** | **The model to copy (§6.8)** |
| `agents/oracle.md` | 13 | 2026-06-10 | good | 0 | Keep |
| `agents/test-critic.md` | 15 | 2026-06-10 | good | 0 | Keep + one line (§6.9) |
| `settings.json` | 10 | 2026-04-30 | — | 1 dead key | §6.10 |

### 6.0 The headline: three skills document a CLI that was deleted

The live root commands are `cleanup branches`, `color`, `config`, `debug`, `enter`, `gc`,
`hooks`, `hub`, `logs`, `memory`, `merge`, `sandbox-gc`, `usage`, `version`. There is no
`cmd/retire.go`, `cmd/spawn.go`, `cmd/messages.go`, `cmd/report.go`, `cmd/status.go`, or
`cmd/kill.go` — those verbs became MCP tools. Consequences:

- `go-cli-best-practices` cites `cmd/retire.go` **eight times** as its canonical worked example,
  including its layout diagram and its DI walkthrough. `cmd/messages.go`, `cmd/report.go`, and
  `cmd/spawn_test.go` are also cited and also absent.
- `e2e-testing-sandboxing`'s entire "Exercising Features" section (three examples: `$SPRAWL_BIN
  spawn`, `$SPRAWL_BIN status`, `$SPRAWL_BIN messages send`) is 100% dead.
- `cli-ux-best-practices` asserts "Sprawl uses direct verbs: spawn, kill, retire, merge, status,
  handoff. …Keep it." Only `merge` exists.
- `testing-practices`'s § *Dependency Injection Testing Pattern* is built on the same dead files
  (§6.1).

This is the QUM-1111 failure mode at scale: an agent reading these skills will produce a plan
whose commands don't exist, and — worse — will believe it has learned the repo's conventions.

### 6.1 `testing-practices` (1933 lines) — is it a second dumping ground?

**Yes, structurally — and no, in the way that matters.** The structure:

| `##` section | Lines | Share |
|---|---|---|
| `## Assertion Rigor` | 35–1687 | **85.5%** |
| `## Dependency Injection Testing Pattern` | 1688–1798 | 5.7% |
| `## Validating Agent Behavior` | 1829–1881 | 2.7% |
| `## Running Tests` | 3–34 | 1.7% |
| `## Manual CLI Validation` | 1799–1828 | 1.6% |
| `## Testing Pyramid` | 1882–1904 | 1.2% |
| `## Common Pitfalls` | 1905–1933 | 1.5% |

One section is six-sevenths of the file, and one of *its* subsections — `### Indistinguishable
from success (QUM-1047)` at lines 429–1122 — is **694 lines, 36% of the whole document**. That
is a standalone incident report living inside a practices guide. The same accretion pattern as
CLAUDE.md: one well-argued incident at a time, nothing deleted.

The crucial difference from CLAUDE.md, and the reason the thesis applies differently: **this is
paid on demand, not on every turn by every agent.** Size is therefore not the cost driver;
*findability* and *accuracy* are. So the prescription is split, not shrink.

The correction to the premise in the brief: at 18,225 words it is **1.7× CLAUDE.md's 10,577**,
not the ~2.5× the line counts suggest — CLAUDE.md's lines are far longer.

**Its content splits cleanly along the thesis's own axis.** Roughly 600 lines are pure,
tree-independent principle and are in excellent shape (`### The rule`, `### Necessary but not
sufficient`, `### Negative assertions`, `### Claims about code, and claims about claims`,
`### Which tree is your claim about?`, `### A null result is a statement about your search`,
`### Reading a control, and reading a failure`, `### The honest limit`). Every confirmed dead
claim is in the other kind of content — a named file, a named test, a count, or a line number.

Dead claims worth calling out:

- **§ *Dependency Injection Testing Pattern* is the most rotted section in the skills layer.**
  Four named files gone (`cmd/retire.go`, `cmd/retire_test.go`, `cmd/messages.go`,
  `cmd/messages_test.go`), `Retire`'s signature wrong (it gained a `ctx` and now returns
  `([]string, error)`), and three identifiers vaporised (`messagesDeps`, `defaultRetireDeps`,
  `defaultMessagesDeps` — zero hits tree-wide). **CLAUDE.md line 552 points readers here.**
- **`internal/shlint` is absent from the working tree *and from all git history*** (`git log
  --all -- internal/shlint` → empty). A ~50-line evidence subsection rests on it. See §6.2 for
  why this one matters beyond the skill.
- Its two opening `go test -run` examples name tests that do not exist
  (`TestRetire_HappyPathDeletesState`, `TestMessagesSend_HappyPath`).
- Counts have drifted exactly as its own § *Which tree is your claim about?* warns: "75 tracked
  shell files" → 72; "115 assertions call `pass`" → 114; "`subagent-model.sh`'s 428 lines" → the
  file is now **50**.
- **Eight of its `file:line` citations have drifted**, several inside passages presented as
  *verbatim* failure transcripts (`weave_handle_test.go:718` → `:726`; `:931` → `:939`). The
  section immediately above them states the rule that an unlocatable quotation is a defect.
- The CLAUDE.md cross-references it quotes — `"TUI-notifier changes are mandatory-tested"`,
  `"Handoff-path changes are mandatory-tested"` — **do not exist in CLAUDE.md** (`grep -F` → 0).
  CLAUDE.md replaced that prose with the matrix table.
- `## Running Tests` teaches `go test ./...` with no mention of `-race` or `make validate`,
  contradicting CLAUDE.md line 62's explicit warning that bare `make test` is "NOT what validate
  uses."

**`query`'s enumeration heuristic is strongly confirmed here.** The detector — *a count followed
by a list of code entities* ("three files", "both", "all 11", "the only") — accounts for a large
share of this file's dead claims, and in every case the count is what rotted:

| Enumeration in the skill | Reality |
|---|---|
| "**three** self-pinning tests on one flag … **all three pass**" | 2 of 3 exist; the sentence is unrunnable |
| "`rt.inTurnLocked() \|\| rt.frameTurnOpen` **occurs twice** … mutating the *other* one leaves this test green" | Occurs **once**. The paragraph's entire point has no live referent |
| "surfaced **three** wrong ones (`items_dim_test.go`, `pendingzone.go`, `tuiadapter_test.go`)" | `internal/tui/tuiadapter_test.go` does not exist |
| "the shape exists at all in **only 1 of 75** tracked shell files" | 72 tracked shell files |
| "the suite's **115** assertions call `pass` directly" | 114 |
| "The standalone CLI exposes **only**: enter, logs, merge, cleanup branches, config show, memory show" | Missing ~10 commands; the word "only" is false |
| e2e harness list = **3** targets | Stale by 10 |
| (sibling: `tui-testing`) "**all 8** test scenarios" | 9 |

This is the argument that `testing-practices` has the same disease as CLAUDE.md rather than being
a healthy on-demand doc — and it is a *sharper* argument than size, because the failures are
identical in kind to the `atomicDuration` "three files"/four case `query` found. Note the
aggravating factor: this file contains a section titled *"Which tree is your claim about?"*
warning against exactly this, and violates it eight times. **Being the document that states the
rule confers no protection from it.**

Two mitigations for the rewrite, in preference order: (1) prefer "several" / "at least N" to an
exact count whenever the number is not itself the point — a floor cannot rot upward; (2) where the
count *is* load-bearing, make it derived, or state the commit it was measured at. The file already
does (2) correctly in one place ("more than twenty", self-labelled as a floor) and that clause is
the one enumeration in the section that is still true.

**Recommendation.** Split into three:

1. **`assertion-rigor` skill (~600 lines)** — the principles, the shell-shape table, the
   corollaries, the worked example. This is the repo's single most valuable piece of engineering
   doctrine and it should be findable on its own name.
2. **`docs/forensics/` (~700 lines)** — `### Indistinguishable from success` and the incident
   archaeology. This is core product truth about how we learned the rule; it is not a practice
   guide, and it is the destination the three-bucket rule reserves for "enough history to
   understand how we got here."
3. **`testing-practices` (~250 lines)** — running tests (fixed to say `-race` / `make validate`),
   the DI pattern (rewritten against `cmd/merge.go` / `cmd/gc.go`), the pyramid, the pitfalls.

Delete outright: `#### The evidence: a detector for this class` (built on a package with no git
history) and every bare count and line-number citation. **Line numbers should not appear in
these docs at all** — they are the fastest-rotting citation form in the repo and eight of eight
have drifted.

### 6.2 The biggest duplication in the audit — and how compression hid a dead claim

CLAUDE.md lines 554 and 556 are compressed restatements of `testing-practices` §§ *Assertion
Rigor* and *The non-asserting fallback*. The overlap is near-total; identical clauses on both
sides include *"an assertion nobody has watched fail is a claim, not a check"*, *"assertion-count
floor"*, *"`0 passed / 0 failed`"*, *"a negative control, a mutation, or a red-first run"*, and
*"a parent-commit control proves a failure is pre-existing, never that it is acceptable"*.

**This duplication is deliberate and test-pinned.** `cmd/docs_assertion_convention_test.go`
declares `const assertionRigorSection = "Assertion Rigor"` — *"Shared so a rename cannot leave
the two homes naming different sections"* — and fails if either home loses the text. So the
usual "the copy dies, the pointer survives" move **cannot be applied here without deleting the
test**, and that decision should be made explicitly rather than discovered by a red build.

My recommendation: keep the two-home split but **move the boundary**. CLAUDE.md should carry the
rule and the two corollaries that are genuinely universal (`exit 77` for skips; `set +e` obliges
a floor) and drop the audit narrative it has absorbed — "six live instances", "five structural",
"`40 PASS / 15 FAIL / exit 0`", "462 lines across 5 harnesses", "a deterministic parser was built
and rejected". That is roughly 120 words of incident detail in the document every agent pays for
on every turn.

**And there is a sharp lesson in that last item.** The skill names the rejected parser:
`internal/shlint` — verifiably dead, absent from all git history. CLAUDE.md describes the same
artifact without naming it. **The compressed copy survives the audit only because it is too vague
to falsify.** That is not a virtue. A claim that cannot be checked is not more durable than one
that can; it is only harder to notice when it goes wrong. If the story is worth telling it should
be told in `docs/` where it can name things and be checked; if it is not, both copies should go.

### 6.3 `go-cli-best-practices` (446 lines) — rewrite or retire

Beyond the dead `cmd/retire.go` spine (§6.0): the module inventory claims Go 1.25.0 (`go.mod`
says 1.25.7) and lists three dependencies (cobra, pflag, mousetrap) where `go.mod` now
direct-requires ~19, including `charm.land/bubbletea/v2`, `connectrpc.com/connect`,
`github.com/jackc/pgx/v5`, `go.uber.org/goleak`, and `gocloud.dev`. Its layout diagram names 9
`internal/` packages where there are 32 — omitting `internal/sprawlmcp` (the actual agent-facing
API) and `internal/tui` (60+ files).

That last point is the real problem: **the skill is titled and scoped for a cobra CLI, and this
repo is now mostly a TUI, a runtime, and an MCP server.** A model editing
`internal/supervisor/drain.go` has no reason to load it, and little reason to want to.

One section is excellent, unique, accurate, and back-referenced from production code:
§ *Subprocess stdio: TTY vs pipe* (QUM-261) — `internal/claude/resumewatch.go:163` cites the
skill by name. **Keep that verbatim.**

**Recommendation.** Cut to ~180 lines: keep TTY-vs-pipe and the cobra/DI conventions rewritten
against live files; delete the 100-line testing section (CLAUDE.md line 552 already routes tests
to `/testing-practices`, so that section is in the wrong home), the dependency inventory
(`go.mod` is the source of truth and this will rot again on the next `go get`), the cobra
validator/flag-type lists (upstream docs a model already knows), and the pkg.go.dev link farm.

### 6.4 `cli-ux-best-practices` (311 lines) — trim and repoint

Its doctrine is good and is genuinely enforced in code (`cmd/sandbox_gc_test.go:248,260` and
`cmd/memory.go:88` cite the skill by name — a rare and healthy pattern). Dead: `sprawl merge
--force` (merge has only `-m`, `--no-validate`, `--dry-run`), a `--json` flag no command has,
`cleanup-branches` as a hyphenated-name example when the live shape is the noun-verb subcommand
`cleanup branches`, and the framing rule "No TUIs, no interactive prompts" — which is
contradicted by the flagship command being a full Bubble Tea TUI and needs an explicit carve-out.

It also duplicates `go-cli-best-practices` near-verbatim on idempotency and on validation-error
wording. Pick one owner; the UX skill is the better home.

Cut the "Patterns from Famously Good CLIs" section (git/gh/kubectl/terraform/docker — generic
prior knowledge, zero repo content) and the redundant output-template appendix. Target ~200 lines.

### 6.5 `linear-issues` (212 lines) — highest-blast-radius fix in the layer

The issue-authoring content is good and its frontmatter description is the best of the seven
(five explicit trigger phrases; a model would load it unprompted). But its tacked-on
§ *Messaging Tools* documents `send_async`, `send_interrupt`, and `message` as the "default
messaging channel" — **all three removed in QUM-550 slice 5, with tests asserting their absence**
(`internal/sprawlmcp/server_sendmessage_test.go:254-259`;
`internal/sprawlmcp/tool_description_sync_test.go:70` bans them by name). The live tool,
`send_message`, is never mentioned. **An agent following this skill has no working way to message
a peer.**

Two live conflicts with CLAUDE.md, where a reader gets a different answer depending on which
document they read:

- **Required fields.** CLAUDE.md line 584: the skill "defines required fields (label, milestone,
  state)". The skill's required list is "Title, Description, Labels, Priority" — milestone and
  state absent, priority absent from CLAUDE.md's. Neither is a superset.
- **Default state.** The skill's convention section says default to `Backlog`; its canonical
  create example 35 lines later uses `Todo`.

**Recommendation.** Delete § *Messaging Tools* entirely — it is off-topic for a Linear skill and
it is the only fully dead block in the file. Messaging semantics belong in the **tool
descriptions** (§4, fourth destination). Reconcile the required-field list and pick one owner;
CLAUDE.md should keep only the pointer.

### 6.6 `e2e-testing-sandboxing` (180 lines) — fix the examples, then it's solid

Frontmatter is good. The hygiene contract, the sandbox-gc wiring, and the closing incident story
(2026-04-21, `rm -rf $SPRAWL_ROOT` destroyed a real repo) are accurate and load-bearing — keep
the story, it is why the DO-NOT list gets obeyed.

Fix: the three dead `$SPRAWL_BIN` examples (§6.0); add `SPRAWL_TMUX_SOCKET` to the env table
(it *is* exported by `sprawl-test-env.sh` and CLAUDE.md lines 437-439 make it load-bearing); and
**stop advising bare `tmux`**. The skill uses `tmux list-sessions`, `tmux new-session`, and
`tmux respawn-window` in three places, which CLAUDE.md explicitly forbids in favour of `_stmux`
— on the sandbox socket, bare `tmux` queries the wrong server. This is not overlap, it is a
contradiction the skill loses.

### 6.7 `tui-testing` (111 lines) — oldest and most broken by proportion

Untouched since 2026-04-13. About 40% of it is wrong:

- Teaches **Tab-switches-panel-focus**, deleted in QUM-695 — `internal/tui/app.go:3179` is
  literally a stub whose comment says so, and there is no `"tab"` handler.
- Says "8 test scenarios"; the script has 9 (test 9, QUM-304 stderr-leak, undocumented).
- **Its ~30-line manual recipe is not copy-pasteable**: every `$` is backslash-escaped
  (`TEST_ROOT=\$(mktemp …)`), an artifact of the file being generated inside a heredoc.
- That same recipe hand-rolls a `/tmp` root and ends in a bare `rm -rf "$TEST_ROOT"` **with no
  `/tmp/` assertion** — the exact shape of the incident the sibling skill was hardened against,
  and a direct violation of CLAUDE.md's `/tmp` hard rules.
- Its two "Known Issues" (no color rendering; assistant text not rendering) are both contradicted
  by four months of subsequent work — `internal/tui/theme.go` and the whole `tui-live-render`
  matrix row exist to assert those very things.
- It never mentions `scripts/e2e-matrix.sh`, which per CLAUDE.md is now the actual gate, nor any
  of the ~15 TUI-touching matrix rows.

**Recommendation.** Delete the manual recipe and replace it with "use `/e2e-testing-sandboxing`
to obtain `$SPRAWL_BIN`/`$SPRAWL_ROOT`/`_stmux`, then `_stmux new-session -d -x 200 -y 50`" —
that one edit removes the escaping bug, the hygiene violation, and a pane-size disagreement with
the sibling skill (120×40 vs the 200×50 the e2e skill says is required) simultaneously. Drop both
Known Issues, fix the scenario count, delete the Tab row, and point at the matrix.

### 6.8 `handoff` (69 lines) — the model the other six should copy

**Zero dead claims.** Every path, symbol, and section name verifies, including the *order* of the
context-blob sections against `internal/memory/context.go:100-167`. And CLAUDE.md's entry for it
is a two-line pointer with no duplicated content.

That is not a coincidence: it is the shortest skill, it describes a ritual rather than a code
structure, and CLAUDE.md never tried to summarize it. **Short + pointer-only + describes a
procedure rather than a file layout = the only skill in the set that did not rot.** That is the
template, and it is the thesis restated from the evidence side.

### 6.9 Frontmatter: three skills are discoverable only through CLAUDE.md

`cli-ux-best-practices`, `go-cli-best-practices`, and `testing-practices` have **no YAML
frontmatter at all** — they start with an H1. Their listing descriptions degrade to the title
("Go CLI Best Practices for Sprawl"), which states no trigger. A model editing
`internal/tui/app.go` has no signal to load any of them.

Combine that with §1.5 — **no child agent's system prompt names the Skill tool** — and the
picture is stark: the three largest skills in the layer are reachable by a child agent only
because CLAUDE.md carries five imperative pointer sentences. **Adding frontmatter to those three
is the cheapest high-value fix in this audit**, and it is a precondition for shrinking CLAUDE.md
safely.

`.claude/agents/oracle.md` and `test-critic.md` are both clean (no repo references, nothing to
rot) and both pinned by the `sidechain-discovery-smoke` matrix row. One gap: **`test-critic.md`
reviews tests against generic TDD lore and never references `/testing-practices`** — so the
repo's aggressively maintained assertion-rigor standard is invisible to the agent whose entire
job is reviewing assertions. One added line fixes it.

### 6.10 `.claude/settings.json`

Reported as findings, not acted on (the brief bars edits here and disk constraints bar testing):

- **`claudeMdExcludes` is not a recognized Claude Code settings key.** It appears exactly once in
  the tree and nothing reads it or tests it. Its evident intent — suppressing the user-global
  `CLAUDE.md` — is therefore **not in effect**, which is observable: that file's contents are
  present in this agent's context and instruct agents to use a `coder_report_task` tool that is
  in no sprawl agent's tool set. Related and tracked separately: `coder_report_task` is
  referenced in the tracked public tree at `internal/sprawlmcp/tools.go:190` and in
  `docs/designs/messaging-overhaul.md`, coupling the schema to host tooling sprawl does not ship.
- No `env` block, despite CLAUDE.md documenting seven operational env knobs.
- No Bash entries under `permissions.allow`, despite `make` / `git` / `bash scripts/*` being the
  documented happy path for every agent. Worth a decision, not a silent gap.

## 7. Bucketing summary for the skills layer

| Item | Destination | One-line reason |
|---|---|---|
| `testing-practices` §§ principles (~600 ln) | **new `assertion-rigor` skill** | The repo's best doctrine; deserves its own findable name |
| `testing-practices` § *Indistinguishable from success* (~694 ln) | **`docs/forensics/`** | Incident archaeology — history, not practice |
| `testing-practices` remainder (~250 ln) | **skill, rewritten** | Running tests / DI / pyramid / pitfalls, against live files |
| `go-cli` § TTY-vs-pipe | **skill, verbatim** | Unique, accurate, cited from production code |
| `go-cli` deps inventory, validator lists, link farm | **cut** | `go.mod` is the source of truth; the rest is upstream docs |
| `go-cli` testing section (~100 ln) | **cut** | Duplicates `/testing-practices`, which CLAUDE.md already points at |
| `cli-ux` doctrine | **skill, trimmed** | Enforced by two live tests; earn its keep |
| `cli-ux` "Famously Good CLIs" + template appendix | **cut** | Generic prior knowledge and self-restatement |
| `linear-issues` authoring content | **skill** | Correct, well-triggered, on-topic |
| `linear-issues` § *Messaging Tools* | **cut** → tool descriptions | Fully dead and off-topic; belongs where it can't rot |
| `e2e-testing-sandboxing` | **skill, examples fixed** | Contract and incident story are load-bearing |
| `tui-testing` manual recipe | **cut** → delegate to e2e skill | Unrunnable as written and violates `/tmp` hygiene |
| `handoff` | **skill, unchanged** | Zero defects; the template for the rest |
| CLAUDE.md's inlined copies of skill content (lines 554, 556, 586-589, 593-595) | **cut to breadcrumbs** | ~120 words of incident detail on every turn, for a rule the skill already owns |
| Five pointer sentences + one line per skill | **CLAUDE.md** | The only thing keeping the layer discoverable for child agents |

**What is lost by the cuts, and why it is acceptable.** The "Famously Good CLIs", validator
lists, and link farms lose nothing — a model already knows them. The dependency and CLI-surface
inventories lose a snapshot that is already wrong and that `go.mod` / `cobra` answer correctly on
demand. CLAUDE.md's audit-narrative sentences lose vividness; the rule survives verbatim and the
narrative moves to `docs/`, which is where a reader who wants to be persuaded can go. The only
genuinely risky cut is the `internal/shlint` evidence section, because it is the argument against
someone rebuilding the parser — so **the "do not rebuild it" instruction must survive in the
skill even though the evidence moves**, or the cut buys a rebuilt parser.


---

## 8. Testing the thesis

The brief asked me to test the thesis, not confirm it. Three results.

### 8.1 Confirmed, strongly: "descriptions of current code structure rot"

Every confirmed dead claim in this audit is a description of code structure, and every
rot-resistant passage is a rule or a principle. `DESCRIPTION.md`'s philosophy sections are two
years' worth of accurate; its capability table went stale in six days (§1.3). The prompt
constants' *rules* are fine; their *status claims* ("once Arc Item #2 ships") are dead (§1.4).
This is a clean natural experiment inside one repo, and it points one way.

### 8.2 Confirmed, and sharper than stated: **the fewer readers, the faster the rot**

The thesis frames rot as a function of doc type. The evidence says it is also a function of
audience size and of whether anything executes the claim (§1.0). The two layers where I found
claims that are simply *false* about current code are the two nobody greps: weave's private
memory and the compiled Go string constants. Meanwhile the MCP tool schemas — the layer read by
every agent on every call — had zero defects.

**Corollary the restructure should adopt: moving a behavioral claim into a layer with more
readers is a rot fix, independent of cutting anything.** That is the argument for the tool
descriptions in §4, and it is the argument against the private-memory status quo in §2.

### 8.3 Pushback: "should this be a test?" has already been tried here, and it cost 794 lines

The thesis suggests preferring a test, a lint rule, or generation over prose. There is a
measured in-tree data point: `cmd/docs_assertion_convention_test.go` — **794 lines of Go**
guarding roughly **three paragraphs** of CLAUDE.md + one `testing-practices` section. Its own
header comment is admirably honest about what it bought:

> "The first two are PRESENCE checks scoped to the section the text has to live in. They fail
> if it is deleted, reworded past the mechanism it names, or relocated — but **they cannot
> verify that anyone follows the convention.**"

and, on the one real invariant it does enforce:

> "It does NOT protect the floor's value — lowering `MIN_ASSERTIONS` while updating the doc
> passes. **Do not cite it as guarding the harness's strength.**"

So: 794 lines to prevent three paragraphs from being deleted or moved. That is a *pin against
deletion*, not a check that the documented thing is true. It is worth having for a rule that
was violated repeatedly, and it is emphatically not a general answer to prose rot — at that
ratio, pinning CLAUDE.md's rot-prone content would cost more lines of test than the document
has words.

Where "make it mechanical" *does* pay, on this evidence:

- **Generation, not pinning.** The role/capability matrix (§1.3) and the agent-type lists in the
  prompts (§1.4) are projections of `spawn.go` variables. Generating them makes the rot
  impossible rather than detectable. This is the only mechanical fix in the audit I would call
  unambiguously worth it.
- **Deduplication, not pinning.** Six behavioral blocks maintained 2–4× in Go (§3) is a rot
  *source*. Collapsing them removes the divergence rather than testing for it — and the golden
  tests already in place will keep working.
- **Deletion beats both.** "ICs cannot spawn agents" doesn't need a test or a rewording; it
  needs deleting, because it is no longer the design.

### 8.4 A qualification the thesis needs for the behavior layer

For CLAUDE.md, "default to cutting" is right. For agent behavior it is the wrong default,
because the measured problem is not excess — it is that two roles get 3.3 KB of behavioral
guidance with no safety content while the same package carries 26 KB for the root, and that the
best operational knowledge in the project is in a single-agent private store. **In this half of
the audit, more of the work is moving and adding than cutting.** The unit of savings here is
not lines deleted from CLAUDE.md; it is incidents not repeated.

---

## 9. Reflection

**Surprising.** (a0) That the `git add -A` ban and the terminology rule sit in the same document,
with the same (absent) CI enforcement, and one has zero violations while the other is violated on
the front page — which means the variable is the *shape of the rule*, not the document it lives
in (§2.5.3). I expected to conclude "prose doesn't work" and the counter-example stopped me.
(a) That the *most accurate* behavior documentation in the repo is the MCP tool
schema — the layer nobody thinks of as documentation — and the least accurate is the file
CLAUDE.md tells you to read first. (b) That the largest duplication I found was not between
documents but between Go string constants in a single package, each pinned by a golden test, so
the maintenance cost is multiplied rather than merely doubled. (c) That researcher and QA — the
two roles whose whole output is a judgment — get the least context with which to judge.
(d) That `testing-practices` is 1.7× CLAUDE.md by *words* (18,225 vs 10,577), not the 2.5×
its line count suggests; CLAUDE.md's lines are much longer.

**Open questions I could not close.**
1. Is the skills layer actually *used*? I can show it is discoverable only via CLAUDE.md prose
   (§1.5) but I have no telemetry on Skill-tool invocations. `.sprawl/logs/mcp-calls.jsonl`
   would not contain them (Skill is not MCP); the session `*.ndjson` wire logs might. This is
   the single most decision-relevant unknown for Part 2 — a skill nobody loads should be cut,
   not consolidated, and I cannot currently tell those two cases apart.
2. Whether `tester` and `code-merger` in `ValidTypes` are reachable at all — they are absent
   from `SupportedTypes` and from the `spawn` enum. Probably dead, but proving it needs the
   spawn path traced.
3. Whether the 36 `_test.go` companion violations (§2.5.2) are a real gap or an artifact of the
   rule meaning per-package rather than per-file. This needs a human decision on the rule's
   intent, not more grepping — and it must be made before the check in §2.5.4 can be written.
4. `## Running Tests` in `testing-practices` teaches an invocation CLAUDE.md explicitly calls
   "NOT what validate uses". I recommend the fix but did not verify that `-race` on the
   whole-suite invocation is appropriate for the ad-hoc single-package case the section is
   actually about; the disk constraint barred measuring it.

**What I would do next, in order.** (1) Fix the two one-line researcher-prompt defects in §1.2
— they are silently losing researcher output today. (2) Extend the safety section to researcher
and QA (§1.1). (3) Answer open question 1 before touching the skills layer at all.

---

*Prepared read-only. No production code, `CLAUDE.md`, `.claude/skills/`, or `docs/` content
outside this file was modified. No Linear issue was created or updated, per the brief.*
