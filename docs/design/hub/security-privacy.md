# security-privacy — Threat Model & Data-Privacy Design (MVP)

*The hub's threat model for the **v1 single-user cloud companion**, the
data-sensitivity inventory, the secret/transit/at-rest posture, and the
token-leak + public-repo hygiene rules. Multi-tenant isolation and
zero-knowledge encryption are **deferred** — documented here as clearly-triggered
future work, not v1 scope.*

See also: [`00-overview`](00-overview.md) · [`01-architecture`](01-architecture.md)
(§4 lease/fence) · [`02-components`](02-components.md) (§1.4 auth) ·
[`03-api-surfaces`](03-api-surfaces.md) · [`10-memory`](10-memory.md) ·
[index](README.md)

---

## 0. TL;DR — read this first

- **The hub is a cloud *companion* to the local binary, and it is SINGLE-USER.**
  It is **not** a multi-tenant service. One person runs it (self-hostable) or one
  person owns a hosted instance. Every threat-model decision below is scoped to
  that reality; the multi-tenant hardening is explicitly future work (§3, §8).
- **Auth = ONE configured bearer token.** The operator configures a single secret.
  Browsers exchange it for an **httpOnly session cookie**; hosts send it as a
  **bearer `Authorization` header**. **No OIDC, no IdP, no allowlist in v1** —
  those return with multi-tenant (§8).
- **Content-trust — default hub-can-read.** The hub reads plaintext transcripts /
  memory so the remote view and future search work. **Client-side (zero-knowledge)
  encryption and per-project content opt-out are DEFERRED** — documented as
  scoped future options (§2, §8), not v1.
- **Encryption:** at-rest relies on **provider/disk encryption**; in-transit is
  **TLS 1.3** on every seam (§4).
- **Token-leak vectors are closed by construction:** no token on a CLI flag
  (→ `ps`/incident-snapshot leak), no token in a URL (→ logs/referer leak),
  the bearer secret lives in a `0600` file / env / secrets manager (§5, §6).
- **No garbage collection in v1** — transcripts/attachments are kept indefinitely.
  This has a real privacy implication (sensitive content persists forever),
  mitigated by the single-user + self-hostable + authenticated posture (§7).
- **Public-repo hygiene is a build-time constraint:** nothing instance-specific
  (endpoints, secrets, host names) is ever committed (§9).

---

## 1. Threat model (single-user companion)

### 1.1 What we're protecting

The data-sensitivity inventory is unchanged by the re-scope — the content classes
the hub concentrates are exactly as sensitive as before. This inventory stays
load-bearing.

| Asset | Sensitivity | Where it lives |
|---|---|---|
| **Session transcripts** | HIGH — raw claude I/O; can contain employer-internal systems, hostnames, customer names, secrets pasted mid-session | blob store (`transcripts/`, [`10` §5](10-memory.md)) |
| **Distilled memory** | HIGH — curated facts/preferences, may name internal context | blob store (`units/`, `snapshots/`) |
| **Event log** | MED–HIGH — live claude output stream, same content class as transcripts | event-log store (PG, [`02` §1.6](02-components.md)) |
| **Attachments** | MED–HIGH — screenshots (design mocks, **error screens with tokens/PII**) | blob store |
| **The bearer token** | CRITICAL — the single configured secret = full hub access (browser + host) | `0600` file / env / secrets manager (§5, §6) |
| **Lease/fence registry** | LOW content, HIGH integrity — wrong holder = write clobber | PG |

The one-liner: **the hub concentrates, in one place, the most sensitive output of
every repo the single user connects.** That concentration is the whole product
value *and* the whole risk — even for one user.

### 1.2 Who we defend against

| Adversary | Capability assumed | Primary mitigation |
|---|---|---|
| **Network attacker** (mobile/NAT path, coffee-shop wifi) | Passive sniff + active MITM | TLS 1.3 everywhere; token never in URL/flag (§4, §5) |
| **Token thief** (leaked token via logs, `ps`, incident snapshot) | Holds the stolen bearer token = full access | Token-leak vectors closed by construction (§5); rotation = reconfigure + restart |
| **Curious/compromised hub operator** | Read DB + blob + memory at rest | For v1 the operator **is** the user (self-host) or a host the user chose to trust; at-rest provider encryption for the honest-but-hosted case (§4). True operator-blind storage is the deferred ZK future (§2, §8) |
| **Malicious/errant host** | A connected host tries to write another project's stream | Lease + fence ([`01` §4](01-architecture.md)) — an *integrity* control, orthogonal to auth |
| **The repo itself leaking** (public GitHub) | Anyone reads committed files | Build-time hygiene (§9): no endpoint/secret/host name ever committed |

