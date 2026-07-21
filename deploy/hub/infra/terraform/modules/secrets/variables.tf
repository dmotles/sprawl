# secrets capability — Azure Key Vault (RBAC-authorized).
#
# Contract (stable across clouds — see ../README.md):
#   in : names[]
#   out: store-ref, secret-refs
#
# HARD RULE: this module takes only secret NAMES, never secret VALUES. Slots
# named in var.names are provisioned EMPTY (placeholder) with their value ignored
# after creation, for pure out-of-band injection (`az keyvault secret set`).
# Secrets whose values are generated in-TF (DB DSN, host-token pepper, cookie
# key) are written by the composing root directly into this vault — see
# ../../azure/main.tf. The vault grants:
#   - reader_principal_id  → "Key Vault Secrets User"    (the app reads at runtime)
#   - officer_object_id    → "Key Vault Secrets Officer" (the deployer writes values)

variable "name" {
  type        = string
  description = "Key Vault name (globally unique, 3-24 chars)."
}

variable "resource_group_name" {
  type        = string
  description = "Resource group the vault lives in."
}

variable "location" {
  type        = string
  description = "Azure region. Never hardcoded in committed IaC."
}

variable "tenant_id" {
  type        = string
  description = "AAD tenant id for the vault. Sourced from the deployer's client config (data source), never committed."
}

variable "names" {
  type        = list(string)
  description = "Secret names to provision as EMPTY out-of-band slots (contract input \"names[]\"). Values are injected after apply; the slot value is ignored by TF."
  default     = []
}

variable "reader_principal_id" {
  type        = string
  description = "Principal (object) id granted Key Vault Secrets User — the app's managed identity."
}

variable "officer_object_id" {
  type        = string
  description = "Object id granted Key Vault Secrets Officer — the deployer identity that writes secret values (TF + `az keyvault secret set`)."
}

variable "tags" {
  type        = map(string)
  description = "Mandatory policy tags (owner/long_running/department/purpose)."
}
