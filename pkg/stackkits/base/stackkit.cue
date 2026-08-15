// Package base - Main StackKit composition schema
package base

// #BaseStackKit is the foundation that all StackKits extend
// It composes all the base configurations into a single structure
#BaseStackKit: {
    // StackKit metadata
    metadata: #StackKitMetadata
    
    // System configuration (host-level)
    system: #SystemConfig

    // Base package set (system tooling)
    packages: #BasePackages
    
    // User accounts
    users: #SystemUsers
    
    // Container runtime
    container: #ContainerRuntime
    
    // Security settings
    security: {
        ssh:       #SSHHardening
        firewall:  #FirewallPolicy
        container: #ContainerSecurityContext
        secrets:   #SecretsPolicy
        tls:       #TLSPolicy
        audit:     #AuditConfig
    }
    
    // Network configuration
    network: {
        defaults: #NetworkDefaults
        dns:      #DNSConfig
        ntp:      #NTPConfig
        vpn:      #VPNConfig
        proxy:    #ProxyConfig
    }
    
    // Observability
    observability: {
        logging:    #LoggingConfig
        health:     #HealthCheck
        metrics:    #MetricsConfig
        alerting:   #AlertingConfig
        monitoring: #MonitoringConfig
        backup:     #BackupConfig
    }
    
    // Service definitions (to be extended by specific StackKits)
    services: [...#ServiceDefinition]
    
    // Node definitions (to be provided by user spec)
    nodes: [...#NodeDefinition]
}

// #StackKitMetadata provides information about the StackKit
#StackKitMetadata: {
    // StackKit identifier
    name: =~"^[a-z][a-z0-9-]+$"
    
    // Display name
    displayName: string
    
    // Version
    version: =~"^[0-9]+\\.[0-9]+\\.[0-9]+(-[a-z0-9]+)?$"
    
    // Short description
    description: string
    
    // Long description (Markdown)
    longDescription?: string
    
    // Author information
    author?: string
    
    // License
    license: string | *"MIT"
    
    // Homepage URL
    homepage?: string
    
    // Minimum kombifyTechstack version required
    minkombifyTechstackVersion: string | *"1.0.0"
    
    // Tags for categorization
    tags: [...string] | *[]
    
    // Deprecated flag
    deprecated: bool | *false
    
    // Deprecation message (if deprecated)
    deprecationMessage?: string
}

// #ServiceDefinition defines a deployable service
#ServiceDefinition: {
    // Service identifier (DNS-compatible)
    name: =~"^[a-z][a-z0-9-]+$"
    
    // Display name
    displayName?: string
    
    // Service type
    type: #ServiceType
    
    // Container image
    image: string
    
    // Image tag
    tag: string | *"latest"
    
    // Service description
    description?: string
    
    // Service dependencies
    needs: [...string] | *[]
    
    // Target node (optional, defaults to "main")
    node?: string
    
    // Network configuration
    network: #ServiceNetworkConfig
    
    // Health check (overrides default)
    healthCheck?: #HealthCheck
    
    // Resource limits
    resources?: #ResourceLimits
    
    // Security context (overrides default)
    securityContext?: #ContainerSecurityContext
    
    // Environment variables
    environment?: [string]: string
    
    // Environment from secrets
    environmentSecrets?: [string]: =~"^secret://"
    
    // Volume mounts
    volumes?: [...#VolumeMount]
    
    // Service-specific configuration (varies by service)
    config?: [string]: _
    
    // Restart policy
    restartPolicy: "always" | "unless-stopped" | "on-failure" | "no" | *"unless-stopped"
    
    // Logging override
    logging?: #LoggingConfig
}

// #ServiceType categorizes services
// Keep in sync with: pkg/unifier/schema/kombination.cue, pkg/validator/spec_validator.go
#ServiceType: 
    "reverse-proxy" |
    "service" |
    "backend" |
    "database" |
    "cache" |
    "queue" |
    "paas" |
    "docker" |
    "monitoring" |
    "backup" |
    "auth" |
    "vpn" |
    "storage" |
    "media" |
    "automation" |
    "development" |
    "custom"

// #VolumeMount defines a volume attachment
#VolumeMount: {
    // Volume name
    name: string
    
    // Mount path inside container
    mountPath: string
    
    // Host path (for bind mounts)
    hostPath?: string
    
    // Read-only mount
    readOnly: bool | *false
    
    // Volume type
    type: "bind" | "volume" | "tmpfs" | *"volume"
    
    // Backup this volume
    backup: bool | *true
}

// #NodeDefinition defines a compute node
#NodeDefinition: {
    // Node identifier
    name: =~"^[a-z][a-z0-9-]+$"
    
    // Node role
    type: "main" | "worker" | "edge"
    
    // Infrastructure provider
    // Keep in sync with: pkg/unifier/schema/kombination.cue, pkg/validator/spec_validator.go
    provider: "local" | "hetzner" | "docker" | "aws" | "gcp" | "azure" | "proxmox" | "digitalocean" | "digitalocean-managed" | "centron" | "ionos"
    
    // SSH configuration (for non-docker providers)
    if provider != "docker" {
        ssh: {
            host: string
            port: int | *22
            user: string | *"ubuntu"
            keyPath?: string
        }
    }
    
    // Resource specifications
    resources?: {
        cpu:    int | *2
        memory: =~"^[0-9]+(Mi|Gi)$" | *"2Gi"
        disk:   =~"^[0-9]+(Gi|Ti)$" | *"20Gi"
    }
    
    // Node labels
    labels?: [string]: string
    
    // Taints (for scheduling)
    taints?: [...string]
}

// #UnifiedSpec is the output after unification
// This is what gets passed to OpenTofu
#UnifiedSpec: {
    // Original stack name
    name: string
    
    // StackKit used
    kit: string
    
    // Timestamp of unification
    unifiedAt: string
    
    // Resolved nodes with all computed values
    nodes: [...#ResolvedNode]
    
    // Resolved services with all computed values
    services: [...#ResolvedService]
    
    // Generated firewall rules
    firewall: [...#FirewallRule]
    
    // Generated OpenTofu variables
    tfvars: [string]: _
}

#ResolvedNode: #NodeDefinition & {
    // Computed internal IP
    _internalIP: string
    
    // Computed hostname
    _hostname: string
    
    // SSH fingerprint (for validation)
    _sshFingerprint?: string
}

#ResolvedService: #ServiceDefinition & {
    // Resolved node placement
    _targetNode: string
    
    // Computed ports (with host ports assigned)
    _resolvedPorts: [...#PortMapping]
    
    // Computed environment (with secrets dereferenced)
    _resolvedEnv: [string]: string
}
