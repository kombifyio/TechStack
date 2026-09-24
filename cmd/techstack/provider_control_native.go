package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	providerexecutor "github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/providercontroljobs"
	"github.com/kombifyio/techstack/internal/providerevidence"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/kombifyio/techstack/internal/substrateruntime"
	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
)

// managedProviderAdapter is the contract every executable managed provider
// must satisfy to open mutations: the at-most-once executor plus the opaque
// custody authority that native admission materializes per tenant subject.
type managedProviderAdapter interface {
	providercontrol.AtMostOnceProvisionExecutor
	providercontrol.ManagedProviderSafetyReporter
	ManagedCredentialAuthority() providercontrol.ManagedCredentialAuthority
}

// managedProviderRegistration binds one adapter build to the provider identity
// whose catalog coverage it must satisfy before it may serve the Wizard.
type managedProviderRegistration struct {
	providerID          string
	adapterID           string
	adapterManifestHash string
	build               func() (managedProviderAdapter, error)
}

func enforceProviderCreateSafety(
	policy providercontrol.StaticProviderCreatePolicy,
	capabilities providercontrol.ProviderSafetyCapabilities,
) {
	if policy == nil || capabilities.ProvisionCandidateDiscovery {
		return
	}
	policy[capabilities.ProviderID] = providercontrol.ProviderCreateDecision{
		Enabled: false, ReasonCode: providercontrol.ProviderCreateReasonSafetyIncomplete,
	}
}

