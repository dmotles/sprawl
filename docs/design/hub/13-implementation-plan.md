# 13 — Implementation Plan (single-user MVP sprint)

*The synthesis doc. Written last. It turns docs 00–12 + `security-privacy` +
`attachments-multimodal` into a phased build plan for the **v2 single-user cloud
companion**, records that the one transport disagreement is now reconciled in the
leaf docs, calls out the de-risking spike that must run first, sketches a cost
envelope, and consolidates every doc's `## Open Questions` into one master list
for the maintainer.*

See also: [`00-overview`](00-overview.md) · [`01-architecture`](01-architecture.md) · [`03-api-surfaces`](03-api-surfaces.md) · [index](README.md)

---

## 0. The MVP thesis — a thin single-user cloud companion on the right spine

The v2 re-scope sharpens the earlier goal. The hub is a **single-user cloud
*companion*** to the local `sprawl` binary — **not** a multi-tenant service. It
does exactly two durable jobs ([`01` §0](01-architecture.md), [`README`](README.md)):

1. **Relay** a running host's live activity stream to a browser for remote view +
   input.
2. **Durably persist** memory, session transcripts, and attachments so they're
   reachable from any machine the single user connects from.

The build strategy is unchanged in spirit — *build the MINIMAL feature set that
establishes the RIGHT architecture* — but "the right architecture" is now much
smaller, because single-user removes almost every source of coordination
complexity. This plan is organized around one question: *what is the smallest
slice that proves the load-bearing spine end-to-end?* — and defers everything
else.

**The load-bearing spine (the one thing that must be right):**

```
claude → local eventbus (seq'd) → uplink → HUB (durable seq'd log + fan-out) → browser
                                                    ▲
browser input → downlink → host turn-queue ─────────┘   (result re-enters uplink)
```

The single load-bearing property is **one durable, seq'd, resumable stream with
the one rule**:

> **Fresh connect → full seq'd log. Reconnect → send my last seq, get the delta.
> Then live-tail.**

Implemented **once** and reused at every seam (claude→bus, bus→hub, hub→browser —
[`01` §2](01-architecture.md), [`09` §0](09-synchronization.md)). This is the one
piece painful to retrofit, and it's justified purely by *connections dropping*
(mobile, NAT, L7 idle timeouts), which is real even for one user. The durable
**transcript IS the seq'd log** — there is no separate ephemeral event-log +
snapshot layering ([`07` §0](07-storage-persistence.md)).

Everything that used to be a *second* load-bearing correctness guarantee —
write-authority via TTL leases + fence tokens — is **gone**. In v2 write authority
is a **trivial advisory active-host marker** ([`01` §4](01-architecture.md),
[`09` §4](09-synchronization.md)); it stops accidental double-drive and drives the
"which host is live" UX, but it does **not** fence or reject writes. Single-user
means real write contention is rare and a human is always in the loop.

**What the MVP deliberately is NOT** (YAGNI — the v2 cut list,
[`01` §8](01-architecture.md)):

- **OIDC / federated identity / user allowlist** — auth is one configured bearer
  token ([`04`](04-authentication.md)).
- **Multi-tenant isolation & enforcement** — single user; the `user_id` column is
  a flex-later hedge, always one value, never enforced
  ([`security-privacy` §3](security-privacy.md)).
- **Fence tokens / lease epochs / TTL leases** — replaced by the advisory
  active-host marker.
- **Version-vector reconnect / force-reclaim / semantic reconcile** — memory is
  last-writer-wins ([`09` §3](09-synchronization.md), [`10`](10-memory.md)).
- **Snapshots / log compaction** — full-log replay on fresh connect, delta on
  reconnect ([`01` §2](01-architecture.md)).
- **GC / retention windows** — transcripts, attachments, and memory are kept
  indefinitely ([`07` §5](07-storage-persistence.md)).
- **Client-side (zero-knowledge) encryption + per-project content opt-out** —
  documented as a seam, deferred ([`security-privacy` §2](security-privacy.md)).
- Hard driver-lock / multiplayer editing / presence indicators; the north-star
  org model; any default/hardcoded hub endpoint; metrics/tracing *servers*.

---

## 1. Transport reconciliation — RESOLVED in the leaf docs (no conflict remains)

The earlier plan flagged one architectural disagreement — `01` described "one
persistent **bidirectional** connection," while `03` argued against betting on a
long-lived full-duplex bidi stream. **In v2 that conflict is already resolved in
the docs themselves; there is nothing left for this plan to reconcile.**

`01` now describes the topology directly as a **"continuously-re-established
server-stream downlink (hub → host commands) plus unary uplink calls"**, with the
heartbeat riding the downlink stream ([`01` §1](01-architecture.md)). That is
exactly `03`'s recommended shape ([`03` §1/§4.5](03-api-surfaces.md)):

| | v2 leaf-doc position (unanimous) |
|---|---|
| Host↔hub downlink | **held-open server-stream** (hub → host), continuously re-established; heartbeat rides it |
| Host↔hub uplink | **unary / batched** `AppendTranscript` (host → hub) |
| Browser↔hub | **server-stream** (events) + **unary** (input) — *forced*, since no stable browser ships `duplex:'full'` as of 2026 ([`03` §2](03-api-surfaces.md)) |
| Reconnect | the *one rule* covers both seams identically; cuts are **expected**, made cheap by the seq'd log |

The symmetry is the KISS win: **one shape, one reconnect rule, two seams.** The
"persistent connection" is persistent in the sense of *continuously
re-established*, not *never dropped*. A later bidi upgrade of the host downlink
stays available purely as an optimization — the protobuf wire types don't change,
only who initiates frames — but it is **not** in scope. **No doc revision is owed
here** (the previous plan's note about rewording `01` is obsolete: `01` already
uses the reconciled language).

---

## 2. FIRST TASK — the de-risking SPIKE (before committing Phase 1)

