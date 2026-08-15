# Services Module - PaaS (Coolify)
#
# Deploys Coolify as the self-hosted PaaS platform for easy 
# application deployment with a visual UI.

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
  description = "Path to store Coolify data"
  type        = string
  default     = "/data/coolify"
}

variable "http_port" {
  description = "HTTP port for Coolify UI"
  type        = number
  default     = 8000
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

# Coolify uses Docker socket for container management
resource "docker_container" "coolify" {
  name  = "${var.name}-coolify"
  image = "ghcr.io/coollabsio/coolify:latest"

  networks_advanced {
    name = var.network_name
  }

  ports {
    internal = 8000
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
    host_path      = "${var.data_path}/db"
    container_path = "/data/coolify/db"
  }

  volumes {
    host_path      = "${var.data_path}/ssh"
    container_path = "/data/coolify/ssh"
  }

  volumes {
    host_path      = "${var.data_path}/source"
    container_path = "/data/coolify/source"
  }

  env = [
    "APP_ID=${var.name}",
    "APP_KEY=base64:${base64encode(random_password.app_key.result)}",
    "DB_PASSWORD=${random_password.db_password.result}",
  ]

  restart = "unless-stopped"

  labels {
    label = "techstack.io/stack"
    value = var.name
  }

  labels {
    label = "techstack.io/component"
    value = "coolify"
  }

  labels {
    label = "techstack.io/service-type"
    value = "paas"
  }
}

resource "random_password" "app_key" {
  length  = 32
  special = false
}

resource "random_password" "db_password" {
  length  = 24
  special = false
}

# Outputs
output "container_id" {
  description = "Coolify container ID"
  value       = docker_container.coolify.id
}

output "url" {
  description = "Coolify UI URL"
  value       = var.access_host != "" ? "http://${var.access_host}:${var.http_port}" : ""
}
