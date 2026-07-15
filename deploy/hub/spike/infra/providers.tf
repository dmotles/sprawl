# azurerm provider — subscription pinned EXPLICITLY (QUM-870 pattern). The value
# is never hardcoded (public repo); it comes from gitignored spike.tfvars /
# TF_VAR_subscription_id / ARM_SUBSCRIPTION_ID.
provider "azurerm" {
  features {}

  subscription_id = var.subscription_id
}
