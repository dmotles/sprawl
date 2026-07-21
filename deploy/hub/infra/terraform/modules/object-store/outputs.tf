# STABLE OUTPUT-NAME CONTRACT (mirrored by a future aws/ object-store):
#   bucket_ref

output "bucket_ref" {
  description = "Logical bucket reference (the blob container name). Paired with account_name to form a gocloud.dev azblob:// URL."
  value       = azurerm_storage_container.this.name
}

output "account_name" {
  description = "Storage account name. The app sets AZURE_STORAGE_ACCOUNT to this so gocloud.dev/blob authenticates via managed identity."
  value       = azurerm_storage_account.this.name
}

output "blob_endpoint" {
  description = "Primary blob service endpoint."
  value       = azurerm_storage_account.this.primary_blob_endpoint
}

output "account_id" {
  description = "Storage account resource ID (used by the root to scope the app's Storage Blob Data Contributor grant)."
  value       = azurerm_storage_account.this.id
}
