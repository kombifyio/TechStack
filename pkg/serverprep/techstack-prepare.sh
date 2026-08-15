#!/bin/bash
set -e

# kombify Techstack Server Preparation Script
# Supported OS: Ubuntu 20.04, 22.04, 24.04
# Installs: Docker, OpenTofu, Terramate (optional)

LOG_FILE="/var/log/techstack-prepare.log"
INSTALL_TERRAMATE=false

log() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

error() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] ERROR: $1" | tee -a "$LOG_FILE"
    exit 1
}

check_root() {
    if [ "$EUID" -ne 0 ]; then
        error "Please run as root"
    fi
}

check_os() {
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        if [ "$ID" != "ubuntu" ]; then
            error "This script currently supports Ubuntu only. Detected: $ID"
        fi
        if [[ "$VERSION_ID" != "20.04" && "$VERSION_ID" != "22.04" && "$VERSION_ID" != "24.04" ]]; then
             log "Warning: Untested Ubuntu version: $VERSION_ID. Proceeding..."
        fi
    else
        error "Cannot determine OS. /etc/os-release not found."
    fi
}

install_dependencies() {
    log "Installing system dependencies..."
    apt-get update -qq
    apt-get install -y -qq ca-certificates curl gnupg lsb-release unzip git jq
}

install_docker() {
    if command -v docker &> /dev/null; then
        log "Docker is already installed."
    else
        log "Installing Docker..."
        install -m 0755 -d /etc/apt/keyrings
        if [ ! -f /etc/apt/keyrings/docker.gpg ]; then
            curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
            chmod a+r /etc/apt/keyrings/docker.gpg
        fi

        echo \
          "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu \
          $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
          tee /etc/apt/sources.list.d/docker.list > /dev/null

        apt-get update -qq
        apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
        log "Docker installed successfully."
    fi

    # Enable and start Docker via systemd if available (not available in containers/CI nodes)
    if [ -d /run/systemd/system ]; then
        systemctl enable docker
        systemctl start docker
    else
        log "systemd not available — skipping 'systemctl enable/start docker' (container environment)"
    fi
}

install_opentofu() {
    if command -v tofu &> /dev/null; then
        log "OpenTofu is already installed."
    else
        log "Installing OpenTofu..."
        # Using the install script from opentofu.org which handles repo setup
        curl --proto '=https' --tlsv1.2 -fsSL https://get.opentofu.org/install-opentofu.sh -o install-opentofu.sh
        chmod +x install-opentofu.sh
        ./install-opentofu.sh --install-method deb
        rm install-opentofu.sh
        log "OpenTofu installed successfully."
    fi
}

install_terramate() {
    if [ "$INSTALL_TERRAMATE" = "true" ]; then
        if command -v terramate &> /dev/null; then
            log "Terramate is already installed."
        else
            log "Installing Terramate..."
            # Latest release lookup via GitHub API could be flaky without token, use direct download or specific version if needed.
            # Using a fixed reliable method or latest from their docs.
            local TM_VERSION=$(curl -s https://api.github.com/repos/terramate-io/terramate/releases/latest | grep tag_name | cut -d '"' -f 4 | sed 's/v//')
            if [ -z "$TM_VERSION" ]; then
                log "Could not detect latest Terramate version. Using default fallback."
                TM_VERSION="0.4.5" 
            fi
            
            curl -L "https://github.com/terramate-io/terramate/releases/download/v${TM_VERSION}/terramate_${TM_VERSION}_linux_amd64.tar.gz" -o terramate.tar.gz
            tar -xzf terramate.tar.gz terramate
            mv terramate /usr/local/bin/
            rm terramate.tar.gz
            log "Terramate installed successfully."
        fi
    else
        log "Skipping Terramate installation (use --terramate to enable)."
    fi
}

configure_system() {
    log "Configuring system..."
    # Add current user to docker group if sudo was used
    if [ -n "$SUDO_USER" ]; then
        usermod -aG docker "$SUDO_USER" || true
        log "Added $SUDO_USER to docker group."
    fi
    
    # Basic security (UFW) - ensure SSH is allowed
    if command -v ufw &> /dev/null; then
        if ! ufw status | grep -q "Status: active"; then
             log "UFW is not active. Not modifying rules to avoid locking out."
        else
             ufw allow ssh
             log "Ensured UFW allows SSH."
        fi
    fi
}

# Parse args
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --terramate) INSTALL_TERRAMATE=true ;;
        --help) echo "Usage: $0 [--terramate]"; exit 0 ;;
        *) echo "Unknown parameter passed: $1"; exit 1 ;;
    esac
    shift
done

log "Starting kombify Techstack Server Preparation..."
check_root
check_os
install_dependencies
install_docker
install_opentofu
install_terramate
configure_system

log "Server preparation complete! You may need to logout and login again for group changes to take effect."
