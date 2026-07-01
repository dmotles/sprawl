# 10 — Portable Memory

*The portable per-`(project, agent)` memory model: one single-writer stream per
agent, provenance on every unit, curation/synthesis instead of textual merge, and
version-vector sync over the event-log spine.*

See also: [`01-architecture.md`](01-architecture.md) (§4 identity/lease/fence, §5
memory) · [`09-synchronization`](09-synchronization.md) · [security-privacy](README.md) ·
[index](README.md)

---

## 1. Problem: memory is trapped

Today weave's memory is **local, untracked, and single-agent**. On disk under
`.sprawl/memory/`:

```
.sprawl/memory/
├── persistent.md              # distilled, curated facts/conventions/preferences
├── timeline.md                # session index: date | session-id | one-line summary
├── sessions/<session-id>.md   # per-session handoff (YAML frontmatter + prose)
├── last-session-id            # pointer to the most recent session
└── weave.lock / .consolidating# local single-writer + consolidation guards
```

It never leaves the machine. Switch hosts and the context is gone
([`00-overview`](00-overview.md#problem--why)). "Combining" two machines' memory
by hand is error-prone, and the obvious reflex — put it in git and merge — is
actively wrong (§4).

The hub makes this memory **portable** by streaming it through the same
event-log spine + durable store as everything else, keyed so that each stream has
exactly one writer.

## 2. One stream per `(project, agent)`

**The agent name is the partition key.** Each logical memory stream is addressed
by `(project, agent)` — e.g. `(sprawl, weave)`, `(sprawl, recon)`. weave is not
special; it is simply agent `weave`.

```
project = sprawl
├── (sprawl, weave)   ── memory stream ──▶ single writer: the weave process
├── (sprawl, recon)   ── memory stream ──▶ single writer: the recon process
└── (sprawl, finn)    ── memory stream ──▶ single writer: the finn process
```

### Single writer by construction

Agent names are **unique across the org** (allocated from the name pool, set as
`SPRAWL_AGENT_IDENTITY` — see [`DESCRIPTION.md`](../../../DESCRIPTION.md) "Agent
Identity"). Because the name *is* the partition key, **each memory stream has
exactly one writer by construction**. There is no write-contention on memory and
therefore no need for a per-stream memory lock or merge algorithm in the common
case.

> This is a different guarantee from the **project write-lease** in
> [`01`](01-architecture.md#4-identity-lease--fencing-conceptual--detail-in-doc-10--09).
> That lease arbitrates *which host* may write on behalf of a project when the
> *same agent* runs from two machines. Name-uniqueness removes cross-*agent*
> contention; the lease removes cross-*host* contention for one agent. Both are
> needed; they solve different axes (§6).

### Simplest way vs. right way — partition granularity

- **Simplest:** one memory blob per project (today's `weave`-only model, just
  hosted). Cost: can't attribute or carry any non-weave agent's memory; forecloses
  the north-star "each agent has its own memory"
  ([`00`](00-overview.md#north-star-vision--not-committed--future)).
- **Right:** partition by `(project, agent)` from day one.
- **Recommendation:** **partition by `(project, agent)` now.** It is a keying
  decision, essentially free to adopt up front and painful to retrofit. In v1
  only `weave` actually writes (matching today), but the schema and storage layout
  already admit every agent — so persistent per-agent memory needs no migration.

## 3. What a memory unit is

A **memory unit** is one durable, self-contained record in a stream. Concretely
it maps onto today's artifacts:

| Unit kind | Today's artifact | Notes |
|---|---|---|
| `session_handoff` | `sessions/<id>.md` | Per-session summary; append-only |
| `distilled_fact` | a line/block in `persistent.md` | Curated knowledge; supersedable |
| `timeline_entry` | a line in `timeline.md` | Index; derivable from handoffs |

The stream is **append-mostly**: new units are appended; curation *supersedes*
prior units rather than editing them in place (§4). This is exactly the
append-only-log-plus-snapshot shape the spine already uses
([`01` §2](01-architecture.md#2-the-event-log-spine-the-strongest-idea--feature-it)) —
memory units *are* events on the per-`(project, agent)` stream.

### Provenance metadata (minimal, on every unit)

Every unit carries provenance so that combining streams is a *semantic* operation,
never a blind concatenation:

```yaml
# provenance header on every memory unit
agent:       weave            # partition key / author
host_id:     host-7f3a        # stable per machine/install (opaque, §5)
run_id:      run-9c21         # per `sprawl enter` process (§5)
created_at:  2026-07-01T05:39:10Z
source:      session          # session | injected | synthesized
session_id:  cc004dc1-…       # originating claude session (when source=session)
supersedes:  [unit-id, …]     # optional: units this curation replaces
```

- `source: session` — emitted from a live session handoff (the common case).
- `source: injected` — added out-of-band (e.g. a user/browser note via the hub).
- `source: synthesized` — produced by an agent-synthesis/consolidation pass
  (today's `consolidate.go` / `regenerate.go` lineage), which reads many units and
  writes a new distilled one that `supersedes` them.

Provenance is the minimum needed to (a) attribute every unit, (b) order and
de-duplicate across machines by `(host_id, run_id, created_at)`, and (c) drive
semantic reconcile after a force-reclaim (§6). Keep it minimal — resist adding
fields until a consumer needs them (YAGNI).

> **Public-repo hygiene.** `host_id` / `run_id` are **opaque, generated
> identifiers** — never a hostname, username, MAC, or machine description.
> Memory *content* can contain sensitive context; the trust/redaction model lives
> in [security-privacy](README.md). This doc governs *structure*, not content
> policy.

## 4. Combining memory = curation or synthesis, NEVER textual merge

This is the load-bearing rule. **When two versions of a stream diverge, we do not
line-merge them.** We either (a) keep both units and let a **curation/synthesis**
pass produce a new distilled unit, or (b) pick a winner by provenance. Git-style
three-way text merge is explicitly rejected.

### Why not git

- **Line-merge of prose produces incoherent Frankentext.** Memory is distilled
  natural-language knowledge, not code. Interleaving two independently-edited
  versions of "what dmotles prefers" yields self-contradictory, half-sentence
  garbage that is worse than either input — and silently so.
- **Conflict markers are meaningless here.** `<<<<<<<`/`>>>>>>>` around prose
  gives a human (or agent) no principled way to resolve; the "right" answer is a
  *rewrite*, not a hunk selection.
- **The unit of meaning isn't the line.** A fact spans sentences and depends on
  surrounding context; git's line granularity cuts across meaning.
- **We already synthesize.** The existing consolidation/regeneration path
  (`internal/memory/{consolidate,regenerate,arc}.go`) *already* combines memory by
  having Claude read and rewrite — that is the correct primitive, and it composes
  naturally with provenance (`source: synthesized`, `supersedes: [...]`).

### The rule

> **To combine memory, an agent reads the union of units and writes a new
> synthesized unit** (`source: synthesized`) that supersedes the inputs — OR a
> human/agent curates by choosing which units survive. **Never** a textual merge.

Divergence severe enough to *need* combining is only reachable via **force-reclaim**
(§6); the normal path (single writer + push/pull) never diverges. Reconcile
mechanics live in [`09-synchronization`](09-synchronization.md); this doc owns the *why-not-git*
rationale and the curation/synthesis contract.

### Simplest way vs. right way — divergence handling

- **Simplest:** last-writer-wins on the whole stream (newest `created_at` +
  fence wins; drop the loser). Cost: silently discards real knowledge written on
  the other machine.
- **Right:** retain both divergent unit-sets and enqueue a synthesis pass that
  produces a superseding distilled unit.
- **Recommendation:** **LWW for the pointer/head, retain-both for the losing
  units.** The stream head advances to the fence-winner immediately (no stall),
  but the superseded/divergent units are **kept in versioned storage** (§5) and a
  `source: synthesized` reconcile pass folds them back in. Cheap to operate, loses
  nothing, and defers the (rare) hard case to an async synthesis instead of a
  blocking merge UI.

## 5. Versioned object-storage layout

Each `(project, agent)` stream is stored as versioned objects in the hub's blob
store (`gocloud.dev/blob`, per [`01` §7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs);
`fileblob`/`memblob` for local/tests → [`12-testability-local-dev`](README.md)).
Postgres holds the stream **index** (heads, version vectors, provenance for query);
blobs hold the unit bodies and snapshots. Layout is conceptual — exact keys are a
storage-doc concern ([`07-storage-persistence`](README.md)):

```
blob://<project>/<agent>/
├── units/<unit-id>.md            # individual memory units (immutable once written)
├── snapshots/<seq>.md            # periodic compacted view (persistent.md-equivalent)
├── head                          # current head seq + version vector
└── transcripts/<session-id>/…    # FULL session transcripts, retained (see below)
```

- **Immutable units + periodic snapshots** mirror the spine's
  append-log-plus-snapshot compaction ([`01` §2](01-architecture.md#simplest-way-vs-right-way)).
  A snapshot is the "distilled current state" (today's `persistent.md`);
  delta-replay from a snapshot is what makes a new host's pull cheap.
- **Versioned** = old snapshots and superseded units are retained (subject to
  retention/GC in [`07`](README.md)), which is what makes retain-both reconcile
  (§4) and rollback possible.

### Full session transcripts are retained

Beyond distilled memory, the hub retains **full session transcripts** per
`(project, agent, session_id)`. In v1 they are *write-and-store only* — nothing
reads them yet. They exist so these **FUTURE consumers** become possible without a
data-model change (all explicitly *not v1*):

- a **memory/session console** for browsing & curating agent memory
  ([`00` north-star](00-overview.md#north-star-vision--not-committed--future));
- **review** — replaying what an agent actually did;
- **search** across historical sessions;
- **memory-eval** — scoring/regression-testing the distillation pipeline against
  ground-truth transcripts.

Naming them as future consumers (not building them) is deliberate: it keeps the
storage shape honest without pulling scope into the MVP.

### Simplest way vs. right way — transcript retention

- **Simplest:** don't store transcripts; keep only distilled memory. Cost: the
  console/review/search/eval features are impossible later without recapturing
  data we'll never have again — the raw signal is gone.
- **Right:** stream transcripts to blob storage from day one, gated by retention.
- **Recommendation:** **retain transcripts, opt-in + retention-bounded.** They're
  cheap in blob storage and irreplaceable after the fact. Gate behind a retention
  window ([`07`](README.md)) and the privacy/redaction model
  ([security-privacy](README.md)) so cost and exposure stay bounded. This is a
  YAGNI-respecting exception: we don't build the *consumers*, we only avoid
  throwing away their fuel.

## 6. Cross-machine identity, lease & sync

### Identity

Two identifiers, reused verbatim from [`01` §4](01-architecture.md#4-identity-lease--fencing-conceptual--detail-in-doc-10--09):

| ID | Scope | Role in memory |
|---|---|---|
| `host_id` | Stable per machine/install | Which physical origin wrote a unit |
| `run_id` | Per `sprawl enter` process | Which live session instance wrote it |

Both are opaque generated values (§3 hygiene note). Together with `created_at`
they totally order and de-duplicate units across machines.

### Write-lease + fencing (per project, memory rides it)

Memory writes are **guarded by the per-project write-lease + fence token** already
defined in [`01` §4](01-architecture.md#4-identity-lease--fencing-conceptual--detail-in-doc-10--09) —
memory does **not** invent its own lock. Every memory write carries the current
fence token; the hub **rejects writes bearing a stale fence**, so a returning
zombie host cannot clobber a stream:

```
host holds lease (fence=N) ──▶ memory write(fence=N)  ─▶ accepted, appended
zombie host returns (fence<N) ─▶ memory write(fence<N) ─▶ REJECTED (stale fence)
lease TTL expires ────────────▶ next claimant gets fence=N+1
```

Name-uniqueness (§2) means *different agents* never contend. The lease/fence
handles the one residual case: the *same agent* (e.g. `weave`) run from two
machines. v1 keys the lease **per-project** (schema anticipates per-agent, enforces
per-project — matching [`01` §8](01-architecture.md#8-what-v1-deliberately-excludes-yagni)).

### Version-vector catch-up on reconnect

On (re)connect, host and hub compare **version vectors** for the stream and follow
the spine's one rule ([`01` §2](01-architecture.md#the-one-rule-written-once-reused-at-every-seam)) —
replay-from-last-seq, else snapshot, then live-tail:

```
on (re)connect, per (project, agent) stream:
  compare version vectors {host_vv} vs {hub_vv}
  local ahead + host holds lease → PUSH local delta up   (units since hub_vv)
  hub ahead                      → PULL hub delta down    (units since host_vv)
  genuine divergence             → only via FORCE-RECLAIM → provenance reconcile (§4)
```

- **local ahead + holds lease → push** the missing units.
- **hub ahead → pull** (a **new host with no local memory is just the extreme
  case**: empty version vector → pull the latest snapshot + tail = "new host pulls
  latest").
- **genuine divergence** is *only* reachable after a force-reclaim (two machines
  wrote the same stream while both believed they held authority). It is resolved by
  **provenance-based semantic reconcile / synthesis (§4)** — never textual merge.

Detailed vector mechanics, lease-flow diagrams, and force-reclaim reconcile live in
[`09-synchronization`](09-synchronization.md); this doc specifies the *memory* payload those
flows carry.

### Connected vs. disconnected

Consistent with [`01` §3](01-architecture.md#3-connected-vs-disconnected), memory
is **disconnected-by-default**:

| Aspect | Disconnected (default) | Connected |
|---|---|---|
| Where memory lives | Local `.sprawl/memory/` (today) | Local **+** hub blob store |
| Writes | Local only | Local, then uplinked (fence-guarded) |
| New host | Starts empty (as today) | Pulls latest snapshot + tail |
| Hub down | Keep writing locally; buffer | Buffer un-acked units, flush on reconnect |

A hub outage never stalls a memory write — the host writes locally and the hub
client flushes buffered units on reconnect (bounded buffer, drop-oldest past
high-water, per [`01` §3](01-architecture.md#simplest-way-vs-right-way-1)).

### The "sync these memories?" prompt

Auto-pushing local memory to a shared hub is a **content-exposure decision**, not a
mechanical one (memory can hold sensitive context — [security-privacy](README.md)).
So the first time a host would upload a stream — and whenever a **force-reclaim**
would fold in divergent remote units — sprawl **prompts the user**:

```
Sync memory for (sprawl, weave) with the hub?
  This uploads N local memory units (last local write: 2026-07-01).
  [Sync]   [Keep local only]   [Review units…]
```

- Default posture is **opt-in**: no silent upload of local memory.
- On force-reclaim divergence, the prompt surfaces *what* diverged (unit counts +
  provenance) before any synthesis pass runs, so the user stays in control of the
  combine.

### Simplest way vs. right way — sync trigger

- **Simplest:** auto-sync every stream on connect, no prompt. Cost: silent
  exposure of possibly-sensitive local memory; surprising overwrites.
- **Right:** explicit per-stream opt-in prompt + review affordance; remember the
  choice per `(project, agent)`.
- **Recommendation:** **prompt once per stream, remember the decision.** One
  interaction, no nagging, and it keeps the exposure decision with the human — the
  right default for a public-repo tool where content sensitivity varies by project.

## 7. How it fits the spine (one picture)

Memory is not a side-channel — units are events on the per-`(project, agent)`
stream, transported by the same uplink/downlink as session events
([`01` §6](01-architecture.md#6-how-the-pieces-fit-requestresponse-paths)):

```
weave session ─▶ memory unit (provenance, fence=N)
              ─▶ local eventbus (seq'd) ─▶ hub client ─▶ Connect uplink
                                                           └─▶ hub append(units/) + index(PG)
                                                                 └─▶ snapshot compaction (periodic)
new host connect ─▶ version-vector compare ─▶ pull snapshot + tail  ("new host pulls latest")
```

Reconnect/replay logic is **inherited** from the spine — memory adds *provenance*,
the *no-textual-merge* reconcile rule, and the *sync prompt*; it does not add new
transport or new reconnect code.

## Open Questions

- **Snapshot cadence for memory streams** — event-count, time, or on-consolidate?
  Distinct from session-event snapshot cadence
  ([`01` OQ](01-architecture.md#open-questions))? Affects new-host pull latency.
- **Version-vector granularity** — per-`(project, agent)` stream is assumed here;
  [`01`](01-architecture.md#open-questions) leaves per-project vs per-stream open.
  Per-stream is the natural fit for single-writer memory — confirm with
  [`09`](README.md).
- **Who runs the synthesis/reconcile pass** — the owning agent on next wake
  (has context, but may be dormant), a dedicated consolidation agent, or a
  hub-side job? Today it's in-process (`consolidate.go`); portability may argue for
  a schedulable pass.
- **Transcript retention window & redaction** — default retention, and how the
  privacy model ([security-privacy](README.md)) redacts transcripts vs. distilled
  units (different sensitivity profiles).
- **`injected` memory authority** — when the browser injects a memory note, whose
  `host_id`/fence does it carry (the hub's? the attached host's?), and does it need
  the lease? Ties into "does the browser ever hold write authority"
  ([`01` OQ](01-architecture.md#open-questions)).
- **Cross-project memory** — user-level preferences (e.g. "dmotles prefers KISS")
  currently live per-project in `persistent.md`. Should there be a
  `(user, *)` / global stream, or does that over-generalize v1? (YAGNI-flagged.)
- **De-dup key stability** — is `(host_id, run_id, created_at)` sufficient to
  de-duplicate replayed units, or do units need their own stable content hash /
  unit-id to survive re-uploads after a buffer flush?
