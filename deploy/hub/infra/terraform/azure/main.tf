# Phase-0 hub — a real ACA deployment of hubd. This root wires four capability
# modules (../modules/) plus the glue Azure needs (RG, Log Analytics, Container
# App Environment, ACR, and the user-assigned identity that ties pull + secret +
# blob access together). See docs/design/hub/06-iac.md and 08-deployment.md.

# Deployer identity — tenant + object id for the Key Vault Secrets Officer grant.
# Resolved at apply from the caller's credentials; never committed.
data "azurerm_client_config" "current" {}

# Dedicated, stand-alone resource group for the Phase-0 hub.
resource "azurerm_resource_group" "hub" {
  name     = var.resource_group_name
  location = var.location
  tags     = var.tags
}

# Log Analytics — backing for the Container App Environment and the destination
# for container stdout (hubd logs JSON).
resource "azurerm_log_analytics_workspace" "hub" {
  name                = var.log_analytics_name
  resource_group_name = azurerm_resource_group.hub.name
  location            = azurerm_resource_group.hub.location
  sku                 = "PerGB2018"
  retention_in_days   = var.log_retention_days
  tags                = var.tags
}

# Container App Environment (managed Envoy ingress). VNet-injected via the
# infrastructure subnet so the app can reach the private Postgres server over the
# VNet. internal_load_balancer_enabled = false keeps PUBLIC HTTPS ingress — only
# the DB is private; the browser + host dial-out still reach the hub from the
# internet. The Consumption workload_profile pins the env to the workload-profiles
# platform, which REQUIRES the infra subnet be delegated to Microsoft.App/
# environments and allows a /27 minimum (see networking.tf).
resource "azurerm_container_app_environment" "hub" {
  name                           = var.environment_name
  resource_group_name            = azurerm_resource_group.hub.name
  location                       = azurerm_resource_group.hub.location
  log_analytics_workspace_id     = azurerm_log_analytics_workspace.hub.id
  infrastructure_subnet_id       = azurerm_subnet.aca_infra.id
  internal_load_balancer_enabled = false
  tags                           = var.tags

  workload_profile {
    name                  = "Consumption"
    workload_profile_type = "Consumption"
  }
}

# Registry for the hubd image. AAD-only (admin user disabled); the app pulls via
# the user-assigned identity + AcrPull below.
resource "azurerm_container_registry" "hub" {
  name                = var.acr_name
  resource_group_name = azurerm_resource_group.hub.name
  location            = azurerm_resource_group.hub.location
  sku                 = "Basic"
  admin_enabled       = false
  tags                = var.tags
}

# User-assigned identity used by the Container App to pull from the ACR, read
# Key Vault secrets, and access blobs. Using a UAMI (not the app's system
# identity) lets the AcrPull + KV grants be created BEFORE the Container App, so
# the first apply pulls the real image without a chicken-and-egg ordering.
resource "azurerm_user_assigned_identity" "hub" {
  name                = var.uami_name
  resource_group_name = azurerm_resource_group.hub.name
  location            = azurerm_resource_group.hub.location
  tags                = var.tags
}

# Let the identity pull from the ACR. Created before the Container App so the
# image pull succeeds on first provision (the load-bearing pull pattern).
resource "azurerm_role_assignment" "acr_pull" {
  scope                = azurerm_container_registry.hub.id
  role_definition_name = "AcrPull"
  principal_id         = azurerm_user_assigned_identity.hub.principal_id
}

# Let the identity read/write blobs via AAD (gocloud.dev/blob azblob://).
resource "azurerm_role_assignment" "blob_contributor" {
  scope                = module.object_store.account_id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = azurerm_user_assigned_identity.hub.principal_id
}

# ---------------------------------------------------------------------------
# Capability modules
# ---------------------------------------------------------------------------

module "database" {
  source = "../modules/database"

  name                  = var.postgres_server_name
  resource_group_name   = azurerm_resource_group.hub.name
  location              = azurerm_resource_group.hub.location
  engine_version        = var.postgres_version
  size                  = var.postgres_size
  storage_mb            = var.postgres_storage_mb
  retention_days        = var.backup_retention_days
  administrator_login   = var.postgres_admin_login
  database_name         = var.postgres_database_name
  delegated_subnet_id   = azurerm_subnet.postgres.id
  private_dns_zone_id   = azurerm_private_dns_zone.postgres.id
  private_dns_zone_name = azurerm_private_dns_zone.postgres.name
  tags                  = var.tags

  # Flexible-server VNet integration is immutable at creation; the zone→VNet link
  # must exist before the server. delegated_subnet_id/private_dns_zone_id create
  # implicit deps on the subnet + zone, but the LINK is a separate resource, so
  # its ordering needs an explicit depends_on.
  depends_on = [azurerm_private_dns_zone_virtual_network_link.postgres]
}

module "object_store" {
  source = "../modules/object-store"

  name                = var.storage_account_name
  resource_group_name = azurerm_resource_group.hub.name
  location            = azurerm_resource_group.hub.location
  container_name      = var.blob_container_name
  versioning_enabled  = var.blob_versioning_enabled
  lifecycle_days      = var.blob_lifecycle_days
  tags                = var.tags
}

module "secrets" {
  source = "../modules/secrets"

