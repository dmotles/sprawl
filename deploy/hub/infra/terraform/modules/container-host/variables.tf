# container-host capability — Azure Container Apps implementation.
#
# Contract (stable across clouds — see ../README.md):
#   in : image, cpu, mem, port, env-refs, identity, scale
#   out: url, identity-id
#
# This module owns ONLY the Container App. The Container App Environment, Log
# Analytics workspace, ACR, and the user-assigned identity are glue created by
# the composing root and passed in (the AcrPull / Key Vault grants must be
# created BEFORE the app, so the identity is provisioned outside this module).

variable "name" {
  type        = string
  description = "Container App name."
}

variable "resource_group_name" {
  type        = string
  description = "Resource group the Container App lives in."
}

variable "container_app_environment_id" {
  type        = string
  description = "ID of the Container App Environment (managed Envoy ingress + Log Analytics backing)."
}

variable "image" {
  type        = string
  description = "Container image reference (e.g. <acr>.azurecr.io/hubd:<tag>)."
}

variable "cpu" {
  type        = number
  description = "vCPU allocation for the container."
  default     = 0.25
}

variable "memory" {
  type        = string
  description = "Memory allocation (e.g. \"0.5Gi\"). Must pair with cpu per ACA's allowed combinations."
  default     = "0.5Gi"
}

variable "target_port" {
  type        = number
  description = "Container listen port (hubd listens on 8080)."
  default     = 8080
}

variable "env" {
  type        = map(string)
  description = "Plain (non-secret) environment variables: name => value."
  default     = {}
}

variable "secret_env" {
  type        = map(string)
  description = "Secret-backed environment variables: env var name => secret name (the secret must appear in var.secrets)."
  default     = {}
}

variable "secrets" {
  type = list(object({
    name                = string
    key_vault_secret_id = string
  }))
  description = "Key Vault-backed secret references resolved via the app's managed identity. No secret VALUES here — only KV references."
  default     = []
}

variable "identity_id" {
  type        = string
  description = "Resource ID of the user-assigned managed identity the app runs as (used for ACR pull + Key Vault secret resolution)."
}

variable "registry_server" {
  type        = string
  description = "ACR login server the app pulls the image from."
}

variable "min_replicas" {
  type        = number
  description = "Minimum replica count. v1 pins single-instance (min == max == 1)."
  default     = 1
}

variable "max_replicas" {
  type        = number
  description = "Maximum replica count. v1 pins single-instance (min == max == 1)."
  default     = 1
}

variable "revision_mode" {
  type        = string
  description = "Container App revision mode."
  default     = "Single"
}

variable "liveness_path" {
  type        = string
  description = "HTTP liveness probe path (process-health only, no external deps)."
  default     = "/healthz"
}

variable "readiness_path" {
  type        = string
  description = "HTTP readiness probe path (gates ingress routing on dependency health)."
  default     = "/readyz"
}

variable "tags" {
  type        = map(string)
  description = "Mandatory policy tags (owner/long_running/department/purpose). Azure tags do NOT inherit, so every resource carries them."
}
