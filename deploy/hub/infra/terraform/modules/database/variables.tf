# database capability — Azure Database for PostgreSQL Flexible Server.
#
# Contract (stable across clouds — see ../README.md):
#   in : version, size, retention_days
#   out: conn-ref, host
#
# The admin password is GENERATED in-TF (random_password) and never a committed
# value nor an operator-supplied input variable — the resulting DSN (conn_ref)
# is a sensitive output the composing root writes into the secrets store.

variable "name" {
  type        = string
  description = "PostgreSQL Flexible Server name (globally unique)."
}

variable "resource_group_name" {
  type        = string
  description = "Resource group the server lives in."
}

variable "location" {
  type        = string
  description = "Azure region. Never hardcoded in committed IaC — sourced from gitignored tfvars."
}

# Contract input "version" — named engine_version here because "version" is a
# reserved variable name inside Terraform module blocks.
variable "engine_version" {
  type        = string
  description = "PostgreSQL major version (contract input \"version\")."
  default     = "16"
}

variable "size" {
  type        = string
  description = "Instance SKU (contract input \"size\"). Smallest burstable by default."
  default     = "B_Standard_B1ms"
}

variable "storage_mb" {
  type        = number
  description = "Provisioned storage in MB."
  default     = 32768
}

variable "retention_days" {
  type        = number
  description = "Automated-backup retention window in days (contract input \"retention_days\"). Infra DR backstop beneath the app's logical retention."
  default     = 7
}

variable "administrator_login" {
  type        = string
  description = "Admin login name. Not a secret; the password is generated in-TF."
  default     = "hubadmin"
}

variable "database_name" {
  type        = string
  description = "Logical database created on the server."
  default     = "hub"
}

variable "public_network_access_enabled" {
  type        = bool
  description = "Whether the server accepts public network connections. Phase-0 default true (with an Azure-services firewall rule); private VNet integration is a future hardening (docs 06 §5.2)."
  default     = true
}

variable "tags" {
  type        = map(string)
  description = "Mandatory policy tags (owner/long_running/department/purpose)."
}
