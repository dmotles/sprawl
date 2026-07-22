# All values are parameterized. Real values live ONLY in gitignored
# terraform.tfvars (see terraform.tfvars.example). Nothing Azure-account-specific
# or employer-specific is committed (public repo). Identifying inputs (ids,
# names, region) intentionally have NO defaults so an apply fails loudly rather
# than inheriting a wrong value.

# ---------------------------------------------------------------------------
# Global / identity
# ---------------------------------------------------------------------------

variable "subscription_id" {
  type        = string
  description = "Azure subscription id to pin ALL resources to. Gitignored value; never committed."
}

variable "location" {
  type        = string
  description = "Azure region (from gitignored tfvars). Never hardcoded in committed IaC."
}

variable "tags" {
  type        = map(string)
  description = "Mandatory policy tags. Keys owner/long_running/department/purpose — applied to the RG AND every resource (Azure tags do NOT inherit). Values from gitignored tfvars."

  validation {
    condition     = alltrue([for k in ["owner", "long_running", "department", "purpose"] : contains(keys(var.tags), k)])
    error_message = "tags must include all four mandatory keys: owner, long_running, department, purpose."
  }
}

variable "resource_group_name" {
  type        = string
  description = "Dedicated, stand-alone resource group for the Phase-0 hub (NOT the state-backend RG, NOT the spike RG)."
}

# ---------------------------------------------------------------------------
# Container host (Log Analytics + Container App Environment + ACR + UAMI + app)
# ---------------------------------------------------------------------------

variable "log_analytics_name" {
  type        = string
  description = "Log Analytics workspace name (backing for the Container App Environment + container stdout)."
}

variable "environment_name" {
  type        = string
  description = "Container App Environment name (managed Envoy ingress)."
}

variable "acr_name" {
  type        = string
  description = "Azure Container Registry name (globally unique, 5-50 lowercase alphanumerics)."
}

variable "container_app_name" {
  type        = string
  description = "Container App name for hubd."
}

variable "uami_name" {
  type        = string
  description = "User-assigned managed identity name (ACR pull + Key Vault read + blob access)."
}

variable "container_image" {
  type        = string
  description = "hubd image reference. Defaults to a PUBLIC placeholder so plan/validate work before the ACR image exists; override at apply with the real ACR image."
  default     = "mcr.microsoft.com/k8se/quickstart:latest"
}

variable "min_replicas" {
  type        = number
  description = "Minimum replicas. v1 single-instance."
  default     = 1
}

variable "max_replicas" {
  type        = number
  description = "Maximum replicas. v1 single-instance."
  default     = 1
}

variable "log_retention_days" {
  type        = number
  description = "Log Analytics retention window in days."
  default     = 30
}

# ---------------------------------------------------------------------------
# Database (managed Postgres)
# ---------------------------------------------------------------------------

variable "postgres_server_name" {
  type        = string
  description = "PostgreSQL Flexible Server name (globally unique)."
}

variable "postgres_version" {
  type        = string
  description = "PostgreSQL major version."
  default     = "16"
}

variable "postgres_size" {
  type        = string
  description = "PostgreSQL instance SKU (smallest burstable by default)."
  default     = "B_Standard_B1ms"
}

variable "postgres_storage_mb" {
  type        = number
  description = "PostgreSQL provisioned storage in MB."
  default     = 32768
}

variable "backup_retention_days" {
  type        = number
  description = "PostgreSQL automated-backup retention window in days (infra DR backstop)."
  default     = 7
}

variable "postgres_admin_login" {
  type        = string
  description = "PostgreSQL admin login. Not a secret; the password is generated in-TF and injected as the DSN secret."
  default     = "hubadmin"
}

variable "postgres_database_name" {
  type        = string
  description = "Logical database name."
  default     = "hub"
}

# ---------------------------------------------------------------------------
# Networking (private Postgres VNet integration; public ACA ingress preserved)
# ---------------------------------------------------------------------------
# The DB is fully private (VNet-injected, no public access); the app keeps public
# HTTPS ingress and reaches the DB over the VNet. Names default (RG-scoped, not
# globally unique). CIDR defaults are verified non-overlapping.

variable "vnet_name" {
  type        = string
  description = "Virtual network name backing private Postgres + ACA VNet integration."
  default     = "hub-vnet"
}

variable "vnet_address_space" {
  type        = list(string)
  description = "VNet address space. Must contain both subnet prefixes without overlap."
  default     = ["10.100.0.0/16"]
}

variable "aca_infra_subnet_name" {
  type        = string
  description = "ACA infrastructure subnet name. Workload-profile env: /27 minimum, delegated to Microsoft.App/environments."
  default     = "hub-aca-infra"
}

variable "aca_infra_subnet_prefix" {
  type        = string
  description = "ACA infra subnet CIDR (>= /27 for a workload-profile ACA environment, delegated to Microsoft.App/environments)."
  default     = "10.100.0.0/27"
}

variable "postgres_subnet_name" {
  type        = string
  description = "Postgres delegated subnet name (delegated to Microsoft.DBforPostgreSQL/flexibleServers)."
  default     = "hub-postgres"
}

variable "postgres_subnet_prefix" {
  type        = string
  description = "Postgres delegated subnet CIDR. Must not overlap the ACA /27."
  default     = "10.100.2.0/24"
}

variable "postgres_dns_zone_label" {
  type        = string
  description = "Leftmost label of the private DNS zone <label>.private.postgres.database.azure.com."
  default     = "hub"
}

# ---------------------------------------------------------------------------
# Object store (blob)
# ---------------------------------------------------------------------------

variable "storage_account_name" {
  type        = string
  description = "Storage account name (globally unique, 3-24 lowercase alphanumerics)."
}

variable "blob_container_name" {
  type        = string
  description = "Blob container (logical bucket) for snapshots/attachments."
  default     = "hub-blobs"
}

variable "blob_versioning_enabled" {
  type        = bool
  description = "Enable blob versioning for snapshot integrity."
  default     = true
}

variable "blob_lifecycle_days" {
  type        = number
  description = "Delete blobs older than N days. 0 disables the lifecycle rule (keep-forever backstop)."
  default     = 0
}

# ---------------------------------------------------------------------------
# Secrets store (Key Vault)
# ---------------------------------------------------------------------------

variable "key_vault_name" {
  type        = string
  description = "Key Vault name (globally unique, 3-24 chars)."
}

# ---------------------------------------------------------------------------
# App config (non-secret env)
# ---------------------------------------------------------------------------

variable "hub_url" {
  type        = string
  description = "Optional external hub URL advertised to clients (SPRAWL_HUB_URL). Leave empty to omit — set out-of-band once the FQDN/custom domain is known (avoids a self-referential apply cycle)."
  default     = ""
}

variable "debug_endpoint" {
  type        = bool
  description = "Enable the gated /debug/state endpoint (SPRAWL_HUB_DEBUG_ENDPOINT)."
  default     = false
}
