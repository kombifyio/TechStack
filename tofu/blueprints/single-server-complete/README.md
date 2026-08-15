# Single Server Complete Template

> **Note:** This is an OpenTofu HCL template. The intelligent configuration logic lives in StackKits (`pkg/stackkits/*.cue`).

## Overview

This OpenTofu template provisions a complete single-server infrastructure stack on Hetzner Cloud with all essential services pre-configured and ready to use.

## What Gets Deployed

### Infrastructure

- **Ubuntu 24.04 LTS** server on Hetzner Cloud
- Pre-configured firewall with secure defaults
- Docker and Docker Compose installed
- OpenTofu CLI installed on the server

### Default User

- **Username**: `kombi` (configurable)
- **SSH Access**: Configured with your public key
- **Sudo Access**: Passwordless sudo for administration
- **Groups**: `sudo`, `docker`

### Services

#### 1. Headscale VPN (Port 8080, 41641/udp)

- Self-hosted Tailscale control plane
- Creates a secure mesh network (tailnet)
- Enables private communication between nodes
- MagicDNS for easy service discovery
- **Config**: `/data/headscale/`

#### 2. Pocketbase (Port 8090)

- Lightweight backend with real-time database
- Built-in authentication system
- File storage and management
- Admin UI at `http://<server-ip>:8090/_/`
- **Data**: `/data/pocketbase/`

#### 3. Dokploy (Port 3000)

- Self-hosted PaaS platform
- Deploy apps from Git repositories
- Docker-based application management
- Web-based management interface
- **Data**: `/data/dokploy/`

### Security Features

- UFW firewall enabled by default
- Only necessary ports exposed
- SSH key-based authentication
- Optional password authentication
- Docker daemon configured with logging limits

## Prerequisites

1. **Hetzner Cloud Account**
   - Sign up at https://console.hetzner.cloud
   - Generate API token with Read & Write permissions

2. **SSH Key Pair**

   ```bash
   ssh-keygen -t ed25519 -C "your_email@example.com"
   cat ~/.ssh/id_ed25519.pub  # Copy this for admin_ssh_key
   ```

3. **OpenTofu Installed**
   ```bash
   # Install OpenTofu
   curl -fsSL https://get.opentofu.org/install-opentofu.sh | sh
   ```

## Usage

### 1. Configure Variables

Copy the example variables file:

```bash
cp terraform.tfvars.example terraform.tfvars
```

Edit `terraform.tfvars` and set:

- `hcloud_token`: Your Hetzner Cloud API token
- `admin_ssh_key`: Your SSH public key
- `stack_name`: A unique name for your deployment
- Other variables as needed

### 2. Initialize OpenTofu

```bash
tofu init
```

### 3. Plan the Deployment

```bash
tofu plan
```

Review the planned changes to ensure everything looks correct.

### 4. Deploy

```bash
tofu apply
```

Type `yes` when prompted to confirm.

The deployment takes approximately 5-10 minutes.

### 5. Access Your Server

After deployment completes, you'll see outputs with connection details:

```bash
# SSH into the server
ssh kombi@<server-ip>

# Check Docker services
docker ps

# View logs
docker compose logs -f
```

## Service Access

After deployment, access your services:

| Service          | URL                          | Purpose           |
| ---------------- | ---------------------------- | ----------------- |
| Headscale API    | `http://<server-ip>:8080`    | VPN control plane |
| Pocketbase API   | `http://<server-ip>:8090`    | Backend API       |
| Pocketbase Admin | `http://<server-ip>:8090/_/` | Admin interface   |
| Dokploy          | `http://<server-ip>:3000`    | PaaS dashboard    |

## Post-Deployment Steps

### 1. Setup Headscale

```bash
# SSH into the server
ssh kombi@<server-ip>

# Create a user in Headscale
docker exec kombify Techstack-headscale headscale users create main

# Generate a pre-auth key
docker exec kombify Techstack-headscale headscale preauthkeys create --user main --expiration 24h
```

### 2. Setup Pocketbase

1. Navigate to `http://<server-ip>:8090/_/`
2. Create your admin account
3. Configure collections and authentication as needed

### 3. Setup Dokploy

1. Navigate to `http://<server-ip>:3000`
2. Complete the initial setup wizard
3. Connect your Git repositories
4. Deploy your first application

## Customization

### Server Sizing

Adjust the `server_type` variable for different sizes:

| Type | vCPU | RAM  | Price/month |
| ---- | ---- | ---- | ----------- |
| cx22 | 2    | 4GB  | ~€5         |
| cx32 | 4    | 8GB  | ~€11        |
| cx42 | 8    | 16GB | ~€22        |

