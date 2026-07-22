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

  # Azure auto-assigns an availability zone at creation (observed: zone "1").
  # Our config intentionally pins no zone, so a re-plan shows zone "1" -> null
  # and would fight Azure's assignment on every apply. Ignore the attribute so
  # we accept whatever zone Azure placed the server in.
  lifecycle {
    ignore_changes = [zone]
  }
}

resource "azurerm_postgresql_flexible_server_database" "this" {
  name      = var.database_name
  server_id = azurerm_postgresql_flexible_server.this.id
  collation = "en_US.utf8"
  charset   = "UTF8"
}

# The server's <name>.postgres.database.azure.com FQDN is only a PUBLIC CNAME
# pointing at a server-managed HASH-named A record in the private zone. The ACA
# subnet can't chase that public CNAME, so the DB is unreachable. Discover the
# hash A record dynamically (no IP/hash literal committed) and alias a stable
# module-owned name (<server>.<zone>) to it so the DSN host resolves in-VNet.
data "azapi_resource_list" "private_zone_a_records" {
  type                   = "Microsoft.Network/privateDnsZones/A@2024-06-01"
  parent_id              = var.private_dns_zone_id
  response_export_values = { names = "value[].name" }
  depends_on             = [azurerm_postgresql_flexible_server.this]
}

resource "azurerm_private_dns_cname_record" "server_alias" {
  name                = azurerm_postgresql_flexible_server.this.name
  zone_name           = var.private_dns_zone_name
  resource_group_name = var.resource_group_name
  ttl                 = 30
  record              = "${data.azapi_resource_list.private_zone_a_records.output.names[0]}.${var.private_dns_zone_name}"
  tags                = var.tags

  lifecycle {
    precondition {
      condition     = length(data.azapi_resource_list.private_zone_a_records.output.names) == 1
      error_message = "Expected exactly one server-managed A record in the dedicated private DNS zone."
    }
  }
}
