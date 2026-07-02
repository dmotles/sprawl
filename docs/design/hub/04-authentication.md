# 04 — Authentication

*The hub's two trust checks, MVP shape: browsers via a login page that trades a
configured bearer token for a session cookie; hosts via hashed bearer tokens.
Conforms to [`01-architecture.md`](01-architecture.md); this is the detail behind
[`02` §1.4](02-components.md) and the auth notes in [`03`](03-api-surfaces.md).*

See also: [`00-overview.md`](00-overview.md) · [`01-architecture.md`](01-architecture.md) ·
[`02-components.md`](02-components.md) · [`03-api-surfaces.md`](03-api-surfaces.md) ·
[index](README.md)

---

## 0. Scope — authN only, single-user MVP

This doc covers **authentication** — *who is this caller, and are they allowed
in at all?* It deliberately stops there.

The hub is a **cloud companion to the local binary**: it relays the live stream
to a browser and durably persists memory / transcripts / attachments. It is
**single-user, not multi-tenant** in v1. That single fact drives every decision
here — most importantly, **there is no OIDC in v1** (see §1).

- **In scope:** browser login (configured bearer token → session cookie), host
  login (hashed bearer token), host-token lifecycle (create / rotate / revoke),
  where the host token lives on the host, and the browser session cookie.
- **Out of scope — defer to the security-privacy doc:** authorization, multi-user
  / tenant isolation, per-agent access scoping, threat model, and the
  content-trust model. Authentication answers *"come in?"*; authorization answers
  *"do what?"*. Keeping them apart keeps this doc small.
- **Also distinct — write authority is NOT auth.** Being authenticated lets you
  *hold a connection*; it does **not** grant the right to *write*. Write
  authority is the active-host marker's job ([`01` §4](01-architecture.md#4-identity-lease--fencing-conceptual--detail-in-doc-10--09),
  [`02` §1.5](02-components.md)). Auth gates the connection; the marker gates the
  write. (In this MVP the marker is trivial — a single active-host flag, no fence
  tokens — but it is still a separate concern from auth.)

```
        ┌─────────── two callers, two trust checks, one component (02 §1.4) ───────────┐
        │                                                                              │
   browser ──login page: present configured bearer token──▶ hub ──▶ signed session cookie
   host    ──bearer token (hashed-verify)────────────────▶ hub ──▶ host connection up
        │                                                                              │
        └────── both then subject to the active-host marker for WRITES (not here) ─────┘
```

---

## 1. Browsers → login page + configured bearer token

There is **no OIDC in v1.** The hub is single-user, so we do not stand up an
OIDC relying party, PKCE, an identity provider as a deploy parameter, or a user
allowlist. Instead the browser authenticates with **one configured bearer
token** — a secret set at deploy time — presented once through a tiny login page.

### 1.1 The login token is a deploy secret — never hardcoded

