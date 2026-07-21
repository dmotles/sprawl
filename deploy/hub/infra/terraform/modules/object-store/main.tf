# Private, AAD-only storage account. Shared access keys are disabled (corporate
# policy: no account keys); the account is reached exclusively via AAD/managed
# identity. Versioning is on by default for snapshot integrity.
resource "azurerm_storage_account" "this" {
  name                            = var.name
  resource_group_name             = var.resource_group_name
  location                        = var.location
  account_tier                    = "Standard"
  account_replication_type        = "LRS"
  min_tls_version                 = "TLS1_2"
  shared_access_key_enabled       = false
  allow_nested_items_to_be_public = false
  tags                            = var.tags

  blob_properties {
    versioning_enabled = var.versioning_enabled
  }
}

# The logical bucket. private = no anonymous read.
resource "azurerm_storage_container" "this" {
  name                  = var.container_name
  storage_account_id    = azurerm_storage_account.this.id
  container_access_type = "private"
}

# Lifecycle GC backstop — coarse infra floor beneath the app's snapshot
# compaction (docs 06 §5.3). Created only when lifecycle_days > 0.
resource "azurerm_storage_management_policy" "this" {
  count              = var.lifecycle_days > 0 ? 1 : 0
  storage_account_id = azurerm_storage_account.this.id

  rule {
    name    = "expire-old-blobs"
    enabled = true

    filters {
      blob_types = ["blockBlob"]
    }

    actions {
      base_blob {
        delete_after_days_since_modification_greater_than = var.lifecycle_days
      }
    }
  }
}
