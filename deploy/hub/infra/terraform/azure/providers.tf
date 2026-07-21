# azurerm provider — subscription pinned EXPLICITLY (QUM-870 pattern). The value
# is NEVER hardcoded (public repo); it comes from gitignored terraform.tfvars /
# TF_VAR_subscription_id / ARM_SUBSCRIPTION_ID. If omitted, azurerm falls back to
# the Azure CLI's active subscription — the wrong-subscription failure mode.
#
# storage_use_azuread = true keeps blob data-plane operations (container create,
# object access) on AAD — no account keys anywhere (corporate policy).
provider "azurerm" {
  features {}

  subscription_id     = var.subscription_id
  storage_use_azuread = true
}
