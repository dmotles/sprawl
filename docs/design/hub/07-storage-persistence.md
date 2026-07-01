# 07 — Storage & Persistence

*The hub's durable substrate: a `Store` interface (in-memory + Postgres), the
migration tooling that lets the schema evolve safely, a **conceptual** entity
sketch, and retention/GC defaults.*

See also: [`01-architecture.md`](01-architecture.md) (§2 event-log spine, §4
lease/fence, §7 stack) · [`09-synchronization`](09-synchronization.md) (retention
floor, ack watermarks, fence durability) · [`10-memory`](10-memory.md) (versioned
blob layout) · [`06` iac](README.md) · [index](README.md)

---

## 0. Scope & the guiding call

This doc owns **how hub state is persisted and how that persistence evolves** — it
does **not** try to nail down every column. The maintainer's explicit guidance:

> Designing the schema in exhaustive detail up front is overkill — we don't yet
> know what we don't know. **Migration tooling is precisely what lets us defer
> that.** Name the likely entities at a sketch level and stop.

So the load-bearing decisions here are (1) the **`Store` interface** that isolates
the app from the DB, and (2) the **migration tooling** that lets the schema grow.
The entity sketch (§4) is deliberately high-level.

Two storage backends, per [`01` §7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs):

| Substrate | Holds | Library |
|---|---|---|
| **Postgres** | Event-log segments, registry, metadata, indexes, watermarks, leases | managed Postgres |
| **Object storage** | Blobs: attachments, snapshot bodies, memory-unit bodies, transcripts | `gocloud.dev/blob` (`memblob`/`fileblob` for tests) |

The split is intentional: **Postgres stores the *index and the small, queryable
truth*; blob storage stores the *large, opaque bodies*.** Snapshots, memory units,
and transcripts have a small PG index row (seq, provenance, blob key) and a large
body in blob storage. This keeps row sizes bounded and lets the two substrates
scale and be priced independently.

---

## 1. The `Store` interface (the central abstraction)

Everything the hub persists goes through **one Go interface**. The app tier never
imports `database/sql` or a blob SDK directly; it depends on `Store`.

