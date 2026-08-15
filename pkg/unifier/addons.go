// Package unifier provides the Add-On Detector for automatic add-on activation.
package unifier

import (
	"fmt"
	"sort"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/validator"
)

// AddonDetector handles automatic add-on detection based on spec characteristics.
// This is Step 2b of the Unifier Pipeline (after StackKit Resolution).
//
// Pipeline: Pre-Validation → StackKit Resolution → Add-On Detection → Unifier Engine
type AddonDetector struct {
	registry map[string]*AddonDefinition
	order    []string // Ordered addon names for deterministic iteration
}

// AddonCondition is a function that checks if an add-on should be activated.
type AddonCondition func(spec *core.KombinationSpec) bool

// AddonDefinition contains the full definition of an add-on.
type AddonDefinition struct {
	Name        string
	DisplayName string
	Description string
	CUEFile     string
	Priority    int // Higher priority addons are listed first
	Condition   AddonCondition
}

// AddonInfo contains metadata about a detected add-on.
type AddonInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Reason      string `json:"reason"`  // Why this add-on was activated
	CUEFile     string `json:"cueFile"` // Path to the add-on CUE file
	Priority    int    `json:"priority"`
}

// DetectionResult contains all detected add-ons for a spec.
type DetectionResult struct {
	Addons       []AddonInfo `json:"addons"`
	SkippedCount int         `json:"skippedCount"` // Number of add-ons that didn't match
	TotalChecked int         `json:"totalChecked"` // Total add-ons checked
}

// NewAddonDetector creates a new add-on detector with the default registry.
func NewAddonDetector() *AddonDetector {
	d := &AddonDetector{
		registry: make(map[string]*AddonDefinition),
		order:    []string{},
	}

	// Register default add-ons
	d.registerDefaultAddons()

	return d
}

// registerDefaultAddons registers all built-in add-on conditions.
func (d *AddonDetector) registerDefaultAddons() {
	// cloud-integration: Activated when any cloud provider is used
	d.RegisterAddonFull(&AddonDefinition{
		Name:        "cloud-integration",
		DisplayName: "Cloud Integration",
		Description: "VPN overlay network, cloud security policies, and external DNS",
		CUEFile:     "pkg/stackkits/addons/cloud-integration.cue",
		Priority:    100,
		Condition: func(spec *core.KombinationSpec) bool {
			for _, node := range spec.Nodes {
				if isCloudProvider(node.Provider) {
					return true
				}
			}
			return false
		},
	})

	// arm-support: Activated when ARM architecture nodes are present
	d.RegisterAddonFull(&AddonDefinition{
		Name:        "arm-support",
		DisplayName: "ARM Support",
		Description: "ARM64-compatible images and ARM-specific resource limits",
		CUEFile:     "pkg/stackkits/addons/arm-support.cue",
		Priority:    90,
		Condition: func(spec *core.KombinationSpec) bool {
			for _, node := range spec.Nodes {
				if hasARMIndicator(node) {
					return true
				}
			}
			return false
		},
	})

	// low-memory: Activated when nodes have < 4GB RAM
	d.RegisterAddonFull(&AddonDefinition{
		Name:        "low-memory",
		DisplayName: "Low Memory Mode",
		Description: "Excludes heavy services, optimized resource limits",
		CUEFile:     "pkg/stackkits/addons/low-memory.cue",
		Priority:    80,
		Condition: func(spec *core.KombinationSpec) bool {
			for _, node := range spec.Nodes {
				if hasLowMemoryIndicator(node) {
					return true
				}
			}
			return false
		},
	})

	// high-availability: Activated when 3+ nodes with HA config
	d.RegisterAddonFull(&AddonDefinition{
		Name:        "high-availability",
		DisplayName: "High Availability",
		Description: "Service replicas, leader election, failover configuration",
		CUEFile:     "pkg/stackkits/addons/high-availability.cue",
		Priority:    95,
		Condition: func(spec *core.KombinationSpec) bool {
			if len(spec.Nodes) < 3 {
				return false
			}
			// Check for HA metadata
			if spec.Metadata != nil {
				if spec.Metadata["ha"] == "true" || spec.Metadata["high-availability"] == "enabled" {
					return true
				}
			}
			// Check for multiple main nodes (HA indicator)
			mainCount := 0
			for _, node := range spec.Nodes {
				if node.Type == "main" {
					mainCount++
				}
			}
			return mainCount >= 2
		},
	})

	// gpu-workloads: Activated when GPU nodes are present
	d.RegisterAddonFull(&AddonDefinition{
		Name:        "gpu-workloads",
		DisplayName: "GPU Workloads",
		Description: "NVIDIA container runtime, GPU scheduling",
		CUEFile:     "pkg/stackkits/addons/gpu-workloads.cue",
		Priority:    85,
		Condition: func(spec *core.KombinationSpec) bool {
			for _, node := range spec.Nodes {
				if hasGPUIndicator(node) {
					return true
				}
			}
			return false
		},
	})

	// vpn-overlay: Activated when VPN is configured
	d.RegisterAddonFull(&AddonDefinition{
		Name:        "vpn-overlay",
		DisplayName: "VPN Overlay",
		Description: "VPN mesh network configuration",
		CUEFile:     "pkg/stackkits/addons/vpn-overlay.cue",
		Priority:    70,
		Condition: func(spec *core.KombinationSpec) bool {
			return spec.Network.VPN != "" && spec.Network.VPN != "none"
		},
	})

	// multi-node: Activated when more than one node is present
	d.RegisterAddonFull(&AddonDefinition{
		Name:        "multi-node",
		DisplayName: "Multi-Node",
		Description: "Multi-node deployment patterns and service distribution",
		CUEFile:     "pkg/stackkits/addons/multi-node.cue",
		Priority:    50,
		Condition: func(spec *core.KombinationSpec) bool {
			return len(spec.Nodes) > 1
		},
	})
}

