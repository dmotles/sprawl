# 11 — Frontend Stack (OPEN RESEARCH → recommendation)

*Which framework/library to build the hub's browser SPA with. Evaluates real
options and recommends one.*

See also: [`00-overview.md`](00-overview.md) · [`01-architecture.md`](01-architecture.md) ·
[`02-components.md`](02-components.md) (§3 frontend) · [`03-api-surfaces.md`](03-api-surfaces.md) · [index](README.md)

---

## 0. TL;DR

- **Recommendation: React 19 + Vite + `@connectrpc/connect-web` + `@connectrpc/connect-query`.**
  Not because it's the "best" framework in the abstract, but because it is the
  **lowest-risk, lowest-familiarity-cost** path for a Go-first maintainer: the
  Connect ecosystem's most mature browser bindings, the deepest pool of
  copy-paste/AI-assisted answers, and a trivially `go:embed`-able Vite static
  build.
- The app is **small** (a live event-log viewer + an instance switcher + an
  OIDC login). At this size, framework *runtime* differences barely register;
  **ecosystem + streaming ergonomics + build simplicity** dominate.
- The **live event stream is NOT consumed via TanStack Query** — it's a bare
  `connect-web` client + `for await` loop appending into an external store
  (see §5). TanStack Query (`connect-query`) is only for the *unary* RPCs
  (`ListInstances`, `Session`, `FetchSnapshot`, `SubmitInput`).
- **Strong runner-up: SolidJS.** If mobile bundle size / append-throughput ever
  becomes a real constraint, Solid's fine-grained reactivity is the "right way"
  for an append-only log — at the cost of a smaller ecosystem.

---

## 1. What the SPA actually has to do

From [`02` §3](02-components.md) and [`03` §2](03-api-surfaces.md), the SPA is a
**pure static event-log consumer + input producer**. Concretely:

1. **Consume a Connect/protobuf server-stream** (`SubscribeInstance`) and follow
   the **one rule**: replay from `last_seq` → else `FetchSnapshot` → then
   live-tail ([`01` §2](01-architecture.md)). This is an **append-only log** feeding
   a scrollback view.
2. **OIDC PKCE in-browser** — relying-party login against a deploy-parameter IdP
   ([`04`](README.md)); no client secret in the SPA.
3. **Single pane of glass** — `ListInstances` + an instance switcher; possibly
   tail several instances at once (open question in [`02`](02-components.md)).
4. **Ship as pure static assets** — no SSR — that `go:embed` into the hub binary
   (§1.8 of [`02`](02-components.md)) *or* serve from a CDN.

**Load-bearing consequences for framework choice:**

