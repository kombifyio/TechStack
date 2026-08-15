// Cloud Integration add-on for kombifyTechstack.
// Activates when cloud provider nodes (Hetzner, AWS, etc.) are detected.
package addon

cloudIntegration: #Addon & {
    metadata: {
        name:        "cloud-integration"
        displayName: "Cloud Integration"
        description: "VPN overlay network, cloud security policies, and external DNS"
        version:     "1.0.0"
        priority:    100
        tags: ["cloud", "hybrid", "vpn", "dns"]
    }

    condition: {
        providers: ["hetzner", "aws", "gcp", "azure", "digitalocean", "digitalocean-managed", "centron", "ionos", "linode", "vultr"]
    }

    spec: {
        // Pre-checks for cloud connectivity
        preChecks: [
            {
                type:        "network_connectivity"
                description: "Verify internet connectivity for cloud API access"
                blocking:    true
            },
        ]

        // Network extensions for cloud
        network: {
            // Additional DNS servers for failover
            extraDNS: ["1.1.1.1", "8.8.8.8"]
            
            // Firewall rules for cloud security
            firewall: [
                {
                    port:     22
                    protocol: "tcp"
                    source:   "10.0.0.0/8"
                    action:   "allow"
                },
                {
                    port:     443
                    protocol: "tcp"
                    action:   "allow"
                },
            ]
        }

        // Environment for cloud services
        environment: {
            TECHSTACK_CLOUD_MODE: "hybrid"
        }

        // Labels for cloud resources
        labels: {
            "techstack.io/cloud-enabled": "true"
            "techstack.io/hybrid-mode":   "true"
        }
    }
}
