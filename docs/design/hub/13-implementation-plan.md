# 13 — Implementation Plan (MVP Sprint)

*The synthesis doc. Written last. It turns docs 00–12 + `security-privacy` +
`attachments-multimodal` into a phased build plan, reconciles the one place the
docs disagree (transport), calls out the de-risking spike that must run first,
sketches a cost envelope, and consolidates every doc's `## Open Questions` into
one master list for the maintainer.*

See also: [`00-overview`](00-overview.md) · [`01-architecture`](01-architecture.md) · [`03-api-surfaces`](03-api-surfaces.md) · [index](README.md)

---

## 0. The MVP thesis — sprint at the architecture, not the feature set

The maintainer's explicit goal: **build the MINIMAL feature set that establishes
the RIGHT architecture to build out the full thing.** This plan is organized
around one question — *what is the smallest slice that proves the load-bearing
spine end-to-end?* — and defers everything that doesn't serve that proof.

**The load-bearing spine (the one thing that must be right):**

```
claude → local eventbus (seq'd) → uplink → HUB (append log + fan-out) → browser
                                                    ▲
browser input → downlink → host turn-queue ─────────┘   (result re-enters uplink)
```

Two properties make this spine *the* architecture, and both are painful to
retrofit:

1. **One seq'd, resumable event log** with the **one rule** — *replay from my
   last seq, else snapshot, then live-tail* — implemented **once** and reused at
   every seam ([`01` §2](01-architecture.md), [`09`](09-synchronization.md)).
2. **Write-authority done safely** — TTL lease + monotonic fence token, so a
   zombie/late writer can never clobber ([`01` §4](01-architecture.md),
   [`09`](09-synchronization.md)). *This is the single correctness guarantee the
   docs unanimously say to build now, not later.*

Everything else — memory portability, attachments, bird's-eye status, the
north-star org model — rides on top and is deferred until the spine is proven.

**What the MVP deliberately is NOT** (YAGNI, per [`01` §8](01-architecture.md)):
multiplayer/driver-lock/presence; per-agent write-leases (schema-keyed, enforced
per-project); the north-star org model; any default/hardcoded hub endpoint;
zero-knowledge encryption; metrics/tracing *servers*.

---

## 1. Transport reconciliation — `01` is superseded by `03` on one point

The docs conflict on exactly one architectural point, and this plan resolves it
in favor of `03`.

| | `01-architecture` framing | `03-api-surfaces` conclusion (**authoritative**) |
|---|---|---|
| Host↔hub conn | "one persistent **bidirectional** connection" | **DO NOT** bet on a single long-lived full-duplex bidi stream |
| Shape | bidi uplink+downlink multiplexed | **held-open server-stream (downlink) + unary/batched (uplink)** |
| Why | NAT dial-out, heartbeat=connection | full-duplex is fragile on managed L7 ingress and **impossible from a browser** (no `duplex:'full'` in any stable browser as of 2026) |

**Decision for this plan:** Phase 0/1 use the **`03` model** —
**heartbeated server-stream downlink + unary/batched uplink**, with the *one
rule* reconnect covering both the host↔hub and browser↔hub seams identically.
The "persistent connection" is persistent in the sense of *continuously
re-established*, not *never dropped*; cuts are **expected** and made cheap/correct
by the seq'd log + snapshot.

