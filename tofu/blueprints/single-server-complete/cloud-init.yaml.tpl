#cloud-config
# kombifyTechstack Single Server Cloud-Init Configuration
# This configures a fresh Ubuntu server with all prerequisites

# Set hostname
hostname: ${hostname}
fqdn: ${hostname}.${domain}

# Create default user
users:
  - name: ${username}
    groups: sudo, docker
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    lock_passwd: false
    ssh_authorized_keys:
      - ${ssh_public_key}
%{ if password_hash != "" ~}
    passwd: ${password_hash}
%{ endif ~}

# Update and install packages
package_update: true
package_upgrade: true
packages:
  - apt-transport-https
  - ca-certificates
  - curl
  - gnupg
  - lsb-release
  - git
  - ufw
  - htop
  - vim
  - jq
  - unzip
  - wget

# Install Docker
runcmd:
  # Install Docker
  - curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
  - sh /tmp/get-docker.sh
  - usermod -aG docker ${username}
  - systemctl enable docker
  - systemctl start docker
  
  # Install Docker Compose
  - mkdir -p /usr/local/lib/docker/cli-plugins
  - curl -SL https://github.com/docker/compose/releases/download/v2.24.0/docker-compose-linux-x86_64 -o /usr/local/lib/docker/cli-plugins/docker-compose
  - chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
  
  # Setup firewall
  - ufw --force enable
  - ufw default deny incoming
  - ufw default allow outgoing
  - ufw allow 22/tcp comment 'SSH'
  - ufw allow 80/tcp comment 'HTTP'
  - ufw allow 443/tcp comment 'HTTPS'
  - ufw allow 41641/udp comment 'Headscale WireGuard'
  - ufw allow 8080/tcp comment 'Headscale API'
  - ufw allow 8090/tcp comment 'Pocketbase'
  - ufw allow 3000/tcp comment 'Dokploy'
  - ufw allow 5260/tcp comment 'kombifyTechstack API'
  - ufw allow 5261/tcp comment 'kombifyTechstack gRPC'
  
  # Create data directories
  - mkdir -p /data/{headscale,pocketbase,dokploy,techstack}
  - chown -R ${username}:${username} /data
  
  # Install OpenTofu
  - curl -fsSL https://get.opentofu.org/install-opentofu.sh | sh
  
  # Setup systemd for Docker services
  - systemctl daemon-reload

# Write additional files
write_files:
  - path: /etc/docker/daemon.json
    content: |
      {
        "log-driver": "json-file",
        "log-opts": {
          "max-size": "10m",
          "max-file": "3"
        },
        "storage-driver": "overlay2"
      }
    permissions: '0644'
  
  - path: /etc/sysctl.d/99-techstack.conf
    content: |
      # Enable IP forwarding for VPN
      net.ipv4.ip_forward=1
      net.ipv6.conf.all.forwarding=1
    permissions: '0644'
  
  - path: /home/${username}/.bashrc.d/techstack.sh
    content: |
      # kombifyTechstack aliases
      alias docker-ps='docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"'
      alias docker-logs='docker compose logs -f'
      alias ks-status='systemctl status techstack'
    permissions: '0644'
    owner: ${username}:${username}

# Apply sysctl changes
bootcmd:
  - sysctl -p /etc/sysctl.d/99-techstack.conf

# Final message
final_message: |
  kombifyTechstack server initialization complete!
  Uptime: $UPTIME seconds
  
  Services will be deployed via OpenTofu:
  - Headscale VPN: http://${hostname}:8080
  - Pocketbase: http://${hostname}:8090
  - Dokploy: http://${hostname}:3000
  
  Default user: ${username}
  SSH access: ssh ${username}@${hostname}
