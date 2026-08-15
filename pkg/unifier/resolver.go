// Package unifier provides the StackKit Resolver for automatic StackKit selection.
package unifier

import (
	"fmt"

	"github.com/kombifyio/techstack/pkg/core"
)

// Common StackKit identifiers used throughout the resolver.
const (
	kitBasement         = StackKitBasement
	resolverArchAArch64 = "aarch64"
	resolverArchARM     = "arm"
	resolverArchARM64   = "arm64"
	resolverDeviceRPI   = "rpi"
	resolverMemory1GB   = "1gb"
	resolverMemory2GB   = "2gb"
	resolverMemory3GB   = "3gb"
	resolverMemoryLow   = "low"
	resolverTypeRPI     = "raspberry-pi"
)

// StackKitResolver handles automatic StackKit selection for Easy-Path users.
// This is Step 2 of the Unifier Pipeline.
//
// Pipeline: Pre-Validation → StackKit Resolution → Add-On Detection → Unifier Engine
type StackKitResolver struct {
	availableKits map[string]bool // Set of available kit names
	kitList       []string        // Ordered list for iteration
}

// NewStackKitResolver creates a new StackKit resolver.
func NewStackKitResolver(availableKits []string) *StackKitResolver {
	if availableKits == nil {
		availableKits = DiscoverAvailableStackKits(DefaultStackKitsDir())
	}
	availableKits = append(availableKits, DefaultKnownStackKits()...)

	kitSet := make(map[string]bool, len(availableKits))
	kitList := make([]string, 0, len(availableKits))
	for _, kit := range availableKits {
		kit = CanonicalStackKitName(kit)
		if kit == "" || !IsSupportedProductStackKit(kit) {
			continue
		}
		if kitSet[kit] {
			continue
		}
		kitSet[kit] = true
		kitList = append(kitList, kit)
	}
	if len(kitList) == 0 {
		for _, kit := range DefaultKnownStackKits() {
			kitSet[kit] = true
			kitList = append(kitList, kit)
		}
	}

	return &StackKitResolver{
		availableKits: kitSet,
		kitList:       kitList,
	}
}

// ResolveResult contains the resolution outcome.
type ResolveResult struct {
	StackKit        string   // Selected StackKit name
	Reason          string   // Human-readable reason for selection
	AutoSelected    bool     // True if auto-selected (Easy-Path), false if user-specified
	Confidence      float64  // 0.0-1.0, how confident we are in the selection
	AlternativeKits []string // Other kits that might also work
	Warnings        []string // Any warnings during resolution
	Valid           bool     // Whether the resolution is valid
}

// Resolve determines the appropriate StackKit for a spec.
// If spec.Kit is already set (Techie-Path), it validates and returns it.
// If spec.Kit is empty (Easy-Path), it auto-selects based on spec content.
func (r *StackKitResolver) Resolve(spec *core.KombinationSpec) *ResolveResult {
	// Techie-Path: User already selected a StackKit
	if spec.Kit != "" {
		return r.validateUserSelection(spec.Kit)
	}

	// Easy-Path: Auto-select based on spec content
	return r.autoSelect(spec)
}

// validateUserSelection validates a user-specified StackKit.
func (r *StackKitResolver) validateUserSelection(kit string) *ResolveResult {
	canonicalKit := CanonicalStackKitName(kit)
	result := &ResolveResult{
		StackKit:     canonicalKit,
		Reason:       "user-specified",
		AutoSelected: false,
		Confidence:   1.0,
		Valid:        true,
	}

	if canonicalKit != kit {
		result.Reason = "user-specified (alias)"
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("StackKit '%s' is treated as '%s' (alias)", kit, canonicalKit))
	}

	// Check if kit is available
	if !r.availableKits[canonicalKit] {
		result.Valid = false
		result.Confidence = 0.0
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("StackKit '%s' is not available. Available kits: %v", canonicalKit, r.kitList))

		// Suggest closest match
		closest := r.findClosestKit(canonicalKit)
		if closest != "" {
			result.AlternativeKits = []string{closest}
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Did you mean '%s'?", closest))
		}
	}

	return result
}

// findClosestKit finds a kit name that's similar to the input.
func (r *StackKitResolver) findClosestKit(input string) string {
	// Simple prefix/suffix matching
	for kit := range r.availableKits {
		if len(input) >= 3 && len(kit) >= 3 {
			// Check if input is contained in kit or vice versa
			if contains(kit, input) || contains(input, kit) {
				return kit
			}
		}
	}
	return ""
}

