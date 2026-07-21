# App-facing outputs — the stable contract a future aws/ root mirrors. No secret
# VALUES are output; only references. The DSN lives only in Key Vault (written
# by main.tf), never surfaced here.

output "hub_url" {
  description = "Public HTTPS URL of the deployed hub (use as sprawl enter --hub-url)."
  value       = module.container_host.url
}

output "hub_fqdn" {
  description = "Raw ingress FQDN of the hub."
  value       = module.container_host.fqdn
}

output "db_host" {
  description = "Postgres server FQDN (the DSN itself is stored only in Key Vault)."
  value       = module.database.host
}

output "bucket_ref" {
  description = "Blob container (logical bucket) reference."
  value       = module.object_store.bucket_ref
}

output "storage_account_name" {
  description = "Storage account name (AZURE_STORAGE_ACCOUNT for the app's managed-identity blob access)."
  value       = module.object_store.account_name
}

output "secret_store_ref" {
  description = "Key Vault URI backing the hub's secrets."
  value       = module.secrets.store_ref
}

output "secret_refs" {
  description = "Out-of-band Key Vault secret references (name => versionless id)."
  value       = module.secrets.secret_refs
}

output "acr_login_server" {
  description = "ACR login server for `az acr build` / the image reference."
  value       = azurerm_container_registry.hub.login_server
}

output "uami_id" {
  description = "Resource ID of the user-assigned managed identity the app runs as."
  value       = azurerm_user_assigned_identity.hub.id
}
