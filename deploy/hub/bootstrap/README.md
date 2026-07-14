# Hub state-backend bootstrap (QUM-870)

Stands up the **remote Terraform state backend** every hub Terraform root
depends on. This is the one-time chicken-and-egg step from
[`docs/design/hub/06-iac.md` §6](../../../docs/design/hub/06-iac.md#6-bootstrap--state-backend):
Terraform state must live in Azure Blob Storage, **never** on local disk or in
this (public) repo.

`bootstrap.sh` uses imperative `az` CLI calls (no Terraform, so no state file is
produced for this step) to create, **idempotently**:

- a **dedicated, net-new** resource group for the state backend only;
- a **StorageV2** storage account — TLS1.2 min, HTTPS-only, public blob access
  **off**, infrastructure encryption required, **shared-key (account-key/SAS)
  auth disabled** (AAD-only), **blob versioning on**;
- a private `tfstate` blob container.

All resources are tagged from the policy-mandated tag set in your config.

## Hygiene (PUBLIC repo)

Nothing instance-specific is committed. Real values (subscription, RG/account
names, region, tag values) live **only** in gitignored files:

| file | committed? | contents |
|---|---|---|
| `bootstrap.sh` | ✅ | fully parameterized script |
| `bootstrap.env.example` | ✅ | placeholders only |
| `bootstrap.env` | ❌ gitignored | your real values |
| `../infra/terraform/backend-config.hcl.example` | ✅ | placeholders only |
| `../infra/terraform/backend-config.hcl` | ❌ gitignored | real backend values |
| Terraform state | ❌ remote only | in the `tfstate` container |

## Run

```bash
cd deploy/hub/bootstrap
cp bootstrap.env.example bootstrap.env   # then fill in real values
./bootstrap.sh                           # idempotent; safe to re-run
```

Requires an authenticated `az` (`az login`) and a **`SUBSCRIPTION_ID`** in your
`bootstrap.env` — every `az` call is pinned to it with `--subscription`, and the
script exports it as `ARM_SUBSCRIPTION_ID` so the Terraform azurerm
backend/provider bind the same subscription. This is a hard rule: without an
explicit pin, `az`/Terraform fall back to the CLI's active subscription, which
can silently target the wrong one. The container step uses `--auth-mode login`,
which needs the **Storage Blob Data Contributor** data-plane role on the account
(control-plane Owner/Contributor is not enough); the script retries to absorb
RBAC propagation lag right after account creation.

Re-running only re-converges tags and blob versioning; existing resources are
not re-created. The account's hardening flags (TLS floor, public-access-off,
infrastructure encryption, shared-key-access-off) are set at **create** time and
not re-asserted on re-run — safe here because the RG is dedicated and net-new.
If those were ever
flipped out-of-band, recreate the account rather than relying on a re-run.

## Wire a Terraform root to the backend

```bash
cd ../infra/terraform
cp backend-config.hcl.example backend-config.hcl   # fill in real values
cd _backend-smoke
terraform init -backend-config=../backend-config.hcl
test ! -e terraform.tfstate    # state is remote-only
```

`_backend-smoke/` is a throwaway root (no resources, just an output) used to
verify the backend binds and no local state is produced. `backend-config.hcl`
may also carry `subscription_id`; if omitted, the `ARM_SUBSCRIPTION_ID` exported
by the bootstrap is used. A real root that declares `azurerm` resources should
copy `../infra/terraform/providers.tf.example` to pin `subscription_id` on the
provider too (the pattern the first concrete root — QUM-871 — inherits).

## Tests

`scripts/test-hub-bootstrap.sh` exercises `bootstrap.sh` against a fake `az`
shim (no cloud): config refusal, hardening flags, mandatory tags, create/converge
idempotency, no-leak grep, and gitignore coverage. Run: `make test-hub-bootstrap`.
