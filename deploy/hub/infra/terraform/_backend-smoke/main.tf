# Throwaway root (QUM-870) that proves `terraform init` binds the remote
# azurerm backend and produces NO local state. It declares no resources and no
# provider — just a static output — so `init` only exercises the backend.
#
# Validate (post-bootstrap, requires az auth):
#   terraform init -backend-config=../backend-config.hcl
#   test ! -e terraform.tfstate   # state is remote-only
#
# Offline validate (no cloud):
#   terraform init -backend=false && terraform validate

output "backend_smoke" {
  value = "remote azurerm backend bound; no local state produced"
}