// composeNativeProviderControl is shared by private and public selfhost builds.
// Commercial adapters and observers are injected only by the private wrapper.
func composeNativeProviderControl(database *sql.DB, edition config.Edition, log *logger.Logger,
	registrations []managedProviderRegistration, onParkedOperation func(providercontrol.OperationRef),
	observer func(*providercontrol.AsyncRequestStore) func(providerexecutor.Command, providerexecutor.Receipt, providerexecutor.Receipt),
) (*providercontrol.Runtime, jobs.RuntimeActions, error) {
	if database == nil {
		return nil, jobs.RuntimeActions{}, fmt.Errorf("compose provider control: database is required")
	}
	capacityPolicy, err := newProviderControlCapacityPolicy(edition)
	if err != nil {
		return nil, jobs.RuntimeActions{}, err
	}
	registry := providercontrol.NewRegistry()
	substrateAdapter, err := substrateruntime.NewAdapter(database, agentcontrol.NewHub(database))
	if err != nil {
		return nil, jobs.RuntimeActions{}, err
	}
	if err := registry.Register(substrate.AdapterID, substrateAdapter); err != nil {
		return nil, jobs.RuntimeActions{}, err
	}
	var gate providercontrol.MutationActivationGate = providercontrol.CertifiedMutationGate{Registry: registry}
	createPolicy := providercontrol.StaticProviderCreatePolicy{}
	managedCredentialAuthorities := providercontrol.ManagedCredentialAuthoritySet{}
	for _, provider := range registrations {
		// A registered provider is create-ready by default. Adapter composition
		// and capability discovery below are the authority; there is no second
		// deployment switch that can silently disable the normal lifecycle.
		createPolicy[provider.providerID] = providercontrol.ProviderCreateDecision{Enabled: true}
		adapter, adapterErr := provider.build()
		if adapterErr != nil {
			createPolicy[provider.providerID] = providercontrol.ProviderCreateDecision{
				Enabled: false, ReasonCode: providercontrol.ProviderCreateReasonSafetyIncomplete,
			}
			// A provider without resolvable custody stays unregistered rather
			// than blocking the ones that do resolve. Its wizard offerings then
			// fail closed on the missing adapter, not on a half-open path.
			if log != nil {
				log.Warn("provider_adapter_unavailable", "provider", provider.providerID, "reason", adapterErr)
			}
			continue
		}
		if registerErr := registry.RegisterAtMostOnceProvision(provider.adapterID, adapter); registerErr != nil {
			return nil, jobs.RuntimeActions{}, fmt.Errorf(
				"register %s provider adapter: %w", provider.providerID, registerErr)
		}
		capabilities := adapter.ProviderSafetyCapabilities()
		if capabilityErr := capabilities.Validate(provider.providerID, provider.adapterID); capabilityErr != nil {
			return nil, jobs.RuntimeActions{}, fmt.Errorf(
				"validate %s provider adapter safety capabilities: %w", provider.providerID, capabilityErr)
		}
		if !capabilities.ProvisionCandidateDiscovery {
			// Keep the adapter registered for polling and exact-handle cleanup,
			// but do not admit a create whose ambiguous outcome cannot be
			// discovered and resolved without guessing.
			enforceProviderCreateSafety(createPolicy, capabilities)
		}
		coverageCtx, cancelCoverage := context.WithTimeout(context.Background(), 5*time.Second)
		coverageErr := providercontrol.ValidateActiveCatalogCoverage(
			coverageCtx,
			database,
			[]providercontrol.CatalogCoverageRequirement{
				{
					ProviderID: provider.providerID, OfferingID: string(serverruntime.RuntimeOfferingStandard),
					AdapterID: provider.adapterID, AdapterManifestHash: provider.adapterManifestHash,
					ProvisionDispatchMode: providercontrol.ProvisionDispatchAtMostOnceManualReconcile,
				},
				{
					ProviderID: provider.providerID, OfferingID: string(serverruntime.RuntimeOfferingPremium),
					AdapterID: provider.adapterID, AdapterManifestHash: provider.adapterManifestHash,
					ProvisionDispatchMode: providercontrol.ProvisionDispatchAtMostOnceManualReconcile,
				},
			},
		)
		cancelCoverage()
		if coverageErr != nil {
			return nil, jobs.RuntimeActions{}, fmt.Errorf(
				"validate %s provider catalog coverage: %w", provider.providerID, coverageErr)
		}
		if log != nil {
			log.Info("provider_adapter_registered",
				"provider", provider.providerID,
				"adapter", provider.adapterID,
				"adapter_manifest_hash", provider.adapterManifestHash,
				"provision_dispatch_mode", providercontrol.ProvisionDispatchAtMostOnceManualReconcile,
				"candidate_discovery", capabilities.ProvisionCandidateDiscovery,
				"exact_handle_decommission", capabilities.ExactHandleDecommission,
				"definitive_absence_proof", capabilities.DefinitiveAbsenceProof,
				"host_firewall_baseline", capabilities.HostFirewallBaseline,
			)
		}
		gate = providercontrol.CertifiedMutationGate{Registry: registry}
		managedCredentialAuthorities[provider.providerID] = adapter.ManagedCredentialAuthority()
	}
	profiles, err := providercontrol.NewPostgresCatalogExecutionProfileResolver(database)
	if err != nil {
		return nil, jobs.RuntimeActions{}, fmt.Errorf("compose provider catalog resolver: %w", err)
	}
	// FailClosedEvidenceVerifier stood here and rejected every envelope, so no
	// decommission could ever seal an absence receipt and no managed-runtime
	// capacity slot was ever released. The absence verifier resolves an
	// evidence reference back to the provider read the adapter recorded, which
	// is a real check rather than a blanket refusal.
	absenceVerifier, err := providerevidence.NewVerifier(database)
	if err != nil {
		return nil, jobs.RuntimeActions{}, fmt.Errorf("compose provider absence verifier: %w", err)
	}
	asyncRequests, err := providercontrol.NewAsyncRequestStore(database)
	if err != nil {
		return nil, jobs.RuntimeActions{}, fmt.Errorf("compose provider async request store: %w", err)
	}
	const provisionResolutionSubject = "system:provider-control-worker"
	var terminalObserver func(providerexecutor.Command, providerexecutor.Receipt, providerexecutor.Receipt)
	if observer != nil {
		terminalObserver = observer(asyncRequests)
	}
	runtime, err := providercontrol.NewRuntime(providercontrol.RuntimeConfig{
		Database:                database,
		Registry:                registry,
		ProfileResolver:         profiles,
		ActivationGate:          gate,
		EvidenceVerifier:        absenceVerifier,
		TerminalFailureObserver: terminalObserver,

		OnWorkerError: func(workerErr error) {
			if log != nil {
				log.Warn("provider_control_worker_blocked", "error", workerErr)
			}
		},
		AutomaticProvisionResolutionSubject: provisionResolutionSubject,
		// A parked provision advances no further on its own. Without this the
		// stall is indistinguishable from an idle queue in the logs.
		OnWorkerParked: func(operation providercontrol.OperationRef) {
			if log != nil {
				log.Warn("provider_control_manual_reconcile_required",
					"tenant", operation.TenantID, "operation", operation.OperationID)
			}
			if onParkedOperation != nil {
				onParkedOperation(operation)
			}
		},
	})
	if err != nil {
		return nil, jobs.RuntimeActions{}, fmt.Errorf("compose provider control runtime: %w", err)
	}
	admission, err := providercontrol.NewNativeAdmission(providercontrol.NativeAdmissionConfig{
		Database: database, Coordinator: runtime.Coordinator(), ActivationGate: gate,
		ProviderCreates: createPolicy,
		CapacityPolicy:  capacityPolicy, ManagedCredentialAuthorities: managedCredentialAuthorities,
	})
	if err != nil {
		return nil, jobs.RuntimeActions{}, fmt.Errorf("compose native provider admission: %w", err)
	}
	decommissioner, err := providercontroljobs.NewNativeDecommissioner(providercontroljobs.NativeDecommissionConfig{
		Database: database, Application: runtime.Application(), Ledger: runtime.Ledger(),
		ProvisionResolution: runtime, ResolutionSubject: provisionResolutionSubject,
	})
	if err != nil {
		return nil, jobs.RuntimeActions{}, fmt.Errorf("compose native provider decommission: %w", err)
	}
	power, err := providercontroljobs.NewNativePowerController(providercontroljobs.NativePowerConfig{
		Database: database, Application: runtime.Application(),
	})
	if err != nil {
		return nil, jobs.RuntimeActions{}, fmt.Errorf("compose native provider power control: %w", err)
	}
	managedDay2.setPower(power)
	manager, err := providercontroljobs.NewManager(providercontroljobs.ManagerConfig{
		Admission: admission, ActivationGate: gate, Decommissioner: decommissioner,
		AsyncRequests: asyncRequests,
	})
	if err != nil {
		return nil, jobs.RuntimeActions{}, fmt.Errorf("compose provider-control job manager: %w", err)
	}
	staleCapacityRecovery, err := providercontroljobs.NewStaleCapacityRecovery(
		providercontroljobs.StaleCapacityRecoveryConfig{
			Database: database, Decommissioner: manager,
			OnError: func(recoveryErr error) {
				if log != nil {
					log.Warn("provider_control_stale_capacity_recovery_blocked", "error", recoveryErr)
				}
			},
		},
	)
	if err != nil {
		return nil, jobs.RuntimeActions{}, fmt.Errorf("compose stale managed capacity recovery: %w", err)
	}
	if err := runtime.RegisterAuxiliaryWorker(staleCapacityRecovery.Run); err != nil {
		return nil, jobs.RuntimeActions{}, fmt.Errorf("register stale managed capacity recovery: %w", err)
	}
	if !neverEnrolledRecoveryDisabled() {
		neverEnrolledRecovery, neverEnrolledErr := providercontroljobs.NewNeverEnrolledRecovery(
			providercontroljobs.NeverEnrolledRecoveryConfig{
				Database: database, Decommissioner: manager,
				EnrollmentWindow: neverEnrolledRecoveryWindowFromEnv(log),
				OnReap: func(candidate providercontroljobs.NeverEnrolledRecoveryCandidate) {
					if log != nil {
						log.Warn("provider_control_never_enrolled_runtime_reaping",
							"tenant_id", candidate.TenantID,
							"stack_id", candidate.StackID,
							"lease_id", candidate.LeaseID,
							"reserved_at", candidate.ReservedAt.UTC().Format(time.RFC3339),
							"age", time.Since(candidate.ReservedAt).Round(time.Minute).String(),
						)
					}
				},
				OnError: func(recoveryErr error) {
					if log != nil {
						log.Warn("provider_control_never_enrolled_runtime_recovery_blocked", "error", recoveryErr)
					}
				},
			},
		)
		if neverEnrolledErr != nil {
			return nil, jobs.RuntimeActions{}, fmt.Errorf("compose never-enrolled managed runtime recovery: %w", neverEnrolledErr)
		}
		if err := runtime.RegisterAuxiliaryWorker(neverEnrolledRecovery.Run); err != nil {
			return nil, jobs.RuntimeActions{}, fmt.Errorf("register never-enrolled managed runtime recovery: %w", err)
		}
	}
	return runtime, jobs.RuntimeActions{
		LeaseManager: manager, LeaseDecommissioner: manager,
	}, nil
}

// neverEnrolledRecoveryDisabled reports the operator kill switch. The reaper
// is enabled by default; disabling it is an incident-control action, never a
// quiet configuration.
func neverEnrolledRecoveryDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TECHSTACK_NEVER_ENROLLED_RECOVERY_DISABLED"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// neverEnrolledRecoveryWindowFromEnv reads the bounded enrollment window. An
// unparsable or out-of-range value falls back to the default with a warning
// instead of failing startup; the constructor still enforces the bounds.
func neverEnrolledRecoveryWindowFromEnv(log *logger.Logger) time.Duration {
	raw := strings.TrimSpace(os.Getenv("TECHSTACK_NEVER_ENROLLED_RECOVERY_WINDOW"))
	if raw == "" {
		return 0
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < providercontroljobs.MinimumNeverEnrolledRecoveryWindow || parsed > 24*time.Hour {
		if log != nil {
			log.Warn("never_enrolled_recovery_window_invalid", "value", raw, "fallback", "1h")
		}
		return 0
	}
	return parsed
}
