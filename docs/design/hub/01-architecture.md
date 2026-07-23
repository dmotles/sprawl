# 01 — Architecture

*The north-star architecture. Every other doc conforms to this one.*

See also: [`00-overview.md`](00-overview.md) · [index](README.md)

---

## 0. Framing: a single-user cloud companion (v1)

The hub is a **hosted cloud companion** to the local `sprawl` binary — not a
multi-tenant service. It exists to do exactly two things for v1:

1. **Relay the live activity stream** from a running host to a browser so the
   single user can view progress remotely and feed input back in.
2. **Durably persist** memory, session transcripts, and attachments so they are
   reachable from any machine the user connects from.

Nothing more. It is **single-user**: one person, their hosts, their browsers. We
keep a `user_id` column on durable rows (always the same value) purely as a
cheap flex-later hedge — we do **not** build multi-tenant isolation or
enforcement now. The live claude session on each host remains the source of
truth; the hub is a broker + durable store + thin auth boundary, never
authoritative over how sprawl runs.

## 1. Topology

Hub-and-spoke. Hosts are NAT'd, so a host **dials OUT** to the hub. There is no
single always-open bidirectional socket; instead the host holds a
**continuously-re-established server-stream downlink** (hub → host commands) plus
**unary uplink** calls (host → hub events/memory/attachments). The heartbeat
rides the downlink stream — while it's open, the host is "present." Transport
details and LB viability live in [`03-api-surfaces`](README.md).

```
        ┌─────────────────────── HUB (single Go container) ───────────────────────┐
        │                                                                          │
        │   Connect/protobuf API   ┌────────────┐   embedded static SPA (go:embed) │
        │   ├─ uplink ingest ──────▶│  durable   │◀──── browser fan-out ────┐       │
        │   ├─ downlink stream       │  seq'd log │                          │       │
        │   ├─ auth (bearer token)   │ (Postgres) │   blob store (gocloud)   │       │
        │   └─ active-host marker    └────────────┘   secrets  (gocloud)     │       │
        └───────▲──────────────────▲────────────────────────────────────────┼──────┘
                │ unary uplink      │ server-stream downlink (re-established) │ HTTPS
   ┌────────────┼──────────────────┼┐                                ┌───────┴────────────┐
   │ host A      (single user's hosts)│                              │ browser (laptop)   │
   │  sprawl enter (root=weave)      │                               │ browser (phone)    │
   │  ├─ claude session (SoT)        │                               └────────────────────┘
   │  ├─ local eventbus (seq'd)      │
   │  └─ hub client (uplink + dl)    │
   └─────────────────────────────────┘
```

- **Host** = one `sprawl enter` process for one repo. The live **claude session
  is the source of truth (SoT)**. The hub never becomes authoritative.
- **Hub** = one deployable Go container: Connect API + optionally-embedded SPA +
  Postgres + blob/secrets. Stateless-ish app tier over a durable store.
- **Browser** = pure static SPA client; just another stream consumer.

## 2. The seq'd-stream spine (the strongest idea — feature it)

Everything rides **one durable, seq'd session stream**, and that stream is the
host's **wire log** — a byte-level `io.TeeReader` tap on Claude's stdout, upstream
of all sprawl logic. The durable session **transcript IS the seq'd wire log** —
there is no separate ephemeral event-log plus snapshot layer.

**Two seq spaces, two jobs** (do not conflate them):

- **Eventbus seq** (QUM-775, `internal/runtime/eventbus.go`): in-process,
  per-runtime, gap-detecting, terminal-undroppable. It drives **live local
  delivery** to the running TUI (viewport, activity ring) and local gap-resync.
  Unchanged by the hub.
- **Wire-log frame seq** (the durable authoritative transcript): the target
  wire-log writer is **frame-oriented — one envelope per Claude protocol frame —
  with a persistent monotonic seq continuous across resumes**. This is the
  **single source of record**: the hub uplink tails it, the hub stores it verbatim,
  and the browser keys its cursor on it. It is also the TUI resume/replay source
  (retiring the prior Claude-JSONL read path, `internal/tui/replay.go`).

```
claude stream
    │
    ▼
wire-log tap  ──(frame-oriented, persistent monotonic seq 1,2,3,… across resumes — the durable source of record)──┐
    │                                                                                                              │
    ├──▶ local eventbus ──(per-runtime seq, gap-detect, terminal-undroppable)──▶ local TUI   (live delivery)       │
    └──▶ hub client TAILS the wire log ──uplink──▶ HUB appends to durable seq'd log ──▶ browser (cold-join + live-tail)
```

### The one rule (written once, reused at every seam)

Every consumer — the local TUI, the hub, a browser, any reconnecting client —
obeys the identical contract:

