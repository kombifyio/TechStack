// Package base - System configuration defaults
package base

import "list"

// #SystemConfig defines host-level system settings
#SystemConfig: {
    // Timezone for the host (UTC recommended for consistency)
    timezone: string | *"UTC"
    
    // System locale
    locale: string | *"en_US.UTF-8"
    
    // Swap configuration (disabled recommended for containers)
    swap: "disabled" | "enabled" | *"disabled"
    
    // Automatic security updates
    unattendedUpgrades: "disabled" | "security" | "all" | *"security"
    
    // Hostname prefix (stack name appended)
    hostnamePrefix?: string
}

// #SystemUsers defines the standard user accounts
#SystemUsers: {
    // Admin user for SSH access and management
    admin: #SystemUser & {
        name:   string | *"ks-admin"
        shell:  string | *"/bin/bash"
        sudo:   true
        groups: [...string] | *["docker", "sudo"]
    }
    
    // Service user for container processes (no login)
    service: #SystemUser & {
        name:   string | *"ks-service"
        shell:  string | *"/usr/sbin/nologin"
        sudo:   false
        uid:    int | *1001
        gid:    int | *1001
        groups: [...string] | *["docker"]
    }
    
    // Additional users (optional)
    extra?: [...#SystemUser]
}

// #SystemUser defines a single system user
#SystemUser: {
    name:    =~"^[a-z_][a-z0-9_-]{0,31}$"
    shell:   string | *"/bin/bash"
    sudo:    bool | *false
    uid?:    int & >=1000 & <=65534
    gid?:    int & >=1000 & <=65534
    groups?: [...string]
    home?:   string
    
    // SSH authorized keys (references, not actual keys)
    sshKeys?: [...string]
}

// #ContainerRuntime defines the container engine
#ContainerRuntime: {
    // Engine type
    engine: "docker" | "podman" | *"docker"
    
    // Enable rootless mode (recommended for Podman)
    rootless: bool | *false
    
    // Docker/Podman socket path
    socketPath: string | *"/var/run/docker.sock"
    
    // Default network driver
    networkDriver: "bridge" | "host" | "overlay" | *"bridge"
    
    // Enable live-restore (keep containers running during daemon restart)
    liveRestore: bool | *true
    
    // Log driver for containers
    logDriver: "journald" | "json-file" | "syslog" | *"journald"
    
    // Default resource limits
    defaultLimits?: #ResourceLimits
}

// #ResourceLimits defines container resource constraints
#ResourceLimits: {
    // CPU limit (number of cores or fraction)
    cpuLimit?: number & >=0.1
    
    // Memory limit (e.g., "512Mi", "2Gi")
    memoryLimit?: =~"^[0-9]+(Mi|Gi)$"
    
    // Memory reservation (soft limit)
    memoryReservation?: =~"^[0-9]+(Mi|Gi)$"
    
    // PIDs limit (prevent fork bombs)
    pidsLimit?: int & >=10 | *200
}

// #PackageManager defines system package configuration
#PackageManager: {
    // Package manager type (auto-detected from OS)
    type: "apt" | "dnf" | "yum" | "apk"
    
    // Additional repositories to enable
    extraRepos?: [...#PackageRepo]
    
    // Packages to install beyond base requirements
    extraPackages?: [...string]
}

#PackageRepo: {
    name:    string
    url:     string
    keyUrl?: string
    enabled: bool | *true
}

// #BasePackages defines essential system tools that should always be installed
// These are the foundation tools needed for any well-managed server.
#BasePackages: {
    // Essential CLI tools
    essential: [...string] | *[
        "curl",           // HTTP client
        "wget",           // File downloader
        "htop",           // Interactive process viewer
        "vim",            // Text editor
        "nano",           // Simple text editor
        "git",            // Version control
        "jq",             // JSON processor
        "tree",           // Directory listing
        "tmux",           // Terminal multiplexer
        "unzip",          // Archive extraction
        "zip",            // Archive creation
    ]
    
    // Network diagnostics
    network: [...string] | *[
        "net-tools",      // ifconfig, netstat, etc.
        "dnsutils",       // dig, nslookup
        "iputils-ping",   // ping
        "traceroute",     // Network path tracing
        "tcpdump",        // Packet capture
        "iftop",          // Network bandwidth monitor
        "nmap",           // Network scanner
    ]
    
    // System monitoring
    monitoring: [...string] | *[
        "sysstat",        // System statistics (sar, iostat)
        "iotop",          // I/O monitor
        "lsof",           // List open files
        "strace",         // System call tracer
        "dstat",          // Versatile resource statistics
    ]
    
    // Security tools
    security: [...string] | *[
        "fail2ban",       // Brute-force protection
        "ufw",            // Uncomplicated firewall
        "ca-certificates",// SSL certificates
        "gnupg",          // Encryption
        "openssl",        // SSL toolkit
    ]
    
    // Container support
    container: [...string] | *[
        "ca-certificates",
        "gnupg",
        "lsb-release",
        "apt-transport-https",
    ]
    
    // User-defined additional packages
    extra: [...string]
    
    // Combined list of all packages
    all: list.Concat([essential, network, monitoring, security, container, extra])
}

// #SystemDefaults combines all system-level defaults
#SystemDefaults: {
    config:   #SystemConfig
    users:    #SystemUsers
    runtime:  #ContainerRuntime
    packages: #BasePackages
}
