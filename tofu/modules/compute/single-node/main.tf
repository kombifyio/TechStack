# Compute Module - Single Node
#
# Provisions a single server that runs all stack components.
# This is the simplest deployment pattern, ideal for home labs
# or development environments.

terraform {
  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.45"
    }
  }
}

variable "name" {
  description = "Name prefix for resources"
  type        = string
}

variable "server_type" {
  description = "Hetzner server type (e.g., cx21, cpx21)"
  type        = string
  default     = "cx21"
}

variable "location" {
  description = "Datacenter location"
  type        = string
  default     = "nbg1"
}

variable "image" {
  description = "OS image to use"
  type        = string
  default     = "ubuntu-24.04"
}

variable "ssh_keys" {
  description = "SSH key IDs to add to the server"
  type        = list(string)
  default     = []
}

variable "labels" {
  description = "Labels to attach to resources"
  type        = map(string)
  default     = {}
}

variable "user_data" {
  description = "Cloud-init user data script"
  type        = string
  default     = ""
}

# Primary server
resource "hcloud_server" "node" {
  name        = "${var.name}-node"
  server_type = var.server_type
  location    = var.location
  image       = var.image
  ssh_keys    = var.ssh_keys
  user_data   = var.user_data

  labels = merge(var.labels, {
    "techstack.io/role"   = "single-node"
    "techstack.io/stack"  = var.name
  })

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }

  lifecycle {
    ignore_changes = [
      user_data,
    ]
  }
}

# Firewall for the node
resource "hcloud_firewall" "node" {
  name = "${var.name}-fw"

  labels = merge(var.labels, {
    "techstack.io/stack" = var.name
  })

  # SSH
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # HTTP/HTTPS
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "80"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # WireGuard (Headscale)
  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "41641"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # kombifyTechstack Agent gRPC
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "5260"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
}

resource "hcloud_firewall_attachment" "node" {
  firewall_id = hcloud_firewall.node.id
  server_ids  = [hcloud_server.node.id]
}

# Outputs
output "server_id" {
  description = "The ID of the created server"
  value       = hcloud_server.node.id
}

output "server_name" {
  description = "The name of the created server"
  value       = hcloud_server.node.name
}

output "ipv4_address" {
  description = "Public IPv4 address"
  value       = hcloud_server.node.ipv4_address
}

output "ipv6_address" {
  description = "Public IPv6 address"
  value       = hcloud_server.node.ipv6_address
}

output "status" {
  description = "Server status"
  value       = hcloud_server.node.status
}
