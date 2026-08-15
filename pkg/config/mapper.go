// Package config provides user configuration mapping to OpenTofu variables.
package config

import (
	"fmt"

	"github.com/kombifyio/techstack/pkg/core"
)

// UserConfig represents the high-level user intent configuration.
type UserConfig struct {
	Stack    StackIntent    `yaml:"stack" json:"stack"`
	Server   ServerIntent   `yaml:"server" json:"server"`
	Services ServicesIntent `yaml:"services" json:"services"`
	Admin    AdminIntent    `yaml:"admin" json:"admin"`
	Network  NetworkIntent  `yaml:"network" json:"network"`
}

// StackIntent defines the stack-level configuration.
type StackIntent struct {
	Name    string `yaml:"name" json:"name"`
	Pattern string `yaml:"pattern" json:"pattern"` // single-node, multi-node-flat, multi-node-ha
}

// ServerIntent defines server provisioning preferences.
type ServerIntent struct {
	Provider string `yaml:"provider" json:"provider"` // hetzner, local, aws, etc.
	Region   string `yaml:"region" json:"region"`
	Size     string `yaml:"size" json:"size"` // small, medium, large or provider-specific
}

// ServicesIntent defines which services to enable.
type ServicesIntent struct {
	VPN     string `yaml:"vpn" json:"vpn"`         // headscale, tailscale, wireguard
	Backend string `yaml:"backend" json:"backend"` // StackKit backend profile, e.g. pocketbase; empty disables
	PaaS    string `yaml:"paas" json:"paas"`       // caprover, dokku, portainer
	Proxy   string `yaml:"proxy" json:"proxy"`     // traefik, nginx-proxy-manager
}

// AdminIntent defines admin user configuration.
type AdminIntent struct {
	Username   string `yaml:"username" json:"username"`
	SSHKeyPath string `yaml:"ssh_key_path" json:"ssh_key_path"`
	Email      string `yaml:"email" json:"email"`
}

// NetworkIntent defines networking preferences.
type NetworkIntent struct {
	Domain           string `yaml:"domain" json:"domain"`
	EnablePublicWeb  bool   `yaml:"enable_public_web" json:"enable_public_web"`
	CloudflareTunnel bool   `yaml:"cloudflare_tunnel" json:"cloudflare_tunnel"`
}

// Mapper converts UserConfig to core.KombinationSpec.
type Mapper struct {
	// blueprintMappings maps patterns to blueprint names
	blueprintMappings map[string]string

	// serverSizeMappings maps size names to provider-specific types
	serverSizeMappings map[string]map[string]string
}

// NewMapper creates a new configuration mapper.
func NewMapper() *Mapper {
	return &Mapper{
		blueprintMappings: map[string]string{
			"single-node":     "single-server-complete",
			"multi-node-flat": "multi-node-flat",
			"multi-node-ha":   "multi-node-ha",
		},
		serverSizeMappings: map[string]map[string]string{
			"hetzner": {
				"small":  "cx22", // 2 vCPU, 4GB RAM
				"medium": "cx32", // 4 vCPU, 8GB RAM
				"large":  "cx42", // 8 vCPU, 16GB RAM
			},
		},
	}
}

// MapToKombinationSpec converts a UserConfig to a core.KombinationSpec.
func (m *Mapper) MapToKombinationSpec(userConfig *UserConfig) (*core.KombinationSpec, error) {
	if err := m.validate(userConfig); err != nil {
		return nil, fmt.Errorf("invalid user config: %w", err)
	}

	spec := &core.KombinationSpec{
		Name:     userConfig.Stack.Name,
		Metadata: make(map[string]string),
	}

	// Map server/node configuration
	node := m.mapNode(userConfig)
	spec.Nodes = []core.NodeSpec{node}

	// Map services
	spec.Services = m.mapServices(userConfig)

	// Map network
	spec.Network = m.mapNetwork(userConfig)

	// Add metadata
	spec.Metadata["pattern"] = userConfig.Stack.Pattern
	spec.Metadata["admin_username"] = userConfig.Admin.Username
	spec.Metadata["admin_email"] = userConfig.Admin.Email

	return spec, nil
}

// mapNode maps server intent to node specification.
func (m *Mapper) mapNode(userConfig *UserConfig) core.NodeSpec {
	provider := userConfig.Server.Provider

	// Map size to provider-specific type
	serverType := userConfig.Server.Size
	if sizeMap, ok := m.serverSizeMappings[provider]; ok {
		if mapped, ok := sizeMap[userConfig.Server.Size]; ok {
			serverType = mapped
		}
	}

	// Determine image based on provider
	image := "ubuntu-24.04"
	if provider == "hetzner" {
		image = "ubuntu-24.04"
	}

	// Build SSH config if path is provided
	var ssh *core.SSHConfig
	if userConfig.Admin.SSHKeyPath != "" {
		ssh = &core.SSHConfig{
			KeyPath: userConfig.Admin.SSHKeyPath,
		}
	}

	return core.NodeSpec{
		Name:     userConfig.Stack.Name + "-main",
		Type:     "main",
		Provider: provider,
		SSH:      ssh,
		Tags: map[string]string{
			"stack":       userConfig.Stack.Name,
			"pattern":     userConfig.Stack.Pattern,
			"role":        "main",
			"server_type": serverType,
			"location":    userConfig.Server.Region,
			"image":       image,
		},
	}
}

// mapServices maps service intents to service specifications.
func (m *Mapper) mapServices(userConfig *UserConfig) []core.ServiceSpec {
	services := []core.ServiceSpec{}

	// VPN service
	if userConfig.Services.VPN != "" {
		services = append(services, core.ServiceSpec{
			Name: "headscale",
			Type: userConfig.Services.VPN,
			Node: userConfig.Stack.Name + "-main",
		})
	}

	// Backend service
	if userConfig.Services.Backend != "" {
		services = append(services, core.ServiceSpec{
			Name: userConfig.Services.Backend,
			Type: userConfig.Services.Backend,
			Node: userConfig.Stack.Name + "-main",
		})
	}

	// PaaS service
	if userConfig.Services.PaaS != "" {
		services = append(services, core.ServiceSpec{
			Name: userConfig.Services.PaaS,
			Type: userConfig.Services.PaaS,
			Node: userConfig.Stack.Name + "-main",
		})
	}

	return services
}

// mapNetwork maps network intent to network specification.
func (m *Mapper) mapNetwork(userConfig *UserConfig) core.NetworkSpec {
	vpn := userConfig.Services.VPN
	if vpn == "" {
		vpn = "none"
	}

	return core.NetworkSpec{
		VPN:    vpn,
		Domain: userConfig.Network.Domain,
	}
}

// validate checks if the user configuration is valid.
func (m *Mapper) validate(userConfig *UserConfig) error {
	if userConfig.Stack.Name == "" {
		return fmt.Errorf("stack name is required")
	}

	if userConfig.Stack.Pattern == "" {
		return fmt.Errorf("stack pattern is required")
	}

	if userConfig.Server.Provider == "" {
		return fmt.Errorf("server provider is required")
	}

	if userConfig.Admin.Username == "" {
		return fmt.Errorf("admin username is required")
	}

	return nil
}
