# Sprawl Hub — Design Docs

The **hub** is an *optional* central broker that gives a single pane of glass over
many local sprawl instances, remote/mobile access, portable memory, and
multimodal ingestion — without changing how sprawl works when no hub is
configured.

> **Read these first:** [`00-overview.md`](00-overview.md) (the *why* & *what*)
> then [`01-architecture.md`](01-architecture.md) (the north-star architecture
> every other doc conforms to).

## Core principles (TL;DR)

- **Broker, not brain.** The live claude session on each host is the source of
  truth. The hub is a realtime fan-out point + durable store + auth boundary —
  *not* an authoritative state store.
- **Disconnected by default.** Sprawl works ~100% as today with no hub. There is
  **no default hub endpoint** in the code — connecting is opt-in via
  `--hub-url` / config / env.
- **One event-log spine.** A single seq'd, resumable event log flows
  claude → local eventbus → uplink → hub → browser. Every consumer follows one
  rule: *replay from my last seq, else snapshot, then live-tail.* Reconnect
  logic is written once and reused at every seam.
- **KISS/YAGNI.** Each doc weighs the *simplest way vs. the right way* and
  recommends the simplest thing that still sets up the right architecture.

## Document index

| # | Doc | Description | Status |
|---|-----|-------------|--------|
| 00 | [overview](00-overview.md) | Problem/why, solution shape, prior-art & build-vs-adopt, north-star vision (not committed) | draft |
| 01 | [architecture](01-architecture.md) | Topology, event-log spine, connected/disconnected, identity/lease/fence, how the pieces fit | draft |
| 02 | [components](02-components.md) | Breakdown of hub-side services and host-side agent additions | draft |
| 03 | api-surfaces | Connect/protobuf RPCs; long-lived-connection viability under cloud LBs | todo |
| 04 | authentication | OIDC relying-party, host→hub PATs, user allowlist | todo |
| 05 | observability | Logging, metrics, tracing, health endpoints | todo |
| 06 | iac | Terraform layout (`azure/` first, AWS door open); parameterization | todo |
| 07 | storage-persistence | DB interface, migration tooling, retention/GC, conceptual schema | todo |
| 08 | deployment | Single Go container, embedded frontend, container-cloud deploy | todo |
| 09 | [synchronization](09-synchronization.md) | Version-vector reconnect, lease/fence flows, force-reclaim reconcile | draft |
| 10 | memory | Portable per-(project,agent) memory streams, provenance, no textual merge | todo |
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
