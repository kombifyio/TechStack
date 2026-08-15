// VPN Overlay add-on for kombifyTechstack.
// Activates when VPN is configured in network settings.
package addon

vpnOverlay: #Addon & {
    metadata: {
        name:        "vpn-overlay"
        displayName: "VPN Overlay"
        description: "VPN mesh network configuration and secure tunneling"
        version:     "1.0.0"
        priority:    70
        tags: ["vpn", "network", "security", "mesh"]
    }

    condition: {
        network: {
            vpn: ["wireguard", "headscale", "tailscale", "zerotier"]
        }
    }

    spec: {
        // Pre-checks for VPN
        preChecks: [
            {
                type:        "wireguard_kernel"
                description: "WireGuard kernel module or userspace tools"
                blocking:    false
            },
        ]

        // Network firewall rules for VPN
        network: {
            firewall: [
                {
                    port:     51820
                    protocol: "udp"
                    action:   "allow"
                },
            ]
        }

        // Environment for VPN
        environment: {
            TECHSTACK_VPN_ENABLED: "true"
        }

        // Labels for VPN resources
        labels: {
            "techstack.io/vpn-enabled": "true"
        }
    }
}
