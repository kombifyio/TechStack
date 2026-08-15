# kombifyTechstack Single Server Complete Blueprint - Services
#
# This file defines the Docker network and service modules.

# ===========================================================================
# Docker Network
# ===========================================================================

resource "docker_network" "techstack" {
  name   = "${var.stack_name}-network"
  driver = "bridge"

  labels {
    label = "techstack.io/stack"
    value = var.stack_name
  }

  depends_on = [time_sleep.wait_for_server]
}

# ===========================================================================
# Services
# ===========================================================================

# Headscale VPN
module "headscale" {
  count  = var.enable_headscale ? 1 : 0
  source = "../../modules/network/overlay"

  name        = var.stack_name
  base_domain = var.domain
  server_url  = local.headscale_url
  data_path   = "/data/headscale"
  labels      = local.common_labels

  depends_on = [docker_network.techstack]
}

# Pocketbase
module "pocketbase" {
  count  = var.enable_pocketbase ? 1 : 0
  source = "../../modules/services/pocketbase"

  name         = var.stack_name
  network_name = docker_network.techstack.name
  data_path    = "/data/pocketbase"
  http_port    = 8090
  labels       = local.common_labels

  depends_on = [docker_network.techstack]
}

# Dokploy
module "dokploy" {
  count  = var.enable_dokploy ? 1 : 0
  source = "../../modules/services/dokploy"

  name         = var.stack_name
  network_name = docker_network.techstack.name
  data_path    = "/data/dokploy"
  http_port    = 3000
  labels       = local.common_labels

  depends_on = [docker_network.techstack]
}
