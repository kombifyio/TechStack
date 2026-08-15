# kombifyTechstack Single Server Complete Blueprint - Firewall
#
# This file defines the firewall rules for the server.

resource "hcloud_firewall" "main" {
  name = "${var.stack_name}-fw"

  labels = local.common_labels

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

  # Headscale WireGuard
  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "41641"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # Headscale API
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "8080"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # Pocketbase
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "8090"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # Dokploy
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "3000"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # kombifyTechstack API
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "5260"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # kombifyTechstack gRPC
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "5261"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
}

resource "hcloud_firewall_attachment" "main" {
  firewall_id = hcloud_firewall.main.id
  server_ids  = [hcloud_server.main.id]
}
