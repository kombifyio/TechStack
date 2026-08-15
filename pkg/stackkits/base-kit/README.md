# Base Homelab StackKit

> Compatibility fixture only. The canonical StackKit catalog and authoring
> docs live in the separate `kombify-StackKits` repository.
>
> **Version:** 2.0.0  
> **Type:** Single-Node  
> **Difficulty:** Beginner

Das einfachste StackKit für den Einstieg in selbst-gehostete Dienste.

## Features

- ✅ Single-Server Deployment
- ✅ Traefik Reverse Proxy mit Auto-SSL
- ✅ Docker-basierte Services
- ✅ Sichere Defaults (SSH Hardening, Firewall)
- ⚠️ Kein High-Availability
- ⚠️ Kein VPN-Overlay

## Voraussetzungen

- Ein Server (lokal oder VPS) mit:
  - Debian 11/12 oder Ubuntu 22.04/24.04
  - Mindestens 2 GB RAM
  - Mindestens 20 GB Disk
  - SSH-Zugang (Key-basiert empfohlen)
- Eine Domain (für SSL-Zertifikate)
- Öffentliche IP oder Port-Forwarding (für externe Erreichbarkeit)

## Enthaltene Services

### Pflicht (required)

| Service     | Beschreibung               | Port          |
| ----------- | -------------------------- | ------------- |
| **Traefik** | Reverse Proxy mit Auto-SSL | 80, 443, 8080 |

### Empfohlen (recommended)

| Service        | Beschreibung              | Zweck      |
| -------------- | ------------------------- | ---------- |
| **Portainer**  | Container Management UI   | Verwaltung |
| **Watchtower** | Auto-Update für Container | Wartung    |

### Optional (available)

| Service     | Beschreibung          |
| ----------- | --------------------- |
| Homepage    | Startseiten-Dashboard |
| Uptime Kuma | Uptime Monitoring     |
| Dozzle      | Log Viewer            |

## Beispiel kombination.yaml

```yaml
name: my-homelab
version: "1.0"
kit: base-kit

nodes:
  - name: homeserver
    type: main
    provider: local
    ssh:
      host: 192.168.1.100
      port: 22
      user: ubuntu

services:
  - name: traefik
    config:
      email: admin@example.com

  - name: portainer
    network:
      proxy:
        domain: portainer.example.com

system:
  timezone: "Europe/Berlin"

security:
  tls:
    acmeEmail: admin@example.com
```

## Defaults

Dieses StackKit erbt alle Defaults aus `base`:

- **SSH:** Key-Only Auth, Root-Login deaktiviert
- **Firewall:** Default-Deny, nur 22/80/443 offen
- **Container:** Non-privileged, read-only rootfs
- **Logging:** journald mit JSON-Format
- **Updates:** Security-Updates automatisch

## Einschränkungen

1. **Single Node:** Keine Multi-Node-Unterstützung
2. **Kein VPN:** Nur LAN-Zugriff auf Admin-UIs
3. **Backup:** Muss manuell konfiguriert werden
4. **Monitoring:** OpenTelemetry Collector, VictoriaMetrics und Grafana nicht enthalten

## Upgrade Path

Wenn du mehr brauchst:

- **Multi-Node?** Use the external StackKit catalog.
- **VPN?** Use the external StackKit catalog.
- **HA?** Use the external StackKit catalog.

## Fehlerbehebung

### Traefik startet nicht

```bash
# Logs prüfen
docker logs traefik

# Häufige Ursache: Port 80/443 bereits belegt
sudo lsof -i :80
sudo lsof -i :443
```

### SSL-Zertifikat fehlt

- Prüfe ob die Domain auf die richtige IP zeigt
- Prüfe ob Port 80 von außen erreichbar ist (für HTTP Challenge)

### Portainer nicht erreichbar

- Prüfe ob Traefik läuft
- Prüfe die Domain-Konfiguration in `kombination.yaml`
