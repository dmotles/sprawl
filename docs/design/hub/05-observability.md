# 05 — Observability

*How we see inside the hub: structured logging, an introspection endpoint tests
can assert on, metrics, tracing across the seams, and container health probes.
Conforms to [`01-architecture.md`](01-architecture.md).*

See also: [`00-overview.md`](00-overview.md) · [`02-components`](02-components.md) ·
[`09-synchronization`](09-synchronization.md) · [`12-testability-local-dev`](README.md) ·
[index](README.md)

---

## 0. Framing — this is a personal-scale hub (KISS/YAGNI)

The hub federates *a handful* of a single maintainer's sprawl instances for one
or two human viewers. It is **not** a multi-tenant SaaS fleet. So the loud
lesson up front:

> **A personal-scale hub does NOT need heavyweight observability on day one.**
> No Prometheus server, no Grafana, no OpenTelemetry collector, no APM vendor.
> Those solve *fleet* problems (SLOs across thousands of instances, on-call
> alerting, capacity planning) the hub does not have.

What the hub *does* need, ordered by payoff:

1. **Structured logs** (slog/JSON) — near-zero cost, huge debugging payoff.
2. **A `/debug/state` introspection endpoint** — the single highest-value item
   here; it is what lets tests assert on *structured state* instead of scraping
   log text (§2). This is the sprawl gold-standard lesson, applied to the hub.
3. **Health/readiness probes** — required, because the orchestrator needs them
   to run the container at all (§4).
4. **Metrics & tracing** — *deferred hooks*, not deployed infra. Wire the seams
   so they're cheap to turn on later; don't stand up the plumbing now (§3).

Everything below is weighed simplest-vs-right against that framing.

---

## 1. Structured logging

### 1.1 slog/JSON, and reuse what sprawl already does

Sprawl already logs through Go's stdlib `log/slog` (e.g.
`internal/runtime/eventbus.go`, `internal/supervisor/*`). The hub uses the
**same library and the same conventions** — one mental model across both
binaries.

- **Format:** `slog.NewJSONHandler` in deployed/container mode; a text handler
  for local-dev TTYs. One line = one event, machine-parseable.
- **Canonical fields** (attached as `slog.Attr`, never interpolated into the
  message string so they stay queryable):

  | field | meaning | source |
  |---|---|---|
  | `run_id` | which live `sprawl enter` session | [`01` §4](01-architecture.md) |
  | `host_id` | which physical origin | [`01` §4](01-architecture.md) |
  | `seq` | event-log seq (append/replay paths) | [`09` §1](09-synchronization.md) |
  | `fence` | fence token on write-path logs | [`01` §4](01-architecture.md) |
  | `component` | `ingest` / `dispatch` / `auth` / `registry` / `store` | [`02` §1](02-components.md) |
  | `trace_id` | correlation across seams (§3) | request-scoped |

- **Request-scoped logger.** The Connect API server (`§1.1` of
  [`02`](02-components.md)) puts a `*slog.Logger` pre-loaded with
  `run_id`/`host_id`/`trace_id` into the request `context.Context`; every
  component pulls it from `ctx` rather than reaching for a global. This is the
  same DI discipline the rest of the codebase uses.

**Never log secrets.** PAT material, OIDC client secrets, and the hashing pepper
are resolved through `gocloud.dev/secrets` ([`02` §1.7](02-components.md)) and
must never hit a log line. Log the *host_id* a PAT authenticates, never the
token. Redact the hub endpoint to host-only where it appears (matches the
host-side "connected to &lt;host&gt;, token redacted" rule in
[`02` §2.6](02-components.md)).

### 1.2 Verbosity control (env)

`slog` levels, selected by env var, no rebuild:

```
SPRAWL_HUB_LOG_LEVEL = debug | info | warn | error   (default: info)
SPRAWL_HUB_LOG_FORMAT = json | text                  (default: json; text for TTY)
```

- `debug` turns on per-frame append/fan-out tracing (seq-level chatter); `info`
  is the deployed default (connects, lease grants, reclaims, errors).
- Mirrors sprawl's existing env-driven knobs (e.g. `SPRAWL_DEBUG_*` toggles) so
  operators learn one pattern.

