package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/providerevidence"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	providerexecutor "github.com/kombifyio/stackkits/contracts/providerexecutor/v1beta1"
)

// devProviderFaultsEnv names a local file whose content decides how every
// managed create on a local test Core ends, without provider cost: a provider
// reason code (provider.timeout, provider.invalid_spec, ...) fails the create
// with that code, and "succeed" binds a placeholder server at a documentation
// address (RFC 5737) that the bootstrap cannot reach. It replaces the
// commercial adapters only on a local managed-runtime test Core (the
// LocalManagedRuntimeE2EAllowed switches) and never in a SaaS edition, so
// create protection can be driven end to end through the product. It has no
// certified provision discovery, so an ambiguous outcome (provider.timeout,
// provider.partial_create, provider.cleanup_required) stays outcome_unknown
// until the operator reconcile path, which this adapter cannot serve.
const devProviderFaultsEnv = "TECHSTACK_DEV_PROVIDER_FAULTS_FILE"

const devFaultPublicIPv4 = "192.0.2.10"

func devProviderFaultRegistrations(edition config.Edition, database *sql.DB, log *logger.Logger, registrations []managedProviderRegistration) []managedProviderRegistration {
	path := strings.TrimSpace(os.Getenv(devProviderFaultsEnv))
	if path == "" || edition.IsSaaS() || !monthlyruntime.LocalManagedRuntimeE2EAllowed() {
		return registrations
	}
	if log != nil {
		log.Warn("dev_provider_faults_active", "file", path,
			"message", "managed provider adapters are replaced by the local fault adapter; no provider is called")
	}
	faulted := make([]managedProviderRegistration, 0, len(registrations))
	for _, registration := range registrations {
		adapter := &devFaultAdapter{
			path: path, providerID: registration.providerID, adapterID: registration.adapterID,
			manifestHash: registration.adapterManifestHash, database: database,
		}
		registration.build = func() (managedProviderAdapter, error) {
			evidence, err := providerevidence.NewRecorder(database)
			if err != nil {
				return nil, err
			}
			adapter.evidence = evidence
			return adapter, nil
		}
		faulted = append(faulted, registration)
	}
	return faulted
}

type devFaultAdapter struct {
	path, providerID, adapterID, manifestHash string
	database                                  *sql.DB
	evidence                                  *providerevidence.Recorder
}

type devFaultPrepared struct {
	binding providercontrol.PreparedProvisionBinding
}

func (p *devFaultPrepared) ProvisionBinding() providercontrol.PreparedProvisionBinding {
	return p.binding
}

func devFaultDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// The fault adapter stands in for the registered provider, so it declares that
// provider and adapter identity with the safety every managed adapter declares.
func (a *devFaultAdapter) ProviderSafetyCapabilities() providercontrol.ProviderSafetyCapabilities {
	return providercontrol.ProviderSafetyCapabilities{
		ProviderID: a.providerID, AdapterID: a.adapterID, AtMostOnceProvision: true,
		ProvisionCandidateDiscovery: true, ExactHandleDecommission: true,
		DefinitiveAbsenceProof: true, HostFirewallBaseline: true,
	}
}

func (a *devFaultAdapter) ProviderCreateRegion(_, requested string) (string, error) {
	if region := strings.ToLower(strings.TrimSpace(requested)); region != "" {
		return region, nil
	}
	if a.providerID == "centron" {
		return "de", nil
	}
	return "de/fra", nil
}

func (a *devFaultAdapter) ManagedCredentialAuthority() providercontrol.ManagedCredentialAuthority {
	return providercontrol.ManagedCredentialAuthority{
		ProviderID: a.providerID, Version: "dev-faults-v1", GrantID: "managed-vps", Scope: "compute",
		CustodyRef:    "custody://local/" + a.providerID + "/dev-faults",
		ConnectionRef: "provider-connection://" + a.providerID + "/dev-faults",
		ValidUntil:    time.Now().UTC().Add(24 * time.Hour),
	}
}

func (a *devFaultAdapter) AtMostOnceProvisionCapability() providercontrol.AtMostOnceProvisionCapability {
	return providercontrol.AtMostOnceProvisionCapability{
		AdapterManifestHash:  a.manifestHash,
		PerHeadInvocationKey: true, SideEffectFreePreparation: true, PreparedRequestDigestBinding: true,
		ReadOnlyProvisionPolling: true, ReadOnlyGeneralObservation: true,
		ExactHandleDecommission: true, ReadOnlyAbsencePolling: true,
	}
}

func (a *devFaultAdapter) PrepareProvision(_ context.Context, invocation providercontrol.AdapterInvocation) (providercontrol.PreparedProvisionRequest, error) {
	command := invocation.Request.Command
	if command.ProviderID != a.providerID || command.AdapterID != a.adapterID {
		return nil, fmt.Errorf("dev provider faults: command identity mismatch")
	}
	return &devFaultPrepared{binding: providercontrol.PreparedProvisionBinding{
		RequestDigest:         devFaultDigest("dev-faults\x00" + command.OperationID + "\x00" + command.ResourceGenerationID),
		CredentialVersionHash: command.CustodyHash,
		ProviderScopeHash:     command.ConnectionHash,
		CorrelationHash:       devFaultDigest(invocation.CorrelationID),
		AdapterManifestHash:   a.manifestHash,
	}}, nil
}

