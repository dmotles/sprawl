# RBAC-authorized Key Vault (no legacy access policies). AAD end-to-end.
resource "azurerm_key_vault" "this" {
  name                       = var.name
  resource_group_name        = var.resource_group_name
  location                   = var.location
  tenant_id                  = var.tenant_id
  sku_name                   = "standard"
  rbac_authorization_enabled = true
  purge_protection_enabled   = true
  soft_delete_retention_days = 7
  tags                       = var.tags
}

# The app reads secrets at runtime via its managed identity.
resource "azurerm_role_assignment" "reader" {
  scope                = azurerm_key_vault.this.id
  role_definition_name = "Key Vault Secrets User"
  principal_id         = var.reader_principal_id
}

# The deployer writes secret values (TF-created generated secrets + out-of-band
# `az keyvault secret set`).
resource "azurerm_role_assignment" "officer" {
  scope                = azurerm_key_vault.this.id
  role_definition_name = "Key Vault Secrets Officer"
  principal_id         = var.officer_object_id
}

# EMPTY out-of-band secret slots. The value is a one-time placeholder; the real
# value is injected after apply and TF ignores drift on it. Depends on the
# officer grant so the create doesn't race RBAC propagation.
resource "azurerm_key_vault_secret" "slot" {
  for_each     = toset(var.names)
  name         = each.value
  value        = "PLACEHOLDER-set-out-of-band"
  key_vault_id = azurerm_key_vault.this.id
  tags         = var.tags

  depends_on = [azurerm_role_assignment.officer]

  lifecycle {
    ignore_changes = [value]
  }
}