**Simplest vs. right.** *Simplest:* a single `-v` bool (on/off debug). *Right:*
full leveled logging with a per-component override map
(`SPRAWL_HUB_LOG_LEVEL_ingest=debug`). *Cost of right:* parsing + documenting a
mini-config nobody may use. **Recommendation:** **one global level env var**
(the middle path). Leveled slog is free and standard; the per-component override
is YAGNI until a noisy component actually forces it — add it then, it's a
one-liner.

### 1.3 Log destinations

- **Deployed:** write JSON to **stdout/stderr only**. The container platform
  captures and ships it (the twelve-factor rule). The hub does **not** own log
  files, rotation, or a log-shipping agent — that's the platform's job and
  keeping it there preserves multi-cloud portability ([`01` §7](01-architecture.md)).
- **Local dev:** text handler to the terminal.

**Simplest vs. right.** *Simplest:* stdout only. *Right:* pluggable sinks (file,
syslog, direct-to-Loki). *Cost of right:* re-implementing what every container
platform already does, plus a provider coupling. **Recommendation:**
**stdout/stderr only.** This *is* the right way for a containerized personal
hub; anything more is the platform's concern.

---

## 2. The introspection endpoint (the gold-standard lesson)

> **The single most valuable thing in this doc.** Sprawl's own hard-won lesson:
> tests that scrape log text or screen output are brittle and lie. Expose
> **structured internal state** and let tests assert on *that*. The hub bakes in
> such an endpoint from day one.

### 2.1 `GET /debug/state` → structured JSON snapshot

A read-only endpoint returning a typed snapshot of the hub's live internal
state. It reads the same in-memory structures the components already hold — it
does **not** compute anything new or touch the write path.

```jsonc
// GET /debug/state  →  200 application/json
{
  "now": "<server-stamped RFC3339>",
  "leases": [                          // from lease/fence registry (02 §1.5)
    { "project": "P", "holder_host_id": "H", "fence": 7,
      "last_heartbeat": "…", "ttl_remaining_ms": 4200 }
  ],
  "connections": [                     // connected hosts + browsers
    { "kind": "host",    "host_id": "H", "run_id": "R", "connected_ms": 90000 },
    { "kind": "browser", "sub": "user@idp", "tailing": ["R"] }
  ],
  "streams": [                         // per event-log stream (09)
    { "run_id": "R", "last_seq": 1043, "snapshot_seq": 1000,
      "subscribers": 2 }
  ],
  "ingest": {                          // append-path counters
    "appended": 1043, "rejected_stale_fence": 2, "deduped": 5 }
}
```

Fields deliberately cover the task's must-haves: **active leases**,
**connected instances**, **per-stream last-seq**, and **buffer depth**
(server-side: snapshot lag `last_seq - snapshot_seq`; the host's *outbound*
buffer depth — [`02` §2.5](02-components.md) — is surfaced host-side, see §2.3).

### 2.2 Why an endpoint beats logs (and beats a metrics scrape)

- **Tests assert on a struct, not a substring.** A reconnect/fence test does
  `GET /debug/state`, unmarshals, and asserts `streams[0].last_seq == 1043` or
  `ingest.rejected_stale_fence == 1`. No log-line regex, no ordering flakiness,
  no ANSI stripping. This is exactly the property that makes sprawl's own
  eventbus/TUI tests reliable.
- **It's the natural e2e oracle.** The local-dev hub story
  ([`12`](README.md)) can spin a real hub + in-memory backends, drive a
  round-trip, then read `/debug/state` to prove the lease moved, the fence
  bumped, and the seq advanced.
- **It's cheap.** It serializes state the components already keep; no new
  bookkeeping.

### 2.3 Boundaries & guardrails

- **Read-only.** Never mutates. Safe to hit repeatedly.
- **Gated.** Behind the same auth as the rest of the API, and additionally
  behind `SPRAWL_HUB_DEBUG_ENDPOINT=1` (default **off** in prod, **on** in
  dev/test). It exposes topology (host_ids, subjects) — not secrets, never token
  bytes — but it's still internal state, so it's opt-in.
