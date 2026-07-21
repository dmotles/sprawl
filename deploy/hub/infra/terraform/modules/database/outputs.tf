# STABLE OUTPUT-NAME CONTRACT (mirrored by a future aws/ database):
#   conn_ref, host

output "conn_ref" {
  description = "Full Postgres DSN (sslmode=require). Sensitive: the composing root writes this into the secrets store; it is never committed and never logged."
  value       = "postgres://${var.administrator_login}:${random_password.admin.result}@${azurerm_postgresql_flexible_server.this.fqdn}:5432/${azurerm_postgresql_flexible_server_database.this.name}?sslmode=require"
  sensitive   = true
}

output "host" {
  description = "Server FQDN."
  value       = azurerm_postgresql_flexible_server.this.fqdn
}