Per [`03` §5](03-api-surfaces.md), **run a ~1–2 day spike against a real managed
container platform (not localhost) BEFORE committing the streaming shape or
building Phase 1.** The whole companion only works if the host's downlink survives
cloud L7 load balancers, and the platform actively fights long-lived streams
(Container Apps ~240s, App Service hard ~230s TCP, generic ingress ~60s idle —
[`03` §4.2](03-api-surfaces.md)). This is the design's **top viability risk** and
the one thing the entire product rests on.

**The spike measures** (from [`03` §5](03-api-surfaces.md)):

1. **Idle survival** — open a server-stream, send nothing, time until the LB cuts
   it (expect 60–240s).
2. **Heartbeat efficacy** — find the max app-level heartbeat interval that keeps
   the stream alive ≥30 min. **Distinguish HTTP/2 PING vs. on-stream DATA** (the
   Envoy `stream_idle_timeout` trap — PINGs may not reset a *per-stream* idle
   timer; on-stream DATA may be mandatory — [`03` §4.3](03-api-surfaces.md)).
3. **Buffering / latency** — first-byte + per-event latency; confirm events
   arrive individually, not buffered into useless batches
   ([`03` §4.4](03-api-surfaces.md)).
4. **Downlink round-trip** — `SubmitInput` (unary) → host receives on its
   downlink → ack, target < ~500ms p95.
5. **Reconnect correctness & frequency** — kill network / roam mobile↔wifi;
   confirm `from_seq` replay resumes with **zero gaps / zero dupes**, and log how
   *often* real mobile/NAT conditions force reconnects.
6. **Config levers** — does Premium Ingress / CLI idle-timeout actually help, and
   is it needed given a working heartbeat?

**Decision gate:** if (2)+(3)+(5) pass with a heartbeated **server-stream**, ship
that; bidi is an unneeded optimization and **WebSocket stays in reserve** (Connect
has no first-class browser WS transport → custom framing cost; build only if the
spike fails — YAGNI). If the server-stream can't be kept alive or is buffered,
escalate to the **WebSocket transport** carrying the same seq'd frames + same
reconnect rule, and re-run the spike.

> The heartbeat that keeps the stream alive **doubles as the advisory active-host
> refresh** ([`03` §1](03-api-surfaces.md)) — a dropped heartbeat simply lets the
> next host claim the advisory marker; there is no fence to reconcile.
> **Do not commit the "one persistent connection" assumption until the spike
> passes.**

---

## 3. Phased plan

Each phase is independently valuable and leaves `main` shippable. Phases 0–1 are
the architecture-proving core; 2–3 build breadth on the proven spine. All four
phases assume single-user throughout: a `user_id` column rides durable rows
(always one value) as a flex-later hedge, never enforced.

### Phase 0 — connectivity/auth spine (no data yet)

**Goal:** prove **auth + NAT dial-out end-to-end.** `sprawl enter` dials out and
*registers an instance*; nothing streams yet.

**Scope:**

