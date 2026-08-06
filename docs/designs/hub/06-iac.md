# 06 — Infrastructure as Code (IaC)

*How the hub's cloud footprint is provisioned: Terraform, fully parameterized,
`azure/` first with the AWS door left open.*

See also: [`01-architecture.md`](01-architecture.md) · [`07` storage-persistence](README.md) ·
[`08` deployment](README.md) · [`04` authentication](README.md) · [index](README.md)

---

## 1. Scope

This doc covers **only** the cloud infrastructure the hub needs, expressed as
Terraform. It does *not* cover the container image build, the app config
plumbing, or the DB schema (those live in [`08`](README.md) and
[`07`](README.md)). The line is: **IaC provisions the boxes; deployment fills
them.**

What the IaC provisions (per [`01` §7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs)):

```
┌──────────────────────── one cloud target (azure/ first) ───────────────────────┐
│                                                                                 │
│   ┌──────────────┐   ┌───────────────┐   ┌──────────────┐   ┌───────────────┐  │
│   │ container    │   │ managed        │   │ object-store │   │ secrets store │  │
│   │ host         │──▶│ Postgres       │   │ bucket       │   │ (KV / vault)  │  │
│   │ (runs the    │   │ (log+registry  │   │ (snapshots,  │   │ PAT pepper,   │  │
│   │  Go image)   │   │  +metadata)    │   │  attachments)│   │ OIDC secret,  │  │
│   └──────────────┘   └───────────────┘   └──────────────┘   │  DB creds)    │  │
│         │                    ▲                    ▲          └───────────────┘  │
│         └─ reads creds/secrets from secrets store ┘  (managed identity, no     │
│                                                       secrets in tfvars/state) │
└─────────────────────────────────────────────────────────────────────────────┘
```

Five resource classes: **container host**, **managed Postgres**, **object-storage
bucket**, **secrets store**, plus the **retention/GC defaults** stamped onto
Postgres + the bucket (§5).

## 2. Why Terraform (build vs. adopt)

- **Simplest way:** hand-click the cloud console, or a shell script of `az …`
  CLI calls. Cost: no state tracking, no diff/plan preview, no reproducibility,
  no clean multi-cloud story, drift goes undetected.
- **Right way:** Terraform with a remote state backend and a plan/apply gate.
  Cost: learning Terraform + one bootstrap step for the state backend.
