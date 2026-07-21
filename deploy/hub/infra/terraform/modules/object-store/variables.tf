# object-store capability — Azure Storage account + blob container.
#
# Contract (stable across clouds — see ../README.md):
#   in : name, lifecycle_days
#   out: bucket-ref
#
# Private access only (no public read), AAD-only (shared key disabled). The app
# reaches blobs via gocloud.dev/blob (azblob://) authenticated by the container's
# managed identity — the composing root grants it Storage Blob Data Contributor.

variable "name" {
  type        = string
  description = "Storage account name (globally unique, 3-24 lowercase alphanumerics) — contract input \"name\"."
}

variable "resource_group_name" {
  type        = string
  description = "Resource group the storage account lives in."
}

variable "location" {
  type        = string
  description = "Azure region. Never hardcoded in committed IaC."
}

variable "container_name" {
  type        = string
  description = "Blob container (the logical bucket)."
  default     = "hub-blobs"
}

variable "versioning_enabled" {
  type        = bool
  description = "Blob versioning for snapshot integrity."
  default     = true
}

variable "lifecycle_days" {
  type        = number
  description = "Delete blobs older than N days (contract input \"lifecycle_days\"). 0 disables the lifecycle rule (keep-forever backstop; docs 06 §5.3 plan §4)."
  default     = 0
}

variable "tags" {
  type        = map(string)
  description = "Mandatory policy tags (owner/long_running/department/purpose)."
}