The browser-login bearer token is **configuration**, resolved at startup from
the secrets path ([`02` §1.7](02-components.md)), never compiled in. There is
**no default and no baked-in secret** (public-repo hygiene, mirrors the "no
default hub endpoint" rule in [`01` §3](01-architecture.md#3-connected-vs-disconnected)).

| Parameter | Source | Notes |
|---|---|---|
| `HUB_LOGIN_TOKEN` | **secrets path** (`gocloud.dev/secrets`, [`02` §1.7](02-components.md)) | The single deploy secret the operator types into the login page. Never committed, never in plaintext env where avoidable. |
| session cookie signing key | **secrets path** | Signs/seals the session cookie the login mints (§6). |

Because there is exactly one user (the operator), a single shared login secret is
sufficient. Verification is a **constant-time compare** of the presented value
against the configured `HUB_LOGIN_TOKEN` — no per-row lookup, no IdP round-trip.

### 1.2 Flow — present token, get a cookie

```
browser                         hub
  │  GET /login                  │  serve a tiny login page (one field: the token)
  │◀─────────────────────────────│
  │  POST /login  {token}         │
  │─────────────────────────────▶│  constant-time compare vs HUB_LOGIN_TOKEN
  │                              │  match ⇒ create server-side session record
  │  Set-Cookie: session          │  mint signed httpOnly session cookie (§6)
  │◀─────────────────────────────│  mismatch ⇒ 401, plain "not authorized" page, no cookie
```

- The login page is served over **HTTPS only**; the token is sent in the POST
  **body**, never on the URL/query string (§6, token-in-URL hygiene).
- On success the hub records a session and sets the cookie; the browser holds
  **only** that cookie thereafter — it never re-sends the login token.
- On mismatch: render a plain "not authorized" page and mint **no** session.

### 1.3 Simplest way vs. right way — browser auth

- **Simplest (chosen):** one configured bearer token, presented via a login
  page, traded for a signed httpOnly session cookie. Cost: a single shared
  secret and no per-user identity — acceptable *because the hub is single-user*.
  It keeps the browser's steady-state credential a server-minted, revocable,
  XSS-resistant cookie rather than a long-lived bearer.
- **Right (deferred to multi-tenant):** full OIDC relying party with a
  bring-your-own IdP, PKCE, and a user allowlist — real per-user identity and
  standards-based revocation.
- **Recommendation: the login-token model now.** OIDC's entire value is *multi-
  user* identity; with one user it is pure overhead (an IdP must exist, be
  configured, and a callback + PKCE + allowlist maintained). The login page +
  session cookie gives us the one property that still matters at single-user
  scale — a short-lived, server-side-revocable browser credential — at a fraction
  of the cost. See §3 for exactly what OIDC buys back when we go multi-tenant.

---

## 2. Hosts → hashed bearer tokens

Each host (`sprawl enter`) authenticates its persistent dial-out connection
([`01` §1](01-architecture.md), [`03` §1](03-api-surfaces.md)) with a **bearer
token** sent on every host↔hub call. The hub verifies it against **hashed**
tokens in Postgres. This is a deliberately full-featured lifecycle even at
single-user scale, because per-host **rotate/revoke** is cheap and worth keeping.

### 2.1 Token shape + hashing

- **Presented token:** an opaque high-entropy string with a readable prefix and
  an embedded lookup id, e.g. `sprawl_hub_<tokenid>_<secret>`. The prefix aids
  secret-scanning tools; the `<tokenid>` gives an **O(1) indexed row lookup** so
  we don't hash-compare against every row.
- **At rest:** we store **only a hash of the `<secret>`**, never the token
  itself. Use a slow password hash (**argon2id**, or bcrypt) with a per-deploy
  **pepper** resolved from the secrets path ([`02` §1.7](02-components.md)). A DB
  dump alone cannot reconstruct usable tokens.
- **Verify path:** parse prefix → look up row by `<tokenid>` → constant-time
  compare `hash(secret, pepper)` against the stored hash → check not-revoked /
  not-expired. Bind the row to a `host_id` on first successful use.

> **Why not a JWT / signed token?** A signed self-describing token can't be
> revoked before expiry without a denylist — which is just a DB round-trip we'd
> be adding back anyway. An opaque hashed token is simpler to reason about and
> revocation is a single `UPDATE`. KISS.

### 2.2 Conceptual table (schema detail → [`07`](README.md))

```
host_token
  id            -- the <tokenid> in the presented string (indexed lookup)
  hash          -- argon2id(secret, pepper); NEVER the token
  label         -- human name ("laptop repo-1", "ci")
  user_id       -- always the single MVP user; present now so multi-user adds no migration (§3)
  host_id       -- bound on first use; nil until then
  created_at
  last_used_at  -- for stale-token hygiene
  expires_at    -- optional TTL
  revoked_at    -- non-null ⇒ rejected
```

### 2.3 Simplest way vs. right way — host auth

- **Simplest:** one shared secret for all hosts. Cost: can't revoke a single
  compromised host without rotating everyone; no per-host attribution in logs;
  and a shared secret in a public-repo ops story is a footgun.
- **Right (chosen):** per-host tokens, hashed at rest, individually revocable.
  Cost: a small table + a token-vending UI + lifecycle.
- **Recommendation: per-host hashed tokens now** (echoing [`02` §1.4](02-components.md)).
  Per-host revocation and attribution are cheap and useful even for a single
  operator with several machines; the cost is one table plus a few unary RPCs we
  already listed in [`03` §2](03-api-surfaces.md)
  (`CreateHostToken` / `ListHostTokens` / `RevokeHostToken`).

---

## 3. Single-user model — and what returns at multi-tenant

Authentication proves the caller holds a valid credential. In v1 that is the
**whole** gate: there is exactly one user, so there is **no allowlist and no
per-user isolation enforcement**. Everything authenticated belongs to the one
operator.

- **One `user_id`, always the same value.** Both the session record (§6) and the
  `host_token` table (§2.2) carry a `user_id` column that always holds the single
  MVP user's id. It is intentionally present now so the multi-user future is a
  *data* change, not a *schema* migration.
- **No allowlist.** With one user there is nobody to allow or deny beyond
  "presented the right credential." The coarse front-door check collapses into
  the credential check itself.
- **No per-user isolation.** There is only one user's data, so tenant/project
  isolation enforcement is deferred entirely to the security-privacy doc.

**What returns when we go multi-tenant (deferred, not designed here):**

- **OIDC** relying-party browser login (real per-user identity, PKCE, IdP as a
  deploy parameter) replaces the single configured login token of §1.
- **An allowlist** (or roles/groups synced from the IdP) gates *which* identities
  may open a session.
- **Per-user isolation** enforcement scopes what each user can see/drive.
- The **`user_id` column is already there** (§2.2, §6), so none of the above
  forces a schema migration — multi-user becomes "write more than one `user_id`"
  plus the authZ design in the security-privacy doc.

---

## 4. Host-token lifecycle + token vending

The management UI is served by the SPA (behind the login of §1 — only a
logged-in browser can mint host tokens), backed by the `CreateHostToken` /
`ListHostTokens` / `RevokeHostToken` RPCs from [`03` §2](03-api-surfaces.md).

### 4.1 Create (vend)

```
browser (logged in) ──CreateHostToken{label}──▶ hub
                                                 │  generate secret (CSPRNG)
                                                 │  token = sprawl_hub_<id>_<secret>
                                                 │  store argon2id(secret,pepper) + label + user_id
        show token ONCE  ◀───────────────────────┘  return FULL token in THIS response only
```

- **Show once, never again.** The full token is returned in the create response
  and displayed once. The hub keeps only the hash, so it *cannot* redisplay it.
  The UI copy says so explicitly and offers copy-to-clipboard.
- Only the hash is persisted; the plaintext exists in the response body and RAM,
  never in the DB or logs.

### 4.2 Rotate

Rotation = **create new + revoke old**, with a grace window so a host isn't cut
off mid-flight:

```
1. CreateHostToken{label:"laptop (rotated)"}  → new token vended, shown once
2. operator installs new token on the host (§5); host reconnects with it
3. RevokeHostToken{old_id}                    → old token dead
```

We do **not** build in-place secret mutation (YAGNI) — create+revoke reuses
existing verbs and leaves an audit trail of both rows.

### 4.3 Revoke

`RevokeHostToken{id}` sets `revoked_at`. Verification (§2.1) rejects revoked
tokens on the **next** call. Because auth is checked per-call on the uplink and
at (re)connect on the downlink, a revoked host is locked out within one call/
reconnect — no long-lived grant to wait out. (Browser session cookies are a
separate lifetime, §6.)

### 4.4 Simplest way vs. right way — lifecycle

- **Simplest:** create-only; to "revoke," delete the row and rotate everything.
  Cost: no rotation grace, no audit trail, blunt.
- **Right (chosen):** create (show-once) + revoke (`revoked_at`) +
  rotate-as-create+revoke, all per-token. Cost: three small unary RPCs + a
  minimal UI screen.
- **Recommendation: the three-verb lifecycle**, because per-host revocation is
  the entire justification for host tokens over a shared secret (§2.3). Rotation
  reuses create+revoke, so it costs no new machinery.

---

## 5. Where the host token lives on the host

The host reads its token via the **secrets path**, resolved the same way as
other host secrets, and **never** from a value passed on the command line.

- **Storage:** a file under the host's sprawl config dir (e.g.
  `.sprawl/secrets/hub-token` or a `gocloud.dev/secrets` reference), mode `0600`,
  owned by the operator. **Never committed** — the path is gitignored, mirroring
  the existing `.env` handling in `CLAUDE.md`.
