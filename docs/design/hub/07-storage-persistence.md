# 07 — Storage & Persistence

*The hub's durable substrate: a `Store` interface (in-memory + Postgres), the
migration tooling that lets the schema evolve safely, and a **conceptual** entity
sketch. Scoped for the **MVP: a single-user cloud companion** — relay the live
stream and durably persist memory, transcripts, and attachments.*

See also: [`01-architecture.md`](01-architecture.md) (§2 durable stream, §4
active-host marker, §7 stack) · [`09-synchronization`](09-synchronization.md)
(one seq'd stream, fresh-connect vs. reconnect delta) · [`10-memory`](10-memory.md)
(checkpoint blob layout) · [`06` iac](README.md) · [index](README.md)

---

## 0. Scope & the guiding call

This doc owns **how hub state is persisted and how that persistence evolves** — it
does **not** try to nail down every column. The maintainer's explicit guidance:

> Designing the schema in exhaustive detail up front is overkill — we don't yet
> know what we don't know. **Migration tooling is precisely what lets us defer
> that.** Name the likely entities at a sketch level and stop.

### MVP framing (v2 re-scope)

The hub is a **cloud companion for a single user**. It does two durable jobs:

1. **Relay** the host's live stream to connected viewers.
2. **Persist** memory checkpoints, session transcripts, and attachments so the
   user can pick up from anywhere and never lose history.

That framing collapses a lot of earlier complexity. The load-bearing decisions
that remain are (1) the **`Store` interface** that isolates the app from the DB,
and (2) the **migration tooling** that lets the schema grow. The entity sketch
(§4) is deliberately high-level.

**Explicitly out of scope for v1** (deleted from this design, not merely
deferred unless noted):

| Cut | Rationale |
|---|---|
| **Multi-tenancy** | Single user. A `user_id` column exists and is *always the same value*; no tenant isolation, no per-tenant enforcement. |
| **OIDC / SSO** | Auth is a **bearer token** ([`04`](README.md)). The `tokens` table stores **hashed** bearer token(s) — nothing more. |
| **Version-vector / reconcile for memory** | Memory sync is **last-writer-wins checkpoint sync**. Provenance metadata columns are *kept*; the vector-clock/merge machinery is cut. |
| **Fence tokens / lease epochs** | Write authority is a **trivial advisory active-host marker row** — no fence column, no lease-epoch, no lease table. |
| **GC / retention windows** | **Keep everything indefinitely** — transcripts, attachments, memory. Revisit only if storage becomes a real problem. |
| **Snapshots** | No snapshot bodies in v1. The durable seq'd stream is the only replay source. |
| **Separate ephemeral event-log + snapshot layering** | There is **one** durable, append-only, seq'd stream per session (§4). It *is* the transcript and it serves both fresh-connect full-send and reconnect delta. |
| **Client-side encryption** | Deferred. Blob bodies are stored as-is (bucket-level encryption from the cloud provider only). |

Two storage substrates, per [`01` §7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs):

| Substrate | Holds | Library |
|---|---|---|
| **Postgres** | Light **index / registry**: users, tokens, hosts, projects, the active-host marker, per-session stream index, memory-unit index, attachment index | managed Postgres |
| **Object storage** | **Bodies**: the seq'd session stream (transcript), memory-unit bodies, attachment blobs | `gocloud.dev/blob` (`memblob`/`fileblob` for tests); secrets via `gocloud.dev/secrets` |

The split is intentional: **Postgres stores the *small, queryable index*; blob
storage stores the *large, opaque bodies*.** Each stream event, memory unit, and
attachment has a small PG index row (seq/key, provenance, blob key) and its body
lives in blob storage. This keeps row sizes bounded and lets the two substrates
scale and be priced independently.

---

## 1. The `Store` interface (the central abstraction)

Everything the hub persists goes through **one Go interface**. The app tier never
imports `database/sql` or a blob SDK directly; it depends on `Store`.

```go
// Package store is the hub's persistence boundary. App code depends on this
// interface only — never on a concrete driver.
type Store interface {
    // The durable seq'd session stream (§4). ONE append-only seq'd log per
    // session — it is the transcript. Serves both fresh-connect full send
    // (from seq 0) and reconnect delta (from a client's last seq).
    AppendStream(ctx, sess SessionID, events []Event) (highSeq Seq, err error)
    ReadStream(ctx, sess SessionID, fromSeq, toSeq Seq) ([]Event, error)
    HeadSeq(ctx, sess SessionID) (Seq, error)

    // Registry: hosts, projects, and the advisory active-host marker (§3).
    UpsertHost(ctx, HostRecord) error
    SetActiveHost(ctx, project ProjectID, holder HostID) error // advisory only
    ReadActiveHost(ctx, project ProjectID) (*ActiveHost, error)

    // Memory: last-writer-wins checkpoint index (bodies in blob; 10).
    // Provenance metadata is retained on each unit; no version-vector merge.
    PutMemoryUnit(ctx, key StreamKey, unit MemoryUnitMeta) error
    ReadMemoryUnits(ctx, key StreamKey) ([]MemoryUnitMeta, error)

    // Blob handle (transcripts, memory bodies, attachments) —
    // thin wrapper over gocloud.dev/blob.
    Blobs() BlobStore

    // Cross-cutting
    Migrate(ctx) error // §2: apply pending migrations at boot
    Close() error
}
```

> **No `GC` method.** v1 keeps everything (§0). If storage ever becomes a real
> problem, retention lands as a *new* method + migration — not a v1 concern.
> **No `PutSnapshot` / `ClaimLease` / `SetWatermark`** either — snapshots, leases,
> and ack-watermark retention plumbing are all cut per the re-scope.

Two implementations, both required from day one:

- **`memStore`** — in-memory maps + `memblob`. Powers unit tests, `sprawl`-side
  fakes, and local dev with **zero external dependencies**
  ([`12-testability`](README.md)). Fast, hermetic, resettable.
- **`pgStore`** — managed Postgres + `gocloud.dev/blob` (`fileblob` locally,
  cloud bucket in prod). The real thing.

### Simplest way vs. right way

- **Simplest:** app code calls `pgx`/SQL and the blob SDK inline wherever it needs
  data. Cost: no seam for tests (every test needs a real Postgres), driver details
  leak everywhere, and swapping/​mocking storage is impossible.
- **Right (recommended):** a single `Store` interface with `memStore` + `pgStore`.
  Cost: one interface to keep honest and a fake to maintain alongside the real impl.
- **Recommendation:** **`Store` interface, two impls.** This is the same
  dependency-injection discipline the rest of sprawl already uses (`deps` structs,
  interface-per-external-dependency — see `CLAUDE.md` *Code Patterns*). The
  in-memory impl is what keeps the hub testable without standing up infrastructure;
  it's cheap now and painful to retrofit. **KISS caveat:** keep the interface
  *narrow* — add a method only when a caller needs it, not speculatively.

> **Query-mapping layer:** hand-written SQL with `pgx` is the KISS default. `sqlc`
> (compile-checked SQL→Go) is a reasonable low-cost upgrade if hand-written query
> code becomes error-prone — it composes with everything here and is deferred, not
> foreclosed. An ORM is explicitly **not** adopted (opaque SQL, migration coupling).

---

## 2. Migration tooling — pick one: **goose**

The schema **will** change as we learn what we don't yet know (§0). The tool must:

1. Be a **library**, not just a CLI — the hub applies pending migrations on boot
   (`Store.Migrate`) so a single container is self-contained.
2. Support **embedded migrations** via `embed.FS` — the hub already embeds its SPA
   with `go:embed` ([`01` §7](01-architecture.md)); migrations ship in the binary
   the same way, no sidecar files at deploy.
3. Support **Go-based migrations**, not just SQL — some evolutions are *data*
   transforms (e.g. backfilling provenance onto old memory-unit rows) that pure SQL
   can't express cleanly.

### Evaluation

| Criterion | **goose** (`pressly/goose/v3`) | **golang-migrate** |
|---|---|---|
| Library + CLI | ✅ both, first-class | ✅ both |
| Embedded (`embed.FS`) | ✅ native | ✅ (`iofs` source) |
| SQL migrations | ✅ | ✅ |
| **Go-code migrations** | ✅ **native** (register a Go func) | ❌ SQL-only |
| Language-agnostic | ❌ Go-focused | ✅ many languages |
| Adoption | Very widely used in Go services | Most widely adopted overall |
| Setup complexity | Low | Slightly higher (source/DB URL drivers) |

### Recommendation: **goose**

Both are solid and either would work. **goose wins for sprawl specifically** on two
axes that matter to *this* project:

- **Go-code migrations.** golang-migrate is SQL-only; the moment we need a data
  backfill (very likely, given we're deferring schema detail and will reshape rows
  as we learn), goose lets us write it as a normal Go function against the same
  connection. golang-migrate would force that logic outside the migration flow.
- **Go-native, library-first, embeds cleanly.** golang-migrate's headline strength
  is being *language-agnostic* — irrelevant here; the hub is a single Go binary.
  goose's tighter Go fit and `embed.FS` support match the "single self-contained
  container that migrates itself on boot" shape exactly.

Migrations live in-repo (e.g. `internal/hub/store/migrations/*.sql` + optional
`*.go`), embedded via `embed.FS` and **applied automatically by `hubd` on boot**
(`pgStore.Migrate` at startup). There is **no standalone `sprawl hub migrate`
CLI** — the local `sprawl` binary is DB-free; the hub owns Postgres and migrates
itself (the "single self-contained container that migrates itself on boot" shape
above). `memStore` builds its schema implicitly (it's just maps) and treats
`Migrate` as a no-op — so tests never run migrations.

### Simplest way vs. right way

- **Simplest:** hand-rolled `CREATE TABLE IF NOT EXISTS` + a `schema_version` int we
  bump ourselves. Cost: no down-migrations, no ordering guarantees, no ecosystem
  tooling, reinventing a solved problem — pure toil the first time we need to alter
  a live table.
- **Right:** adopt a migration tool (goose).
- **Recommendation:** **adopt goose.** Migration tooling is the *whole reason* we
  can defer schema detail (§0). Rolling our own is the one place YAGNI does **not**
  apply — the safety of evolving a live schema is exactly what we're buying.

> **Wire-compat note:** protobuf/`buf breaking` guards the *API* wire format
> ([`03`](README.md)); goose guards the *DB* schema. They're independent evolution
> axes and both are needed.

---

## 3. Write authority — an advisory active-host marker (no fence/lease)

The hub is a **companion**, not the source of truth: the host owns the session.
For a single user with (typically) one active host, write authority needs only a
**trivial advisory marker** — a single row per project recording which host is
currently the active writer:

```
active_host(project_id, host_id, heartbeat_at)   -- one row per project, upsert
```

`SetActiveHost` upserts the row; `ReadActiveHost` reads it. That's the whole
mechanism. It's **advisory**: it lets a viewer/UI show "host X is live" and lets a
newly-connecting host see it's taking over, but the hub does **not** fence or
reject writes on the strength of it.

**Explicitly cut** (was §3 in the pre-MVP design): fence tokens, per-lease
`epoch`, lease TTL/reclaim transactions, and the monotonic-across-failover
durability requirement. Those existed to arbitrate *concurrent contending
writers* across a multi-tenant fleet — a problem a single-user companion does not
have. If multi-writer contention ever becomes real, fencing lands as a migration
that adds columns to this row; nothing here forecloses it.

---

## 4. Conceptual entity sketch (high-level — NOT column-level)

> ⚠️ **Deliberately a sketch.** Per §0 we name entities and relationships, then
> stop. Columns, indexes, and constraints are worked out *in migrations* as needs
> become concrete — that's what the tooling is for. This is the "what exists and how
> it relates" map, not a DDL spec.

```
        ┌──────────┐        ┌────────────────┐
        │  users   │◀──────▶│    tokens      │  hashed bearer token(s) — auth (04)
        │ (exactly │        │ (hashed only)  │  NO OIDC; NO plaintext
        │  ONE row)│        └────────────────┘
        └────┬─────┘
             │ owns everything (user_id constant everywhere)
     ┌───────┼───────────────────────────────┐
     ▼       ▼                                 ▼
 ┌─────────┐  ┌───────────────┐        ┌────────────────────┐
 │  hosts  │  │   projects    │◀──mark─│   active_host      │ advisory only (§3)
 │(host_id)│  │  (project_id) │        │ project→host,      │ no fence, no lease,
 └────┬────┘  └──────┬────────┘        │ heartbeat_at       │ no epoch
      │ origin       │ scopes          └────────────────────┘
      │              │
      │              ▼
      │   ┌─────────────────────────────┐
      │   │  sessions                   │  one per `sprawl enter`
      │   │  (session_id)               │  owns a seq space
      │   └──────────┬──────────────────┘
      │              │ appends
      │              ▼
      │   ┌─────────────────────────────┐
      │   │  session_stream (index)     │  THE durable seq'd log =
      │   │  (session_id, seq)          │  the transcript. Append-only.
      │   │  → blob body per event      │──▶ blob body
      │   │  Serves fresh-connect full  │
      │   │  send + reconnect delta.    │
      │   └─────────────────────────────┘
      │
      ▼
 ┌─────────────────────────────┐        ┌───────────────────────────┐
 │  memory_units (index)       │        │  attachments (index)      │
 │  key=(project, agent)       │        │  kind, size, blob_key,    │──▶ blob body
 │  seq, provenance, blob_key  │──▶blob │  owning session           │  (screenshots,
 │  last-writer-wins (10)      │  body  └───────────────────────────┘   09 downlink)
 └─────────────────────────────┘
```

**Likely entities, named and stopped-at:**

| Entity | Grain | Body location | Notes |
|---|---|---|---|
| `users` | the single principal | PG | **Exactly one row.** `user_id` is constant everywhere; no multi-tenant enforcement ([`04`](README.md)) |
| `tokens` | bearer token(s) | PG (**hashed**) | No OIDC. Never store plaintext |
| `hosts` | per machine/install | PG | `host_id` opaque ([`10` §3](10-memory.md)) |
| `projects` | per repo/project | PG | Active-host + memory scope |
| `active_host` | per project | PG | Advisory marker only; no fence/lease/epoch (§3) |
| `sessions` | per `sprawl enter` | PG | `session_id`; owns a seq space ([`09` §1](09-synchronization.md)) |
| `session_stream` | `(session_id, seq)` | PG index + **blob body** | **THE** durable seq'd log = the transcript; append-only; serves fresh-connect + reconnect ([`01` §2](01-architecture.md), [`09`](09-synchronization.md)) |
| `memory_units` | unit in `(project, agent)` stream | PG index + **blob body** | Last-writer-wins checkpoint; provenance metadata retained ([`10`](10-memory.md)) |
| `attachments` | image/blob | PG index + **blob body** | Multimodal ingestion |

That's the sketch. **We are not specifying columns, types, or indexes here** —
those land in the first migration and evolve from there.

> **What's gone vs. the pre-MVP sketch:** `leases`, `event_log_segments` +
> `snapshots` (collapsed into the single `session_stream`), `memory_streams` as a
> separate registry table (folded into the `memory_units` key), and `watermarks`
> (no retention floor to drive). See §0.

### Simplest way vs. right way — schema depth up front

- **Simplest (and correct here):** name entities + relationships, defer columns to
  migrations. Cost: the first migration has real design work in it (that's fine —
  it's where the work *belongs*).
- **"Right"-looking but wrong:** exhaustive normalized DDL now. Cost: we'd be
  guessing at columns for consumers that don't exist yet, then migrating away from
  those guesses anyway. Pure waste.
- **Recommendation:** **sketch now, detail in migrations.** This *is* the KISS/YAGNI
  call, and it's the maintainer's explicit direction.

---

## 5. Retention: keep everything (v1)

**v1 keeps everything indefinitely** — transcripts, attachments, and memory units
are all retained forever. There is no GC pass, no retention window, no ack-watermark
trim, and no snapshot floor.

Rationale: this is a **single-user companion**. The volume one user generates is
small enough that unbounded growth is not a near-term problem, and the *cost* of a
wrong GC — silently losing irreplaceable transcript or memory history — dwarfs the
storage savings. "Keep everything" is also the simplest possible design: no
policy, no floor arithmetic, no orphan-sweep, nothing to get wrong.

> **Revisit only if storage becomes a real problem.** If it does, retention lands
> as a new `Store` method + a goose migration — a deliberate, measured addition,
> not a v1 guess. Until then: no deletion path exists, by design.

The single durable seq'd stream (§4) is never trimmed, so replay from any client's
last-seen seq (or from seq 0 for a fresh connect) is always available directly from
the stream — which is exactly why v1 needs **no snapshots** ([`09`](09-synchronization.md)).

---

## 6. How it fits the spine (one picture)

```
uplink event ─▶ Store.AppendStream(session, [..])   (PG index row per seq + blob body — 09 §1)
                     │
fresh connect ─▶ Store.ReadStream(session, 0, head)          full send from seq 0
reconnect     ─▶ Store.ReadStream(session, lastSeq+1, head)  delta from client's last seq
                     │  (ONE stream serves both — no snapshot path)

memory checkpoint ─▶ Store.PutMemoryUnit(key, (seq, provenance, blobKey))  (PG index + blob body — 10)
new host          ─▶ ReadMemoryUnits(key) → pull latest (last-writer-wins)  (10 §6)

active writer ─▶ Store.SetActiveHost(project, host)   (advisory marker; no fence — §3)
```

Storage adds **no reconnect/catch-up logic of its own** — it is the durable buffer
the spine's one rule reads and writes, and the single seq'd stream is the only
replay source. `memStore` makes all of the above run in a test with no external
services.

---

## Open Questions

- **`sqlc` vs. hand-written `pgx`.** Start hand-written (KISS); adopt `sqlc` if
  query code proves error-prone? Or adopt it up front for compile-checked SQL?
- **Event body in PG vs. blob.** Small stream events could live inline in PG rows
  rather than a blob body each — is there a size threshold below which inlining is
  cheaper (fewer blob round-trips) and above which bodies spill to blob (e.g. a huge
  tool output)? Or do we cap event size upstream and inline everything?
- **Blob key scheme.** `blob://<project>/<agent>/…` ([`10` §5](10-memory.md)) is the
  memory layout; do session-stream events and attachments share a bucket with a key
  prefix, or separate buckets per data class? (Single-user, so no tenant dimension
  in the key — one fewer thing to design.)
- **When does "keep everything" break?** At what stored-volume or cost point should
  retention be reconsidered (§5)? Worth a rough back-of-envelope on one user's
  yearly transcript + attachment footprint so we know the order of magnitude before
  it bites.
- **De-dup key for replayed memory units** (inherited from
  [`10` OQ](10-memory.md#open-questions)): under last-writer-wins, is
  `(host_id, session_id, created_at)` a sufficient uniqueness key, or do units need
  a content hash / stable `unit-id` column to survive re-uploads after a buffer
  flush?
- **Client-side encryption (deferred).** When it lands, does it change the index —
  e.g. do we need per-blob key-wrapping metadata columns, and does that touch the
  `attachments` / `memory_units` / `session_stream` index rows?
