// Package base - Security policies and configurations
package base

import "list"

// #SSHHardening defines SSH server security settings
#SSHHardening: {
    // Disable password authentication (key-only)
    passwordAuth: bool | *false
    
    // Disable root login
    permitRootLogin: "yes" | "no" | "prohibit-password" | *"no"
    
    // Maximum authentication attempts
    maxAuthTries: int & >=1 & <=10 | *3
    
    // SSH port (22 is standard, can be changed)
    port: int & >0 & <=65535 | *22
    
    // Allowed users (whitelist approach)
    allowUsers: [...string] | *["ks-admin"]
    
    // Allowed groups (alternative to allowUsers)
    allowGroups?: [...string]
    
    // Client alive interval for keepalive
    clientAliveInterval: int | *300
    
    // Client alive count max
    clientAliveCountMax: int | *3
    
    // Disable X11 forwarding
    x11Forwarding: bool | *false
    
    // Disable TCP forwarding (enable only if needed)
    allowTcpForwarding: bool | *false
    
    // Intrusion detection with fail2ban
    fail2ban: #Fail2banConfig
}

// #Fail2banConfig defines fail2ban settings
#Fail2banConfig: {
    // Enable fail2ban
    enabled: bool | *true
    
    // Maximum retries before ban
    maxRetry: int & >=1 | *5
    
    // Ban duration
    banTime: =~"^[0-9]+[smhd]$" | *"1h"
    
    // Time window for counting failures
    findTime: =~"^[0-9]+[smhd]$" | *"10m"
    
    // Action on ban (iptables, firewalld, etc.)
    banAction: "iptables-multiport" | "firewalld" | "nftables" | *"iptables-multiport"
    
    // Email notifications (optional)
    notifyEmail?: string
}

// #FirewallPolicy defines host firewall rules
#FirewallPolicy: {
    // Default policy for inbound traffic
    defaultInbound: "allow" | "deny" | *"deny"
    
    // Default policy for outbound traffic
    defaultOutbound: "allow" | "deny" | *"allow"
    
    // Firewall backend
    backend: "iptables" | "nftables" | "firewalld" | "ufw" | *"ufw"
    
    // Base rules (always applied)
    _baseRules: [
        {port: 22, protocol: "tcp", source: "any", comment: "SSH"},
    ]
    
    // Service-specific rules (merged with base)
    rules: [...#FirewallRule]
    
    // Computed: all rules = base + service rules (using list.Concat for CUE v0.11+)
    _allRules: list.Concat([_baseRules, rules])
}

// #FirewallRule defines a single firewall rule
#FirewallRule: {
    // Port number or range
    port: int & >0 & <=65535 | =~"^[0-9]+:[0-9]+$"
    
    // Protocol
    protocol: "tcp" | "udp" | "icmp" | *"tcp"
    
    // Source restriction
    source: "any" | "lan" | "vpn" | =~"^[0-9]{1,3}\\.[0-9]{1,3}\\.[0-9]{1,3}\\.[0-9]{1,3}(/[0-9]{1,2})?$"
    
    // Direction
    direction: "in" | "out" | *"in"
    
    // Action
    action: "allow" | "deny" | *"allow"
    
    // Human-readable comment
    comment?: string
}

// #ContainerSecurityContext defines security settings for containers
#ContainerSecurityContext: {
    // NEVER allow privileged containers
    privileged: false
    
    // Read-only root filesystem
    readOnlyRootfs: bool | *true
    
    // Prevent privilege escalation
    noNewPrivileges: bool | *true
    
    // Run as non-root user (ks-service UID)
    runAsUser: int | *1001
    
    // Run as non-root group
    runAsGroup: int | *1001
    
    // Ensure non-root
    runAsNonRoot: bool | *true
    
    // Linux capabilities
    capabilities: #Capabilities
    
    // Seccomp profile
    seccompProfile: "runtime/default" | "unconfined" | string | *"runtime/default"
    
    // AppArmor profile (for Docker on Ubuntu)
    apparmorProfile?: string
}

// #Capabilities defines Linux capability settings
#Capabilities: {
    // Drop all capabilities by default
    drop: [...string] | *["ALL"]
    
    // Add only what's needed
    add: [...string] | *[]
    
    // Common capability sets for reference:
    // NET_BIND_SERVICE - bind to ports < 1024
    // SYS_TIME - modify system clock
    // CHOWN - change file ownership
}

// #SecretsPolicy defines how secrets are handled
#SecretsPolicy: {
    // Never allow plaintext secrets in spec files
    allowPlaintext: false
    
    // Secret reference prefix
    secretPrefix: string | *"secret://"
    
    // Supported backends (for future integration)
    backends: [...string] | *["env", "file"]
    
    // Encryption at rest (for local storage)
    encryptionAtRest: bool | *true
}

// #AuditConfig defines audit logging settings
#AuditConfig: {
    // Enable auditd
    enabled: bool | *true
    
    // Log file path
    logFile: string | *"/var/log/audit/audit.log"
    
    // Rules to audit
    rules: [...#AuditRule] | *[
        {key: "ssh_config", path: "/etc/ssh/sshd_config", permissions: "wa"},
        {key: "passwd_changes", path: "/etc/passwd", permissions: "wa"},
        {key: "sudo_usage", path: "/etc/sudoers", permissions: "wa"},
    ]
}

#AuditRule: {
    key:         string
    path:        string
    permissions: =~"^[rwax]+$"
}

// #TLSPolicy defines TLS/HTTPS requirements
#TLSPolicy: {
    // Minimum TLS version
    minVersion: "1.2" | "1.3" | *"1.2"
    
    // Require TLS for all external communication
    requireTLS: bool | *true
    
    // Certificate source
    certSource: "acme" | "self-signed" | "manual" | *"acme"
    
    // ACME provider (if certSource == "acme")
    acmeProvider?: "letsencrypt" | "zerossl" | "buypass"
    
    // ACME email for notifications
    acmeEmail?: string
}
