# Services Module - Dokploy
#
# Deploys Dokploy as a self-hosted PaaS platform for deploying applications
# with Git integration and Docker support.

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

variable "network_name" {
  description = "Docker network to attach to"
  type        = string
}

variable "data_path" {
  description = "Path to store Dokploy data"
  type        = string
  default     = "/data/dokploy"
}

variable "http_port" {
  description = "HTTP port for Dokploy UI"
  type        = number
  default     = 3000
}

variable "access_host" {
  description = "Host or DNS name used to construct browser-facing URLs (optional)"
  type        = string
  default     = ""
}

variable "labels" {
  description = "Labels to attach to resources"
  type        = map(string)
  default     = {}
}

# Dokploy container
resource "docker_container" "dokploy" {
  name  = "${var.name}-dokploy"
  image = "dokploy/dokploy:latest"

  networks_advanced {
    name = var.network_name
  }

  ports {
    internal = 3000
    external = var.http_port
    protocol = "tcp"
  }

  # Mount Docker socket for container management
  volumes {
    host_path      = "/var/run/docker.sock"
    container_path = "/var/run/docker.sock"
  }

  # Persistent data
  volumes {
    host_path      = var.data_path
    container_path = "/app/data"
  }

  env = [
    "NODE_ENV=production",
    "PORT=3000",
  ]

  restart = "unless-stopped"

  labels {
    label = "techstack.io/stack"
    value = var.name
  }

  labels {
    label = "techstack.io/component"
    value = "dokploy"
  }

  labels {
    label = "techstack.io/service-type"
    value = "paas"
  }

  dynamic "labels" {
    for_each = var.labels
    content {
      label = labels.key
      value = labels.value
    }
  }
}

# Outputs
output "container_id" {
  description = "Dokploy container ID"
  value       = docker_container.dokploy.id
}

output "url" {
  description = "Dokploy UI URL"
  value       = var.access_host != "" ? "http://${var.access_host}:${var.http_port}" : ""
}
