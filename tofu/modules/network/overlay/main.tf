# Network Module - Overlay Network
#
# Creates a private mesh network using Headscale (self-hosted Tailscale control plane).
# All nodes join this network for secure internal communication.

terraform {
  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.0"
    }
  }
}

variable "name" {
  description = "Name prefix for resources"
  type        = string
}

variable "base_domain" {
  description = "Base domain for Headscale (e.g., hs.example.com)"
  type        = string
}

variable "server_url" {
  description = "Public URL for Headscale server"
  type        = string
}

variable "ip_prefixes" {
  description = "IP prefixes for the tailnet"
  type        = list(string)
  default     = ["100.64.0.0/10", "fd7a:115c:a1e0::/48"]
}

variable "magic_dns_domains" {
  description = "DNS search domains for MagicDNS"
  type        = list(string)
  default     = ["ts.net"]
}

variable "data_path" {
  description = "Path to store Headscale data"
  type        = string
  default     = "/var/lib/headscale"
}

variable "labels" {
  description = "Labels to attach to resources"
  type        = map(string)
  default     = {}
}

# Headscale configuration file
resource "local_file" "headscale_config" {
  filename = "${var.data_path}/config.yaml"
  content  = yamlencode({
    server_url                      = var.server_url
    listen_addr                     = ":8080"
    metrics_listen_addr             = ":9090"
    grpc_listen_addr                = ":50443"
    grpc_allow_insecure             = false
    
    private_key_path                = "/var/lib/headscale/private.key"
    noise = {
      private_key_path              = "/var/lib/headscale/noise_private.key"
    }
    
    prefixes = {
      v4                            = var.ip_prefixes[0]
      v6                            = var.ip_prefixes[1]
      allocation                    = "sequential"
    }
    
    derp = {
      server = {
        enabled                     = false
      }
      urls                          = ["https://controlplane.tailscale.com/derpmap/default"]
      auto_update_enabled           = true
      update_frequency              = "24h"
    }
    
    disable_check_updates           = true
    ephemeral_node_inactivity_timeout = "30m"
    
    database = {
      type                          = "sqlite"
      sqlite = {
        path                        = "/var/lib/headscale/db.sqlite"
      }
    }
    
    dns = {
      magic_dns                     = true
      base_domain                   = var.magic_dns_domains[0]
      nameservers = {
        global                      = ["1.1.1.1", "8.8.8.8"]
      }
    }
    
    log = {
      format                        = "json"
      level                         = "info"
    }
    
    policy = {
      mode                          = "file"
      path                          = ""
    }
  })
}

# Docker network for Headscale
resource "docker_network" "overlay" {
  name   = "${var.name}-overlay"
  driver = "bridge"

  labels {
    label = "techstack.io/stack"
    value = var.name
  }

  labels {
    label = "techstack.io/component"
    value = "overlay-network"
  }
}

# Headscale container
resource "docker_container" "headscale" {
  name  = "${var.name}-headscale"
  image = "headscale/headscale:latest"

  command = ["serve"]

  networks_advanced {
    name = docker_network.overlay.name
  }

  ports {
    internal = 8080
    external = 8080
    protocol = "tcp"
  }

  ports {
    internal = 9090
    external = 9090
    protocol = "tcp"
  }

  volumes {
    host_path      = var.data_path
    container_path = "/var/lib/headscale"
  }

  volumes {
    host_path      = local_file.headscale_config.filename
    container_path = "/etc/headscale/config.yaml"
    read_only      = true
  }

  restart = "unless-stopped"

  labels {
    label = "techstack.io/stack"
    value = var.name
  }

  labels {
    label = "techstack.io/component"
    value = "headscale"
  }

  depends_on = [local_file.headscale_config]
}

# Outputs
output "network_id" {
  description = "Docker network ID"
  value       = docker_network.overlay.id
}

output "network_name" {
  description = "Docker network name"
  value       = docker_network.overlay.name
}

output "headscale_container_id" {
  description = "Headscale container ID"
  value       = docker_container.headscale.id
}

output "headscale_url" {
  description = "Headscale server URL"
  value       = var.server_url
}
