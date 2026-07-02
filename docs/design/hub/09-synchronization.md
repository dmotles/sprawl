# 09 — Synchronization

*How a browser catches up to a live session after any disconnect, and how
memory travels between sessions — in the MVP v1 single-user model.*

See also: [`01-architecture.md`](01-architecture.md) (event-log spine) ·
[`10-memory`](README.md) (memory streams) · [index](README.md)

---

> **MVP re-scope (v2).** The hub is a **cloud companion** for a **single user**:
> it relays the live session stream to a browser and durably persists
> memory / transcripts / attachments. This doc was originally written for a
> multi-writer, multi-host future; that machinery is **cut for v1**. The cut list
> and *why each cut is safe for a single user* is
> [§5](#5-what-was-cut-and-why-its-safe-for-single-user) — read it before adding
> anything back for multi-user.

## 0. The one rule (kept, simplified)

The spine survives the re-scope intact, because the thing it solves for —
**cloud connections drop** (mobile, NAT, L7 LB idle timeouts) — is real even for
one user. Every consumer (browser, and the hub itself as it ingests from the
host) obeys one contract:

> **Replay from my last seq. If I have no last seq, get the full log. Then
> live-tail.**

```
on (re)connect:
  if have(last_seq):  send my last_seq  → peer streams delta (last_seq+1 .. head)
  else:               peer streams the full seq'd log (1 .. head)
  live-tail: peer holds the channel open and pushes new seq'd events as they occur
```

That is the **entire** reconnect rule in v1. **No snapshot tier** — the delta
(or full log) *is* the catch-up. This is the one piece of sync worth keeping
properly, and it's now much smaller than it was.

```
claude ─▶ local eventbus ─▶ hub client ─uplink─▶ HUB (durable log) ─stream─▶ browser
         └── seam 1 ──┘    └──── seam 2 ────┘                       └── seam 3 ──┘
             (local)         (host→hub, WAN)                          (hub→browser, WAN)
```

The rule is written **once** and reused at each seam. Seam 1 already exists
(`internal/runtime/eventbus.go`: seq-stamped, gap-detecting, terminal-undroppable
— QUM-669/QUM-775). Seams 2 and 3 are the same "replay-then-tail" consumer
pointed at the hub's durable log instead of an in-process channel.

---

## 1. The live stream (seq'd transcript)

**One durable, append-only, seq'd stream per session** — the transcript. It is a
**cache of the host's truth**, never authoritative ("cloud companion, not brain",
[`00`](00-overview.md)/[`01`](01-architecture.md)). Losing it costs history, not
correctness: the live claude session is still the source of truth and re-streams
from its current state.

### Seq stamping — reused, not reinvented

The local eventbus already stamps a **monotonic, 1-indexed `Seq`** on every
`RuntimeEvent` under `publishMu`, so subscribers see strictly ascending seqs
(`EventBus.Publish`, `eventbus.go:364`), with gap detection and terminal-event
undroppability already solved. The hub **stores the host's seq verbatim** — it
does not re-number.

- **`(session, seq)` is the event identity.** A session maps to one
  `sprawl enter` run (`run-id`, [`01` §4](01-architecture.md)); its seq space is
  1..head. Because v1 is single-user and (see §4) only one host is active per
  session at a time, one seq stream per session is unambiguous — no cross-writer
  interleaving to reconcile.
- **Per-session monotonicity is the only invariant.** The hub appends in seq
  order and applies **idempotently by seq**: any event with `seq ≤ last_seq` is a
  no-op. At-least-once delivery + apply-by-seq = effectively-once state, so replay
  can safely overlap live-tail without dedup buffers. (Same discipline the TUI
  uses locally — QUM-775.)

### Reconnect = replay delta, else full log

```
fresh browser (no last_seq)   → hub streams full log 1..head, then live-tails
returning browser (last_seq=L)→ hub streams L+1..head,      then live-tails
```

