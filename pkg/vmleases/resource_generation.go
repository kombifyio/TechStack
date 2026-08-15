package vmleases

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/google/uuid"
)

const (
	// MetadataKeyResourceGenerationID identifies one concrete provider-resource
	// generation behind a lease. It is generated only by the lease authority
	// and must survive ordinary lease updates unchanged.
	MetadataKeyResourceGenerationID = "resource_generation_id"
	// MetadataKeyDecommissionClaimDigest is an authority-owned, immutable claim
	// that binds every provider teardown attempt to one exact resource
	// generation. Once claimed, that generation cannot be replaced underneath
	// an in-flight decommission.
	MetadataKeyDecommissionClaimDigest = "resource_decommission_claim_digest"
	resourceGenerationDigestVersion    = "techstack.vmlease.resource-generation/v1"
)

var (
	ErrResourceGenerationUnavailable = errors.New("vmleases: resource generation unavailable")
	ErrResourceGenerationImmutable   = errors.New("vmleases: resource generation id is immutable")
	ErrResourceGenerationDigest      = errors.New("vmleases: valid resource generation digest required")
	ErrResourceGenerationSuperseded  = errors.New("vmleases: resource generation superseded")
	ErrDecommissionClaimImmutable    = errors.New("vmleases: decommission generation claim is immutable")
	ErrLeaseIdentityConflict         = errors.New("vmleases: lease id conflicts with idempotency key")
)

type resourceGenerationDigestPayload struct {
	Version              string `json:"version"`
	TenantID             string `json:"tenant_id"`
	LeaseID              string `json:"lease_id"`
	ResourceGenerationID string `json:"resource_generation_id"`
	ProviderID           string `json:"provider_id"`
	EngineVMID           string `json:"engine_vm_id"`
	SimulationID         string `json:"simulation_id"`
	VMID                 string `json:"vm_id"`
}

func assignNewResourceGenerationID(lease *vmlease.Lease) error {
	if lease == nil {
		return ErrResourceGenerationUnavailable
	}
	generationID, err := uuid.NewRandom()
	if err != nil {
		return fmt.Errorf("%w: generate id: %v", ErrResourceGenerationUnavailable, err)
	}
	setLeaseMetadata(lease, MetadataKeyResourceGenerationID, generationID.String())
	return nil
}

// ResourceGenerationID returns the authority-generated identifier for the
// concrete provider-resource generation currently bound to lease.
func ResourceGenerationID(lease vmlease.Lease) string {
	return strings.TrimSpace(lease.Metadata[MetadataKeyResourceGenerationID])
}

// ResourceGenerationDigest returns a canonical opaque binding between the
// tenant/lease generation and the stable provider identity currently stored on
// the lease. The fixed JSON struct gives the hash input deterministic field
// ordering while the version marker provides domain separation.
func ResourceGenerationDigest(tenantID string, lease vmlease.Lease) (string, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return "", ErrTenantRequired
	}
	leaseTenantID, err := tenantIDFromLease(lease)
	if err != nil {
		return "", err
	}
	if leaseTenantID != tenantID {
		return "", ErrTenantMismatch
	}
	leaseID := strings.TrimSpace(string(lease.ID))
	generationID := ResourceGenerationID(lease)
	providerID := strings.ToLower(strings.TrimSpace(lease.Resource.ProviderID))
	engineVMID := strings.TrimSpace(lease.Resource.EngineVMID)
	simulationID := strings.TrimSpace(lease.Resource.SimulationID)
	vmID := strings.TrimSpace(lease.Resource.VMID)
	if leaseID == "" || generationID == "" || providerID == "" {
		return "", ErrResourceGenerationUnavailable
	}
	payload, err := json.Marshal(resourceGenerationDigestPayload{
		Version:              resourceGenerationDigestVersion,
		TenantID:             tenantID,
		LeaseID:              leaseID,
		ResourceGenerationID: generationID,
		ProviderID:           providerID,
		EngineVMID:           engineVMID,
		SimulationID:         simulationID,
		VMID:                 vmID,
	})
	if err != nil {
		return "", fmt.Errorf("%w: encode binding: %v", ErrResourceGenerationUnavailable, err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func validResourceGenerationDigest(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func ensureResourceGenerationUnchanged(existing, updated vmlease.Lease) error {
	if ResourceGenerationID(existing) != ResourceGenerationID(updated) {
		return ErrResourceGenerationImmutable
	}
	return nil
}

func ensureCancellationMonotonic(existing, updated vmlease.Lease) error {
	if existing.CancelledAt == nil {
		return nil
	}
	if updated.CancelledAt == nil || !updated.CancelledAt.Equal(*existing.CancelledAt) {
		return ErrLeaseCancelled
	}
	if updated.DesiredState != vmlease.DesiredStateStopped && updated.DesiredState != vmlease.DesiredStateArchived {
		return ErrLeaseCancelled
	}
	return nil
}

func ensureDecommissionClaimUnchanged(existing, updated vmlease.Lease) error {
	existingClaim := strings.TrimSpace(existing.Metadata[MetadataKeyDecommissionClaimDigest])
	updatedClaim := strings.TrimSpace(updated.Metadata[MetadataKeyDecommissionClaimDigest])
	if existingClaim != "" && updatedClaim != existingClaim {
		return ErrDecommissionClaimImmutable
	}
	if updatedClaim == "" {
		return nil
	}
	tenantID, err := tenantIDFromLease(updated)
	if err != nil {
		return err
	}
	digest, err := ResourceGenerationDigest(tenantID, updated)
	if err != nil {
		return err
	}
	if updatedClaim != digest {
		return ErrResourceGenerationDigest
	}
	return nil
}

func hasDecommissionClaim(lease vmlease.Lease) bool {
	return strings.TrimSpace(lease.Metadata[MetadataKeyDecommissionClaimDigest]) != ""
}

func cloneLease(lease vmlease.Lease) vmlease.Lease {
	cloned := lease
	cloned.Metadata = maps.Clone(lease.Metadata)
	if lease.CancelledAt != nil {
		cancelledAt := *lease.CancelledAt
		cloned.CancelledAt = &cancelledAt
	}
	return cloned
}
