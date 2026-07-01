# security-privacy — Threat Model & Data-Privacy Design

*The hub's threat model, the load-bearing **content-trust decision** (can the hub
read synced transcripts/memory?), app-enforced tenant isolation, and the
transit/at-rest/secret-handling posture.*

See also: [`00-overview`](00-overview.md) · [`01-architecture`](01-architecture.md)
(§4 lease/fence) · [`02-components`](02-components.md) (§1.4 auth) ·
[`03-api-surfaces`](03-api-surfaces.md) · [`10-memory`](10-memory.md) ·
[index](README.md)

---

## 0. TL;DR — read this first

- **The key decision — content-trust model:** **default hub-can-read** the synced
  transcripts/memory (so server-side search and the future memory/session console
  can exist), **but** (a) **per-project opt-out**, (b) **self-hostable** so a
  privacy-max user runs their own hub, and (c) **design the blob layer so
  client-side encryption can be switched on later** without a schema migration.
  Full rationale + tradeoff in §2. This matters because transcripts/memory can hold
  **employer-internal / sensitive content** (per `CLAUDE.md` hygiene), and for a
  **public** tool other users sync *their own* private repos.
- **Tenant isolation is APP-ENFORCED.** We dropped any BaaS, so vanilla Postgres
  gives us **no free row-level security**. Every query is scoped by the
  authenticated user in application code, funnelled through one authorization
  chokepoint (§3). This is a deliberate correctness burden we take on explicitly.
- **Auth gates the connection; the lease gates the write** ([`02` §1.4](02-components.md)).
  Browsers → OIDC (IdP is a deploy parameter). Hosts → per-host PATs, **hashed** in
  Postgres, individually revocable (§5).
- **Token-leak vectors are closed by construction:** no token on a CLI flag
  (→ `ps`/incident-snapshot leak), no token in a URL (→ logs/referer leak),
  secrets via `gocloud.dev/secrets` (§6).
- **Public-repo hygiene is a build-time constraint:** nothing instance-specific
  (endpoints, IdP, tenant/host names) is ever committed (§7).

---

## 1. Threat model

### 1.1 What we're protecting

| Asset | Sensitivity | Where it lives |
|---|---|---|
| **Session transcripts** | HIGH — raw claude I/O; can contain employer-internal systems, hostnames, customer names, secrets pasted mid-session | blob store (`transcripts/`, [`10` §5](10-memory.md)) |
| **Distilled memory** | HIGH — curated facts/preferences, may name internal context | blob store (`units/`, `snapshots/`) |
| **Event log** | MED–HIGH — live claude output stream, same content class as transcripts | event-log store (PG, [`02` §1.6](02-components.md)) |
| **Attachments** | MED–HIGH — screenshots (design mocks, **error screens with tokens/PII**) | blob store |
| **Host→hub PATs** | CRITICAL — a valid PAT = a host's uplink/downlink identity | PG (hashed) |
| **OIDC client secret / hashing pepper** | CRITICAL — compromise breaks auth for everyone | secrets manager |
| **Lease/fence registry** | LOW content, HIGH integrity — wrong holder = write clobber | PG |

The one-liner: **the hub concentrates, in one place, the most sensitive output of
every connected repo.** That concentration is the whole product value *and* the
whole risk.

### 1.2 Who we defend against

| Adversary | Capability assumed | Primary mitigation |
|---|---|---|
| **Other authenticated users** (public multi-tenant hub) | Valid OIDC login, can call every RPC | App-enforced tenant isolation (§3) — the sharpest risk for a public tool |
| **Network attacker** (mobile/NAT path, coffee-shop wifi) | Passive sniff + active MITM | TLS 1.3 everywhere; PAT never in URL/flag (§4, §5) |
| **Curious/compromised hub operator** | Read DB + blob + memory at rest | Content-trust model (§2): self-host or client-side-encrypt for true zero-knowledge; at-rest encryption for the honest-but-hosted case |
| **Token thief** (leaked PAT via logs, `ps`, incident snapshot) | Holds a stolen PAT | Per-host scope + instant revocation (§5); no token on CLI flag or in URL |
| **Malicious/errant host** | A connected host tries to write another project's stream | Lease + fence ([`01` §4](01-architecture.md)); PAT→project binding (§5) |
| **The repo itself leaking** (public GitHub) | Anyone reads committed files | Build-time hygiene (§7): no endpoint/IdP/secret/tenant ever committed |

