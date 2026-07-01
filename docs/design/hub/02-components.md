# 02 — Components

*Breakdown of hub-side services and host-side agent additions. Conforms to
[`01-architecture.md`](01-architecture.md).*

See also: [`00-overview.md`](00-overview.md) · [`03-api-surfaces`](README.md) ·
[`04-authentication`](README.md) · [`07-storage-persistence`](README.md) ·
[`09-synchronization`](README.md) · [`12-testability-local-dev`](README.md) ·
[index](README.md)

---

## 0. Reading guide

This doc names the moving parts and draws boundaries between them. It does **not**
specify wire formats (→ [`03`](README.md)), auth flows (→ [`04`](README.md)),
schema (→ [`07`](README.md)), or reconnect mechanics (→ [`09`](README.md)) — it
points at those docs where a part needs them.

Two deployables, one spine:

- **Hub** — a *single Go container* holding several internal services.
- **Host** — the existing `sprawl enter` process, plus *one additive, optional*
  hub client.
- **Frontend SPA** — a pure static event-log consumer, embedded in the hub
  binary or served separately.

> **One binary, many parts.** Everything under §1 compiles into the one hub
> container and everything under §2 into the one `sprawl enter` binary. Naming
> them as distinct components is a *seam* discipline, not a process boundary —
> each is wired behind an interface so it can be faked and tested in isolation
> (→ [`12`](README.md)). "Separable for testing, colocated for deploy."

```
        ┌──────────────── HUB container ────────────────┐
        │  Connect API server                           │
        │   ├─ uplink ingest ─┐        ┌─ lease/fence    │
        │   ├─ downlink disp.  │        │   registry     │
        │   └─ auth (OIDC+PAT) │        │                │
        │        ┌─────────────▼────────▼────────┐       │
        │        │  event-log store (interface)  │       │
        │        └───────────────────────────────┘       │
        │  blob store (gocloud)   secrets (gocloud)      │
        │  optionally-embedded SPA (go:embed)            │
        └───────▲───────────────────────────────┼───────┘
      uplink /  │ persistent bidi conn          │ HTTPS (static + API)
      downlink  │                               ▼
        ┌───────┴──────────────────┐     ┌──────────────┐
        │ host: sprawl enter        │     │ browser SPA  │
        │  ├─ eventbus (existing)   │     │ (consumer)   │
        │  └─ hub client (NEW):     │     └──────────────┘
        │      subscriber · uplink  │
        │      sender · downlink rx │
        │      buffer · lease claim │
        └───────────────────────────┘
```

---

## 1. Hub-side components (inside the one container)

### 1.1 Connect API server

**Responsibility.** Terminate all client traffic: the hosts' persistent bidi
connections and the browsers' HTTPS/API calls. Owns the RPC service definitions
and dispatches each call into the right internal component. It is the only part
that touches the network.

**Boundary.** Transport + routing only. It does *not* interpret event payloads,
make lease decisions, or run auth logic itself — it calls the auth, registry, and
store components. Wire shapes, streaming vs. unary, and LB survivability are
[`03`](README.md)'s problem; this component just hosts whatever service that doc
defines.

- Serves: uplink ingest RPC, downlink dispatch, and browser read/fan-out
  subscription.
- Serves the SPA's static assets too when embedded (§1.8), on the same listener.

**Simplest vs. right.** Simplest: one big service with all RPCs and hand-rolled
JSON. Right: `buf`-managed protobuf services split by concern (ingest / dispatch
/ read), codegen'd, with `buf breaking` in CI. **Recommendation:** protobuf +
`buf` from day one — the wire is the one contract with independently-deployed
browsers and long-lived hosts; back-compat pain is expensive to retrofit. Keep it
to a *small* number of services, not one-per-verb.

### 1.2 Uplink ingest

**Responsibility.** Receive event-log frames pushed up each host's connection,
authenticate/authorize them (delegating to §1.4), **check the fence token**
(delegating to §1.5), and — if valid — append them to the event-log store
(§1.6), then hand them to fan-out (§1.3).

