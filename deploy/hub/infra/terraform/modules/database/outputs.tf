# STABLE OUTPUT-NAME CONTRACT (mirrored by a future aws/ database):
#   conn_ref, host

# The DSN host is the stable module-owned alias (<server>.<zone>) resolvable
# in-VNet via the CNAME in main.tf — NOT the server's public .fqdn CNAME, which
# the ACA subnet cannot chase into the private zone.
locals {
  private_host = "${azurerm_postgresql_flexible_server.this.name}.${var.private_dns_zone_name}"
}

output "conn_ref" {
  description = "Full Postgres DSN (sslmode=require). Sensitive: the composing root writes this into the secrets store; it is never committed and never logged."
  value       = "postgres://${var.administrator_login}:${random_password.admin.result}@${local.private_host}:5432/${azurerm_postgresql_flexible_server_database.this.name}?sslmode=require"
  sensitive   = true
}

output "host" {
  description = "Resolvable private DSN host (<server>.<zone>) aliased to the server's private A record."
  value       = local.private_host
}