### 1.3 Explicitly out of scope for v1 (YAGNI)

- **Multi-tenancy.** v1 is a single-user companion; there is no "other
  authenticated user" to defend against because there is no second user. The
  app-enforced tenant-isolation chokepoint is **deferred** (§3) with a crisp
  build trigger (§8).
- **Zero-knowledge / client-side encryption** and **per-project content opt-out**
  — deferred future options (§2, §8), not built in v1.
- **Compliance regimes** (SOC2/HIPAA/etc.) — not a goal of a personal-scale tool.
- **Garbage collection / retention windows** — v1 keeps everything (§7).
- **DoS/rate-limiting hardening** beyond basic sanity — a single-user, single-token
  surface bounds abuse.

---

## 2. Content-trust — default hub-can-read (ZK deferred)

> **Can the hub *read* the plaintext of the transcripts and memory it stores?**

**v1 decision: yes — default hub-can-read.** The hub stores and reads plaintext so
the remote view works today and server-side search can be built later. For a
single-user companion this is the pragmatic choice: the "operator" is the user
(self-hosting) or a host they explicitly chose to trust, so the operator-adversary
threat that would motivate client-side encryption is not present in v1.

### 2.1 Why hub-can-read is right for the MVP

- **The single user owns the trust decision by owning the deployment.** A
  privacy-max user **self-hosts** the whole hub (one Go container + Postgres +
  blob, [`01` §7](01-architecture.md)); then operator == user and no cryptography
  is needed to close the operator-adversary gap.
- **The features that justify the hub — remote view now, search later — need
  plaintext.** Zero-knowledge storage would make the hub a dumb ciphertext bucket
  and kill those features. Building ZK now would be pure YAGNI: no user has asked,
  it imposes heavy key-management burden, and it contradicts the roadmap.
- **At-rest provider encryption (§4) covers the honest-but-hosted case** — disk
  theft alone does not yield plaintext.

### 2.2 DEFERRED — two clearly-scoped future privacy options

Neither ships in v1. Both are documented so the data model doesn't foreclose them.

1. **Client-side (zero-knowledge) encryption seam.** Body blobs
   (transcripts/units) would pass through an `encrypt(bytes)→bytes` / `decrypt`
   interface (identity today). Flipping it to client-held keys yields
   operator-blind storage — at the cost of server-side search/synthesis, plus a
   hard key-management story (rotation, recovery, multi-device sync,
   "lose the key = lose the memory"). See §8 for the build trigger.