**Boundary.** The append-side of the spine. It enforces exactly one invariant of
its own: *events are appended in seq order per session, and a frame bearing a
stale fence is rejected before it touches the store* (the zombie-writer guard
from [`01` §4](01-architecture.md)). Everything else it delegates.

- Idempotent on re-delivered seqs (a reconnecting host may re-send un-acked
  frames from its buffer — §2.5); dedupe by `(session, seq)`.
- Emits an ack (last-persisted seq) so the host can advance its buffer.

### 1.3 Downlink dispatch (+ fan-out)

**Responsibility.** Two directions of "push":

1. **Fan-out to browsers** — after ingest appends an event, push it to every
   browser currently live-tailing that session.
2. **Downlink to the host** — carry browser input ("user typed X") back down the
   *originating host's* persistent connection so the host can enqueue it into its
   one turn-queue.

**Boundary.** Pure transport of already-formed frames. It does **not** interpret
browser input — per [`01` §6](01-architecture.md), the hub only *transports* the
input; the host decides what it means. Fan-out is read-only replication of the
log; it never mutates state. Routing a downlink to the right host is a lookup in
the lease/connection registry (§1.5).

**Simplest vs. right.** Simplest: fan-out only to currently-connected browsers,
no per-browser cursor — a browser that blips reloads from snapshot. Right: each
browser is "just another consumer following the one rule" ([`01` §2](01-architecture.md)),
resuming from its last seq. **Recommendation:** implement the one-rule
resume for browsers too — it's the *same* code path as every other seam, so it's
cheaper to reuse than to special-case, and it makes mobile reconnects feel
instant.

### 1.4 Auth (OIDC relying-party + PAT verify)

**Responsibility.** Two distinct trust checks, one component:

- **Browsers → OIDC.** The hub is an OIDC *relying party* (via `go-oidc`);
  the IdP is a **deploy parameter**, never hardcoded. Validates the user is on
  the allowlist.
- **Hosts → PAT.** Each host authenticates its persistent connection with a
  Personal Access Token, verified against **hashed** tokens in Postgres.

**Boundary.** Answers "who is this caller and are they allowed?" and nothing
else. It does not decide *write authority* — that is the lease's job (§1.5).
Auth gates the *connection*; the lease gates the *write*. Full flows, token
lifecycle, allowlist model → [`04`](README.md).

**Simplest vs. right.** Simplest: a single shared secret for hosts and
basic-auth for the browser. Right: per-host hashed PATs + real OIDC. **Recommendation:**
do real OIDC + hashed PATs now — this is the auth boundary that justifies the
hub existing; a shared secret can't be revoked per-host and leaks badly in a
public repo's ops story.

### 1.5 Lease / fence registry

**Responsibility.** Track, per **project** (schema keyed for per-agent later),
`{holder_host_id, fence_token, last_heartbeat}`. Grant/renew leases, hand out
monotonic fence tokens, expire stale leases (TTL), and answer "which host holds
write authority for project P, and on which connection do I reach it?" for
downlink routing (§1.3).

**Boundary.** The write-authority + connection-directory brain. It decides
*who may write*; the ingest path (§1.2) *enforces* that decision by checking the
fence on each frame. It does not store events (§1.6) and does not authenticate
(§1.4). Reclaim/force-reclaim/version-vector reconcile mechanics live in
[`09`](README.md); this component just holds the registry state those flows read
and write.

- **The persistent connection IS the heartbeat** ([`01` §4](01-architecture.md)):
  connection alive → lease renewed; connection dropped → TTL starts ticking.
- Emits the "fresh lease + different claimant" signal that prompts the user
  (*Stop current holder* / *Force-reclaim*).

**Simplest vs. right.** Simplest: one global lock per project, last-connection-wins,
no fence. Right: TTL lease + monotonic fence + version-vector reconnect. **Recommendation:**
lease + fence now (echoing [`01` §4](01-architecture.md)) — it's a small table and
a cheap check, and it's the single correctness guarantee that's painful to
retrofit once real writes exist. Enforce per-project; schema-key for per-agent.

### 1.6 Event-log store (interface)

**Responsibility.** Persist the append-only, per-session seq'd event log and the
periodic snapshots that compact it; serve three reads: *append*, *replay from
seq*, *load latest snapshot*. This is the durable half of the spine.