- **Proto/Connect contract + `buf` toolchain** from day one: `buf generate` →
  Go (`connect-go`) + TS (`connect-es`); `buf lint`/`buf format`/**`buf breaking`
  in CI** wired into `make validate` (Go-only repo today) ([`03` §3](03-api-surfaces.md),
  [`08`](08-deployment.md)). Additive-only field policy; never reuse field
  numbers — this is what keeps the deferred v2 features (snapshots, richer auth)
  **additive** rather than breaking.
- **Single Go container skeleton** (`./hubd`): one Connect listener; `go:embed`
  SPA seam (empty shell is fine); `/healthz` (deps-free) + `/readyz`
  (deps-checked) split; graceful `SIGTERM` drain ([`08`](08-deployment.md),
  [`05` §4](05-observability.md)).
- **`Store` interface + two impls** (`memStore`, `pgStore`) behind
  dependency-injection, from the start; **goose** migrations embedded via
  `embed.FS`, applied by `pgStore.Migrate` at boot; `sprawl hub migrate`
  subcommand ([`07`](07-storage-persistence.md)). Phase-0 tables (sketch, columns
  land in migrations — [`07` §4](07-storage-persistence.md)):
  `users` (**exactly one row**), `tokens` (**hashed** bearer token(s)), `hosts`,
  `projects`, `active_host` (advisory marker), `sessions`. **No `leases` table**
  (cut with fencing).
- **`gocloud.dev/blob` + `gocloud.dev/secrets`** wired (memblob/fileblob + local
  secrets impl in dev/test) — the abstraction *is* the local-dev backend, so it
  costs ~nil ([`07`](07-storage-persistence.md), [`06`](06-iac.md)).
- **Auth, for real but single-user** ([`04`](04-authentication.md),
  [`security-privacy` §5](security-privacy.md)):
  - **One configured bearer token — NO OIDC.** The token is a **deploy secret**
    resolved at startup from the secrets path, never compiled in, no default
    (public-repo hygiene, mirrors "no default hub endpoint"). There is **no
    relying-party flow, no PKCE, no IdP parameter, no user allowlist.**
  - **Browser login:** a tiny `/login` page trades the configured bearer token
    (constant-time compare vs `HUB_LOGIN_TOKEN`) for a signed **httpOnly / Secure
    / SameSite** session cookie backed by a server-side session record; the SPA
    never re-sends the token ([`04` §1/§6](04-authentication.md)).
  - **Host auth:** hosts send a **hashed bearer token**
    (`sprawl_hub_<tokenid>_<secret>`, stored argon2id + per-deploy pepper from the
    secrets path) in the `Authorization` header; O(1) indexed lookup by
    `<tokenid>`; per-host `create(show-once)/revoke/rotate` lifecycle
    (`CreateHostToken`/`ListHostTokens`/`RevokeHostToken`). **Token never on a CLI
    flag / URL / log** — resolved from a `0600` file / env / secrets ref only
    (dodges the QUM-728 snapshot-leak vector; hard rule —
    [`04` §5](04-authentication.md), [`security-privacy` §5.2](security-privacy.md)).
- **Instance registration** RPC (`RegisterInstance`): `sprawl enter` dials out
  with its bearer token, the hub records `{host_id, run_id, repo_label, user_id}`
  and surfaces it in `ListInstances` ([`03` §1/§2](03-api-surfaces.md)).
- **`--hub-url` / env / config** plumbing with **default firmly empty** (public-
  repo hygiene); log resolved endpoint host-only, token redacted
  ([`01` §3](01-architecture.md), [`02` §2.6](02-components.md)).
- **Observability floor**: slog/JSON with canonical attrs
  (`run_id/host_id/seq/component/trace_id`); `/debug/state` endpoint (gated,
  read-only) from day one ([`05`](05-observability.md)).
- **IaC** ([`06`](06-iac.md)): `bootstrap/` (remote encrypted TF state) + `azure/`
  concrete root + `modules/` capability contracts (container-host, database,
  object-store, secrets). `terraform.tfvars.example` placeholders only; no
  instance-specific defaults. `aws/` = README stub.

**Simplest vs. right (Phase 0):** the temptation is a shared secret + basic-auth
+ inline SQL. The **right-sized** call is deliberately smaller than the pre-v2
plan: **single configured bearer token (not OIDC) + hashed per-host tokens +
`Store` interface + `buf breaking`.** OIDC's entire value is multi-*user*
identity; with one user it is pure overhead. But the wire contract + storage seam
+ per-host revocation are still exactly what's ruinous to retrofit once three
deployables evolve independently, so those are chosen now (endorsed by
[`04`](04-authentication.md)/[`07`](07-storage-persistence.md)).

**Done when:** a real host dials out through a real deployed hub, authenticates
(bearer token → session cookie in the browser, bearer header on the host), and
appears in `ListInstances`; `/debug/state` shows the connection + advisory-marker
registry; `buf breaking` gates CI.

### Phase 1 — read-only single pane (the resume, one rule)

**Goal:** uplink the seq'd stream and **live-tail one session in the browser.**
This alone kills window-juggling and gives remote/mobile *view*.

> **Gate:** the §2 spike must have passed with a heartbeated server-stream (or the
> WS fallback chosen) before this phase commits.

**Scope:**

- **Host-side hub client**: subscribe to the existing local seq'd eventbus
  (reuse the subscriber API unchanged; honor its drop telemetry —
  [`02` §2.1](02-components.md)); **bounded local outbound buffer**, drop-oldest
  past high-water, **one log per truncation** + a `truncated-from` marker
  ([`01` §3](01-architecture.md), [`09` §2](09-synchronization.md)).
- **Uplink**: `AppendTranscript` **unary/batched** carrying
  `{host_id, run_id, entries[], from_seq}`. The hub **stores the host's seq
  verbatim** (does not re-number) and appends **idempotently by seq** per session
  — any event with `seq ≤ last_seq` is a no-op ([`09` §1](09-synchronization.md),
  [`03` §1](03-api-surfaces.md)).
- **Durable seq'd stream = the transcript**: **one** append-only per-session log
  in the `Store` (PG index row per `(session, seq)` + blob body per event). There
  is **no snapshot tier and no separate event-log layer** — the transcript *is*
  the log and serves both fresh-connect full-send (from seq 0) and reconnect delta
  (from a client's last seq) ([`01` §2](01-architecture.md),
  [`07` §4](07-storage-persistence.md), [`09` §1](09-synchronization.md)).
- **Downlink fan-out**: `SubscribeInstance` **held-open server-stream** with an
  **on-stream heartbeat every ~20–30s** (beats the 60–240s ceilings; doubles as
  advisory-marker refresh). Browsers follow the **one rule** too — same code path
  ([`03` §2/§4.5](03-api-surfaces.md)).
- **Advisory active-host marker** (no lease/fence): a host connecting for a
  session becomes active if none is; if another host is active it is **rejected
  with "another host is active — stop it or reclaim"** (user-resolved, not
  automatic). The marker is refreshed by the downlink heartbeat and goes stale
  (reclaimable) after a TTL past a dropped connection ([`01` §4](01-architecture.md),
  [`09` §4](09-synchronization.md)). **No fence token, no epoch, no lease txn.**
- **Reconnect keying = a scalar `last_seq` per session** (no version vector —
  single active host per session means no cross-writer divergence to detect;
  [`09` §5](09-synchronization.md)).
- **Retention = keep everything** (no ack-watermark trimming, no snapshot floor,
  no GC). A returning browser can always delta-replay from its `last_seq`, however
  old; the host trims its *outbound* buffer on a simple "hub confirmed receipt
  through seq W" receipt ([`07` §5](07-storage-persistence.md),
  [`09` §1/§2](09-synchronization.md)).
- **SPA (React 19 + Vite + `@connectrpc/connect-web` / `connect-query`)**
  ([`11`](11-frontend-stack.md)): **bearer-token login → httpOnly cookie** shell
  (not OIDC-gated); `ListInstances` switcher; **live-tail** via a bare
  `connect-web` `for await` loop appending into an external append-only store
  (Zustand / `useSyncExternalStore`), **virtualized** log view
  (`@tanstack/react-virtual`); reconnect-per-one-rule in a framework-agnostic
  plain-TS transport module. `go:embed`'d, no SSR.
- **`trace_id`** propagated through frames + logs ([`05` §3.2](05-observability.md)).

**Simplest vs. right (Phase 1):** simplest = hub stores only the latest state,
clients full-reload on every reconnect. **Right — durable seq'd log kept in full +
replay-delta-else-full + live-tail — is chosen** because delta-replay is what
makes a mobile reconnect feel *instant*, and the reconnect machinery already
exists locally (QUM-775 seq'd eventbus); we're extending resilience sprawl already
has, not inventing it. Note the *expensive* part (the snapshot/compaction tier)
is deliberately **dropped**; we keep only the cheap, high-value part.

**Done when:** from a phone/laptop browser, the maintainer sees the live output
of a running `sprawl enter` session, and a network blip (roam wifi↔mobile)
resumes with zero gaps/dupes via `from_seq` replay against the full log.

### Phase 2 — downlink input + attachments

**Goal:** **type to a session from the browser**, and **upload an image → content
block** (the feasibility-verified screenshot path).

**Scope:**

- **Downlink input**: `SubmitInput` (unary) → hub → host's `SubscribeCommands`
  downlink stream → **the ONE turn-queue**, reusing sprawl's existing
  message/turn-queue plumbing; the hub only *transports* input, never interprets
  it ([`01` §6](01-architecture.md), [`02` §2.3](02-components.md)). No source tag
  in v1 (add only if double-driving proves confusing). Lightweight **"N clients
  connected"** guard only — no driver-lock/presence.
- **Attachments (VERIFIED FEASIBLE)** ([`attachments-multimodal`](attachments-multimodal.md)):
  the `claude` CLI in **stream-json input mode** (exactly how sprawl launches it)
  **accepts `message.content` as an array of blocks including base64 `image`
  blocks** (verified against the Agent SDK streaming-input contract; single-
  message mode does **not** — only streaming input does).
  - **Schema change** in `internal/protocol/types.go`: `MessageParam.Content`
    (string) → typed union adding `Blocks []ContentBlock` (+ `ImageSource`) with
    a **custom `MarshalJSON`** — 100% wire-back-compat (text turns still emit
    `"content":"…"`). This is the one thing ugly to retrofit; do the typed union.
    Existing text-`MessageParam` construction sites are untouched.
  - **Browser path (primary)**: browser uploads bytes → hub blob store (mint
    `{attachment_id, media_type, size, sha256}`); the downlink turn carries
    **refs, not bytes**; the **host pulls bytes** from blob, base64-encodes,
    sniffs media_type (∈ jpeg/png/gif/webp, ≤10 MB), assembles
    `Blocks: [image…, text…]` (image-then-text), enqueues. Bytes never ride the
    seq'd log ("broker, not brain").
  - **Local `/attach <path>` (secondary, ship FIRST)**: bytes already on host →
    skip hub entirely → same `Blocks` assembly. Terminal-agnostic; proves the
    multimodal plumbing end-to-end **before** the hub blob round-trip, and works
    in disconnected mode. True TUI clipboard/bracketed-paste capture is deferred.

**Simplest vs. right (attachments):** simplest = inline base64 blocks on the
turn-queue/log (blows the 10 MB stdin cap, bloats every replay). Right (chosen for
v1) = **base64 blocks + blob-store-by-reference**: bytes via blob, refs via the
turn-queue. Right-*later* = Anthropic **Files API `file_id`** to avoid re-sending
bytes on every history replay — **deferred** until screenshot-heavy sessions prove
the re-send cost real (YAGNI).

**Done when:** the maintainer types into a live session from the browser and the
turn lands; and an image dropped in the browser (or `/attach`'d locally) reaches
claude as an image content block and gets a vision response.

### Phase 3 — memory + transcript archive sync + bird's-eye status

**Goal:** portable memory and multi-instance overview. Console/curation features
stay future.

**Scope:**

- **Memory streams** ([`10`](10-memory.md)): one logical stream per
  **`(project, agent)`** (agent name = partition key ⇒ **single writer by
  construction**, no memory write-contention). Unit kinds `session_handoff` /
  `distilled_fact` / `timeline_entry`; **minimal provenance** on every unit
  (`agent, host_id, run_id, created_at, source`, optional `session_id`,
  `supersedes`). Versioned blob layout (immutable units + pushed checkpoints).
- **Sync = write-local → push-on-handoff → pull-on-start → last-writer-wins**
  ([`09` §3](09-synchronization.md), [`10` §6](10-memory.md)):
  - **Write local always** — `.sprawl/memory/` is the working copy + offline
    fallback; a hub outage never stalls a memory write.
  - **PUSH on handoff** — the whole agent stream head is uploaded as a checkpoint
    at the natural `/handoff` boundary.
  - **PULL on session start** — a starting host fetches the latest checkpoint;
    if the hub copy is newer, overwrite local. A new host is just the extreme
    case (empty local ⇒ take the hub copy wholesale).
  - **Last-writer-wins by `created_at`** — safe **because of the single-writer-
    by-agent-name invariant**, not because of merging. **No version vectors, no
    force-reclaim, no semantic/textual merge, no reconcile engine** — there is
    nothing to reconcile with one writer. Provenance *metadata* is recorded for a
    future console/synthesis pass, but v1 never invokes a reconcile algorithm.
- **Concurrency guard = the same advisory active-host marker** (per
  `(project, agent)` stream): a second host starting the same agent while a marker
  is fresh is advised + rejected; a stale marker is taken over; a genuine race
  falls to LWW — an **accepted single-user limitation** (worst case: one host's
  un-pushed handoff is overwritten), not a merge case ([`10` §6](10-memory.md)).
- **No textual merge, ever** — combining memory is **curation or synthesis**
  (an agent reads the union and writes a new distilled unit that `supersedes` the
  inputs), never a git-style line-merge (incoherent Frankentext). This is the
  contract any *future* combine must honor; v1 simply never combines
  ([`10` §4](10-memory.md)).
- **Session transcript archive**: full transcripts per `(project, agent,
  session_id)` are **write-and-store only, kept indefinitely (no GC, no retention
  window)**. Nothing reads them yet; they exist so future consumers (console,
  review, search, memory-eval) become possible without a data-model change
  ([`07` §5](07-storage-persistence.md), [`10` §5](10-memory.md)). Privacy is
  bounded by the single-user + self-hostable + authenticated posture
  ([`security-privacy` §7](security-privacy.md)).
- **Bird's-eye status pane**: `ListInstances` enriched (active-host marker holder,
  N-clients, last-seen, per-session last-seq) — the multi-host "single pane of
  glass" overview.

**Explicitly future (NOT Phase 3):** memory/session **console** (browse/curate);
user-addressable sub-agents; the north-star org model
([`00`](00-overview.md#north-star-vision--not-committed--future)); the write-lease
+ fence + version-vector + semantic-reconcile stack (returns only with
multi-writer — [`09` §5](09-synchronization.md)).

**Done when:** weave's memory survives a host switch (pull on a new machine),
last-writer-wins resolves a two-host push cleanly, and the status pane shows all
connected instances at a glance.

---

## 4. Cost / scaling envelope (single-user MVP — order of magnitude only)

The hub is a companion for *a handful* of one maintainer's instances and one or
two human viewers — **not** a multi-tenant SaaS fleet ([`01` §0](01-architecture.md),
[`05`](05-observability.md)). Sizing is deliberately tiny; these are
order-of-magnitude figures for a generic public-cloud target ("Azure" as a
stand-in), **not** a quote. The re-scope barely moves the envelope — it removes
some secrets and the GC/snapshot machinery, and makes blob growth unbounded (but
one user's footprint is small).

| Component | MVP shape | Rough monthly envelope (OoM) | Notes |
|---|---|---|---|
| **Container** (`./hubd`) | 1 small always-on instance (~0.5 vCPU / 1 GB), single replica | **~$15–40** | Single instance holds in-memory advisory-marker/fan-out + held-open server-streams. Serverless-container scale-to-zero is a *poor* fit (long-lived downlink streams ⇒ effectively always-on). |
| **Managed Postgres** | Smallest burstable tier, backups on, private networking | **~$15–50** | Light **index/registry** only — users(one), tokens(hashed), hosts, projects, active_host, sessions, per-`(session,seq)` stream index, memory-unit + attachment index. Bodies live in blob, so PG stays small. |
| **Object storage** (blob) | Seq'd transcript bodies + attachments + memory/transcript bodies, **kept indefinitely (no GC)** | **~$1–5 + egress** | Attachments ≤10 MB each. **No lifecycle GC in v1** — storage grows unbounded, but a single user's yearly footprint is small (revisit only if it bites, [`07` §5](07-storage-persistence.md)). Egress on browser blob-fetch is the swing factor. |
| **Secrets store** | The single bearer token + session-cookie signing key + host-token pepper + DB creds | **~$0–3** | A few secrets; negligible. **No OIDC client secret** (no OIDC). |
| **Egress / networking** | Heartbeated streams + fan-out | **low, but the wildcard** | ~20–30s on-stream heartbeats × few streams = trivial bytes; per-event fan-out to 1–2 browsers is small. Blob egress dominates if screenshot-heavy. |
| **TF remote state** | Tiny state container | **~$0–1** | One-time bootstrap. |

**Envelope: roughly ~$35–100/month** for a single-user MVP, dominated by the
always-on container + Postgres floor. Two structural cost levers:

- **Always-on floor is unavoidable** given long-lived downlink streams — the one
  place the "persistent connection" model costs real money. Scale-to-zero is off
  the table for the hub tier.
- **Blob egress** is the only super-linear term, and only under heavy attachment
  use; the base64-inline-by-reference design keeps large bytes off the RPC/log
  path. With no GC, *storage* also grows monotonically, but at one user's volume
  that's a rounding error, not a lever.

**Scaling ceiling (single container):** the open sizing question is *how many
concurrent held-open server-streams a single Go container holds before goroutine/
memory pressure matters* ([`03`](03-api-surfaces.md)/[`08`](08-deployment.md) OQ).
For single-user (a handful of hosts + 1–2 browsers) this is a non-issue;
multi-instance scale-out (externalized marker/fan-out registry, sticky vs.
stateless-over-Postgres routing) is deferred until a real second user or fan-out
ceiling forces it ([`06`](06-iac.md) OQ).

---

## 5. Cross-cutting standing choices (KISS/YAGNI recap)

The load-bearing "simplest vs. right + recommendation" calls, consolidated so the
plan doesn't relitigate them per phase (full rationale in the cited leaf docs).
Rows marked **▸ v2 cut** reflect where the re-scope simplified the earlier call.

| Choice | Simplest | **Recommendation (right-sized for single-user v2)** | Source |
|---|---|---|---|
| Event delivery | latest-state-only, full reload | **one durable append-only seq'd log + delta replay; NO snapshot tier** ▸ v2 cut | [`01`](01-architecture.md)/[`07`](07-storage-persistence.md)/[`09`](09-synchronization.md) |
| Host↔hub transport | one bidi stream forever | **server-stream downlink + unary uplink, expect disconnects** (already reconciled in `01`/`03`) | [`03`](03-api-surfaces.md) |
| Write authority | global lock, last-conn-wins | **trivial advisory active-host marker; NO fence/lease/epoch** ▸ v2 cut | [`01`](01-architecture.md)/[`09`](09-synchronization.md) |
| Host auth | shared secret | **per-host hashed bearer tokens (argon2id+pepper), revocable** | [`04`](04-authentication.md)/[`security-privacy`](security-privacy.md) |
| Browser auth | basic-auth / shared password | **one configured bearer token → httpOnly session cookie; NO OIDC** ▸ v2 cut | [`04`](04-authentication.md) |
| Memory sync | git line-merge | **write-local → push-on-handoff → pull-on-start → last-writer-wins; NO version-vector/reconcile; never textual** ▸ v2 cut | [`09`](09-synchronization.md)/[`10`](10-memory.md) |
| Storage seam | inline SQL + blob SDK | **`Store` interface, `memStore`+`pgStore`, goose migrations** | [`07`](07-storage-persistence.md) |
| Blob/secrets | local FS + env vars | **`gocloud.dev/blob`+`/secrets` from day one** | [`02`](02-components.md)/[`07`](07-storage-persistence.md) |
| Retention | GC / retention windows | **keep everything indefinitely; NO GC in v1** ▸ v2 cut | [`07`](07-storage-persistence.md)/[`09`](09-synchronization.md)/[`10`](10-memory.md) |
| Frontend | (choose) | **React 19 + Vite + connect-web/-query, `go:embed`** | [`11`](11-frontend-stack.md) |
| Deploy | recreate; embed-only | **single container, embed toggle, rolling+drain** | [`08`](08-deployment.md) |
| Observability | grep logs / one healthz | **slog/JSON + `/debug/state` + healthz/readyz split; metrics/tracing = seams** | [`05`](05-observability.md) |
| Schema evolution | be careful | **`buf breaking` in CI + additive-only, day one** | [`03`](03-api-surfaces.md)/[`08`](08-deployment.md) |
| IaC | click-ops / flat dir | **Terraform, `azure/` root + `modules/` contracts + `bootstrap/` state** | [`06`](06-iac.md) |
| Attachments | (verify) inline | **base64 blocks + blob-store-by-reference; `/attach` first; Files API deferred** | [`attachments-multimodal`](attachments-multimodal.md) |
| Content trust | absolutist ZK vs plaintext | **hub-can-read default; ZK seam + per-project opt-out DEFERRED (opaque-blob seam kept)** ▸ v2 cut | [`security-privacy`](security-privacy.md) |
| Tenant isolation | trust queries | **single `user_id` value now; authz chokepoint DEFERRED to first second user** ▸ v2 cut | [`security-privacy`](security-privacy.md) |
| Test strategy | live claude + grep logs | **fixture at `RuntimeEvent` layer + hermetic fakes + `/debug/state` asserts + e2e rows** | [`12`](12-testability-local-dev.md) |

---

## 6. Consolidated master Open-Questions list

*Every v2 doc's `## Open Questions`, deduplicated, categorized, and tagged by
source. This is the required deliverable to drive the next round. Cross-doc
duplicates are merged with all sources listed. Questions the re-scope
**resolved or cut** are dropped; where a still-live question was *narrowed* by the
plan it is marked **[resolved in 13]** with the resolution. Purely multi-tenant/
future questions (OIDC allowlist internals, fence granularity, version-vector
transfer size, GC scheduling, snapshot cadence, ZK key model, multi-tenant
isolation mechanics) are **removed** — they belong to the deferred multi-user
revision, not the v1 build (their build triggers live in
[`security-privacy` §8](security-privacy.md)).*

### A. Transport, connections & reconnect (all live)
- **A1. Heartbeat interval** — fixed conservative ~20s, or negotiated per-
  connection from a hub-advertised ceiling? *(`01`, `03`)* → partly gated by the
  §2 spike.
- **A2. PING vs. on-stream DATA** — does the target managed ingress reset its
  *stream*-idle timer on HTTP/2 PING, or is an on-stream heartbeat DATA event
  mandatory (Envoy `stream_idle_timeout` trap)? *(`03`)* → the §2 spike answers.
- **A3. WebSocket fallback trigger** — what concrete spike metric flips us from
  server-stream to WS (idle-survival failure, a p95 reconnect-rate threshold, or
  App-Service-class WS-only policy)? *(`03`, `09`)*
- **A4. Downlink fan-out sizing** — how many concurrent held-open server-streams
  can one Go container hold before goroutine/memory pressure; and does a later
  scale-out need sticky routing or does stateless-over-Postgres let any container
  answer any client? *(`03`, `08`)*
- **A5. Uplink batching window** — how long may `AppendTranscript` batch before
  flush without the browser feeling laggy (100/250ms)? *(`03`)*
- **A6. Full-log cold-start ceiling** — with **no snapshots in v1**, at what
  single-session transcript length does a fresh browser full-log send feel slow on
  mobile? That threshold is the trigger to add a snapshot/compaction tier back
  (it slots *under* the one rule without changing it). *(`01`, `03`, `09`)*
- **A7. Receipt cadence (host→hub)** — how often does the hub confirm "received
  through seq W" so the host trims its outbound buffer — per-batch, timer, or
  piggybacked on the stream? Affects buffer size vs. WAN chatter. *(`09`)*
- **A8. Buffer high-water policy** — how much local outbound history before drop-
  oldest, per-session or global? v1 is memory-only (host restart ⇒ new session +
  fresh full-log), so disk-spill is out. *(`01`, `09`)*

### B. Write-authority (advisory marker)
- **B1. Active-host TTL / staleness** — how long after a dropped heartbeat before
  the advisory marker is reclaimable? Too short ⇒ a brief blip hands the session
  away; too long ⇒ a genuinely-moved user waits. *(`03`, `09`, `10`)*
- **B2. Reclaim UX** — where does the "another host is active — stop or reclaim"
  prompt surface (CLI on the second host, browser, both), and what does "stop" do
  to the first host's running turn? *(`01`, `09`)*
- **B3. Is the advisory marker enough** to avoid confusing double-driving in a
  single-user setup, or is even the lightweight "N clients connected" guard needed
  at the write layer? *(`00`, `01`, `03`)*
- **B4. Zombie-writer limitation acceptance** — confirm the product owner accepts
  the documented no-fence limitation: in the narrow reclaim window a returning
  host *can* overwrite newer state via LWW. Safe for one user; must be re-added
  for multi-writer. *(`09`, `10`)*

### C. Storage & schema
- **C1. `sqlc` vs. hand-written `pgx`** — adopt compile-checked SQL up front or
  only if query code proves error-prone? *(`07`)*
- **C2. Event body in PG vs. blob** — a size threshold above which a body spills
  to blob (huge tool output) vs. inline small events in the PG index row; or cap
  event size upstream and inline everything? *(`07`)*
- **C3. Blob key scheme & bucket layout** — do session-stream events, attachments,
  and memory bodies share a bucket with key prefixes, or separate buckets per data
  class? (Single-user ⇒ no tenant dimension in the key.) *(`07`, `06`, `10`)*
- **C4. When does "keep everything" break?** — at what stored-volume/cost point
  should retention be reconsidered ([`07` §5](07-storage-persistence.md))? Worth a
  rough back-of-envelope on one user's yearly transcript + attachment footprint.
  *(`07`)*
- **C5. De-dup key for replayed memory units** — under LWW, is
  `(host_id, session_id, created_at)` sufficient to de-duplicate re-uploaded
  units, or do they need a content hash / stable `unit-id`? *(`07`, `10`)*

### D. Memory
- **D1. Checkpoint granularity** — whole-stream-head-at-handoff (chosen), or
  per-unit-delta to shrink upload for a large `persistent.md`? (Delta push edges
  back toward version vectors — resist unless size actually hurts.) *(`10`)*
- **D2. Push trigger beyond handoff** — also push on clean session exit or on a
  timer to bound data loss if a session runs long before handoff, or accept
  "handoff-or-lose" for v1? *(`09`, `10`)*
- **D3. Pull-overwrite safety on crash** — if local has un-pushed writes newer
  than the hub checkpoint (crash before push), should pull-on-start still
  overwrite, or detect local-newer and skip? *(`10`)*
- **D4. Cross-project / user-level memory** — a `(user, *)` global stream for
  preferences vs. per-project only (YAGNI-flagged for v1). *(`10`)*
- **D5. `injected` memory authority** — when the browser injects a note, whose
  `host_id` does it carry, and does it interact with the advisory marker?
  *(`10`)*

### E. Auth & session (single-user)
- **E1. Host-token hashing cost** — argon2id (memory-hard) vs. bcrypt given
  per-uplink verification; or cache a verified `(tokenid → host_id)` for a short
  TTL to avoid hashing every call? *(`04`)*
- **E2. Session store backing** — signed **stateless** cookie (unrevokable pre-
  expiry) vs. **server-side** session table (revocable, +1 read/request). §6 leans
  server-side — acceptable, or is a short-TTL stateless cookie enough for v1?
  *(`04`)*
- **E3. Bearer-token rotation** — the single configured token is the whole auth
  surface; how is it rotated without dropping every host + browser at once, and is
  a brief accept-old+new overlap window worth building even at single-user scale?
  *(`01`, `03`, `04`, `security-privacy`)*
- **E4. Host-token ↔ host_id binding** — bind on first use (flexible, leak-before-
  use risk) vs. name the host at create time (stricter attribution). *(`04`)*
- **E5. Multiple browsers/devices** — one shared session vs. independent per-
  device sessions with independent logout (ties to E2). *(`04`)*
- **E6. Host-token scope** — in v1 a token authenticates a host for *all* its
  projects; when per-project authZ lands (multi-tenant), should tokens carry a
  scope, or does scoping live entirely in the authZ layer? (Flagged so the token
  shape doesn't foreclose it.) *(`04`, `security-privacy`)*

### F. Security & privacy (single-user posture)
- **F1. Session-cookie hardening** — beyond httpOnly/Secure/SameSite, does the
  browser session need an idle timeout / no-persistence mode for a shared/kiosk
  device, even in single-user v1? *(`security-privacy`)*
- **F2. Transcript redaction on ingest** — given no-GC means raw transcripts
  persist forever and carry pasted secrets/PII, is a lightweight secret-scrubbing
  pass on ingest worth doing in v1 anyway? *(`security-privacy`, `10`,
  `attachments-multimodal`)*
- **F3. Attachment retention vs. the log** — screenshots may contain secrets; does
  the blob store want a shorter retention / on-demand purge for attachments than
  the "keep everything" default, even in v1? *(`attachments-multimodal`,
  `security-privacy`)*
- **F4. Multi-tenant trigger timing** — §8 keys the whole OIDC/isolation/PAT/ZK
  stack to "the first second user." Is there an intermediate "share read-only with
  one trusted person" step that would force a subset earlier? *(`security-privacy`)*

### G. Attachments / multimodal
- **G1. Files API vs. inline base64 for multi-turn** — base64 re-sends full bytes
  on every history replay; switch to `file_id` (host uploads to Anthropic Files
  API, beta header, tracks ids) if screenshot-heavy sessions get expensive — worth
  it or premature? *(`attachments-multimodal`)* → plan **defers** Files API.
- **G2. Blob fetch channel** — host pulls attachment bytes over the persistent
  conn vs. a separate authenticated HTTPS GET (simpler, second auth surface).
  *(`attachments-multimodal`)*
- **G3. Where to enforce size/format** — browser (fast, spoofable) / hub (single
  choke point) / host (authoritative, wasted upload on reject); lean host-
  authoritative + browser-advisory. *(`attachments-multimodal`)*
- **G4. `--replay-user-messages` echo of image turns** — confirm the CLI echoes an
  image-bearing user message intact (uuid preserved) so the consumption-ack
  contract holds for multimodal turns (text verified; image to be smoke-tested).
  *(`attachments-multimodal`)*
- **G5. Turn-queue ordering with attachments** — if a text turn and an image turn
  enqueue near-simultaneously from different sources, is strict arrival order
  right, or should an attachment "stick" to its accompanying text? (Overlaps I2.)
  *(`attachments-multimodal`)*
- **G6. Max practical inline size on stdin** — confirm the combined stream-json
  line (base64 + ~1.37× JSON overhead) stays under the host's 10 MB stdin write
  limit. *(`attachments-multimodal`)*

### H. Deployment, IaC & observability
- **H1. `buf breaking` baseline** — track `main` HEAD vs. last-released hub tag so
  in-flight `main` churn doesn't block PRs (and where the baseline image lives).
  *(`03`, `08`, `12`)*
- **H2. Embed toggle mechanism** — build tag vs. runtime flag vs. both; one image
  that runs either way, or two images (embedded / API-only)? *(`08`, `11`)*
- **H3. Graceful-drain grace period** — fixed (10–30s) vs. tied to the platform's
  termination grace period. *(`08`)*
- **H4. Rollout on the target platform** — does it support true rolling deploys
  with readiness gating out of the box, or does TF need explicit revision/traffic-
  split config? *(`08`, `06`)*
- **H5. Single-machine co-located deploy** — a blessed "hub + one host on one box"
  convenience deploy; does it change packaging (compose file) or is it just
  config? *(`00`, `08`)*
- **H6. Single-instance → multi-instance trigger** — at what point does
  externalizing the in-memory advisory-marker/fan-out registry become necessary,
  and does it change the IaC contract (LB, shared cache)? *(`06`, overlaps A4)*
- **H7. Managed identity vs. connection strings** — how uniformly can host→(DB,
  bucket, secrets) rely on managed identity across the first cloud, and does the
  fallback muddy the contract `aws/` must mirror? *(`06`)*
- **H8. AWS parity trigger** — what concrete event justifies building `aws/`
  (second target? AWS contributor?); until then is the README stub + stable
  output-name contract enough? *(`06`)*
- **H9. TF state backend location & generated secrets** — same cloud/account as
  the hub or separated for blast-radius; and is encrypted remote state enough for
  generated secrets, or should all secret material be issued out-of-band? *(`06`)*
- **H10. `/debug/state` in prod** — compiled out via build tag (safest) vs. auth-
  gated off-by-default (useful, larger surface); and flat snapshot vs. `?project=`
  filtering at multi-host scale. *(`05`, `12`, `security-privacy`)*
- **H11. Host-side introspection surface** — does the host half need its own
  `/debug/state`-shaped surface for e2e, or is `peek` + the incident snapshot
  enough to assert host buffer depth / last-acked seq? *(`05`, `12`)*
- **H12. `trace_id` origin** — generated at the browser edge, host edge, or minted
  by the hub on first contact (affects whether a locally-typed, no-browser turn
  gets a trace id). *(`05`)*
- **H13. Metrics/tracing trigger** — what concrete incident flips the deferred
  `/metrics` + OTel-spans seams from dark to on, so we don't build "just in case"?
  *(`05`)*

### I. Testing & product scope
- **I1. Scripted-stream fixture format & home** — checked-in JSON `RuntimeEvent`
  table vs. a small DSL; where fixtures live so Go seam tests and the TS
  `web-contract` row share one source (recorded sessions risk public-repo leakage).
  *(`12`)*
- **I2. testcontainers in CI** — is Docker-in-Docker allowed for the Postgres /
  `hub-fullstack` rows, or do those stay local-only + soak on the in-memory arm?
  *(`12`)*
- **I3. Golden-fixture regeneration policy** — a reviewed `make regen-contract-
  goldens` so an intended wire change is one deliberate diff, not silent drift.
  *(`12`)*
- **I4. Frontend test toolchain** — the SPA choice constrains the headless runner
  and whether `connect-es` golden assertions share tooling with render tests.
  *(`11`, `12`)*
- **I5. compose ↔ Terraform fidelity** — must local `docker-compose` use the same
  images as the TF prod stack, or just the same shapes, to be a trustworthy smoke?
  *(`12`, `06`, `08`)*
- **I6. Cross-source turn-queue ordering** — when local TUI *and* browser enqueue,
  is strict arrival order sufficient, or is source tagging needed for UX clarity?
  *(`01`, `09`)* → plan ships **no source tag in v1** (add if confusing).
- **I7. Smallest useful MVP slice** — *(`00`)* → **[resolved in 13]**: read-only
  single pane (Phase 1) first, downlink (Phase 2) next.
- **I8. Per-host push of attachments in v1** — *(`00`)* → **[resolved in 13]**:
  browser upload + local `/attach`; per-host push deferred.
- **I9. `user_id` hedge scope** — which durable tables carry the column now so a
  later multi-tenant migration is a schema no-op? *(`01`, `security-privacy`)* →
  plan carries it on every durable row (users/tokens/projects/instances/streams),
  always one value.
- **I10. North-star schema anticipation** — how much of the org model should the
  foundation schema anticipate now vs. migrate later? *(`00`)* → plan keys memory
  per-`(project, agent)` (free, future-enabling) but builds only single-user
  behavior; broader org model deferred.

---

## Open Questions (my own — beyond the consolidated list)

- **Spike platform ≠ eventual platform.** The §2 spike must run on the *actual*
  target managed platform, but the docs keep the platform parameterized ("Azure"
  as a generic stand-in). If the maintainer's real target differs from the spike
  target, the idle-timeout/heartbeat findings may not transfer. Name a concrete
  spike target (privately), or run the spike on ≥2 platforms to bound the answer?
- **Phase-0 "registers an instance" needs a live event to be meaningful.** With
  no data flowing, a registration is hard to verify beyond a `/debug/state` row.
  Is a single synthetic heartbeat/hello event acceptable in Phase 0 to prove the
  round-trip, or does that blur the Phase-0/1 boundary?
- **Attachment schema change lands before the hub exists.** The
  `MessageParam.Blocks` union + `/attach` (Phase 2, host-local) touches
  `internal/protocol/types.go` and the mandatory `replay-echo` e2e matrix row (per
  `CLAUDE.md`). Should it be pulled *earlier* (into Phase 0/1) as a standalone,
  hub-independent, verified-feasible slice — the cheapest possible end-to-end
  multimodal proof?
- **No doc owns end-to-end latency budget.** Individual targets exist (downlink
  <500ms p95, heartbeat 20–30s, uplink batch 100–250ms) but nothing composes them
  into a single "browser keypress → claude sees it → response on screen" budget.
  Worth defining before the spike so its round-trip test (§2.4) has a pass/fail bar.
- **Cost floor vs. scale-to-zero.** The ~$35–100/mo floor is dominated by the
  always-on container needed for long-lived streams. If cost mattered more than
  latency, a poll- or WS-with-reconnect-on-wake model *could* allow scale-to-zero
  — explicitly *not* recommended (kills live-tail UX), but recorded as the one
  lever that would move the floor.
- **"Keep everything" has no exit ramp defined.** [`07` §5](07-storage-persistence.md)
  keeps everything until it "becomes a real problem," but no metric defines that
  point. Worth a rough storage-growth projection now so the retention/GC decision
  is data-driven when it eventually fires, not a panic.
