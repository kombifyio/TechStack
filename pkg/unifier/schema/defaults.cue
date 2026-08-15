// Package schema - Smart defaults for kombifyTechstack configurations
package schema

// #Provider defines supported infrastructure providers (duplicate for standalone loading)
// Keep in sync with: kombination.cue, pkg/stackkits/base/stackkit.cue, pkg/validator/spec_validator.go
#Provider: "local" | "hetzner" | "docker" | "aws" | "gcp" | "azure" | "proxmox" | "digitalocean" | "digitalocean-managed" | "centron" | "ionos"

// #ServiceType defines supported service categories (duplicate for standalone loading)
// Keep in sync with: kombination.cue, pkg/stackkits/base/stackkit.cue
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

// #SmartDefaults provides computed default values based on context
#SmartDefaults: {
	// Input: the node type
	nodeType: "main" | "worker" | "edge"

	// Output: recommended resource defaults
	resources: {
		if nodeType == "main" {
			cpu:    4
			memory: "4Gi"
			disk:   "50Gi"
		}
		if nodeType == "worker" {
			cpu:    2
			memory: "2Gi"
			disk:   "20Gi"
		}
		if nodeType == "edge" {
			cpu:    1
			memory: "1Gi"
			disk:   "10Gi"
		}
	}
}

// #ProviderDefaults provides provider-specific defaults
#ProviderDefaults: {
	provider: "local" | "hetzner" | "docker" | "aws" | "gcp" | "azure" | "proxmox" | "digitalocean" | "digitalocean-managed" | "centron" | "ionos"

	// SSH defaults vary by provider
	sshDefaults: {
		if provider == "hetzner" {
			user: "root"
			port: 22
		}
		if provider == "local" {
			user: "ubuntu"
			port: 22
		}
		if provider == "aws" {
			user: "ec2-user"
			port: 22
		}
		if provider == "gcp" {
			user: "admin"
			port: 22
		}
		if provider == "azure" {
			user: "azureuser"
			port: 22
		}
		if provider == "proxmox" {
			user: "root"
			port: 22
		}
		if provider == "digitalocean" {
			user: "root"
			port: 22
		}
		if provider == "docker" {
			// Docker doesn't use SSH
		}
	}
}

// #ServiceDefaults provides service-specific defaults
#ServiceDefaults: {
	serviceType: "reverse-proxy" | "database" | "docker" | "monitoring" | "backup" | "auth" | "storage" | "custom"

	// Default ports by service type
	ports: {
		if serviceType == "reverse-proxy" {
			[{container: 80, host: 80}, {container: 443, host: 443}]
		}
		if serviceType == "database" {
			[{container: 5432}]  // PostgreSQL default
		}
		if serviceType == "monitoring" {
			[{container: 3000}]  // Grafana default
		}
	}

	// Default dependencies
	needs: {
		if serviceType == "reverse-proxy" {
			["docker"]
		}
		if serviceType == "database" {
			["docker"]
		}
		if serviceType == "monitoring" {
			["docker", "database"]
		}
	}
}
