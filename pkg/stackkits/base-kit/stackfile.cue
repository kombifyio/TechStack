// Package base_kit provides a minimal StackKit for single-server homelab deployments.
//
// Features:
//   - Single node deployment (local server or VPS)
//   - Traefik reverse proxy with auto-SSL
//   - Docker-based service deployment
//   - Basic monitoring (optional)
//   - Secure defaults (SSH hardening, firewall)
//   - Essential system tools (e.g. Docker) will be installed by the bootstrapper when needed
//
// Use Cases:
//   - Personal home server
//   - Development environment
//   - Small self-hosted services
//
// Limitations:
//   - Single node only (no HA)
//   - No VPN overlay (local network only)
//   - Basic backup (local only)

package base_kit

import "github.com/kombihq/stackkits/base"

// #HomelabStarterKit extends the base StackKit with homelab-specific settings
#HomelabStarterKit: base.#BaseStackKit & {
    // StackKit metadata
    metadata: {
        name:        "base-kit"
        displayName: "Base Homelab"
        version:     "1.0.0"
        description: "Minimal single-server homelab setup with Traefik and Docker"
        author:      "kombifyTechstack Team"
        license:     "MIT"
        tags:        ["homelab", "single-node", "beginner", "docker"]
        
        minkombifyTechstackVersion: "1.0.0"
    }
    
    // System defaults for homelab
    system: {
        timezone:           string | *"UTC"
        locale:             "en_US.UTF-8"
        swap:               "disabled"
        unattendedUpgrades: "security"
    }
    
    // Base packages - essential tools for any well-managed server
    packages: base.#BasePackages & {
        // Homelab-specific extras
        extra: [
            "docker-compose",   // Docker Compose v2
            "rsync",            // File synchronization
            "sqlite3",          // SQLite CLI
        ]
    }
    
    // User configuration
    users: base.#SystemUsers
    
    // Docker as container runtime
    container: base.#ContainerRuntime & {
        engine:        "docker"
        rootless:      false  // Standard Docker for simplicity
        liveRestore:   true
        logDriver:     "journald"
        networkDriver: "bridge"
    }
    
    // Security settings (inherit base with homelab tweaks)
    security: {
        ssh: base.#SSHHardening & {
            // Homelab may allow password auth initially for setup
            passwordAuth:    bool | *false
            permitRootLogin: "no"
            maxAuthTries:    3
            port:            int | *22
        }
        
        firewall: base.#FirewallPolicy & {
            defaultInbound:  "deny"
            defaultOutbound: "allow"
            backend:         "ufw"
            rules: [
                // HTTP/HTTPS for reverse proxy
                {port: 80, protocol: "tcp", source: "any", comment: "HTTP"},
                {port: 443, protocol: "tcp", source: "any", comment: "HTTPS"},
            ]
        }
        
        container: base.#ContainerSecurityContext
        secrets:   base.#SecretsPolicy
        tls:       base.#TLSPolicy & {
            minVersion:  "1.2"
            requireTLS:  true
            certSource:  "acme"
            acmeProvider: "letsencrypt"
        }
        audit: base.#AuditConfig & {
            enabled: false  // Disabled by default for homelab
        }
    }
    
    // Network configuration (LAN-first for homelab)
    network: {
        defaults: base.#NetworkDefaults & {
            defaultExposure: "internal"
            adminExposure:   "internal"  // Homelab = same network, no VPN needed
            internalDomain:  string | *"home.local"
        }
        
        dns: base.#DNSConfig & {
            servers:    ["1.1.1.1", "8.8.8.8"]
            localCache: true
        }
        
        ntp: base.#NTPConfig
        
        vpn: base.#VPNConfig & {
            type: "none"  // No VPN for starter kit
        }
        
        proxy: base.#ProxyConfig & {
            enabled:           true
            type:              "traefik"
            httpPort:          80
            httpsPort:         443
            forceHTTPS:        true
            dashboardExposure: "internal"
            dashboardPort:     8080
        }
    }
    
    // Observability (minimal for homelab)
    observability: {
        logging: base.#LoggingConfig & {
            driver:    "journald"
            format:    "json"
            retention: "7d"
            level:     "info"
        }
        
        health: base.#HealthCheck & {
            enabled:     true
            interval:    "30s"
            timeout:     "10s"
            retries:     3
            startPeriod: "60s"
        }
        
        metrics: base.#MetricsConfig & {
            enabled: false  // Opt-in for homelab
        }
        
        alerting: base.#AlertingConfig & {
            enabled: false
        }
        
        backup: base.#BackupConfig & {
            enabled: false  // Opt-in, user configures
        }
    }
    
    // Required services for this StackKit
    _requiredServices: ["traefik"]
    
    // Recommended services (shown in Wizard)
    _recommendedServices: ["portainer", "watchtower"]
    
    // Available service catalog for this kit
    _availableServices: [
        "traefik",      // Reverse proxy (required)
        "portainer",    // Container management UI
        "watchtower",   // Auto-update containers
        "homepage",     // Dashboard
        "uptime-kuma",  // Uptime monitoring
        "dozzle",       // Log viewer
    ]
    
    // Node constraints for this kit
    _nodeConstraints: {
        minNodes: 1
        maxNodes: 1
        types:    ["main"]
    }
}