// DispatchPreparedProvision reads the fault file once per create.
func (a *devFaultAdapter) DispatchPreparedProvision(_ context.Context, _ providercontrol.PreparedProvisionRequest, _ providercontrol.ExecutePermit) providerexecutor.ExecutionResult {
	raw, err := os.ReadFile(a.path)
	outcome := strings.TrimSpace(string(raw))
	if err != nil || outcome == "" {
		outcome = providerexecutor.ReasonCodeProviderTransient
	}
	if outcome != "succeed" {
		return providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed,
			Reason: &providerexecutor.Reason{Code: outcome, Retryable: false},
		}
	}
	return providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
		Resources: a.serverBindings(providerexecutor.ObservationUnknown, providerexecutor.CleanupPending),
	}
}

func (a *devFaultAdapter) PollProvisionPresence(_ context.Context, invocation providercontrol.AdapterInvocation) providerexecutor.ExecutionResult {
	return a.present(invocation)
}

func (a *devFaultAdapter) ObserveReadOnly(_ context.Context, invocation providercontrol.AdapterInvocation) providerexecutor.ExecutionResult {
	return a.present(invocation)
}

func (a *devFaultAdapter) present(invocation providercontrol.AdapterInvocation) providerexecutor.ExecutionResult {
	if len(invocation.Request.Previous.Resources) == 0 {
		return providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed,
			Reason: &providerexecutor.Reason{Code: providerexecutor.ReasonCodeProviderPartialCreate},
		}
	}
	return providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent,
		Resources: a.serverBindings(providerexecutor.ObservationPresent, providerexecutor.CleanupRequired),
	}
}

func (a *devFaultAdapter) serverBindings(observation providerexecutor.ObservationState, cleanup providerexecutor.CleanupState) []providerexecutor.ResourceBinding {
	server := a.providerID + "-dev-faults://local/server-" + devFaultPublicIPv4
	ip := a.providerID + "-dev-faults-ip://" + devFaultPublicIPv4
	return []providerexecutor.ResourceBinding{
		{BindingID: "server", Kind: a.providerID + ".dev-server", NativeRef: server,
			OwnershipHash: devFaultDigest(server), Disposition: providerexecutor.DispositionDelete,
			Observation: observation, Cleanup: cleanup},
		{BindingID: "public-ip", Kind: a.providerID + ".dev-public-ip", NativeRef: ip, ParentBindingID: "server",
			OwnershipHash: devFaultDigest(ip), Disposition: providerexecutor.DispositionDelete,
			Observation: observation, Cleanup: cleanup},
	}
}

func (a *devFaultAdapter) DecommissionExactHandles(_ context.Context, invocation providercontrol.AdapterInvocation) providerexecutor.ExecutionResult {
	return providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseDeleteAccepted,
		Resources: devFaultHandles(invocation.Request),
	}
}

func (a *devFaultAdapter) PollDecommissionAbsence(ctx context.Context, invocation providercontrol.AdapterInvocation) providerexecutor.ExecutionResult {
	handles := devFaultHandles(invocation.Request)
	if invocation.Request.Previous.Phase != providerexecutor.PhaseAbsencePending {
		return providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAbsencePending, Resources: handles,
		}
	}
	command := invocation.Request.Command
	for index := range handles {
		evidence, err := a.evidence.Record(ctx, command, providerexecutor.ResourceTarget{
			BindingID: handles[index].BindingID, Kind: handles[index].Kind, NativeRef: handles[index].NativeRef,
			ParentBindingID: handles[index].ParentBindingID, OwnershipHash: handles[index].OwnershipHash,
			Disposition: handles[index].Disposition,
		}, providerevidence.Observation{
			Endpoint: "dev-faults://" + a.providerID + "/" + handles[index].BindingID, StatusCode: http.StatusNotFound,
			CollectedAt: time.Now().UTC(),
		})
		if err != nil {
			return providerexecutor.ExecutionResult{
				Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed,
				Reason: &providerexecutor.Reason{Code: providerexecutor.ReasonCodeProviderTransient, Retryable: true},
			}
		}
		handles[index].Observation = providerexecutor.ObservationAbsent
		handles[index].Cleanup = providerexecutor.CleanupComplete
		handles[index].Evidence = []providerexecutor.Evidence{evidence}
	}
	return providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhaseAbsent, Resources: handles,
	}
}

func devFaultHandles(request providerexecutor.ExecutionRequest) []providerexecutor.ResourceBinding {
	if len(request.Previous.Resources) > 0 {
		return append([]providerexecutor.ResourceBinding(nil), request.Previous.Resources...)
	}
	handles := make([]providerexecutor.ResourceBinding, 0, len(request.Command.Targets))
	for _, target := range request.Command.Targets {
		handles = append(handles, providerexecutor.ResourceBinding{
			BindingID: target.BindingID, Kind: target.Kind, NativeRef: target.NativeRef,
			ParentBindingID: target.ParentBindingID, OwnershipHash: target.OwnershipHash, Disposition: target.Disposition,
			Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupPending,
		})
	}
	return handles
}