- **Recommendation:** **Terraform.** It's the de-facto standard, matches the
  "adopt the plumbing" stance in [`00`](00-overview.md#prior-art--build-vs-adopt),
  and its provider model is exactly what keeps the AWS door open (§3) without
  rewriting the app. The maintainer runs this rarely (stand up a hub once, tweak
  occasionally), so a heavyweight CD pipeline is YAGNI — a local
  `terraform plan/apply` against a remote state backend is enough for v1.

## 3. Module layout (keep the AWS door open)

The organizing rule: a **cloud-agnostic root that wires together a set of
capability modules**, with **one provider-specific implementation per capability
per cloud**. `azure/` ships first; `aws/` is a *future sibling*, not built now.

```
infra/terraform/
├── modules/                     # provider-agnostic CONTRACTS (input/output shape)
│   ├── container-host/          # var: image, cpu, mem, port, env-refs → out: url, identity-id
│   ├── database/                # var: version, size, retention_days → out: conn-ref, host
│   ├── object-store/            # var: name, lifecycle_days → out: bucket-ref
│   └── secrets/                 # var: names[] → out: store-ref, secret-refs
│
├── azure/                       # ← BUILT FIRST: azure impls of each capability
│   ├── main.tf                  # instantiates the four capability modules (azure flavor)
│   ├── variables.tf             # all inputs, no defaults for instance-specific values
│   ├── outputs.tf               # hub URL, DB ref, bucket ref, secret refs
│   ├── providers.tf             # azurerm provider + remote state backend config
│   ├── versions.tf              # required_providers + TF version pin
│   └── terraform.tfvars.example # COMMITTED template; real values live in gitignored .tfvars
│
└── aws/                         # ← FUTURE (do NOT build now): same capability set, aws impls
    └── (README stub only: "mirror azure/, swap azurerm→aws provider")
```

Two viable structures for the provider split — the choice is what actually keeps
the door open cheaply:

- **Simplest way:** *no shared `modules/` layer* — just an `azure/` dir of flat
  resources. When AWS is wanted, copy-paste `azure/` → `aws/` and rewrite.
  Cost: the two clouds drift; there's no enforced common contract; the app-facing
  outputs (hub URL, DB ref, bucket ref, secret refs) can diverge in name/shape.
- **Right way:** a thin **`modules/` capability layer** defining the *contract*
  (input variables + output names) that every cloud implements identically, with
  `azure/` and (later) `aws/` as thin instantiation roots. Cost: one extra
  indirection layer up front, mild over-engineering for a single cloud.
- **Recommendation:** **middle path — ship `azure/` as the first concrete root
  now, and define the four capability contracts as the seam.** Because the app
  consumes infra only through a fixed set of outputs (hub URL, a DB
  connection *reference*, a bucket *reference*, secret *references* — all
  resolved at runtime via `gocloud.dev`, per [`02` §1.7](02-components.md)),
  the "contract" is really just *a stable set of output names*. Standardize
  those output names now; the `modules/` layer can start as documentation of that
  contract and harden into real reusable modules only when `aws/` is actually
  built. **YAGNI guardrail:** don't build `aws/` or fully abstract modules
  speculatively — just don't hardcode anything that would make adding `aws/` a
  rewrite. The `gocloud.dev` blob/secrets portability ([`01` §7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs))
  is what makes this cheap: the *app* is already cloud-agnostic, so IaC only has
  to match a small output contract.

## 4. Parameterization & secret hygiene (hard requirement)

**Nothing instance-specific is committed.** This is a public repo
([`README` conventions](README.md), [`CLAUDE.md`](../../../CLAUDE.md) public-repo
hygiene) — no endpoints, tenant IDs, subscription IDs, resource-group names,
region choices, or org names land in tracked files.

| Artifact | Committed? | Contains |
|---|---|---|
| `variables.tf` | ✅ yes | variable *declarations* + descriptions; **no instance defaults** for sensitive/identifying inputs |
| `terraform.tfvars.example` | ✅ yes | placeholder values only (`region = "<your-region>"`, `db_size = "..."`) |
| `terraform.tfvars` (real) | ❌ **gitignored** | actual region, sizes, names, IdP issuer URL, allowlist |
| Terraform **state** | ❌ **remote backend** | never local, never committed; holds resolved values |
| Secret *values* (DB password, PAT pepper, OIDC client secret) | ❌ never in TF at all | generated in-cloud / injected; only *references* flow to the app |

Rules that fall out of this:

- **No hardcoded endpoints.** The hub URL is a Terraform *output*, not an input;
  hosts learn it via explicit `--hub-url`/env/config ([`01` §3](01-architecture.md#3-connected-vs-disconnected),
  [`02` §2.6](02-components.md)). There is **no default hub endpoint anywhere in
  the tree** — infra included.
- **Secrets are born in the cloud, not in Terraform.** Prefer resources that
  *generate* a secret (e.g. a managed Postgres admin password created by the
  provider, a random pepper via `random_password`) and write it straight into the
  **secrets store**, so the plaintext never sits in a `.tfvars`. The app reads it
  at runtime through `gocloud.dev/secrets` ([`02` §1.7](02-components.md)).
  - *Caveat (call out honestly):* Terraform **state still records generated
    secret values**. That's why the state backend must be remote + encrypted +
    access-controlled, and why the truly sensitive material (OIDC client secret
    from the IdP) is ideally *set directly in the secrets store out-of-band* and
    only *referenced* by name in TF (`data` lookup), never *created* by TF.
- **IdP is a deploy parameter.** The OIDC issuer URL / client ID are `.tfvars`
  inputs (gitignored), never committed — matches [`04`](README.md)'s "IdP is a
  deploy parameter, never hardcoded."
- **App→cloud auth via managed identity**, not a stored credential where the
  platform supports it: the container host gets a managed identity that's granted
  read on the secrets store and the bucket, so the *only* bootstrap secret is the
  one the platform injects. Simplest fallback (connection strings in the secrets
  store) stays available where managed identity isn't practical.

### Simplest vs. right — where do secrets live?

- **Simplest:** put every secret in a gitignored `secrets.tfvars`, pass with
  `-var-file`. Cost: plaintext on the operator's disk; easy to fat-finger into a
  commit; rotation is manual.
- **Right:** generate/inject into the cloud secrets store; app resolves at
  runtime; TF holds only references + (unavoidably) generated values in encrypted
  remote state. Cost: one out-of-band step for externally-issued secrets (OIDC).
- **Recommendation:** **secrets store + references; generate in-cloud where
  possible; remote encrypted state.** The one manual out-of-band step (drop the
  OIDC client secret into the store) is worth it to keep the IdP secret out of
  state entirely.

## 5. What each resource provisions (+ retention/GC defaults)

Retention/GC is **coordinated conceptually** with [`07` storage-persistence](README.md)
and the sync retention model in [`09` §2](09-synchronization.md). IaC's job is to
set the *infrastructure-level* floors/defaults; the app enforces the
*logical* retention (ack-watermark/snapshot trimming) on top. Two layers,
deliberately: infra defaults are a **backstop**, not the primary policy.

### 5.1 Container host
Runs the single Go image ([`02` §1`](02-components.md), [`08`](README.md)).
Parameters: image ref, CPU/mem, listen port, min/max scale (v1: **single
instance**, scale-to-one — the persistent bidi connections and in-memory
lease/fan-out state make horizontal scaling a non-goal for v1; see Open
Questions), managed identity, and env/secret references. Outputs the **hub URL**.

- **Simplest:** a single always-on VM running the container. Cost: OS patching,
  no managed TLS, manual restarts.
- **Right:** a managed container service (serverless container / app platform)
  with platform TLS + rolling deploy + injected identity.
- **Recommendation:** **managed container service, single instance.** Offloads
  TLS and patching; single-instance keeps the connection/lease model simple
  ([`01` §4](01-architecture.md#4-identity-lease--fencing-conceptual--detail-in-doc-10--09)).

### 5.2 Managed Postgres
Holds the event-log, lease/fence registry, hashed PATs, and metadata
([`01` §7](01-architecture.md#7-stack-at-a-glance-rationale-validated-in-leaf-docs),
[`07`](README.md)). Parameters: engine version, instance size, storage size,
**backup-retention days**, whether public network access is off (prefer
private + managed-identity/allowlist).

- **Retention/GC defaults IaC sets:** the **automated-backup retention window**
  (a `retention_days` variable, default a modest value like 7 — a parameter, not
  baked) and **storage auto-grow** on/off. IaC does **not** set the *logical*
  log-trim policy — that's the app's ack/snapshot-watermark GC
  ([`09` §2](09-synchronization.md)). Infra backups are the disaster-recovery
  backstop *beneath* logical retention.
- **Simplest:** smallest instance, backups off, public access. Cost: data-loss
  risk, exposed surface.
- **Right:** right-sized instance, private networking, backups on with a
  parameterized window, storage auto-grow.
- **Recommendation:** **backups on (parameterized window), private networking,
  storage auto-grow on.** Cheap insurance; all knobs are `.tfvars` inputs.

### 5.3 Object-storage bucket
Holds session/memory **snapshots** and **attachments**
([`02` §1.7](02-components.md), [`10` §](10-memory.md), attachments doc).
Accessed via `gocloud.dev/blob` so the bucket is just a reference to the app.
Parameters: name/prefix, versioning on/off, **lifecycle (GC) rules**, private
access (no public read).

- **Retention/GC defaults IaC sets:** **lifecycle rules** — e.g. expire
  objects under a `transcripts/` or `attachments/` prefix after a parameterized
  `lifecycle_days`, and (optionally) transition older snapshots to a cheaper
  storage tier. These are the storage-side backstop for the transcript-retention
  window discussed in [`10` §](10-memory.md) ("retain transcripts, opt-in +
  retention-bounded"). Again: infra lifecycle rule = coarse backstop; the app's
  snapshot-compaction ([`09` §3](09-synchronization.md)) is the fine-grained
  policy.
- **Simplest:** one bucket, no versioning, no lifecycle — keep everything
  forever. Cost: unbounded cost growth; no protection against accidental
  overwrite.
- **Right:** versioning on for snapshot integrity + lifecycle rules to bound
  cost, private-only, parameterized windows.
- **Recommendation:** **private bucket, lifecycle rules on (parameterized days),
  versioning on for the snapshot prefix.** Keeps blob cost bounded without the
  app having to reason about physical GC.

### 5.4 Secrets store
Holds runtime secret material: PAT-hashing pepper, OIDC client secret, DB
credentials (where not via managed identity) — see
[`02` §1.7](02-components.md), [`04`](README.md). Parameters: store name, the
*set of secret names* to provision (values injected, not committed), and an
access grant to the container host's managed identity.

- **Recommendation:** provision the store + **empty/generated secret slots** and
  grant the host identity read access; **inject values out-of-band** (generated
  in-cloud or set manually for externally-issued secrets). No secret value is a
  Terraform *input variable*.

## 6. Bootstrap & state backend

One chicken-and-egg step: the **remote state backend** (a storage container for
TF state) must exist before `terraform init`.

- **Simplest:** local state file. Cost: not shareable, no locking, easy to lose,
  contains resolved secrets on the operator's disk — **rejected** for anything
  beyond a throwaway.
- **Right:** a tiny separate **bootstrap** config (or a one-line CLI/script) that
  creates the encrypted, access-controlled state container once; the main config
  then uses it as its backend with state locking.
- **Recommendation:** **a minimal `bootstrap/` step that stands up the remote
  encrypted state backend**, documented as a one-time prerequisite. Everything
  else runs against that backend with locking. This keeps state (and its
  unavoidable generated-secret values) off local disk and out of git.

## 7. Local dev / testing — no cloud required

Consistent with [`12` testability-local-dev](README.md) and
[`02` §1.7](02-components.md): the app's `gocloud.dev` abstractions mean **no
Terraform is needed to run the hub locally** — `fileblob`/`memblob`, a local
Postgres (or container), and env-var secrets back the tests. Terraform is only
for standing up a *real* cloud hub. Call this out so no one thinks the cloud
infra is on the critical path for development.

## Open Questions

- **Single-instance assumption:** v1 provisions one container instance (in-memory
  lease/fan-out state). At what point does multi-instance (and externalizing the
  fan-out/lease registry) become necessary, and does that change the IaC contract
  (load balancer, shared cache)? Flagged against the long-lived-connection
  viability risk in [`01` Open Questions](01-architecture.md#open-questions) /
  [`03`](README.md).
- **State backend location:** should the remote TF state backend live in the same
  cloud/account as the hub, or be deliberately separated for blast-radius? Affects
  the bootstrap step.
- **Generated secrets in state:** is remote encrypted state + access control
  sufficient, or do we want *all* sensitive material issued out-of-band (never
  generated by TF) to keep state fully secret-free — at the cost of more manual
  setup?
- **Managed identity vs. connection strings:** how uniformly can we rely on
  managed identity for host→(DB, bucket, secrets) across the first cloud, and
  does the fallback (connection strings in the secrets store) muddy the
  contract that `aws/` must later mirror?
- **Retention parameter ownership:** infra sets coarse backstops (backup window,
  bucket lifecycle) while the app sets logical retention
  ([`09` §2](09-synchronization.md)). Where should the *authoritative* default
  live so the two layers don't contradict — infra `.tfvars`, app config, or a
  single shared source?
- **AWS parity trigger:** what concrete event justifies actually building
  `aws/` (a second deploy target? a contributor on AWS?), and until then is the
  README stub + stable output-name contract enough to prevent `azure/` from
  accreting provider-specific assumptions?
- **Region/data-residency:** region is a parameter, but do snapshots/attachments
  (potentially containing transcript content, per [`10`](README.md)) carry
  residency constraints that IaC should encode (bucket region pinning)?
