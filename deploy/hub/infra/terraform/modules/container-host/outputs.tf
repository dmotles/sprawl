# STABLE OUTPUT-NAME CONTRACT (mirrored by a future aws/ container-host):
#   url, identity_id

output "url" {
  description = "Public HTTPS URL of the hub (the client's --hub-url)."
  value       = "https://${azurerm_container_app.this.ingress[0].fqdn}"
}

output "fqdn" {
  description = "Raw ingress FQDN (without scheme)."
  value       = azurerm_container_app.this.ingress[0].fqdn
}

output "identity_id" {
  description = "Resource ID of the managed identity the app runs as (echoed from input for a uniform cross-cloud contract)."
  value       = var.identity_id
}
