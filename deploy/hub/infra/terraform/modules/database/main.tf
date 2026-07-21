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
# storage auto-grow on, public access parameterized (docs 06 §5.2).
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
  public_network_access_enabled = var.public_network_access_enabled
  tags                          = var.tags
}

resource "azurerm_postgresql_flexible_server_database" "this" {
  name      = var.database_name
  server_id = azurerm_postgresql_flexible_server.this.id
  collation = "en_US.utf8"
  charset   = "UTF8"
}

# When public access is on, allow other Azure services (the Container App) to
# reach the server. 0.0.0.0-0.0.0.0 is Azure's "allow internal Azure traffic"
# sentinel, NOT the public internet. Skipped entirely when public access is off.
resource "azurerm_postgresql_flexible_server_firewall_rule" "azure_services" {
  count            = var.public_network_access_enabled ? 1 : 0
  name             = "AllowAzureServices"
  server_id        = azurerm_postgresql_flexible_server.this.id
  start_ip_address = "0.0.0.0"
  end_ip_address   = "0.0.0.0"
}
