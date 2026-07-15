# QUM-871 transport spike infrastructure — a DISPOSABLE Azure Container Apps
# deployment used only to test long-lived server-stream survival. Every resource
# lives in a dedicated net-new RG so teardown is a single clean destroy that
# never touches the QUM-870 state backend.

# Dedicated, disposable resource group (separate from the state-backend RG).
resource "azurerm_resource_group" "spike" {
  name     = var.resource_group_name
  location = var.location
  tags     = var.tags
}

# Log Analytics workspace — required backing for the Container Apps Environment
# and the destination for container stdout (the server's append/subscribe logs).
resource "azurerm_log_analytics_workspace" "spike" {
  name                = var.log_analytics_name
  resource_group_name = azurerm_resource_group.spike.name
  location            = azurerm_resource_group.spike.location
  sku                 = "PerGB2018"
  retention_in_days   = 30
  tags                = var.tags
}

# Container Apps Environment (managed Envoy ingress — the L7 whose ~240s idle
# timeout the spike is testing against).
resource "azurerm_container_app_environment" "spike" {
  name                       = var.environment_name
  resource_group_name        = azurerm_resource_group.spike.name
  location                   = azurerm_resource_group.spike.location
  log_analytics_workspace_id = azurerm_log_analytics_workspace.spike.id
  tags                       = var.tags
}

# Throwaway registry for the spike image. AAD-only (admin user disabled); the
# Container App pulls via a user-assigned identity + AcrPull below.
resource "azurerm_container_registry" "spike" {
  name                = var.acr_name
  resource_group_name = azurerm_resource_group.spike.name
  location            = azurerm_resource_group.spike.location
  sku                 = "Basic"
  admin_enabled       = false
  tags                = var.tags
}

# User-assigned identity used by the Container App to pull from the ACR. Using a
# UAMI (rather than the app's system-assigned identity) lets the AcrPull grant be
# created BEFORE the Container App, so the first-and-only apply can pull the real
# image without a chicken-and-egg ordering (system identity → AcrPull → app).
resource "azurerm_user_assigned_identity" "spike" {
  name                = "${var.container_app_name}-pull"
  resource_group_name = azurerm_resource_group.spike.name
  location            = azurerm_resource_group.spike.location
  tags                = var.tags
}

# Let the pull identity read from the throwaway ACR. Created before the Container
# App so the image pull succeeds on first provision.
resource "azurerm_role_assignment" "acr_pull" {
  scope                = azurerm_container_registry.spike.id
  role_definition_name = "AcrPull"
  principal_id         = azurerm_user_assigned_identity.spike.principal_id
}

# The spike server. External HTTP/2 ingress is load-bearing: Connect
# server-streaming needs end-to-end HTTP/2, and Envoy speaks cleartext HTTP/2 to
# the container (allow_insecure_connections). min_replicas = 1 prevents
# scale-to-zero from killing the idle stream mid-test. There is NO Terraform
# lever for the Envoy stream_idle_timeout — the heartbeat interval (<< 240s) is
# the mitigation under test.
resource "azurerm_container_app" "spike" {
  name                         = var.container_app_name
  resource_group_name          = azurerm_resource_group.spike.name
  container_app_environment_id = azurerm_container_app_environment.spike.id
  revision_mode                = "Single"
  tags                         = var.tags

  # Grant the pull role before the app provisions its revision.
  depends_on = [azurerm_role_assignment.acr_pull]

  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.spike.id]
  }

  registry {
    server   = azurerm_container_registry.spike.login_server
    identity = azurerm_user_assigned_identity.spike.id
  }

  template {
    min_replicas = 1
    max_replicas = 1

    container {
      name   = "server"
      image  = var.container_image
      cpu    = 0.25
      memory = "0.5Gi"

      env {
        name  = "HEARTBEAT_INTERVAL_SECONDS"
        value = tostring(var.heartbeat_interval_seconds)
      }
    }
  }

  ingress {
    external_enabled           = true
    target_port                = 8080
    transport                  = "http2"
    allow_insecure_connections = true

    traffic_weight {
      latest_revision = true
      percentage      = 100
    }
  }
}
