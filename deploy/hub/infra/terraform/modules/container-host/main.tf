# The hub Container App. External HTTP/2 ingress is load-bearing: Connect
# server-streaming needs end-to-end HTTP/2, and Envoy speaks cleartext HTTP/2 to
# the container (allow_insecure_connections). Single revision, single replica for
# v1 (the persistent connection + in-memory lease/fan-out state make horizontal
# scaling a non-goal — docs 06 §5.1). The image is pulled via the passed-in
# user-assigned identity (AcrPull granted by the root BEFORE this app).
resource "azurerm_container_app" "this" {
  name                         = var.name
  resource_group_name          = var.resource_group_name
  container_app_environment_id = var.container_app_environment_id
  revision_mode                = var.revision_mode
  tags                         = var.tags

  identity {
    type         = "UserAssigned"
    identity_ids = [var.identity_id]
  }

  registry {
    server   = var.registry_server
    identity = var.identity_id
  }

  # Key Vault-backed secrets, resolved by the app's managed identity at runtime.
  dynamic "secret" {
    for_each = { for s in var.secrets : s.name => s }
    content {
      name                = secret.value.name
      key_vault_secret_id = secret.value.key_vault_secret_id
      identity            = var.identity_id
    }
  }

  template {
    min_replicas = var.min_replicas
    max_replicas = var.max_replicas

    container {
      name   = "hubd"
      image  = var.image
      cpu    = var.cpu
      memory = var.memory

      # Plain (non-secret) env vars.
      dynamic "env" {
        for_each = var.env
        content {
          name  = env.key
          value = env.value
        }
      }

      # Secret-backed env vars — value comes from a named secret block above.
      dynamic "env" {
        for_each = var.secret_env
        content {
          name        = env.key
          secret_name = env.value
        }
      }

      # Liveness = "is the process wedged?" — process-health only, no external
      # deps (a dep-checking liveness causes restart storms on a DB blip).
      liveness_probe {
        transport = "HTTP"
        port      = var.target_port
        path      = var.liveness_path
      }

      # Readiness = "should traffic route here now?" — gated on deps (DB, secrets,
      # migrations, blob). Not-ready pulls the revision out of rotation without a
      # restart, and gates rollout on revision swap (docs 08 §4).
      readiness_probe {
        transport = "HTTP"
        port      = var.target_port
        path      = var.readiness_path
      }
    }
  }

  ingress {
    external_enabled           = true
    target_port                = var.target_port
    transport                  = "http2"
    allow_insecure_connections = true

    traffic_weight {
      latest_revision = true
      percentage      = 100
    }
  }
}
