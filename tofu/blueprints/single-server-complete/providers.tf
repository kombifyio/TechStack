# kombifyTechstack Single Server Complete Blueprint - Providers & Locals
#
# This file defines provider configurations and local values.

terraform {
  required_version = ">= 1.6"

  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.45"
    }
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
    time = {
      source  = "hashicorp/time"
      version = "~> 0.10"
    }
  }
}

# ===========================================================================
# Locals
# ===========================================================================

locals {
  hostname      = "${var.stack_name}-server"
  headscale_url = var.headscale_domain != "" ? "https://${var.headscale_domain}" : "http://${local.hostname}:8080"

  common_labels = merge(var.labels, {
    "techstack.io/stack"     = var.stack_name
    "techstack.io/managed"   = "true"
    "techstack.io/blueprint" = "single-server-complete"
  })
}

# ===========================================================================
# Provider Configuration
# ===========================================================================

provider "hcloud" {
  token = var.hcloud_token
}

provider "docker" {
  host = "ssh://${var.admin_username}@${hcloud_server.main.ipv4_address}"

  ssh_opts = ["-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"]
}
