// Package monthlyruntime owns TechStack's subscription-backed runtime products.
package monthlyruntime

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
)

const (
	MetadataKeyRuntimeLane        = "runtime_lane"
	MetadataKeyRuntimeOfferingID  = "runtime_offering_id"
	MetadataKeyBillingCadence     = "billing_cadence"
	MetadataKeyServerMode         = "server_mode"
	MetadataKeyBillingMode        = "billing_mode"
	MetadataKeyProviderID         = providercatalog.ProviderIDField
	MetadataKeyLeaseProvider      = providercatalog.LegacyLeaseProviderField
	MetadataKeyProviderRegion     = "provider_region"
	MetadataKeyIONOSDatacenter    = "ionos_datacenter"
	MetadataKeySimulateProviderID = providercatalog.LegacySimulateProviderIDField
	MetadataKeySimulateLifecycle  = "simulate_node_lifecycle"

	ServerModeManagedCloud = "managed-cloud"
	ProviderCentron        = providercatalog.ProviderCentron
	ProviderIONOS          = providercatalog.ProviderIONOS
	DefaultIONOSDatacenter = "de/fra"
	NodeLifecyclePVM       = "pvm"
	BillingSubscription    = "subscription"
)

type Offering struct {
	ID             serverruntime.RuntimeOfferingID `json:"id"`
	Name           string                          `json:"name"`
	BillingCadence serverruntime.BillingCadence    `json:"billing_cadence"`
	Image          string                          `json:"image"`
	VCPUs          int                             `json:"vcpus"`
	MemoryMB       int                             `json:"memory_mb"`
	DiskGB         int                             `json:"disk_gb"`
	Region         string                          `json:"region"`
}

func Catalog() []Offering {
	return []Offering{
		{
			ID:             serverruntime.RuntimeOfferingStandard,
			Name:           "Monthly Runtime Standard",
			BillingCadence: serverruntime.BillingCadenceMonthly,
			Image:          "ubuntu-24.04",
			// Cloud Kit (Coolify, hub, PocketID, TinyAuth, host-security)
			// needs at least 4 vCPU / 8 GiB. Demo and kombify-operated
			// managed VPS must not go below this floor.
			VCPUs:    4,
			MemoryMB: 8192,
			DiskGB:   80,
			Region:   "de-fra",
		},
		{
			ID:             serverruntime.RuntimeOfferingPremium,
			Name:           "Monthly Runtime Premium",
			BillingCadence: serverruntime.BillingCadenceMonthly,
			Image:          "ubuntu-24.04",
			VCPUs:          4,
			MemoryMB:       8192,
			DiskGB:         320,
			Region:         "de-fra",
		},
	}
}

func OfferingByID(id serverruntime.RuntimeOfferingID) (Offering, bool) {
	for _, offering := range Catalog() {
		if offering.ID == id {
			return offering, true
		}
	}
	return Offering{}, false
}

// ResolveOffering requires an explicit catalog product for cost-bearing
// provider work. Provider custody defaults are not an order and must never be
// used as a silent replacement for a missing or unknown offering.
func ResolveOffering(id string) (Offering, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return Offering{}, fmt.Errorf("monthlyruntime: runtime offering is required")
	}
	offering, ok := OfferingByID(serverruntime.RuntimeOfferingID(trimmed))
	if !ok {
		return Offering{}, fmt.Errorf("monthlyruntime: unsupported runtime offering %q", trimmed)
	}
	return offering, nil
}

func OfferingForMinimumResources(minVCPUs, minMemoryMB int) (Offering, bool) {
	var selected Offering
	for _, offering := range Catalog() {
		if minVCPUs > 0 && offering.VCPUs < minVCPUs {
			continue
		}
		if minMemoryMB > 0 && offering.MemoryMB < minMemoryMB {
			continue
		}
		if selected.ID == "" || offering.VCPUs < selected.VCPUs ||
			(offering.VCPUs == selected.VCPUs && offering.MemoryMB < selected.MemoryMB) {
			selected = offering
		}
	}
	return selected, selected.ID != ""
}

func LargestOffering() (Offering, bool) {
	var selected Offering
	for _, offering := range Catalog() {
		if selected.ID == "" || offering.VCPUs > selected.VCPUs ||
			(offering.VCPUs == selected.VCPUs && offering.MemoryMB > selected.MemoryMB) ||
			(offering.VCPUs == selected.VCPUs && offering.MemoryMB == selected.MemoryMB && offering.DiskGB > selected.DiskGB) {
			selected = offering
		}
	}
	return selected, selected.ID != ""
}

func DefaultOfferingID() serverruntime.RuntimeOfferingID {
	return serverruntime.RuntimeOfferingStandard
}