// Detect analyzes a spec and returns all applicable add-ons.
func (d *AddonDetector) Detect(spec *core.KombinationSpec) *DetectionResult {
	result := &DetectionResult{
		Addons:       []AddonInfo{},
		TotalChecked: len(d.registry),
	}

	// Collect matching addons
	var matched []AddonInfo
	for _, name := range d.order {
		def := d.registry[name]
		if def.Condition(spec) {
			info := AddonInfo{
				Name:        def.Name,
				DisplayName: def.DisplayName,
				Description: def.Description,
				CUEFile:     def.CUEFile,
				Priority:    def.Priority,
				Reason:      d.getReasonForAddon(name, spec),
			}
			matched = append(matched, info)
		} else {
			result.SkippedCount++
		}
	}

	// Sort by priority (highest first)
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].Priority > matched[j].Priority
	})

	result.Addons = matched
	return result
}

// DetectNames returns just the names of applicable add-ons.
func (d *AddonDetector) DetectNames(spec *core.KombinationSpec) []string {
	result := d.Detect(spec)
	names := make([]string, len(result.Addons))
	for i, addon := range result.Addons {
		names[i] = addon.Name
	}
	return names
}

// getReasonForAddon returns a human-readable reason for why an addon was activated.
func (d *AddonDetector) getReasonForAddon(name string, spec *core.KombinationSpec) string {
	switch name {
	case "cloud-integration":
		for _, node := range spec.Nodes {
			if isCloudProvider(node.Provider) {
				return "Cloud provider '" + node.Provider + "' detected on node '" + node.Name + "'"
			}
		}
		return "Cloud provider node detected"

	case "arm-support":
		for _, node := range spec.Nodes {
			if hasARMIndicator(node) {
				return "ARM architecture detected on node '" + node.Name + "'"
			}
		}
		return "ARM architecture node detected"

	case "low-memory":
		for _, node := range spec.Nodes {
			if hasLowMemoryIndicator(node) {
				if node.Tags != nil {
					if mem, ok := node.Tags["memory"]; ok {
						return "Node '" + node.Name + "' has limited memory (" + mem + ")"
					}
				}
				return "Node '" + node.Name + "' has limited resources"
			}
		}
		return "Node with limited memory detected (< 4GB)"

	case "high-availability":
		mainCount := 0
		for _, node := range spec.Nodes {
			if node.Type == "main" {
				mainCount++
			}
		}
		if mainCount >= 2 {
			return "Multiple main nodes detected (" + fmt.Sprint(mainCount) + " nodes)"
		}
		return "3+ nodes with HA configuration detected"

	case "gpu-workloads":
		for _, node := range spec.Nodes {
			if hasGPUIndicator(node) {
				return "GPU-enabled node '" + node.Name + "' detected"
			}
		}
		return "GPU-enabled node detected"

	case "vpn-overlay":
		return "VPN '" + spec.Network.VPN + "' configured in network spec"

	case "multi-node":
		return "Multiple nodes detected (" + fmt.Sprint(len(spec.Nodes)) + " nodes)"

	default:
		return "Condition matched"
	}
}

