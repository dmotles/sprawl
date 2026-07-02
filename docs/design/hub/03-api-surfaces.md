# 03 — API Surfaces

*The Connect/protobuf RPC surfaces, the buf toolchain, and the **top viability
risk**: does a long-lived connection survive typical cloud L7 load balancers?*

See also: [`00-overview.md`](00-overview.md) · [`01-architecture.md`](01-architecture.md) · [index](README.md)

> **MVP scope (v2 — single-user cloud companion).** The hub is a cloud
> *companion* to the local binary: (1) relay a live activity stream to a browser
> for view + input, and (2) durably persist memory + transcripts + attachments.
> **Not multi-tenant.** Auth is a single configured bearer token — **no OIDC**.
> Memory sync is checkpoint push/pull with **last-writer-wins** (no
> version-vector / fence / semantic reconcile; provenance metadata is retained).
> Write-authority is a **trivial advisory active-host marker** (no fence tokens).
> **No snapshots in v1** and **no GC/retention** — the durable seq'd transcript
> *is* the log; fresh connect replays the full log, reconnect replays the delta.

---

## 0. TL;DR — read this first

- **Two Connect surfaces:** (1) **host↔hub** (Go↔Go, private, bearer-token) and
  (2) **api↔webapp** (browser↔hub, bearer-token → httpOnly cookie). Both carry
  the *same event-log spine*
  ([`01`](01-architecture.md#2-the-event-log-spine-the-strongest-idea--feature-it));
  the reconnect contract ("replay from last seq, else full log, then live-tail")
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

Private, machine-to-machine. Authenticated by the single configured **bearer
token** in an `Authorization` header (same secret the browser uses; see
[`04`](README.md)). The host **dials out**; no inbound host ports. Every request
carries the (always-single) `user_id` for schema-forward compatibility — there is
**no multi-tenant enforcement** in v1.

### RPCs (conceptual — not a committed `.proto`)

| RPC | Shape | Purpose |
|---|---|---|
| `RegisterInstance` | unary | Host announces itself: `{host_id, run_id, repo_label, user_id}` → hub records/updates the instance row. Idempotent; called on connect. |
| `AppendTranscript` | unary or client-stream | Uplink: host appends seq'd transcript/event entries (claude output) to the durable log. Carries `{host_id, run_id, entries[], from_seq}`. **The transcript IS the event log** ([`01`](01-architecture.md#2-the-event-log-spine-the-strongest-idea--feature-it)). |
| `SubscribeCommands` | **server-stream** | Downlink: hub pushes commands to the host ("user typed X", "release requested"). Held open; this is the connection that must survive the LB. |
| `ClaimActiveHost` / `ReleaseActiveHost` | unary | **Advisory** write-authority marker for a project: sets/clears "the currently active host." **No fence token, no epoch** — best-effort, last-writer-wins; the marker is informational (drives the "which host is live / N clients" UX), not a hard lock. |
| `PushMemory` / `PullMemory` | unary | Memory **checkpoint** sync: push a per-`(project, agent)` memory checkpoint up (provenance-tagged), pull the latest down. **Last-writer-wins** — no version-vector, no textual/semantic merge ([`10`](README.md)). |
| `UploadAttachment` | client-stream **or** presigned-URL unary | Screenshot/image ingestion. Prefer a unary call that returns a `gocloud.dev/blob` presigned URL the host PUTs to directly — keeps large blobs off the RPC path (attachments doc). |

> **The persistent connection = the heartbeat.** An application-level heartbeat
> rides the downlink stream (see §4.3); it keeps the stream alive through idle
> timeouts and refreshes the advisory active-host marker. A dropped heartbeat
> simply lets the next host claim the advisory marker — there is no fence to
> reconcile.

### Simplest way vs. right way (host↔hub transport)

- **Simplest:** one `Chat`-style **bidi stream** carrying uplink transcript *and*
  downlink commands multiplexed. Cost: relies on full-duplex HTTP/2 surviving the
  LB indefinitely; Connect itself warns against long-lived bidi (§4.1); one flaky
  frame kills both directions at once.
- **Right:** **split the directions** — a held-open **server-stream** for
  downlink + **unary/batched** `AppendTranscript` for uplink. Cost: two call
  types instead of one; uplink is batched rather than instantaneously streamed
  (sub-second batching is fine for this workload).
- **Recommendation:** **split directions (server-stream downlink + unary
  uplink).** It sidesteps full-duplex fragility, matches what the browser seam is
  *forced* to do anyway (§2), and lets the *one* reconnect rule cover both seams
  identically. Keep the door open to upgrade the downlink to bidi later purely as
  an optimization — the wire types don't change, only who initiates frames.

## 2. Surface (2): api ↔ webapp

Public, browser-facing. **No OIDC.** Auth = the single configured **bearer
token**: the SPA posts it once to `Login`, which sets an **httpOnly session
cookie**; subsequent calls carry the cookie. This is the entire auth surface —
**no relying-party flow, no user allowlist, no multi-tenant RPCs.** The SPA is
*just another event-log consumer*.

### RPCs (conceptual)

| RPC | Shape | Purpose |
|---|---|---|
| `Login` / `Logout` | unary | Exchange the configured bearer token for an httpOnly session cookie (`Login`); clear it (`Logout`). No IdP, no allowlist. |
| `ListInstances` | unary | Enumerate connected instances (`host_id`, repo label, active-host marker, N-clients-connected, last-seen). |
| `SubscribeInstance` | **server-stream** | Uplink→browser: live-tail an instance's transcript/event log. Carries `from_seq`; server replays the delta (or the full log on a fresh connect), then live-tails. |
| `SubmitInput` | unary | Downlink: "user typed X in the browser" → hub → host's `SubscribeCommands` stream → the one turn-queue ([`01` §6](01-architecture.md#6-how-the-pieces-fit-requestresponse-paths)). |
| `FetchTranscript` | unary | Cold-start / gap-recovery: fetch the transcript log — **full** (`from_seq=0`) or **delta** (`from_seq=N`) — returns entries + head `seq`. **No snapshot in v1**; the log itself is the source. |

> **No `FetchSnapshot`, no PAT-management, no OIDC endpoints in v1.** Snapshots,
> retention/GC, and richer auth are deferred (see the scope banner and Open
> Questions). Client-side encryption and per-project opt-out are likewise
> deferred.

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
  protobuf here is safe independent evolution of the deployables; skipping the
  gate throws that away for near-zero savings. It's also what keeps the deferred
  v2 features (snapshots, richer auth) **additive** rather than breaking.

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
| **App Service** | **~230s** idle timeout at the hardware **load balancer** (TCP level) | **No — not configurable.** | Any TCP connection idle >230s is killed. Must keep bytes flowing. WebSockets are the sanctioned long-lived path here. |
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
event well under the idle ceiling, say every 20–30s), **not just HTTP/2 PINGs.**
This heartbeat doubles as the advisory active-host refresh (§1).

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
  - carries seq'd log entries; on cut, client reconnects with from_seq (the ONE rule)

UPLINK (host → hub, browser → hub):    UNARY / batched
  - AppendTranscript (host) and SubmitInput (browser) are short unary calls
  - no long-lived upstream to keep alive; each call is LB-friendly HTTP/2 or /1.1

RECONNECT:  identical at both seams — replay(from_seq); on fresh connect from_seq=0
            (full log — no snapshot in v1), then tail live
```

- This **never depends on full-duplex** and **never depends on an un-cuttable
  long stream.** Cuts are *expected*; the seq'd log makes reconnect cheap and
  correct. The persistent connection is "persistent" in the sense of
  *continuously re-established*, not *never dropped*.
- **No snapshots in v1** (scope banner): a fresh connect replays the full seq'd
  transcript. If cold-start replay ever gets too heavy, snapshots are the
  additive v2 optimization — the reconnect rule and wire types don't change
  (`FetchTranscript` gains a snapshot ref; `from_seq` semantics stay identical).
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
- **Full-log cold-start cost:** with **no snapshots in v1**, how large does a
  single-project transcript get before full-log replay on fresh connect feels
  slow on mobile — and is that the trigger to add v2 snapshots?
- **Uplink batching window:** how long may `AppendTranscript` batch before flush
  without the browser feeling laggy — 100ms? 250ms?
- **Attachment path:** presigned-URL PUT (blob store direct) vs. client-stream
  through the RPC — settle in the attachments doc; affects whether
  `UploadAttachment` is unary or a stream.
- **Advisory-marker staleness:** with no fence token, how long after a heartbeat
  gap should the active-host marker be considered stale enough for another host
  to claim it — and is a purely advisory marker enough to avoid confusing
  double-driving in a single-user setup?
- **Bearer-token rotation:** the single configured token is the whole auth
  surface — how is it rotated without dropping every host + browser at once?
  (Deferred detail → [`04`](README.md).)
- **WebSocket fallback trigger:** what concrete spike metric flips us from
  server-stream to WS — an idle-survival failure, a p95 reconnect rate above some
  threshold, or platform policy (App-Service-class WS-only)?
- **`buf breaking` baseline:** track against `main` HEAD, or against the
  last-released hub tag so in-flight `main` churn doesn't block PRs?
