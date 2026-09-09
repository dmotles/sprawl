# Read-only hub UI (`webui/`)

The read-only web UI over the hub's Postgres event log (QUM-1349). Distinct from
[`web/`](../web/README.md), which is the frozen hub wire-log SPA — same stack,
different app, no shared code.

Stack: **React 19 + Vite 6 + TypeScript + react-router**. No Connect, no proto,
no auth: the UI talks plain JSON REST to a **same-origin `/api`**.

## Slice status

Slice B (this one) ships the **shell**: design system, routing, responsive
layout, and empty states for all six views. No data is fetched yet — slice C
wires the views to the read API.

## Views

Goals · Workflows · Fleet · Event ledger · Cost & usage · Inbox. The list lives
in [`src/views/index.tsx`](src/views/index.tsx) and is the single source of
truth for the nav, the routes, and the phone topbar title.

Two views carry a permanent caveat, because the schema cannot support the
obvious reading:

- **Fleet** is *advisory*. `agent_sessions` is a projection; each host's local
  agent state is the authority.
- **Cost & usage** numbers are *lower bounds*. Cost lives only in the payloads
  of spillable events, so spend recorded during a database outage never reaches
  the log.

## Design system

**Dark mode only — no light theme and no toggle.** Tokens live in
[`src/styles/tokens.css`](src/styles/tokens.css); `app.css` uses tokens and no
raw hex, and `src/styles/tokens.test.ts` enforces both of those properties.

Colour comes from the `/dataviz` reference palette's **dark column, unmodified**.
Those eight categorical steps were validated as a set against the `#1a1a19`
surface (`validate_palette.js --mode dark`: all six checks pass; worst adjacent
CVD ΔE 8.4, worst adjacent normal-vision ΔE 19.3). Re-stepping any of them by
eye voids that result. Status colours are reserved and never reused as a series.

## No baked-in endpoint

The bundle must contain no absolute API URL, so one image runs against any
deployment. `npm run build && npm run check:endpoints` enforces it; the check
also runs inside the image build, so a violation fails the Docker build. It
fails loudly on a missing or JS-less `dist/` rather than reporting a vacuous
pass. Watched failing: baking `https://api.example.invalid/api` into a rendered
string made it exit 1 naming the URL and the file; the clean tree exits 0.

## Local development

```sh
cd webui
npm install
npm run dev     # http://localhost:5173, proxying /api to 127.0.0.1:8081
npm test        # vitest component suite
npm run build   # -> dist/ (gitignored; built inside the image)
```

`npm run dev`'s proxy target is dev-server config and is never bundled.

## Containers

`deploy/webui/Dockerfile.web` builds the SPA with node and serves `dist/` from
`nginx-unprivileged`, reverse-proxying `/api` to the read API. The upstream is
injected at run time via `SPRAWL_UIAPI_UPSTREAM` (nginx-unprivileged's envsubst
entrypoint renders `nginx.conf.template`) — nothing about a deployment is baked
into the image.

That proxy is **architecture, not a dev convenience**: per QUM-1350 the read API
is never exposed directly, so this container is the only route a browser has to
it — and being same-origin is exactly what lets the bundle carry no endpoint.

The deploy target is **linux/amd64** (QUM-1350) while dev hosts here are arm64,
so build it explicitly:

```sh
docker buildx build --platform linux/amd64 -f deploy/webui/Dockerfile.web .
```

The node stage is pinned to `$BUILDPLATFORM` so it runs natively — its output is
platform-independent JS — and only the nginx runtime stage is cross-built.

## Committed vs ignored

`webui/node_modules/`, `webui/dist/` and `*.tsbuildinfo` are gitignored. Unlike
`web/`, this app's `dist` is **not** committed: it is never `go:embed`ded, so
`make validate` stays node-free without it.

Vitest is deliberately **not** part of `make validate` for the same reason —
run it explicitly with `cd webui && npm test`.
