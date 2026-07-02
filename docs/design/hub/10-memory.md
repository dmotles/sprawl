# 10 — Portable Memory

*The portable per-`(project, agent)` memory model: one single-writer stream per
agent, provenance on every unit, and a **checkpoint push/pull + last-writer-wins**
sync to the cloud companion. No merge, no synthesis engine in v1.*

See also: [`01-architecture.md`](01-architecture.md) (§4 identity, §5 memory) ·
[`09-synchronization`](09-synchronization.md) · [security-privacy](README.md) ·
[index](README.md)

---

## 0. MVP scope (v2 re-scope)

The hub is a **single-user cloud companion**: it relays the live stream and
**durably persists** memory, transcripts, and attachments. It is *not* a
multi-writer coordinator. For memory that means the whole model is:

> **Write local always → checkpoint PUSH on handoff → PULL on session start →
> last-writer-wins.** That's it.

Deliberately **cut from v1** (kept as future direction, not built):

- version-vector reconnect / delta-replay for memory;
- force-reclaim + provenance-based *semantic reconcile / synthesis* engine;
- write-lease + fence tokens for memory (memory uses a trivial active-host
  advisory marker instead — §6);
- snapshots / compaction of the memory stream.

**Kept because it's cheap and future-enabling:** the `(project, agent)` stream
keying (§2), the **provenance metadata** on every unit (§3), and the
**why-not-git** rationale (§4) — all still true, none of it costs anything in v1.

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
([`00-overview`](00-overview.md#problem--why)). The hub makes this memory
**portable** by durably persisting it in the cloud and handing it back on the
next session — a simple checkpoint round-trip, not a distributed database.

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
therefore no per-stream lock or merge algorithm in v1.

The only residual contention is the *same agent* (e.g. `weave`) running from two
hosts against the same cloud stream. In a single-user companion this is a
mistake, not a workflow — v1 handles it with a trivial **active-host advisory
marker** plus last-writer-wins (§6), not a lease/fence protocol.

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
prior units rather than editing them in place. This matches the append-only shape
of today's on-disk memory and of the event-log spine
([`01` §2](01-architecture.md#2-the-event-log-spine-the-strongest-idea--feature-it)).

### Provenance metadata (minimal, on every unit)

Every unit carries provenance. In v1 **nothing consumes it as a reconcile
engine** — it is recorded because it is cheap and future-enabling (attribution,
a later console, a possible synthesis pass):

```yaml
# provenance header on every memory unit
agent:       weave            # partition key / author
host_id:     host-7f3a        # stable per machine/install (opaque, §5)
run_id:      run-9c21         # per `sprawl enter` process (§5)
created_at:  2026-07-01T05:39:10Z
source:      session          # session | injected | synthesized
session_id:  cc004dc1-…       # optional: originating claude session (when source=session)
supersedes:  [unit-id, …]     # optional: units this curation replaces
```

- `source: session` — emitted from a live session handoff (the common case).
- `source: injected` — added out-of-band (e.g. a user note via the hub).
- `source: synthesized` — produced by a consolidation pass (today's
  `consolidate.go` / `regenerate.go` lineage). Local consolidation still runs;
  there is no *cross-host* synthesis engine in v1.

Provenance is the minimum needed to (a) attribute every unit and (b) leave the
door open for a future console / synthesis pass. Keep it minimal — resist adding
fields until a consumer needs them (YAGNI).

> **Public-repo hygiene.** `host_id` / `run_id` are **opaque, generated
> identifiers** — never a hostname, username, MAC, or machine description.
> Memory *content* can contain sensitive context; the trust/redaction model lives
> in [security-privacy](README.md). This doc governs *structure*, not content
> policy.

## 4. Combining memory = curation or synthesis, NEVER textual merge

In v1 the sync model (§6) is engineered so that **memory streams never have to be
combined**: single writer per stream, one active host at a time, last-writer-wins.
But the rule below still governs — it is *why* we chose checkpoint+LWW over a
git-style merge, and it is the contract any future combine must honor.

> **When two versions of a stream would diverge, we do not line-merge them.**
> A combine, if it ever happens, is **curation** (a human/agent picks which units
> survive) or **synthesis** (an agent reads the union and writes a new distilled
> unit that `supersedes` the inputs) — **never** a textual three-way merge.

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
- **We already synthesize locally.** The existing consolidation/regeneration path
  (`internal/memory/{consolidate,regenerate,arc}.go`) *already* combines memory by
  having Claude read and rewrite — that is the correct primitive if a cross-host
  combine is ever built, and it composes naturally with provenance
  (`source: synthesized`, `supersedes: [...]`).

**v1 consequence:** because we never merge, the cost of the rare two-host race is
bounded to "one host's un-pushed checkpoint is overwritten by a newer push" — an
**accepted single-user limitation** (§6), not a correctness bug in a shared store.

## 5. Object-storage layout

Each `(project, agent)` stream is stored as objects in the hub's blob store
(`gocloud.dev/blob`, per [`01` §7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs);
`fileblob`/`memblob` for local/tests → [`12-testability-local-dev`](README.md)).
Postgres holds a small **index** (per-stream latest checkpoint id, `created_at`,
provenance for lookup); blobs hold the unit bodies and transcripts. Exact keys are
a storage-doc concern ([`07-storage-persistence`](README.md)):

```
blob://<project>/<agent>/
├── units/<unit-id>.md            # individual memory units (immutable once written)
├── checkpoint/<checkpoint-id>.md # a pushed handoff snapshot of the stream head
└── transcripts/<session-id>/…    # FULL session transcripts, retained INDEFINITELY
```

- **No snapshots/compaction of the memory stream in v1.** A "checkpoint" here is
  just the pushed state at a handoff boundary (§6), not a compaction artifact.
  Compaction is a future optimization, not needed for a single user's stream.
- **Immutable units** keep provenance and history intact for a future console.

### Full session transcripts are retained indefinitely

Beyond distilled memory, the hub retains **full session transcripts** per
`(project, agent, session_id)`, **kept indefinitely — no GC in v1**. They are
*write-and-store only*; nothing reads them yet. They exist so these **FUTURE
consumers** become possible without a data-model change (all explicitly *not v1*):

- a **memory/session console** for browsing & curating agent memory
  ([`00` north-star](00-overview.md#north-star-vision--not-committed--future));
- **review** — replaying what an agent actually did;
- **search** across historical sessions;
- **memory-eval** — scoring/regression-testing the distillation pipeline against
  ground-truth transcripts.

Naming them as future consumers (not building them) keeps the storage shape
honest without pulling scope into the MVP.

### Simplest way vs. right way — transcript retention

- **Simplest:** don't store transcripts; keep only distilled memory. Cost: the
  console/review/search/eval features are impossible later without recapturing
  data we'll never have again — the raw signal is gone.
- **Right:** stream transcripts to blob storage and keep them.
- **Recommendation:** **retain transcripts indefinitely (no GC in v1).** For a
  single user they are cheap in blob storage and irreplaceable after the fact;
  a retention/GC policy is a later concern ([`07`](README.md)) if volume ever
  matters. Exposure is bounded by the single-user trust model
  ([security-privacy](README.md)).

## 6. Sync: write-local → push-on-handoff → pull-on-start → LWW

This is the whole v1 sync model. No version vectors, no fence tokens, no
reconnect delta-replay for memory.

```
session start ──▶ PULL: fetch latest checkpoint for (project, agent) from hub
                        └─▶ if hub newer than local → overwrite local .sprawl/memory
                        └─▶ (new host / empty local → just take the hub copy)
during session ─▶ WRITE LOCAL ALWAYS: memory lands in .sprawl/memory as today
handoff ───────▶ PUSH: upload the current stream head as a new checkpoint
                        └─▶ hub stores it; latest-wins by created_at
```

- **Write local always.** The local `.sprawl/memory/` is the working copy and the
  offline fallback — sprawl behaves exactly as today with no hub configured
  ([`01` §3](01-architecture.md#3-connected-vs-disconnected)). A hub outage never
  stalls a memory write; the push simply retries at the next handoff.
- **Checkpoint PUSH on handoff.** At each handoff boundary (the natural
  consolidation point today) the host uploads the stream head as a checkpoint.
- **PULL on session start.** A new session fetches the latest checkpoint first, so
  a fresh host starts with the cloud memory instead of empty. A **new host is just
  the extreme case** — empty local, take the hub copy wholesale.
- **Last-writer-wins.** The checkpoint with the newest `created_at` is the truth.
  No merge, no vector compare.

### Active-host advisory marker (the only concurrency guard)

The one bad case is the *same agent* pushing from two hosts. v1 does **not** use a
lease/fence protocol for this (that was cut). Instead, on pull the host records a
lightweight **active-host advisory marker** on the stream; if a *second* host
starts a session for the same `(project, agent)` while a marker is fresh, sprawl
**advises and rejects the second host** ("this stream is active on another host").

- It is **advisory**, not a hard distributed lock — good enough for one user who
  simply shouldn't be driving the same agent from two machines at once.
- If the marker is stale (previous host exited/crashed), the new host takes over.
- If two pushes race anyway, **last-writer-wins** and the older push is
  overwritten. This is an **accepted single-user limitation**, not a merge case —
  documented here so it's a known trade-off, not a surprise.

### Simplest way vs. right way — concurrency

- **Simplest (chosen for v1):** active-host advisory marker + last-writer-wins.
  Cost: a two-host race can lose one host's un-pushed delta; no automatic recovery.
- **Right (future):** the write-lease + fence-token + version-vector reconnect
  from [`01` §4](01-architecture.md#4-identity-lease--fencing-conceptual--detail-in-doc-10--09)
  / [`09`](09-synchronization.md), with provenance-based semantic reconcile (§4).
- **Recommendation:** **advisory marker + LWW now.** For a single-user companion
  the failure it permits (occasionally lose one machine's un-pushed handoff) is
  rare and low-cost; the lease/fence/reconcile machinery is real complexity that
  only pays off in the multi-user / multi-writer world that is explicitly out of
  v1 scope. Provenance metadata (§3) is retained so the "right" path stays open.

### Auth (context only)

The push/pull round-trip authenticates with a **single configured bearer token**
(no OIDC in v1) — see [`04-authentication`](README.md) for the token model. Called
out here only because sync is the memory path that crosses the network.

## 7. How it fits (one picture)

Memory portability is a checkpoint round-trip layered on the companion relay — it
does **not** add new transport or reconnect code:

```
weave session ─▶ memory unit (provenance) ─▶ .sprawl/memory (local, always)
   handoff    ─▶ PUSH checkpoint ─▶ hub blob store  units/ + checkpoint/ + transcripts/
 next start   ─▶ PULL latest checkpoint ─▶ overwrite local if hub newer (LWW)
```

Live session events still flow over the event-log spine
([`01` §6](01-architecture.md#6-how-the-pieces-fit-requestresponse-paths)); memory
sync is a coarser, handoff-boundary checkpoint on top of the same connection and
the same durable store.

## Open Questions

- **Checkpoint granularity** — is "whole stream head at handoff" fine, or should
  push be per-unit-delta to reduce upload size for large `persistent.md`? (Delta
  push edges back toward version vectors — resist unless size actually hurts.)
- **Handoff is the only push trigger?** — do we also push on clean session exit,
  or on a timer, to bound data loss if a session runs very long before handoff?
- **Advisory-marker TTL** — how long before a marker is considered stale and a new
  host may take over? Too short = false "active elsewhere"; too long = a crashed
  host blocks the next session.
- **Pull-overwrite safety** — if local has un-pushed writes newer than the hub
  checkpoint (e.g. a crash before push), should pull-on-start still overwrite, or
  detect local-newer and skip? (LWW by `created_at` should cover it, but the crash
  window needs a decision.)
- **Cross-project / user-level memory** — user preferences (e.g. "dmotles prefers
  KISS") live per-project in `persistent.md` today. A `(user, *)` global stream is
  future scope (YAGNI-flagged for v1).
- **When (if ever) to graduate to lease/fence + reconcile** — what concrete
  multi-writer need would justify building the "right" path cut here?
