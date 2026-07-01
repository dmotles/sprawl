# 01 — Architecture

*The north-star architecture. Every other doc conforms to this one.*

See also: [`00-overview.md`](00-overview.md) · [index](README.md)

---

## 1. Topology

Hub-and-spoke. Hosts are NAT'd and **dial OUT** to the hub, holding one
persistent bidirectional connection each. The hub fans host event-logs out to
browsers and routes browser input back down.

```
        ┌─────────────────────── HUB (single Go container) ───────────────────────┐
        │                                                                          │
        │   Connect/protobuf API   ┌────────────┐   embedded static SPA (go:embed) │
        │   ├─ uplink ingest ──────▶│  event-log │◀──── downlink fan-out ───┐       │
        │   ├─ downlink dispatch    │   store    │                          │       │
        │   ├─ auth (OIDC + PAT)    │  (Postgres)│   blob store (gocloud)   │       │
        │   └─ lease/fence registry └────────────┘   secrets  (gocloud)     │       │
        └───────▲───────────────────────────────────────────────────────────┼──────┘
                │ persistent bidi conn (dial-out, heartbeat=connection)      │ HTTPS
   ┌────────────┼───────────────┐                                  ┌─────────┴──────────┐
   │ host A     │  host B  ...   │                                  │ browser (laptop)   │
   │  sprawl enter (root=weave)  │                                  │ browser (phone)    │
   │  ├─ claude session (SoT)    │                                  └────────────────────┘
   │  ├─ local eventbus (seq'd)  │
   │  └─ hub client (uplink/dl)  │
   └─────────────────────────────┘
```

- **Host** = one `sprawl enter` process for one repo. The live **claude session
  is the source of truth (SoT)**. The hub never becomes authoritative.
- **Hub** = one deployable Go container: Connect API + optionally-embedded SPA +
  Postgres + blob/secrets. Stateless-ish app tier over a durable store.
- **Browser** = pure static SPA client; just another event-log consumer.

## 2. The event-log spine (the strongest idea — feature it)

Everything rides one **seq'd, resumable event log**. Sprawl already has the hard
part locally: the eventbus is seq-stamped, gap-detecting, and marks terminal
events undroppable (QUM-775, `internal/runtime/eventbus.go`; TUI resync in
QUM-669/QUM-775).

```
claude stream
    │
    ▼
local eventbus  ──(seq 1,2,3,…, gap-detect, terminal-undroppable)──┐
    │                                                              │
    ├──▶ local TUI      (consumer)                                 │
    └──▶ hub client ──uplink──▶ HUB persists log ──fan-out──▶ browser (consumer)
```

### The one rule (written once, reused at every seam)

Every consumer — the local TUI, the hub, a browser, any reconnecting client —
obeys the identical contract:

> **Replay from my last seq. If that seq is no longer available, load a
> snapshot, then live-tail.**

```
on (re)connect:
  if have(last_seq) and hub.has(last_seq+1):   replay(last_seq+1 .. head)   # cheap catch-up
  else:                                          load(snapshot); set last_seq = snapshot.seq
  live-tail(from = last_seq)                                                 # subscribe to new events
```

This is the single most important property of the design: **reconnect logic
exists once** and is reused at each seam (claude→bus, bus→hub, hub→browser). Each
seam is "just another consumer following the one rule." Gap detection and
terminal-event guarantees are inherited from the existing local eventbus rather
than reinvented per seam.

### Why a log (and not RPC-per-update)

- Natural resumability across flaky mobile/NAT connections — the whole point.
- Ordering + gap detection are already solved locally; a log preserves them
  end-to-end.
- New consumer types (a future org-chart watcher, a metrics tap) attach without
  new plumbing.

### Simplest way vs. right way

- **Simplest:** hub stores only the latest snapshot per session; clients always
  full-reload on reconnect. Cost: no cheap catch-up, heavy reloads on every
  mobile blip, lost fine-grained history.
- **Right:** append-only per-session log with periodic snapshots for compaction;
  clients replay the delta. Cost: retention/GC policy + snapshotting.