- **Live-tail transport:** a **held-open channel** — Connect server-stream, with
  a **WebSocket fallback** if a long-lived Connect stream doesn't survive the
  deployment's L7 LB (viability is the top risk flagged in
  [`03-api-surfaces`](README.md)). Either way the *sync* contract above is
  transport-agnostic: catch up by seq, then receive appended seqs.
- **No snapshots.** The full-log send replaces the snapshot cold-start path. For
  a single user's session lengths this is acceptable; if a very long transcript
  ever makes full-log cold-start painful on mobile, a snapshot/compaction tier is
  the first thing to add back (noted in Open Questions), and it slots *under* the
  same one rule without changing it.

### Retention (v1): keep everything

v1 **keeps the full per-session log indefinitely — no GC**
([`README`](README.md) core principles; [`07-storage`](README.md)). This is the
simplest possible policy and it removes an entire class of "your `last_seq` was
trimmed" edge cases: a returning browser can *always* delta-replay from its
`last_seq`, however old. **No ack-watermark-driven trimming** in v1 — with a
single user, no snapshot floor to advance, and no GC, there's nothing for a
retention watermark to drive (YAGNI). If storage growth ever bites, add a simple
age/size GC later; the one rule already handles a GC'd `last_seq` (fall back to
full log).

### Simplest way vs. right way
- **Simplest:** hub keeps only "latest state," browser full-reloads every
  reconnect. Cost: every mobile blip is a heavy reload; no delta catch-up.
- **Right (v1):** durable seq'd log, kept in full + replay-delta-else-full +
  live-tail. Cost: storing the log (bounded by "keep everything until it hurts").
- **Recommendation:** **the v1 log.** Delta-replay is what makes a reconnect feel
  instant over a flaky link; it's cheap and it's the justified core of this doc.
  We *dropped* the snapshot tier (the expensive part) and kept the cheap,
  high-value part.

---

## 2. Local outbound buffer (host → hub, during a hub blip)

Disconnected is the default and never a degraded mode
([`01` §3](01-architecture.md)): a hub outage must never stall a turn. While the
hub is unreachable the host **keeps running** and buffers un-acked uplink events,
flushing on reconnect via the one rule.

**Policy: bounded in-memory ring, drop-oldest past a high-water mark, `log()` the
truncation once per episode** (rate-limited like the eventbus drop-warn,
`eventbus.go:276`).

- **Drop oldest, not newest** — the newest events keep the browser's live view
  current on reconnect; the dropped-oldest span just becomes a history gap the
  hub renders as "history gap here" (carry a `truncated-from` marker). **No
  silent caps.**
- **Bound:** a per-session cap (with a global ceiling as a safety net). v1 is
  memory-only — a host restart means a new session/run and a fresh full-log
  anyway, so disk-spill buys little (YAGNI).
- **No uplink ack watermark needed for correctness:** the host trims on a simple
  "hub confirmed receipt through seq W" signal (which the hub already needs to
  append). This is a receipt, not the elaborate bidirectional watermark scheme of
  the old draft — that existed to drive snapshot floors, which are now gone.

### Simplest way vs. right way
- **Simplest:** unbounded in-memory buffer. Cost: a long outage during a busy
  session OOMs the host — violates "hub outage never stalls/kills a turn."
- **Right (v1):** bounded ring + drop-oldest + truncation log.
- **Recommendation:** **bounded per-session ring, logged.** Bounded memory,
  correctness preserved (missing span → history gap, not corruption), user told.

---

## 3. Memory sync (last-writer-wins)

Memory is the *other* thing the hub durably persists. Its sync model in v1 is
deliberately trivial, and it's **safe because of a structural invariant**, not
because of clever merging.

**Model:**

```
write local ALWAYS         → memory is written to the host's local store first, always
checkpoint PUSH on handoff → at /handoff, the agent's memory stream is pushed to the hub
pull on SESSION START      → on next session start, pull the hub's copy down
resolution: LAST-WRITER-WINS
```

