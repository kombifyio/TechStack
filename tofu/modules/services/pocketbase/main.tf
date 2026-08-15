# Services Module - Pocketbase
#
# Deploys Pocketbase as a lightweight backend with real-time DB,
# authentication, and file storage.

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
  description = "Path to store Pocketbase data"
  type        = string
  default     = "/data/pocketbase"
}

variable "http_port" {
  description = "HTTP port for Pocketbase API"
  type        = number
  default     = 8090
}

variable "access_host" {
  description = "Host or DNS name used to construct browser-facing URLs (optional)"
  type        = string
  default     = ""
}

variable "admin_email" {
  description = "Admin email for initial setup"
  type        = string
  default     = ""
}

variable "admin_password" {
  description = "Admin password for initial setup"
  type        = string
  default     = ""
  sensitive   = true
}

variable "labels" {
  description = "Labels to attach to resources"
  type        = map(string)
  default     = {}
}

# Pocketbase container
resource "docker_container" "pocketbase" {
  name  = "${var.name}-pocketbase"
  image = "ghcr.io/muchobien/pocketbase:latest"

  networks_advanced {
    name = var.network_name
  }

  ports {
    internal = 8090
    external = var.http_port
    protocol = "tcp"
  }

  # Persistent data directory
  volumes {
    host_path      = var.data_path
    container_path = "/pb_data"
  }

  # Command to run Pocketbase
  command = ["serve", "--http=:8090"]

  restart = "unless-stopped"

  labels {
    label = "techstack.io/stack"
    value = var.name
  }

  labels {
    label = "techstack.io/component"
    value = "pocketbase"
  }

  labels {
    label = "techstack.io/service-type"
    value = "backend"
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
  description = "Pocketbase container ID"
  value       = docker_container.pocketbase.id
}

output "url" {
  description = "Pocketbase API URL"
  value       = var.access_host != "" ? "http://${var.access_host}:${var.http_port}" : ""
}

output "admin_ui_url" {
  description = "Pocketbase Admin UI URL"
  value       = var.access_host != "" ? "http://${var.access_host}:${var.http_port}/_/" : ""
}
