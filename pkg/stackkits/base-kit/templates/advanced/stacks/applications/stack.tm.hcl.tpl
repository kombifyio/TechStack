stack {
  name        = "applications"
  description = "User applications"
  id          = "{{.StackID}}-applications"

  after = ["services"]
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

    # Application configuration from globals
    locals {
      applications   = try(global.applications, {})
      services       = global.services
      network_config = global.network
      common_labels  = global.common_labels
    }

    # Applications configuration
    resource "local_file" "applications_config" {
      filename = "${path.module}/applications-config.json"
      content = jsonencode({
        applications = local.applications
        depends_on   = keys(local.services)
        network      = local.network_config
        labels       = local.common_labels
      })
    }
  }
}

generate_hcl "_terramate_generated_outputs.tf" {
  content {
    output "applications_deployed" {
      description = "List of deployed applications"
      value       = keys(local.applications)
    }

    output "applications_count" {
      description = "Number of applications deployed"
      value       = length(local.applications)
    }

    output "stack_complete" {
      description = "Whether the full stack deployment is complete"
      value       = true
    }
  }
}
