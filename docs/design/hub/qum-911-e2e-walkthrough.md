# QUM-911 — Hub P1 E2E: live-tail + reconnect zero-gap proof

*The capstone verification for Hub Phase 1: prove the read-only, wire-log-
authoritative browser live-tail stack end to end, **local only** (a local hubd,
no cloud). Wires together the host tailer (QUM-908), the hub in-mem fan-out
(QUM-909), and the SPA (QUM-910) against a real local `hubd`.*

See also: [`12-testability-local-dev.md`](12-testability-local-dev.md) (the
"mock the claude stream" linchpin, `/debug/state` assertions) ·
[`09-synchronization.md`](09-synchronization.md) (the one reconnect rule) ·
[index](README.md).

---

## 0. TL;DR

- **Automated gate (Go, deterministic, CI-safe):** `make test-hub-e2e` — or the
  matrix row `make test-e2e-matrix-hub-e2e`. It stands up a **real local hubd
  process** (built from `cmd/hubd`, in-memory store, no cloud), mints a host
  bearer token over the **real** `/login` → `CreateHostToken` browser flow,
  ships a deterministic seq'd wire log through the **real** host tailer
  (`internal/hubtail`), and subscribes with the **real** generated Connect
  client as the browser stand-in.
- **What it proves** (maps 1:1 to the QUM-911 ACs, minus the rendered browser):
  live-tail (AC1 data path), the running/idle pill data source (AC3), and
  **zero-gap / zero-dupe reconnect** across both a subscriber network blip and a
  **hubd restart** (AC2), keyed by the durable wire seq.
