# Sprawl v2: Log-Centric Rearchitecture — Plan of Record

**Date:** 2026-08-18
**Status:** design-only (approved; milestones ship independently — check Linear for current per-milestone progress, not this doc)

> This is a repo-safe copy of the Linear plan-of-record document for the v2
> milestone (`QUM-1247`..`QUM-1256`, tracked under the "v2: Log-Centric
> Rearchitecture" project milestone; this doc's own issue is `QUM-1248`). It
> exists so an agent working a v2 issue finds an in-tree pointer instead of
> nothing, or worse, only the now-partially-superseded
> [hub design](hub/README.md) (see "Relationship to the hub design" below and
> `hub/README.md`'s status block). The Linear document is canonical; this file
> is a snapshot taken at approval time and is not automatically kept in sync —
> if the two disagree, treat that as a sign this file needs a refresh, not as
> a tiebreak in this file's favor.

Approved 2026-08-18 by dmotles after a full planning cycle: codebase
exploration, external platform canvass, three competing data-layer proposals,
and a three-lens adversarial review (distributed-systems, migration-reality,
workflow-semantics; 41 findings). All user decisions are recorded inline. This
document is the source of truth for the QUM v2 milestone issues; each issue
links back to the Linear original.

## Context

Sprawl works but seven problems compound: rampant agent over-engineering
(evidence points at sprawl's own built-in prompts); agent types too coarse;
task tracking dies with each Claude session; state is machine-local (host
migration pain); cost out of control (team-plan overages, opus
over-assigned); no eval/benchmark loop; no cross-host view. Plus a confirmed
bug inflating reported cost 4–10x.

The redesign re-centers sprawl on a **durable append-only event log in one
shared bare Postgres** (chosen over Supabase-composed and NATS event-native
alternatives), with **versioned definitions** (agent cards, workflow
definitions, event-type schemas), an **engine-driven workflow layer** with
explicit open/close event contracts, a **log-fed temporal memory system**,
and an **eval/replay harness**. Evolve the sprawl repo in place; keep
wire-protocol, supervisor, worktree machinery, TUI, e2e harness.

## The target model

### Projects & bootstrap

* Identity = one Postgres connection string (`SPRAWL_DB_DSN` env or
  `~/.config/sprawl/secrets.yaml`, 0600 — never in-repo). No required local
  state beyond cache/spill.
* Project = repo remote URL (unique key; temp name if unset, renameable). Own
  namespace in log + memory; cross-project queries allowed. First enable
  emits `repo_initialized` (git SHA + metadata).

### Event contracts (the heart)

* Typed events with **open/close contracts**: a GOAL stays open until a
  RESULT closes it (`closes_event_id`). Outstanding work is derived
  deterministically; a sweeper detects stalls and re-pokes — replacing
  Claude's in-session task tracker, surviving 529s/crashes/session loss.
* **Closes are final; the log is monotone.** RESULT events carry
  `outcome: success|failure|aborted|superseded`. Defects caught after close
  emit `REWORK_REQUESTED` referencing the closed goal, opening a new
  contract linked by `follows_event_id` (artifact chain inherited).
  **Re-engagement policy is workflow-defined per goal type**: re-engage the
  original agent with the miss pointed out (updated result), or
  discard-and-redo on a stronger model. Cancel = system `GOAL_CANCELLED`
  close, transitive to open children; redirect = cancel + new goal, never
  mutation.
* `workflow_id` correlates events into instances. **Goal owner = creating
  agent**, notified when the result lands; notification is itself an
  open/close pair (`OWNER_NOTIFY`/`NOTIFY_ACKED` at turn boundary) so lost
  notifications are swept, not assumed.
* ASK_QUESTIONS/ANSWER_QUESTIONS sub-protocol (follow-ups carry
  `follow_up_of`; later answers may supersede earlier ones append-only).
* **The human sits above the system**: an `ask_user` tool (managers/high-level
  cards only) dispatches to a first-class **user inbox** (open
  USER_QUESTION contracts; sweeper never pokes human-blocked waits). The
  human may interject into *any* agent directly; interjections are recorded
  as events so the log stays complete. weave is not a proxy/relay.
* Thin events (≤ ~8KB, schema-validated) reference fat **artifacts**
  (reports, plans, diffs + base git SHA), content-addressed (sha256 dedup).
* Agents get narrow tools (`reread_my_goal`, `get_workflow_log`,
  `report_result`, `ask_questions`, `search_log`, `search_memory`), never raw
  SQL.
* Long-term: docs/knowledge drift out of committed repo files into
  log/memory.

### Goal/workflow taxonomy

* **Root (weave)**: pure orchestrator. Classifies user requests into goal
  types via a documented decision table in its card (overlaps resolved by
  *purpose*; misclassification mid-flight → close `superseded` + reopen
  correctly typed, artifacts carried). Can run ~5 initiatives concurrently.
* **Pointed goals** (one controlling agent, ≤1 tier of **sprawl sub-agents**
  — worktree-sharing; enforced by spawn refusing calls from sub-agent
  identities; Claude sidechains unlimited but strippable per card): RESEARCH
  (sonnet), BUG_INVESTIGATION (opus; trace → reproduce/confidence → or
  candidates+proposals; promotion to a change goal is an explicit owner
  action carrying `derived_from_event_id` + artifacts), DOC_UPDATE (sonnet,
  style rules in card), BUILD_DEV_SCRIPTS_UPDATE, CI_CD_UPDATE,
  DEPLOYMENT_CHANGE (IaC), BACKEND_CODE_CHANGE (full TDD), CLI_CODE_CHANGE,
  FRONTEND_CODE_CHANGE, CODE_CLEANUP_CHANGE (deletion + existing coverage).
  Each carries its own validation philosophy — fixes the TDD-everything
  misfit.
* **Management workflows** (manager cards, opus+): STRATEGIC_PIVOT
  (goal-def→research→design→planning→N changes→review), NEW_FEATURE
  (design→planning→N changes→review), REFACTOR, CLEANUP
  (research→planning→N changes→review). Step outcomes are an enum — success
  / retry / backtrack-to(step) / escalate / abort — with per-step
  max-retry; escalation = ASK_QUESTIONS up the owner chain, terminating at
  the human. Multi-surface features fan out only via management workflows;
  change type is per child goal.
* **Integration**: each management workflow instance gets an integration
  branch owned by a dedicated **integrator card**, merging child branches in
  dependency order via `sprawl merge`; unresolvable conflicts become
  REWORK/CODE_CHANGE goals.
* **Pointed task agents** (workflow steps = full worktree agents, mergeable;
  names deconflicted from sidechain names): planner (opus/fable), researcher
  (sonnet), designer (opus/fable), test-writer (sonnet), test-critic
  (sonnet), implementer (sonnet/opus), code-reviewer (sonnet/opus),
  commit-writer (haiku), QA (sonnet), deployer (sonnet), debugger (opus),
  integrator, security/perf reviewers (opus). Model defaults are hypotheses
  — the eval harness decides.

### Cost governance

* **Per-instance budgets**: every workflow instance gets a token/$ budget at
  open (default per workflow type); exceeded → sweeper pauses the instance
  and asks the human (user inbox).
* **Global daily ceiling**: new goal admission queues (priority-ordered) when
  hit. Both enforced from `agent_runs` data.
* Poke budgets: per-goal sweep count with exponential backoff; cap →
  `GOAL_STUCK` escalation to human + quarantine (dead-letter), never
  infinite re-poke.

### Controlled flexibility

Definitions static in normal operation. Tuning: user + helper agent author
new card/workflow/event-schema *versions* (immutable rows), test against a
point-in-time replay, then promote. Agents only *propose* (log events);
humans publish. Every published card passes **card-lint** (the safety
invariants — injection escalation, guardrails, deleted-tool-surface —
enforced on every version, not just seeds).

## Data layer (bare Postgres ≥16 + pgvector; nothing vendor-bound)

Host: Azure Database for PostgreSQL Flexible Server B1ms (~$15–20/mo; step up
to B2s when HNSW needs more RAM) — named as a generic public-cloud target per
this repo's hub-docs convention, not as the maintainer's specific instance
(no subscription/tenant/resource identifiers here). Neon's free tier is
documented as the option for OSS contributors without their own instance.

* Definitions ×3 (`event_type_schemas` — additive-only within a name,
  breaking change = new event type name; `agent_cards`; `workflow_defs`).
  Identity `(name, version)`, immutable, **referenced by id not name**;
  `sprawl def bump` walks the dep graph; appenders validate against the
  *pinned* schema_id carried on the emit call, never "latest".
* Append-only `events` enforced by grants; content-addressed `artifacts`
  (>2MB object-storage spill is a permitted later escape hatch);
  `workflow_instances`, `agent_sessions`. Full DDL sketch: Appendix A.
* **Open/close**: anti-join + `open_contracts` projection maintained in the
  appender txn; drop/rebuild-equality test required.
* **Dispatch — event-level claims (user decision)**: consumers claim events
  via `INSERT … ON CONFLICT DO NOTHING` on `(event_id, consumer)` with lease
  timeout — exactly-one actor per event, idempotent under crash/redelivery.
  Events/steps carry **host affinity** for worktree-bound work; unaffined
  events claimable by any host. `SPAWN_INTENT` write-ahead before any local
  resource creation; host startup reconciles local AgentState ↔ intents
  (adopt orphans, GC strays, emit SPAWN_FAILED).
* **Ordering**: `pg_advisory_xact_lock` (auto-released) with payload
  validation *before* the lock; NOTIFY is doorbell-only — dispatcher
  correctness = seq-cursor catch-up + 2–5s poll (test with LISTEN disabled).
* **Liveness**: derived from turn-boundary events (run-started/turn-finished
  via the runtime EventBus), not wall-clock heartbeats. `agent_sessions` is
  an advisory *projection*; the local AgentState taxonomy stays the sole
  wake arbiter — sweeper pokes route through the existing wake path, **never
  poke InTurn or operator-Paused agents**; an owner blocked on an open child
  contract/question is not stalled. Owner dead → reassign to workflow
  engine/root explicitly.
* **Offline = degraded mode (user decision)**: only telemetry/lifecycle
  events spill (gitignore-verified dir, retention defined, embedded seed
  schemas validate at spill time, dead-letter on replay failures);
  goal-open/close and dispatch **fail loudly** while the DB is down —
  running agents continue on local state, no new coordination starts. Agents
  never brick.
* **Notification delivery (user decision)**: the event log IS the
  notification queue. The dispatcher injects event payloads into agent
  streams by reusing the existing hardened injection mechanics (drain path),
  with per-(event, recipient) delivery tracking in the DB. The on-disk
  `.sprawl` maildir storage is retired once parity is proven; agents re-read
  context via `get_workflow_log`.
* **Semantic search**: `event_embeddings` HNSW, async embedder (another
  cursor consumer), pluggable backend (hosted small model default, Ollama
  for dev), hybrid vector+tsvector. Whole-log embedding ≈ $5–25 one-time.

## Memory (same DB, log-fed)

* Log = episodic; memory = semantic beliefs: `entities`,
  `facts(valid_from, valid_to, confidence, superseded_by)`,
  `fact_provenance(fact_id, event_id)` — Graphiti-style
  supersede-don't-delete; "what did we believe at T" is a query.
* Extractor = cursor consumer over distillation-rich event types;
  haiku-class extract→dedup→invalidate→insert; rebuildable by cursor reset.
* Delivery: spawn-time injection (hybrid retrieval + 1-hop graph expansion,
  token budget per card) + `search_memory`/`expand_entity` tools;
  MEMORY_HYGIENE goal for curation.
* **Transition (user decision): dual-write then replace** — from M1, weave
  handoff summaries also emit as log events; at M6 the DB takes over
  spawn-time injection and file memory (`internal/memory`) is frozen.

## Buy vs. build (canvassed 2026-08-18)

* No framework replaces sprawl (all own the agent loop; none treats a Claude
  Code session in a worktree as the execution unit). **DBOS spike**
  (`dbos-transact-golang`) for durable workflow execution — exit criteria:
  step side-effect + close-event append must be one transaction in our
  Postgres, AND a demonstrated *backtrack*, not just crash-resume; otherwise
  thin hand-rolled engine over open_contracts. Temporal only if
  multi-host-heavy.
* Eval/observability: OTel GenAI spans → Langfuse Cloud (free tier, OTLP,
  judge tooling, self-host escape). `agent_runs` is the system of record.
* Cheap models: gateway-agnostic (`ANTHROPIC_BASE_URL/AUTH_TOKEN/MODEL` per
  card). GLM/Kimi/Haiku hold up for mechanical work. LiteLLM default (pin
  vetted version; PyPI 1.82.7/.8 shipped malware — pin around those
  releases specifically). Anthropic-via-Foundry/Bedrock = same list price,
  different invoice. Foundry Agent Service: not a fit for coding agents.
* Messaging: Postgres; NATS is the someday step-up. No managed memory SaaS
  (employer context stays in our DB).

## Relationship to the hub design (`docs/designs/hub/`)

> **Note on terminology:** this section's "v2" is *this* document's v2 (the
> sprawl log-centric rearchitecture). The hub docs use "v2" for their own,
> unrelated second design iteration (single-user, post-multi-tenant cut) —
> `13-implementation-plan.md` and `QUM-913` both use "v2" that way. The two
> are not the same v2.

The sprawl v2 event log absorbs the hub's storage half (docs 00/01/07/09/10 —
durable seq'd stream, storage/persistence, portable memory) and inverts its
philosophy: the shared Postgres IS the authoritative coordination spine, not
an optional companion. The hub's UI half (browser relay, remote view+input,
attachments, auth boundary — docs 03/04/11, attachments-multimodal) remains
valid future work as a thin view layer over this database. See
`docs/designs/hub/README.md`'s status block for the doc-by-doc
superseded/retargeted breakdown (`QUM-1248`). No issue currently tracks
aligning the hub docs' own prose to this (sprawl v2) model — `QUM-913` is a
different, unrelated hygiene ticket about the hub's own v2 iteration; do not
follow it expecting sprawl-v2 content.

## Milestones

M0 (cost fix) → M1a (store core) → M1b (dispatch) → M2 (definitions/cards) →
M3a (engine + 2 goals) → M3b (taxonomy + mgmt workflows) → M4 (search/eval) →
M5 (cheap tier) → M6 (memory). Each is an issue in the Sprawl Linear project
carrying its full implementation detail; dependencies are wired in Linear.

| Milestone | Issue |
|---|---|
| M0 — cost fix | `QUM-1247` |
| M1a — store core | `QUM-1249` |
| M1b — dispatch | `QUM-1250` |
| M2 — definitions/cards | `QUM-1251` |
| M3a — engine + 2 goals | `QUM-1252` |
| M3b — taxonomy + mgmt workflows | `QUM-1253` |
| M4 — search/eval | `QUM-1254` |
| M5 — cheap tier | `QUM-1255` |
| M6 — memory | `QUM-1256` |

The milestone→issue-key mapping above is durable (keys don't change); status
is deliberately not tabulated here because it goes stale within a day — check
Linear (project "Sprawl", milestone "v2: Log-Centric Rearchitecture") for
current per-milestone status.

## Cross-cutting requirements (apply to every milestone)

* DB-unreachable must never brick an agent (degraded mode + embedded seeds +
  local AgentState authoritative for runtime lifecycle); test unreachable
  paths every milestone.
* Public-repo hygiene: DSN/keys only in the secrets file; spill dir
  gitignore-verified; employer-context events in DB, never the tracked tree.
* Repo gates: every assertion ships a demonstrated-can-fail control (per
  /testing-practices); `make validate`; owed e2e matrix rows derived per
  diff (per /e2e-matrix); smoke-test `./sprawl`; TUI changes trigger
  /tui-testing.

---

## Appendix A — Data-layer DDL sketch (from the accepted proposal)

```sql
CREATE TABLE projects (
  id uuid PRIMARY KEY, remote_url text UNIQUE NOT NULL, created_at timestamptz);

-- Immutable definitions: one pattern, three tables. (name, version) is the identity.
CREATE TABLE event_type_schemas (
  id uuid PRIMARY KEY, name text NOT NULL, version int NOT NULL,
  json_schema jsonb NOT NULL,           -- required payload fields
  closes text NULL,                     -- name of the opener this type closes
  opens boolean NOT NULL DEFAULT false,
  supersedes uuid REFERENCES event_type_schemas(id),
  UNIQUE (name, version));

CREATE TABLE agent_cards (
  id uuid PRIMARY KEY, name text, version int, prompt text,
  tools jsonb, model text, effort text, rubric jsonb, budgets jsonb,
  UNIQUE (name, version));

CREATE TABLE workflow_defs (
  id uuid PRIMARY KEY, name text, version int,
  trigger_event_schema uuid REFERENCES event_type_schemas(id),
  steps jsonb NOT NULL,   -- ordered [{step, card_id, emits:[schema_id], outcome policy: retry/backtrack/escalate/abort, re-engagement policy}]
  UNIQUE (name, version));

CREATE TABLE events (
  seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,  -- global total order
  id uuid UNIQUE NOT NULL,
  project_id uuid NOT NULL REFERENCES projects,
  workflow_instance_id uuid NOT NULL,
  schema_id uuid NOT NULL REFERENCES event_type_schemas,
  agent_session_id uuid,                -- emitter
  owner_agent_id uuid,                  -- for GOAL: who to notify on close
  closes_event_id uuid REFERENCES events(id),  -- RESULT→GOAL, ANSWER→ASK
  payload jsonb NOT NULL,               -- small, schema-validated; ≤ ~8KB by policy
  artifact_id uuid REFERENCES artifacts(id),
  at timestamptz NOT NULL DEFAULT now());
CREATE INDEX ON events (workflow_instance_id, seq);
CREATE INDEX ON events (project_id, seq);

CREATE TABLE artifacts (
  id uuid PRIMARY KEY,
  kind text NOT NULL,               -- 'goal_text' | 'report' | 'diff' | 'spawn_context' | ...
  content text NOT NULL,
  base_git_sha text, size_bytes int,
  sha256 bytea UNIQUE);             -- content-addressed dedupe

CREATE TABLE workflow_instances (
  id uuid PRIMARY KEY, project_id uuid, workflow_def_id uuid REFERENCES workflow_defs,
  triggered_by_event uuid, started_at timestamptz, closed_at timestamptz NULL,
  budget_tokens bigint, budget_usd numeric);

CREATE TABLE agent_sessions (   -- ADVISORY PROJECTION; local AgentState is the wake arbiter
  id uuid PRIMARY KEY, card_id uuid REFERENCES agent_cards, host text,
  last_turn_boundary timestamptz, status text);

-- Derived, rebuildable projection (insert on open, delete on close, same txn as append)
CREATE TABLE open_contracts (
  event_id uuid PRIMARY KEY, owner_agent_id uuid,
  workflow_instance_id uuid, opened_at timestamptz);

-- Dispatch claims: exactly-one actor per event
CREATE TABLE event_claims (
  event_id uuid, consumer text, host text, claimed_at timestamptz,
  lease_expires timestamptz, PRIMARY KEY (event_id, consumer));

CREATE TABLE event_embeddings (
  event_id uuid REFERENCES events(id), chunk int NOT NULL DEFAULT 0,
  embedding vector(1024), model text NOT NULL,
  PRIMARY KEY (event_id, chunk));
CREATE INDEX ON event_embeddings USING hnsw (embedding vector_cosine_ops);

-- Memory (M6)
CREATE TABLE entities (id uuid PRIMARY KEY, project_id uuid, kind text, name text,
  summary text, embedding vector(1024));
CREATE TABLE facts (id uuid PRIMARY KEY, subject_entity uuid, predicate text,
  object_entity uuid NULL, object_value text NULL,
  valid_from timestamptz, valid_to timestamptz NULL,
  confidence real, superseded_by uuid NULL, embedding vector(1024));
CREATE TABLE fact_provenance (fact_id uuid, event_id uuid, PRIMARY KEY (fact_id, event_id));
```

Open-goals query (correctness reference for the projection):

```sql
SELECT g.* FROM events g
JOIN event_type_schemas s ON s.id = g.schema_id AND s.opens
LEFT JOIN events c ON c.closes_event_id = g.id
WHERE c.id IS NULL AND g.project_id = $1;
```

## Appendix B — Key hardening mechanisms (from adversarial review; binding on implementers)

 1. **Idempotent dispatch**: every side-effecting consumption goes through
    `event_claims` (`INSERT ON CONFLICT DO NOTHING` + lease). Crash-and-redeliver
    must never double-spawn.
 2. **SPAWN_INTENT write-ahead**: append intent (under the claim) BEFORE
    creating worktree/branch/session; startup reconciliation adopts orphans
    matching intents, GCs strays, emits SPAWN_FAILED for intents with no
    local trace.
 3. **Poke discipline**: pokes are conditional inserts keyed
    `(goal_event_id, sweep_epoch)`; per-goal exponential backoff; cap →
    GOAL_STUCK + quarantine. Sweeper is transitive-block-aware and never
    pokes InTurn/operator-Paused agents or human-owned waits.
 4. **Liveness from turn boundaries**, not heartbeats (the supervisor
    heartbeat was deleted in `QUM-730`/`QUM-1071`; long quiet turns are
    legitimate).
 5. **NOTIFY is latency only**: all dispatcher tests must pass with LISTEN
    disabled.
 6. **Version pinning end-to-end**: emit calls carry pinned schema_id;
    appender validates against it; `def bump` refuses breaking bumps that
    in-flight instances reference; event schemas additive-only within a name.
 7. **Advisory lock**: `pg_advisory_xact_lock` only (auto-released); validate
    payload before taking it.
 8. **open_contracts** must have a drop→rebuild→assert-equal-to-anti-join
    test.
 9. **spawn_context artifact**: every spawn records the fully rendered
    prompt + artifact ids + memory block + card version; bench replay uses
    it and discloses post-spawn-message contamination.
10. **Legacy control arm**: today's prompts registered verbatim as
    `legacy-*@1` cards before slimming, so every run has a card_version_id
    and A/Bs have a baseline.
11. **Card-lint on publish**: safety invariants (injection escalation,
    action-care guardrails, deleted-tool-surface absence) enforced on every
    published card version — publish must not bypass what the prompt tests
    used to pin.
12. **Coexistence visibility**: legacy prose-driven spawns emit paired
    `legacy: true` open/close lifecycle events so the outstanding-work view
    is complete during migration.
13. **DBOS spike exit criteria**: step side-effect + close-event append in
    ONE Postgres transaction, and a demonstrated backtrack — else reject
    DBOS for the thin engine.
14. **Embedded seed schemas**: event-type schema seeds compiled into the
    binary so degraded-mode validation works without the DB; replay
    failures dead-letter, never silently drop.

## Open Questions

* Exact hosted-Postgres provider and region for the maintainer's own
  instance (deliberately not named here — see "Public vs private repo
  hygiene" in `CLAUDE.md`; provider specifics belong in the private secrets
  file, not this tree).
* Whether the DBOS spike (Appendix B, item 13) passes its exit criteria, and
  what the fallback hand-rolled engine's minimal feature set is if it does
  not.
* Final model assignment per pointed-task-agent role (Goal/workflow
  taxonomy) is explicitly a hypothesis pending the eval/replay harness
  (M4) — do not treat the models listed above as locked.
