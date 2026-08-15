// Package unifier provides the Unifier Engine implementation.
// This file contains the Unify method and related helper functions.
package unifier

import (
	"fmt"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/placement"
	"github.com/kombifyio/techstack/pkg/validator"
)

// Unify merges user spec with system policies and resolves all computed values.
// Returns a fully resolved UnifiedSpec ready for OpenTofu generation.
// This is Phase 2 of the Two-Phase Unifier: determine WHERE to place services.
func (e *Engine) Unify(spec *core.KombinationSpec, workers []core.Worker) (*core.UnifiedSpec, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// First validate
	result, err := e.Validate(spec)
	if err != nil {
		return nil, err
	}
	if !result.Valid {
		firstMsg := ""
		if len(result.Errors) > 0 {
			firstMsg = result.Errors[0].Message
		}
		if firstMsg != "" {
			return nil, fmt.Errorf("validation failed: %d errors (first: %s)", len(result.Errors), firstMsg)
		}
		return nil, fmt.Errorf("validation failed")
	}

	// Build unified spec with resolved values
	unified := &core.UnifiedSpec{
		KombinationSpec: *spec,
		StackKit:        spec.Kit,
	}
	selectedStackKit := spec.Kit
	if selectedStackKit == "" {
		selectedStackKit = e.selectStackKit(spec)
	}
	decisionContext := BuildDecisionContext(spec, nil)
	decisionTrace := BuildDecisionTrace(spec, decisionContext, &ResolveResult{
		StackKit:     selectedStackKit,
		Reason:       "unifier-engine direct resolution",
		AutoSelected: spec.Kit == "",
		Confidence:   0.75,
		Valid:        true,
	}, nil)
	ApplyUnifiedDecisionArtifacts(unified, decisionContext, decisionTrace)
	if unified.StackKit == "" {
		unified.StackKit = decisionTrace.SelectedStackKit
	}

	// Resolve nodes with computed values
	for _, node := range spec.Nodes {
		resolved := core.ResolvedNode{
			NodeSpec: node,
		}
		// Apply provider-specific defaults
		resolved = e.applyNodeDefaults(resolved)
		unified.ResolvedNodes = append(unified.ResolvedNodes, resolved)
	}

	// Resolve services with computed values
	for _, svc := range spec.Services {
		resolved := core.ResolvedService{
			ServiceSpec: svc,
			Status:      "pending",
		}
		// Apply service-specific defaults
		resolved = e.applyServiceDefaults(resolved, spec.Nodes)
		unified.ResolvedServices = append(unified.ResolvedServices, resolved)
	}

	// Resolve network configuration
	unified.ResolvedNetwork = e.applyNetworkDefaults(spec.Network)

	// Phase 2: Place services on workers
	if len(workers) > 0 {
		// Use RequirementsSpec from Analyze() if available
		requirements, err := e.Analyze(spec)
		if err != nil {
			return nil, fmt.Errorf("analyze failed before placement: %w", err)
		}

		// The StackKit rollout installs the container runtime as part of the
		// kit, so Docker is an outcome of this deployment rather than a
		// precondition for planning it. Without this, a freshly provisioned
		// managed VM can never be planned: every service defaults to requiring
		// Docker, and Docker only appears once the rollout has run.
		placementEngine := placement.NewPlacementEngine().WithProvisionedContainerRuntime()

		// Match services to workers
		placements, quality, err := placementEngine.PlaceServices(requirements, workers, spec.Services)
		if err != nil {
			return nil, fmt.Errorf("service placement failed: %w", err)
		}

		unified.Placements = placements
		unified.PlacementQuality = quality

		// Log placement results
		for _, p := range placements {
			if len(p.Warnings) > 0 {
				e.logger.Warn("placement warning",
					"service", p.Service.Name,
					"worker", p.WorkerName,
					"warnings", p.Warnings)
			}
		}

		e.logger.Info("placement complete",
			"services", len(placements),
			"workers", len(workers),
			"quality", quality)
	} else {
		e.logger.Warn("no workers available for placement - services will use node affinity only")
	}

	return unified, nil
}

// applyNodeDefaults applies provider-specific defaults to a resolved node.
func (e *Engine) applyNodeDefaults(node core.ResolvedNode) core.ResolvedNode {
	// Apply SSH defaults based on provider
	if node.SSH != nil && node.SSH.Port == 0 {
		node.SSH.Port = 22
	}
	if node.SSH != nil && node.SSH.User == "" {
		switch node.Provider {
		case "hetzner":
			node.SSH.User = "root"
		case "aws":
			node.SSH.User = "ec2-user"
		case "gcp":
			node.SSH.User = "admin"
		case "azure":
			node.SSH.User = "azureuser"
		default:
			node.SSH.User = "ubuntu"
		}
	}
	return node
}

