# Sprawl Hub — Design Docs

The **hub** is an *optional*, **single-user** hosted **cloud companion** to the
local `sprawl` binary. For v1 it does exactly two things: (1) **relay the live
activity stream** to a browser for remote view + input, and (2) **durably
persist** memory, session transcripts, and attachments so they're reachable from
any machine. Nothing more — no multi-tenant service, no authoritative state
store.

> **Read these first:** [`00-overview.md`](00-overview.md) (the *why* & *what*)
> then [`01-architecture.md`](01-architecture.md) (the north-star architecture
> every other doc conforms to).

## Status (2026-08-18) — partially superseded by v2

The [v2 log-centric rearchitecture](../v2-log-centric-rearchitecture.md)
(`QUM-1248`) absorbs this design's storage half and **inverts its core
philosophy**: where this doc says "cloud companion, not brain; local session
is source of truth; no default endpoint," v2 makes the shared Postgres event
log the authoritative coordination spine, with the connection string as the
system identity. That inversion means the docs below split into two groups:

**Superseded by the v2 event log** (do not build against these; consult the
v2 doc instead):

| doc | why superseded |
|---|---|
| [`00-overview.md`](00-overview.md) | Its problem statement and solution shape assume a companion/relay hub over a locally-authoritative sprawl; v2's shared Postgres inverts that authority relationship. |
| [`01-architecture.md`](01-architecture.md) | Its north-star topology (hub-and-spoke broker, no authoritative state) is the specific thing v2 replaces with the Postgres event log as the spine. |
| [`07-storage-persistence.md`](07-storage-persistence.md) | Its store interface and keep-everything schema are superseded by v2's `events`/`artifacts`/definitions schema (see v2 doc, Appendix A). |
| [`09-synchronization.md`](09-synchronization.md) | Its reconnect-replay spine and advisory active-host marker are superseded by v2's event-level claims, seq-cursor dispatch, and turn-boundary liveness. |
| [`10-memory.md`](10-memory.md) | Its portable last-writer-wins memory streams are superseded by v2's log-fed temporal memory (`entities`/`facts`/`fact_provenance`, supersede-don't-delete). |

**Retained, retargeted as future work** (still valid designs, but as a thin
view/relay layer over the v2 Postgres rather than an independent store):

| doc | why retained |
|---|---|
| [`03-api-surfaces.md`](03-api-surfaces.md) | The RPC/long-lived-connection concerns are still real for any browser client; only the backing store changes. |
| [`04-authentication.md`](04-authentication.md) | The bearer-token → session-cookie auth boundary is orthogonal to where state lives. |
| [`11-frontend-stack.md`](11-frontend-stack.md) | Frontend framework selection is independent of the storage/coordination redesign. |
| [`attachments-multimodal.md`](attachments-multimodal.md) | Screenshot/image ingestion is a UI-layer concern that still applies once the hub is a view over v2. |

Docs not listed above (`02-components`, `05-observability`, `06-iac`,
`08-deployment`, `12-testability-local-dev`, `security-privacy`,
`13-implementation-plan`) were `todo`/unwritten at v2 approval time and carry
no assessment yet; treat them as needing the same superseded-vs-retargeted
pass before anyone writes them. `QUM-913` tracks aligning the affected docs'
own prose to the v2 model; this status block is not a substitute for that
edit, it is the pointer until that edit lands.

## Core principles (TL;DR)

- **Cloud companion, not brain.** The live claude session on each host is the
  source of truth. The hub is a realtime relay + durable store + thin auth
  boundary — *not* an authoritative state store.
- **Single-user.** One user, their hosts, their browsers. A `user_id` column
  (always one value) is kept purely as a cheap flex-later hedge; no multi-tenant
  isolation or enforcement is built.
- **Disconnected by default.** Sprawl works ~100% as today with no hub. There is
  **no default hub endpoint** in the code — connecting is opt-in via
  `--hub-url` / config / env.
- **One durable seq'd stream.** The durable session transcript *is* the seq'd
  log. Fresh connect → full log; reconnect → send last seq, get the delta; then
  live-tail. Reconnect logic is written once and reused at every seam. No
  separate snapshot layer in v1.
- **Simple sync.** Bearer-token auth (no OIDC); last-writer-wins memory
  (single-writer-by-agent-name makes it safe); an advisory active-host marker
  (no fence tokens); keep everything indefinitely (no GC).
- **KISS/YAGNI.** Each doc weighs the *simplest way vs. the right way* and
  recommends the simplest thing that still sets up the right architecture.

## Document index

| # | Doc | Description | Status |
|---|-----|-------------|--------|
| 00 | [overview](00-overview.md) | Problem/why, solution shape, prior-art & build-vs-adopt, north-star vision (not committed) | draft |
| 01 | [architecture](01-architecture.md) | Topology, event-log spine, connected/disconnected, identity/lease/fence, how the pieces fit | draft |
| 02 | components | Breakdown of hub-side services and host-side agent additions | todo |
| 03 | api-surfaces | Connect/protobuf RPCs; long-lived-connection viability under cloud LBs | todo |
| 04 | authentication | Single configured bearer token → httpOnly session cookie; host uses same style (OIDC deferred) | todo |
| 05 | observability | Logging, metrics, tracing, health endpoints | todo |
| 06 | iac | Terraform layout (`azure/` first, AWS door open); parameterization | todo |
| 07 | storage-persistence | Store interface + goose migrations, conceptual schema, keep-everything (no GC) | todo |
| 08 | deployment | Single Go container, embedded frontend, container-cloud deploy | todo |
| 09 | synchronization | Reconnect-replay spine, advisory active-host marker (no fence/lease/reconcile) | todo |
| 10 | memory | Portable per-(project,agent) streams, last-writer-wins, provenance metadata, no textual merge | todo |
| 11 | frontend-stack | SPA framework selection (open research) | todo |
| 12 | testability-local-dev | Local hub, in-memory backends, fakes, e2e story | todo |
| — | security-privacy | Threat model + content trust model | todo |
| — | attachments-multimodal | Screenshot/image ingestion + Claude image-input feasibility | todo |
| 13 | implementation-plan | MVP sprint plan (written last) | todo |

> Leaf docs own their own files. This index is **not** meant to be edited by leaf
> authors beyond flipping their row's status to `draft`/`done`.

## Conventions

- Every doc ends with an `## Open Questions` section.
- ASCII/mermaid diagrams preferred over prose where a picture is clearer.
- **Public-repo hygiene:** no employer/company-internal names, systems, hosts,
  tenants, customers, or the maintainer's specific instance. "Azure" appears
  only as a generic public-cloud target. Everything deployment-specific is
  parameterized.