**Why last-writer-wins is safe here:** memory is partitioned **one stream per
`(project, agent)`**, and the **agent name is the partition key**
([`01` §5](01-architecture.md)). Agent names are unique across the org, so each
memory stream has a **single writer by construction**. Two hosts cannot both be
the authoritative writer of the same agent's memory at the same time (§4 keeps
one host active per session). With a single writer there is no concurrent-edit
conflict to merge — the most recent checkpoint is simply the truth. Last-writer-
wins is not a compromise here; it is *correct given the invariant*.

- **No provenance-based semantic reconcile, no version vectors, no textual
  merge** in v1 — there is nothing to reconcile when there's one writer. (The
  earlier design carried all three to handle divergent lineages that only arise
  under multi-writer / force-reclaim, both cut. See §5.) Provenance *metadata* on
  memory units may still be recorded for curation ([`10`](README.md)); what's cut
  is the reconcile *algorithm*, which v1 never invokes.
- **Checkpoint granularity = the whole agent's memory stream at handoff.** Coarse
  and simple; a handoff is already the natural "flush my state" boundary
  ([`CLAUDE.md`](../../../CLAUDE.md) session-handoff; `handoff` MCP tool).
- Mechanics of what a memory unit contains and how it's stored live in
  [`10-memory`](README.md); sync's only job is push-on-handoff / pull-on-start /
  last-writer-wins.

### Simplest way vs. right way
- **Simplest:** exactly this — push on handoff, pull on start, last-writer-wins.
- **Right (multi-writer future):** provenance + semantic reconcile (the old §8).
- **Recommendation:** **last-writer-wins for v1.** The single-writer-by-name
  invariant makes the "simplest" option also the *correct* option for one user.
  Don't pay for reconcile machinery that guards a race that can't occur yet.

---

## 4. Write-authority (advisory active-host marker)

To keep two hosts from both driving/persisting the *same* session, the hub keeps
a **trivial advisory marker: "which host is active for this session."**

```
host connects for session S:
  no active host   → become active
  active = me      → continue
  active = other   → REJECT: "another host is active for this session — stop it or reclaim"
```

- **The persistent connection is the liveness signal** ([`01`](01-architecture.md)):
  when the active host's connection drops and stays dropped past a TTL, the marker
  goes stale and the next host may claim it.
- **User-facing, not automatic:** a second host is *told* to stop the other or
  reclaim — a single user resolves this by knowing which machine they meant. No
  automatic arbitration.

### Explicitly NO fence tokens in v1

Fence tokens / lease epochs (the old §5) existed to make last-writer-wins **safe
against a zombie writer** — a host whose lease lapsed during a partition, then
returned and clobbered the new holder. **We accept that risk in v1** as a known
limitation:

- v1 is **single-user**. The only way to hit the zombie-writer race is for *one
  person* to have the *same session* live on *two machines*, lose connectivity on
  one, reclaim on the other, then have the first reconnect and write — a
  self-inflicted, rare, and recoverable situation (worst case: one stale memory
  checkpoint, re-fixable by the user).
- The cost of fencing (a monotonic token compared on every write, durable across
  DB failover) is **not justified** by that residual single-user risk.

> **Known, accepted limitation:** without fence tokens, a returning zombie host
> *can* overwrite newer state via last-writer-wins in the narrow reclaim window.
> Safe for one user; **must be re-added for multi-user / multi-writer** (see §5).

### Simplest way vs. right way
- **Simplest:** no marker at all; both hosts write. Cost: silent double-drive,
  confusing interleaved transcript.
- **Right (multi-writer):** TTL lease + monotonic fence + stale-fence rejection.
- **Recommendation:** **advisory marker only** for v1 — enough to stop accidental
  double-drive, honest about the zombie-writer gap, and cheap. Fencing is the
  first correctness item to restore when multi-user lands.

---

## 5. What was cut, and why it's safe for single-user

This section is a **map for the future multi-user revision.** Each cut item lists
what it defended against and why that threat is inert for one user.

