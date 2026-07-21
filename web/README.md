# hub SPA (`web/`)

The hub's browser single-page app. **Minimal by design** (QUM-878 / Hub P0-4):
a login form plus an authenticated view that lists instances via
`ListInstances`. No router, no state libraries, no live-tail — those are Phase 1
(see [`docs/design/hub/11-frontend-stack.md`](../docs/design/hub/11-frontend-stack.md)).

Stack: **React 19 + Vite + `@connectrpc/connect-web`** (connect-es v1).

## How auth works (docs 04 §1/§6)

1. On load the SPA calls `ListInstances`. The browser's **HttpOnly session
   cookie** (if present) rides the fetch automatically — the transport uses a
   same-origin base (`baseUrl: "/"`), so there is **no hardcoded hub endpoint**.
2. If the call returns `Unauthenticated`, the SPA shows the **login form**. The
   form does a native `POST /login` with the token in the request **body** (never
   the URL). hubd constant-time-compares it against `SPRAWL_HUB_LOGIN_TOKEN`,
   mints a server-side `login_sessions` record, sets the signed HttpOnly /
   Secure / SameSite=Strict cookie, and redirects to `/app/`.
3. The SPA then reloads authenticated. **It never stores or re-sends the login
   token** — the cookie is its only credential. "Log out" does `POST /logout`.

## Build pipeline

```
make hub-web
```

which runs, in order:

1. `cd web && npm ci` — install deps (incl. the `protoc-gen-es` /
   `protoc-gen-connect-es` codegen plugins into `web/node_modules`).
2. `buf generate --template buf.gen.web.yaml` (with `web/node_modules/.bin` on
   PATH) — regenerate the connect-es TypeScript bindings into
   [`web/gen/hub/v1/`](gen/hub/v1) (`hub_pb.ts` messages + `hub_connect.ts`
   client). This is the SPA's wire contract; it mirrors the Go bindings.
3. `cd web && npm run build` — `tsc -b && vite build`, emitting hashed static
   assets into **`cmd/hubd/web/dist`**, which `cmd/hubd/main.go` `//go:embed`s
   and serves under `/app/` (with a `base: "/app/"` so asset URLs resolve there;
   `GET /login` serves the same `index.html`).

## What is committed vs. ignored

- **Committed:** the generated `web/gen/**` bindings and the built
  `cmd/hubd/web/dist/**`. This is deliberate — `go build`, `make build`, and
  `make validate` must work **without a node toolchain**. `make validate` never
  runs node; `make hub-web` is the only node-dependent target and is opt-in.
- **Ignored:** `web/node_modules/` (see repo `.gitignore`).

**When you change the SPA source or the proto**, re-run `make hub-web` and commit
the regenerated `web/gen` + `cmd/hubd/web/dist` alongside your source change.

## Public-repo hygiene

No secrets, tokens, or hub endpoints in the source or the built bundle — the
transport is same-origin and the login token is server-side only. `make validate`
runs the whole-tree leak scan over the committed `dist`.

## Local dev

`cd web && npm run dev` runs Vite's dev server, but the RPCs + `/login` need a
running `hubd`. For an end-to-end check, prefer `make hub-web` then run the
`hubd` binary (see the QUM-878 e2e notes) so the embedded SPA is exercised as
shipped.

> **Local HTTPS caveat.** The session cookie is `Secure`, so a browser silently
> drops it over plain `http://localhost` — after login you'd 303 to `/app/` but
> stay unauthenticated. Front `hubd` with a TLS terminator (or a `https://`
> dev proxy) when driving the flow in a real browser. Scripted `curl` checks can
> replay the cookie explicitly with `-b "sprawl_hub_session=<value>"`, which
> bypasses the `Secure` gate.
