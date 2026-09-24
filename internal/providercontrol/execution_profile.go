package providercontrol

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

var executionProfileDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var disallowedCompositeProviderIDs = map[string]struct{}{
	"centron-managed": {},
	"ionos-managed":   {},
}

func normalizeExecutionProfile(profile ExecutionProfile) (ExecutionProfile, ExecutionProfileSnapshot, error) {
	profile.ProviderID = strings.ToLower(strings.TrimSpace(profile.ProviderID))
	profile.AdapterID = strings.TrimSpace(profile.AdapterID)
	profile.CredentialMode = CredentialMode(strings.ToLower(strings.TrimSpace(string(profile.CredentialMode))))
	profile.RuntimeProfileID = strings.TrimSpace(profile.RuntimeProfileID)
	profile.OfferingID = strings.TrimSpace(profile.OfferingID)
	profile.CatalogVersion = strings.TrimSpace(profile.CatalogVersion)
	profile.CapabilitySnapshotHash = strings.ToLower(strings.TrimSpace(profile.CapabilitySnapshotHash))
	profile.AdapterManifestHash = strings.ToLower(strings.TrimSpace(profile.AdapterManifestHash))
	profile.ProvisionDispatchMode = ProvisionDispatchMode(strings.ToLower(strings.TrimSpace(string(profile.ProvisionDispatchMode))))
	profile.CustodyRef = strings.TrimSpace(profile.CustodyRef)
	profile.CustodyHash = strings.ToLower(strings.TrimSpace(profile.CustodyHash))
	profile.ConnectionRef = strings.TrimSpace(profile.ConnectionRef)
	profile.ConnectionHash = strings.ToLower(strings.TrimSpace(profile.ConnectionHash))
	profile.ExecutionProfileHash = strings.ToLower(strings.TrimSpace(profile.ExecutionProfileHash))
	if err := validateManagedBootstrapNetworkProfile(profile.ProviderID, profile.AdapterID, profile.ManagedBootstrapNetwork); err != nil {
		return ExecutionProfile{}, ExecutionProfileSnapshot{}, fmt.Errorf(
			"%w: managed bootstrap network execution profile is invalid: %v", ErrInvalidRequest, err,
		)
	}

	if _, disallowed := disallowedCompositeProviderIDs[profile.ProviderID]; disallowed {
		return ExecutionProfile{}, ExecutionProfileSnapshot{}, fmt.Errorf(
			"%w: provider_id %q is not a canonical vendor identifier",
			ErrInvalidRequest, profile.ProviderID,
		)
	}
	for _, field := range []struct{ name, value string }{
		{"provider_id", profile.ProviderID},
		{"adapter_id", profile.AdapterID},
		{"runtime_profile_id", profile.RuntimeProfileID},
		{"offering_id", profile.OfferingID},
		{"catalog_version", profile.CatalogVersion},
	} {
		if err := validateExecutionProfileIdentifier(field.name, field.value); err != nil {
			return ExecutionProfile{}, ExecutionProfileSnapshot{}, err
		}
	}
	if profile.CredentialMode != CredentialModeManaged && profile.CredentialMode != CredentialModeBYOK && profile.CredentialMode != CredentialModeWorkerHeld {
		return ExecutionProfile{}, ExecutionProfileSnapshot{}, fmt.Errorf(
			"%w: credential_mode must be managed or byok", ErrInvalidRequest,
		)
	}
	if profile.CredentialMode == CredentialModeWorkerHeld && (profile.ProviderID != "proxmox" || profile.AdapterID != "proxmox-substrate-v1" || profile.ProvisionDispatchMode != ProvisionDispatchProviderCorrelation) {
		return ExecutionProfile{}, ExecutionProfileSnapshot{}, fmt.Errorf("%w: worker-held credentials require the crash-recoverable Proxmox substrate adapter", ErrInvalidRequest)
	}
	if !admittedProvisionDispatchMode(profile.ProvisionDispatchMode) {
		return ExecutionProfile{}, ExecutionProfileSnapshot{}, fmt.Errorf(
			"%w: provision_dispatch_mode %q is not admitted", ErrAdapterSafety, profile.ProvisionDispatchMode,
		)
	}
	for _, field := range []struct{ name, value string }{
		{"capability_snapshot_hash", profile.CapabilitySnapshotHash},
		{"adapter_manifest_hash", profile.AdapterManifestHash},
		{"custody_hash", profile.CustodyHash},
		{"connection_hash", profile.ConnectionHash},
		{"execution_profile_hash", profile.ExecutionProfileHash},
	} {
		if !executionProfileDigestPattern.MatchString(field.value) {
			return ExecutionProfile{}, ExecutionProfileSnapshot{}, fmt.Errorf(
				"%w: %s must be a lowercase sha256 digest", ErrInvalidRequest, field.name,
			)
		}
	}

	snapshot := ExecutionProfileSnapshot{
		ProviderID:              profile.ProviderID,
		AdapterID:               profile.AdapterID,
		CredentialMode:          profile.CredentialMode,
		RuntimeProfileID:        profile.RuntimeProfileID,
		OfferingID:              profile.OfferingID,
		CatalogVersion:          profile.CatalogVersion,
		CapabilitySnapshotHash:  profile.CapabilitySnapshotHash,
		AdapterManifestHash:     profile.AdapterManifestHash,
		ProvisionDispatchMode:   profile.ProvisionDispatchMode,
		ManagedBootstrapNetwork: profile.ManagedBootstrapNetwork,
		ExecutionProfileHash:    profile.ExecutionProfileHash,
	}
	return profile, snapshot, nil
}

func validateExecutionProfileSnapshot(snapshot ExecutionProfileSnapshot, command providerexecutor.Command) error {
	_, normalized, err := normalizeExecutionProfile(ExecutionProfile{
		ProviderID:              snapshot.ProviderID,
		AdapterID:               snapshot.AdapterID,
		CredentialMode:          snapshot.CredentialMode,
		RuntimeProfileID:        snapshot.RuntimeProfileID,
		OfferingID:              snapshot.OfferingID,
		CatalogVersion:          snapshot.CatalogVersion,
		CapabilitySnapshotHash:  snapshot.CapabilitySnapshotHash,
		AdapterManifestHash:     snapshot.AdapterManifestHash,
		ProvisionDispatchMode:   snapshot.ProvisionDispatchMode,
		ManagedBootstrapNetwork: snapshot.ManagedBootstrapNetwork,
		CustodyRef:              "custody://validation/placeholder",
		CustodyHash:             "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		ConnectionRef:           "provider-connection://validation/placeholder",
		ConnectionHash:          "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		ExecutionProfileHash:    snapshot.ExecutionProfileHash,
	})
	if err != nil {
		return err
	}
	if normalized != snapshot ||
		snapshot.ProviderID != command.ProviderID ||
		snapshot.AdapterID != command.AdapterID ||
		snapshot.CapabilitySnapshotHash != command.CapabilitySnapshotHash ||
		snapshot.ExecutionProfileHash != command.ExecutionProfileHash {
		return fmt.Errorf("%w: execution profile snapshot does not match sealed command", ErrInvalidRequest)
	}
	return nil
}

func validateExecutionProfileIdentifier(name, value string) error {
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%w: %s is required and must be a bounded identifier", ErrInvalidRequest, name)
	}
	return nil
}