**Boundary.** Defined as a **Go interface**, with vanilla managed Postgres as
the production impl and an in-memory impl for tests (→ [`12`](README.md)). It
stores bytes+seq+metadata; it does not know what an event *means*, does not do
auth, does not do fan-out. Retention/GC, snapshot cadence, and concrete schema
are [`07`](README.md)'s domain.

**Simplest vs. right.** Simplest: store only the latest snapshot per session;
clients always full-reload. Right: append-only log + periodic snapshots, clients
replay the delta ([`01` §2](01-architecture.md)). **Recommendation:** append-only
log + periodic snapshots behind the interface — the delta-replay is what makes
reconnect feel instant, and the interface keeps the impl swappable and testable.

### 1.7 Blob store & secrets (gocloud)

**Responsibility.**

- **Blob** (`gocloud.dev/blob`): large/opaque payloads that don't belong inline
  in the log — attachments/screenshots (→ attachments doc) and possibly
  snapshot bodies (→ [`07`](README.md)).
- **Secrets** (`gocloud.dev/secrets`): runtime secret material (e.g. PAT hashing
  pepper, OIDC client secret) resolved through a portable interface.

**Boundary.** Thin portability adapters. They exist so *no cloud provider is
baked into app code* — memblob/fileblob and a local secrets impl back the tests
and local-dev hub. Multi-cloud (Azure-first, AWS door open) comes from here +
parameterized Terraform (→ [`06`](README.md)), not from provider SDK calls in
business logic.

**Simplest vs. right.** Simplest: local filesystem for blobs, env vars for
secrets, no abstraction. Right: `gocloud` interfaces with local backends in
dev/test and cloud backends in prod. **Recommendation:** `gocloud` from the
start — the abstraction cost is ~nil (it *is* the local-dev backend too), and it
keeps the multi-cloud promise real without provider lock-in.

### 1.8 Optionally-embedded SPA

**Responsibility.** Ship the frontend's built static assets inside the hub
binary via `go:embed` and serve them from the Connect listener, so the hub is a
single deployable artifact.

**Boundary.** Serving bytes only — the hub has *zero* coupling to the SPA's
framework or internals; it hands the browser HTML/JS/CSS and the browser then
speaks the same public API as any other client. "Optionally" embedded: a
separate static host/CDN is a valid deploy too (→ [`08`](README.md)), so this is
a build/deploy convenience, not an architectural dependency.

**Simplest vs. right.** Simplest: `go:embed` the SPA — one container, one deploy.
Right (at scale): CDN-served SPA, hub is API-only. **Recommendation:** embed for
v1 (one artifact, trivially matches API+asset versions), keep the API surface
CDN-friendly so splitting later is a deploy change, not a code change.

---

## 2. Host-side additions to `sprawl enter` (the hub client)

> **Additive and optional — the load-bearing constraint.** Everything in §2 is
> dormant unless a hub URL is configured. With no `--hub-url`/env/config there is
> **zero behavior change**: no connection attempt, no new goroutines on the hot
> path, no new failure modes. Disconnected is the default and the fallback, never
> a degraded mode ([`01` §3](01-architecture.md)). A hub outage must never stall
> a turn.

The hub client is one cohesive unit inside `sprawl enter`, made of the parts
below. It reuses existing host machinery wherever possible rather than adding
parallel plumbing.

### 2.1 Eventbus subscriber (another consumer)

**Responsibility.** Subscribe to the **existing** per-runtime `EventBus`
(`internal/runtime/eventbus.go`) as *one more consumer* alongside the TUI
viewport, activity ring, and log writers. Receive seq-stamped `RuntimeEvent`s
and feed them to the uplink sender (§2.2).

**Boundary.** A read-only tap on the local spine. It adds a subscriber; it does
**not** change how the bus publishes, and it inherits the bus's existing
guarantees for free: seq stamping, gap detection, terminal-event
undroppability, and the per-subscriber backpressure/drop telemetry described in
the eventbus package doc. Being "just another subscriber" is exactly why the
uplink seam obeys the same one-rule contract as the TUI.

