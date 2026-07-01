# 12 — Testability & Local Dev

*How every hub seam is tested without a live LLM or a real cloud, and how a
maintainer runs the whole production-shaped stack locally — including several
worktrees at once.*

See also: [`01-architecture.md`](01-architecture.md) (event-log spine, lease/fence) ·
[`02-components.md`](02-components.md) (the "Faked in tests via" matrix) ·
[`03-api-surfaces.md`](03-api-surfaces.md) (Connect/buf, streaming shapes) ·
[`09-synchronization.md`](09-synchronization.md) (the one reconnect rule) ·
[index](README.md)

---

## 0. TL;DR — read this first

- **The linchpin is a mocked claude stream.** A scripted, seq'd fixture event
  source stands in for the live LLM so **no seam test depends on a real
  model** — deterministic, offline, fast. Everything else in this doc hangs off
  that one decision (§1).
- **Every dependency has a hermetic fake** already named in
  [`02` §4](02-components.md#4-component--responsibility-matrix): in-memory
  `Store`, `gocloud` memblob/fileblob, a fake IdP that mints real JWTs, static
  PATs. Postgres gets a `testcontainers-go` suite so the *real* SQL path is
  exercised too (§2).
- **Local dev runs the production shape**: `docker-compose` brings up hub +
  Postgres + blob; multiple worktrees coexist via a **port probe + `--port-file`**
  (no stdout-scrape race) and per-worktree state dirs / DB schemas (§3).
- **Four integration seams, each deterministic** because the claude stream and
  every WAN dependency are fixtures (§4).
- **Contract testing** is `buf breaking` in CI + **golden protobuf fixtures**
  asserted on *both* the Go and TS sides so an old host / old browser tab can't
  silently break (§5).
- **Tests assert structured state** via a `/debug/state` introspection endpoint,
  **never scraped logs** (§6).
- **New e2e-matrix rows** (`hub-client`, `hub-api`, `hub-fullstack`,
  `web-contract`) extend `CLAUDE.md`'s existing touched-files→row table (§7).

The guiding principle throughout: **"separable for testing, colocated for
deploy"** ([`02` §0](02-components.md#0-reading-guide)). Every component is
already behind an interface; this doc says what to put behind it in a test.

---

## 1. The linchpin — MOCK THE CLAUDE STREAM

Sprawl's source of truth is the live claude session ([`01`
§1](01-architecture.md#1-topology)). A test that needs a real model is slow,
non-deterministic, costs tokens, and can't run in CI. **So the top testability
decision is: the hub and hub-client are tested against a scripted event source
that replays a fixed sequence of `RuntimeEvent`s** — the same seq-stamped events
the local eventbus (`internal/runtime/eventbus.go`) already produces.

This is not a new idea in the codebase — it is the *generalization* of the
existing pattern. Sprawl's own e2e rows already inject deterministic behavior via
diagnostic seams (`SPRAWL_BACKEND_HANG_TIMEOUT`, `SPRAWL_DEBUG_DROP_NEXT_TERMINAL_MSG`,
`SPRAWL_DEBUG_GAP_INJECT`; see `CLAUDE.md`'s matrix). The hub client subscribes
to the eventbus as *just another consumer* ([`02`
§2.1](02-components.md#21-eventbus-subscriber-another-consumer)), so a fixture
that **publishes a scripted seq'd stream onto a real eventbus** exercises the
entire uplink path with zero LLM involvement.

### What the fixture is

```
fixture script (JSON/table) ─▶ ScriptedStreamSource ─▶ real EventBus.Publish(seq)
   [{seq:1, kind:assistant, ...},                          │
    {seq:2, kind:tool_use, ...},                           ├─▶ TUI (real)
    {seq:3, kind:terminal, undroppable:true}, ...]         └─▶ hub client (under test)
```

- **Seq'd and ordered** to match the real bus contract (monotonic, 1-indexed,
  gap-detecting, terminal-undroppable — [`09`
  §1](09-synchronization.md#1-seq-stamping--reused-not-reinvented)). Tests can
  therefore assert reconnect/replay behavior deterministically: "drop the
  connection after seq 3, reconnect, assert seqs 4..HEAD replay with zero
  gaps/dupes."
- **Scriptable pathologies**: inject a gap, a duplicate seq, a terminal event, a
  long idle (to exercise the heartbeat, [`03`
  §4.3](03-api-surfaces.md#43-http2-keepalive--ping-tuning--necessary-but-not-sufficient)),
  or a burst (to exercise buffer high-water + drop-oldest, [`09`
  §6](09-synchronization.md#6-local-outbound-buffer-high-water-policy)).
- **Replayable**: the same script → the same bytes on the wire → golden-
  comparable at the hub's store.

### Simplest way vs. right way

- **Simplest:** stub the claude subprocess with a canned stdout transcript and
  parse it as today. Cost: couples every test to the claude *wire* format and the
  parser; can't easily inject seq-level pathologies; re-tests the parser instead
  of the hub.
- **Right:** a `ScriptedStreamSource` that emits already-parsed, seq-stamped
  `RuntimeEvent`s directly onto a real `EventBus`, bypassing the subprocess and
  parser entirely for hub tests.
- **Recommendation:** **the right way — fixture at the `RuntimeEvent` layer.**
  The hub cares about seq'd events, not claude's stdout grammar. Injecting at the
  event layer makes hub tests immune to claude CLI churn and lets a single JSON
  fixture drive uplink, fan-out, reconnect, and buffer tests. Keep a *thin*
  subprocess-transcript fake in reserve only for the one row that must prove the
  real parser→bus wiring (that's an existing sprawl concern, not a hub one).

> **Consequence:** every seam test below (§4) is deterministic *because* its
> input is this fixture, not a model. This is why §1 is the linchpin — remove it
> and the other four seams become flaky live-LLM tests.

---

## 2. Hermetic dependencies

The [`02` §4](02-components.md#4-component--responsibility-matrix) matrix already
prescribes a fake per component. This section pins the concrete choice and the
one tradeoff each carries.

### 2.1 Event-log store — in-memory impl + testcontainers Postgres

The store is a **Go interface** ([`02`
§1.6](02-components.md#16-event-log-store-interface)) with two impls:

| Impl | Used by | Buys |
|---|---|---|
| `memStore` (in-memory) | unit + most seam tests | instant, zero-setup, deterministic; the default for the four seams (§4) |
| Postgres (real) via **`testcontainers-go`** | a dedicated store suite | proves the *real* SQL — append ordering, `(run_id, seq)` uniqueness/idempotency, retention-trim queries, snapshot lookup "newest ≤ S" ([`09` §3](09-synchronization.md#3-snapshot-cadence--snapshot-fallback)) |

Both impls run the **same interface conformance test suite** (a shared
table-driven test that any `Store` must pass). That is the guard that the
in-memory fake doesn't drift from real Postgres semantics.

- **Simplest vs. right:** simplest is in-memory only (fast, but SQL bugs escape
  to prod); right is in-memory for breadth + testcontainers for the SQL contract.
  **Recommendation: both, behind one conformance suite.** The in-memory impl
  keeps the 4 seams fast; the testcontainers suite (gated like the live rows,
  skippable when Docker is absent — mirror `SPRAWL_E2E_SKIP_NO_CLAUDE` with e.g.
  `SPRAWL_TEST_SKIP_NO_DOCKER`) proves the real driver. Never let the in-memory
  fake be the *only* thing the store logic is tested against.

### 2.2 Blob & secrets — gocloud memblob/fileblob

`gocloud.dev/blob` and `gocloud.dev/secrets` are chosen precisely because their
local backends *are* the test backends ([`02`
§1.7](02-components.md#17-blob-store--secrets-gocloud)):

- **`memblob`** for unit tests (attachments, snapshot bodies) — no filesystem.
- **`fileblob`** for local-dev + integration (a real directory, survives across
  a compose run, inspectable by hand).
- **secrets:** a local/static secrets impl (dev) resolving the same interface as
  the cloud KMS in prod — so the PAT-hashing pepper and OIDC client secret paths
  are exercised without a cloud.

The abstraction cost is ~nil because it is *also* the local-dev backend — no
provider is baked into app code, honoring the multi-cloud promise ([`01`
§7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs)).

### 2.3 Test OIDC issuer — mint real JWTs, exercise the real verify path

Auth is an OIDC relying party via `go-oidc` ([`02`
§1.4](02-components.md#14-auth-oidc-relying-party--pat-verify)). The fake must
not stub out verification — that would leave the security-critical path untested.

- **A test issuer** stands up a tiny HTTP server exposing a real
  `/.well-known/openid-configuration` + JWKS, with a locally-generated signing
  key. It **mints valid JWTs** for test users.
- The hub's **real `go-oidc` verifier** validates them against the fetched JWKS —
  so signature verification, issuer/audience checks, expiry, and the **allowlist
  gate** all run for real. Negative tests mint deliberately-bad tokens (wrong
  issuer, expired, unknown user) and assert rejection.
- **PATs** are tested with a static known token whose **hash** is seeded in the
  store; the real hash-compare path runs (no shared-secret shortcut, matching
  [`04`](README.md)'s "hashed PATs" decision).

- **Simplest vs. right:** simplest injects a "verified user" and skips crypto;
  right runs the real verifier against a test-issued JWT. **Recommendation: the
  test issuer.** Auth is the boundary that justifies the hub existing ([`02`
  §1.4](02-components.md#14-auth-oidc-relying-party--pat-verify)); a test that
  bypasses verification tests nothing that matters. `go-oidc` + a self-hosted
  JWKS is a well-trodden pattern and adds only a few lines.

---

## 3. Local dev + multi-worktree isolation

Sprawl is developed *inside itself* — several agent worktrees may each want to
run a hub or a hub-connected `sprawl enter` at once. Two isolation axes:
**ports** and **state**.

### 3.1 Port probe + `--port-file` (no stdout-scrape race)

Multiple hubs on one machine collide on a fixed port. The fix is two parts:

1. **Port probe with fallback.** The hub binds `0` (OS-assigned) or probes a
   base port and increments until a free one is found. No hardcoded default
   (also public-repo hygiene: no baked-in endpoint, [`01`
   §3](01-architecture.md#3-connected-vs-disconnected)).
2. **`--port-file <path>`.** The hub writes the *actually-bound* `host:port` to a
   file **atomically** (write-temp + rename) once listening. Tests and sibling
   processes read the file instead of scraping stdout.

```
hub --port 0 --port-file .sprawl/hub.addr
   └─ binds :54417, writes ".sprawl/hub.addr" = "127.0.0.1:54417" (atomic rename)
test / sprawl enter ──reads──▶ .sprawl/hub.addr ──▶ --hub-url
```

- **Why not scrape stdout?** It's the classic race: the reader may attach after
  the line is printed, or interleave with other logs, or the format drifts. A
  file that appears atomically *after* the listener is up is a clean readiness +
  address signal in one.
- **Simplest vs. right:** simplest is a fixed port + hope; right is probe +
  `--port-file`. **Recommendation: probe + `--port-file`.** It's the deterministic
  primitive the e2e rows (§7) need to find the hub they just launched, and it
  makes N-worktree local dev collision-free. (This mirrors sprawl's existing
  aversion to stdout-scrape races — cf. the deterministic seams in `CLAUDE.md`.)

### 3.2 Per-worktree state dirs + DB schema

- **State dir:** each worktree already has its own `.sprawl/` ([`CLAUDE.md`],
  "Meta: Developing Sprawl Inside Sprawl"). The hub's local state (port file,
  fileblob dir, PAT store) lives under the worktree's `.sprawl/hub/`, so two
  worktrees never share hub state.
- **DB schema-per-worktree:** against one local Postgres, each worktree/test uses
  a **distinct schema** (or database) named from the worktree/namespace — e.g.
  `hub_<namespace>`. Migrations run into that schema; teardown drops it. This is
  the DB analogue of the `SPRAWL_NAMESPACE` isolation the sandbox already uses
  for tmux ([`CLAUDE.md`], tmux safety). testcontainers gives each *suite* a
  throwaway instance; schema-per-worktree covers the shared-local-PG dev case.

- **Simplest vs. right:** simplest is one shared DB, hope tests don't collide;
  right is schema/db-per-worktree with drop-on-teardown. **Recommendation:
  schema-per-worktree**, keyed off the existing namespace — cheap, and it makes
  concurrent worktree hub runs (and parallel test packages) safe by construction.

### 3.3 Extend `scripts/sprawl-test-env.sh`

The existing script builds the binary and seeds a `/tmp` sandbox repo with
`SPRAWL_NAMESPACE` + `SPRAWL_ROOT` (safety-guarded to `/tmp`). Extend it,
**opt-in behind a flag/env** so today's callers are unaffected:

- `--with-hub` (or `SPRAWL_TEST_WITH_HUB=1`): launch a local hub with
  `--port 0 --port-file "$TEST_ROOT/.sprawl/hub.addr"`, back it with
  fileblob + a testcontainers (or local schema) Postgres, mint a dev PAT + seed
  its hash, and export `SPRAWL_HUB_URL` read back from the port file.
- Emit the extra env (`SPRAWL_HUB_URL`, `SPRAWL_HUB_PAT`) alongside the current
  exports so `eval "$(...)"` wires a connected host in one step.
- Preserve every existing safety guard (refuse-in-worktree, `/tmp`-only,
  cleanup traps) and add hub teardown (stop container, drop schema) to the trap.

### 3.4 `docker-compose` for the production-shaped stack

A `docker-compose.yml` (under a `deploy/local/` or `hack/` dir, **not** wired
into any default endpoint) brings up the **production shape** locally:

```
services:
  hub:       build ., ports "0:8080" (or fixed for convenience), reads OIDC issuer
             + DB DSN + blob bucket from env; serves embedded SPA
  postgres:  managed-PG-equivalent image, volume for durability across runs
  oidc:      the test issuer (§2.3) so the browser login flow works end-to-end
  (blob:     fileblob volume — or a MinIO-class S3 shim if we want the real
             gocloud/blob "s3blob" code path exercised locally)
```

- **Purpose:** manual full-stack smoke ("open the browser, log in via the test
  issuer, tail a scripted session, type input back down") and the `hub-fullstack`
  e2e row (§7). It is the local twin of the Terraform-provisioned prod stack
  ([`06`](README.md), [`08`](README.md)) — same containers, parameterized config,
  no cloud.
- **Simplest vs. right:** simplest is `go run ./hub` + a local PG you manage by
  hand; right is compose bringing the whole shape up reproducibly.
  **Recommendation: compose for the full-stack path, plain `go test` +
  in-memory/testcontainers for everything else.** Don't force compose on unit
  tests (slow); do use it for the one row that proves browser↔hub↔host through
  real containers.

---

## 4. The four integration seams (all deterministic)

Each seam is deterministic *because* its input is the scripted claude stream
(§1) and every WAN dependency is a fake (§2). They map directly onto the
reconnect seams in [`09` §0](09-synchronization.md#0-the-one-rule-written-once-reused-at-every-seam).

| # | Seam | What it proves | Fakes used | e2e row (§7) |
|---|---|---|---|---|
| 1 | **sprawl ↔ hub** | host hub-client uplink → hub ingest → store append; fence check; ack → buffer trim; reconnect replay from `from_seq` | scripted stream, in-mem store, static PAT, fake conn or real bufconn | `hub-client` |
| 2 | **sprawl ↔ hub ↔ api** | end-to-end append then browser-side `SubscribeInstance` fan-out; one-rule replay to a browser client; `SubmitInput` downlink → host turn-queue | + test issuer (OIDC), in-mem registry | `hub-api` |
| 3 | **sprawl ↔ hub ↔ api ↔ web** | the full pane-of-glass round trip incl. the real SPA — **headless first**, then **Playwright** for the rendered browser | compose stack (§3.4), test issuer, fileblob | `hub-fullstack` |
| 4 | **web ↔ api** (fixture-replay) | the SPA against a **mock stream** replaying a recorded fixture — pure frontend reconnect/render logic, no host needed | recorded event fixture served by a stub api | `web-contract` |

### 4.1 Seam 1 — sprawl ↔ hub (Go↔Go)

The workhorse. Run the hub-client and the hub in-process (or over a Connect
`bufconn`/`httptest` listener — [`02`
§4](02-components.md#4-component--responsibility-matrix): "httptest / bufconn").
Drive a scripted stream; assert the store's appended `(run_id, seq)` sequence
equals the script (golden), that a stale fence is **rejected** ([`09`
§5](09-synchronization.md#5-fence-tokens-on-uplink-writes-stale-fence-rejection)),
that acks advance and the outbound buffer trims ([`09`
§4](09-synchronization.md#4-idempotent-apply--bidirectional-ack-watermarks)), and
that a mid-stream disconnect + reconnect replays cleanly with zero gaps/dupes.

### 4.2 Seam 2 — add the api (browser-facing RPCs)

Layer the OIDC-authed api surface ([`03`
§2](03-api-surfaces.md#2-surface-2-api--webapp)) on seam 1. A **Go browser-client
stand-in** logs in via the test issuer, calls `ListInstances` /
`SubscribeInstance`, and asserts it receives the same seq'd events the scripted
host produced. Then it calls `SubmitInput` and the test asserts the input lands
in the host's **one turn-queue** ([`02`
§2.3](02-components.md#23-downlink-receiver--the-one-turn-queue)). This is the
full uplink+downlink loop with no real browser yet.

### 4.3 Seam 3 — add the real web (headless, then Playwright)

- **Headless first:** exercise the SPA's transport/reducer logic in a JS test
  runner against the real api (or the seam-2 stub) — fast, no browser binary.
- **Playwright** for the rendered path on the `docker-compose` stack (§3.4): real
  Chromium logs in through the test issuer, tails a scripted session, types
  input, and asserts the round trip renders. This is the only seam that needs a
  browser binary, so it is the heaviest row — gated + skippable like the live
  rows.
- **Simplest vs. right:** simplest is Playwright-only (one tool, but slow and
  flaky-prone); right is headless-for-logic + Playwright-for-render.
  **Recommendation: split them.** Put reconnect/replay/reducer assertions in fast
  headless tests; reserve Playwright for "does it actually render + round-trip in
  a real browser," where its cost is justified.

### 4.4 Seam 4 — web ↔ api with fixture-replay mock streams

The SPA is "just an event-log consumer" ([`02`
§3](02-components.md#3-frontend-spa-just-an-event-log-consumer)). Test it in
isolation by **replaying a recorded event fixture** through a stub api that
speaks the real `SubscribeInstance` wire shape. This proves the browser's
one-rule reconnect (replay-from-seq, snapshot-fallback, live-tail) and rendering
**without any host or LLM at all** — the fastest, most deterministic frontend
test. The fixture is the *same kind of artifact* as the scripted claude stream
(§1), recorded once and asserted as golden.

---

## 5. Contract testing — buf breaking + golden protobuf fixtures

Three deployables evolve independently (hub, host, browser) — an old host must
keep talking to a new hub, and a stale mobile tab must not die cryptically
([`03` §3](03-api-surfaces.md#3-buf-toolchain)). Two guards:

### 5.1 `buf breaking` in CI

`buf breaking` runs against a baseline so a field renumber/removal that would
break a deployed peer **fails the build**. Wire it into `make validate` next to
the Go checks (this repo has Go-only validation today — no markdown/JS linters,
per [`03` §3](03-api-surfaces.md#3-buf-toolchain)). Also run `buf lint` +
`buf format`.

- **Baseline (open in [`03`](03-api-surfaces.md#open-questions)):** track against
  `main` HEAD for tightest safety, or the last-released hub tag to avoid
  in-flight `main` churn blocking PRs. **Recommendation: last-released tag** as
  the breaking baseline (that's the version real deployed peers run), with
  `buf lint`/`format` on every PR. Revisit if release cadence makes the tag too
  stale.

### 5.2 Golden protobuf fixtures — asserted on BOTH sides

`buf breaking` catches *schema* breaks; golden fixtures catch *serialization/
interpretation* drift. Check in a set of **canonical encoded messages** (binary
+ their expected decoded form) and assert them in **both** codegen targets:

```
testdata/contract/*.binpb  (canonical wire bytes, checked in)
        │
        ├─▶ Go test   (connect-go):  decode → assert fields; encode → assert bytes
        └─▶ TS test   (connect-es):  decode → assert fields; encode → assert bytes
```

- This proves the Go host/hub and the TS browser agree on the wire for the same
  bytes — the thing `buf breaking` alone can't (it reasons about the schema, not
  a concrete message a real peer sent). Regenerate goldens deliberately (a
  `make` target) so a diff is a reviewed decision, never an accident.
- **Simplest vs. right:** simplest is `buf breaking` only; right adds cross-
  language golden fixtures. **Recommendation: both.** The golden set is cheap
  insurance for the exact failure mode (Go and TS disagreeing on an evolved
  message) that `buf breaking` doesn't cover, and it's the natural home for the
  seam-4 (§4.4) replay fixture too.

---

## 6. Observability for tests — `/debug/state`, not scraped logs

Tests must assert on **structured state**, not log strings (log scraping is
brittle and is exactly the race §3.1 avoids for ports). The hub exposes a
**`/debug/state`** introspection endpoint returning structured JSON:

- lease/fence registry state (`{project → holder_host_id, fence_token, last_heartbeat, n_clients}`),
- per-`run-id` log head seq, retention floor, snapshot seqs ([`09`
  §2–§3](09-synchronization.md#2-durable-replay-buffer--retention-window)),
- connected hosts + live browser consumer watermarks,
- buffer/ack watermarks.

A test drives a scripted stream, then **asserts against `/debug/state`** — e.g.
"after force-reclaim, `fence_token` incremented and both lineages' `run-id`s are
present" ([`09` §8](09-synchronization.md#8-force-reclaim--provenance-based-semantic-reconcile)),
or "after all consumers acked, retention floor advanced." This is the hub analogue
of sprawl's existing structured-state assertions and the `/debug`-style
introspection the incident snapshot already gathers ([`CLAUDE.md`], incident
hotkey).

- **Access:** dev/test-only, gated (build tag or auth-required + off by default in
  prod) so it isn't an information-leak surface in a public deploy.
- **Simplest vs. right:** simplest is grep the logs; right is a structured
  endpoint. **Recommendation: `/debug/state`.** Deterministic assertions,
  refactor-stable (log wording can change freely), and it doubles as an ops
  introspection tool. **No log-scraping in seam tests.**

---

## 7. New e2e-matrix rows (touched-files → row)

Extend `CLAUDE.md`'s **Validating Changes** table and `scripts/e2e-tests/` with
four rows, following the existing convention (one `scripts/e2e-tests/<row>.sh`
with `test_metadata` preflight + `test_run`; `make test-e2e-matrix-<row>`).
Preflights declare needs (`needs_docker=1`, `needs_node=1`, `needs_playwright=1`,
`needs_claude=1` where a real parser→bus path is asserted) and **skip cleanly**
when a dependency is absent (mirror `SPRAWL_E2E_SKIP_NO_CLAUDE`).

| files touched | matrix row | proves (seam §4) |
|---|---|---|
| host hub-client: eventbus subscriber, uplink sender, downlink receiver, lease claim, outbound buffer, `--hub-url`/config plumbing ([`02` §2](02-components.md#2-host-side-additions-to-sprawl-enter-the-hub-client)) | `hub-client` | seam 1: uplink/append/fence/ack/reconnect against in-mem store + scripted stream |
| hub: Connect server, uplink ingest, downlink dispatch/fan-out, lease/fence registry, event-log store, auth ([`02` §1](02-components.md#1-hub-side-components-inside-the-one-container)) | `hub-api` | seam 2: OIDC (test issuer) + fan-out to a Go browser-client + `SubmitInput` → turn-queue |
| `docker-compose` stack, embedded SPA (`go:embed`), the SPA↔api transport, or the full round-trip wiring | `hub-fullstack` | seam 3: real browser via Playwright on the compose stack (headless variant for logic) |
| the `.proto` sources, `buf.gen.yaml`/`buf.yaml`, generated Go/TS clients, or the SPA's reducer/transport code | `web-contract` | seam 4: golden protobuf fixtures (Go+TS) + `buf breaking` + fixture-replay SPA reconnect/render |

- The Postgres `Store` conformance suite (§2.1) runs under `hub-api` (and as a
  standalone `go test` package) with `needs_docker=1` for the testcontainers arm.
- **`web-contract`** is the row that must run `buf breaking` and regenerate/verify
  the golden fixtures on both language sides (§5).
- **Consistency with existing rows:** these follow the same "touched X → run row
  Y" discipline as every row in `CLAUDE.md` today (e.g. `merge-reuse`,
  `wake-live`). When a hub file is touched, the mapping tells the agent exactly
  which row to run — same contract, new surface.

- **Simplest vs. right:** simplest is one giant `hub-e2e` row; right is four rows
  matched to the four seams + the touched-files granularity the repo already uses.
  **Recommendation: four rows.** Granular rows keep CI fast (touch only the
  host client → run only `hub-client`) and match the existing table's philosophy;
  a single mega-row would re-run Playwright + Docker for a one-line buffer change.

---

## 8. What this doc deliberately keeps minimal (KISS/YAGNI)

- **No live-LLM tests in CI.** The scripted stream (§1) is the contract; a real
  claude run is a manual/soak activity, never a gate.
- **No bespoke reconnect test per seam.** The one rule ([`09`
  §0](09-synchronization.md#0-the-one-rule-written-once-reused-at-every-seam))
  is tested once and reused; a per-seam catch-up test would mean the design
  diverged from the spine.
- **No cloud in tests.** gocloud local backends + test issuer + testcontainers
  cover the real code paths without a provider account.
- **Playwright only where render matters** (§4.3) — logic goes in fast headless
  tests.
- **No WebSocket test harness** unless the [`03`
  §5](03-api-surfaces.md#5-recommended-de-risking-spike-run-before-committing-streaming-shape)
  spike forces WS — don't build a transport test for a transport we haven't
  committed to.

---

## Open Questions

- **Scripted-stream fixture format & home.** Is the fixture a checked-in JSON
  table of `RuntimeEvent`s, a recorded real session (sanitized), or a small DSL?
  Recorded sessions are realistic but risk leaking employer/internal content
  (public-repo hygiene) — synthetic fixtures are safer but hand-authored. Where
  do fixtures live so both Go seam tests and the TS `web-contract` row share one
  source of truth?
- **testcontainers in CI.** Does the CI environment allow Docker-in-Docker for
  the Postgres/`hub-fullstack` rows, or do those stay local-only + soak, with CI
  running the in-memory arm? What's the `needs_docker` skip story on the CI
  runner?
- **`buf breaking` baseline** (inherited from [`03`](03-api-surfaces.md#open-questions)):
  `main` HEAD vs. last-released tag — final call, and where the baseline image is
  stored.
- **Golden-fixture regeneration policy.** How are cross-language goldens
  regenerated and reviewed so an intended wire change is one deliberate diff, not
  a silent drift — a `make regen-contract-goldens` target gated behind review?
- **Frontend test toolchain.** The SPA framework is deferred to [`11`](README.md);
  its choice constrains the headless runner and whether `connect-es` golden
  assertions live in the same test tooling as the render tests.
- **`/debug/state` in production.** Fully compiled out via build tag (safest, but
  no prod introspection) vs. auth-gated + off-by-default (useful for ops, larger
  surface)? Ties to [`05-observability`](README.md) and
  [`security-privacy`](README.md).
- **Multi-worktree shared Postgres vs. testcontainers-per-run.** Is
  schema-per-worktree against one local PG (§3.2) worth maintaining alongside
  testcontainers, or does testcontainers-per-suite make the shared-PG path
  redundant for everyone except the `--with-hub` interactive sandbox?
- **How faithfully must the local `docker-compose` mirror the Terraform prod
  stack** ([`06`](README.md)/[`08`](README.md)) to be a trustworthy smoke — same
  images, or just same shapes? Drift between the two is a latent "works locally,
  breaks in prod" risk.