See [Hetzner Pricing](https://www.hetzner.com/cloud#pricing) for full list.

### Disable Services

Set service flags to `false` in `terraform.tfvars`:

```hcl
enable_headscale  = false  # Disable VPN
enable_pocketbase = false  # Disable backend
enable_dokploy    = false  # Disable PaaS
```

### Change Ports

Modify the module calls in `main.tf` to use different ports:

```hcl
module "pocketbase" {
  # ...
  http_port = 9090  # Changed from 8090
}
```

## Data Persistence

All service data is stored in `/data/` directory:

```
/data/
├── headscale/    # VPN configuration and database
├── pocketbase/   # Database and uploaded files
├── dokploy/      # Application data
└── kombify Techstack/   # kombify Techstack control plane data
```

### Backup Strategy

Backup these directories regularly:

```bash
# Create a backup
sudo tar -czf kombify Techstack-backup-$(date +%Y%m%d).tar.gz /data/

# Restore from backup
sudo tar -xzf kombify Techstack-backup-20240101.tar.gz -C /
```

## Troubleshooting

### Server not accessible after deployment

Wait 2-3 minutes for cloud-init to complete. Check server console in Hetzner Cloud Panel.

### Docker services not starting

```bash
# SSH into server
ssh kombi@<server-ip>

# Check Docker status
sudo systemctl status docker

# Check cloud-init logs
sudo cat /var/log/cloud-init-output.log
```

### Firewall blocking connections

```bash
# Check UFW status
sudo ufw status

# Allow additional port if needed
sudo ufw allow 9000/tcp
```

### Reset a service

```bash
# Stop and remove container
docker stop kombify Techstack-pocketbase
docker rm kombify Techstack-pocketbase

# Clear data (WARNING: deletes all data)
sudo rm -rf /data/pocketbase

# Re-run tofu apply to recreate
tofu apply
```

## Cleanup

To destroy all resources:

```bash
tofu destroy
```

Type `yes` when prompted. This will:

- Delete the Hetzner Cloud server
- Remove the SSH key from Hetzner
- Delete the firewall rules

**Note**: This does NOT delete local state files or backups.

## Architecture

```
┌─────────────────────────────────────────┐
│  Hetzner Cloud Server (Ubuntu 24.04)   │
│                                         │
│  ┌────────────────────────────────┐    │
│  │  Docker Network                │    │
│  │                                │    │
│  │  ┌──────────┐  ┌────────────┐ │    │
│  │  │Headscale │  │ Pocketbase │ │    │
│  │  │:8080     │  │ :8090      │ │    │
│  │  └──────────┘  └────────────┘ │    │
│  │                                │    │
│  │  ┌──────────┐                 │    │
│  │  │ Dokploy  │                 │    │
│  │  │ :3000    │                 │    │
│  │  └──────────┘                 │    │
│  └────────────────────────────────┘    │
│                                         │
│  Persistent Data: /data/                │
└─────────────────────────────────────────┘
         ↑
         │ SSH (Port 22)
         │ HTTP/HTTPS (80/443)
         │ Service Ports
         │
    [Internet]
```

## Integration with kombify Techstack

This blueprint is designed to work with the kombify Techstack control plane:

1. **Worker Registration**: Server can register as a kombify Techstack worker
2. **State Management**: OpenTofu state stored in SQLite database
3. **Blueprint Mapping**: Follows the single-node pattern from mapping strategy
4. **User Config**: Can be generated from kombify Techstack wizard

### User Config Example

```yaml
# kombify Techstack.yaml - User configuration
stack:
  name: my-stack
  pattern: single-node

server:
  provider: hetzner
  region: nbg1
  size: cx22

services:
  vpn: headscale
  backend: pocketbase
  paas: dokploy

admin:
  username: kombi
  ssh_key_path: ~/.ssh/id_ed25519.pub
```

This user config maps to the blueprint variables automatically.

## Next Steps

After successfully deploying this blueprint:

1. ✅ **Secure Your Services**: Configure HTTPS with Let's Encrypt
2. ✅ **Setup Monitoring**: Add OpenTelemetry Collector and optional VictoriaMetrics
3. ✅ **Configure Backups**: Set up automated backups to S3
4. ✅ **Deploy Apps**: Use Dokploy to deploy your applications
5. ✅ **Add Nodes**: Scale to multi-node with additional workers

## Related Documentation

- [Configuration](../../../docs/CONFIGURATION.md)
- [API surface map](../../../docs/API.md)
- [OpenTofu Modules](../../modules/)
- [Headscale Documentation](https://headscale.net)
- [Pocketbase Documentation](https://pocketbase.io/docs)
- [Dokploy Documentation](https://dokploy.com/docs)

## Support

For issues or questions:

- GitHub Issues: https://github.com/kombifyio/TechStack/issues
- Discussions: https://github.com/kombifyio/TechStack/discussions
