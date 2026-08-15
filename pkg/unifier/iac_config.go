package unifier

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/core"
)

// unifiedToIaCConfig converts a UnifiedSpec to IaCConfig.
// TD-10-3: Defaults are extracted from the spec where available,
// falling back to sensible defaults only when not specified.
func (g *IaCGenerator) unifiedToIaCConfig(unified *core.UnifiedSpec) (*IaCConfig, error) {
	sshUser, sshPort, sshKeyPath := g.extractSSHDefaults(unified)
	networkMode := g.deriveNetworkMode(unified)
	tlsMode := g.deriveTLSMode(unified)

	config := &IaCConfig{
		StackName:              unified.Name,
		StackKit:               unified.StackKit,
		Mode:                   g.Mode,
		Variant:                g.getMetadataString(unified.Metadata, "variant", iacDefaultVariant),
		ComputeTier:            g.getMetadataString(unified.Metadata, "compute_tier", iacDefaultComputeTier),
		NetworkMode:            networkMode,
		TLSMode:                tlsMode,
		SubdomainPrefix:        g.getMetadataString(unified.Metadata, "subdomain_prefix", g.getMetadataString(unified.Metadata, "subdomainPrefix", "")),
		SSHPort:                sshPort,
		SSHUser:                sshUser,
		SSHKeyPath:             sshKeyPath,
		DockerHost:             g.getMetadataString(unified.Metadata, "docker_host", g.getMetadataString(unified.Metadata, "dockerHost", "")),
		EnablePlatformFallback: g.getMetadataBool(unified.Metadata, "enable_platform_fallback", false),
		PlatformFallbackMode:   g.getMetadataString(unified.Metadata, "platform_fallback_mode", ""),
		EnableFirewall:         g.getMetadataBool(unified.Metadata, "enable_firewall", true),
		AllowedPorts:           g.deriveAllowedPorts(unified),
	}
	config.BaseKitServiceEnablement = g.baseKitServiceEnablementFromMetadata(unified.Metadata)
	config.AdminEmail = firstNonEmptyString(
		g.getMetadataString(unified.Metadata, "admin_email", ""),
		g.getMetadataString(unified.Metadata, "adminEmail", ""),
		g.getMetadataString(unified.Metadata, "mail_address", ""),
		g.getMetadataString(unified.Metadata, "deployment_email", ""),
	)
	config.AdminPasswordPlaintext = g.getMetadataString(unified.Metadata, "admin_password_plaintext", "")
	config.TinyAuthUsers = g.getMetadataString(unified.Metadata, "tinyauth_users", "")
	config.PocketIDStaticAPIKey = g.getMetadataString(unified.Metadata, "pocketid_static_api_key", "")
	config.PocketIDEncryptionKey = g.getMetadataString(unified.Metadata, "pocketid_encryption_key", "")

	if len(unified.ResolvedNodes) > 0 {
		node := unified.ResolvedNodes[0]
		if node.PrivateIP != "" {
			config.WorkerIP = node.PrivateIP
		} else if node.PublicIP != "" {
			config.WorkerIP = node.PublicIP
		}
		// Core ResolvedNode currently exposes OS as a coarse string (e.g., "linux").
		// IaCConfig expects a distro identifier (e.g., "ubuntu-24"), but we keep
		// this best-effort mapping until the Unifier provides richer OS metadata.
		if node.OS != "" {
			config.OSType = node.OS
		}
	}

	if unified.ResolvedNetwork.Domain != "" {
		config.Domain = unified.ResolvedNetwork.Domain
	}
	if unified.ResolvedNetwork.VPNType != "" {
		config.VPNType = unified.ResolvedNetwork.VPNType
	}
	if unified.ResolvedNetwork.Subnet != "" {
		config.Subnet = unified.ResolvedNetwork.Subnet
	}

	for _, svc := range unified.ResolvedServices {
		iacSvc := IaCService{
			Name:        svc.Name,
			Image:       svc.Image,
			Port:        svc.Port,
			Environment: svc.Environment,
			DependsOn:   svc.Needs,
			Restart:     "unless-stopped",
			Public:      false,
		}
		config.Services = append(config.Services, iacSvc)
	}

	return config, nil
}

