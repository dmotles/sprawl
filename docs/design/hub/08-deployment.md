# 08 — Deployment

*How the hub ships and runs: one Go container, an embedded (or split) frontend,
resync-tolerant redeploys, health/readiness, config/secrets injection, and
version-skew management.*

See also: [`00-overview.md`](00-overview.md) · [`01-architecture.md`](01-architecture.md) · [`03-api-surfaces.md`](03-api-surfaces.md) · [index](README.md)

---

## 0. TL;DR — read this first

- **One artifact.** The hub is a **single Go container**: Connect API +
  optionally-`go:embed`'d frontend SPA. Deployable on any container cloud
  (generic target; "Azure" only as a representative public cloud).
- **Start embedded.** Ship the SPA *inside* the binary for v1. Splitting the
  frontend onto a CDN later is a **deploy change, not a code change** — gated by
  a build/config toggle, not a rewrite ([§2](#2-frontend-embed-vs-split)).
- **Redeploys are blips, not outages.** Because the whole design assumes
  frequent expected disconnects and cheap seq-replay reconnect
  ([`03` §4.5](03-api-surfaces.md#45-recommended-design-robust-regardless-of-the-above)),
  a hub restart is just another reconnect. This is *why* we can embed the
  frontend now without fearing frontend-triggered redeploys ([§3](#3-resync-tolerant-redeploys)).
- **Config has no defaults that leak.** Endpoints, IdP, DB, secrets are all
  injected at deploy; **no hardcoded hub endpoint** ships in the code (public-repo
  hygiene) ([§5](#5-configsecrets-injection-at-deploy)).
- **Three deployables evolve independently** (host, hub, browser). `buf breaking`
  is the wire-compat gate that makes skew safe ([§6](#6-rollout--version-skew)).

---

## 1. The single-container model

```
┌──────────────── hub container (one Go binary) ────────────────┐
│  process: ./hubd                                              │
│   ├─ Connect/protobuf listener  (host↔hub + api↔webapp)       │
│   ├─ go:embed'd SPA assets      (served on the same listener) │
│   ├─ /healthz  /readyz          (health & readiness)          │
│   └─ graceful-shutdown handler  (SIGTERM → drain → exit)      │
│  reads at boot: config + secrets (injected, no defaults)      │
└───────────────────────────────────────────────────────────────┘
        │ Postgres (managed)     │ blob store (gocloud)   │ secrets (gocloud)
        ▼                        ▼                        ▼
   external, addressed purely by injected config — nothing baked in
```

- **Stateless-ish app tier.** All durable state lives in Postgres + the blob
  store ([`01` §7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs),
  [`02` §1.6–1.7](02-components.md)). The container itself holds only in-flight
  connections and buffers, so it is **freely restartable / horizontally
  replaceable** — the property that makes rolling redeploys safe ([§3](#3-resync-tolerant-redeploys)).
- **One listener, two surfaces + assets.** The Connect listener serves both RPC
  surfaces ([`03`](03-api-surfaces.md)) *and* the embedded SPA bytes
  ([`02` §1.8](02-components.md#18-optionally-embedded-spa)). No second web
  server, no sidecar.
- **Any container cloud.** The artifact is a plain OCI image; it runs on managed
  container platforms, k8s, or a VM with a container runtime. Portability across
  clouds comes from `gocloud.dev` (blob/secrets) + parameterized Terraform
  ([`06`](README.md)), *not* from provider SDKs in app code.

### Simplest way vs. right way (packaging)

- **Simplest:** one Go binary, one `Dockerfile`, one image. Everything (API +
  SPA) in the process. Cost: the frontend and API redeploy together; a pure
  frontend change forces a hub restart.
- **Right (at scale):** API-only hub image + separately-hosted SPA (CDN);
  independent release cadences. Cost: two artifacts, two pipelines, an
  asset-versioning story, CORS/cache config.
- **Recommendation:** **single container for v1.** One artifact trivially keeps
  API and asset versions in lockstep, and (per [§3](#3-resync-tolerant-redeploys))
  frontend-triggered redeploys are cheap because clients resync. Keep the SPA
  served behind a clean, CDN-friendly path so splitting later is a deploy change
  ([§2](#2-frontend-embed-vs-split)). KISS wins here decisively.

---

## 2. Frontend: embed vs. split

The hub can serve the SPA two ways. This is a **toggle**, chosen at
build/deploy time, *not* a fork in the code.

```
EMBEDDED (v1 default)              SPLIT (later, if needed)
  build SPA → dist/                  build SPA → dist/ → upload to CDN/static host
  go:embed dist/ into ./hubd         ./hubd built with embed OFF (serves API only)
  one image serves API + assets      browser loads SPA from CDN, calls hub API
```

- **Toggle mechanism (recommended):** a **Go build tag** (e.g. `embedspa`)
  selects between an `embed.FS`-backed asset handler and a no-op handler that
  returns 404/redirect for asset paths. A build without the tag is API-only. A
  runtime config flag (`serve_embedded_spa: true|false`) can *additionally* gate
  serving even in an embed build, so one image can run either way. Prefer the
  build tag as the primary lever (keeps API-only images small), with the runtime
  flag as the fast override.
- **Why the split stays cheap:** the SPA is *already* "just another event-log
  consumer" ([`03` §2](03-api-surfaces.md#2-surface-2-api--webapp)) speaking the
  public API. Moving where its bytes are served changes **nothing** about the API
  contract — the browser still hits the same Connect endpoints. So the split is a
  deploy/topology change, confined to §1's asset handler + an ingress/CDN rule.
- **Contract to preserve now (so the split stays a deploy change):**
  1. Serve assets under a clean, cache-friendly prefix distinct from the API path
     (so a CDN can front assets without touching RPC routes).
  2. No server-side rendering, no server-injected state — pure static assets
     ([`01` §7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs)).
  3. The SPA discovers its API base via config it fetches/derives, never a
     compiled-in absolute hub URL (mirrors the "no default endpoint" rule).

### Simplest way vs. right way (embed toggle)

- **Simplest:** always embed, no toggle. Cost: can't ever serve API-only without
  a code edit; the split becomes a code change, contradicting the goal.
- **Right:** clean build-tag/config toggle from day one. Cost: a build tag + a
  no-op asset handler + one CI matrix entry to prove the API-only build compiles.
- **Recommendation:** **build-tag toggle, defaulting to embedded.** The cost is
  tiny and it's the concrete thing that makes "split later" a deploy change. Ship
  embedded; keep the API-only build green in CI so it never bit-rots.

---

## 3. Resync-tolerant redeploys

**Claim:** a hub redeploy is a **brief blip clients recover from**, not an
outage — and that is precisely what lets us embed the frontend now without
fearing frontend-triggered redeploys.

### Why redeploys are safe

The design already assumes **frequent, expected disconnects** and makes
reconnect cheap and correct ([`03` §4.5](03-api-surfaces.md#45-recommended-design-robust-regardless-of-the-above)).
A redeploy is just a disconnect the operator caused:

- **No long-lived bidi to lose.** Per [`03`](03-api-surfaces.md#0-tldr--read-this-first),
  the connection model is a **heartbeated server-stream downlink + unary/batched
  uplink**, *not* one long-lived full-duplex stream. On redeploy:
  - the **downlink server-stream** to each host/browser is cut (old container
    exits) → each client reconnects and resumes via the **one rule**
    ([`01` §2](01-architecture.md#the-one-rule-written-once-reused-at-every-seam)):
    *replay from last seq, else snapshot, then live-tail.*
  - **uplink** calls (`PushEvents`, `SubmitInput`) are short unary calls — an
    in-flight one may fail and is simply **retried** against the new container.
    No long upstream to keep alive.
- **State survives the restart.** The event log, snapshots, lease/fence registry,
  and memory all live in Postgres + blob ([`01` §7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs)),
  not in the container. The new container reads the same store and answers
  `from_seq` replay / snapshot fetch exactly as the old one did.
- **Host keeps running regardless.** A hub outage *never* stalls a turn
  ([`01` §3](01-architecture.md#3-connected-vs-disconnected)); the host buffers
  un-acked uplink events (bounded, drop-oldest past high-water) and flushes on
  reconnect. So even a slow or failed rollout degrades to "disconnected default,"
  never to a broken host.

```
redeploy timeline (per client):
  ─ live-tail ─┤ container SIGTERM/exit ├─ reconnect ─ replay(from_seq) ─ live-tail ─
               ↑ downlink cut               ↑ hits new container, same Postgres/blob
        (host buffers uplink meanwhile; unary retries land on new container)
```

### Graceful shutdown (make the blip as brief as possible)

On `SIGTERM` the container should:

1. **Fail readiness** (`/readyz` → not-ready) so the platform stops routing *new*
   connections to it while draining.
2. **Close held-open downlink streams politely** (send a final heartbeat / close
   frame) so clients reconnect promptly instead of waiting for a timeout.
3. **Drain in-flight unary calls** for a bounded grace period, then exit.
4. Rely on the platform to spin the new revision up first (rolling, not
   stop-then-start) so there's always a container to reconnect to.

### The frontend-redeploy point, explicitly

Because the above holds, **embedding the frontend does not create a scary
redeploy class.** A frontend-only change rebuilds the one image and rolls it;
connected clients experience the same brief resync as any hub restart, and pick
up the new SPA on their next page load. We do **not** need to split the frontend
to protect uptime — resync tolerance already buys that. (Split later for scale
or release-cadence reasons, per [§2](#2-frontend-embed-vs-split), not for
safety.)

### Simplest way vs. right way (redeploy strategy)

- **Simplest:** stop old container, start new (recreate). Cost: a hard gap where
  *no* container answers; every client hits reconnect-with-backoff; uplink
  retries pile up; worst case the buffer high-water trips.
- **Right:** rolling deploy with readiness gating + graceful drain (new revision
  healthy before old exits). Cost: momentarily two revisions live (fine — both
  are stateless over the same store) + a shutdown handler.
- **Recommendation:** **rolling + graceful drain.** The seq-log makes even a hard
  recreate *correct*, but rolling makes it nearly *seamless*. The only real code
  cost is the SIGTERM handler in §1; the platform does the rest. Do this from v1.

---

## 4. Container health & readiness

Two orthogonal signals (link [`05-observability`](README.md) for the metrics/log
side; this section is the deploy-plumbing view).

| Endpoint | Question it answers | Depends on | On failure |
|---|---|---|---|
| `/healthz` (liveness) | "Is the process wedged?" | process alive, event loop responsive — **no external deps** | platform **restarts** the container |
| `/readyz` (readiness) | "Should traffic route here *now*?" | Postgres reachable, secrets resolved, migrations applied, blob reachable | platform **stops routing** (no restart) until ready again |

- **Keep liveness dependency-free.** A `/healthz` that pings Postgres will cause a
  **restart storm** during a transient DB blip — restarting the app can't fix a
  DB outage. Liveness = "the process itself is healthy"; readiness = "its
  dependencies are healthy." This split is the single most important health
  design decision.
- **Readiness gates rollout.** The rolling deploy ([§3](#3-resync-tolerant-redeploys))
  waits for the new revision's `/readyz` before draining the old one, and readiness
  flips to not-ready first on `SIGTERM`. So readiness is what makes both startup
  and shutdown graceful.
- **Startup ordering:** resolve secrets → connect Postgres → **apply/verify
  migrations** ([`07`](README.md)) → open blob → *then* report ready → *then*
  accept downlink subscriptions. A container that's up but pre-migration must read
  as not-ready, never ready.

### Simplest way vs. right way (health)

- **Simplest:** one `/healthz` returning 200 if the process is up; used for both
  liveness and readiness. Cost: DB-blip restart storms; traffic routed to a
  not-yet-migrated container.
- **Right:** distinct `/healthz` (deps-free) + `/readyz` (deps-checked, gates
  routing & rollout). Cost: two handlers + a lightweight dep-probe cache (probe
  deps on a short interval, don't hammer Postgres per request).
- **Recommendation:** **two endpoints, cached dep probes.** It's a few lines and
  it's what makes rolling deploys and dependency blips behave. Standard practice;
  no reason to skimp.

---

## 5. Config/secrets injection at deploy

Everything environment-specific is **injected at deploy**; the image is
identical across environments (12-factor). See [`06-iac`](README.md) for how
Terraform wires these, and [`02` §2.6](02-components.md#26---hub-url--config--env-plumbing)
for the *host-side* `--hub-url` precedence.

| Category | Examples | Source at deploy | In the image? |
|---|---|---|---|
| Non-secret config | listen port, DB host/name, blob bucket URL, IdP issuer URL, allowlist ref, heartbeat interval, embed-SPA flag | env vars / config, set by Terraform | **no** |
| Secrets | DB password, PAT-hashing pepper, OIDC client secret | `gocloud.dev/secrets` reference resolved at boot ([`02` §1.7](02-components.md#17-blob-store--secrets-gocloud)) | **no** |
| Baked defaults | — | — | **none that leak** |

- **No default hub endpoint, ever** ([`01` §3](01-architecture.md#3-connected-vs-disconnected)).
  That rule lives on the *host* side (the host must be told where the hub is);
  the hub itself likewise bakes in **no** environment-specific address, tenant, or
  credential. Public-repo hygiene: the code carries only *shapes* of config, never
  values.
- **Secrets by reference, resolved at boot.** The container gets a *reference*
  (e.g. a secret URI the platform's secret manager backs), not the secret literal,
  and resolves it through the `gocloud.dev/secrets` portable interface at startup.
  This keeps secrets out of the image, out of env-var dumps where avoidable, and
  swappable per cloud without code change. Fail startup **loudly** (not-ready)
  if a required secret can't be resolved.
- **Fail-fast validation at boot.** On startup, validate that all required config
  is present and well-formed *before* reporting ready. A missing DB URL or
  unresolved secret should produce a clear log line and a not-ready state, not a
  half-up container that 500s under load.
- **Local/dev backends.** For local hub + tests, the same interfaces bind to
  `memblob`/`fileblob` and a local secrets impl ([`02` §1.7](02-components.md#17-blob-store--secrets-gocloud),
  [`12`](README.md)) — no cloud account needed to run the hub.

### Simplest way vs. right way (config/secrets)

- **Simplest:** plain env vars for everything, secrets included. Cost: secrets
  leak into process listings / crash dumps / logs; no rotation story; not portable
  across clouds' secret managers.
- **Right:** non-secret config via env; secrets via `gocloud.dev/secrets`
  references resolved at boot, with fail-fast validation. Cost: a secrets
  interface + a boot-time resolve step.
- **Recommendation:** **env for config, `gocloud` secrets for secrets, validate
  at boot.** The secrets indirection is already the chosen stack
  ([`01` §7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs));
  using it at deploy is free and it's the thing standing between us and a leaked
  credential in a public-repo world.

---

## 6. Rollout & version skew

Three independently-deployed artifacts, three cadences:

```
   host (sprawl enter)        hub (hubd)              browser (SPA)
   updates when the           updates on redeploy     updates on page reload
   maintainer upgrades        (operator-controlled)   (whenever a client
   their local sprawl                                  refreshes)
        └────────── all speak the SAME Connect/protobuf wire ──────────┘
                          skew is the normal state, not an error
```

**Skew is permanent and expected:** an old host may talk to a new hub for weeks;
a stale browser tab may outlive several hub deploys. The wire contract must
tolerate this.

### `buf breaking` is the load-bearing guarantee

- **`buf breaking` in CI** ([`03` §3](03-api-surfaces.md#3-buf-toolchain)) rejects
  any wire-incompatible change (field renumber/removal/type change) against the
  baseline, so a hub deploy **cannot** silently break a deployed host or an old
  browser tab.
- **Additive-only field policy + never reuse field numbers.** New capabilities
  are new optional fields / new RPCs; old clients ignore what they don't know.
- **Baseline question** (open): track `buf breaking` against `main` HEAD or the
  last-released hub tag? ([`03` Open Questions](03-api-surfaces.md#open-questions)).

### Version handshake & display

- **Advertise versions.** The hub reports its build version (and min-compatible
  host/SPA if we adopt one) on connect; hosts send their version on
  `PushEvents`/lease claim; the SPA sends its build on subscribe. Surface these in
  `ListInstances` and the UI so skew is *visible*, not mysterious.
- **Soft-warn, don't hard-block, on skew** (v1). If a client is older than a
  declared floor, show a "please update" banner rather than refusing service —
  `buf breaking` already guarantees the *wire* still works. Reserve hard version
  gates for a genuine, unavoidable breaking migration (should be rare-to-never if
  the additive policy holds).
- **Frontend/SPA version match (embedded case).** When embedded, SPA assets and
  the API are the *same image* — they can never skew against each other on a given
  container. The only skew is **browser-cached old SPA vs. new API**, handled by
  asset cache-busting (content-hashed filenames) so a reload pulls the new SPA.

### Migrations & rollout ordering

- **Backward-compatible migrations** ([`07`](README.md)): a new hub revision must
  run against the schema the *old* revision left, because during a rolling deploy
  both revisions are briefly live over the same Postgres. Prefer
  expand→migrate→contract (add columns/tables first, remove only after all
  revisions using the old shape are gone).
- **Rollout order for a coordinated change:** widen the wire/schema (additive) →
  deploy hub → let hosts/browsers adopt at their own pace → (much later, if ever)
  retire the old field in a separate release once no client uses it.

### Simplest way vs. right way (skew management)

- **Simplest:** no version handshake, no breaking gate; assume everyone updates
  together. Cost: false — they don't; a silent wire break bricks an un-updated
  host or a phone tab cryptically, exactly when you can't reach it.
- **Right:** `buf breaking` gate + additive-only policy + version advertisement +
  soft-warn banners + expand/contract migrations. Cost: one CI step + a version
  field or two + migration discipline.
- **Recommendation:** **the right way — it's cheap and it's the whole point of
  protobuf here.** Three independently-updating deployables is the reality;
  `buf breaking` + additive-only is the minimal discipline that makes that reality
  safe. Skip only the *hard* version-gate machinery until a real breaking
  migration forces it (YAGNI).

---

## Open Questions

- **Embed toggle mechanism:** build tag vs. runtime flag vs. both — which is the
  primary lever, and do we want a single image that can run either way, or two
  images (embedded / API-only)? (Leans build-tag; confirm against CI/image-size
  cost.)
- **Graceful-drain grace period:** how long to drain in-flight unary calls and
  hold politely-closing downlink streams on `SIGTERM` before force-exit —
  fixed (e.g. 10–30s) or tied to the platform's termination grace period?
- **Rollout strategy on the target platform:** does the chosen managed container
  platform support true rolling deploys with readiness gating out of the box, or
  do we need explicit revision/traffic-split config in Terraform ([`06`](README.md))?
- **Min-compatible-version floor:** do we adopt an explicit declared floor per
  deployable (hub↔host↔SPA), or lean entirely on `buf breaking` + soft-warn and
  never hard-gate in v1?
- **`buf breaking` baseline:** `main` HEAD vs. last-released hub tag (shared with
  [`03`](03-api-surfaces.md#open-questions)) — which avoids blocking in-flight PRs
  without letting a real break through?
- **Single-machine co-located deploy:** should there be a blessed "hub + one host
  on the same box" convenience deploy (shared with
  [`00` Open Questions](00-overview.md#open-questions)), and if so does it change
  packaging (e.g. a compose file) or is it just config?
- **Downlink fan-out sizing:** how many concurrent held-open server-streams can
  one container hold before we must scale out — and does scaling out to N
  containers require sticky routing or does the stateless-over-Postgres model make
  any container answer any client? (Sizing shared with
  [`03` Open Questions](03-api-surfaces.md#open-questions).)
- **Config hot-reload:** can `--hub-url`/PAT/allowlist change without a hub
  restart, or is a redeploy the sanctioned path (cheap anyway per [§3](#3-resync-tolerant-redeploys))?