func OfferingIDFromMetadata(metadata map[string]string) serverruntime.RuntimeOfferingID {
	if metadata == nil {
		return DefaultOfferingID()
	}
	if raw := strings.TrimSpace(metadata[MetadataKeyRuntimeOfferingID]); raw != "" {
		return serverruntime.RuntimeOfferingID(raw)
	}
	return DefaultOfferingID()
}

// NormalizeMetadata formats non-authoritative monthly-runtime metadata. Fresh
// product writes must call NormalizeFreshMetadata so invalid or legacy provider
// identity is returned as an error before persistence.
func NormalizeMetadata(metadata map[string]string, offeringID serverruntime.RuntimeOfferingID) map[string]string {
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	delete(out, MetadataKeyLeaseProvider)
	delete(out, MetadataKeySimulateProviderID)
	if offeringID == "" {
		offeringID = OfferingIDFromMetadata(metadata)
	}
	if _, ok := OfferingByID(offeringID); !ok {
		offeringID = DefaultOfferingID()
	}
	if out[MetadataKeyServerMode] == ServerModeManagedCloud || strings.TrimSpace(out[MetadataKeyServerMode]) == "" {
		out[MetadataKeyServerMode] = serverruntime.RuntimeLaneMonthly
	}
	out[MetadataKeyRuntimeLane] = serverruntime.RuntimeLaneMonthly
	out[MetadataKeyRuntimeOfferingID] = string(offeringID)
	out[MetadataKeyBillingCadence] = string(serverruntime.BillingCadenceMonthly)
	if strings.TrimSpace(out[MetadataKeyBillingMode]) == "" {
		out[MetadataKeyBillingMode] = BillingSubscription
	}
	if out[MetadataKeyProviderID] == "" {
		out[MetadataKeyProviderID] = ProviderCentron
	}
	if out[MetadataKeyProviderID] == ProviderIONOS {
		datacenter := NormalizeIONOSDatacenter(firstNonEmptyString(
			out[MetadataKeyIONOSDatacenter],
			out[MetadataKeyProviderRegion],
		))
		out[MetadataKeyIONOSDatacenter] = datacenter
		out[MetadataKeyProviderRegion] = datacenter
	}
	if strings.TrimSpace(out[MetadataKeySimulateLifecycle]) == "" {
		out[MetadataKeySimulateLifecycle] = NodeLifecyclePVM
	}
	return out
}

// NormalizeFreshMetadata validates exact provider identity before returning
// metadata suitable for a new managed-runtime write. Composite aliases and the
// legacy provider fields are rejected rather than translated.
func NormalizeFreshMetadata(metadata map[string]string, offeringID serverruntime.RuntimeOfferingID) (map[string]string, error) {
	if err := providercatalog.ValidateNoLegacyProviderFields(
		metadata[MetadataKeyLeaseProvider],
		metadata[MetadataKeySimulateProviderID],
	); err != nil {
		return nil, fmt.Errorf("monthlyruntime: %w", err)
	}
	providerID := metadata[MetadataKeyProviderID]
	if _, err := providercatalog.CanonicalProviderID(providerID); err != nil {
		return nil, fmt.Errorf("monthlyruntime: %w", err)
	}
	out := NormalizeMetadata(metadata, offeringID)
	out[MetadataKeyProviderID] = providerID
	return out, nil
}

func NormalizeIONOSDatacenter(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	normalized = strings.ReplaceAll(normalized, "_", "/")
	switch normalized {
	case "", "default", "de/fra", "de-fra", "fra", "frankfurt":
		return DefaultIONOSDatacenter
	case "de/txl", "de-txl", "txl", "berlin":
		return "de/txl"
	case "us/ewr", "us-ewr", "ewr", "newark":
		return "us/ewr"
	case "us/las", "us-las", "las", "las-vegas":
		return "us/las"
	case "de/fra/2", "de-fra-2", "fra2", "frankfurt-2":
		return "de/fra/2"
	default:
		return DefaultIONOSDatacenter
	}
}

func ProviderFromMetadata(metadata map[string]string) string {
	if metadata == nil {
		return ProviderCentron
	}
	if value := strings.TrimSpace(metadata[MetadataKeyProviderID]); value != "" {
		return value
	}
	return ProviderCentron
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func IsMonthlyRuntimeMetadata(metadata map[string]string) bool {
	if metadata == nil {
		return false
	}
	return metadata[MetadataKeyRuntimeLane] == serverruntime.RuntimeLaneMonthly ||
		metadata[MetadataKeyServerMode] == serverruntime.RuntimeLaneMonthly ||
		metadata[MetadataKeyServerMode] == ServerModeManagedCloud
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}
