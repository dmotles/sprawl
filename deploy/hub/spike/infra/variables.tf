# All values are parameterized — real values live ONLY in gitignored spike.tfvars
# (see spike.tfvars.example). Nothing Azure-account-specific is committed.

variable "subscription_id" {
  type        = string
  description = "Azure subscription id to pin all resources to. Gitignored value; never committed."
}

variable "location" {
  type        = string
  description = "Azure region (e.g. from gitignored tfvars). Never hardcoded in committed IaC."
}

variable "tags" {
  type        = map(string)
  description = "Mandatory resource tags (owner/long_running/department/purpose). Applied to the RG and every resource. Values from gitignored tfvars."
}

variable "resource_group_name" {
  type        = string
  description = "Dedicated, DISPOSABLE spike resource group — net-new, separate from the state-backend RG, so teardown is a single clean destroy."
}

variable "log_analytics_name" {
  type        = string
  description = "Log Analytics workspace name."
}

variable "environment_name" {
  type        = string
  description = "Container Apps Environment name."
}

variable "container_app_name" {
  type        = string
  description = "Container App name."
}

variable "acr_name" {
  type        = string
  description = "Throwaway Azure Container Registry name (globally unique, 5-50 alnum)."
}

variable "container_image" {
  type        = string
  description = "Server image reference. Defaults to a public placeholder so `terraform plan` succeeds before the ACR image exists; overridden at apply with the real ACR image."
  default     = "mcr.microsoft.com/k8se/quickstart:latest"
}

variable "heartbeat_interval_seconds" {
  type        = number
  description = "Heartbeat interval passed to the server container (the value under test; try ~20s and one other)."
  default     = 20
}
