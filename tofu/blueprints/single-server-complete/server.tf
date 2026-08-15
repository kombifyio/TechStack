# kombifyTechstack Single Server Complete Blueprint - Server Resources
#
# This file defines the server provisioning resources.

# ===========================================================================
# SSH Key
# ===========================================================================

resource "hcloud_ssh_key" "admin" {
  name       = "${var.stack_name}-admin-key"
  public_key = var.admin_ssh_key

  labels = local.common_labels
}

# ===========================================================================
# Server Provisioning
# ===========================================================================

# Cloud-init configuration
data "template_file" "cloud_init" {
  template = file("${path.module}/cloud-init.yaml.tpl")

  vars = {
    hostname       = local.hostname
    domain         = var.domain
    username       = var.admin_username
    ssh_public_key = var.admin_ssh_key
    password_hash  = var.admin_password_hash
  }
}

# Main server
resource "hcloud_server" "main" {
  name        = local.hostname
  server_type = var.server_type
  location    = var.location
  image       = var.image
  ssh_keys    = [hcloud_ssh_key.admin.id]
  user_data   = data.template_file.cloud_init.rendered

  labels = merge(local.common_labels, {
    "techstack.io/role" = "main"
  })

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }

  lifecycle {
    ignore_changes = [user_data]
  }
}

# Wait for server to be ready
resource "time_sleep" "wait_for_server" {
  depends_on = [hcloud_server.main]

  create_duration = "60s"
}
