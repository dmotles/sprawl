# 04 — Authentication

*The hub's two trust checks: browsers via OIDC, hosts via Personal Access
Tokens. Conforms to [`01-architecture.md`](01-architecture.md); this is the
detail behind [`02` §1.4](02-components.md) and the auth notes in
[`03`](03-api-surfaces.md).*

See also: [`00-overview.md`](00-overview.md) · [`01-architecture.md`](01-architecture.md) ·
[`02-components.md`](02-components.md) · [`03-api-surfaces.md`](03-api-surfaces.md) ·
[index](README.md)

---

## 0. Scope — authN only

This doc covers **authentication** — *who is this caller, and are they allowed
in at all?* It deliberately stops there.

- **In scope:** browser login (OIDC), host login (PAT), the user allowlist, PAT
  lifecycle (create / rotate / revoke), where the PAT lives on the host, and the
  browser session after an OIDC callback.
- **Out of scope — defer to the security-privacy doc:** authorization,
  tenant/project isolation, per-agent access scoping, threat model, and the
  content-trust model. Authentication answers *"come in?"*; authorization answers
  *"do what?"*. Keeping them apart keeps this doc small.
- **Also distinct — write authority is NOT auth.** Being authenticated lets you
  *hold a connection*; it does **not** grant the right to *write*. Write
  authority is the lease/fence's job ([`01` §4](01-architecture.md#4-identity-lease--fencing-conceptual--detail-in-doc-10--09),
  [`02` §1.5](02-components.md)). Auth gates the connection; the lease gates the
  write. A host can be perfectly authenticated and still be rejected on a stale
  fence.

```
        ┌─────────── two callers, two trust checks, one component (02 §1.4) ───────────┐
        │                                                                              │
   browser ──OIDC (Authorization Code + PKCE)──▶  hub  ──▶ allowlist check ──▶ session cookie
   host    ──PAT (bearer, hashed-verify)───────▶  hub  ──▶ host_id bound     ──▶ connection up
        │                                                                              │
        └──────────── both then subject to lease/fence for WRITES (not here) ──────────┘
```

---

## 1. Browsers → OIDC (the hub is a relying party)

