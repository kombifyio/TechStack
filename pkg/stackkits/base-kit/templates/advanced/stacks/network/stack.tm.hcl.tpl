stack {
  name        = "network"
  description = "Network infrastructure: VPN, DNS, Firewall"
  id          = "{{.StackID}}-network"

  # No dependencies - runs first
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
      }
    }

    # Network configuration from globals
    locals {
      network_config = global.network
      nodes          = global.nodes
    }

    # VPN configuration resource
    resource "local_file" "network_config" {
      filename = "${path.module}/network-config.json"
      content = jsonencode({
        vpn_enabled = local.network_config.vpn_enabled
        subnet      = local.network_config.subnet
        domain      = local.network_config.domain
        nodes       = local.nodes
      })
    }
  }
}

generate_hcl "_terramate_generated_outputs.tf" {
  content {
    output "network_configured" {
      description = "Whether network was configured"
      value       = true
    }

    output "vpn_enabled" {
      description = "Whether VPN is enabled"
      value       = local.network_config.vpn_enabled
    }

    output "network_domain" {
      description = "The configured network domain"
      value       = local.network_config.domain
    }

    output "network_subnet" {
      description = "The configured network subnet"
      value       = local.network_config.subnet
    }
  }
}
