# Networking substrate for the fully-private Postgres Flexible Server (QUM-879).
#
# Root-level glue (not a modules/ capability): a VNet is a cross-cutting
# substrate shared by TWO capabilities — the ACA environment consumes the
# infrastructure subnet, and the database consumes the delegated subnet + private
# DNS zone. Like the RG, Log Analytics workspace, and UAMI that already live in
# the root, this is composition glue rather than a single-capability black box,
# and it is Azure-specific (a future aws/ root would implement VPC/security-group
# topology entirely differently). The modules/ seam is reserved for app-consumed,
# cross-cloud capability contracts (database, object-store, secrets,
# container-host) — networking is neither, so it stays here.

resource "azurerm_virtual_network" "hub" {
  name                = var.vnet_name
  resource_group_name = azurerm_resource_group.hub.name
  location            = azurerm_resource_group.hub.location
  address_space       = var.vnet_address_space
  tags                = var.tags
}

# ACA infrastructure subnet. The Container App Environment is CONSUMPTION-ONLY
# (no workload_profile block), so this subnet MUST be >= /23 and MUST NOT be
# delegated to any service (Azure rejects a delegated infra subnet for a
# consumption-only env). azurerm_subnet has no tags argument.
resource "azurerm_subnet" "aca_infra" {
  name                 = var.aca_infra_subnet_name
  resource_group_name  = azurerm_resource_group.hub.name
  virtual_network_name = azurerm_virtual_network.hub.name
  address_prefixes     = [var.aca_infra_subnet_prefix]
}

# Delegated subnet for the flexible server's private VNet injection. Delegation
# to Microsoft.DBforPostgreSQL/flexibleServers reserves the subnet exclusively
# for the DB server. Not shared with anything else.
resource "azurerm_subnet" "postgres" {
  name                 = var.postgres_subnet_name
  resource_group_name  = azurerm_resource_group.hub.name
  virtual_network_name = azurerm_virtual_network.hub.name
  address_prefixes     = [var.postgres_subnet_prefix]

  delegation {
    name = "fs"
    service_delegation {
      name    = "Microsoft.DBforPostgreSQL/flexibleServers"
      actions = ["Microsoft.Network/virtualNetworks/subnets/join/action"]
    }
  }
}

# Private DNS zone for the flexible server. The name MUST end in
# .postgres.database.azure.com; the leftmost label is parameterized.
resource "azurerm_private_dns_zone" "postgres" {
  name                = "${var.postgres_dns_zone_label}.private.postgres.database.azure.com"
  resource_group_name = azurerm_resource_group.hub.name
  tags                = var.tags
}

# Link the zone to the VNet so in-VNet clients resolve the server's private FQDN.
# The flexible server must not provision before this link exists — the module
# call in main.tf carries an explicit depends_on for that ordering.
resource "azurerm_private_dns_zone_virtual_network_link" "postgres" {
  name                  = "${var.postgres_dns_zone_label}-pdz-link"
  resource_group_name   = azurerm_resource_group.hub.name
  private_dns_zone_name = azurerm_private_dns_zone.postgres.name
  virtual_network_id    = azurerm_virtual_network.hub.id
  registration_enabled  = false
  tags                  = var.tags
}