// extractSSHDefaults extracts SSH configuration from the first available node.
// Returns user, port, and keyPath with sensible defaults if not specified.
func (g *IaCGenerator) extractSSHDefaults(unified *core.UnifiedSpec) (user string, port int, keyPath string) {
	user = iacDefaultSSHUser
	port = 22
	keyPath = ""

	// Try to get SSH config from resolved nodes first (has computed values)
	if len(unified.ResolvedNodes) > 0 {
		node := unified.ResolvedNodes[0]
		if node.SSH != nil {
			if node.SSH.User != "" {
				user = node.SSH.User
			}
			if node.SSH.Port > 0 {
				port = node.SSH.Port
			}
			if node.SSH.KeyPath != "" {
				keyPath = node.SSH.KeyPath
			}
			return
		}
	}

	// Fall back to raw node spec if resolved nodes don't have SSH config
	if len(unified.Nodes) > 0 {
		nodeSpec := unified.Nodes[0]
		if nodeSpec.SSH != nil {
			if nodeSpec.SSH.User != "" {
				user = nodeSpec.SSH.User
			}
			if nodeSpec.SSH.Port > 0 {
				port = nodeSpec.SSH.Port
			}
			if nodeSpec.SSH.KeyPath != "" {
				keyPath = nodeSpec.SSH.KeyPath
			}
		}
	}

	return
}

// deriveNetworkMode determines the network mode based on spec configuration.
// Priority: metadata > resolved network > node provider inference > default
func (g *IaCGenerator) deriveNetworkMode(unified *core.UnifiedSpec) string {
	// Check metadata first for explicit override
	if mode, ok := unified.Metadata["network_mode"]; ok && mode != "" {
		return mode
	}

	// If domain is set, likely public or hybrid
	if unified.ResolvedNetwork.Domain != "" {
		// Check if any node has public IP
		hasPublicIP := false
		for _, node := range unified.ResolvedNodes {
			if node.PublicIP != "" {
				hasPublicIP = true
				break
			}
		}
		if hasPublicIP {
			return iacNetworkModePublic
		}
		return iacNetworkModeHybrid
	}

	// Check VPN configuration
	if unified.ResolvedNetwork.VPNType != "" {
		return iacNetworkModeHybrid
	}

	// Infer from node providers
	for _, node := range unified.Nodes {
		switch node.Provider {
		case providerHetzner, providerAWS, providerGCP, providerAzure, providerDigitalOcean, providerDigitalOceanManaged, providerCentron, providerIONOS:
			return iacNetworkModePublic
		}
	}

	return providerLocal
}

// deriveTLSMode determines TLS mode based on network configuration.
// Priority: metadata > domain presence > default
func (g *IaCGenerator) deriveTLSMode(unified *core.UnifiedSpec) string {
	// Check metadata first for explicit override
	if mode, ok := unified.Metadata["tls_mode"]; ok && mode != "" {
		return mode
	}

	// If domain is set, use ACME for real certificates
	if unified.ResolvedNetwork.Domain != "" {
		return iacTLSModeACME
	}

	// Default to self-signed for local/dev environments
	return iacTLSModeSelfSigned
}

// deriveAllowedPorts determines the allowed firewall ports.
// Priority: metadata > service ports + defaults
func (g *IaCGenerator) deriveAllowedPorts(unified *core.UnifiedSpec) []int {
	// Check metadata for explicit port list (comma-separated string)
	if portsStr, ok := unified.Metadata["allowed_ports"]; ok && portsStr != "" {
		ports := g.parsePortList(portsStr)
		if len(ports) > 0 {
			return ports
		}
	}

	// Start with essential defaults
	portSet := map[int]bool{
		22:  true, // SSH
		80:  true, // HTTP
		443: true, // HTTPS
	}

	// Add ports from resolved services
	for _, svc := range unified.ResolvedServices {
		if svc.Port > 0 {
			portSet[svc.Port] = true
		}
	}

	// Convert to sorted slice
	ports := make([]int, 0, len(portSet))
	for p := range portSet {
		ports = append(ports, p)
	}

	// Sort ports for consistent output
	for i := 0; i < len(ports)-1; i++ {
		for j := i + 1; j < len(ports); j++ {
			if ports[i] > ports[j] {
				ports[i], ports[j] = ports[j], ports[i]
			}
		}
	}

	return ports
}