```go
// Package store is the hub's persistence boundary. App code depends on this
// interface only — never on a concrete driver.
type Store interface {
    // Event log (per run-id append-only; §4, spine in 01 §2 / 09)
    AppendEvents(ctx, runID RunID, events []Event) (highSeq Seq, err error)
    ReadEvents(ctx, runID RunID, fromSeq, toSeq Seq) ([]Event, error)
    HeadSeq(ctx, runID RunID) (Seq, error)

    // Snapshots (index in PG, body in blob; §4)
    PutSnapshot(ctx, runID RunID, seq Seq, body BlobRef) error
    NewestSnapshot(ctx, runID RunID, atOrBelow Seq) (*SnapshotMeta, error)

    // Registry: hosts, runs, instances/leases (§4)
    UpsertHost(ctx, HostRecord) error
    ClaimLease(ctx, project ProjectID, holder HostID) (fence Fence, err error) // §3-fence
    RenewLease(ctx, project ProjectID, holder HostID, fence Fence) error
    ReadLease(ctx, project ProjectID) (*Lease, error)

    // Ack watermarks (drive retention floor + outbound-buffer trim; 09 §4)
    SetWatermark(ctx, consumer ConsumerID, runID RunID, seq Seq) error
    RetentionFloor(ctx, runID RunID) (Seq, error)

    // Memory stream index (bodies in blob; 10 §5)
    AppendMemoryUnit(ctx, key StreamKey, unit MemoryUnitMeta) error
    ReadMemoryUnits(ctx, key StreamKey, sinceSeq Seq) ([]MemoryUnitMeta, error)

    // Blob handle (attachments, bodies) — thin wrapper over gocloud.dev/blob
    Blobs() BlobStore

    // Cross-cutting
    GC(ctx, GCPolicy) (GCReport, error) // §5
    Migrate(ctx) error                  // §2: apply pending migrations at boot
    Close() error
}
```

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
   transforms (e.g. backfilling provenance onto old memory-unit rows, recomputing a
   watermark) that pure SQL can't express cleanly.

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
`*.go`), embedded via `embed.FS`, applied by `pgStore.Migrate` at startup and
runnable via a `sprawl hub migrate` subcommand for ops. `memStore` builds its
schema implicitly (it's just maps) and treats `Migrate` as a no-op — so tests never
run migrations.

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

## 3. Fence-token durability (sync requirement → storage decision)

[`09` §5](09-synchronization.md#5-fence-tokens-on-uplink-writes-stale-fence-rejection)
flags this as a storage decision but sets a hard requirement: the fence token must
be **monotonic across a hub DB failover**. Two options:

- **Global free-running counter** — one sequence for all leases. Simplest, but a
  restore-from-backup could *reset* it, and a reused-lower fence would let a zombie
  writer's stale write be wrongly accepted. Blast radius = every lease.
- **Per-lease `epoch`, bumped in the same transaction that grants/reclaims the
  lease** — monotonic by construction, travels with the lease row, survives failover
  with the row. Blast radius = one lease.

**Recommendation: per-lease `epoch`** (matching [`09`](09-synchronization.md)'s
lean). It's a single integer column on the lease row, incremented atomically at
claim/reclaim. Postgres row-level durability guarantees the monotonicity sync
requires; a physical replica/failover carries the committed epoch. This localizes
any restore anomaly to a single project's lease rather than the whole fleet.

---

## 4. Conceptual entity sketch (high-level — NOT column-level)

> ⚠️ **Deliberately a sketch.** Per §0 we name entities and relationships, then
> stop. Columns, indexes, and constraints are worked out *in migrations* as needs
> become concrete — that's what the tooling is for. This is the "what exists and how
> it relates" map, not a DDL spec.

```
                    ┌──────────┐        ┌──────────┐
                    │  users   │        │  tokens  │  host→hub PATs (hashed),
                    │(allowlist│◀──────▶│  (PATs)  │  user sessions — auth (04)
                    └────┬─────┘        └──────────┘
                         │ allowlisted principals
             ┌───────────┼───────────────────────────────┐
             ▼           ▼                                 ▼
        ┌─────────┐  ┌───────────────┐              ┌──────────────┐
        │  hosts  │  │   projects    │◀────lease────│    leases    │ 1 per project
        │(host_id)│  │  (project_id) │   (per proj) │ holder,      │ (schema admits
        └────┬────┘  └──────┬────────┘              │ epoch/fence, │  per-agent;
             │              │                       │ TTL, hb)     │  enforced
             │ origin       │ scopes                └──────────────┘  per-project — 01 §4)
             ▼              ▼
     ┌──────────────────────────┐        ┌───────────────────────────┐
     │  runs / instances        │        │  memory_streams           │
     │  (run_id per sprawl enter)│       │  key=(project, agent)      │ single writer
     └──────────┬───────────────┘        └──────────┬────────────────┘  by name (10 §2)
                │ emits                              │ appends
                ▼                                    ▼
     ┌──────────────────────────┐        ┌───────────────────────────┐
     │  event_log_segments      │        │  memory_units (index)      │
     │  (run_id, seq) append-   │        │  seq, provenance, blob_ref │──▶ blob body
     │  only; the spine (01 §2) │        │  supersedes[]  (10 §3)     │
     └──────────┬───────────────┘        └───────────────────────────┘
                │ periodically compacted
                ▼
     ┌──────────────────────────┐        ┌───────────────────────────┐
     │  snapshots (index)       │        │  attachments (index)       │
     │  (run_id, seq, blob_ref) │──▶blob │  kind, size, blob_ref,     │──▶ blob body
     └──────────────────────────┘  body  │  owning run/session        │  (screenshots,
                                          └───────────────────────────┘   09 downlink)
     ┌──────────────────────────┐
     │  watermarks              │  per (consumer, run_id) → last_seq;
     │  (ack cursors, 09 §4)    │  drives retention floor (§5) + buffer trim
     └──────────────────────────┘
```

**Likely entities, named and stopped-at:**

| Entity | Grain | Body location | Notes |
|---|---|---|---|
| `users` | principal | PG | Auth allowlist ([`04`](README.md)) |
| `tokens` | host→hub PAT / user session | PG (**hashed**) | Never store plaintext PATs |
| `hosts` | per machine/install | PG | `host_id` opaque ([`10` §3](10-memory.md)) |
| `projects` | per repo/project | PG | Lease + memory scope |
| `leases` | per project (v1) | PG | `epoch`/fence, TTL, heartbeat (§3) |
| `runs` / `instances` | per `sprawl enter` | PG | `run_id`; owns a seq space ([`09` §1](09-synchronization.md)) |
| `event_log_segments` | `(run_id, seq)` | PG (small events) | Append-only spine ([`01` §2](01-architecture.md)) |
| `snapshots` | `(run_id, seq)` | PG index + **blob body** | Compaction ([`09` §3](09-synchronization.md)) |
| `memory_streams` | `(project, agent)` | PG index | Single-writer ([`10`](10-memory.md)) |
| `memory_units` | unit in a stream | PG index + **blob body** | Provenance, `supersedes` |
| `attachments` | image/blob | PG index + **blob body** | Multimodal ingestion |
| `watermarks` | `(consumer, run_id)` | PG | Ack cursors ([`09` §4](09-synchronization.md)) |
| `transcripts` | `(project, agent, session)` | **blob body** | Write-only in v1 ([`10` §5](10-memory.md)) |

That's the sketch. **We are not specifying columns, types, or indexes here** —
those land in the first migration and evolve from there.

### Simplest way vs. right way — schema depth up front

- **Simplest (and correct here):** name entities + relationships, defer columns to
  migrations. Cost: the first migration has real design work in it (that's fine —
  it's where the work *belongs*).
- **"Right"-looking but wrong:** exhaustive normalized DDL now. Cost: we'd be
  guessing at columns for consumers that don't exist yet (transcripts,
  attachments), then migrating away from those guesses anyway. Pure waste.
- **Recommendation:** **sketch now, detail in migrations.** This *is* the KISS/YAGNI
  call, and it's the maintainer's explicit direction.

---

## 5. Retention & GC defaults (coordinate with IaC `06`)

Retention is a **policy, never a correctness property** — shrinking any window only
forces more snapshot-fallbacks, never data loss, because the one rule
([`09`](09-synchronization.md)) always covers a trimmed gap with a snapshot. So the
defaults below are safe to tune per-deployment (IaC-parameterized, [`06`](README.md)).

| Data class | Default retention | Trim driver | Rationale |
|---|---|---|---|
| **Event-log segments** | Trim ≤ `RetentionFloor` + fixed safety margin | ack watermarks + newest snapshot ([`09` §2/§4](09-synchronization.md)) | Cache of host truth; safe to lose |
| **Snapshots** | Keep newest N per run (small N) + newest overall | count-based | Bounds cold-start; older snapshots superseded |
| **Memory units** | **Retain (versioned)**; GC only superseded units past a grace window | `supersedes` + age | Retain-both reconcile needs losers ([`10` §4](10-memory.md)) |
| **Transcripts** | **Bounded retention window**, opt-in | age | Irreplaceable but bulky; privacy-gated ([`10` §5](10-memory.md)) |
| **Attachments** | Age / orphan (unreferenced) sweep | age + refcount | Blob cost control |
| **Tokens (revoked/expired)** | Purge past grace | expiry | Hygiene ([`04`](README.md)) |

**Mechanics:** one **`Store.GC(GCPolicy)`** pass, watermark-driven, no per-consumer
bookkeeping beyond the watermark rows ([`09` §2](09-synchronization.md) KISS lean).
PG rows are deleted below the floor; blob bodies are deleted by a companion
orphan-sweep (delete a blob only once no PG index row references it — index first,
blob second, so a crash mid-GC leaves harmless orphans, never dangling refs).

The **retention floor is bounded below by the newest snapshot seq** so one stuck
consumer can't pin the whole log; past a grace window a slow consumer is cut loose
to the snapshot path ([`09` §4](09-synchronization.md)).

### Simplest way vs. right way

- **Simplest:** keep everything forever. Cost: unbounded PG + blob growth; every
  session's full history retained after everyone caught up.
- **Right:** watermark-driven trim for the log, count/age windows for the rest, one
  GC pass, everything IaC-parameterized.
- **Recommendation:** **the table above as defaults, all knobs parameterized in
  [`06`](README.md).** Ship conservative defaults (generous margins), let deployers
  tighten. Never trim below the snapshot floor. `log()` any bulk deletion — no
  silent purges.

---

## 6. How it fits the spine (one picture)

```
uplink event ─▶ Store.AppendEvents(run, [..])         (PG segments, seq verbatim — 09 §1)
                     │
   periodic compaction ─▶ Store.PutSnapshot(run, seq, blobRef)   (PG index + blob body)
                     │
ack watermark ─▶ Store.SetWatermark(consumer, run, seq)
                     │
   GC pass ─▶ Store.GC(policy): trim segments ≤ RetentionFloor,   (09 §2/§4)
             sweep orphan blobs, age out transcripts/attachments  (§5)

memory unit ─▶ Store.AppendMemoryUnit(key,(seq,provenance,blobRef))  (PG index + blob body — 10)
new host    ─▶ ReadMemoryUnits(key, sinceSeq)  or NewestSnapshot → pull latest (10 §6)
```

Storage adds **no reconnect/catch-up logic of its own** — it is the durable buffer
the spine's one rule reads and writes. `memStore` makes all of the above run in a
test with no external services.

---

## Open Questions

- **`sqlc` vs. hand-written `pgx`.** Start hand-written (KISS); adopt `sqlc` if
  query code proves error-prone? Or adopt it up front for compile-checked SQL?
- **Snapshot body format.** [`09` §3](09-synchronization.md) deferred *content*
  here: rendered transcript vs. serialized reducer state? Affects blob size, replay
  cost, and whether the frontend or hub materializes it. Needs a frontend
  ([`11`](README.md)) round-trip.
- **Event body in PG vs. blob.** Small events live in PG rows — but is there a size
  threshold above which an event body should spill to blob (e.g. a huge tool
  output)? Or do we cap event size upstream?
- **Blob key scheme & multi-tenancy.** `blob://<project>/<agent>/…`
  ([`10` §5](10-memory.md)) is the memory layout; do event snapshots/attachments
  share a bucket with a key prefix, or separate buckets per data class for
  independent lifecycle/retention rules in the cloud?
- **GC scheduling.** Cron-style periodic pass, on-write incremental, or
  watermark-triggered? And who runs it — the hub process itself or an ops
  subcommand ([`06`](README.md))?
- **Postgres partitioning at scale.** Does `event_log_segments` need range/hash
  partitioning by `run_id` (or time) before it's large, or is that a
  cross-that-bridge-later migration? (Migration tooling makes deferring safe.)
- **Backup/restore & fence monotonicity.** §3 picks per-lease epoch; confirm the
  chosen managed-Postgres backup/PITR strategy ([`06`](README.md)) actually
  preserves committed epochs across a restore (the one anomaly we must rule out).
- **De-dup key for replayed memory units** (inherited from
  [`10` OQ](10-memory.md#open-questions)): is `(host_id, run_id, created_at)` a
  sufficient storage-level uniqueness key, or do units need a content hash / stable
  `unit-id` column to survive re-uploads after a buffer flush?
