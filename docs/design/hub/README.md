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