- **Resolution precedence** (highest first), matching [`02` §2.6](02-components.md):
  the secrets-path reference, then env (`SPRAWL_HUB_TOKEN`), then config. Absent ⇒
  the hub client stays inert (disconnected default).
- **NEVER a value on a CLI flag.** A token passed as `--hub-token=sprawl_hub_…`
  leaks into the **process table** (`ps auxf`), shell history, and — critically
  in this codebase — the **incident snapshot** bundle (`Ctrl+\`, `ps auxf` +
  `/proc/<pid>/status`, see `CLAUDE.md` QUM-728). The flag layer may name a
  *path/reference* (`--hub-token-file`), never the secret itself.

```
ALLOWED:  --hub-token-file .sprawl/secrets/hub-token   (a path; secret read from file, 0600)
ALLOWED:  SPRAWL_HUB_TOKEN=…    (env; not on argv, not in ps output)
FORBIDDEN: --hub-token sprawl_hub_abc_secret           (argv → ps auxf → incident snapshot LEAK)
```

The same rule applies to the **browser login token** (§1): the operator types it
into the login page, and it is configured server-side from the secrets path —
it is never placed on a CLI flag, in a URL, or in logs.

### 5.1 Simplest way vs. right way — host token storage

- **Simplest:** accept `--hub-token=<token>` on the flag. Cost: guaranteed
  process-table + incident-snapshot leak; the single worst place to put a secret
  in *this* codebase.
- **Right (chosen):** file (0600) / secrets-reference / env only; flag names a
  path, never a value. Cost: one extra file, an existing pattern to reuse.
- **Recommendation: file/secrets/env only, no value-on-flag.** It matches the
  established `.env` convention and specifically dodges the QUM-728 snapshot leak
  vector. This is a hard rule, not a preference.

---

## 6. Browser session after login

After a successful login (§1.2), the hub mints its **own session** — a signed
cookie backed by a server-side record. The browser never holds the login token
after that first POST.

- **Signed, httpOnly session cookie.** The cookie holds an opaque session id (or
  a signed/encrypted token) and is `HttpOnly` (no JS access → XSS can't exfil it),
  `Secure` (HTTPS only), `SameSite=Strict` (CSRF resistance; there is no external
  IdP redirect to accommodate, so `Strict` is safe here). Signed/sealed with a
  key from the secrets path.
- **Avoid token-in-URL.** No session id and no login token is ever placed in a
  URL, query string, or fragment — URLs leak via referrer headers, browser
  history, server logs, and shoulder-surfing.
- **Server-side session record** keyed by the cookie's session id: `{user_id,
  issued_at, expires_at}` (the `user_id` is always the single MVP user, §3).
  Logout (`Logout` RPC, [`03` §2](03-api-surfaces.md)) deletes the record and
  clears the cookie. Idle + absolute TTLs bound session lifetime; on expiry the
  SPA sends the browser back to the login page (§1.2).
- **The session cookie is the browser's only credential.** This shrinks the
  browser's attack surface to "can present a valid hub cookie," and keeps the
  configured login token entirely server-side.

### 6.1 Simplest way vs. right way — session

- **Simplest:** keep the login token in `localStorage` and send it as a bearer.
  Cost: JS-readable (XSS exfil), no server-side revocation, a long-lived secret
  living in the browser. Bad.
- **Right (chosen):** server-minted signed httpOnly cookie + server-side session
  record. Cost: a session table/store + cookie signing key.
- **Recommendation: signed httpOnly cookie + server session.** httpOnly defeats
  the most common token-theft path (XSS), server-side records give real logout/
  revocation, and keeping the login token off the browser is strictly safer.
  Standard, boring, correct.

---

## 7. Two-check summary

| Caller | Mechanism | Verified against | Grants | Lifetime | Revoke |
|---|---|---|---|---|---|
| Browser | login page; present configured bearer token | constant-time compare vs `HUB_LOGIN_TOKEN` | signed httpOnly session cookie | idle+absolute TTL | Logout / rotate the deploy secret |
| Host | bearer token, `sprawl_hub_<id>_<secret>` | argon2id hash + pepper in PG | authenticated host↔hub connection | until revoked/expired | `RevokeHostToken` (effective next call/reconnect) |

Neither check grants **write authority** — that is the active-host marker
([`01` §4](01-architecture.md#4-identity-lease--fencing-conceptual--detail-in-doc-10--09)),
and **authorization / multi-user isolation** is the security-privacy doc's
problem (§3).

---

## Open Questions

- **Host-token hashing choice:** argon2id (memory-hard, best) vs. bcrypt
  (ubiquitous, simpler) — does the hub's expected auth-call rate make argon2id's
  cost noticeable, given we verify on the uplink path? Or do we cache a verified
  `(tokenid → host_id)` for a short TTL to avoid hashing every call?
- **Session store backing:** signed **stateless** cookie (no server record, can't
  revoke individual sessions before expiry) vs. **server-side** session table
  (revocable, but adds a store + a read per request). §6 leans server-side — is
  the per-request read acceptable, or is a short-TTL stateless cookie good enough
  for a single-user v1?
- **Login-token rotation:** rotating `HUB_LOGIN_TOKEN` is a redeploy/secret
  update that invalidates the browser's ability to re-login (existing cookies
  survive until TTL). Is redeploy-to-rotate acceptable for v1, or do we want a
  live "change login token" path even at single-user scale?
- **Host-token ↔ host_id binding:** bind on first use (flexible) vs. require the
  operator to name the host at create time (stricter attribution)? First-use
  binding is simpler but a leaked-before-first-use token could bind to an
  attacker's host.
- **Multiple browsers/devices:** one shared session or independent per-device
  sessions with independent logout? Ties into the session-store decision above.
- **Host-token scope:** in v1 a host token authenticates a host for *all* its
  projects. When per-agent/per-project authZ lands (security-privacy doc), should
  tokens carry a scope, or does scoping live entirely in the authZ layer?
  (Flagged so the token shape doesn't foreclose it.)
</content>
</invoke>
