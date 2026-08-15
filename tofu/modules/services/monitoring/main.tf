# Services Module - Monitoring (Uptime Kuma)
#
# Deploys Uptime Kuma for basic uptime monitoring with 
# a beautiful status page and alerting.

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
  description = "Path to store monitoring data"
  type        = string
  default     = "/data/uptime-kuma"
}

variable "http_port" {
  description = "HTTP port for Uptime Kuma UI"
  type        = number
  default     = 3001
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

# Uptime Kuma container
resource "docker_container" "uptime_kuma" {
  name  = "${var.name}-uptime-kuma"
  image = "louislam/uptime-kuma:latest"

  networks_advanced {
    name = var.network_name
  }

  ports {
    internal = 3001
    external = var.http_port
    protocol = "tcp"
  }

  volumes {
    host_path      = var.data_path
    container_path = "/app/data"
  }

  restart = "unless-stopped"

  labels {
    label = "techstack.io/stack"
    value = var.name
  }

  labels {
    label = "techstack.io/component"
    value = "uptime-kuma"
  }

  labels {
    label = "techstack.io/service-type"
    value = "monitoring"
  }
}

# Outputs
output "container_id" {
  description = "Uptime Kuma container ID"
  value       = docker_container.uptime_kuma.id
}

output "url" {
  description = "Uptime Kuma UI URL"
  value       = var.access_host != "" ? "http://${var.access_host}:${var.http_port}" : ""
}
