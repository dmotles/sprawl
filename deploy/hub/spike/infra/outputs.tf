output "container_app_fqdn" {
  description = "Public FQDN of the spike server (the client's -addr, as https://<fqdn>)."
  value       = azurerm_container_app.spike.ingress[0].fqdn
}

output "acr_login_server" {
  description = "ACR login server for `az acr build` / the container image reference."
  value       = azurerm_container_registry.spike.login_server
}
