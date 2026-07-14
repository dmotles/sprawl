terraform {
  # Partial backend config. Real values are injected at init time from the
  # gitignored backend-config.hcl:
  #   terraform init -backend-config=../backend-config.hcl
  # The azurerm backend is built into Terraform core (no provider plugin needed),
  # so state lives remotely in the bootstrap container — never on local disk.
  backend "azurerm" {}
}
