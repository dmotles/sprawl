# modules/ — capability contracts

Per [`docs/design/hub/06-iac.md` §3](../../../../docs/design/hub/06-iac.md), the
hub's IaC keeps the AWS door open via a thin capability-contract seam: a
cloud-agnostic set of module *contracts* (input variables + output names) that
each cloud implements identically.

**These are currently AZURE-FLAVORED** (`azurerm` implementations), stood up by
QUM-879 alongside the first concrete root [`../azure/`](../azure/README.md). They
are the real, `terraform validate`-able modules that `../azure/main.tf`
composes. When a second deploy target justifies it, an `aws/` root mirrors these
same contracts with `aws`-provider implementations — **`aws/` stays a README
stub for now** (do not build it speculatively; YAGNI per §3).

## The cross-cloud seam = STABLE OUTPUT NAMES

The app consumes infra only through a fixed set of outputs (hub URL, a DB
connection *reference*, a bucket *reference*, secret *references* — all resolved
at runtime via `gocloud.dev`). The contract is therefore *a stable set of
output names*. **Do not let output names drift** — a future `aws/` container-host
must output `url` + `identity_id` exactly as the azure one does, or the roots
diverge and adding `aws/` becomes a rewrite.

| capability      | key inputs                                   | STABLE outputs            |
|-----------------|----------------------------------------------|---------------------------|
| `container-host`| image, cpu, memory, target_port, env/secret-refs, identity_id, scale | `url`, `identity_id` |
| `database`      | engine_version¹, size, retention_days        | `conn_ref`, `host`        |
| `object-store`  | name, lifecycle_days                         | `bucket_ref`              |
| `secrets`       | names[]                                      | `store_ref`, `secret_refs`|

¹ The contract input "version" is declared as `engine_version` because
`version` is a reserved variable name inside Terraform module blocks.

Modules also expose a few azure-specific helper outputs (e.g. `object-store`'s
`account_name`/`account_id`, `secrets`' `store_id`, `container-host`'s `fqdn`)
that the azure root wires internally. Those are NOT part of the cross-cloud
contract — only the names in the table above are load-bearing across clouds.

Likewise, the azure `database` implementation takes azure-specific
private-networking inputs (`delegated_subnet_id`, `private_dns_zone_id`) and is
**fully private** — VNet-injected with `public_network_access_enabled = false`,
no public firewall rule. The VNet/subnet/DNS substrate itself is root-level glue
(`../azure/networking.tf`), not a capability module. A future `aws/` database
would achieve the equivalent private posture with its own provider constructs
while preserving the same `conn_ref`/`host` outputs.

## Secret hygiene (HARD)

The `secrets` module takes only secret **names**, never **values**. Empty
out-of-band slots are provisioned with `ignore_changes = [value]`; generated
values (DB DSN, host-token pepper, cookie key) are written into the vault by the
composing root. No secret value is ever a committed literal or an operator-typed
input variable. See [`../azure/README.md`](../azure/README.md) for the base64
alphabet foot-gun (URL-safe pepper vs. standard cookie key).