- **The LLM is mocked, by design.** [`12` §1](12-testability-local-dev.md#1-the-linchpin--mock-the-claude-stream)
  makes "no seam test depends on a real model" the top testability decision. The
  wire-log NDJSON is the real host↔hub interface; the capstone drives the **real
  tailer** over a hand-authored seq'd fixture, exercising the whole uplink →
  fan-out → subscribe path with no `claude` and no TTY.
- **The rendered browser (AC1 visual) is a documented manual walkthrough** (§3):
  a real browser needs TLS because the session cookie is `Secure` (dropped over
  plain `http`). The SPA's reducer/reconnect logic is already unit-covered
  (`web/src/wire/transcript.test.ts`, `web/src/wire/stream.test.ts`).

Files:
- Gate test: `internal/hub/e2e/local_e2e_test.go` (build tag `hub_e2e`).
- Contiguity checker + permanent negative control (untagged, runs on every
  `make validate`): `internal/hub/e2e/contiguity_test.go`.
- Matrix row: `scripts/e2e-tests/hub-e2e.sh`.
- Evidence: [`evidence/qum-911/`](evidence/qum-911/).

---

## 1. The automated gate — scenarios & invariant

The single invariant behind every zero-gap claim is `checkContiguous(seqs,
resumeSeq)`: the **DATA**-frame seqs a subscriber received on one connection
must be strictly ascending by exactly one, with the first equal to
`resumeSeq+1` (heartbeat frames carry seq 0 and are excluded, matching the SPA's
"only DATA advances lastSeq" rule). `TestCheckContiguous_DetectsGap` is the
permanent, untagged negative control that keeps the checker honest (it flags
forward gaps, dupes, non-monotonic runs, and wrong resume floors). Verified to
have teeth: flipping the blip reconnect from `fromSeq=lastSeq` to `fromSeq=0`
makes `network_blip` fail with `first seq = 1, want 6` (a replayed dupe caught by
the checker) — see [`evidence/qum-911/teeth-check.txt`](evidence/qum-911/teeth-check.txt).

`TestHubLocalE2E` subtests:

| subtest | scenario | asserts |
|---|---|---|
| `live_output` | ship seqs 1..5 (incl. `running`@2, `idle`@5), subscribe `from_seq=0` | receives DATA 1..5 contiguous; `/debug/state` reports the stream with `frame_count=5`, `last_seq=5` |
| `pill_state` | same backlog | the `session_state_changed` events arrive in order `[running, idle]` (the exact source the SPA pill reduces) |
| `network_blip` | subscriber caught up at seq 5 drops; frames 6..7 ship while it is gone; it resumes `from_seq=5` | receives exactly DATA 6..7 — contiguous, **no seq ≤ 5 replayed** (zero gap, zero dupe) |
| `hubd_restart` | kill + relaunch hubd on the same port (fresh memStore: backlog **and** token wiped); re-mint token; the **same** tailer (cursor preserved) re-uplinks only seq 8..10; caught-up subscriber reconnects `from_seq=7` | receives exactly DATA 8..10 contiguous across the restart seam; **plus** documents the P1 cold-join gap (below) |

### Restart fidelity & the two honest caveats

1. **Token re-mint after restart (memStore only).** memStore is non-persistent,
   so a hubd restart wipes the token record along with the backlog. The test
   re-mints a fresh token and swaps it into the tailer's push path via a
   mutable-bearer wrapper — **the same `Tailer` instance keeps its cursor**, so
   we genuinely prove "cursor survives restart, only seq > cursor re-ships." A
   real Postgres-backed deploy persists the token (no re-mint needed); the
   wire-seq continuity contract under test is orthogonal to token durability. We
   deliberately do **not** pull in testcontainers/Postgres for P1.
2. **Cold-join-after-restart gap (documented P1 limitation).** The fan-out
   backlog is in-memory regardless of store (durable sink deferred — QUM-909),
   so after a restart the backlog is rebuilt only from what the host re-uplinks,
   and the tailer's cursor does not rewind. A subscriber that was **caught up**
   reconnects with zero gaps (its `from_seq` is at the cursor). A **fresh**
   cold-join (`from_seq=0`) after a restart sees the rebuilt tail starting at
   `cursor+1`, not seq 1 — the capstone asserts this explicitly so the
   limitation is a tracked fact, not a surprise. A durable replay sink (later
   phase) closes it.

### Determinism

No `claude`, no tmux, no TTY, no network beyond loopback. Readiness is a
`/healthz` poll; the backlog is fully shipped before each subscribe, so replay
is immediate (well under the 20 s heartbeat cadence); the hubd restart reuses a
pre-reserved fixed port and all Connect clients disable keep-alives so no stale
connection can survive the restart.

Run it:

```bash
make test-hub-e2e
# or via the matrix driver:
make test-e2e-matrix-hub-e2e
```

Captured transcript: [`evidence/qum-911/go-test.txt`](evidence/qum-911/go-test.txt).

---

## 2. Operator note — finding (host_id, run_id, session_id) (QUM-923)

`SubscribeWireLog` needs the full `(host_id, run_id, session_id)` triple, and
there is no session-list RPC yet (QUM-923). Pull the triple from the gated
`/debug/state` endpoint (`SPRAWL_HUB_DEBUG_ENDPOINT=1`), whose `streams[]`
entries carry all three plus `frame_count` / `last_seq`, or from the host
`sprawl enter` startup log (`[enter] hub: registered with … as <host_id>`). Do
not block on a UI session picker.

---

## 3. Manual browser walkthrough (AC1 — rendered, real browser)

The rendered pane-of-glass needs a real browser, which needs TLS: the hub
session cookie is `Secure`, so it is silently dropped over plain `http://` (see
`web/README.md`, "Secure-cookie caveat"). This is out of P1 automation scope
(Playwright is deferred — [`12` §4.3](12-testability-local-dev.md#43-seam-3--add-the-real-web-headless-then-playwright)).
Steps to reproduce by hand:

1. **Build the SPA into the embed dir** (needs node; the committed `web/dist` is
   already embedded, rebuild only if you changed `web/`):
   ```bash
   make hub-web        # npm ci + buf generate + vite build -> cmd/hubd/web/dist
   go build -o /tmp/hubd ./cmd/hubd
   ```
2. **Run hubd with browser login enabled, behind TLS.** Terminate TLS with a
   local reverse proxy (e.g. `caddy`, `stunnel`, or `mkcert` + a tiny TLS proxy)
   in front of hubd, or run hubd on `https://localhost` via such a proxy:
   ```bash
   SPRAWL_HUB_DEBUG_ENDPOINT=1 \
   SPRAWL_HUB_LOGIN_TOKEN='choose-a-dev-login-token' \
   SPRAWL_HUB_COOKIE_KEY="$(openssl rand -base64 32)" \
   SPRAWL_HUB_SECRET_URL="base64key://$(openssl rand -base64 32)" \
   /tmp/hubd -addr 127.0.0.1:8080
   # then front 127.0.0.1:8080 with TLS on, say, https://localhost:8443
   ```
3. **Log in.** Open `https://localhost:8443/login`, enter the login token, land
   on `/app/`.
4. **Run a real hub-connected session** in a worktree, in another terminal:
   ```bash
   sprawl hub url set http://127.0.0.1:8080     # host uplink is cleartext h2c; browser is the TLS side
   printf '%s' '<a bearer minted from the Tokens view>' | sprawl hub token set
   sprawl enter                                  # registers + tails its wire log
   ```
   (Mint the host bearer from the SPA "Tokens" view, or reuse the automated
   flow's `/login` → `CreateHostToken`.)
5. **Observe** in the browser: the session's live output streams read-only, and
   the running/idle status pill flips as the session works then goes idle.
6. **Reconnect proofs by hand:** toggle wifi / airplane mode (or kill the proxy
   briefly) and watch the tail resume with no gap; restart hubd and watch the
   browser reconnect and the tail rebuild as the host re-uplinks.

**Evidence format:** capture a screenshot (or short asciinema) of the browser
showing live output + the pill for AC1/AC3, and the `go test -v` transcript for
the automated AC1(data)/AC2/AC3 proofs. Store non-secret artifacts under
[`evidence/qum-911/`](evidence/qum-911/). **Public-repo hygiene:** commit only
synthetic fixtures — never a real session transcript, host id, or token.

---

## Open Questions

- **Durable replay sink** (closes the cold-join-after-restart gap, §1 caveat 2):
  which store shape, and does it fold into the existing `Store` interface or a
  new wire-frame table? Tracked as later-phase work under QUM-906.
- **Session-list RPC** (QUM-923): once it lands, the manual `/debug/state`
  triple lookup (§2) and the test's fixed IDs can key off it instead.
- **Playwright `hub-fullstack` row** ([`12` §7](12-testability-local-dev.md#7-new-e2e-matrix-rows-touched-files--row)):
  when added, it supersedes the manual browser walkthrough (§3) for AC1's
  rendered path.
