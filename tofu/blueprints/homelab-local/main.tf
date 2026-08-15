# kombifyTechstack Homelab Local Blueprint
#
# This blueprint configures a local Ubuntu server as part of your homelab.
# Unlike cloud blueprints, this uses the null provider + SSH for bootstrapping.
#
# Requirements:
# - Ubuntu 22.04/24.04 server in your local network
# - SSH access with key-based authentication
# - Server reachable from the kombifyTechstack control plane
#
# Usage:
#   1. Set your server IP and SSH key path in terraform.tfvars
#   2. Run: tofu init && tofu apply

terraform {
  required_version = ">= 1.6"
  
  required_providers {
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
  }
}

# ===========================================================================
# Variables
# ===========================================================================

variable "stack_name" {
  description = "Name of this kombifyTechstack homelab"
  type        = string
  default     = "my-homelab"
}

variable "server_ip" {
  description = "IP address of your Ubuntu server"
  type        = string
}

variable "ssh_user" {
  description = "SSH username for the server"
  type        = string
  default     = "ubuntu"
}

variable "ssh_private_key_path" {
  description = "Path to SSH private key for authentication"
  type        = string
  default     = "~/.ssh/id_ed25519"
}

variable "admin_username" {
  description = "Username for the admin account"
  type        = string
  default     = "kombi"
}

# Services to enable
variable "enable_pocketbase" {
  description = "Install PocketBase backend"
  type        = bool
  default     = true
}

variable "enable_headscale" {
  description = "Install Headscale VPN"
  type        = bool
  default     = true
}

variable "enable_dokploy" {
  description = "Install Dokploy PaaS"
  type        = bool
  default     = false
}

variable "enable_monitoring" {
  description = "Install observability profile (OpenTelemetry Collector baseline)"
  type        = bool
  default     = false
}

variable "control_plane_url" {
  description = "URL of the kombifyTechstack control plane"
  type        = string
  default     = ""
}

variable "registration_token" {
  description = "Token for registering this agent with control plane"
  type        = string
  default     = ""
  sensitive   = true
}

# ===========================================================================
# Bootstrap Script
# ===========================================================================

