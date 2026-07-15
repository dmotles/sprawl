terraform {
  # Partial azurerm backend — real values injected at init from the gitignored
  # backend-config.hcl (see backend-config.hcl.example; key = "spike.tfstate").
  # Reuses the QUM-870 remote state backend (AAD-only, use_azuread_auth). State
  # is remote-only; no local *.tfstate is ever produced.
  backend "azurerm" {}
}
