# 09 — Synchronization

*The resumable event-log mechanics: how a client (TUI, hub, browser) catches up
after any disconnect, and how write-authority stays safe across reconnects.*

See also: [`01-architecture.md`](01-architecture.md) (event-log spine, lease/fence) ·
[`10-memory`](README.md) (provenance reconcile) · [index](README.md)

---

## 0. The one rule (written once, reused at every seam)

This whole document is the elaboration of **one contract**, stated in
[`01`](01-architecture.md#the-one-rule-written-once-reused-at-every-seam):

> **Replay from my last seq. If that seq is no longer available, load a
> snapshot, then live-tail.**

```
on (re)connect:
  if have(last_seq) and peer.has(last_seq+1):  replay(last_seq+1 .. head)   # cheap catch-up
  else:                                          load(snapshot); last_seq = snapshot.seq
  live-tail(from = last_seq)                                                 # subscribe to new
```

Everything below — retention windows, snapshot cadence, ack watermarks, fences,
version vectors, force-reclaim — exists to make this single rule correct and
cheap at **every seam**:

```
claude ─▶ local eventbus ─▶ hub client ─uplink─▶ HUB ─fan-out─▶ browser
         └── seam 1 ──┘    └──── seam 2 ────┘        └── seam 3 ──┘
             (local)         (host→hub, WAN)          (hub→browser, WAN)
```

**This reuse is the spine of the design.** We do not write per-seam catch-up
logic. Seam 1 already exists (`internal/runtime/eventbus.go`: seq-stamped,
gap-detecting, terminal-undroppable — QUM-669/QUM-775). Seams 2 and 3 are *the
same consumer* pointed at a durable buffer instead of an in-process channel. If
you find yourself writing bespoke reconnect code for a seam, stop — you are
diverging from the spine.

---

## 1. Seq stamping — reused, not reinvented

The local eventbus stamps a **monotonic, 1-indexed `Seq`** on every
`RuntimeEvent` under `publishMu`, guaranteeing subscribers observe strictly
ascending seqs (`EventBus.Publish`, `eventbus.go:364`). Gap detection
(`last_seq+1 != seq`) and terminal-event undroppability are already solved there.

The hub extends this **without re-stamping**. Two rules keep it coherent:

- **`(run-id, seq)` is the global event identity.** `run-id` is per
  `sprawl enter` process ([`01` §4](01-architecture.md)). A host's local seq
  space resets to 1 on every new `run-id`; pairing with `run-id` keeps the hub's
  log unambiguous across host restarts. The hub never renumbers — it stores the
  host's seq verbatim.
- **Per-stream monotonicity is the only invariant.** The hub appends events for
  a given `run-id` in seq order and rejects out-of-order/duplicate seqs
  idempotently (§4). It does **not** impose a global order across runs; consumers
  that watch multiple runs interleave by arrival, exactly as the browser does
  today for multiple agents.

### Simplest way vs. right way
- **Simplest:** hub assigns its own seq on ingest. Cost: two seq spaces to
  reconcile, and the host can no longer answer "did my event N land?" without a
  translation table.
- **Right:** carry `(run-id, host-seq)` end-to-end; hub stores verbatim.
- **Recommendation:** **carry it end-to-end.** The host's seq is already
  authoritative and durable-in-spirit; a second numbering buys nothing and
  breaks ack symmetry (§4).

---

## 2. Durable replay buffer + retention window

The hub keeps a **per-`run-id` append-only log** (Postgres; schema in
[`07`](README.md)). This is the "durable store" half of "broker, not brain" —
it is a **cache of the host's truth**, safe to lose (a reconnecting host
re-establishes a snapshot), never authoritative.

```
run-id=R:  [seq 1][seq 2]…[seq K  ]…………………[seq HEAD]
                          └ trim floor ┘        └ live tail
                            (min ack     retention window = HEAD - floor
                             watermark)
```

- **Retention floor** = the lowest seq any *still-relevant* consumer might
  replay from. Trimmed forward by ack watermarks (§4) and snapshot creation
  (§3). Below the floor, `peer.has(last_seq+1)` is false and the client falls
  back to a snapshot — the one rule handles it automatically.
- **Retention window** bounds storage. It is a policy, not a correctness
  property: shrinking it only forces more snapshot-fallbacks, never data loss
  (the snapshot always covers the gap).

### Simplest way vs. right way
- **Simplest:** keep the full log forever. Cost: unbounded Postgres growth;
  every session's entire history retained even after everyone has caught up.