**Simplest vs. right.** Simplest: reuse the existing subscriber mechanism as-is.
Right: same, plus honoring the bus's drop telemetry so an over-slow uplink is
observable (not silently lossy). **Recommendation:** reuse the subscriber API
unchanged and surface uplink drops via the existing telemetry — no new bus
concepts, and lossiness stays visible.

### 2.2 Uplink sender

**Responsibility.** Push events received from §2.1 up the persistent connection
to the hub's ingest (§1.2), tracking the last hub-acked seq and advancing the
outbound buffer (§2.5) as acks arrive.

**Boundary.** The host end of the append path. It frames + sends + tracks acks;
it does not decide *what* is an event (the bus did) and does not retry-forever in
a way that blocks the turn — unsendable events go to the bounded buffer and the
connection layer reconnects. Attaches the current fence token to each frame so
the hub can enforce write authority.

### 2.3 Downlink receiver → the ONE turn-queue

**Responsibility.** Receive downlink frames (browser input transported by
§1.3) and enqueue them into sprawl's **single existing turn-queue** — the same
queue the local TUI feeds — so browser-originated and locally-typed input
converge on one ordered stream into claude.

**Boundary.** This is the critical reuse point: **it does not build a second
input path.** It adapts a hub frame into the existing message/turn-queue plumbing
and stops there. Read fan-out, not multiplayer ([`01` §6](01-architecture.md)):
multiple typers just enqueue; there's no presence, no driver-lock in v1, only the
lightweight "N clients connected" guard. Turn-queue ordering across sources is
flagged as an open question in [`01`](01-architecture.md).

**Simplest vs. right.** Simplest: enqueue downlink input with no source tag.
Right: tag input with its source for UX clarity ("from browser (phone)"). **Recommendation:**
enqueue into the one queue now (correctness); add optional source tagging only if
double-driving proves confusing — YAGNI until observed.

### 2.4 Lease claim / heartbeat

**Responsibility.** On connect, claim the per-project write-lease from the
registry (§1.5), receive the fence token (used by §2.2), and keep it alive.
Surface the "different claimant" prompt to the user (*Stop* / *Force-reclaim*)
and handle the outcome.

**Boundary.** The host end of write-authority. Crucially, **the persistent
connection IS the heartbeat** ([`01` §4](01-architecture.md)) — there is no
separate heartbeat ping to maintain; keeping the connection up *is* renewing the
lease. This component owns claiming/relinquishing and holding the current fence,
not the reconnect/reconcile algorithm (→ [`09`](README.md)).

### 2.5 Bounded local outbound buffer

**Responsibility.** Hold un-acked outbound events while the hub is unreachable
or slow, and flush them (in seq order) on reconnect, so a hub blip doesn't punch
holes in browser history.

**Boundary.** A bounded queue with a **drop-oldest past a high-water mark**
policy, and a `log()` whenever truncation happens ([`01` §3](01-architecture.md)).
It bounds memory; it is *not* a durable store and makes no delivery guarantee
beyond "best-effort within the high-water window." The exact high-water size and
whether it's per-session or global is an open question in [`01`](01-architecture.md).

**Simplest vs. right.** Simplest: best-effort send, drop anything the hub misses
while down. Right: bounded buffer, flush on reconnect, drop-oldest with a log.
**Recommendation:** bounded buffer with drop-oldest + truncation logging — keeps
memory bounded while preserving history across realistic outages, and the log
makes any gap explicit rather than silent.

### 2.6 `--hub-url` / config / env plumbing

**Responsibility.** The single opt-in switch that turns the whole hub client on.
Resolve a hub URL + PAT from (precedence, highest first) an explicit
`--hub-url` flag, then env, then `.sprawl/config.yaml`. When unresolved, the
client is entirely inert.

**Boundary.** Configuration only. It has **no default value** — there is *no
hardcoded hub endpoint* anywhere in the code (public-repo hygiene,
[`01` §3](01-architecture.md)). Absent config ⇒ §2.1–§2.5 never start. PAT
material is read via the secrets path, never committed. Auth specifics →
[`04`](README.md).

