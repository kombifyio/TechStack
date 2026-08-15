# Homelab Local Template

> **Note:** This is an OpenTofu HCL template. The intelligent configuration logic lives in StackKits (`pkg/stackkits/*.cue`).

Dieses Template bootstrappt einen lokalen Ubuntu-Server für dein Homelab.

## Voraussetzungen

- Ubuntu 22.04 oder 24.04 Server
- SSH-Zugang mit Key-basierter Authentifizierung
- Server ist von deinem Rechner aus erreichbar

## Schnellstart

1. **Template kopieren**

   ```bash
   cp terraform.tfvars.example terraform.tfvars
   ```

2. **Konfiguration anpassen**

   ```hcl
   # terraform.tfvars
   server_ip  = "192.168.1.100"  # Deine Server-IP
   stack_name = "mein-homelab"

   enable_pocketbase = true
   enable_headscale  = true
   ```

3. **Deployment starten**
   ```bash
   tofu init
   tofu plan
   tofu apply
   ```

## Services

| Service    | Port | Beschreibung              |
| ---------- | ---- | ------------------------- |
| PocketBase | 8090 | Backend, Auth, Admin-UI   |
| Headscale  | 8080 | Self-hosted Tailscale VPN |
| Dokploy    | 3000 | GitOps PaaS Platform      |

## Variablen

| Variable               | Default             | Beschreibung                           |
| ---------------------- | ------------------- | -------------------------------------- |
| `server_ip`            | -                   | **Pflicht:** IP-Adresse deines Servers |
| `stack_name`           | "my-homelab"        | Name für diesen Stack                  |
| `ssh_user`             | "ubuntu"            | SSH-Benutzername                       |
| `ssh_private_key_path` | "~/.ssh/id_ed25519" | Pfad zum SSH Private Key               |
| `enable_pocketbase`    | true                | PocketBase installieren                |
| `enable_headscale`     | true                | Headscale VPN installieren             |
| `enable_dokploy`       | false               | Dokploy PaaS installieren              |

## Nach dem Deployment

Nach erfolgreichem Deployment sind deine Services erreichbar:

- **PocketBase Admin:** `http://<server-ip>:8090/_/`
- **Headscale:** `http://<server-ip>:8080`
- **Dokploy:** `http://<server-ip>:3000` (wenn aktiviert)

### Worker Registrierung

Die Control Plane generiert für jeden Stack einen **Registration Token**, der in der Dashboard-Übersicht angezeigt wird. Verwende den Token auf deinem Server, um Agenten später mit dem Control Plane zu registrieren.

Beispiel (empfohlen):

```bash
# Setze Token als Umgebungsvariable
export KOMBI_REGISTRATION_TOKEN="<token>"

# (später) Beispiel-Befehl für die Agent-Registrierung
kombify Techstack agent register --server "http://<control-plane>" --token "$KOMBI_REGISTRATION_TOKEN"
```

## Troubleshooting

### SSH-Verbindung schlägt fehl

```bash
# Test SSH-Verbindung
ssh -i ~/.ssh/id_ed25519 ubuntu@192.168.1.100

# SSH-Key zum Agent hinzufügen
ssh-add ~/.ssh/id_ed25519
```

### Docker läuft nicht

```bash
# Auf dem Server
sudo systemctl status docker
sudo systemctl start docker
```

### Services prüfen

```bash
# Auf dem Server
sudo docker ps
sudo docker logs pocketbase
sudo docker logs headscale
```
