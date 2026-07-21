# STABLE OUTPUT-NAME CONTRACT (mirrored by a future aws/ secrets):
#   store_ref, secret_refs

output "store_ref" {
  description = "Key Vault URI (the secrets store reference)."
  value       = azurerm_key_vault.this.vault_uri
}

output "store_id" {
  description = "Key Vault resource ID (used by the root to write generated-value secrets into this vault)."
  value       = azurerm_key_vault.this.id
}

output "secret_refs" {
  description = "Map of out-of-band secret name => versionless Key Vault secret id, for wiring into the container app's secret blocks."
  value       = { for name, s in azurerm_key_vault_secret.slot : name => s.versionless_id }
}