> **Fresh connect → get the full seq'd log. Reconnect → send my last seq, get the
> delta. Then live-tail.**

```
on connect:
  if have(last_seq):   replay(last_seq+1 .. head)     # reconnect: cheap delta
  else:                replay(0 .. head)               # fresh connect: full log
  live-tail(from = head)                               # subscribe to new events
```

This is the single most important property of the design: **reconnect logic
exists once** and is reused at each durable seam (claude→wire-log, wire-log→hub,
hub→browser). Each seam is "just another consumer following the one rule." Gap
detection and terminal-event guarantees for the *live* local path are inherited
from the existing eventbus rather than reinvented per seam; the *durable* cursor
every seam keys on is the wire-log frame seq.

New events tail live over a Connect **server-stream**, with a **WebSocket
fallback** where L7 infrastructure doesn't hold server-streams open (see
[`03`](README.md)).

### Why a log (and not RPC-per-update)

- Natural resumability across flaky mobile/NAT connections — the whole point.
- Ordering + gap detection are already solved locally; a log preserves them
  end-to-end.
- New consumer types (a future org-chart watcher, a metrics tap) attach without
  new plumbing.

### Why the wire log (and not Claude's JSONL)

- **Complete by construction.** The tap sits upstream of every piece of
  sprawl-side logic, so no downstream bug can make it miss a byte Claude emitted. A
  forensic audit confirmed the wire log is a **faithful superset** of Claude's
  canonical JSONL — 0 real model content lost across 195 session pairs; every
  assistant message, tool call, user message, and even the compaction summary text
  + boundary metadata cross the wire. **No Claude-JSONL reconciliation is required**
  for model-visible content; the only JSONL-exclusive residue (a synthetic
  `"No response requested."` placeholder; the `isCompactSummary` flag / uuid map) is
  non-essential.
- **More durable than the JSONL.** The wire log lives under sprawl's own control
  and outlived Claude's transcript in 387 of 582 observed cases (Claude had GC'd its
  JSONL while the wire log survived).

### No snapshots in v1 (KISS)

- **Simplest:** fresh connect replays the full log; reconnect replays the delta.
  Cost: a very large session could make a fresh cold-start slow.
- **Right (later):** periodic snapshots for compaction so cold-start is bounded.
  Cost: snapshotting + a compaction/retention policy.
- **Recommendation:** **ship without snapshots.** Defer them until a genuinely
  giant session proves cold-start slow — then add snapshots behind the same "one
  rule" without changing consumers. Storage is kept indefinitely (no GC), so the
  log is always complete.

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

## 4. Identity & active-host marker (conceptual — detail in doc 09)

Three identifiers:

| ID | Scope | Meaning |
|---|---|---|
| `host-id` | Stable per machine/install | Which physical origin |
| `run-id` | Per `sprawl enter` process | Which live session instance |
| `user_id` | Constant (single-user) | Flex-later hedge; always one value in v1 |

Write authority is **trivial and advisory** in v1 — no leases, no fence tokens,
no epochs:

- The hub records a single **active-host marker** per project:
  `{active_host_id, active_run_id, last_seen}`. The downlink stream keeps
  `last_seen` fresh.
- When a **second host** tries to become active for the same project, the hub
  rejects it with a clear, actionable message:
  **"another host is active — stop it or reclaim."**
- **Reclaim** is a deliberate user action that flips the marker to the new host.
  There is no automatic contention resolution, no fence-token math, no
  last-writer-wins race to reason about — single-user means real conflicts are
  rare and a human is always in the loop.

```
host A active  ──▶ hub: {active_host_id=A, active_run_id, last_seen}
host B connects ─▶ hub: REJECT → "another host is active — stop or reclaim"
user reclaims   ─▶ hub: {active_host_id=B, …}      # deliberate, human-driven
```

### Simplest way vs. right way

- **Simplest (chosen):** one advisory active-host marker + a clear reject/reclaim
  message. Cost: no protection against a truly adversarial concurrent writer —
  acceptable for single-user.
- **Right (later, if multi-tenant):** TTL leases + monotonic fence tokens +
  version-vector reconnect. Cost: a registry + fence checks on every write.
- **Recommendation:** **ship the advisory marker.** Fence/lease machinery only
  earns its complexity once multiple independent users (or automated writers)
  can race — which is out of scope until multi-tenant is real.

## 5. Memory (conceptual — detail in doc 10)

- **One logical memory stream per `(project, agent)`.** The **agent name is the
  partition key**; weave is just agent `weave`. Because agent names are unique
  across the org, each stream has a **single writer** by construction.
