// Package base - Network configuration and policies
package base

import "list"

// #NetworkDefaults defines network-level settings
#NetworkDefaults: {
    // Default service exposure level
    defaultExposure: #ServiceExposure | *"internal"
    
    // Admin UIs are always VPN-only by default
    adminExposure: #ServiceExposure | *"vpn"
    
    // Internal domain for service discovery
    internalDomain: =~"^[a-z][a-z0-9.-]+$" | *"home.local"
    
    // Enable IPv6 support
    ipv6Enabled: bool | *false
    
    // MTU for internal network
    mtu: int & >=576 & <=9000 | *1500
}

// #ServiceExposure defines how a service is accessible
#ServiceExposure: "internal" | "vpn" | "public"

// Exposure level descriptions:
// - internal: Only accessible from LAN (same network segment)
// - vpn: Only accessible via VPN connection
// - public: Accessible from the internet (requires explicit port exposure)

// #DNSConfig defines DNS resolution settings
#DNSConfig: {
    // Upstream DNS servers
    servers: [...string] | *["1.1.1.1", "8.8.8.8"]
    
    // Search domains for resolution
    searchDomains: [...string] | *[]
    
    // Enable local DNS caching (dnsmasq/systemd-resolved)
    localCache: bool | *true
    
    // DNS over HTTPS/TLS
    dnsOverTLS: bool | *false
    
    // DNSSEC validation
    dnssec: "off" | "allow-downgrade" | "on" | *"allow-downgrade"
}

// #NTPConfig defines time synchronization settings
#NTPConfig: {
    // NTP is always enabled (critical for TLS, logs, etc.)
    enabled: true
    
    // NTP servers
    servers: [...string] | *["pool.ntp.org"]
    
    // Fallback servers
    fallbackServers: [...string] | *["time.google.com", "time.cloudflare.com"]
    
    // Allow large time jumps on startup
    allowLargeAdjustment: bool | *true
    
    // Hardware clock in UTC
    hwclockUTC: bool | *true
}

// #VPNConfig defines VPN overlay network settings
#VPNConfig: {
    // VPN type (none = no VPN overlay)
    type: "none" | "wireguard" | "headscale" | "tailscale" | *"none"
    
    // VPN interface name
    interface: string | *"wg0"
    
    // Listen port (for self-hosted VPN)
    listenPort: int & >0 & <=65535 | *51820
    
    // VPN subnet (for Wireguard)
    subnet?: =~"^[0-9]{1,3}\\.[0-9]{1,3}\\.[0-9]{1,3}\\.[0-9]{1,3}/[0-9]{1,2}$"
    
    // Headscale-specific settings
    if type == "headscale" {
        headscale: #HeadscaleConfig
    }
    
    // Tailscale-specific settings
    if type == "tailscale" {
        tailscale: #TailscaleConfig
    }
}

#HeadscaleConfig: {
    // Headscale server URL
    serverUrl: string
    
    // Auth key reference (not plaintext)
    authKeyRef: =~"^secret://" | string
    
    // Namespace/user
    namespace: string | *"default"
}

#TailscaleConfig: {
    // Auth key reference (not plaintext)
    authKeyRef: =~"^secret://" | string
    
    // Accept routes from other nodes
    acceptRoutes: bool | *true
    
    // Advertise as exit node
    exitNode: bool | *false
}

// #PortMapping defines service port exposure
#PortMapping: {
    // Container port (internal)
    container: int & >0 & <=65535
    
    // Host port (external, optional)
    host?: int & >0 & <=65535
    
    // Protocol
    protocol: "tcp" | "udp" | *"tcp"
    
    // Exposure level (overrides service default)
    exposure?: #ServiceExposure
    
    // Bind address (empty = all interfaces; set loopback for local-only)
    bindAddress: string | *""
}

// #ProxyConfig defines reverse proxy settings
#ProxyConfig: {
    // Enable reverse proxy
    enabled: bool | *true
    
    // Proxy type
    type: "traefik" | "caddy" | "nginx" | *"traefik"
    
    // HTTP port
    httpPort: int | *80
    
    // HTTPS port
    httpsPort: int | *443
    
    // Auto-redirect HTTP to HTTPS
    forceHTTPS: bool | *true
    
    // Dashboard exposure (admin UI)
    dashboardExposure: #ServiceExposure | *"vpn"
    
    // Dashboard port
    dashboardPort: int | *8080
}

// #ServiceNetworkConfig defines per-service network settings
#ServiceNetworkConfig: {
    // Service exposure level
    exposure: #ServiceExposure | *"internal"
    
    // Port mappings
    ports: [...#PortMapping]
    
    // Reverse proxy settings (for public/vpn services)
    proxy?: {
        // Domain name for this service
        domain: string
        
        // Path prefix (default: /)
        pathPrefix: string | *"/"
        
        // Enable websocket support
        websocket: bool | *false
        
        // Custom headers
        headers?: [string]: string
        
        // Health check path
        healthPath: string | *"/health"
    }
    
    // Internal DNS aliases
    aliases?: [...string]
}

// Reserved ports that cannot be used by custom services
#ReservedPorts: [22, 80, 443, 5260, 5261, 5263, 5265]

// #PortValidator validates port assignments
#PortValidator: {
    port: int & >0 & <=65535
    
    // Fail if port is reserved
    _valid: !list.Contains(#ReservedPorts, port)
}