- **Recommendation:** **append-only log + periodic snapshots.** Snapshots bound
  storage and make cold-start cheap; the delta-replay is what makes reconnect
  feel instant. Retention/GC details → [`07-storage-persistence`](README.md).

## 3. Connected vs. disconnected

Disconnected is the **default and the fallback**, never a degraded mode.

| Aspect | Disconnected (default) | Connected |
|---|---|---|
| Configured? | No `--hub-url`/env/config | Explicit opt-in |
| Behavior | ~100% as today, zero change | Same, plus uplink/downlink |
| SoT | Local claude session | Still local claude session |
| Hub role | — | Broker: fan-out + durable store + auth |
| Failure mode | n/a | Hub down ⇒ host keeps running; buffers/reconnects |

- **No default hub endpoint** ships in the code (public-repo hygiene). The host
  connects only when told to.
- If the hub is unreachable, the host **keeps working locally** and the hub
  client reconnects and replays per the one rule. A hub outage must never stall a
  turn.

### Simplest way vs. right way

- **Simplest:** best-effort uplink; drop events the hub missed while down. Cost:
  browser history has holes after any outage.
- **Right:** bounded local outbound buffer of un-acked events; flush on
  reconnect. Cost: a little disk + a high-water policy.
- **Recommendation:** **bounded local buffer with drop-oldest past a
  high-water mark**, and `log()` when truncation happens. Keeps memory bounded
  while preserving history for realistic outages.

## 4. Identity, lease & fencing (conceptual — detail in doc 10 & 09)

Three identifiers:

| ID | Scope | Meaning |
|---|---|---|
| `host-id` | Stable per machine/install | Which physical origin |
| `run-id` | Per `sprawl enter` process | Which live session instance |
| **write-lease** | Per **project** (schema keyed for per-agent later) | Who currently holds write authority |

The hub tracks per lease: `{holder_host_id, fence_token, last_heartbeat}`.

- **The persistent connection IS the heartbeat.** The lease has a TTL.
- **Stale lease** (TTL expired) → **clean reclaim** by a new claimant.
- **Fresh lease + a different claimant appears** → prompt the user:
  *Stop the current holder* or *Force-reclaim*.
- **Fencing tokens make last-writer-wins safe.** Every write carries the current
  fence token; the hub **rejects writes bearing a stale fence**. A returning
  zombie holder cannot clobber the new one.

```
claim/renew ──▶ hub: {holder_host_id, fence_token=N, last_heartbeat}
write(fence=N)  ──▶ accepted
write(fence<N)  ──▶ REJECTED (stale fence)          # zombie can't clobber
TTL expired     ──▶ next claimant gets fence=N+1
```

### Reconnect = version-vector compare (not textual merge)

On reconnect the host and hub compare version vectors:

- **local ahead + host holds lease** → **push** local delta up.
- **hub ahead** → **pull** hub delta down.
- **genuine divergence** → only reachable via **force-reclaim**, and resolved by
  **provenance-based semantic reconcile**, *never* a textual merge. (Rationale
  and mechanics in [`09-synchronization`](README.md) and
  [`10-memory`](README.md).)

### Simplest way vs. right way

- **Simplest:** single global lock per project, no fence; last connection wins.
  Cost: a zombie/late writer can silently clobber; races on flaky links.
- **Right:** TTL lease + monotonic fence token + version-vector reconnect.
  Cost: a small registry table + fence checks on the write path.
- **Recommendation:** **do the lease + fence now** — it's cheap, and it's the one
  correctness guarantee that's painful to retrofit once real writes exist. Skip
  per-agent lease keying for v1 (schema-key it, enforce per-project).

## 5. Memory (conceptual — detail in doc 10)

- **One logical memory stream per `(project, agent)`.** The **agent name is the
  partition key**; weave is just agent `weave`. Because agent names are unique
  across the org, each stream has a **single writer** by construction — no
  write-contention on memory.