// parsePortList parses a comma-separated port list string.
func (g *IaCGenerator) parsePortList(s string) []int {
	var ports []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var port int
		if _, err := fmt.Sscanf(part, "%d", &port); err == nil && port > 0 && port <= 65535 {
			ports = append(ports, port)
		}
	}
	return ports
}

// getMetadataString retrieves a string value from metadata with a default fallback.
func (g *IaCGenerator) getMetadataString(metadata map[string]string, key, defaultVal string) string {
	if metadata == nil {
		return defaultVal
	}
	if val, ok := metadata[key]; ok && val != "" {
		return val
	}
	return defaultVal
}

// getMetadataBool retrieves a boolean value from metadata with a default fallback.
func (g *IaCGenerator) getMetadataBool(metadata map[string]string, key string, defaultVal bool) bool {
	if metadata == nil {
		return defaultVal
	}
	if val, ok := metadata[key]; ok {
		switch strings.ToLower(val) {
		case "true", "yes", "1", "enabled", "on":
			return true
		case "false", "no", "0", "disabled", "off":
			return false
		}
	}
	return defaultVal
}

func (g *IaCGenerator) baseKitServiceEnablementFromMetadata(metadata map[string]string) map[string]bool {
	if metadata == nil {
		return nil
	}
	out := map[string]bool{}
	for _, key := range baseKitServiceEnableVars {
		if _, ok := metadata[key]; !ok {
			continue
		}
		out[key] = g.getMetadataBool(metadata, key, false)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ConfigFromUnifiedSpecMultiNode creates an IaCConfig from UnifiedSpec with multi-node support.
func (g *IaCGenerator) ConfigFromUnifiedSpecMultiNode(unified *core.UnifiedSpec) (*IaCConfig, error) {
	config, err := g.unifiedToIaCConfig(unified)
	if err != nil {
		return nil, err
	}

	for _, node := range unified.ResolvedNodes {
		config.Nodes = append(config.Nodes, resolvedNodeToIaCNode(node))
	}

	if len(config.Nodes) == 1 {
		config.Nodes[0].Role = iacNodeRoleStandalone
	}
	if len(config.Nodes) > 1 {
		configureAutoSwarm(config)
	}

	return config, nil
}

func resolvedNodeToIaCNode(node core.ResolvedNode) IaCNode {
	ip := node.PrivateIP
	if ip == "" {
		ip = node.PublicIP
	}
	role := iacNodeRoleWorker
	if nodeRole, ok := node.Tags["role"]; ok {
		role = nodeRole
	}

	iacNode := IaCNode{
		Name:      node.Name,
		IP:        ip,
		PrivateIP: node.PrivateIP,
		PublicIP:  node.PublicIP,
		OSType:    node.OS,
		Role:      role,
		Labels:    node.Tags,
	}

	if node.SSH != nil {
		iacNode.SSHUser = node.SSH.User
		iacNode.SSHPort = node.SSH.Port
		iacNode.SSHKeyPath = node.SSH.KeyPath
	}

	return iacNode
}

func configureAutoSwarm(config *IaCConfig) {
	managers := 0
	for _, node := range config.Nodes {
		if node.Role == iacNodeRoleManager {
			managers++
		}
	}
	if managers == 0 {
		config.Nodes[0].Role = iacNodeRoleManager
		managers = 1
	}
	mode := iacSwarmModeSingleManager
	if managers >= 3 {
		mode = "ha"
	}
	config.SwarmConfig = &SwarmConfig{
		Enabled:        true,
		Mode:           mode,
		ManagerCount:   managers,
		WorkerCount:    len(config.Nodes) - managers,
		EncryptOverlay: true,
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
