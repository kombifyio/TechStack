// Package addon defines the base schema for kombifyTechstack add-ons.
// Add-ons extend StackKits with conditional capabilities.
package addon

// Addon represents a kombifyTechstack add-on definition.
// Add-ons are conditionally activated based on spec characteristics.
#Addon: {
    // Metadata
    apiVersion: "techstack.io/v1alpha1"
    kind:       "Addon"

    metadata: {
        name:        string & =~"^[a-z][a-z0-9-]{2,62}$"
        displayName: string
        description: string
        version:     string | *"1.0.0"
        
        // Priority for load order (higher = earlier)
        priority: int | *50
        
        // Tags for categorization
        tags: [...string] | *[]
    }

    // Condition defines when this add-on activates.
    // This is evaluated against the KombinationSpec.
    condition?: {
        // Match on provider types
        providers?: [...string]
        
        // Match on node tags
        nodeTags?: [string]: string
        
        // Match on network settings
        network?: {
            vpn?: string | [...string]
        }
        
        // Match on minimum node count
        minNodes?: int
        
        // Match on metadata fields
        metadata?: [string]: string
        
        // Custom CEL expression (future)
        expression?: string
    }

    // Spec defines what the add-on provides.
    spec: {
        // Service definitions to add/modify
        services?: [string]: #ServiceOverlay

        // Network configuration extensions
        network?: #NetworkExtension

        // Environment variables to inject
        environment?: [string]: string

        // Labels to add to all resources
        labels?: [string]: string

        // Annotations for resources
        annotations?: [string]: string

        // Pre-checks required for this add-on
        preChecks?: [...#PreCheckDef]

        // Resource constraints
        resources?: #ResourceConstraints
    }
}

// ServiceOverlay defines modifications to a service.
#ServiceOverlay: {
    // Whether to enable/disable the service
    enabled?: bool
    
    // Image override
    image?: string
    
    // Additional environment variables
    environment?: [string]: string
    
    // Resource limits
    resources?: {
        memory?: string
        cpu?:    string
    }
    
    // Additional labels
    labels?: [string]: string
    
    // Volumes to mount
    volumes?: [...{
        name:      string
        mountPath: string
        readOnly?: bool
    }]
}

// NetworkExtension defines network configuration extensions.
#NetworkExtension: {
    // Additional DNS servers
    extraDNS?: [...string]
    
    // Custom routes
    routes?: [...{
        destination: string
        gateway:     string
    }]
    
    // Firewall rules to add
    firewall?: [...{
        port:     int
        protocol: "tcp" | "udp" | *"tcp"
        source?:  string
        action:   "allow" | "deny" | *"allow"
    }]
}

// PreCheckDef defines a pre-deployment check.
#PreCheckDef: {
    type:        string
    description: string
    minVersion?: string
    blocking:    bool | *false
}

// ResourceConstraints defines resource allocation guidance.
#ResourceConstraints: {
    // Minimum memory required for add-on services
    minMemory?: string
    
    // Recommended memory
    recommendedMemory?: string
    
    // GPU requirements
    gpu?: {
        required: bool | *false
        vendor?:  "nvidia" | "amd"
        minVRAM?: string
    }
}