  name                = var.key_vault_name
  resource_group_name = azurerm_resource_group.hub.name
  location            = azurerm_resource_group.hub.location
  tenant_id           = data.azurerm_client_config.current.tenant_id
  # No out-of-band secret slots — every hub secret is now generated in-TF and
  # written into the vault directly by the root (see below).
  names               = []
  reader_principal_id = azurerm_user_assigned_identity.hub.principal_id
  officer_object_id   = data.azurerm_client_config.current.object_id
  tags                = var.tags
}

# ---------------------------------------------------------------------------
# Generated secrets (born in-TF, correct encoding per slot, written into the KV)
# ---------------------------------------------------------------------------

# Host-token pepper (SPRAWL_HUB_SECRET_URL keeper). gocloud localsecrets uses
# base64.URLEncoding, so the key MUST be URL-SAFE base64 — a '+' or '/' would
# decode to the WRONG 32 bytes (a '/' truncates the URL host) and minted host
# tokens would not verify. random_bytes.base64 is STANDARD base64, so translate
# the alphabet (+/ => -_) here.
resource "random_bytes" "pepper" {
  length = 32
}

# Cookie-signing key (SPRAWL_HUB_COOKIE_KEY). OPPOSITE alphabet: login.go uses
# base64.StdEncoding, so this MUST stay STANDARD base64 (random_bytes.base64 is
# already standard) and decode to >=32 bytes. A URL-safe value would silently
# disable browser login.
resource "random_bytes" "cookie_key" {
  length = 32
}

# Browser login token (SPRAWL_HUB_LOGIN_TOKEN). Unlike the pepper/cookie keys
# this is an OPAQUE shared string compared verbatim by hubd (any non-empty value
# enables browser login), so its encoding is unconstrained — no base64-alphabet
# foot-gun. .hex is chosen so an operator can copy it out of Key Vault and paste
# it into `sprawl enter` / the browser without shell- or URL-quoting hazards.
resource "random_bytes" "login_token" {
  length = 32
}

resource "azurerm_key_vault_secret" "dsn" {
  name         = "hub-dsn"
  value        = module.database.conn_ref
  key_vault_id = module.secrets.store_id
  tags         = var.tags
  depends_on   = [module.secrets]
}

resource "azurerm_key_vault_secret" "secret_url" {
  name         = "hub-secret-url"
  value        = "base64key://${replace(replace(random_bytes.pepper.base64, "+", "-"), "/", "_")}"
  key_vault_id = module.secrets.store_id
  tags         = var.tags
  depends_on   = [module.secrets]
}

resource "azurerm_key_vault_secret" "cookie_key" {
  name         = "hub-cookie-key"
  value        = random_bytes.cookie_key.base64
  key_vault_id = module.secrets.store_id
  tags         = var.tags
  depends_on   = [module.secrets]
}

resource "azurerm_key_vault_secret" "login_token" {
  name         = "hub-login-token"
  value        = random_bytes.login_token.hex
  key_vault_id = module.secrets.store_id
  tags         = var.tags
  depends_on   = [module.secrets]
}

# ---------------------------------------------------------------------------
# Container host — wires env + KV-backed secret env from the module outputs
# ---------------------------------------------------------------------------

locals {
  # Non-secret env. SPRAWL_HUB_URL is added only when set (avoids a self-cycle
  # on the app's own FQDN; set out-of-band once a custom domain is known).
  hub_env = merge(
    {
      SPRAWL_HUB_LOG_FORMAT     = "json"
      SPRAWL_HUB_BLOB_URL       = "azblob://${module.object_store.bucket_ref}"
      AZURE_STORAGE_ACCOUNT     = module.object_store.account_name
      AZURE_CLIENT_ID           = azurerm_user_assigned_identity.hub.client_id
      SPRAWL_HUB_DEBUG_ENDPOINT = var.debug_endpoint ? "1" : "0"
    },
    var.hub_url == "" ? {} : { SPRAWL_HUB_URL = var.hub_url },
  )

  # KV-backed secret env: env var name => secret block name.
  hub_secret_env = {
    SPRAWL_HUB_DSN         = "hub-dsn"
    SPRAWL_HUB_SECRET_URL  = "hub-secret-url"
    SPRAWL_HUB_COOKIE_KEY  = "hub-cookie-key"
    SPRAWL_HUB_LOGIN_TOKEN = "hub-login-token"
  }

  # Secret blocks: name => KV versionless secret id (resolved via the UAMI).
  hub_secrets = [
    { name = "hub-dsn", key_vault_secret_id = azurerm_key_vault_secret.dsn.versionless_id },
    { name = "hub-secret-url", key_vault_secret_id = azurerm_key_vault_secret.secret_url.versionless_id },
    { name = "hub-cookie-key", key_vault_secret_id = azurerm_key_vault_secret.cookie_key.versionless_id },
    { name = "hub-login-token", key_vault_secret_id = azurerm_key_vault_secret.login_token.versionless_id },
  ]
}

module "container_host" {
  source = "../modules/container-host"

  name                         = var.container_app_name
  resource_group_name          = azurerm_resource_group.hub.name
  container_app_environment_id = azurerm_container_app_environment.hub.id
  image                        = var.container_image
  target_port                  = 8080
  identity_id                  = azurerm_user_assigned_identity.hub.id
  registry_server              = azurerm_container_registry.hub.login_server
  min_replicas                 = var.min_replicas
  max_replicas                 = var.max_replicas
  env                          = local.hub_env
  secret_env                   = local.hub_secret_env
  secrets                      = local.hub_secrets
  tags                         = var.tags

  # Pull role must exist before the app provisions its first revision.
  depends_on = [azurerm_role_assignment.acr_pull]
}
