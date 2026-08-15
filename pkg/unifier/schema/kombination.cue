// Package schema defines the extended CUE schemas for kombifyTechstack spec validation.
// This file contains the complete kombination.yaml schema with all fields.
package schema

import (
	"time"
)

// #KombinationSpec is the root schema for kombination.yaml files.
// This represents the user's desired state before unification.
#KombinationSpec: {
	// Required: Name of the stack (must be DNS-compatible)
	name: #DNSName

	// Optional: Schema version for future compatibility
	version?: =~"^[0-9]+\\.[0-9]+$" | *"1.0"

	// Optional: StackKit to use (empty string = auto-select)
	kit?: #StackKitRef | *""

	// Required: At least one node must be defined
	nodes: [#Node, ...#Node]

	// Optional: Services to deploy
	services?: [...#Service]

	// Optional: System configuration
	system?: #SystemConfig

	// Optional: Security configuration
	security?: #SecurityConfig

	// Optional: Network configuration
	network?: #NetworkConfig

	// Optional: Identity and recovery bootstrap contract
	identity?: #IdentityConfig

	// Optional: Observability configuration
	observability?: #ObservabilityConfig

	// Optional: Intents for smart defaults
	intents?: [...#Intent]

	// Optional: Custom metadata
	metadata?: #Metadata
}

// #DNSName enforces DNS-compatible naming (RFC 1123)
#DNSName: =~"^[a-z][a-z0-9-]{0,61}[a-z0-9]$"

// #StackKitRef is a reference to a StackKit template
// Directory names use -kit suffix: basement-kit, cloud-kit, or modern-homelab
// Empty string allowed for Easy-Path auto-selection
#StackKitRef: "" | "basement-kit" | "cloud-kit" | "modern-homelab"

// =============================================================================
// NODE DEFINITIONS
// =============================================================================

// #Node defines a compute node in the stack
#Node: {
	// Required: Unique node name within the stack
	name: #DNSName

	// Required: Node role - determines capabilities and defaults
	type: #NodeType

	// Required: Infrastructure provider
	provider: #Provider

	// Optional: SSH configuration (required for local/existing providers)
	ssh?: #SSHConfig

	// Optional: Resource constraints
	resources?: #ResourceSpec

	// Optional: Labels for service placement
	labels?: [string]: string

	// Optional: Taints for scheduling
	taints?: [...string]

	// Validation: edge nodes can only use local/docker providers
	if type == "edge" {
		provider: "local" | "docker"
	}
}

// #NodeType defines node roles
#NodeType: "main" | "worker" | "edge"

// #Provider defines supported infrastructure providers
// Keep in sync with: pkg/stackkits/base/stackkit.cue, pkg/validator/spec_validator.go
// Core providers: local, hetzner, docker, aws, gcp, azure
// Extended providers: proxmox, digitalocean, managed subscription leases.
#Provider: "local" | "hetzner" | "docker" | "aws" | "gcp" | "azure" | "proxmox" | "digitalocean" | "digitalocean-managed" | "centron" | "ionos"

// #SSHConfig defines SSH connection parameters
#SSHConfig: {
	host: #IPOrHostname
	port?: >0 & <=65535 | *22
	user?: =~"^[a-z_][a-z0-9_-]*$" | *"ubuntu"
	key_path?: string
}

// #IPOrHostname matches either an IP address or a hostname
#IPOrHostname: =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}$" | =~"^[a-zA-Z0-9][a-zA-Z0-9.-]*[a-zA-Z0-9]$"

// =============================================================================
// IDENTITY BOOTSTRAP
// =============================================================================

#IdentityConfig: {
	owner?: #OwnerBootstrap
	recovery?: #RecoveryBootstrap

	// Allow StackKit-specific identity module extensions.
	[string]: _
}

#OwnerBootstrap: {
	source: "local" | "cloud"
	email?: string
	username?: string
	displayName?: string

	[string]: _
}

#RecoveryBootstrap: {
	passphraseHashPresent?: bool | string
	passphraseHashRef?: =~"^secret://"
	passphrase_hash?: string

	[string]: _
}

// #ResourceSpec defines compute resource requirements
#ResourceSpec: {
	cpu?:    int & >=1 | *2
	memory?: =~"^[0-9]+(Mi|Gi)$" | *"2Gi"
	disk?:   =~"^[0-9]+(Gi|Ti)$" | *"20Gi"
}

// =============================================================================
// SERVICE DEFINITIONS
// =============================================================================