// applyServiceDefaults applies service-specific defaults.
// Resolves default Docker images and ports based on service type.
func (e *Engine) applyServiceDefaults(svc core.ResolvedService, nodes []core.NodeSpec) core.ResolvedService {
	// Default to main node if not specified
	if svc.Node == "" {
		for _, n := range nodes {
			if n.Type == "main" {
				svc.Node = n.Name
				break
			}
		}
		// Fallback to first node if no main node
		if svc.Node == "" && len(nodes) > 0 {
			svc.Node = nodes[0].Name
		}
	}

	// Resolve default Docker image based on service type
	if svc.Image == "" && svc.Type != "" {
		if defaultImage := validator.GetDefaultImage(svc.Type); defaultImage != "" {
			svc.Image = defaultImage
			e.logger.Debug("applied default image",
				"service", svc.Name,
				"type", svc.Type,
				"image", svc.Image)
		}
	}

	// Resolve default port based on service type
	if svc.Port == 0 && svc.Type != "" {
		if defaultPort := validator.GetDefaultPort(svc.Type); defaultPort != 0 {
			svc.Port = defaultPort
			e.logger.Debug("applied default port",
				"service", svc.Name,
				"type", svc.Type,
				"port", svc.Port)
		}
	}

	return svc
}

// applyNetworkDefaults applies network configuration defaults.
// Calculates Subnet, Gateway, and Nameservers based on VPN type.
//
// Subnet ranges by VPN type (designed to avoid conflicts):
//   - headscale/tailscale: 100.64.0.0/10 (CGNAT range, ~4M addresses)
//   - wireguard: 10.200.0.0/16 (private range, ~65K addresses)
//   - zerotier: 10.147.0.0/16 (private range, ~65K addresses)
//   - none/default: 10.0.0.0/24 (local only, 254 addresses)
func (e *Engine) applyNetworkDefaults(network core.NetworkSpec) core.ResolvedNetwork {
	resolved := core.ResolvedNetwork{
		NetworkSpec: network,
	}

	// Apply VPN defaults and derive network topology
	if network.VPN == "" {
		resolved.VPNType = "none"
	} else {
		resolved.VPNType = network.VPN
	}

	// Calculate Subnet and Gateway based on VPN type
	// Each VPN type has its own address space to avoid conflicts
	switch resolved.VPNType {
	case "headscale":
		// Headscale uses CGNAT range (100.64.0.0/10) by default
		// This is the same range Tailscale uses - standard for mesh VPNs
		resolved.Subnet = "100.64.0.0/10"
		resolved.Gateway = "100.64.0.1"
	case "tailscale":
		// Tailscale uses CGNAT range (100.64.0.0/10) like Headscale
		// Both derive from the Tailscale protocol
		resolved.Subnet = "100.64.0.0/10"
		resolved.Gateway = "100.64.0.1"
	case "wireguard":
		// WireGuard typical private range - larger than /24 for homelab scalability
		resolved.Subnet = "10.200.0.0/16"
		resolved.Gateway = "10.200.0.1"
	case "zerotier":
		// ZeroTier uses 10.x range - separate from wireguard to allow coexistence
		resolved.Subnet = "10.147.0.0/16"
		resolved.Gateway = "10.147.0.1"
	case "none":
		// Local-only deployment uses standard private range
		resolved.Subnet = "10.0.0.0/24"
		resolved.Gateway = "10.0.0.1"
	default:
		// Unknown VPN type - log warning and use safe defaults
		e.logger.Warn("unknown VPN type, using defaults",
			"vpn_type", resolved.VPNType,
			"default_subnet", "10.0.0.0/24")
		resolved.Subnet = "10.0.0.0/24"
		resolved.Gateway = "10.0.0.1"
	}

	// Preserve explicit domains only. For local StackKits, an omitted domain is
	// meaningful: StackKits applies its own browser-native local default.
	if network.Domain != "" {
		resolved.Domain = network.Domain
	}

	// Set default nameservers if user hasn't specified any
	// Check the input NetworkSpec, not the resolved struct
	// Note: NetworkSpec may need a Nameservers field added if user-override is desired
	if len(resolved.Nameservers) == 0 {
		// Use privacy-respecting public DNS
		// For VPN types with MagicDNS, the VPN client handles DNS internally
		resolved.Nameservers = []string{
			"1.1.1.1", // Cloudflare (fast, privacy-focused)
			"1.0.0.1", // Cloudflare secondary
			"9.9.9.9", // Quad9 (security-focused fallback)
		}
	}

	return resolved
}