- **Sync is simple — last-writer-wins:**
  - **write local always** — the host writes memory locally as it does today;
  - **checkpoint push on handoff** — memory is pushed up at handoff boundaries;
  - **pull on start** — a starting host pulls the latest from the hub.

  Single-writer-by-agent-name makes **last-writer-wins safe**: two hosts don't
  legitimately write the same agent's memory at once. There is **no
  version-vector reconnect, no force-reclaim, no semantic reconcile engine** in
  v1.
- **Provenance metadata stays on every unit** (who/when/source) — it's cheap and
  useful for later curation — but v1 does no reconcile with it. Git remains the
  wrong tool for prose memory (line-merge produces incoherent Frankentext); we
  record that rationale so nobody reaches for a textual merge later.

## 6. How the pieces fit (request/response paths)

**Uplink (host → hub → browser):**
```
claude frame ─▶ wire-log tap(frame seq) ─┬▶ eventbus ─▶ local TUI (live delivery)
                                         └▶ hub client TAILS ─▶ Connect unary uplink ─▶ hub append(log)
                                                                                        └─▶ fan-out ─▶ browser
```

**Downlink (browser → hub → host):** "user typed X in the browser"
```
browser input ─▶ Connect RPC ─▶ hub ─▶ downlink server-stream to the active host
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
| DB | Vanilla **managed Postgres**; Store interface + **goose** migrations | Log + marker + metadata → [`07`](README.md) |
| Object storage | `gocloud.dev/blob` (memblob/fileblob for tests) | Attachments → [`07`](README.md) |
| Secrets | `gocloud.dev/secrets` | Multi-cloud portable |
| Auth | **One configured bearer token** (a secret): browser presents it once → httpOnly session cookie; host uses the same bearer-token style. No OIDC, no BaaS. | → [`04`](README.md) |
| IaC | **Terraform**, `azure/` first, AWS door open; everything parameterized | → [`06`](README.md) |
| Frontend | Pure static SPA (no SSR); framework is open research | → [`11`](README.md) |

> Multi-cloud portability (Azure-first, AWS later) comes from `gocloud.dev` +
> parameterized Terraform, so the app code doesn't bake in a provider. "Azure" is
> a generic public-cloud target here — not attributed to any organization.

> **Auth rationale:** single-user means we don't need federated identity. One
> configured bearer token is the smallest thing that provides an auth boundary;
> the browser trades it for an httpOnly session cookie so the token isn't
> re-sent per request, and the host authenticates with the same bearer-token
> style. OIDC (`go-oidc`) returns only when multi-tenant is real.

## 8. What v1 deliberately excludes (YAGNI)

- **OIDC / federated identity** — bearer token only until multi-tenant.
- **Multi-tenant isolation & enforcement** — single-user; `user_id` column is a
  hedge, not enforced.
- **Fence tokens / lease epochs / TTL leases** — replaced by the advisory
  active-host marker.
- **Version-vector reconnect, force-reclaim, semantic reconcile** — memory is
  last-writer-wins.
- **Snapshots / log compaction** — full-log replay for now.
- **GC / retention windows** — transcripts, attachments, and memory kept
  indefinitely.
- **Client-side (zero-knowledge) encryption & per-project content opt-out** —
  single-user + TLS + provider-at-rest is enough for v1; ZK/opt-out documented as
  future (see security-privacy doc).
- Hard driver-lock / multiplayer editing / presence indicators.
- The north-star org model (persistent declarative agents, cross-tree work
  requests, org-chart watcher) — see [`00`](00-overview.md#north-star-vision--not-committed--future).
- Any default/hardcoded hub endpoint.

## Open Questions

- **Downlink transport under cloud LBs:** how long do typical L7 load balancers
  hold a Connect server-stream open, and how aggressive must the
  re-establish/backoff be? When is the WebSocket fallback required vs. optional?
  (Deferred to [`03-api-surfaces`](README.md), flagged as the top viability risk.)
- **Full-log cold-start ceiling:** at what session size does full-log replay
  become noticeably slow on mobile, i.e. when do snapshots stop being deferrable?
- **Active-host reclaim UX:** where does the "another host is active — stop or
  reclaim" prompt surface (CLI on the second host, browser, both), and what does
  "stop" do to the first host's running turn?
- **Bearer-token rotation:** how is the single token rotated without downtime,
  and is a second valid token allowed during rotation?
- **Turn-queue ordering across sources:** when local TUI *and* browser both
  enqueue, is strict arrival order sufficient, or do we need source tagging for
  UX clarity?
- **Buffer high-water policy:** how much local outbound history to retain during
  a hub outage before drop-oldest, and is that per-session or global?
- **`user_id` hedge scope:** which durable tables carry the column now so a later
  multi-tenant migration is a schema no-op rather than a rewrite?