- **Provenance metadata on every unit.** Combining memory is **curation or
  agent-synthesis, never textual merge.** Git is the wrong tool for prose memory
  (line-merge produces incoherent Frankentext); we record that rationale
  explicitly. Portability comes from streaming these units through the same spine
  + store, not from a git history.

## 6. How the pieces fit (request/response paths)

**Uplink (host → hub → browser):**
```
claude event ─▶ eventbus(seq) ─▶ hub client ─▶ Connect uplink ─▶ hub append(log)
                                                                    └─▶ fan-out ─▶ browser
```

**Downlink (browser → hub → host):** "user typed X in the browser"
```
browser input ─▶ Connect RPC ─▶ hub ─▶ downlink on the host's persistent conn
              ─▶ host enqueues into the ONE turn-queue ─▶ claude ─▶ (result re-enters uplink)
```

- **Read fan-out, not multiplayer.** All viewers see the same output stream.
  Multiple typers just enqueue into the single turn-queue; no presence/typing
  UI, no hard driver-lock in v1 — only a lightweight "N clients connected" guard.
- The downlink reuses sprawl's existing message/turn-queue plumbing on the host
  side; the hub only *transports* the input, it doesn't interpret it.

## 7. Stack at a glance (rationale validated in leaf docs)

| Concern | Choice | Note |
|---|---|---|
| API + frontend | Single Go container: Connect (connectrpc) + `go:embed` SPA | Deployable on any container cloud → [`08`](README.md) |
| Transport | Connect + protobuf; **buf** toolchain (codegen, `buf breaking`) | Wire back-compat → [`03`](README.md) |
| DB | Vanilla **managed Postgres** | Log + registry + metadata → [`07`](README.md) |
| Object storage | `gocloud.dev/blob` (memblob/fileblob for tests) | Attachments, snapshots → [`07`](README.md) |
| Secrets | `gocloud.dev/secrets` | Multi-cloud portable |
| Auth | OIDC relying-party (`go-oidc`), IdP is a **deploy parameter**; host→hub PATs (hashed in PG); user allowlist. No BaaS. | → [`04`](README.md) |
| IaC | **Terraform**, `azure/` first, AWS door open; everything parameterized | → [`06`](README.md) |
| Frontend | Pure static SPA (no SSR); framework is open research | → [`11`](README.md) |

> Multi-cloud portability (Azure-first, AWS later) comes from `gocloud.dev` +
> parameterized Terraform, so the app code doesn't bake in a provider. "Azure" is
> a generic public-cloud target here — not attributed to any organization.

## 8. What v1 deliberately excludes (YAGNI)

- Hard driver-lock / multiplayer editing / presence indicators.
- Per-agent write-leases (schema anticipates; enforcement stays per-project).
- The north-star org model (persistent declarative agents, cross-tree work
  requests, org-chart watcher) — see [`00`](00-overview.md#north-star-vision--not-committed--future).
- Any default/hardcoded hub endpoint.

## Open Questions

- **Transport for the persistent bidi conn:** does a long-lived Connect
  bidi-stream survive typical cloud L7 load balancers, or do we need a
  reconnect-friendly framing (server-streaming + separate uplink unary, or
  WebSocket fallback)? (Deferred to [`03-api-surfaces`](README.md), flagged as
  the top viability risk.)
- **Snapshot cadence:** time-based, event-count-based, or hybrid? What bounds
  cold-start latency acceptably for mobile?
- **Fence-token durability:** monotonic counter in Postgres vs. per-lease epoch —
  what survives a hub DB failover cleanly?
- **Version-vector granularity:** per-project, per-(project,agent), or
  per-stream? Affects reconnect chattiness.
- **Turn-queue ordering across sources:** when local TUI *and* browser both
  enqueue, is strict arrival order sufficient, or do we need source tagging for
  UX clarity?
- **Buffer high-water policy:** how much local outbound history to retain during
  a hub outage before drop-oldest, and is that per-session or global?
- **Does the browser ever need write authority itself**, or does it always act
  "as the host" it's attached to (host holds the lease, browser just feeds the
  turn-queue)?
