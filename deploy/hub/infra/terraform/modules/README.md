# modules/ — capability contracts (doc-only stub)

Per [`docs/design/hub/06-iac.md` §3](../../../../docs/design/hub/06-iac.md), the
hub's IaC keeps the AWS door open via a thin capability-contract seam: a
cloud-agnostic set of module *contracts* (input variables + output names) that
each cloud implements identically.

**YAGNI:** these are intentionally **documentation only** for now. Real reusable
modules are hardened *when `aws/` is actually built* — not speculatively. The
four capabilities and their stable output names are:

| capability      | key inputs                          | key outputs        |
|-----------------|-------------------------------------|--------------------|
| `container-host`| image, cpu, mem, port, env-refs     | url, identity-id   |
| `database`      | version, size, retention_days       | conn-ref, host     |
| `object-store`  | name, lifecycle_days                | bucket-ref         |
| `secrets`       | names[]                             | store-ref, secret-refs |

The first concrete root (`azure/`) is not part of QUM-870; this issue only
stands up the remote state backend (`../bootstrap/` + `backend-config.hcl.example`).