- **Right:** trim to `min(ack watermarks, latest snapshot seq)`, retain a small
  safety margin above the floor for late/slow reconnectors.
- **Recommendation:** **trim to the ack/snapshot floor + a fixed safety margin**
  (e.g. keep events for a configurable grace window past the floor). KISS: one
  GC pass driven by watermarks; no per-consumer bookkeeping beyond a watermark
  row.

---

## 3. Snapshot cadence + snapshot fallback

A **snapshot** is a materialized "state as of seq S" for a `run-id`: enough to
render the session without replaying from seq 1. Snapshots (a) bound cold-start
cost and (b) let the retention floor advance without stranding cold clients.

**When a client falls back to a snapshot** (per the one rule): `last_seq` is
missing or below the retention floor. The client loads the newest snapshot with
`snapshot.seq ≤ head`, sets `last_seq = snapshot.seq`, then live-tails the delta.
A brand-new browser (no `last_seq`) always takes this path — it *is* the
mobile-cold-start path.

### Cadence: hybrid (recommended)

The foundation asks: time-based, event-count, or hybrid, and what bounds mobile
cold-start? Answer: **hybrid — snapshot when `events_since_last ≥ N` OR
`time_since_last ≥ T`, whichever first, and never more often than a floor
interval.**

- **Event-count (`N`) bounds replay cost.** After a snapshot, a cold client
  replays at most `N` events. Pick `N` so that replaying `N` events over a slow
  mobile link stays under the cold-start budget.
- **Time (`T`) bounds staleness for low-traffic sessions.** An idle session that
  emitted 3 events in an hour still gets a periodic snapshot so its floor can
  advance and its cold-start stays a single fetch.
- **Floor interval** prevents snapshot storms during bursty output.

**Mobile cold-start bound.** Target: a phone opening a session cold does **one
snapshot fetch + at most `N` event replay**. Make `N` and snapshot size the
tuned knobs; the *shape* (one fetch + bounded delta) is fixed. We deliberately
do **not** commit numeric SLOs here — they belong in load-testing during the MVP
([`13`](README.md)) — but the design guarantees cold-start is O(1 snapshot +
≤N events), never O(full history).