| Cut | What it defended against | Why safe for single-user v1 | Re-add when |
|---|---|---|---|
| **Snapshots / compaction tier** | Unbounded cold-start on very long sessions | One user's session lengths ⇒ full-log cold-start is acceptable | Full-log cold-start gets slow on mobile for long transcripts |
| **Version-vector reconnect compare** | Deciding push/pull/divergence across many concurrent writers/streams | One active host per session (§4) + single-writer memory (§3) ⇒ no divergence to detect; a scalar `last_seq` per session suffices | Multiple hosts/browsers may each hold write authority |
| **Fence tokens / lease epochs** | A zombie writer clobbering the new holder after lease lapse | Single-user; the race needs one person on two machines mid-partition — rare, self-inflicted, recoverable (§4) | Multi-writer, or the zombie-writer window becomes a real hazard |
| **Force-reclaim flow** | Wresting a *fresh* lease from a different active writer | No "different writer" exists for one user; the advisory marker's "stop or reclaim" prompt is enough | Concurrent independent writers exist |
| **Provenance-based semantic reconcile** | Merging two divergent memory lineages without textual merge | Single-writer-by-name (§3) ⇒ no divergent lineages; last-writer-wins is correct, not lossy | Divergence becomes reachable (i.e. after fences + force-reclaim return) |
| **Bidirectional ack watermarks** | Trimming buffers + advancing snapshot retention floors | No snapshots + keep-everything ⇒ no floor to advance; host trims outbound on a simple receipt (§2) | Snapshots and/or multi-consumer retention return |

**The through-line:** almost every cut item existed to make **concurrent writes**
safe. v1 has **no concurrent writers** — one active host per session, one writer
per memory stream (by agent name). Remove concurrency and the entire
version-vector / fence / reconcile edifice becomes machinery guarding a race that
cannot occur. What remains — a seq'd stream you can replay from your last seq —
is justified purely by *connections dropping*, which is real for a single mobile
user, so it stays.

---

## 6. Putting it together — one reconnect, end to end (v1)

```
browser blips offline, comes back:
  1. reconnects, sends last_seq = L  (or nothing, if fresh)      (§0)
  2. hub streams L+1..head           (or 1..head if fresh)       (§1)
  3. browser applies by seq, ignoring ≤ L (idempotent)          (§1)
  4. hub holds the channel open and live-tails new seqs         (§1)

host reconnects to hub after a blip:
  1. flushes bounded outbound buffer (or truncated-from marker) (§2)
  2. hub confirms receipt through seq W; host trims ≤ W          (§2)
  3. advisory active-host check: me? continue. other? user resolves (§4)

session ends / handoff:
  1. memory checkpoint pushed to hub                            (§3)
next session start:
  2. memory pulled from hub; last-writer-wins                   (§3)
```

Every step is the **same seq-replay rule** at a different seam, plus two trivial
additions (advisory marker, last-writer-wins memory) that are correct *because
v1 has a single writer everywhere.*

---

## Open Questions

- **Full-log cold-start ceiling.** At what transcript length does a fresh browser
  full-log send become unacceptable on a slow mobile link? That threshold is the
  trigger to add a snapshot/compaction tier back (it slots under the one rule
  without changing it).
- **Live-tail transport.** Does a long-lived Connect server-stream survive the
  target deployment's L7 LB idle timeouts, or do we ship the WebSocket fallback
  from day one? (Owned by [`03-api-surfaces`](README.md); the top viability
  risk.)
- **Active-host TTL.** How long after a dropped connection before the advisory
  marker is reclaimable? Too short ⇒ a brief blip hands the session away; too
  long ⇒ a genuinely-moved user waits.
- **Receipt cadence (host→hub).** How often does the hub confirm "received
  through seq W" so the host can trim its outbound buffer — per-batch, timer, or
  piggybacked on the stream? Affects buffer size vs. WAN chatter.
- **Memory checkpoint on crash (no clean handoff).** Push is on handoff; if a
  session dies without handoff, the last local memory writes never reach the hub.
  Do we also push on a periodic timer, or accept "handoff-or-lose" for v1?
- **Zombie-writer limitation acceptance.** Confirm the single-user product owner
  accepts the documented no-fence limitation (§4) for v1.