// #TraefikService defines the Traefik reverse proxy
#TraefikService: base.#ServiceDefinition & {
    name:        "traefik"
    displayName: "Traefik"
    type:        "reverse-proxy"
    image:       "traefik"
    tag:         "v3.0"
    description: "Modern reverse proxy and load balancer"
    
    needs: []
    
    network: {
        exposure: "public"
        ports: [
            {container: 80, host: 80, protocol: "tcp"},
            {container: 443, host: 443, protocol: "tcp"},
            {container: 8080, host: 8080, protocol: "tcp", exposure: "internal"},
        ]
    }
    
    volumes: [
        {name: "traefik-certs", mountPath: "/letsencrypt", backup: true},
        {name: "docker-socket", mountPath: "/var/run/docker.sock", hostPath: "/var/run/docker.sock", readOnly: true, type: "bind"},
    ]
    
    config: {
        api: {
            dashboard: true
            insecure:  false
        }
        entryPoints: {
            web: {
                address: ":80"
                http: redirections: entryPoint: to: "websecure"
            }
            websecure: {
                address: ":443"
            }
        }
        certificatesResolvers: {
            letsencrypt: {
                acme: {
                    email:   string  // From user spec
                    storage: "/letsencrypt/acme.json"
                    httpChallenge: entryPoint: "web"
                }
            }
        }
        providers: {
            docker: {
                exposedByDefault: false
                network:          "traefik"
            }
        }
    }
    
    restartPolicy: "unless-stopped"
}

// #PortainerService defines Portainer container management
#PortainerService: base.#ServiceDefinition & {
    name:        "portainer"
    displayName: "Portainer"
    type:        "monitoring"
    image:       "portainer/portainer-ce"
    tag:         "latest"
    description: "Container management UI"
    
    needs: ["traefik"]
    
    network: {
        exposure: "internal"
        ports: [
            {container: 9000},
        ]
        proxy: {
            domain:     string  // Set by user
            pathPrefix: "/"
            websocket:  true
        }
    }
    
    volumes: [
        {name: "portainer-data", mountPath: "/data", backup: true},
        {name: "docker-socket", mountPath: "/var/run/docker.sock", hostPath: "/var/run/docker.sock", readOnly: true, type: "bind"},
    ]
    
    restartPolicy: "unless-stopped"
}

// #WatchtowerService defines automatic container updates
#WatchtowerService: base.#ServiceDefinition & {
    name:        "watchtower"
    displayName: "Watchtower"
    type:        "automation"
    image:       "containrrr/watchtower"
    tag:         "latest"
    description: "Automatic container updates"
    
    needs: []
    
    network: {
        exposure: "internal"
        ports:    []
    }
    
    volumes: [
        {name: "docker-socket", mountPath: "/var/run/docker.sock", hostPath: "/var/run/docker.sock", readOnly: true, type: "bind"},
    ]
    
    environment: {
        WATCHTOWER_CLEANUP:       "true"
        WATCHTOWER_SCHEDULE:      "0 4 * * *"  // 4 AM daily
        WATCHTOWER_NOTIFICATIONS: "shoutrrr"
    }
    
    restartPolicy: "unless-stopped"
}