**Simplest vs. right.** Simplest: a single `--hub-url` flag. Right: flag + env +
config-file with clear precedence and a redacted "connected to <host>" status
line. **Recommendation:** support all three sources (matches sprawl's existing
config conventions) but keep the *default* firmly empty; log the resolved
endpoint (host only, token redacted) so operators can see what's on.

---

## 3. Frontend SPA (just an event-log consumer)

**Responsibility.** Render a live view of one or more sessions and let the user
type input back. It **replays from its last seq, else loads a snapshot, then
live-tails** — the identical one-rule contract every other consumer follows
([`01` §2](01-architecture.md)). Input it collects is sent to the hub and
transported down to the host's turn-queue (§1.3 → §2.3).

**Boundary.** A pure client. It holds **no authority** and is **not** a source of
truth — it never writes to the store directly, never holds a lease (open question
in [`01`](01-architecture.md): does the browser ever need write authority, or
does it always act "as the host" it's attached to?). It speaks only the public
API. Framework selection is deliberately deferred to [`11`](README.md); this
component's contract is just "event-log consumer + input producer," independent of
framework.

**Simplest vs. right.** Simplest: full-reload on every (re)connect, no local seq
cursor. Right: one-rule resume with snapshot + delta. **Recommendation:** one-rule
resume — same contract as the TUI and hub, and it's what makes mobile usable.

---

## 4. Component / responsibility matrix

| Component | Lives in | Reads spine | Writes spine | Auth? | Authority? | Faked in tests via |
|---|---|---|---|---|---|---|
| Connect API server | hub | — | — | routes to §1.4 | — | httptest / bufconn |
| Uplink ingest | hub | — | append | delegates | enforces fence | in-mem store |
| Downlink dispatch/fan-out | hub | tail | — | delegates | — | in-mem registry |
| Auth (OIDC+PAT) | hub | — | — | **yes** | connection gate | fake IdP / static PAT |
| Lease/fence registry | hub | — | — | — | **decides** | in-mem registry |
| Event-log store | hub | replay/snap | append | — | — | in-mem impl |
| Blob/secrets | hub | — | — | — | — | memblob/fileblob |
| Embedded SPA | hub | — | — | — | — | serve from disk |
| Eventbus subscriber | host | tap | — | — | — | fake bus |
| Uplink sender | host | — | send | carries PAT/fence | — | fake conn |
| Downlink receiver | host | — | → turn-queue | — | — | fake conn |
| Lease claim/heartbeat | host | — | — | — | holds fence | fake registry |
| Outbound buffer | host | — | — | — | — | direct unit test |
| `--hub-url` plumbing | host | — | — | — | — | env/flag/config injection |
| Frontend SPA | browser | replay+tail | via downlink | OIDC | **none** | mock transport |

Every "faked via" column entry is why these components are named separately even
though they ship in two binaries — each is behind an interface so it can be
exercised in isolation. The end-to-end local story (real hub + in-memory
backends + fakes) lives in [`12-testability-local-dev`](README.md).

---

## Open Questions

- **Uplink batching vs. per-event send.** Does §2.2 send each event
  individually, or coalesce into batches for mobile-link efficiency? Batching
  interacts with ack granularity and the buffer (§2.5) high-water accounting.
- **Where does the "N clients connected" guard live** — is it purely a §1.5
  registry count surfaced to §2.3/the SPA, or does the host enforce anything?
- **Snapshot production ownership.** Does the host produce snapshots and upload
  them, or does the hub compact the log into snapshots server-side? Affects
  whether §2 grows a snapshotter or it stays entirely hub-side ([`07`](README.md)).
- **Does the eventbus subscriber (§2.1) need its own backpressure policy** distinct
  from the TUI's, given the uplink can be far slower than a local consumer? Or is
  the existing per-subscriber drop+telemetry sufficient?
- **Multi-session fan-out in one browser.** The single-pane-of-glass goal implies
  a browser tailing *many* hosts at once — is that N independent one-rule
  consumers in the SPA, or a hub-side multiplexed stream? (Touches [`03`](README.md).)
- **Config reload.** Can `--hub-url`/PAT change without restarting `sprawl enter`
  (hot-attach to a hub mid-session), or is hub attachment fixed at process start?