- **Host-side mirror.** The host hub-client keeps its own tiny introspection
  surface for *its* half of the spine — last-acked seq, outbound buffer depth,
  lease/fence held, connection state ([`02` §2`](02-components.md)). Reuse
  sprawl's existing incident-snapshot / `peek` machinery rather than adding a
  listener on the NAT'd host (no inbound ports — [`01` §1](01-architecture.md)).

**Simplest vs. right.** *Simplest:* no endpoint; tests grep logs. *Right:*
typed `/debug/state` snapshot. *Cost of right:* keeping the snapshot struct in
sync as components grow + the auth/flag gate. **Recommendation:** **build
`/debug/state` on day one.** It is the cheapest reliability lever the hub has and
it directly encodes sprawl's gold-standard testing lesson. The sync cost is
trivial because it reflects state that already exists.

---

## 3. Metrics & tracing — deferred hooks, not deployed infra

The hub's observability *needs* at personal scale are met by §1 + §2. Metrics
and tracing are where fleet-scale tools tempt over-engineering, so the stance is
explicit: **design the seams to be instrument-*able*; do not deploy the
instrumentation.**

### 3.1 Metrics

**What would eventually be worth exposing** (so the shape is known):

- Ingest append rate, stale-fence rejections, dedupe count.
- Fan-out lag (append→browser-delivered), per stream.
- Active connections (hosts, browsers), active leases, reclaim events.
- Outbound-buffer high-water / truncation events (host side,
  [`02` §2.5](02-components.md)).

Note every one of these is **already in `/debug/state`** as a counter/gauge. A
future metrics exporter is a thin adapter over that same state — not new
bookkeeping.

**Simplest vs. right.**
- *Simplest:* the `/debug/state` counters above; scrape them ad hoc. **Cost:**
  no time-series history, no dashboards, no alerting.
- *Right (fleet):* Prometheus client → `/metrics`, a Prometheus server,
  Grafana, alert rules. **Cost:** an always-on metrics stack + dashboards to
  maintain, for one user.
- *Middle:* add the `prometheus/client_golang` handler exposing the same
  counters at `/metrics`, but **don't run a Prometheus server** — leave it dark
  until there's a reason.

**Recommendation:** ship **`/debug/state` counters now (§2)**; leave a
clearly-marked `// TODO(observability): /metrics adapter` seam. **Prefer
Prometheus over OpenTelemetry-metrics** *if/when* metrics are added — the pull
model needs no collector to run, which suits a single small container far better
than an OTel pipeline. Standing up any metrics *server* is YAGNI today.

### 3.2 Tracing across the sprawl → hub → api → web seams

The interesting correlation is one action crossing all four seams:

```
browser input ─▶ hub (downlink) ─▶ host turn-queue ─▶ claude
      ▲                                                   │
      └────── fan-out ◀── hub (ingest) ◀── eventbus ◀─────┘
   trace_id threads the whole round-trip
```

- **The cheap 90%:** propagate a **`trace_id`** (a correlation id, generated at
  the browser or host edge) through every frame and log it as a `slog.Attr`
  (§1.1). Grepping one `trace_id` across host + hub JSON logs reconstructs the
  causal chain — no tracing backend required. The event-log's `(run_id, seq)`
  already gives strong intra-stream ordering ([`09`](09-synchronization.md));
  `trace_id` just links *across* the seams.
- **The expensive 10%:** real distributed spans (OpenTelemetry SDK → an OTLP
  collector → Jaeger/Tempo) with per-span timing.

**Simplest vs. right.** *Simplest:* nothing; correlate by `run_id`+timestamp.
*Right:* full OTel spans + collector. *Cost of right:* an OTel SDK dependency, a
collector to run, and span plumbing on the hot path — heavy for one user.
**Recommendation:** **propagate `trace_id` in frames + logs now** (a string
field and a log attr — nearly free and it makes the JSON logs a poor-man's
trace). Defer OTel spans behind the same seam as metrics; adopt only if a
latency mystery actually demands span timing. If adopted, OTel tracing is the
right choice *because* it's vendor-neutral and matches the multi-cloud stance.

---

## 4. Container health / readiness probes

The orchestrator needs these to schedule and route to the container, so they are
**not optional**. Two standard, cheap endpoints:

| endpoint | question | checks | failure meaning |
|---|---|---|---|
| `GET /healthz` (liveness) | "is the process wedged?" | process up; event loop responsive | restart the container |
| `GET /readyz` (readiness) | "can it serve traffic?" | Postgres reachable; blob/secrets resolvable; migrations applied | pull from rotation, don't kill |

```
orchestrator ──GET /healthz──▶ 200 "ok"                 (cheap, no deps)
orchestrator ──GET /readyz───▶ 200 / 503 {deps: {...}}  (checks downstreams)
```

- **`/healthz` stays dependency-free** — a wedged/slow Postgres must *not* flap
  liveness and cause a restart loop; that's what readiness is for. This
  liveness-vs-readiness split is the standard trap and we call it out explicitly.
- **`/readyz` returns a small JSON** of per-dependency status (reusing the
  §2 pattern) so a failing probe is *diagnosable*, not just a bare 503.
- **Unauthenticated but boring.** Probes reveal only up/down + dependency
  names, never state or secrets, so the orchestrator can hit them without
  credentials. (Contrast with `/debug/state`, which is gated — §2.3.)
- These live on the same Connect listener as everything else
  ([`02` §1.1](02-components.md)); no extra port.

**Simplest vs. right.** *Simplest:* one `/healthz` returning 200 always. *Right:*
split liveness/readiness with real dependency checks. *Cost of right:* a couple
of dependency pings and the discipline to keep `/healthz` dep-free.
**Recommendation:** **both endpoints, with the liveness/readiness split.** It's
the minimum that lets an orchestrator behave correctly (no restart loops on a DB
blip), and it's a few lines. A single always-200 health check is a known
foot-gun under real deploys.

---

## 5. Summary — what ships when

| Item | v1 (now) | Deferred hook | Never (YAGNI) |
|---|---|---|---|
| slog/JSON structured logs + env level | ✅ | | |
| `/debug/state` introspection endpoint | ✅ | | |
| `/healthz` + `/readyz` probes | ✅ | | |
| `trace_id` propagation in frames + logs | ✅ | | |
| `/metrics` (Prometheus adapter over §2 counters) | | ✅ seam | |
| OpenTelemetry spans + collector | | ✅ seam | |
| Prometheus/Grafana/APM *server stack* | | | ❌ (personal scale) |
| Hub-owned log files / rotation / shippers | | | ❌ (platform's job) |

The through-line: **logs + one introspection endpoint + health probes** cover a
personal-scale hub completely; metrics and tracing are cheap-to-enable seams,
not day-one infrastructure.

---

## Open Questions

- **Where does per-stream fan-out lag get measured** — is append→browser-delivered
  latency observable purely from `/debug/state` gauges, or does it need a small
  timing ring per stream? (Touches the fan-out design in [`02` §1.3](02-components.md).)
- **`/debug/state` at multi-host scale.** With several hosts and multiple
  browsers, does one flat snapshot stay readable, or does it need
  filtering (`?project=` / `?run_id=`)? Cheap to add, but when?
- **Does the host half of the spine need its own `/debug/state`-shaped surface**
  for e2e tests, or is reusing `peek` + the incident-snapshot bundle
  (`Ctrl+\` incident snapshot, per `CLAUDE.md`) sufficient to assert host-side buffer depth / last-acked
  seq?
- **`trace_id` origin.** Generated at the browser edge, the host edge, or minted
  by the hub on first contact? Affects whether a locally-typed (no-browser) turn
  still gets a trace id.
- **Probe cadence vs. lease TTL.** Should `/readyz` failure interact with the
  lease/heartbeat model ([`01` §4](01-architecture.md)) at all, or are they
  strictly independent (orchestrator health ≠ write-authority)?
- **Log volume at `debug`.** Per-frame seq tracing at `debug` could be very
  chatty on a busy multi-host session — do we need sampling, or is level-gating
  enough given logs go to stdout and the platform handles retention?
- **Metrics trigger.** What concrete signal (a real latency/loss incident?)
  should flip the deferred `/metrics` seam from dark to on — i.e. how do we avoid
  building it "just in case"?