- **No SSR, ever.** SSR-first frameworks (Next.js, SvelteKit's default mode) are
  *disqualified in their default configuration* — they must be coerced into
  static-SPA mode, which is friction, not a feature.
- **Browser can only server-stream + unary** — never client-stream or bidi
  (verified §4). Every candidate hits the identical Connect transport, so
  *streaming-transport support is not a differentiator* — they all use the same
  `@connectrpc/connect-web` under the hood. What *does* differ is how gracefully
  each framework's state model absorbs a high-frequency append stream (§5).

---

## 2. Options table

Bundle sizes are framework runtime, gzipped, approximate (2026). "Connect fit"
= quality of first-class Connect/TanStack-Query bindings. "Familiarity/AI cost"
= how easily a non-frontend-specialist (with LLM help) ships correctly.

| Option | Runtime (gz) | Static-SPA fit | Connect fit | Append-log state fit | Ecosystem / maturity | Familiarity + AI cost |
|---|---|---|---|---|---|---|
| **React 19 + Vite** | ~40 KB | ✅ native (Vite SPA) | ✅✅ best — `connect-query` React-first | ⚠️ needs external store + virtualization to avoid re-render churn | ✅✅ largest | ✅✅ lowest — most examples/training data |
| **Preact + Vite** | ~4 KB | ✅ native | ✅ works (React-compatible via `preact/compat`) | ⚠️ same as React (same model) | ✅ good (React-compat) | ✅ low (React knowledge transfers) |
| **SolidJS + Vite** | ~7–18 KB | ✅ native | ✅ `connect-query` has a Solid adapter; TanStack Solid Query mature | ✅✅ fine-grained signals map 1:1 to append-only updates | ⚠️ smaller but mature | ⚠️ medium — fewer answers, JSX-but-not-React gotchas |
| **SvelteKit (adapter-static)** | ~10 KB | ⚠️ must force `ssr=false` + fallback page; `go:embed` CSS-dup gotcha | ✅ Svelte Query + connect-web | ✅ runes/stores fit streaming well | ✅ good | ⚠️ medium |
| **Vanilla TS + connect-web** | ~0 (just the client) | ✅ native | ✅ raw client | ❌ hand-roll all DOM/reconnect/render | n/a | ❌ high — you rebuild a framework badly |

*Bundle-size sources: React core ~40 KB gz; Preact ~3–4 KB; Solid ~7–18 KB
depending on features — all dwarfed by the app + protobuf message code, so for
this app the practical delta is tens of KB, not a UX cliff even on mobile.*

---

## 3. The choice, dimension by dimension

### 3.1 Static-SPA + `go:embed` (constraint 4)

- **React / Preact / Solid on Vite** are **natively SPAs** — `vite build` emits a
  hashed `dist/` of pure static assets; point `//go:embed dist` at it and serve
  from the Connect listener ([`02` §1.8](02-components.md)). Zero coercion.
- **SvelteKit** is SSR-first; SPA mode requires `export const ssr = false` +
  `adapter-static` with a fallback page, and a **known gotcha**: adapter-static
  can still emit server/SSR CSS into the build, *doubling* the CSS bytes embedded
  into a Go binary (sveltejs/kit#9161). Workable, but it's swimming upstream.
- **Verdict:** Vite-based SPAs win on this constraint; SvelteKit pays a
  "make-it-static" tax.

### 3.2 Connect/protobuf client support (constraint 1)

All candidates consume the **same** `@connectrpc/connect-web` transport, so raw
capability is equal. The differentiator is the **TanStack Query binding**
(`@connectrpc/connect-query`), which makes the *unary* RPCs (loading, caching,
mutations) ergonomic:

- **React:** `connect-query` is React-first and the most battle-tested.
- **Solid / Svelte / Vue:** TanStack Query has framework adapters; connect-query
  interop is good but less trodden.
- **Vanilla:** you get the raw client and hand-roll caching/loading state.

**Simplest way vs. right way (query layer):** Simplest = call the generated
client directly and hold results in component state. Right = `connect-query` +
TanStack Query for cache/loading/retry on the unary RPCs. **Recommendation:**
use `connect-query` for the handful of unary calls (it's cheap and removes
boilerplate) — but see §5: **do not** try to model the live stream through it.

### 3.3 Append-only event-log ergonomics (the real differentiator)

The scrollback is a **monotonically growing list** fed by a high-frequency
stream. Two rendering realities:

- **Fine-grained frameworks (Solid, Svelte):** a signal/store append updates
  exactly the DOM nodes that changed. Appending event N+1 doesn't re-run the
  whole view. This is the *natural* shape for an event log — the "right way."
- **VDOM frameworks (React, Preact):** naïvely holding the log in `useState`
  re-renders the whole list on every append. **Mitigation is well-known and
  mandatory:** keep the log in an **external store** (Zustand, or a tiny
  `useSyncExternalStore` store) and **virtualize** the list (`@tanstack/react-virtual`)
  so only visible rows render. With that, React handles a fast stream fine.

**Simplest way vs. right way (state for the stream):**
- **Simplest:** `useState([...log, evt])` on each event. Cost: re-render storms,
  jank on mobile once the log is long.
- **Right:** external append-only store + windowed virtualization + a bounded
  in-memory ring (drop-oldest above a high-water mark, mirroring the host buffer
  policy in [`01` §3](01-architecture.md)). Cost: two small libraries + a store module.
- **Recommendation:** **the right way regardless of framework** — it's a handful
  of lines and it's the one place this app can actually get slow. Solid/Svelte
  give you most of it for free; React needs the store+virtualization, which is a
  solved, boring pattern.

### 3.4 OIDC PKCE in-browser (constraint 2)

Framework-agnostic — handled by a library, not the framework. Use a maintained
RP library that does **Authorization Code + PKCE** with no client secret (e.g.
`oidc-client-ts`), IdP endpoints injected at runtime (deploy parameter, never
hardcoded — [`04`](README.md), and public-repo hygiene). Every candidate
integrates this identically. **Not a differentiator.**

### 3.5 Familiarity / AI-assist cost (explicit per standing rules)

This repo is Go + a bespoke TUI; the frontend is not the maintainer's home turf.
That makes **"how easily can one person + an LLM ship this correctly"** a
first-class criterion, not an afterthought:

- **React** has by far the most training data, examples, and Connect-specific
  docs → the highest odds of AI-assisted correctness on the first pass.
- **Solid/Svelte** are excellent but you'll hit "the answer is React-shaped, now
  translate it" friction more often.

---

## 4. Streaming reality check (verified, 2026)

Confirms [`03` §2/§4](03-api-surfaces.md) at the library level so the frontend
doc doesn't re-litigate it:

- **`connect-web` supports *server* streaming in the browser** over `fetch` —
  consumed as a `for await (const msg of client.subscribe(...))` loop. This is
  exactly what `SubscribeInstance` needs.
- **Browsers cannot client-stream or bidi** — `fetch` request-body streaming
  (`duplex: 'full'`) is still not shipped across stable browsers, so connect-web
  deliberately **omits** client-stream/bidi. This is *fine*: our uplink from the
  browser is unary (`SubmitInput`) by design ([`03` §4.5](03-api-surfaces.md)).
- **Reconnect (the one rule) is app-level, not transport-level.** When the
  server-stream cuts (mobile roam, LB idle-timeout — [`03` §4.2](03-api-surfaces.md)),
  the `for await` loop ends; we re-open with `from_seq` and replay-or-snapshot.
  This logic is identical across all frameworks — it lives in a plain TS module,
  not in components.

**Implication:** streaming-transport capability is *table stakes met equally by
all candidates*. The choice rides on §3.1/§3.3/§3.5, not on transport.

---

## 5. Recommended shape (if React is chosen)

```
┌ transport module (framework-agnostic, plain TS) ──────────────┐
│  connect-web client (protobuf, generated by buf → connect-es)  │
│  streamController: for-await SubscribeInstance                 │
│    → append into store; on end → reconnect(from_seq) [one rule]│
│    → gap? → FetchSnapshot → reset store → resume tail          │
└───────────────────────────────────────────────────────────────┘
        │ appends                          ▲ unary calls
        ▼                                  │
┌ event store (Zustand / useSyncExternalStore) ┐   ┌ connect-query (TanStack) ┐
│  append-only ring, bounded high-water         │   │ ListInstances / Session  │
│  selectors: tail window, per-instance cursor  │   │ FetchSnapshot / SubmitInput│
└───────────────────────────────────────────────┘   └──────────────────────────┘
        │                                            │
        ▼                                            ▼
┌ React view: virtualized log (@tanstack/react-virtual)          ┐
│  + instance switcher (single pane) + OIDC-gated shell          │
└─────────────────────────────────────────────────────────────────┘
```

- **The stream never touches TanStack Query** — Query is request/response
  cached-data machinery; an infinite append stream is a poor fit. Bare client +
  store is simpler and correct.
- **`connect-query` handles only unary RPCs** — loading/caching/mutation for the
  switcher, snapshot fetch, and input submit.
- **Multi-instance (single pane):** start as **N independent one-rule stream
  controllers** (one per tailed instance) writing into a keyed store — matches
  "just another consumer" and defers the hub-side multiplexing question in
  [`02`](02-components.md) / [`03`](03-api-surfaces.md). YAGNI on a multiplexed
  stream until N gets large.

---

## 6. Simplest way vs. right way (framework-level summary)

- **Simplest way:** **React + Vite.** Most examples, best Connect bindings,
  trivial static build, lowest AI-assist friction. Cost: you must add an external
  store + list virtualization for the stream (a boring, solved pattern), and you
  ship ~40 KB of runtime you don't strictly need.
- **Right way (for an append-only realtime log specifically):** **SolidJS.**
  Fine-grained reactivity fits the stream natively, smallest bundle for mobile.
  Cost: smaller ecosystem, more "translate the React answer" moments, medium
  familiarity tax on a Go-first maintainer.
- **Recommendation:** **React + Vite for v1.** The app is small enough that
  Solid's runtime edge is invisible here, while React's ecosystem + familiarity
  edge is felt on *every* line written. Keep the transport + store as
  **framework-agnostic plain-TS modules** (§5) so that if mobile perf ever
  demands it, swapping the *view layer* to Solid is a contained rewrite, not a
  rebuild. **KISS wins now; the escape hatch is cheap to preserve.**

---

## Open Questions

- **Preact-as-React-drop-in?** Preact via `preact/compat` gets ~4 KB runtime with
  near-React DX and keeps `connect-query`. Is the compat-layer risk (occasional
  library incompatibilities) worth ~36 KB, given the total bundle is dominated by
  app + protobuf code anyway?
- **Multi-instance fan-out:** N independent stream controllers in the SPA (simple,
  matches "just another consumer") vs. a hub-side multiplexed stream (fewer
  connections, more hub code) — ties to the open question in
  [`02`](02-components.md) / [`03`](03-api-surfaces.md). At what N does client-side
  fan-out stop scaling?
- **Store choice:** Zustand vs. a hand-rolled `useSyncExternalStore` module —
  is one dependency worth avoiding, or does Zustand's ergonomics pay for itself?
- **Log retention in-browser:** what high-water mark bounds the in-memory event
  ring on a phone before drop-oldest, and should the UI signal truncation the way
  the host buffer does ([`01` §3](01-architecture.md))?
- **OIDC library:** `oidc-client-ts` vs. a lighter hand-rolled PKCE flow — how
  much of the library do we actually use, and does its size/complexity justify it
  over ~100 lines of PKCE? (Settle alongside [`04`](README.md).)
- **CDN vs. `go:embed` default:** embed for v1 simplicity is assumed here; does
  any target deploy ([`08`](README.md)) actually need the CDN split, or is that
  purely a future option?
- **Do we need a router at all?** Single-pane + switcher may be one route with
  client state; adding a router (TanStack Router / React Router) may be YAGNI.