// #Service defines a service to be deployed
#Service: {
	// Required: Unique service name
	name: #DNSName

	// Required: Service type determines deployment behavior
	type: #ServiceType

	// Optional: Target node (defaults to first "main" node)
	node?: string

	// Optional: Enable/disable service
	enabled?: bool | *true

	// Optional: Dependencies on other services
	needs?: [...string]

	// Optional: Service-specific configuration
	config?: [string]: _

	// Optional: Port exposure
	ports?: [...#Port]

	// Optional: Network configuration
	network?: #ServiceNetworkConfig

	// Optional: Resource limits
	resources?: #ServiceResourceLimits

	// Optional: Volume mounts
	volumes?: [...#VolumeMount]

	// Optional: Environment variables
	environment?: [string]: string

	// Optional: Secret references
	environmentSecrets?: [string]: =~"^secret://"

	// Optional: Restart policy
	restartPolicy?: "always" | "unless-stopped" | "on-failure" | "no" | *"unless-stopped"
}

// #ServiceType defines supported service categories
// Keep in sync with: pkg/stackkits/base/stackkit.cue, pkg/validator/spec_validator.go
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

// #Port defines a port mapping
#Port: {
	container: >0 & <=65535
	host?:     >0 & <=65535
	protocol?: "tcp" | "udp" | *"tcp"
}

// #ServiceNetworkConfig defines service networking
#ServiceNetworkConfig: {
	proxy?: {
		enabled?: bool | *true
		domain?:  string
		tls?:     "auto" | "manual" | "disabled" | *"auto"
		middlewares?: [...string]
	}
	internal?: {
		expose?: bool | *false
		port?:   int
	}
}

// #ServiceResourceLimits defines resource constraints for a service
#ServiceResourceLimits: {
	cpuLimit?:     =~"^[0-9]+(\\.[0-9]+)?$" | *"1.0"
	memoryLimit?:  =~"^[0-9]+(Mi|Gi)$" | *"512Mi"
	cpuRequest?:   =~"^[0-9]+(\\.[0-9]+)?$"
	memoryRequest?: =~"^[0-9]+(Mi|Gi)$"
}

// #VolumeMount defines a volume attachment
#VolumeMount: {
	name:       string
	mountPath:  string
	hostPath?:  string
	readOnly?:  bool | *false
	type?:      "bind" | "volume" | "tmpfs" | *"volume"
	backup?:    bool | *true
}

// =============================================================================
// SYSTEM CONFIGURATION
// =============================================================================

// #SystemConfig defines host-level system settings
#SystemConfig: {
	timezone?: string | *"UTC"
	locale?:   string | *"en_US.UTF-8"
	swap?:     "disabled" | "auto" | =~"^[0-9]+(Mi|Gi)$" | *"disabled"
	unattendedUpgrades?: "disabled" | "security" | "all" | *"security"
	
	packages?: {
		base?: [...string]
		extra?: [...string]
	}
}

// =============================================================================
// SECURITY CONFIGURATION
// =============================================================================

// #SecurityConfig defines security settings
#SecurityConfig: {
	ssh?: #SSHSecurityConfig
	firewall?: #FirewallConfig
	tls?: #TLSConfig
	container?: #ContainerSecurityConfig
	secrets?: #SecretsConfig
}

// #SSHSecurityConfig defines SSH hardening
#SSHSecurityConfig: {
	port?:           >0 & <=65535 | *22
	passwordAuth?:   bool | *false
	permitRootLogin?: "yes" | "no" | "prohibit-password" | *"no"
	maxAuthTries?:   int & >=1 & <=10 | *3
	allowUsers?:     [...string]
}

// #FirewallConfig defines firewall rules
#FirewallConfig: {
	enabled?:        bool | *true
	backend?:        "ufw" | "iptables" | "nftables" | *"ufw"
	defaultInbound?: "allow" | "deny" | *"deny"
	defaultOutbound?: "allow" | "deny" | *"allow"
	rules?: [...#FirewallRule]
}

// #FirewallRule defines a single firewall rule
#FirewallRule: {
	port:     >0 & <=65535
	protocol?: "tcp" | "udp" | "both" | *"tcp"
	source?:  "any" | "lan" | "vpn" | =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/[0-9]{1,2}$" | *"any"
	comment?: string
}

// #TLSConfig defines TLS/SSL settings
#TLSConfig: {
	minVersion?:    "1.2" | "1.3" | *"1.2"
	certSource?:    "acme" | "manual" | "selfsigned" | *"acme"
	acmeEmail?:     string
	acmeProvider?:  "letsencrypt" | "zerossl" | *"letsencrypt"
}

// #ContainerSecurityConfig defines container security context
#ContainerSecurityConfig: {
	readOnlyRootfs?:  bool | *false
	noNewPrivileges?: bool | *true
	dropCapabilities?: [...string]
}

