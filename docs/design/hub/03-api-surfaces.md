# 03 — API Surfaces

*The Connect/protobuf RPC surfaces, the buf toolchain, and the **top viability
risk**: does a long-lived connection survive typical cloud L7 load balancers?*

See also: [`00-overview.md`](00-overview.md) · [`01-architecture.md`](01-architecture.md) · [index](README.md)

---

## 0. TL;DR — read this first

- **Two Connect surfaces:** (1) **host↔hub** (Go↔Go, private, PAT-authed) and
  (2) **api↔webapp** (browser↔hub, OIDC-authed). Both carry the *same event-log
  spine* ([`01`](01-architecture.md#2-the-event-log-spine-the-strongest-idea--feature-it));
  the reconnect contract ("replay from last seq, else snapshot, then live-tail")
  is written once and reused at both seams.
- **Verdict on the "one persistent bidi stream" assumption: DO NOT BET ON IT.**
  A single long-lived **full-duplex bidi** stream is *technically* possible
  Go↔Go, but it is fragile on managed L7 ingress and **outright impossible from a
  browser** (see §4). Recommend the **symmetric fallback as the *primary* design**:
  **one held-open server-stream (downlink) + unary/batched calls (uplink)** at
  both seams. It is more robust, simpler to reason about, and reuses one reconnect
  pattern everywhere.
- **De-risk with a small spike** (§5) *before* committing any streaming shape.

---

## 1. Surface (1): host ↔ hub

Private, machine-to-machine. Authenticated by a host→hub PAT (hashed in PG, see
[`04`](README.md)). The host **dials out**; no inbound host ports.

### RPCs (conceptual — not a committed `.proto`)

| RPC | Shape | Purpose |
|---|---|---|
| `PushEvents` | unary or client-stream | Uplink: host appends seq'd event-log entries (claude output) to the hub. Carries `{host_id, run_id, fence_token, events[], from_seq}`. |
| `SubscribeCommands` | **server-stream** | Downlink: hub pushes commands to the host ("user typed X", "reclaim requested", "snapshot now"). Held open; this is the connection that must survive the LB. |
| `ClaimLease` / `RenewLease` / `ReleaseLease` | unary | Write-authority: claim/renew/release the per-project write-lease; returns `fence_token`. Renew doubles as an explicit heartbeat when the stream is quiet. |
| `SyncMemory` | unary or client-stream | Push per-`(project, agent)` memory units (provenance-tagged) up; pull deltas down. No textual merge ([`10`](README.md)). |
| `SyncSession` | unary | Reconcile session metadata / version-vectors on (re)connect ([`09`](README.md)). |
| `UploadAttachment` | client-stream **or** presigned-URL unary | Screenshot/image ingestion. Prefer a unary call that returns a `gocloud.dev/blob` presigned URL the host PUTs to directly — keeps large blobs off the RPC path (attachments doc). |

> **The persistent connection = the heartbeat = the lease liveness signal**
> ([`01` §4](01-architecture.md#4-identity-lease--fencing-conceptual--detail-in-doc-10--09)).
> Whichever streaming shape wins, an application-level heartbeat rides it (see
> §4.3) — a dropped heartbeat is what expires a stale lease.

### Simplest way vs. right way (host↔hub transport)

- **Simplest:** one `Chat`-style **bidi stream** carrying uplink events *and*
  downlink commands multiplexed. Cost: relies on full-duplex HTTP/2 surviving the
  LB indefinitely; Connect itself warns against long-lived bidi (§4.1); one flaky
  frame kills both directions at once.
- **Right:** **split the directions** — a held-open **server-stream** for
  downlink + **unary/batched** `PushEvents` for uplink. Cost: two call types
  instead of one; uplink events are batched rather than instantaneously
  streamed (sub-second batching is fine for this workload).
- **Recommendation:** **split directions (server-stream downlink + unary
  uplink).** It sidesteps full-duplex fragility, matches what the browser seam is
  *forced* to do anyway (§2), and lets the *one* reconnect rule cover both seams
  identically. Keep the door open to upgrade the downlink to bidi later purely as
  an optimization — the wire types don't change, only who initiates frames.

## 2. Surface (2): api ↔ webapp

Public, browser-facing. Authenticated via OIDC (session cookie / bearer) — see
[`04`](README.md). The SPA is *just another event-log consumer*.

### RPCs (conceptual)

| RPC | Shape | Purpose |
|---|---|---|
| `Login` / `Logout` / `Session` | unary | OIDC relying-party flow bootstrap; returns current user + allowlist status. |
| `ListInstances` | unary | Enumerate connected hosts/instances (`host_id`, repo label, lease holder, N-clients-connected, last-seen). |
| `SubscribeInstance` | **server-stream** | Uplink→browser: live-tail an instance's event log. Carries `from_seq`; server replays delta or hands a snapshot ref, then live-tails. |
| `SubmitInput` | unary | Downlink: "user typed X in the browser" → hub → host's `SubscribeCommands` stream → the one turn-queue ([`01` §6](01-architecture.md#6-how-the-pieces-fit-requestresponse-paths)). |
| `FetchSnapshot` | unary | Cold-start / gap-recovery: fetch the latest snapshot (or a blob ref) for an instance, returns its `seq`. |
| `CreatePAT` / `ListPATs` / `RevokePAT` | unary | Host→hub PAT lifecycle (management UI). Tokens shown once; stored hashed. |

### Why the browser seam settles the debate

Browsers **cannot** do client-streaming or full-duplex bidi over `fetch`: true
bidi needs `duplex: 'full'`, which as of early 2026 is **not shipped in any
stable browser** (WHATWG proposal; behind a Chromium flag only). Connect's own
web transport therefore **does not support bidi or client streaming** in the
browser. So the browser seam is *architecturally forced* into
**server-stream (events) + unary (input)** — there is no bidi option to lose.
Making the host seam match it (§1) means **one shape, one reconnect rule, two
seams.** That symmetry is the KISS win.

## 3. buf toolchain

- **Codegen:** `buf generate` produces **Go** (`connect-go`) for hub + host and
  **TypeScript** (`connect-es`) for the SPA from one `.proto` source of truth.
  A single `buf.gen.yaml` drives both; no hand-written wire types.
- **Wire back-compat:** `buf breaking` runs in CI against the `main` baseline so
  a field renumber / removal that would break a deployed host or an old browser
  tab fails the build. This matters because **hosts and browsers update
  independently of the hub** — an old host must keep talking to a new hub.
- **Lint/format:** `buf lint` + `buf format` keep the schema consistent; wire
  into `make validate` alongside the Go checks (this repo has no markdown/JS
  linters today — Go-only validation).

### Simplest way vs. right way (schema evolution)

- **Simplest:** no `buf breaking`; just be careful. Cost: a silent wire break
  ships and a mobile browser tab or an un-updated host dies cryptically.
- **Right:** `buf breaking` gate in CI + additive-only field policy. Cost: one CI
  step + discipline (never reuse field numbers).
- **Recommendation:** **`buf breaking` in CI from day one.** The whole point of
  protobuf here is safe independent evolution of three deployables; skipping the
  gate throws that away for near-zero savings.

---

## 4. TOP VIABILITY RISK — long-lived connections through cloud L7 LBs

> This is the design's **top viability risk** ([`01` Open Questions](01-architecture.md#open-questions)).
> The "broker" only works if the host's downlink connection survives.

### 4.1 Does a long-lived Connect bidi-stream survive managed L7 ingress?

**Short answer: unreliably, and the platform fights you.** Findings:

- **Streaming requires end-to-end HTTP/2.** *Every* proxy on the path —
  including cloud-provider ones — must speak HTTP/2. NGINX-class ingress can't
  proxy streaming Connect at all; Envoy / HAProxy(TCP) / Apache can. Managed
  container platforms front you with Envoy (good) but you don't fully control it.
- **Connect explicitly discourages long-lived streams.** Its docs advise keeping
  streams **short-lived**, warn that long-lived streams "are more likely to
  encounter bugs and edge cases in HTTP/2 flow control," and recommend
  server-streaming clients "set short deadlines and repeat the call when the
  deadline is exceeded." The framework's own guidance is *against* the
  one-forever-stream assumption.
- **Browsers can't bidi at all** (§2) — so a bidi design can never be uniform.

### 4.2 Concrete platform idle-timeout ceilings (Azure as representative target)

| Platform | Idle/request ceiling | Tunable? | Implication |
|---|---|---|---|
| **Container Apps** (Envoy ingress) | **240s** request/route timeout by default | Partly — "Premium Ingress" raises the request idle timeout up to **~1 hour**; request-idle-timeout also settable via CLI on the environment | A quiet stream is torn down at the ceiling unless you keep traffic flowing **and** raise the timeout. |
| **App Service** | **~230s** idle timeout at the Azure **hardware load balancer** (TCP level) | **No — not configurable.** | Any TCP connection idle >230s is killed. Must keep bytes flowing. WebSockets are the sanctioned long-lived path here. |
| **Generic managed ingress** (kube ingress-nginx etc.) | Commonly **60s** idle on gRPC streams, and known to close idle streams **even with HTTP/2 keepalive annotations set** | Varies | Assume a low default idle timeout everywhere; don't rely on defaults. |

Takeaway: **plan for an idle-timeout ceiling in the 60–240s range on any managed
platform, some of it non-negotiable.** A connection that is idle longer than the
ceiling *will* be cut. Survival = never being idle that long + reconnecting
cleanly when cut anyway.

### 4.3 HTTP/2 keepalive / PING tuning — necessary but **not sufficient**

- HTTP/2 has a mandatory **PING** frame; gRPC-style keepalive sends PINGs on an
  interval (`PermitWithoutStream=true` to ping with no active RPC). PING frames
  put bytes on the wire, which resets **TCP-level** idle timers (e.g. App
  Service's hardware LB).
- **The trap:** HTTP/2 PING is a **connection-level** frame. **Envoy's
  `stream_idle_timeout` is per-*stream*** and is **not** reset by connection-level
  PINGs — a documented Envoy behavior: idle gRPC streams get closed ~2 min even
  with keepalive PINGs enabled, because no DATA frames flowed *on the stream*.
  The fix upstream is `stream_idle_timeout: 0` on streaming routes — but on
  **managed** ingress you often can't set that.
- **Another trap:** ping too aggressively and the server sends `GOAWAY`
  `too_many_pings`. Client and server keepalive settings must agree.

**Consequence:** to survive managed Envoy you need **application-level heartbeat
messages *on the stream itself*** (DATA frames — e.g. a periodic `Heartbeat`
event / `RenewLease` well under the idle ceiling, say every 20–30s), **not just
HTTP/2 PINGs.** This heartbeat doubles as the lease-liveness signal (§1).

### 4.4 Proxy/ingress buffering of streams

Some proxies **buffer** response bodies, which defeats streaming (the browser/host
sees nothing until the buffer flushes or the request ends). Envoy streams by
default; NGINX buffers unless `proxy_buffering off` + HTTP/2. The spike (§5) must
verify **first-byte and per-event latency**, not just "the connection stays open"
— a buffered stream looks connected but delivers events in useless batches.

### 4.5 Recommended design (robust regardless of the above)

```
DOWNLINK (hub → host, hub → browser):  held-open SERVER-STREAM
  - app-level heartbeat event every ~20-30s (beats the 60-240s ceilings)
  - carries seq'd events; on cut, client reconnects with from_seq (the ONE rule)

UPLINK (host → hub, browser → hub):    UNARY / batched
  - PushEvents (host) and SubmitInput (browser) are short unary calls
  - no long-lived upstream to keep alive; each call is LB-friendly HTTP/2 or /1.1

RECONNECT:  identical at both seams — replay(from_seq) else snapshot, then tail
```

- This **never depends on full-duplex** and **never depends on an un-cuttable
  long stream.** Cuts are *expected*; the seq'd log + snapshot make reconnect
  cheap and correct. The persistent connection is "persistent" in the sense of
  *continuously re-established*, not *never dropped*.
- **WebSocket fallback (kept in reserve, not v1):** if even a heartbeated
  server-stream proves flaky on a target platform (or a platform only blesses WS
  for long-lived conns, as App Service does), a WebSocket transport carries the
  same seq'd frames with the same reconnect rule. Connect has no first-class
  browser WebSocket transport today, so this is a **custom framing** cost — only
  pay it if the spike says the server-stream path fails. Do **not** build it
  speculatively (YAGNI).

### Simplest way vs. right way (the persistent-connection assumption)

- **Simplest:** assume one bidi stream stays up forever; add reconnect only when
  it breaks in prod. Cost: breaks in prod, on mobile, at the worst time; browser
  can't do it anyway.
- **Right:** design for **frequent expected disconnects** from day one —
  heartbeated server-stream downlink + unary uplink + seq-replay reconnect, WS
  held in reserve. Cost: batched (not instantaneous) uplink; a heartbeat timer.
- **Recommendation:** **the right way**, because the reconnect machinery already
  exists locally (the seq'd eventbus, QUM-775) and the *one rule* makes each seam
  "just another consumer." We're not building new resilience — we're extending
  resilience sprawl already has.

## 5. Recommended de-risking spike (run BEFORE committing streaming shape)

A ~1–2 day spike against the **actual target platform** (a real managed container
host, not just localhost), measuring:

1. **Idle survival:** open a server-stream, send *nothing*, time until the LB
   cuts it. Confirms the platform ceiling (expect 60–240s).
2. **Heartbeat efficacy:** repeat with an app-level heartbeat event every
   20/30/60s. Find the **max heartbeat interval that keeps the stream alive
   indefinitely** (≥30 min). Distinguish HTTP/2 PING vs. on-stream DATA — verify
   whether PING alone suffices or DATA is required (the Envoy `stream_idle_timeout`
   trap, §4.3).
3. **Buffering / latency:** measure **first-byte** and **per-event** delivery
   latency; confirm events arrive individually, not buffered into batches (§4.4).
4. **Downlink round-trip:** time `SubmitInput` (unary) → host receives on its
   downlink stream → ack. Must feel interactive (target < ~500ms p95).
5. **Reconnect correctness & frequency:** kill the network / roam mobile↔wifi;
   confirm `from_seq` replay resumes with **zero gaps / zero dupes**, and log how
   *often* real mobile/NAT conditions force a reconnect over a multi-hour session.
6. **Config levers:** confirm whether Premium Ingress / CLI idle-timeout actually
   extends survival, and whether it's needed given a working heartbeat.

**Decision gate:** if (2)+(3)+(5) pass with a heartbeated **server-stream**, ship
that — bidi is an unneeded optimization and WebSocket stays in reserve. If the
server-stream can't be kept alive or is buffered, escalate to the **WebSocket
transport** and re-run the spike. **Do not commit the "one persistent connection"
assumption until this spike passes.**

---

## Open Questions

- **Heartbeat interval:** what's the safe max across target platforms — a fixed
  conservative 20s, or negotiated per-connection from a hub-advertised ceiling?
- **PING vs. on-stream DATA:** does the target managed ingress reset its
  stream-idle timer on HTTP/2 PING, or is an on-stream heartbeat event mandatory?
  (The Envoy `stream_idle_timeout` behavior says DATA; must confirm on the
  managed variant we can't directly configure.)
- **Uplink batching window:** how long may `PushEvents` batch before flush
  without the browser feeling laggy — 100ms? 250ms? Tie to the snapshot cadence
  question in [`01`](01-architecture.md#open-questions).
- **Attachment path:** presigned-URL PUT (blob store direct) vs. client-stream
  through the RPC — settle in the attachments doc; affects whether
  `UploadAttachment` is unary or a stream.
- **WebSocket fallback trigger:** what concrete spike metric flips us from
  server-stream to WS — an idle-survival failure, a p95 reconnect rate above some
  threshold, or platform policy (App-Service-class WS-only)?
- **Downlink fan-out to N hosts from one hub process:** how many concurrent
  held-open server-streams can a single Go container hold before memory/goroutine
  pressure matters? (Sizing → [`08`](README.md).)
- **`buf breaking` baseline:** track against `main` HEAD, or against the
  last-released hub tag so in-flight `main` churn doesn't block PRs?