2. **Per-project content opt-out.** A project flagged **"don't sync content"**
   would still fan out status/events (so the remote view works) but keep
   transcript/memory bodies local. Composes with the
   [`10` §6 "sync these memories?" prompt](10-memory.md#the-sync-these-memories-prompt).
   Useful mainly in a *shared/hosted* setting — low value for a single self-hosting
   user, hence deferred. See §8.

**Design hedge (cheap, kept in v1):** store body content as **opaque blobs** with
provenance/index metadata separate in PG. This keeps the ZK seam and per-project
opt-out addable *later* without a schema migration — we don't build them, we just
don't paint ourselves into a corner.

---

## 3. Tenant isolation — single value now, chokepoint DEFERRED

v1 is single-user, so there is no cross-tenant read to prevent yet. We take one
cheap, forward-compatible step and defer the expensive one:

- **Keep a `user_id` column** on every project/instance/stream row. In v1 it
  **always holds one value** (the single configured user). Carrying it now means
  no migration when multi-tenant arrives.
- **DEFER the authorization chokepoint.** The multi-tenant design — every query
  funnelled through one mandatory `authz` layer that scopes `WHERE user_id =
  $caller`, plus a cross-tenant "user A cannot read user B" test suite — is **not
  built in v1**. It is the **flex-later hedge**: the `user_id` column is the seam,
  the chokepoint is the future work (§8).

> **Note the orthogonal integrity axis.** The [`01` §4](01-architecture.md)
> **write-lease** (which *host* may write for a project) is unrelated to tenant
> isolation and **stays in v1** — it prevents write clobbers between a user's own
> hosts, which is a real single-user concern.

---

## 4. Encryption — in transit & at rest

### 4.1 In transit

- **TLS 1.3 on every seam.** Browser↔hub and host↔hub both run over HTTPS/HTTP2
  ([`03`](03-api-surfaces.md)); no plaintext transport, ever, including the
  held-open downlink server-stream.
- **TLS terminates at the managed ingress** ([`01` §7](01-architecture.md));
  hub↔PG and hub↔blob use the provider's in-transit encryption.
- Certs are a **deploy parameter** (managed cert / ACME) — never committed (§9).

### 4.2 At rest

- **Rely on provider / disk at-rest encryption** (transparent volume / storage
  encryption) for the DB and blob store. This is the v1 baseline: disk theft alone
  does not yield plaintext.
- **The bearer token is never stored in plaintext where it can be dumped** — it is
  a configured secret resolved at runtime from a `0600` file / env / secrets
  manager (§5, §6), not committed and not sitting in the DB.
- **Deferred:** the client-side body-encryption seam (§2.2) sits conceptually
  *above* provider at-rest encryption; it is not built in v1.

### 4.3 Simplest vs. right

- **Simplest & sufficient for v1:** provider at-rest encryption + TLS in transit +
  a never-plaintext-at-rest bearer secret.
- **Deferred right-way extras:** app-level body encryption (§2.2) — only when the
  ZK trigger fires (§8).

---

## 5. The bearer token & the auth boundary

v1 auth is **one configured bearer token**. There is no per-host PAT registry, no
OIDC, no allowlist — those are multi-tenant features (§8).

### 5.1 How it works

- **One secret, configured by the operator.** The same token authenticates both
  paths.
- **Browser → httpOnly session cookie.** The browser presents the token once; the
  hub sets an **httpOnly, Secure, SameSite** session cookie. The token itself is
  never readable by page JS and never lands in a URL.
- **Host → bearer header.** A connected host sends the token in the
  `Authorization: Bearer …` header on its uplink/downlink connection.
- **Rotation = reconfigure + restart.** Because it's a single configured secret,
  rotation is "change the config value, restart the hub, update hosts/browser."
  No per-host revocation machinery in v1 (that returns with per-host PATs under
  multi-tenant, §8).

### 5.2 Token-leak vectors — closed by construction

These are the concrete ways the bearer token escapes, and how the design
forecloses each. **This section stays fully in force.**

| Vector | How it leaks | Mitigation |
|---|---|---|
| **CLI flag** (`--token=…`) | Visible in `ps auxf`, shell history, **and the `Ctrl+\` incident snapshot** (which captures `ps auxf` + `/proc/<pid>` — see `CLAUDE.md`) | **Never accept the token on a flag.** Resolve it via the secrets path / env / config file (mode `0600`), matching the `.env` handling for `CLAUDE_CODE_OAUTH_TOKEN` (`CLAUDE.md` QUM-518). |
| **Token in URL** | Query strings land in access logs, proxy logs, browser history, `Referer` headers | **Never put the token in a URL.** Hosts ride the `Authorization` header; the browser uses an httpOnly cookie after the initial exchange. No `?token=` anywhere. |
| **Incident snapshots / logs** | The forensic bundle captures mcp-calls, process args, fds | Token never in argv (above); scrub/redact secret-shaped material from any hub log line; snapshots are gitignored forensic artifacts (`CLAUDE.md` hygiene) |
| **Committed to repo** | Token pasted into config that gets checked in | Secret only via secrets manager / gitignored `.env` (§6, §9); CI has no token; nothing instance-specific committed |

> **Design rule (single sentence):** *the token only ever exists in a header, an
> httpOnly cookie, an env var, a `0600` file, or the secrets manager — never in an
> argv, a URL, a log, or a committed file.*

---

## 6. Secret handling

- **`gocloud.dev/secrets`** ([`02` §1.7](02-components.md)) is the single portable
  interface for the runtime secret material — in v1 that is **the bearer token**
  (and, when it's used, any session-cookie signing key). Backed by the provider's
  secrets manager in prod; a local impl for tests/dev — so no cloud provider is
  baked into app code.
- **No secret in app config or PG plaintext.** Config carries *references*
  (secret names / URIs), resolved at runtime through the secrets path.
- **The `.env` shim pattern** already established for host-side auth
  (`CLAUDE.md` QUM-518: `scripts/run-claude` sources a `0600`, gitignored `.env`)
  is the model for how a host resolves the bearer token locally — same hygiene,
  same never-committed guarantee.

---

## 7. Retention — no GC in v1 (privacy implication)

**v1 has no garbage collection.** Transcripts and attachments are **kept
indefinitely** — there is no retention window, no expiry, no compaction of raw
bodies.

- **Privacy implication (stated plainly):** the most sensitive content class
  (transcripts, attachments — HIGH sensitivity per §1.1) **persists forever** on
  the hub. Anything ever synced is recoverable for as long as the hub lives.
- **Why it's acceptable for the MVP:** the exposure is bounded by the
  single-user + self-hostable + authenticated posture. Only the one configured
  user can reach the data, and a privacy-max user self-hosts so the storage is on
  infrastructure they control. There is no third party accumulating other people's
  content.
- **Future work:** retention windows / GC (especially a stricter window for
  raw transcripts, which are far more likely to carry pasted secrets than curated
  units) become relevant when multi-tenant or hosted-for-others arrives (§8).

---

## 8. Deferred hardening & its triggers

Everything trimmed from the pre-MVP design is collected here with the concrete
condition that should make us build it. The seams left in v1 (`user_id` column,
opaque body blobs, `gocloud.dev/secrets`) keep each addable without a rewrite.

| Deferred item | v1 seam kept | **Build trigger** |
|---|---|---|
| **OIDC + IdP + user allowlist** (replaces the single bearer token) | Single configured secret; auth isolated behind one boundary | The moment a **second distinct user** must log in to the same hub instance. |
| **App-enforced tenant-isolation chokepoint** (mandatory `authz` scoping + cross-tenant test suite) | `user_id` column present, always one value | Same trigger — the first multi-user hub instance. Do **not** ship multi-user without it. |
| **Per-host PATs** (hashed, individually revocable) + rotation without downtime | Single bearer token; header-based auth | When multiple hosts belonging to **different owners** connect, or when per-host revocation (lost laptop for one of many owners) is required. |
| **Client-side / zero-knowledge body encryption** | Opaque body blobs behind a would-be `encrypt()` seam | A user requires **operator-blind** storage on a hub they do **not** control (i.e. hosted-for-others), and self-hosting is not an acceptable answer. |
| **Per-project content opt-out** | Content stored as separable bodies vs. status/events | A **shared/hosted** deployment where a user wants some projects' bodies to stay local while still using the pane-of-glass. |
| **Retention windows / GC** (esp. transcript redaction/expiry) | Bodies stored as discrete, addressable blobs | Multi-tenant/hosted growth, a compliance requirement, or storage-cost pressure. |

The rule: **v1 stays single-user and simple; the first genuine multi-tenant /
hosted-for-others requirement is the trigger to build the OIDC + isolation +
PAT + ZK stack together**, not piecemeal.

---

## 9. Public-repo hygiene as a build-time constraint

The repo is **public** (`github.com/dmotles/sprawl`). Security here is partly a
*build-time* property: the source tree must never leak the maintainer's (or any
user's) deployment specifics. Per `CLAUDE.md`'s Public-vs-Private section — **this
section stays fully in force.**

- **No default/hardcoded hub endpoint** in code ([`01` §3](01-architecture.md)) —
  connecting is opt-in via `--hub-url`/env/config. Absent config, the hub client
  never starts.
- **No host alias, machine name, or customer name** committed. Cloud providers
  appear only as generic public-cloud targets, never attributed to an organization.
- **`host_id`/`run_id` are opaque generated identifiers** — never a hostname,
  username, MAC, or machine description ([`10` §3 hygiene note](10-memory.md)).
- **Everything deployment-specific is parameterized** (Terraform variables,
  [`01` §7](01-architecture.md)); IaC ships the *shape*, not the *values*.
- **The bearer token / signing key are never committed** — secrets manager or
  gitignored `.env` only (§6).
- **Forensic/incident artifacts stay gitignored** and default to
  unsanitized-⇒-not-committed.
- **CI has no live secret.** lint/test run without any real endpoint or token;
  e2e against a live hub uses local fakes ([`12`](README.md)).

This is enforceable in review: a reviewer must refuse to merge anything that names
an internal system/host or bakes in an endpoint/secret (`CLAUDE.md` reviewer
rule). It's a security control, not just style.

---

## Open Questions

- **Session-cookie hardening:** beyond httpOnly/Secure/SameSite, does the browser
  session need an idle timeout / no-persistence mode for a shared/kiosk device,
  even in single-user v1?
- **Bearer-token rotation ergonomics:** "reconfigure + restart" is fine for one
  user — but is a brief overlap window (accept old+new) worth it to avoid a
  host-reconnect blip during rotation?
- **Transcript redaction on ingest:** given no-GC (§7) means raw transcripts
  persist forever, is a lightweight secret-scrubbing pass on ingest worth doing in
  v1 anyway ([`10` OQ](10-memory.md#open-questions))?
- **When exactly does the multi-tenant trigger fire?** §8 keys the whole
  OIDC/isolation/PAT/ZK stack to "the first second user" — is there an
  intermediate "share read-only with one trusted person" step that would force a
  subset earlier?
- **Metadata sensitivity if ZK ever lands:** even with encrypted bodies, PG holds
  provenance (agent names, timestamps, `host_id`, sizes). Would a real ZK claim
  require obfuscating that too? (Deferred, but flagged.)