### Simplest way vs. right way
- **Simplest:** no snapshots; always replay from seq 1 (or keep only "latest
  full state," full-reload every reconnect). Cost: cold-start grows unbounded
  with session length; a long weave session becomes unopenable on mobile.
- **Right:** periodic snapshots + delta replay.
- **Recommendation:** **hybrid cadence.** Event-count alone starves idle
  sessions of floor-advancement; time alone lets a bursty session accumulate a
  huge replay tail. Hybrid is a two-line predicate and covers both.

> **Snapshot content is out of scope here** — what a snapshot materializes
> (rendered transcript vs. reducer state) is a [`07-storage`](README.md) /
> frontend concern. Synchronization only requires that a snapshot is
> self-sufficient at its seq and that exactly one is discoverable as "newest ≤ S."

---

## 4. Idempotent apply + bidirectional ack watermarks

### Apply-by-seq (idempotent)

Every consumer applies events **keyed by `(run-id, seq)`** and ignores any seq
`≤` its `last_seq`. This makes replay safe to overlap with live-tail: if a client
replays `last_seq+1..head` and a live event for `head` arrives concurrently,
applying it twice is a no-op. No dedup buffer, no exactly-once transport needed —
**at-least-once delivery + apply-by-seq = effectively-once state.** This is the
same discipline the TUI already uses locally (gap-detect + resync, QUM-775);
the hub seams inherit it.

### Ack watermarks (both directions)

Acks are how buffers get trimmed. They are **cumulative watermarks**, not
per-event acks — "I have durably processed everything through seq W."

```
UPLINK ack (hub → host):     "persisted through seq W_up"
   └─ host may trim its local outbound buffer ≤ W_up  (§6)

DOWNLINK ack (browser → hub): "rendered/consumed through seq W_down"
   └─ hub may advance retention floor toward min(all W_down)  (§2)
```

- **Uplink:** the hub acks the highest contiguously-persisted seq. The host's
  outbound buffer (§6) drops everything at/below it.
- **Downlink:** each browser/consumer periodically reports its `last_seq`. The
  hub's retention floor = `min` over live consumers' watermarks (bounded below by
  the newest snapshot so a slow consumer can't pin the whole log — past a grace
  window it's cut loose to the snapshot path).

### Simplest way vs. right way
- **Simplest:** no acks; time-based buffer expiry both directions. Cost: either
  premature trim (client can't catch up → forced snapshot every blip) or
  never-trim (unbounded buffers).
- **Right:** cumulative watermarks drive trim on both ends.
- **Recommendation:** **cumulative watermarks.** They're a single integer per
  consumer, piggyback on the existing heartbeat/live-tail traffic, and make
  trim provably safe. Bound the downlink floor by the snapshot seq so one stuck
  phone can't wedge GC.

---

## 5. Fence tokens on uplink writes (stale-fence rejection)

Only the **write-lease holder** may push authoritative uplink writes for a
project (the event *log* is host-origin; "writes" here means anything that
mutates hub-authoritative state — memory units, lease-guarded ops; see
[`01` §4](01-architecture.md), [`10`](README.md)). Every such write carries the
**current fence token**; the hub **rejects any write bearing a fence `<` the
current one.**

```
claim/renew ──▶ hub: {holder_host_id, fence=N, last_heartbeat}   # conn = heartbeat
write(fence=N)  ──▶ ACCEPTED
write(fence<N)  ──▶ REJECTED (stale fence)     # zombie holder can't clobber
TTL expired     ──▶ next claimant gets fence=N+1  (monotonic)
```

- **The persistent connection IS the heartbeat** ([`01`](01-architecture.md)).
  Lease has a TTL; a dropped connection lets the TTL lapse.
- **Stale-fence rejection is the correctness backstop** that makes
  last-writer-wins safe across flaky links: a returning zombie holder (network
  partition healed after the lease was reclaimed) is holding fence `N`, but the
  world moved to `N+1` — its writes bounce. It learns it's stale from the
  rejection and re-syncs via the one rule.
- **Rejection is a signal, not an error to swallow.** A stale-fence rejection
  means "you are no longer the writer" → the host drops to read/replay mode and
  surfaces it (so the user isn't silently typing into a dead lease).

**Fence-token durability** (foundation OQ): fence must be **monotonic across a
hub DB failover**. Recommendation: a per-lease `epoch` bumped in the same
transaction that grants/reclaims the lease, stored in Postgres — monotonic by
construction, survives failover with the row. A free-running global counter is
simpler but risks resetting on a restore-from-backup; per-lease epoch localizes
the blast radius. (Final call deferred to [`07-storage`](README.md), but sync
*requires* monotonicity-across-failover — flag it there.)

### Simplest way vs. right way
- **Simplest:** lease only, no fence; whoever holds the lease writes. Cost: a
  zombie whose TTL lapsed but who hasn't noticed can clobber the new holder in
  the reclaim window.
- **Right:** lease + monotonic fence on every write.
- **Recommendation:** **fence now.** It's a single integer compare on the write
  path and the one guarantee that is *painful to retrofit* once real writes exist
  (matches [`01` §4](01-architecture.md)'s recommendation).

---

## 6. Local outbound buffer high-water policy

When the hub is unreachable, the host **keeps running** (disconnected is never a
degraded mode — [`01` §3](01-architecture.md)) and buffers un-acked uplink
events locally, flushing on reconnect per the one rule.

**Policy (recommended): bounded ring, drop-oldest past a high-water mark, and
`log()` the truncation.**

```
buffer: [un-acked seq W_up+1 .............. HEAD]
        │←──────── bounded by high-water ───────→│
   overflow ⇒ drop OLDEST, advance a "truncated-from" marker, log() once/episode
```

- **Drop oldest, not newest.** Newest events keep the browser's live view
  current on reconnect; the dropped-oldest span becomes a **snapshot-fallback**
  gap — the one rule already covers it (client sees `peer.has(last_seq+1)=false`
  → snapshot). Dropping *newest* would instead strand the live tail.
- **Bound (foundation OQ — how much / per-session or global?):** **per-`run-id`
  cap** (each session buffers its own outbound; a chatty session can't starve a
  quiet one) **with a global ceiling** as a safety net across all runs on the
  host. The per-run cap is the primary knob; the global ceiling only trips under
  pathological multi-session backlog.
- **Observability:** emit exactly one `log()` per truncation episode (rate-
  limited like the eventbus drop-warn, `eventbus.go:276`), and carry a
  `truncated-from` marker to the hub so the browser can render "history gap here"
  rather than silently missing events. **No silent caps.**

### Simplest way vs. right way
- **Simplest:** unbounded in-memory buffer. Cost: a long hub outage during a
  busy session OOMs the host — the exact "hub outage must never stall/kill a
  turn" invariant, violated.
- **Right:** bounded ring + drop-oldest + snapshot fallback + truncation log.
- **Recommendation:** **bounded per-run ring, drop-oldest, logged.** Memory
  stays bounded; correctness is preserved by the snapshot path; the user is told
  when history was lost. (Matches [`01` §3](01-architecture.md).)

> **Disk vs. memory:** v1 keeps the buffer in memory (KISS). Spilling to local
> disk to survive a host *restart* mid-outage is a YAGNI extension — the host
> restart already means a new `run-id` and a fresh snapshot, so persisting the
> old buffer buys little. Revisit only if real outages routinely outlast a
> session.

---

## 7. Version-vector reconnect compare

On reconnect the host and hub compare **version vectors** to decide who is
behind, *before* streaming any delta. This is the "which way does data flow"
decision that the one rule's `peer.has(last_seq+1)` check implements per-stream;
the version vector is its multi-stream generalization.

```
compare(local_vv, hub_vv):
  local ahead  (host holds lease) ──▶ PUSH   local delta up   (normal: host is SoT)
  hub  ahead                      ──▶ PULL   hub delta down    (host was offline/restarted)
  concurrent / divergent          ──▶ only reachable via FORCE-RECLAIM → §8 reconcile
```

- **Local-ahead is the common case:** the host is the source of truth
  ([`01`](01-architecture.md)); it usually has events the hub hasn't durably
  stored yet (the un-acked buffer, §6). Push them.
- **Hub-ahead** happens when the host restarted (new `run-id`) or lost its local
  state; it pulls the hub's retained log/snapshot to rehydrate the browser view.
  Note the *live claude session* is still SoT for *new* turns — pull only
  rehydrates history.
- **Genuine divergence** (both sides advanced the same stream independently) is
  **unreachable in normal operation** because the lease + fence (§5) guarantee a
  single writer per stream. It can only arise after a **force-reclaim** (§8), and
  is resolved by provenance-based semantic reconcile — **never a textual merge.**

### Granularity (foundation OQ: per-project / per-(project,agent) / per-stream)

**Recommendation: version-vector granularity = per-`run-id` event stream, keyed
`(run-id → last_seq)`; memory streams keyed `(project, agent) → last_seq`
separately (see [`10`](README.md)).**

Rationale:
- The **event log's natural single-writer unit is the `run-id`** (one live
  session emits one seq stream). A per-project VV would lump independent runs
  together and make reconnect chatty (any run advancing dirties the whole
  project vector). A finer-than-run granularity buys nothing — within a run,
  `seq` already *is* the version.
- **Memory is a different spine consumer** with its own single-writer unit,
  `(project, agent)` ([`01` §5](01-architecture.md)) — so its VV is keyed there.
- The **write-lease stays per-project** for v1 ([`01` §4](01-architecture.md)):
  coarse *authority*, fine *versioning*. These are orthogonal — lease granularity
  answers "who may write," VV granularity answers "what's behind." Keeping VV at
  the stream level while the lease is per-project is intentional and cheap.

So the "version vector" is really a **compact map of `stream-key → high-seq`** on
each side; compare is a per-key `>`/`<`/incomparable check. This is deliberately
lighter than a classic dotted-version-vector: because each stream has a single
writer (by lease+name construction), a scalar high-seq per stream is a complete
version — no per-writer dots needed.

### Simplest way vs. right way
- **Simplest:** single scalar "last event seq" per project; reconnect compares
  one number. Cost: false divergence signals when independent runs advance;
  can't tell *which* stream is behind → over-fetch.
- **Right:** map of `stream-key → high-seq`, compared per key.
- **Recommendation:** **per-stream high-seq map.** It's still just integers, it
  makes push/pull decisions per stream (minimal transfer), and it degrades to
  the scalar case when there's one stream. Avoid full dotted-VVs — single-writer
  streams don't need them (YAGNI).

---

## 8. Force-reclaim → provenance-based semantic reconcile

Divergence is only reachable when the user chooses **Force-reclaim** on a *fresh*
lease held by someone else ([`01` §4](01-architecture.md): "Fresh lease + a
different claimant → *Stop current holder* or *Force-reclaim*"). Force-reclaim
bumps the fence (old holder's writes now bounce, §5) but the old holder may have
produced state the new holder doesn't have → two divergent lineages of the same
stream.

**Resolution is provenance-based semantic reconcile, NEVER a textual merge.**

- **Event log:** the two lineages are **kept as distinct `run-id` streams** and
  presented side-by-side (both are real history from real sessions). There is
  nothing to "merge" — an event log is append-only; you don't line-merge two
  transcripts. The browser shows both lineages with their provenance
  (`host-id`, `run-id`, time). This is trivially correct because events are
  immutable and seq-scoped to their run.
- **Memory:** each unit carries provenance metadata ([`01` §5](01-architecture.md),
  [`10`](README.md)); combining divergent memory is **curation or
  agent-synthesis** (weave reads both, writes a reconciled unit with provenance
  pointing at its sources) — **not** `git merge`, which produces incoherent
  Frankentext for prose. The mechanics live in [`10-memory`](README.md); sync's
  only job is to (a) detect divergence via §7, (b) preserve both lineages
  intact with provenance, and (c) hand them to the reconcile path.

```
force-reclaim(fence N→N+1)
   old lineage: run=R_old  seq…  (fence N, now stale) ─┐
   new lineage: run=R_new  seq…  (fence N+1)          ─┼─▶ keep BOTH, tagged by provenance
                                                       │
   event log ─▶ present side-by-side (immutable, no merge)
   memory    ─▶ semantic reconcile in doc 10 (curation/synthesis, no textual merge)
```

### Simplest way vs. right way
- **Simplest:** last-writer-wins on force-reclaim — silently discard the old
  lineage. Cost: real work/history from the stopped holder is lost with no trace.
- **Right:** preserve both lineages with provenance; reconcile semantically.
- **Recommendation:** **preserve + provenance.** For the *event log* this is
  nearly free (immutable append-only streams — just don't delete the old
  `run-id`). For *memory*, defer the reconcile UX to [`10`](README.md) but never
  auto-discard. Force-reclaim is rare and user-initiated; losing history silently
  is the one outcome we refuse.

---

## 9. Putting it together — one reconnect, end to end

```
browser blips offline, comes back:
  1. sends its VV {run-R: last_seq=L, …}                       (§7)
  2. hub compares: hub-ahead on run-R                          (§7)
  3. hub.has(L+1)?
        yes → stream replay L+1..head                          (§0,§1)
        no  → newest snapshot ≤ head; client sets last_seq;    (§3)
              stream delta from there
  4. client applies by seq, ignoring ≤ last_seq (idempotent)   (§4)
  5. client live-tails; periodically acks last_seq             (§4)
  6. hub advances retention floor = min(consumer acks, snapshot)(§2)

host reconnects to hub (was buffering during outage):
  1. VV compare: local-ahead, host holds lease                 (§7)
  2. push un-acked buffer (or truncated-from marker + snapshot) (§6)
  3. every uplink write carries fence=current                  (§5)
        stale? → host drops to read-mode, surfaces to user      (§5)
  4. hub acks W_up; host trims outbound buffer ≤ W_up          (§4,§6)
```

Every numbered step is **the same rule** applied at a different seam. No seam has
private catch-up logic. That is the design working as intended.

---

## Open Questions

- **Snapshot `N`/`T`/floor-interval values + mobile cold-start SLO.** We fix the
  *shape* (one snapshot + ≤N replay) but not the numbers — they need real
  load-testing on a slow mobile link ([`13`](README.md)). What replay count feels
  instant on a phone?
- **Fence durability across DB failover.** Per-lease epoch (recommended) vs.
  global monotonic counter — final call is a [`07-storage`](README.md) decision,
  but sync *requires* monotonic-across-failover; confirm the chosen scheme
  guarantees it.
- **Downlink ack cadence.** How often should a browser report its watermark —
  per-event (chatty), timer-based, or piggybacked on heartbeat? Affects GC
  latency vs. WAN chatter.
- **Slow-consumer eviction grace.** How long may one stuck consumer pin the
  retention floor before it's cut loose to the snapshot path? Needs a concrete
  grace window.
- **Local outbound buffer: disk spill?** v1 is memory-only (host restart ⇒ new
  `run-id` + snapshot). Do real outages ever outlast a session enough to justify
  disk-backed buffering?
- **VV transfer size at scale.** A host with many concurrent `run-id`s sends a
  larger stream-key map on reconnect. Is a per-key map fine, or do we need a
  digest/rolling-hash compare for hosts with many streams?
- **Cross-source turn-queue ordering** (inherited from [`01`](01-architecture.md)
  OQ): when local TUI and browser both enqueue, sync guarantees per-stream seq
  order on the *output* log, but input ordering across sources is a turn-queue
  concern — does it need source tagging for UX clarity?