The hub is an **OIDC relying party (RP)** using [`go-oidc`](https://github.com/coreos/go-oidc).
It does **not** implement an identity provider, store passwords, or bake in any
provider.

### 1.1 The IdP is a deploy parameter — never hardcoded

The issuer URL, client ID, and client secret are **configuration**, resolved at
startup, never compiled in. There is **no default IdP and no provider baked into
the code** (public-repo hygiene, mirrors the "no default hub endpoint" rule in
[`01` §3](01-architecture.md#3-connected-vs-disconnected)).

| Parameter | Source | Notes |
|---|---|---|
| `OIDC_ISSUER_URL` | env / config | Discovery via `<issuer>/.well-known/openid-configuration`; `go-oidc` fetches JWKS + endpoints. |
| `OIDC_CLIENT_ID` | env / config | Public. |
| `OIDC_CLIENT_SECRET` | **secrets path** (`gocloud.dev/secrets`, [`02` §1.7](02-components.md)) | Never in env-in-plaintext where avoidable, never committed. |
| `OIDC_REDIRECT_URL` | env / config | The hub's own `…/auth/callback`; deployment-specific, parameterized. |
| `OIDC_SCOPES` | config, default `openid email` | Minimal — we need a stable subject + an email to match the allowlist. |

Any standards-compliant IdP works because we speak only the OIDC discovery +
Authorization Code flow. The hub is provider-agnostic by construction.

### 1.2 Flow — Authorization Code + PKCE

```
browser                 hub (RP)                       IdP (deploy param)
  │  GET /login            │                                │
  │───────────────────────▶│  build authz URL               │
  │                        │  (state, nonce, PKCE challenge) │
  │  302 → IdP authorize    │                                │
  │────────────────────────┼───────────────────────────────▶│
  │                        │        user authenticates       │
  │  302 → /auth/callback?code&state                          │
  │◀───────────────────────┼────────────────────────────────│
  │  GET /auth/callback     │                                │
  │───────────────────────▶│  verify state, exchange code    │
  │                        │───────────────────────────────▶│  (code + PKCE verifier)
  │                        │◀───────────────────────────────│  id_token + access_token
  │                        │  verify id_token (sig via JWKS, │
  │                        │  iss, aud, exp, nonce)          │
  │                        │  ► ALLOWLIST CHECK (§3)          │
  │  Set-Cookie: session    │  mint signed httpOnly session   │
  │◀───────────────────────│                                 │
```

- **PKCE** (`S256`) is used even with a confidential client — cheap, closes the
  code-interception hole, and lets the same flow serve a future public client
  without redesign.
- **`state`** (CSRF) and **`nonce`** (replay) are mandatory and verified. Both
  are bound to the pre-login session (short-lived signed cookie) so a forged
  callback is rejected.
- We validate the **ID token** for identity (subject + email). The access token
  is not needed for anything beyond the token exchange in v1 — we don't call the
  IdP's userinfo or any resource API — so we don't store it (YAGNI).

### 1.3 Simplest way vs. right way — browser auth

- **Simplest:** HTTP basic-auth or a single shared password on the SPA. Cost: no
  real identity, no per-user allowlist, no revocation, credentials leak trivially,
  and it reads as amateur-hour in a public repo's ops story.
- **Right:** full OIDC RP with a bring-your-own IdP as a deploy parameter.
  Cost: an IdP must exist and be configured; a callback route + session plumbing.
- **Recommendation: OIDC RP now.** This is the whole reason the hub is an "auth
  boundary." OIDC gives real identity, standards-based revocation (the IdP can
  disable a user), and provider portability — and `go-oidc` makes it a small
  amount of code. The IdP-as-deploy-param keeps the repo clean and multi-cloud.

---

## 2. Hosts → Personal Access Tokens (PATs)

Each host (`sprawl enter`) authenticates its persistent dial-out connection
([`01` §1](01-architecture.md), [`03` §1](03-api-surfaces.md)) with a **PAT**
sent as a bearer credential on every host↔hub call. The hub verifies it against
**hashed** tokens in Postgres.

### 2.1 Token shape + hashing

- **Presented token:** an opaque high-entropy string with a readable prefix and
  an embedded lookup id, e.g. `sprawl_pat_<tokenid>_<secret>`. The prefix aids
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
pat
  id            -- the <tokenid> in the presented string (indexed lookup)
  hash          -- argon2id(secret, pepper); NEVER the token
  label         -- human name ("laptop repo-1", "ci")
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
- **Right:** per-host PATs, hashed at rest, individually revocable.
  Cost: a small table + a token-vending UI + lifecycle.
- **Recommendation: per-host hashed PATs now** (echoing [`02` §1.4](02-components.md)).
  Per-host revocation and attribution are exactly what an auth boundary is for,
  and the cost is one table plus a few unary RPCs we already listed in
  [`03` §2](03-api-surfaces.md) (`CreatePAT` / `ListPATs` / `RevokePAT`).

---

## 3. User allowlist model

Authentication proves *who*; the **allowlist** decides *whether that who gets in
at all*. It is intentionally the crudest possible gate for v1.

- **Model:** a small table (or config list) of allowed **stable subject
  identifiers** — prefer the IdP `sub` claim (immutable) with `email` as a
  human-readable secondary. On OIDC callback (§1.2), after ID-token validation,
  the hub checks the subject/email against the allowlist; not present ⇒ **deny**,
  render a plain "not authorized" page, mint **no** session.
- **During testing, exactly one user gets in.** The allowlist starts with a
  single entry (the operator). This is a feature, not a placeholder — it keeps
  the blast radius at one identity until multi-user authZ (tenant isolation)
  is designed in the security-privacy doc.
- **Allowlist ≠ authorization.** Being on the allowlist means "you may open a
  session and see what you're shown." What you're *allowed to see or drive*
  across projects/agents is **authZ** and is deferred. The allowlist is a coarse
  front door, not a permission system.

### 3.1 Simplest way vs. right way — allowlist

- **Simplest:** a hardcoded single email. Cost: re-deploy to add anyone; and a
  literal identity in a public repo violates hygiene.
- **Right (eventual):** roles/groups synced from the IdP (claims-based).
  Cost: real authZ design — premature now.
- **Recommendation: a config/DB allowlist of subject ids, seeded with one
  entry.** No identities committed to the repo (they live in deploy config/DB).
  It's a one-line check, trivially extended to N users, and it defers the real
  authZ decision to where it belongs (security-privacy doc) without blocking v1.

---

## 4. PAT lifecycle + token vending

The management UI is served by the SPA (behind OIDC — only an allowlisted,
logged-in browser can mint host tokens), backed by the `CreatePAT` / `ListPATs`
/ `RevokePAT` RPCs from [`03` §2](03-api-surfaces.md).

### 4.1 Create (vend)

```
browser (logged in) ──CreatePAT{label}──▶ hub
                                           │  generate secret (CSPRNG)
                                           │  token = sprawl_pat_<id>_<secret>
                                           │  store argon2id(secret,pepper) + label
        show token ONCE  ◀─────────────────┘  return FULL token in THIS response only
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
1. CreatePAT{label:"laptop (rotated)"}  → new token vended, shown once
2. operator installs new token on the host (§5); host reconnects with it
3. RevokePAT{old_id}                    → old token dead
```

We do **not** build in-place secret mutation (YAGNI) — create+revoke reuses
existing verbs and leaves an audit trail of both rows.

### 4.3 Revoke

`RevokePAT{id}` sets `revoked_at`. Verification (§2.1) rejects revoked tokens on
the **next** call. Because auth is checked per-call on the uplink and at
(re)connect on the downlink, a revoked host is locked out within one call/
reconnect — no long-lived grant to wait out. (Session cookies are a separate
lifetime, §6.)

### 4.4 Simplest way vs. right way — lifecycle

- **Simplest:** create-only; to "revoke," delete the row and rotate everything.
  Cost: no rotation grace, no audit trail, blunt.
- **Right:** create (show-once) + revoke (`revoked_at`) + rotate-as-create+revoke,
  all per-token. Cost: three small unary RPCs + a minimal UI screen.
- **Recommendation: the three-verb lifecycle**, because per-host revocation is
  the entire justification for PATs over a shared secret (§2.3). Rotation reuses
  create+revoke, so it costs no new machinery.

---

## 5. Where the PAT lives on the host

The host reads its PAT via the **secrets path**, resolved the same way as other
host secrets, and **never** from a value passed on the command line.

- **Storage:** a file under the host's sprawl config dir (e.g.
  `.sprawl/secrets/hub-pat` or a `gocloud.dev/secrets` reference), mode `0600`,
  owned by the operator. **Never committed** — the path is gitignored, mirroring
  the existing `.env` handling in `CLAUDE.md`.
- **Resolution precedence** (highest first), matching [`02` §2.6](02-components.md):
  the secrets-path reference, then env (`SPRAWL_HUB_PAT`), then config. Absent ⇒
  the hub client stays inert (disconnected default).
- **NEVER a value on a CLI flag.** A token passed as `--hub-pat=sprawl_pat_…`
  leaks into the **process table** (`ps auxf`), shell history, and — critically
  in this codebase — the **incident snapshot** bundle (`Ctrl+\`, `ps auxf` +
  `/proc/<pid>/status`, see `CLAUDE.md` QUM-728). The flag layer may name a
  *path/reference* (`--hub-pat-file`), never the secret itself.

```
ALLOWED:  --hub-pat-file .sprawl/secrets/hub-pat   (a path; secret read from file, 0600)
ALLOWED:  SPRAWL_HUB_PAT=…    (env; not on argv, not in ps output)
FORBIDDEN: --hub-pat sprawl_pat_abc_secret         (argv → ps auxf → incident snapshot LEAK)
```

### 5.1 Simplest way vs. right way — host token storage

- **Simplest:** accept `--hub-pat=<token>` on the flag. Cost: guaranteed
  process-table + incident-snapshot leak; the single worst place to put a secret
  in *this* codebase.
- **Right:** file (0600) / secrets-reference / env only; flag names a path, never
  a value. Cost: one extra file, an existing pattern to reuse.
- **Recommendation: file/secrets/env only, no value-on-flag.** It matches the
  established `.env` convention and specifically dodges the QUM-728 snapshot leak
  vector. This is a hard rule, not a preference.

---

## 6. Browser session after OIDC callback

After a successful callback + allowlist pass (§1.2, §3), the hub mints its **own
session** — it does **not** hand the IdP tokens to the browser.

- **Signed, httpOnly session cookie.** The cookie holds an opaque session id (or
  a signed/encrypted token) and is `HttpOnly` (no JS access → XSS can't exfil it),
  `Secure` (HTTPS only), `SameSite=Lax` (CSRF resistance while allowing the
  top-level redirect back from the IdP). Signed/sealed with a key from the
  secrets path.
- **Avoid token-in-URL.** No access/ID token, and no session id, is ever placed
  in a URL, query string, or fragment — URLs leak via referrer headers, browser
  history, server logs, and shoulder-surfing. The `code`/`state` on the callback
  URL are single-use and consumed immediately server-side.
- **Server-side session record** keyed by the cookie's session id: `{subject,
  email, issued_at, expires_at}`. Logout (`Logout` RPC, [`03` §2](03-api-surfaces.md))
  deletes the record and clears the cookie. Idle + absolute TTLs bound session
  lifetime; on expiry the SPA silently re-runs the OIDC flow.
- **The SPA never sees IdP tokens.** It only ever holds the hub session cookie.
  This keeps the IdP relationship entirely server-side and shrinks the browser's
  attack surface to "can present a valid hub cookie."

### 6.1 Simplest way vs. right way — session

- **Simplest:** put the raw ID token in `localStorage` and send it as a bearer.
  Cost: JS-readable (XSS exfil), no server-side revocation, token-in-storage
  sprawl. Bad.
- **Right:** server-minted signed httpOnly cookie + server-side session record.
  Cost: a session table/store + cookie signing key.
- **Recommendation: signed httpOnly cookie + server session.** httpOnly defeats
  the most common token-theft path (XSS), server-side records give real logout/
  revocation, and keeping IdP tokens off the browser is strictly safer. Standard,
  boring, correct.

---

## 7. Two-check summary

| Caller | Mechanism | Verified against | Grants | Lifetime | Revoke |
|---|---|---|---|---|---|
| Browser | OIDC Auth-Code+PKCE, IdP = deploy param | IdP (JWKS) + allowlist | signed httpOnly session cookie | idle+absolute TTL | Logout / IdP disables user / drop from allowlist |
| Host | PAT bearer, `sprawl_pat_<id>_<secret>` | argon2id hash + pepper in PG | authenticated host↔hub connection | until revoked/expired | `RevokePAT` (effective next call/reconnect) |

Neither check grants **write authority** — that is the lease/fence layer
([`01` §4](01-architecture.md#4-identity-lease--fencing-conceptual--detail-in-doc-10--09)),
and **authorization / tenant isolation** is the security-privacy doc's problem.

---

## Open Questions

- **PAT hashing choice:** argon2id (memory-hard, best) vs. bcrypt (ubiquitous,
  simpler) — does the hub's expected auth-call rate make argon2id's cost
  noticeable, given we verify a PAT on the uplink path? Or do we cache a verified
  `(tokenid → host_id)` for a short TTL to avoid hashing every call?
- **Session store backing:** signed **stateless** cookie (no server record, can't
  revoke individual sessions before expiry) vs. **server-side** session table
  (revocable, but adds a store + a read per request). §6 leans server-side — is
  the per-request read acceptable, or is a short-TTL stateless cookie + refresh
  good enough for v1?
- **Allowlist source of truth:** DB table (editable live via UI) vs. deploy
  config (immutable without redeploy)? DB is friendlier; config is simpler and
  harder to fat-finger. Which for v1's single-user reality?
- **PAT ↔ host_id binding:** bind on first use (flexible) vs. require the operator
  to name the host at create time (stricter attribution)? First-use binding is
  simpler but a leaked-before-first-use token could bind to an attacker's host.
- **OIDC logout federation:** do we need RP-initiated logout / back-channel logout
  (kill the hub session when the IdP session ends), or is local hub-session
  logout enough for v1?
- **Multiple browsers per user:** one shared session or independent per-device
  sessions with independent logout? Ties into the session-store decision above.
- **PAT scope:** in v1 a PAT authenticates a host for *all* its projects. When
  per-agent/per-project authZ lands (security-privacy doc), should PATs carry a
  scope, or does scoping live entirely in the authZ layer? (Flagged so the token
  shape doesn't foreclose it.)