> **Doc-revision to make later (NOT done here — I do not edit `01`):**
> `01-architecture.md` §1/§6 and its "persistent **bidirectional** connection"
> language are **superseded** by `03` §1/§4 on the transport shape. When someone
> next revises `01`, reword "one persistent bidirectional connection" to
> "one continuously-re-established downlink server-stream + unary uplink,"
> cross-referencing `03`. The *conceptual* claim in `01` ("heartbeat = the
> connection = lease liveness") survives — the heartbeat just rides the
> server-stream as an on-stream DATA event, not a full-duplex frame.

The bidi upgrade stays available later purely as an optimization: the protobuf
wire types don't change, only who initiates frames.

---

## 2. FIRST TASK — the de-risking SPIKE (before committing Phase 1)

Per [`03` §5](03-api-surfaces.md), **run a ~1–2 day spike against a real managed
container platform (not localhost) BEFORE committing the streaming shape or
building Phase 1.** The whole broker only works if the host's downlink survives
cloud L7 load balancers, and the platform actively fights long-lived streams
(Container Apps ~240s, App Service hard ~230s TCP, generic ingress ~60s idle —
[`03` §4.2](03-api-surfaces.md)).

**The spike measures** (from [`03` §5](03-api-surfaces.md)):

1. **Idle survival** — open a server-stream, send nothing, time until the LB cuts
   it (expect 60–240s).
2. **Heartbeat efficacy** — find the max app-level heartbeat interval that keeps
   the stream alive ≥30 min. **Distinguish HTTP/2 PING vs. on-stream DATA** (the
   Envoy `stream_idle_timeout` trap — PINGs may not reset a *per-stream* idle
   timer; on-stream DATA may be mandatory).
3. **Buffering / latency** — first-byte + per-event latency; confirm events
   arrive individually, not buffered into useless batches.
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

> The spike de-risks the one assumption the entire product rests on for ~2 days
> of work. **Do not commit the "one persistent connection" assumption until it
> passes.**

---

## 3. Phased plan

Each phase is independently valuable and leaves `main` shippable. Phases 0–1 are
the architecture-proving core; 2–3 build breadth on the proven spine.

### Phase 0 — connectivity/auth spine (no data yet)

**Goal:** prove **auth + NAT dial-out end-to-end.** `sprawl enter` dials out and
*registers an instance*; nothing streams yet.

**Scope:**

- **Proto/Connect contract + `buf` toolchain** from day one: `buf generate` →
  Go (`connect-go`) + TS (`connect-es`); `buf lint`/`buf format`/**`buf breaking`
  in CI** wired into `make validate` (Go-only repo today) ([`03` §3](03-api-surfaces.md),
  [`08`](08-deployment.md)). Additive-only field policy; never reuse field numbers.
- **Single Go container skeleton** (`./hubd`): one Connect listener; `go:embed`
  SPA seam (empty shell is fine); `/healthz` (deps-free) + `/readyz`
  (deps-checked) split; graceful `SIGTERM` drain ([`08`](08-deployment.md),
  [`05` §4](05-observability.md)).
- **`Store` interface + two impls** (`memStore`, `pgStore`) behind
  dependency-injection, from the start; **goose** migrations embedded via
  `embed.FS`, applied by `pgStore.Migrate` at boot; `sprawl hub migrate`
  subcommand ([`07`](07-storage-persistence.md)). Phase-0 tables:
  `users`, `tokens`, `hosts`, `projects`, `leases`, `runs/instances`.
- **`gocloud.dev/blob` + `gocloud.dev/secrets`** wired (memblob/fileblob + local
  secrets impl in dev/test) — the abstraction *is* the local-dev backend, so it
  costs ~nil ([`07`](07-storage-persistence.md), [`06`](06-iac.md)).
- **Auth, for real** ([`04`](04-authentication.md), [`security-privacy`](security-privacy.md)):
  - **OIDC relying-party** (Authorization Code + PKCE `S256`, `state`+`nonce`),
    `go-oidc`; IdP issuer/client-id/secret are **deploy parameters**, never
    compiled in. Server-minted signed **httpOnly/Secure/SameSite=Lax** session
    cookie; SPA never sees IdP tokens.
  - **Host→hub PATs**: `sprawl_pat_<id>_<secret>`, stored **hashed** (argon2id +
    pepper from secrets path), per-host binding, create(show-once)/revoke/rotate.
    **Token never on a CLI flag / URL / log** — resolved from 0600 file / env /
    secrets ref only (dodges the QUM-728 snapshot-leak vector; hard rule).
  - **Single-user allowlist** seeded with one subject id (no identity committed).
- **Instance registration** RPC: `sprawl enter` dials out with its PAT, the hub
  records `{host_id, run_id, project}` and surfaces it in `ListInstances`.
- **`--hub-url` / env / config** plumbing with **default firmly empty** (public-
  repo hygiene); log resolved endpoint host-only, token redacted
  ([`02` §2.6](02-components.md)).
- **Observability floor**: slog/JSON with canonical attrs
  (`run_id/host_id/seq/fence/component/trace_id`); `/debug/state` endpoint
  (gated, read-only) from day one ([`05`](05-observability.md)).
- **IaC** ([`06`](06-iac.md)): `bootstrap/` (remote encrypted TF state) + `azure/`
  concrete root + `modules/` capability contracts (container-host, database,
  object-store, secrets). `terraform.tfvars.example` placeholders only; no
  instance-specific defaults. `aws/` = README stub.

**Simplest vs. right (Phase 0):** the temptation is a shared secret + basic-auth
+ inline SQL. **Right way — real OIDC + hashed per-host PATs + `Store` interface
+ `buf breaking` — is chosen now** because auth *is* the reason the hub exists,
per-host revocation/attribution is the point, and the wire contract + storage
seam are exactly what's ruinous to retrofit once three deployables evolve
independently. Cost: a few days more than the toy version; unanimously endorsed
by [`02`](02-components.md)/[`04`](04-authentication.md)/[`07`](07-storage-persistence.md).

**Done when:** a real host dials out through a real deployed hub, authenticates
(OIDC in browser, PAT on host), and appears in `ListInstances`; `/debug/state`
shows the connection + lease registry; `buf breaking` gates CI.

### Phase 1 — read-only single pane (the resume, one rule)

**Goal:** uplink the seq'd event stream and **live-tail one session in the
browser.** This alone kills window-juggling and gives remote/mobile *view*.

> **Gate:** the §2 spike must have passed with a heartbeated server-stream (or the
> WS fallback chosen) before this phase commits.

**Scope:**

- **Host-side hub client**: subscribe to the existing local seq'd eventbus
  (reuse the subscriber API unchanged; honor its drop telemetry —
  [`02` §2.1](02-components.md)); **bounded local outbound buffer**, drop-oldest
  past high-water, **one log per truncation** + a `truncated-from` marker
  ([`01` §3](01-architecture.md), [`09`](09-synchronization.md)).
- **Uplink**: `PushEvents` **unary/batched** carrying
  `{host_id, run_id, fence_token, events[], from_seq}`. Hub appends **in seq
  order per run**, **rejecting stale-fence frames before the store**
  ([`02` §1.2](02-components.md), [`03` §1](03-api-surfaces.md)).
- **Event-log store**: append-only per-`run-id` log in PG + **periodic snapshots**
  (hybrid cadence: `events_since ≥ N` OR `time_since ≥ T`), snapshot bodies in
  blob, index in PG ([`01` §2](01-architecture.md), [`07`](07-storage-persistence.md),
  [`09`](09-synchronization.md)). Retention trimmed to
  `min(ack watermarks, latest snapshot seq)` + safety margin; never below the
  snapshot floor; log bulk deletions.
- **Downlink fan-out**: `SubscribeInstance` **held-open server-stream** with an
  **on-stream heartbeat every ~20–30s** (beats the 60–240s ceilings; doubles as
  lease-liveness). Browsers follow the **one rule** too — same code path
  ([`02` §1.3](02-components.md), [`03` §4.5](03-api-surfaces.md)).
- **Lease + fence enforced** per-project (schema-keyed for per-agent later):
  `ClaimLease/RenewLease/ReleaseLease` returning `fence_token`; **per-lease
  `epoch`** bumped in the same txn that grants/reclaims (monotonic by
  construction, survives failover — [`07` §3](07-storage-persistence.md),
  [`09`](09-synchronization.md)). Renew doubles as explicit heartbeat when quiet.
- **Version-vector reconnect**: keyed **per-`run-id` → last_seq** for events
  (separate `(project,agent)` keying reserved for memory in Phase 3)
  ([`09`](09-synchronization.md)).
- **SPA (React 19 + Vite + `@connectrpc/connect-web` / `connect-query`)**
  ([`11`](11-frontend-stack.md)): OIDC-gated shell; `ListInstances` switcher;
  **live-tail** via a bare `connect-web` `for await` loop appending into an
  external append-only store (Zustand / `useSyncExternalStore`), **virtualized**
  log view (`@tanstack/react-virtual`); reconnect-per-one-rule in a
  framework-agnostic plain-TS transport module (escape hatch to Solid stays
  cheap). `go:embed`'d, no SSR.
- **`trace_id`** propagated through frames + logs ([`05` §3.2](05-observability.md)).

**Simplest vs. right (Phase 1):** simplest = hub stores only the latest snapshot,
clients full-reload on every reconnect. **Right — append-only log + periodic
snapshots + delta replay — is chosen** because delta-replay is what makes mobile
reconnect feel *instant*, and the reconnect machinery already exists locally
(QUM-775 seq'd eventbus); we're extending resilience sprawl already has, not
inventing it.

**Done when:** from a phone/laptop browser, the maintainer sees the live output
of a running `sprawl enter` session, and a network blip (roam wifi↔mobile)
resumes with zero gaps/dupes via `from_seq` replay.

### Phase 2 — downlink input + attachments

**Goal:** **type to a session from the browser**, and **upload an image → content
block** (the feasibility-verified screenshot path).

**Scope:**

- **Downlink input**: `SubmitInput` (unary) → hub → host's downlink stream →
  **the ONE turn-queue**, reusing sprawl's existing message/turn-queue plumbing;
  hub only *transports* input, never interprets it ([`01` §6](01-architecture.md),
  [`02` §2.3](02-components.md)). No source tag in v1 (add only if double-driving
  proves confusing). Lightweight **"N clients connected"** guard only — no
  driver-lock/presence.
- **Attachments (VERIFIED FEASIBLE)** ([`attachments-multimodal`](attachments-multimodal.md)):
  the `claude` CLI in **stream-json input mode** (exactly how sprawl launches it)
  **accepts `message.content` as an array of blocks including base64 `image`
  blocks** (verified against the Agent SDK streaming-input contract; single-
  message mode does **not** — only streaming input does).
  - **Schema change** in `internal/protocol/types.go`: `MessageParam.Content`
    (string) → typed union adding `Blocks []ContentBlock` (+ `ImageSource`) with
    a **custom `MarshalJSON`** — 100% wire-back-compat (text turns still emit
    `"content":"…"`). This is the one thing ugly to retrofit; do the typed union.
  - **Browser path (primary)**: browser uploads bytes → hub blob store (mint
    `{attachment_id, media_type, size, sha256}`); the downlink turn carries
    **refs, not bytes**; the **host pulls bytes** from blob, base64-encodes,
    sniffs media_type (∈ jpeg/png/gif/webp, ≤10 MB), assembles
    `Blocks: [image…, text…]` (image-then-text), enqueues. Bytes never ride the
    event log ("broker, not brain").
  - **Local `/attach <path>` (secondary, ship FIRST)**: bytes already on host →
    skip hub entirely → same `Blocks` assembly. Terminal-agnostic; proves the
    multimodal plumbing end-to-end **before** the hub blob round-trip. True TUI
    clipboard/bracketed-paste capture is deferred.

**Simplest vs. right (attachments):** simplest = inline base64 blocks + blob-
store-by-reference (chosen for v1). Right-later = Anthropic **Files API
`file_id`** to avoid re-sending bytes on every history replay — **deferred** until
screenshot-heavy sessions prove the re-send cost real (YAGNI).

**Done when:** the maintainer types into a live session from the browser and the
turn lands; and an image dropped in the browser (or `/attach`'d locally) reaches
claude as an image content block and gets a vision response.

### Phase 3 — memory + session archive sync + bird's-eye status

**Goal:** portable memory and multi-instance overview. Console/curation features
stay future.

**Scope:**

- **Memory streams** ([`10`](10-memory.md)): one logical stream per
  **`(project, agent)`** (agent name = partition key ⇒ **single writer by
  construction**, no memory write-contention). Unit kinds `session_handoff` /
  `distilled_fact` / `timeline_entry`; **minimal provenance** on every unit
  (`agent, host_id, run_id, created_at, source`, optional `session_id`,
  `supersedes`). Versioned blob layout (immutable units + snapshots).
- **No textual merge, ever** — combining memory is **curation or synthesis**.
  **LWW for the stream head** (advance to fence-winner, no stall), **retain-both
  for losing units**; a `source: synthesized` reconcile pass folds divergent
  lineages back in async. Git-line-merge of prose = incoherent Frankentext; the
  unit of meaning isn't the line.
- **Sync guarded by the same lease + fence** as events; **opt-in**: a
  **"sync these memories?"** prompt (once per stream, remember the decision), and
  on force-reclaim divergence a prompt surfacing *what* diverged before synthesis
  runs. Disconnected-by-default: writes local first (`.sprawl/memory/`), then
  fence-guarded uplink; bounded buffer on hub outage.
- **Session archive / transcript sync**: write-and-store only in v1 (future
  consumers: console, review, search), behind a **retention window**; privacy-
  gated ([`07`](07-storage-persistence.md), [`10`](10-memory.md)).
- **Force-reclaim reconcile** flow end-to-end: version-vector compare →
  local-ahead+holds-lease PUSH / hub-ahead PULL / genuine-divergence (only via
  force-reclaim) → provenance-based semantic reconcile, never textual
  ([`09`](09-synchronization.md)).
- **Bird's-eye status pane**: `ListInstances` enriched (lease holder, N-clients,
  last-seen, per-run last-seq) — the multi-host "single pane of glass" overview.

**Explicitly future (NOT Phase 3):** memory/session **console** (browse/curate);
user-addressable sub-agents; the north-star org model
([`00`](00-overview.md#north-star-vision--not-committed--future)).

**Done when:** weave's memory survives a host switch (pull on a new machine), a
force-reclaim preserves both lineages with provenance, and the status pane shows
all connected instances at a glance.

---

## 4. Cost / scaling envelope (single-user MVP — order of magnitude only)

The hub federates *a handful* of one maintainer's instances for one or two human
viewers — **not** a multi-tenant SaaS fleet ([`05`](05-observability.md)). Sizing
is deliberately tiny; these are order-of-magnitude figures for a generic
public-cloud target ("Azure" as a stand-in), **not** a quote.

| Component | MVP shape | Rough monthly envelope (OoM) | Notes |
|---|---|---|---|
| **Container** (`./hubd`) | 1 small always-on instance (~0.5 vCPU / 1 GB), single replica | **~$15–40** | Single instance holds in-memory lease/fan-out + held-open server-streams. Serverless-container scale-to-zero is a *poor* fit (long-lived downlink streams ⇒ effectively always-on). |
| **Managed Postgres** | Smallest burstable tier, backups on, private networking | **~$15–50** | Log segments + registry + metadata; snapshot bodies live in blob, so PG stays small. |
| **Object storage** (blob) | Snapshots + attachments + memory/transcript bodies, lifecycle rules on | **~$1–5 + egress** | Attachments ≤10 MB each; lifecycle GC bounds growth. Egress on browser blob-fetch is the swing factor. |
| **Secrets store** | OIDC client secret + PAT pepper + DB creds | **~$0–3** | A few secrets; negligible. |
| **Egress / networking** | Heartbeated streams + fan-out | **low, but the wildcard** | ~20–30s on-stream heartbeats × few streams = trivial bytes; per-event fan-out to 1–2 browsers is small. Blob egress dominates if screenshot-heavy. |
| **TF remote state** | Tiny state container | **~$0–1** | One-time bootstrap. |

**Envelope: roughly ~$35–100/month** for a single-user MVP, dominated by the
always-on container + Postgres floor. Two structural cost levers:

- **Always-on floor is unavoidable** given long-lived downlink streams — the one
  place the "persistent connection" model costs real money. Scale-to-zero is off
  the table for the hub tier.
- **Blob egress** is the only super-linear term, and only under heavy attachment
  use; the base64-inline-by-reference design keeps large bytes off the RPC/log
  path, and lifecycle rules bound storage.

**Scaling ceiling (single container):** the open sizing question is *how many
concurrent held-open server-streams a single Go container holds before goroutine/
memory pressure matters* ([`03`](03-api-surfaces.md)/[`08`](08-deployment.md) OQ).
For single-user (a handful of hosts + 1–2 browsers) this is a non-issue; multi-
instance scale-out (externalized lease/fan-out registry, sticky vs. stateless-
over-Postgres routing) is deferred until a real second user or fan-out ceiling
forces it ([`06`](06-iac.md) OQ).

---

## 5. Cross-cutting standing choices (KISS/YAGNI recap)

The load-bearing "simplest vs. right + recommendation" calls, consolidated so the
plan doesn't relitigate them per phase (full rationale in the cited leaf docs):

| Choice | Simplest | **Recommendation (right-sized)** | Source |
|---|---|---|---|
| Event delivery | latest-snapshot-only, full reload | **append-only log + periodic snapshots + delta replay** | [`01`](01-architecture.md)/[`07`](07-storage-persistence.md)/[`09`](09-synchronization.md) |
| Host↔hub transport | one bidi stream forever | **server-stream downlink + unary uplink, expect disconnects** | [`03`](03-api-surfaces.md) |
| Write authority | global lock, last-conn-wins | **TTL lease + per-lease epoch/fence, now** | [`01`](01-architecture.md)/[`07`](07-storage-persistence.md)/[`09`](09-synchronization.md) |
| Host auth | shared secret | **per-host hashed PATs (argon2id+pepper), revocable** | [`04`](04-authentication.md)/[`security-privacy`](security-privacy.md) |
| Browser auth | basic-auth / shared password | **OIDC RP (PKCE) + server httpOnly session cookie** | [`04`](04-authentication.md) |
| Storage seam | inline SQL + blob SDK | **`Store` interface, `memStore`+`pgStore`, goose migrations** | [`07`](07-storage-persistence.md) |
| Blob/secrets | local FS + env vars | **`gocloud.dev/blob`+`/secrets` from day one** | [`02`](02-components.md)/[`07`](07-storage-persistence.md) |
| Frontend | (choose) | **React 19 + Vite + connect-web/-query, `go:embed`** | [`11`](11-frontend-stack.md) |
| Deploy | recreate; embed-only | **single container, embed toggle, rolling+drain** | [`08`](08-deployment.md) |
| Observability | grep logs / one healthz | **slog/JSON + `/debug/state` + healthz/readyz split; metrics/tracing = seams** | [`05`](05-observability.md) |
| Schema evolution | be careful | **`buf breaking` in CI + additive-only, day one** | [`03`](03-api-surfaces.md)/[`08`](08-deployment.md) |
| IaC | click-ops / flat dir | **Terraform, `azure/` root + `modules/` contracts + `bootstrap/` state** | [`06`](06-iac.md) |
| Memory merge | git line-merge | **never textual — curation/synthesis; LWW head + retain-both** | [`10`](10-memory.md) |
| Attachments | (verify) inline | **base64 blocks + blob-store-by-reference; `/attach` first; Files API deferred** | [`attachments-multimodal`](attachments-multimodal.md) |
| Content trust | absolutist ZK vs plaintext | **layered: hub-can-read default + per-project opt-out + self-host + encryption seam (identity now)** | [`security-privacy`](security-privacy.md) |
| Tenant isolation | trust queries | **single app-layer authz chokepoint (`WHERE owner_user_id`); RLS later as backstop** | [`security-privacy`](security-privacy.md) |
| Test strategy | live claude + grep logs | **fixture at `RuntimeEvent` layer + hermetic fakes + `/debug/state` asserts + 4 e2e rows** | [`12`](12-testability-local-dev.md) |

---

## 6. Consolidated master Open-Questions list

*Every doc's `## Open Questions`, deduplicated, categorized, and tagged by source.
This is the required deliverable to drive the next round. Cross-doc duplicates are
merged with all sources listed. Questions the plan already resolves are marked
**[resolved in 13]** with the resolution.*

### A. Transport, connections & reconnect
- **A1. Heartbeat interval** — fixed conservative ~20s, or negotiated per-
  connection from a hub-advertised ceiling? *(`01`, `03`)* → partly gated by the
  §2 spike.
- **A2. PING vs. on-stream DATA** — does the target managed ingress reset its
  *stream*-idle timer on HTTP/2 PING, or is an on-stream heartbeat DATA event
  mandatory (Envoy `stream_idle_timeout` trap)? *(`03`)* → the §2 spike answers.
- **A3. WebSocket fallback trigger** — what concrete spike metric flips us from
  server-stream to WS (idle-survival failure, p95 reconnect rate threshold, or
  App-Service-class WS-only policy)? *(`03`)*
- **A4. Downlink fan-out sizing** — how many concurrent held-open server-streams
  can one Go container hold before goroutine/memory pressure; does scale-out need
  sticky routing or does stateless-over-Postgres let any container answer any
  client? *(`03`, `08`, `02`, `11`)*
- **A5. Uplink batching window** — how long may `PushEvents` batch before flush
  without the browser feeling laggy (100/250ms)? Tie to snapshot cadence. *(`03`,
  `02`)*
- **A6. Config hot-reload / hot-attach** — can `--hub-url`/PAT/allowlist change
  without restarting `sprawl enter` / redeploying the hub, or is attachment fixed
  at process start? *(`02`, `08`)*
- **A7. Multi-instance-in-one-browser fan-out** — N independent one-rule stream
  controllers in the SPA vs. a hub-side multiplexed stream; at what N does client-
  side fan-out stop scaling? *(`02`, `03`, `11`)*

### B. Storage, retention & GC
- **B1. Snapshot cadence numbers** — the *shape* is fixed (hybrid `N`/`T`); the
  values + mobile cold-start SLO need real load-testing. What replay count feels
  instant on a phone? *(`01`, `07`, `09`)*
- **B2. Snapshot body format** — rendered transcript vs. serialized reducer
  state? Affects blob size, replay cost, and whether frontend or hub materializes
  it (needs a `11` round-trip). *(`07`, `09`)*
- **B3. Event body in PG vs. blob** — size threshold above which an event body
  spills to blob (huge tool output), or cap event size upstream? *(`07`)*
- **B4. Blob key scheme & bucket layout** — shared bucket with key prefixes vs.
  separate buckets per data class (events/snapshots/attachments/memory) for
  independent lifecycle rules; residency/region pinning for transcript-bearing
  blobs? *(`07`, `06`, `10`)*
- **B5. GC scheduling & ownership** — cron periodic / on-write incremental /
  watermark-triggered; run by the hub process or an ops subcommand? *(`07`,
  `06`)*
- **B6. `sqlc` vs. hand-written `pgx`** — adopt compile-checked SQL up front or
  only if query code proves error-prone? *(`07`)*
- **B7. Postgres partitioning at scale** — does `event_log_segments` need
  range/hash partitioning before it's large, or defer via migration? *(`07`)*
- **B8. Retention parameter ownership** — infra `.tfvars` (coarse backstops) vs.
  app config (logical retention); where does the authoritative default live so
  the two layers don't contradict? *(`06`, `07`, `09`)*
- **B9. Slow-consumer eviction grace** — how long may one stuck consumer pin the
  retention floor before it's cut to the snapshot path? Needs a concrete window.
  *(`09`)*

### C. Sync, lease & fencing
- **C1. Fence durability across DB failover** — confirm the chosen managed-
  Postgres backup/PITR strategy actually preserves committed per-lease epochs
  across a restore (the one anomaly to rule out). *(`01`, `07`, `09`)* → plan
  picks per-lease epoch; **verification still owed.**
- **C2. Version-vector granularity** — per-project vs. per-`(project,agent)` vs.
  per-`run-id`. Plan uses **per-`run-id` for events, `(project,agent)` for
  memory** *(`01`, `09`, `10`)* — **[resolved in 13]**, confirm with `09`/`10`.
- **C3. Downlink ack cadence** — per-event (chatty), timer-based, or piggybacked
  on heartbeat? Affects GC latency vs. WAN chatter. *(`09`)*
- **C4. Cross-source turn-queue ordering** — when local TUI *and* browser enqueue,
  is strict arrival order sufficient, or is source tagging needed for UX clarity?
  *(`01`, `09`)* → plan ships **no source tag in v1** (add if confusing).
- **C5. VV transfer size at scale** — per-key map vs. a digest/rolling-hash
  compare for hosts with many concurrent `run-id`s. *(`09`)*
- **C6. Local outbound buffer: disk spill?** — v1 is memory-only (host restart ⇒
  new `run-id`+snapshot). Do real outages ever outlast a session enough to
  justify disk-backed buffering? *(`09`)*
- **C7. Buffer high-water policy** — how much local outbound history before drop-
  oldest, per-session or global? *(`01`)*

### D. Memory
- **D1. Memory snapshot cadence** — event-count, time, or on-consolidate?
  Distinct from session-event cadence (B1); affects new-host pull latency.
  *(`10`)*
- **D2. Who runs synthesis/reconcile** — owning agent on next wake (has context,
  may be dormant), a dedicated consolidation agent, or a hub-side job? *(`10`)*
- **D3. `injected` memory authority** — when the browser injects a note, whose
  `host_id`/fence does it carry, and does it need the lease? Ties to D-of-write-
  authority (E-below / A). *(`10`, `01`)*
- **D4. Cross-project / user-level memory** — a `(user, *)` global stream for
  preferences vs. per-project only (YAGNI-flagged). *(`10`)*
- **D5. De-dup key stability** — is `(host_id, run_id, created_at)` sufficient to
  de-duplicate replayed units, or do units need a content hash / stable `unit-id`
  to survive re-uploads after a buffer flush? *(`07`, `10`)*

### E. Auth, security & privacy
- **E1. Does the browser ever need write authority itself**, or does it always
  act "as the host" it's attached to (host holds lease, browser just feeds the
  turn-queue)? *(`01`, `10`, `security-privacy`)* → plan assumes **browser-as-
  host** for v1; north-star may force capability tokens.
- **E2. PAT hashing cost** — argon2id vs. bcrypt given per-uplink verification;
  or cache a verified `(tokenid→host_id)` for a short TTL to avoid hashing every
  call? *(`04`)*
- **E3. Session store backing** — stateless signed cookie (unrevokable pre-
  expiry) vs. server-side session table (revocable, +1 read/request). *(`04`)*
- **E4. Allowlist source of truth & management** — DB table (live-editable) vs.
  deploy config (immutable); static / bootstrap-admin / self-service; and how
  removal interacts with data the removed user owns. *(`04`, `security-privacy`)*
- **E5. PAT↔host_id binding** — bind on first use (flexible, leak-before-use
  risk) vs. name the host at create time (stricter attribution). *(`04`)*
- **E6. PAT scope granularity** — is owner+project binding enough, or will the
  north-star (user-addressable sub-agents, browser write authority) force
  capability-scoped tokens without foreclosing the token shape? *(`04`,
  `security-privacy`)*
- **E7. OIDC logout federation** — RP-initiated / back-channel logout vs. local
  hub-session logout only for v1? *(`04`)*
- **E8. Multiple browsers per user** — one shared session vs. independent per-
  device sessions with independent logout. *(`04`)*
- **E9. Content-trust default granularity** — per-project opt-out (v1) vs. also a
  hub-wide "never store bodies" posture for a blanket-privacy user who won't
  self-host. *(`security-privacy`)*
- **E10. Encryption-seam key model (future ZK)** — per-`(project,agent)` / per-
  project / per-user master key; where keys live, cross-device sync, and the
  *recovery* story (lose key = lose memory). *(`security-privacy`)*
- **E11. Metadata leakage under ZK** — PG still holds provenance (agent names,
  timestamps, `host_id`, counts, sizes); is that itself sensitive enough to need
  obfuscation for a true ZK claim? *(`security-privacy`)*
- **E12. Transcript vs. distilled-unit redaction** — do we need a redaction/
  scrubbing pass on ingest, or a stricter retention window for transcripts
  specifically (they carry raw secrets/PII far more than curated units)? *(`10`,
  `security-privacy`, `attachments-multimodal`)*
- **E13. At-rest encryption of the event log in PG** — should event bodies ride
  the encryption seam too, or is the log ephemeral/snapshot-compacted enough to
  leave provider-encrypted only? *(`security-privacy`)*
- **E14. Browser-side content exposure** — shared/kiosk-device threat worth a
  client-side session timeout / no-persistence mode, even with a self-hosted or
  ZK hub? *(`security-privacy`)*

### F. Attachments / multimodal
- **F1. Files API vs. inline base64 for multi-turn** — base64 re-sends full bytes
  on every history replay; switch to `file_id` (host uploads to Anthropic Files
  API, beta header, tracks ids) if screenshot-heavy sessions get expensive —
  worth it or premature? *(`attachments-multimodal`)* → plan **defers** Files
  API.
- **F2. Blob fetch channel** — host pulls attachment bytes over the persistent
  conn vs. a separate authenticated HTTPS GET (simpler, but a second auth
  surface). *(`attachments-multimodal`)*
- **F3. Where to enforce size/format** — browser (fast, spoofable) / hub (single
  choke point) / host (authoritative, wasted upload on reject); lean host-
  authoritative + browser-advisory. *(`attachments-multimodal`)*
- **F4. `--replay-user-messages` echo of image turns** — confirm the CLI echoes
  an image-bearing user message intact (uuid preserved) so the consumption-ack
  contract holds for multimodal turns (text verified; image to be smoke-tested).
  *(`attachments-multimodal`)*
- **F5. Turn-queue ordering with attachments** — if a text turn and an image turn
  enqueue near-simultaneously from different sources, is strict arrival order
  right, or should an attachment "stick" to its accompanying text? *(`attachments-
  multimodal`, overlaps C4)*
- **F6. Max practical inline size on stdin** — confirm the combined stream-json
  line (base64 + ~1.37× JSON overhead) stays under the host's 10 MB stdin write
  limit. *(`attachments-multimodal`)*

### G. Deployment, IaC & observability
- **G1. `buf breaking` baseline** — track `main` HEAD vs. last-released hub tag so
  in-flight `main` churn doesn't block PRs (and where the baseline image lives).
  *(`03`, `08`, `12`)*
- **G2. Embed toggle mechanism** — build tag vs. runtime flag vs. both; one image
  that runs either way, or two images (embedded / API-only)? *(`08`, `11`)*
- **G3. Graceful-drain grace period** — fixed (10–30s) vs. tied to the platform's
  termination grace period. *(`08`)*
- **G4. Rollout on the target platform** — does it support true rolling deploys
  with readiness gating out of the box, or does TF need explicit revision/traffic-
  split config? *(`08`, `06`)*
- **G5. Min-compatible-version floor** — explicit declared floor per deployable
  vs. lean on `buf breaking` + soft-warn, never hard-gate in v1. *(`08`)*
- **G6. Single-machine co-located deploy** — a blessed "hub + one host on one box"
  convenience deploy; does it change packaging (compose file) or is it just
  config? *(`00`, `08`)*
- **G7. Single-instance → multi-instance trigger** — at what point does
  externalizing the in-memory lease/fan-out registry become necessary, and does
  it change the IaC contract (LB, shared cache)? *(`06`, overlaps A4)*
- **G8. Managed identity vs. connection strings** — how uniformly can host→(DB,
  bucket, secrets) rely on managed identity across the first cloud, and does the
  fallback muddy the contract `aws/` must mirror? *(`06`)*
- **G9. AWS parity trigger** — what concrete event justifies building `aws/`
  (second target? AWS contributor?); until then is the README stub + stable
  output-name contract enough to stop `azure/` accreting provider-specific
  assumptions? *(`06`)*
- **G10. TF state backend location** — same cloud/account as the hub, or
  deliberately separated for blast-radius? *(`06`)*
- **G11. Generated secrets in state** — is encrypted remote state + access control
  sufficient, or should *all* sensitive material be issued out-of-band (never
  TF-generated) at the cost of more manual setup? *(`06`)*
- **G12. Per-stream fan-out lag measurement** — observable purely from
  `/debug/state` gauges, or needs a small timing ring per stream? *(`05`)*
- **G13. `/debug/state` at multi-host scale** — one flat snapshot vs. filtering
  (`?project=` / `?run_id=`); when to add. *(`05`)*
- **G14. Host-side introspection surface** — does the host half need its own
  `/debug/state`-shaped surface for e2e, or is `peek` + the incident snapshot
  enough to assert host buffer depth / last-acked seq? *(`05`, `12`)*
- **G15. `trace_id` origin** — generated at the browser edge, host edge, or minted
  by the hub on first contact (affects whether a locally-typed, no-browser turn
  gets a trace id). *(`05`)*
- **G16. Probe cadence vs. lease TTL** — should `/readyz` failure interact with
  the lease/heartbeat model, or stay strictly independent (orchestrator health ≠
  write authority)? *(`05`)*
- **G17. Log volume at `debug`** — per-frame seq tracing could be very chatty on a
  busy multi-host session; sampling vs. level-gating. *(`05`)*
- **G18. Metrics/tracing trigger** — what concrete incident flips the deferred
  `/metrics` (Prometheus-preferred) and OTel-spans seams from dark to on, so we
  don't build "just in case"? *(`05`)*

### H. Testing & local-dev
- **H1. Scripted-stream fixture format & home** — checked-in JSON `RuntimeEvent`
  table, sanitized recorded session, or a small DSL; where fixtures live so Go
  seam tests and the TS `web-contract` row share one source (recorded sessions
  risk public-repo leakage). *(`12`)*
- **H2. testcontainers in CI** — is Docker-in-Docker allowed for the Postgres /
  `hub-fullstack` rows, or do those stay local-only + soak with CI on the in-
  memory arm? `needs_docker` skip story. *(`12`)*
- **H3. Golden-fixture regeneration policy** — a reviewed `make regen-contract-
  goldens` so an intended wire change is one deliberate diff, not silent drift.
  *(`12`)*
- **H4. Frontend test toolchain** — the SPA choice constrains the headless runner
  and whether `connect-es` golden assertions share tooling with render tests.
  *(`11`, `12`)*
- **H5. `/debug/state` in production** — fully compiled out via build tag (safest,
  no prod introspection) vs. auth-gated off-by-default (useful, larger surface).
  *(`12`, `05`, `security-privacy`)*
- **H6. Multi-worktree shared PG vs. testcontainers-per-run** — is schema-per-
  worktree against one local PG worth maintaining alongside testcontainers?
  *(`12`)*
- **H7. compose ↔ Terraform fidelity** — must local `docker-compose` use the same
  images as the TF prod stack, or just the same shapes, to be a trustworthy
  smoke? *(`12`, `06`, `08`)*

### I. Product & scope
- **I1. Smallest useful MVP slice** — read-only pane first vs. full drive-a-
  session round-trip. *(`00`)* → **[resolved in 13]**: read-only single pane
  (Phase 1) first, downlink (Phase 2) next.
- **I2. Is "N clients connected" enough of a guard**, or will accidental double-
  driving cause real confusion in practice? *(`00`, `01`)*
- **I3. Should the hub ever run co-located with a host** (single-machine
  convenience), or always a separate service? *(`00`, overlaps G6)*
- **I4. How much of the north-star org model should the foundation schema
  anticipate now** vs. migrate to later? *(`00`)* → plan schema-keys per-agent
  lease/memory but enforces per-project; broader org model deferred.
- **I5. Do we need per-host *push* of screenshots/attachments in v1**, or is
  browser-side upload sufficient? *(`00`)* → **[resolved in 13]**: browser upload
  + local `/attach`; per-host push deferred.

---

## Open Questions (my own — beyond the consolidated list)

- **Spike platform ≠ eventual platform.** The §2 spike must run on the *actual*
  target managed platform, but the docs keep the platform parameterized ("Azure"
  as a generic stand-in). If the maintainer's real target differs from the spike
  target, the idle-timeout/heartbeat findings may not transfer. Should the plan
  name a concrete spike target (privately), or run the spike on ≥2 platforms to
  bound the answer?
- **Phase-0 "registers an instance" needs a live event to be meaningful.**
  Phase 0 proves auth + dial-out with *no data*, but a registration with nothing
  flowing is hard to verify beyond a row in `/debug/state`. Is a single
  synthetic heartbeat/hello event acceptable in Phase 0 to prove the round-trip,
  or does that blur the Phase-0/1 boundary?
- **Attachment schema change lands before the hub exists.** The
  `MessageParam.Blocks` union + `/attach` (Phase 2, host-local) touches
  `internal/protocol/types.go` and the mandatory `replay-echo` e2e matrix row
  (per `CLAUDE.md`). Should the schema change + `/attach` be pulled *earlier*
  (even into Phase 0/1) as a standalone, hub-independent slice, since it's
  verified-feasible and has zero hub dependency? It may be the cheapest possible
  end-to-end multimodal proof.
- **Lease is per-project but a host is per-`sprawl enter` (per-repo).** The
  mapping of `host_id`/`run_id`/`project` when one machine runs several
  `sprawl enter` sessions on the *same* repo (rare, but possible) isn't spelled
  out — do two runs on one project contend for one lease (expected), and is that
  the desired UX?
- **Cost envelope assumes always-on; is scale-to-zero worth a WS re-think?** The
  ~$35–100/mo floor is dominated by the always-on container needed for long-lived
  streams. If cost mattered more than latency, a poll-based or WS-with-
  reconnect-on-wake model *could* allow scale-to-zero — explicitly *not*
  recommended (kills the live-tail UX), but worth recording as the one lever that
  would move the cost floor.
- **No doc owns end-to-end latency budget.** Individual targets exist (downlink
  <500ms p95, heartbeat 20–30s, uplink batch 100–250ms) but nothing composes them
  into a single "browser keypress → claude sees it → response on screen" budget.
  Worth defining before the spike so (4) has a pass/fail bar.