// #SecretsConfig defines secrets management
#SecretsConfig: {
	backend?:        "file" | "vault" | "env" | *"file"
	rotationPolicy?: "manual" | "scheduled" | *"manual"
}

// =============================================================================
// NETWORK CONFIGURATION
// =============================================================================

// #NetworkConfig defines network settings
#NetworkConfig: {
	vpn?:     "headscale" | "wireguard" | "tailscale" | "none" | *"none"
	domain?:  =~"^[a-z][a-z0-9.-]+$" | *"local"
	
	dns?: {
		servers?: [...string] | *["1.1.1.1", "8.8.8.8"]
		search?:  [...string]
	}
	
	internal?: {
		subnet?:  =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/[0-9]{1,2}$"
		gateway?: =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}$"
	}
	
	proxy?: {
		http?:    string
		https?:   string
		noProxy?: [...string]
	}
}

// =============================================================================
// OBSERVABILITY CONFIGURATION
// =============================================================================

// #ObservabilityConfig defines monitoring and logging
#ObservabilityConfig: {
	logging?: #LoggingConfig
	metrics?: #MetricsConfig
	health?:  #HealthConfig
	alerting?: #AlertingConfig
	backup?:  #BackupConfig
}

// #LoggingConfig defines logging settings
#LoggingConfig: {
	driver?:    "journald" | "json-file" | "loki" | *"journald"
	retention?: =~"^[0-9]+[hdwm]$" | *"7d"
	level?:     "debug" | "info" | "warn" | "error" | *"info"
}

// #MetricsConfig defines metrics collection
#MetricsConfig: {
	enabled?: bool | *false
	nodeExporter?: {
		enabled?: bool | *true
	}
	cadvisor?: {
		enabled?: bool | *false
	}
}

// #HealthConfig defines health check settings
#HealthConfig: {
	enabled?:  bool | *true
	interval?: =~"^[0-9]+[smh]$" | *"30s"
	timeout?:  =~"^[0-9]+[smh]$" | *"10s"
}

// #AlertingConfig defines alerting channels
#AlertingConfig: {
	enabled?:  bool | *false
	channels?: [...("webhook" | "email" | "slack" | "discord")]
}

// #BackupConfig defines backup settings
#BackupConfig: {
	enabled?:   bool | *false
	schedule?:  =~"^[0-9*,-/]+ [0-9*,-/]+ [0-9*,-/]+ [0-9*,-/]+ [0-9*,-/]+$" | *"0 3 * * *"
	retention?: =~"^[0-9]+[hdwm]$" | *"30d"
	
	target?: {
		type?: "local" | "s3" | "restic" | "rclone" | *"local"
		path?: string | *"/var/backups/techstack"
		// S3 specific
		bucket?: string
		region?: string
		endpoint?: string
	}
	
	include?: [...("volumes" | "configs" | "databases")] | *["volumes", "configs"]
	exclude?: [...string]
}

// =============================================================================
// INTENTS (Smart Defaults)
// =============================================================================

// #Intent can be a simple string or a detailed object
#Intent: #SimpleIntent | #DetailedIntent

// #SimpleIntent is just a string reference
#SimpleIntent: "self-hosted" | "privacy-focused" | "low-maintenance" | "high-availability" | "development" | "media-server" | "smart-home" | "gaming"

// #DetailedIntent is a full intent specification
#DetailedIntent: {
	intent:   #SimpleIntent
	priority?: "low" | "medium" | "high" | *"medium"
	constraints?: [string]: _
	features?: [...string]
	integrations?: [...string]
}

// =============================================================================
// METADATA
// =============================================================================

// #Metadata defines custom metadata
#Metadata: {
	owner?:       string
	environment?: "production" | "staging" | "development" | *"production"
	project?:     string
	created_by?:  "wizard" | "import" | "manual" | *"manual"
	tags?:        [...string]
	
	// Allow arbitrary additional fields
	[string]: _
}

// =============================================================================
// VALIDATION RESULTS
// =============================================================================

// #ValidationResult is the output of the validation step
#ValidationResult: {
	valid:   bool
	errors?: [...#ValidationError]
	warnings?: [...#ValidationWarning]
	
	// Timestamp of validation
	timestamp: time.Format(time.RFC3339)
	
	// Resolved StackKit (for auto-selection)
	resolvedKit?: #StackKitRef
}

// #ValidationError represents a validation error
#ValidationError: {
	path:    string  // JSON path to the invalid field
	code:    string  // Error code for programmatic handling
	message: string  // Human-readable description
}

// #ValidationWarning represents a non-fatal issue
#ValidationWarning: {
	path:    string
	code:    string
	message: string
}