locals {
  services_to_install = compact([
    var.enable_pocketbase ? "pocketbase" : "",
    var.enable_headscale ? "headscale" : "",
    var.enable_dokploy ? "dokploy" : "",
    var.enable_monitoring ? "monitoring" : "",
  ])
  
  bootstrap_script = <<-EOF
    #!/bin/bash
    set -e
    
    echo "🏠 kombifyTechstack Homelab Bootstrap"
    echo "================================"
    echo "Stack: ${var.stack_name}"
    echo "Server: ${var.server_ip}"
    echo "Services: ${join(", ", local.services_to_install)}"
    echo ""
    
    # Update system
    echo "📦 Updating system packages..."
    apt-get update -q
    DEBIAN_FRONTEND=noninteractive apt-get upgrade -y -q
    
    # Install Docker if not present
    if ! command -v docker &> /dev/null; then
      echo "🐳 Installing Docker..."
      curl -fsSL https://get.docker.com | sh
      usermod -aG docker ${var.ssh_user}
    fi
    
    # Create techstack directory
    mkdir -p /opt/techstack/{data,certs,services}
    
    # Install PocketBase
    %{ if var.enable_pocketbase }
    echo "🗄️ Installing PocketBase..."
    docker pull ghcr.io/pocketbase/pocketbase:latest
    docker run -d \
      --name pocketbase \
      --restart unless-stopped \
      -p 8090:8090 \
      -v /opt/techstack/data/pocketbase:/pb_data \
      ghcr.io/pocketbase/pocketbase:latest
    %{ endif }
    
    # Install Headscale
    %{ if var.enable_headscale }
    echo "🔐 Installing Headscale..."
    docker pull headscale/headscale:latest
    mkdir -p /opt/techstack/data/headscale
    docker run -d \
      --name headscale \
      --restart unless-stopped \
      -p 8080:8080 \
      -v /opt/techstack/data/headscale:/var/lib/headscale \
      headscale/headscale:latest serve
    %{ endif }
    
    # Install Dokploy
    %{ if var.enable_dokploy }
    echo "🚀 Installing Dokploy..."
    docker pull dokploy/dokploy:latest
    docker run -d \
      --name dokploy \
      --restart unless-stopped \
      -p 3000:3000 \
      -v /var/run/docker.sock:/var/run/docker.sock \
      -v /opt/techstack/data/dokploy:/app/.dokploy \
      dokploy/dokploy:latest
    %{ endif }
    
    # Download and install kombifyTechstack agent
    echo "📥 Installing kombifyTechstack Agent..."
    AGENT_DIR="/opt/techstack/agent"
    mkdir -p $AGENT_DIR
    
    # For now, we'll create a placeholder - in production this downloads from GitHub releases
    cat > $AGENT_DIR/start.sh << 'AGENT_SCRIPT'
    #!/bin/bash
    export TECHSTACK_CORE_ADDR="${var.control_plane_url}"
    export TECHSTACK_AGENT_ID="agent-${var.stack_name}"
    echo "kombifyTechstack Agent starting..."
    echo "Core: $TECHSTACK_CORE_ADDR"
    echo "Agent ID: $TECHSTACK_AGENT_ID"
    # Agent binary would run here
    AGENT_SCRIPT
    chmod +x $AGENT_DIR/start.sh
    
    # Create systemd service for agent
    cat > /etc/systemd/system/techstack-agent.service << 'SYSTEMD'
    [Unit]
    Description=kombifyTechstack Agent
    After=network.target docker.service
    
    [Service]
    Type=simple
    ExecStart=/opt/techstack/agent/start.sh
    Restart=always
    RestartSec=10
    
    [Install]
    WantedBy=multi-user.target
    SYSTEMD
    
    systemctl daemon-reload
    systemctl enable techstack-agent
    
    echo ""
    echo "✅ Bootstrap complete!"
    echo ""
    echo "Services running:"
    docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
    echo ""
    echo "Access your services:"
    %{ if var.enable_pocketbase }
    echo "  - PocketBase: http://${var.server_ip}:8090/_/"
    %{ endif }
    %{ if var.enable_headscale }
    echo "  - Headscale:  http://${var.server_ip}:8080"
    %{ endif }
    %{ if var.enable_dokploy }
    echo "  - Dokploy:    http://${var.server_ip}:3000"
    %{ endif }
  EOF
}

# ===========================================================================
# Resources
# ===========================================================================

resource "null_resource" "bootstrap" {
  triggers = {
    services = join(",", local.services_to_install)
    server   = var.server_ip
  }
  
  connection {
    type        = "ssh"
    host        = var.server_ip
    user        = var.ssh_user
    private_key = file(pathexpand(var.ssh_private_key_path))
  }
  
  provisioner "remote-exec" {
    inline = [
      "echo '${base64encode(local.bootstrap_script)}' | base64 -d > /tmp/bootstrap.sh",
      "chmod +x /tmp/bootstrap.sh",
      "sudo /tmp/bootstrap.sh",
    ]
  }
}

# ===========================================================================
# Outputs
# ===========================================================================

output "server_ip" {
  description = "IP address of the homelab server"
  value       = var.server_ip
}

output "stack_name" {
  description = "Name of this homelab stack"
  value       = var.stack_name
}

output "services" {
  description = "List of enabled services"
  value       = local.services_to_install
}

output "pocketbase_url" {
  description = "PocketBase admin URL"
  value       = var.enable_pocketbase ? "http://${var.server_ip}:8090/_/" : null
}

output "headscale_url" {
  description = "Headscale URL"
  value       = var.enable_headscale ? "http://${var.server_ip}:8080" : null
}

output "dokploy_url" {
  description = "Dokploy URL"
  value       = var.enable_dokploy ? "http://${var.server_ip}:3000" : null
}