### 1.3 Explicitly out of scope for v1 (YAGNI)

- **Hub operator as active adversary** *while* content-trust is left at the
  hub-can-read default — that user chose to trust their hub (or self-host). We give
  them the *option* to not trust it (§2), not a guarantee against the default.
- **Multi-user-per-project sharing / ACLs.** v1 is single-owner-per-project
  (matches the maintainer's real workflow). Fine-grained sharing is north-star.
- **Compliance regimes** (SOC2/HIPAA/etc.) — not a goal of a personal-scale tool.
- **DoS/rate-limiting hardening** beyond basic sanity — a small allowlisted user
  set (§5) bounds abuse.

---

## 2. THE key decision — content-trust model

> **Can the hub *read* the plaintext of the transcripts and memory it stores?**
> Everything else in this doc is downstream of this one choice.

### 2.1 The two poles

| | **Hub-can-read** (server-side plaintext) | **Zero-knowledge** (client-side-encrypted blobs) |
|---|---|---|
| Hub sees | Plaintext transcripts, memory, events | Opaque ciphertext only; keys never leave the host/browser |
| Server-side **search** | ✅ Possible (index the text) | ❌ **Impossible** — hub can't index what it can't read |
| Memory/session **console** ([`00` north-star](00-overview.md#north-star-vision--not-committed--future)) | ✅ Browse/curate/replay works | ❌ **Breaks** — console renders in the browser only, no server assist, no cross-device search |
| Server-side **synthesis/reconcile** ([`10` §4](10-memory.md)) | ✅ A hub-side job can read+rewrite units | ❌ Must run client-side only (dormant-agent problem, [`10` OQ](10-memory.md#open-questions)) |
| Operator compromise exposes content | ✅ Yes (mitigated by at-rest enc, §4) | ❌ No — this is the whole point |
| Key management burden | None | HIGH — per-`(project,agent)` keys, rotation, recovery, multi-device key sync, "lose the key = lose the memory forever" |
| Attachment preview / thumbnails | ✅ Server can render | ❌ Client-only decode |

The tension is stark: **the exact features that justify the hub existing (search,
console, server-side synthesis) are the features zero-knowledge kills.** ZK buys
maximum privacy at the cost of making the hub a dumb ciphertext bucket — at which
point most of its value over "sync a file to object storage" evaporates.

### 2.2 Why this is genuinely hard here

- **Content is sensitive by nature.** Per `CLAUDE.md`'s Public-vs-Private hygiene,
  transcripts/memory routinely carry employer-internal systems, hostnames,
  customer names, internal URLs. Forensic/incident artifacts are *especially*
  likely to leak. This is not hypothetical PII — it's the maintainer's actual
  working context.
- **Public tool, many-tenant blast radius.** Because the repo is public and the
  hub is self-service, **other users sync their own private repos** through a hub
  instance they may not operate. Their trust decision must be *theirs*, not
  baked in by us.
- **We can't have both search and zero-knowledge** on the same bytes without
  searchable/homomorphic encryption — which is complex, weak, or slow, and is a
  hard no for a KISS/YAGNI personal-scale tool.

### 2.3 Simplest way vs. right way

- **Simplest way:** hub-can-read, full stop. All features work; ship fast. Cost: a
  compromised or curious operator sees everything; no path to privacy for a user
  who needs it; a future ZK requirement forces a painful storage-layer rewrite.
- **Right way (absolutist):** end-to-end client-side encryption from day one. Cost:
  kills search + console + server synthesis, imposes heavy key management, and
  contradicts the north-star — an expensive guarantee most users of a *personal*
  tool don't need.
- **Recommendation — layered, not absolutist:**
  1. **Default: hub-can-read.** So search, the console, and server-side synthesis
     are *possible* (they're north-star, not v1 — but the data model must not
     foreclose them). At-rest encryption (§4) protects the honest-but-hosted case.
  2. **Per-project opt-out.** A project can be flagged **"don't sync content to the
     hub"** — status/events still fan out (so the pane-of-glass works) but
     transcripts/memory bodies stay local. Composes with the existing
     [`10` §6 "sync these memories?" prompt](10-memory.md#the-sync-these-memories-prompt):
     opt-out = permanently answer *Keep local only*. This is the pragmatic privacy
     valve for the sensitive-repo case, available in v1.
  3. **Self-hostable.** The whole hub is one Go container + Postgres + blob/secrets
     ([`01` §7](01-architecture.md)). A privacy-max user **runs their own hub** and
     the operator-adversary threat disappears without any crypto. This is the
     cheapest strong-privacy answer and it already falls out of the deploy model.
  4. **Design the blob layer so client-side encryption can be switched on later.**
     Store unit/transcript bodies as **opaque blobs behind an encryption seam**
     (an `encrypt(bytes)→bytes` / `decrypt` interface that is identity by default).
     Keep provenance/index metadata in PG *unencrypted* (that's what search/console
     need and it's lower-sensitivity), but keep bodies swappable to ciphertext.
     Then per-project (or global) **client-side encryption becomes a config flip**
     that only sacrifices the features that need to read bodies — no schema
     migration, no data model change.

**Why this is the KISS/YAGNI-honest answer:** we don't *build* ZK now (YAGNI —
no user has asked, and it kills the roadmap), but we don't *foreclose* it
either (the seam is nearly free). The two privacy escape hatches that ship in v1 —
**per-project opt-out** and **self-host** — cover the real sensitive-content case
without any cryptography. ZK stays a future switch, gated behind the seam.

```
                       ┌─────────────── per project ───────────────┐
  sync content = OFF ──┤ status/events fan out; bodies stay local  │  (v1 valve)
                       └────────────────────────────────────────────┘
  sync content = ON  ──▶ bodies → blob store, behind encryption seam:
                           enc = identity  → hub-can-read (default; search/console work)
                           enc = clientkey → zero-knowledge (future flip; those features degrade)
  privacy-max user   ──▶ self-host the whole hub → operator == you
```

---

## 3. Tenant authorization & isolation (app-enforced)

**We dropped any BaaS**, so there is **no database row-level-security for free.**
Vanilla managed Postgres ([`01` §7](01-architecture.md)) enforces nothing about
*who* may read a row — that is 100% the application's job. We name this explicitly
because it's the single easiest thing to get wrong on a public multi-tenant hub.

### 3.1 The model

- **Owner = the authenticated OIDC user.** Every project/instance/stream row
  carries an `owner_user_id`. v1 is **single-owner-per-project** (§1.3).
- **One authorization chokepoint.** Every browser-facing RPC ([`03` §2](03-api-surfaces.md))
  resolves the caller's `user_id` from the validated OIDC session and passes it
  into a **single `authz` layer** that scopes *every* query: `WHERE owner_user_id
  = $caller`. No handler builds a query that isn't funnelled through it.
- **Host writes are bound by PAT→project.** A host's PAT (§5) is minted for a
  specific owner; the ingest path ([`02` §1.2](02-components.md)) rejects any
  frame whose `project`/`host_id` doesn't match the PAT's binding — *before* the
  fence check. So a compromised host can't even *address* another tenant's stream.

```
browser  ─OIDC session─▶ resolve user_id ─▶ authz.scope(user_id) ─▶ query WHERE owner=user_id
host     ─PAT─────────▶ resolve pat.owner+pat.project ─▶ reject frame if project mismatch ─▶ fence check ─▶ append
```

### 3.2 Simplest way vs. right way

- **Simplest:** trust handlers to remember the `WHERE owner = …` clause each time.
  Cost: one forgotten clause = a cross-tenant read; impossible to audit; the
  classic multi-tenant breach.
- **Right:** a mandatory `authz` chokepoint every query passes through, plus a
  defense-in-depth check. Cost: a little plumbing discipline.
- **Recommendation:** **single chokepoint + belt-and-suspenders.** Route all
  tenant-scoped reads through one function that *requires* a caller identity (make
  it impossible to call the store without one — the type system carries the
  `user_id`). As a cheap DB-side backstop, consider Postgres RLS policies later
  *in addition* (not instead) — but v1 correctness rests on the app layer. Add a
  test that asserts user A cannot read user B's rows across every RPC.

> **Note the two orthogonal isolation axes.** This §3 isolation (which *user* owns
> the data) is distinct from the [`01` §4](01-architecture.md) **write-lease**
> (which *host* may write for a project) and [`10` §2](10-memory.md) **name
> partitioning** (which *agent* owns a memory stream). Auth/tenant isolation gates
> *visibility*; the lease gates *write authority*; the name gates *stream
> ownership*. All three are needed; they solve different axes.

---

## 4. Encryption — in transit & at rest

### 4.1 In transit

- **TLS 1.3 on every seam.** Browser↔hub and host↔hub both run over HTTPS/HTTP2
  ([`03`](03-api-surfaces.md)); no plaintext transport, ever, including the
  held-open downlink server-stream.
- **TLS terminates at the managed ingress** (the generic public-cloud target,
  [`01` §7](01-architecture.md)); hub↔PG and hub↔blob use the provider's
  in-transit encryption. No hub-internal plaintext hop leaves the trust boundary.
- Certs are a **deploy parameter** (managed cert / ACME) — never committed (§7).

### 4.2 At rest

- **DB + blob at-rest encryption** via the managed provider (transparent volume /
  storage encryption). This is the baseline protection for the **hub-can-read**
  default (§2): an operator with disk access still needs the running system.
- **The encryption seam (§2.4)** sits *above* provider at-rest encryption: body
  blobs pass through `encrypt()` (identity by default) so client-side keys can be
  layered on later for true operator-blind storage.
- **Secrets are never at rest in the DB in plaintext** — PATs are **hashed**
  (§5); the OIDC client secret and PAT-hashing pepper live in the secrets manager
  (§6), not in PG or config.

### 4.3 Simplest vs. right

- **Simplest:** rely solely on provider at-rest encryption; store PATs plaintext.
  Cost: a DB dump = every host's identity, forever.
- **Right:** provider at-rest enc **and** app-level hashing of the crown-jewel
  secrets **and** an encryption seam for bodies.
- **Recommendation:** the right way — provider encryption is table-stakes and free;
  hashing PATs is mandatory (§5); the body seam is nearly free and unlocks §2's
  future ZK. Do all three.

---

## 5. PAT scoping, revocation & the auth boundary

Hosts authenticate their persistent connection with a **Personal Access Token**
([`02` §1.4](02-components.md)); browsers use OIDC. Auth gates the *connection*;
the lease gates the *write*.

### 5.1 PAT lifecycle

- **Minted per host, bound to an owner (and project scope).** `CreatePAT` /
  `ListPATs` / `RevokePAT` are browser RPCs ([`03` §2](03-api-surfaces.md)).
- **Shown exactly once at creation; stored only as a hash.** PG holds
  `hash(pat + pepper)` (pepper from the secrets manager, §6) — never the token.
  A DB compromise yields hashes, not usable tokens.
- **Instantly & individually revocable.** Because each host has its *own* PAT,
  revoking one host (lost laptop, leaked token) doesn't disrupt the others — the
  core reason we rejected a single shared secret ([`02` §1.4](02-components.md)).
- **Scope = owner + project binding**, enforced on the ingest path *before* the
  fence check (§3.1). v1 keeps scope coarse (per-owner, project-bound); finer
  capability scoping is YAGNI until multi-user sharing exists.
- **Rotation:** create-new → deploy → revoke-old (overlap window), no downtime.

### 5.2 Token-leak vectors — closed by construction

These are the concrete ways a token escapes, and how the design forecloses each:

| Vector | How it leaks | Mitigation |
|---|---|---|
| **CLI flag** (`--pat=…`) | Visible in `ps auxf`, shell history, **and the `Ctrl+\` incident snapshot** (which captures `ps auxf` + `/proc/<pid>` — see `CLAUDE.md`) | **Never accept a token on a flag.** Resolve it via the secrets path / env / config file (mode `0600`), matching the `.env` handling for `CLAUDE_CODE_OAUTH_TOKEN` (`CLAUDE.md` QUM-518). |
| **Token in URL** | Query strings land in access logs, proxy logs, browser history, `Referer` headers | **Never put a token in a URL.** PATs ride the `Authorization` header; OIDC uses cookies/bearer in headers. No `?token=` anywhere. |
| **Incident snapshots / logs** | The forensic bundle captures mcp-calls, process args, fds | Token never in argv (above); scrub/redact secret-shaped material from any hub log line; snapshots are gitignored forensic artifacts (`CLAUDE.md` hygiene) |
| **Committed to repo** | Token pasted into config that gets checked in | Secrets only via secrets manager / gitignored `.env` (§6, §7); `buf`/CI has no token; nothing instance-specific committed |

> **Design rule (single sentence):** *a secret only ever exists in a header, an
> env var, a `0600` file, or the secrets manager — never in an argv, a URL, a log,
> or a committed file.*

---

## 6. Secret handling

- **`gocloud.dev/secrets`** ([`02` §1.7](02-components.md)) is the single portable
  interface for runtime secret material: **OIDC client secret** and **PAT-hashing
  pepper**. Backed by the provider's secrets manager in prod; a local impl for
  tests/dev — so no cloud provider is baked into app code.
- **No secret in app config or PG plaintext.** Config carries *references*
  (secret names / URIs), resolved at runtime through the secrets path.
- **The `.env` shim pattern** already established for host-side auth
  (`CLAUDE.md` QUM-518: `scripts/run-claude` sources a `0600`, gitignored `.env`)
  is the model for how a host resolves its PAT locally — same hygiene, same
  never-committed guarantee.

**Simplest vs. right.** Simplest: env vars only, hardcode the pepper. Right:
`gocloud.dev/secrets` interface + provider backend + local fake.
**Recommendation:** the interface now — it's cheap, keeps the multi-cloud
portability promise ([`01` §7](01-architecture.md)), and means test/dev never
touch a real secret store.

---

## 7. Public-repo hygiene as a build-time constraint

The repo is **public** (`github.com/dmotles/sprawl`). Security here is partly a
*build-time* property: the source tree must never leak the maintainer's (or any
user's) deployment specifics. Per `CLAUDE.md`'s Public-vs-Private section:

- **No default/hardcoded hub endpoint** in code ([`01` §3](01-architecture.md)) —
  connecting is opt-in via `--hub-url`/env/config. Absent config, the hub client
  never starts.
- **No IdP identity, tenant, host alias, or customer name** committed. The IdP is
  a **deploy parameter** ([`02` §1.4](02-components.md)); "Azure" appears only as a
  generic public-cloud target, never attributed to an organization.
- **`host_id`/`run_id` are opaque generated identifiers** — never a hostname,
  username, MAC, or machine description ([`10` §3 hygiene note](10-memory.md)).
- **Everything deployment-specific is parameterized** (Terraform variables,
  [`01` §7](01-architecture.md)); IaC ships the *shape*, not the *values*.
- **Forensic/incident artifacts stay gitignored** and default to unsanitized-⇒-not-committed.
- **CI has no live secret.** `buf`/lint/test run without any real endpoint, IdP,
  or token; e2e against a live hub uses local fakes ([`12`](README.md)).

This is enforceable in review: a reviewer must refuse to merge anything that names
an internal system/host/tenant or bakes in an endpoint/IdP (`CLAUDE.md` reviewer
rule). It's a security control, not just style.

---

## Open Questions

- **Content-trust default per *user* vs per *project*:** v1 puts the opt-out at
  project granularity — is a hub-wide "never store bodies" posture also needed for
  a user who wants blanket privacy but doesn't want to self-host?
- **Encryption-seam key model (when ZK is switched on):** per-`(project,agent)`
  key, per-project key, or per-user master key? Where do keys live, how do they
  sync across the user's devices, and what's the *recovery* story (lose key = lose
  memory)? — the hard part of ZK, deliberately deferred but flagged.
- **Metadata leakage under ZK:** even with encrypted bodies, PG holds provenance
  (agent names, timestamps, `host_id`, unit counts, sizes). Is that metadata
  itself sensitive enough to require encryption/obfuscation for a true ZK claim?
- **Transcript vs. distilled-unit redaction:** transcripts are far more likely to
  contain raw secrets/PII than curated units ([`10` OQ](10-memory.md#open-questions)).
  Do we need a redaction/scrubbing pass on ingest, or a stricter retention window
  for transcripts specifically?
- **OIDC allowlist management:** how is the user allowlist ([`02` §1.4](02-components.md))
  administered — static config, a bootstrap admin, self-service request+approve?
  And how does allowlist removal interact with data the removed user owns?
- **PAT scope granularity:** is per-owner + project-binding enough, or will the
  north-star (user-addressable sub-agents, browser write authority —
  [`01` OQ](01-architecture.md#open-questions)) force capability-scoped tokens?
- **At-rest encryption of the event log in PG:** provider transparent encryption
  covers the volume, but should event bodies (same content class as transcripts)
  also ride the §2.4 encryption seam, or is the log inherently ephemeral enough
  (snapshot-compacted) to leave as provider-encrypted only?
- **Browser-side content exposure:** even with a self-hosted or ZK hub, the SPA
  renders plaintext in the browser — is there a shared/kiosk-device threat worth a
  client-side session timeout / no-persistence mode?