// contains checks if s contains substr (case insensitive-ish).
func contains(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// autoSelect determines the best StackKit based on spec characteristics.
func (r *StackKitResolver) autoSelect(spec *core.KombinationSpec) *ResolveResult {
	analysis := r.analyzeSpec(spec)

	var result *ResolveResult

	switch {
	case analysis.HasCloud:
		result = &ResolveResult{
			StackKit:        StackKitCloud,
			Reason:          "cloud provider detected - using Cloud Kit",
			AutoSelected:    true,
			Confidence:      0.95,
			AlternativeKits: []string{kitBasement},
		}

	case analysis.AllARM:
		result = &ResolveResult{
			StackKit:        kitBasement,
			Reason:          "ARM architecture detected - using Basement Kit",
			AutoSelected:    true,
			Confidence:      0.90,
			AlternativeKits: []string{StackKitCloud},
		}

	case analysis.NodeCount == 1 && analysis.LocalOnly:
		result = &ResolveResult{
			StackKit:        kitBasement,
			Reason:          "single local node detected - ideal for Basement Kit",
			AutoSelected:    true,
			Confidence:      0.95,
			AlternativeKits: []string{StackKitCloud},
		}

	case analysis.LocalOnly:
		result = &ResolveResult{
			StackKit:        kitBasement,
			Reason:          "local nodes detected - using Basement Kit",
			AutoSelected:    true,
			Confidence:      0.88,
			AlternativeKits: []string{StackKitCloud},
		}

	default:
		result = &ResolveResult{
			StackKit:        kitBasement,
			Reason:          "default selection for general homelab use",
			AutoSelected:    true,
			Confidence:      0.75,
			AlternativeKits: []string{StackKitCloud},
		}
	}

	result.Valid = r.availableKits[result.StackKit]
	if !result.Valid {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("Selected kit '%s' not available, falling back to 'basement-kit'", result.StackKit))
		result.StackKit = kitBasement
		result.Valid = r.availableKits[kitBasement]
	}

	return result
}

// specAnalysis contains analyzed characteristics of a spec.
type specAnalysis struct {
	NodeCount     int
	HasLocal      bool
	HasCloud      bool
	LocalOnly     bool
	CloudOnly     bool
	HasARM        bool
	AllARM        bool
	HasLowMemory  bool // < 4GB RAM nodes
	IsDevelopment bool
	HasHA         bool // High availability indicators
}

// analyzeSpec extracts characteristics from a spec for decision making.
func (r *StackKitResolver) analyzeSpec(spec *core.KombinationSpec) specAnalysis {
	analysis := specAnalysis{
		NodeCount: len(spec.Nodes),
	}

	localCount, cloudCount := analyzeProviderPlacement(spec.Nodes, &analysis)
	armCount := analyzeNodeTags(spec.Nodes, &analysis)

	analysis.LocalOnly = localCount > 0 && cloudCount == 0
	analysis.CloudOnly = cloudCount > 0 && localCount == 0
	analysis.AllARM = armCount == len(spec.Nodes) && armCount > 0
	analysis.IsDevelopment = hasDevelopmentMetadata(spec.Metadata)
	analysis.HasHA = hasHAIndicators(spec.Nodes)

	return analysis
}

func analyzeProviderPlacement(nodes []core.NodeSpec, analysis *specAnalysis) (int, int) {
	localCount := 0
	cloudCount := 0

	for _, node := range nodes {
		switch node.Provider {
		case providerLocal, providerHomelab, providerDocker:
			localCount++
			analysis.HasLocal = true
		case providerHetzner, providerAWS, providerGCP, providerAzure, providerDigitalOcean, providerDigitalOceanManaged, providerCentron, providerIONOS:
			cloudCount++
			analysis.HasCloud = true
		}
	}

	return localCount, cloudCount
}

func analyzeNodeTags(nodes []core.NodeSpec, analysis *specAnalysis) int {
	armCount := 0

	for _, node := range nodes {
		if hasResolverARMIndicator(node.Tags) {
			armCount++
			analysis.HasARM = true
		}
		if hasResolverLowMemoryIndicator(node.Tags) {
			analysis.HasLowMemory = true
		}
	}

	return armCount
}

func hasResolverARMIndicator(tags map[string]string) bool {
	switch tags["arch"] {
	case resolverArchARM64, resolverArchARM, resolverArchAArch64:
		return true
	default:
		return tags["type"] == resolverTypeRPI || tags["device"] == resolverDeviceRPI
	}
}

func hasResolverLowMemoryIndicator(tags map[string]string) bool {
	switch tags["memory"] {
	case resolverMemoryLow, resolverMemory1GB, resolverMemory2GB, resolverMemory3GB:
		return true
	default:
		return false
	}
}

func hasDevelopmentMetadata(metadata map[string]string) bool {
	return metadata["environment"] == "development" ||
		metadata["env"] == "dev" ||
		metadata["purpose"] == "testing"
}

func hasHAIndicators(nodes []core.NodeSpec) bool {
	const nodeTypeMain = "main"

	if len(nodes) < 3 {
		return false
	}

	mainNodes := 0
	for _, node := range nodes {
		if node.Type == nodeTypeMain {
			mainNodes++
		}
	}

	return mainNodes >= 2
}

// IsKitAvailable checks if a StackKit is available.
func (r *StackKitResolver) IsKitAvailable(kit string) bool {
	return r.availableKits[kit]
}

// GetAvailableKits returns all available StackKits.
func (r *StackKitResolver) GetAvailableKits() []string {
	return r.kitList
}

// AddKit adds a new StackKit to the available kits.
func (r *StackKitResolver) AddKit(kit string) {
	kit = CanonicalStackKitName(kit)
	if !IsSupportedProductStackKit(kit) {
		return
	}
	if !r.availableKits[kit] {
		r.availableKits[kit] = true
		r.kitList = append(r.kitList, kit)
	}
}

// RemoveKit removes a StackKit from the available kits.
func (r *StackKitResolver) RemoveKit(kit string) {
	delete(r.availableKits, kit)
	// Remove from list
	for i, k := range r.kitList {
		if k == kit {
			r.kitList = append(r.kitList[:i], r.kitList[i+1:]...)
			break
		}
	}
}