// RegisterAddon adds a custom add-on condition (simple version).
func (d *AddonDetector) RegisterAddon(name string, condition AddonCondition) {
	d.RegisterAddonFull(&AddonDefinition{
		Name:        name,
		DisplayName: name,
		Description: "Custom add-on",
		CUEFile:     "pkg/stackkits/addons/" + name + ".cue",
		Priority:    0,
		Condition:   condition,
	})
}

// RegisterAddonFull adds a custom add-on with full definition.
func (d *AddonDetector) RegisterAddonFull(def *AddonDefinition) {
	if _, exists := d.registry[def.Name]; !exists {
		d.order = append(d.order, def.Name)
	}
	d.registry[def.Name] = def
}

// UnregisterAddon removes an add-on from the registry.
func (d *AddonDetector) UnregisterAddon(name string) {
	delete(d.registry, name)
	// Remove from order
	for i, n := range d.order {
		if n == name {
			d.order = append(d.order[:i], d.order[i+1:]...)
			break
		}
	}
}

// GetRegisteredAddons returns all registered add-on names.
func (d *AddonDetector) GetRegisteredAddons() []string {
	return d.order
}

// GetAddonDefinition returns the full definition of an add-on.
func (d *AddonDetector) GetAddonDefinition(name string) *AddonDefinition {
	return d.registry[name]
}

// ListAddons returns all registered add-on definitions in priority order.
func (d *AddonDetector) ListAddons() []AddonInfo {
	var addons []AddonInfo
	for _, name := range d.order {
		def := d.registry[name]
		addons = append(addons, AddonInfo{
			Name:        def.Name,
			DisplayName: def.DisplayName,
			Description: def.Description,
			CUEFile:     def.CUEFile,
			Priority:    def.Priority,
		})
	}
	return addons
}

// Helper functions for safe tag access

// hasARMIndicator checks if a node has ARM architecture indicators.
func hasARMIndicator(node core.NodeSpec) bool {
	if node.Tags == nil {
		return false
	}
	if arch, ok := node.Tags["arch"]; ok {
		if arch == "arm64" || arch == "arm" || arch == "aarch64" {
			return true
		}
	}
	if node.Tags["type"] == "raspberry-pi" || node.Tags["device"] == "rpi" {
		return true
	}
	return false
}

// hasLowMemoryIndicator checks if a node has low memory indicators.
func hasLowMemoryIndicator(node core.NodeSpec) bool {
	if node.Tags == nil {
		return false
	}
	if mem, ok := node.Tags["memory"]; ok {
		switch mem {
		case "low", "1gb", "2gb", "3gb", "1024", "2048", "3072":
			return true
		}
	}
	if node.Tags["resources"] == "limited" {
		return true
	}
	return false
}

// hasGPUIndicator checks if a node has GPU indicators.
func hasGPUIndicator(node core.NodeSpec) bool {
	if node.Tags == nil {
		return false
	}
	if node.Tags["gpu"] == "true" || node.Tags["nvidia"] == "true" {
		return true
	}
	if node.Tags["type"] == "gpu" || node.Tags["accelerator"] != "" {
		return true
	}
	return false
}

// isCloudProvider checks if a provider is a cloud provider.
// Uses the shared validator package for consistent provider detection.
func isCloudProvider(provider string) bool {
	return validator.IsCloudProvider(provider)
}
