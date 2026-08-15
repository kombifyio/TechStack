# kombifyTechstack Single Server Complete Blueprint - Outputs
#
# This file defines all output values.

output "server_id" {
  description = "The ID of the created server"
  value       = hcloud_server.main.id
}

output "server_name" {
  description = "The name of the server"
  value       = hcloud_server.main.name
}

output "server_ipv4" {
  description = "Public IPv4 address"
  value       = hcloud_server.main.ipv4_address
}

output "server_ipv6" {
  description = "Public IPv6 address"
  value       = hcloud_server.main.ipv6_address
}

output "ssh_command" {
  description = "SSH command to connect to the server"
  value       = "ssh ${var.admin_username}@${hcloud_server.main.ipv4_address}"
}

output "headscale_url" {
  description = "Headscale server URL"
  value       = var.enable_headscale ? local.headscale_url : "disabled"
}

output "pocketbase_url" {
  description = "Pocketbase API URL"
  value       = var.enable_pocketbase ? "http://${hcloud_server.main.ipv4_address}:8090" : "disabled"
}

output "pocketbase_admin_url" {
  description = "Pocketbase Admin UI URL"
  value       = var.enable_pocketbase ? "http://${hcloud_server.main.ipv4_address}:8090/_/" : "disabled"
}

output "dokploy_url" {
  description = "Dokploy UI URL"
  value       = var.enable_dokploy ? "http://${hcloud_server.main.ipv4_address}:3000" : "disabled"
}

output "status" {
  description = "Deployment status summary"
  value = {
    server_ready     = hcloud_server.main.status
    headscale_ready  = var.enable_headscale
    pocketbase_ready = var.enable_pocketbase
    dokploy_ready    = var.enable_dokploy
  }
}
