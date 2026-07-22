# Admin password generated in-TF so no plaintext credential is ever committed or
# supplied as an operator tfvar. special=false keeps the value URL-safe for the
# DSN. (The value is recorded in encrypted remote state — docs 06 §4 caveat.)
# special=false keeps the value URL-safe for the DSN. min_* pins guarantee the
# generated password satisfies Azure Postgres' complexity rule (>=3 of upper /
# lower / digit / special) deterministically, so an apply can't burn on a rare
# all-one-class draw.
resource "random_password" "admin" {
  length      = 32
  special     = false
  min_upper   = 1
  min_lower   = 1
  min_numeric = 1
}

# Smallest burstable managed Postgres. Backups on (parameterized window),
# storage auto-grow on. FULLY PRIVATE: no public network access — the server is
# VNet-injected into a delegated subnet and reached only over the VNet, with its
# private FQDN resolved via the linked private DNS zone (docs 06 §5.2).
resource "azurerm_postgresql_flexible_server" "this" {
  name                          = var.name
  resource_group_name           = var.resource_group_name
  location                      = var.location
  version                       = var.engine_version
  administrator_login           = var.administrator_login
  administrator_password        = random_password.admin.result
  sku_name                      = var.size
  storage_mb                    = var.storage_mb
  auto_grow_enabled             = true
  backup_retention_days         = var.retention_days
  public_network_access_enabled = false
  delegated_subnet_id           = var.delegated_subnet_id
  private_dns_zone_id           = var.private_dns_zone_id
  tags                          = var.tags
}

resource "azurerm_postgresql_flexible_server_database" "this" {
  name      = var.database_name
  server_id = azurerm_postgresql_flexible_server.this.id
  collation = "en_US.utf8"
  charset   = "UTF8"
}
