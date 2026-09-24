package monthlyruntime

import "strings"

// Operational (non-entitlement) managed-runtime failure identifiers. These ride
// in the SAME structured error envelope as ManagedRuntimeEntitlementDenialDetails
// (service.go) so every surface — 403 entitlement denial, 502 provision failure,
// stalled dashboard note, 409 decommission-blocked — parses exactly one shape.
// Unlike the entitlement denial, these are retryable and carry no feature lists.
const (
	// DecommissionBlockedUnreachableErrorCode marks a decommission blocked
	// because the runtime is unreachable (force offered).
	DecommissionBlockedUnreachableErrorCode = "decommission_blocked_unreachable"
	// DecommissionBlockedProtectedErrorCode marks a decommission blocked
	// because the lease is a protected demo anchor server.
	DecommissionBlockedProtectedErrorCode = "decommission_blocked_protected"

	// ReasonRuntimeUnreachable is the reason_code for a decommission blocked on
	// an unreachable runtime.
	ReasonRuntimeUnreachable = "runtime_unreachable"
	// ReasonLeaseProtected is the reason_code for a decommission blocked on a
	// protected demo anchor lease.
	ReasonLeaseProtected = "lease_protected"
)

type failureEnvelope struct {
	phase, phaseLabel, errorCode, reasonCode string
	capability, providerID                   string
	requiredFeatures, missingFeatures        []string
	retryable                                bool
	guidance                                 map[string]any
}

func structuredFailureDetails(spec failureEnvelope, extra map[string]any) map[string]any {
	details := map[string]any{
		"phase":             spec.phase,
		"phase_label":       spec.phaseLabel,
		"error_code":        spec.errorCode,
		"reason_code":       spec.reasonCode,
		"capability":        spec.capability,
		"required_features": compactStringSlice(spec.requiredFeatures),
		"missing_features":  compactStringSlice(spec.missingFeatures),
		"retryable":         spec.retryable,
		"user_guidance":     spec.guidance,
		"support_context": map[string]any{
			"feature_source": "Stripe/FGA/Flagship entitlement chain",
			"cost_bearing":   true,
		},
	}
	if spec.providerID != "" {
		details["provider_id"] = spec.providerID
	}
	for key, value := range extra {
		if _, canonical := details[key]; !canonical {
			details[key] = value
		}
	}
	return details
}

func managedRuntimeProviderID(providerID string) string {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		providerID = ProviderCentron
	}
	return providerID
}

// DecommissionProtectedDetails is the structured envelope returned (409) when a
// decommission is refused because the lease is one of the protected demo
// anchor servers. Not retryable and no force offer: the protection is
// deliberate operator configuration, not a transient condition.
func DecommissionProtectedDetails(providerID, leaseID string) map[string]any {
	extra := map[string]any{"force_offered": false}
	if id := strings.TrimSpace(leaseID); id != "" {
		extra["lease_id"] = id
	}
	return structuredFailureDetails(failureEnvelope{
		phase: "managed_runtime_decommission", phaseLabel: "Managed runtime decommission",
		errorCode: DecommissionBlockedProtectedErrorCode, reasonCode: ReasonLeaseProtected,
		capability: ManagedRuntimeCapability, providerID: managedRuntimeProviderID(providerID),
		guidance: map[string]any{
			"title": "This server is part of the kombify live demo",
			"body":  "The demo's anchor servers are protected and cannot be decommissioned. If this account currently has managed-server capacity, use the add-server flow instead.",
			"next_steps": []string{
				"Try the add-server flow if managed-server capacity is available for this account.",
			},
		},
	}, extra)
}

// DecommissionUnreachableDetails is the structured envelope returned (409) when
// a decommission is blocked because the runtime is unreachable. When
// forceOffered is true it points the caller at force=true, which cancels the
// lease and reconciles the provider resource in the background.
func DecommissionUnreachableDetails(providerID, leaseID string, forceOffered bool) map[string]any {
	extra := map[string]any{"force_offered": forceOffered}
	if id := strings.TrimSpace(leaseID); id != "" {
		extra["lease_id"] = id
	}
	nextSteps := []string{"Check whether the runtime is reachable and retry the decommission."}
	body := "The managed runtime did not respond to the decommission request. Forced decommission is unavailable until durable provider reconciliation is ready."
	if forceOffered {
		nextSteps = append(nextSteps, "Force decommission to cancel the lease; the provider resource is reconciled in the background so it does not keep billing.")
		body = "The managed runtime did not respond to the decommission request. Force decommission cancels the lease and reconciles the underlying provider resource in the background."
	}
	return structuredFailureDetails(failureEnvelope{
		phase: "managed_runtime_decommission", phaseLabel: "Managed runtime decommission",
		errorCode: DecommissionBlockedUnreachableErrorCode, reasonCode: ReasonRuntimeUnreachable,
		capability: ManagedRuntimeCapability, providerID: managedRuntimeProviderID(providerID), retryable: true,
		guidance: map[string]any{
			"title":      "Cannot reach the server to decommission it",
			"body":       body,
			"next_steps": nextSteps,
		},
	}, extra)
}
