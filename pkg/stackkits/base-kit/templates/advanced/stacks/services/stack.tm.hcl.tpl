stack {
  name        = "services"
  description = "Core services: Traefik, Monitoring"
  id          = "{{.StackID}}-services"

  after = ["network"]
}

generate_hcl "_terramate_generated_main.tf" {
  content {
    # Provider configuration
    terraform {
      required_providers {
        local = {
          source  = "hashicorp/local"
          version = "~> 2.4"
        }
        docker = {
          source  = "kreuzwerker/docker"
          version = "~> 3.0"
        }
      }
    }

    # Service configuration from globals
    locals {
      services       = global.services
      network_config = global.network
      common_labels  = global.common_labels
    }

    # Core services configuration
    resource "local_file" "services_config" {
      filename = "${path.module}/services-config.json"
      content = jsonencode({
        services = local.services
        network  = local.network_config
        labels   = local.common_labels
      })
    }

    # Dynamic service resources
    dynamic "resource" {
      for_each = local.services
      content {
        local_file = {
          (resource.key) = {
            filename = "${path.module}/service-${resource.key}.json"
            content = jsonencode({
              name   = resource.key
              type   = resource.value.type
              node   = resource.value.node
              port   = resource.value.port
              image  = resource.value.image
              labels = local.common_labels
            })
          }
        }
      }
    }
  }
}

generate_hcl "_terramate_generated_outputs.tf" {
  content {
    output "services_deployed" {
      description = "List of deployed services"
      value       = keys(local.services)
    }

    output "services_count" {
      description = "Number of services deployed"
      value       = length(local.services)
    }
  }
}
