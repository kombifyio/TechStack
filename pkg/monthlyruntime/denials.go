package monthlyruntime

import "strings"

// Operational (non-entitlement) managed-runtime failure identifiers. These ride
// in the SAME structured error envelope as ManagedRuntimeEntitlementDenialDetails
// (service.go) so every surface — 403 entitlement denial, 502 provision failure,
// stalled dashboard note, 409 decommission-blocked — parses exactly one shape.
// Unlike the entitlement denial, these are retryable and carry no feature lists.
const (
	// ManagedRuntimeProvisionFailedErrorCode marks an add-server / provisioning
	// request that failed after its job was created.
	ManagedRuntimeProvisionFailedErrorCode = "managed_runtime_provision_failed"
	// RuntimeEnrollmentStalledErrorCode marks a runtime pending enrollment past
	// EnrollmentStalledThreshold.
	RuntimeEnrollmentStalledErrorCode = "runtime_enrollment_stalled"
	// DecommissionBlockedUnreachableErrorCode marks a decommission blocked
	// because the runtime is unreachable (force offered).
	DecommissionBlockedUnreachableErrorCode = "decommission_blocked_unreachable"
	// DecommissionBlockedProtectedErrorCode marks a decommission blocked
	// because the lease is a protected demo anchor server.
	DecommissionBlockedProtectedErrorCode = "decommission_blocked_protected"

	// ReasonProvisionRequestFailed is the default reason_code when a provision
	// failure is not further classified by the caller.
	ReasonProvisionRequestFailed = "provision_request_failed"
	// ReasonEnrollmentStalled is the reason_code for a stalled enrollment.
	ReasonEnrollmentStalled = "enrollment_stalled"
	// ReasonRuntimeUnreachable is the reason_code for a decommission blocked on
	// an unreachable runtime.
	ReasonRuntimeUnreachable = "runtime_unreachable"
	// ReasonLeaseProtected is the reason_code for a decommission blocked on a
	// protected demo anchor lease.
	ReasonLeaseProtected = "lease_protected"
)

// managedRuntimeFailureDetails builds the canonical structured error envelope
// shared by every managed-runtime operational failure. It mirrors the key set
// of ManagedRuntimeEntitlementDenialDetails so a single frontend / OpenAPI
// shape covers entitlement denials and lifecycle failures alike. Operational
// failures carry no entitlement feature lists and are (unless overridden)
// retryable.
func managedRuntimeFailureDetails(phase, phaseLabel, errorCode, reasonCode, providerID string, retryable bool, guidance, extra map[string]any) map[string]any {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		providerID = ProviderCentron
	}
	details := map[string]any{
		"phase":             phase,
		"phase_label":       phaseLabel,
		"error_code":        errorCode,
		"reason_code":       reasonCode,
		"capability":        ManagedRuntimeCapability,
		"provider_id":       providerID,
		"required_features": []string{},
		"missing_features":  []string{},
		"retryable":         retryable,
		"user_guidance":     guidance,
		"support_context": map[string]any{
			"feature_source": "Stripe/FGA/Flagship entitlement chain",
			"cost_bearing":   true,
		},
	}
	for k, v := range extra {
		details[k] = v
	}
	return details
}

// ManagedRuntimeProvisionFailureDetails is the structured envelope returned when
// an add-server / provisioning request fails after its job was created. It is
// retryable and carries the job_id so the client can open creation progress and
// offer a retry. reasonCode may be supplied by a caller that classified the
// failure; it defaults to ReasonProvisionRequestFailed.
func ManagedRuntimeProvisionFailureDetails(providerID, jobID, reasonCode string, err error) map[string]any {
	if strings.TrimSpace(reasonCode) == "" {
		reasonCode = ReasonProvisionRequestFailed
	}
	extra := map[string]any{}
	if id := strings.TrimSpace(jobID); id != "" {
		extra["job_id"] = id
	}
	if err != nil {
		extra["error_details"] = err.Error()
	}
	return managedRuntimeFailureDetails(
		"managed_runtime_provision",
		"Managed runtime provisioning",
		ManagedRuntimeProvisionFailedErrorCode,
		reasonCode,
		providerID,
		true,
		map[string]any{
			"title": "Managed server request failed",
			"body":  "The managed runtime server could not be provisioned. Its creation job captured the failure; open creation progress to see the detail and retry.",
			"next_steps": []string{
				"Open creation progress to see the failure detail.",
				"Retry the server request.",
				"If it keeps failing, contact kombify support with the job id.",
			},
		},
		extra,
	)
}

// StalledEnrollmentDetails is the structured envelope surfaced on the dashboard
// when a managed runtime has been pending enrollment past
// EnrollmentStalledThreshold. It guides the owner to reconnect the runtime.
func StalledEnrollmentDetails(providerID, leaseID string) map[string]any {
	extra := map[string]any{}
	if id := strings.TrimSpace(leaseID); id != "" {
		extra["lease_id"] = id
	}
	return managedRuntimeFailureDetails(
		"managed_runtime_enrollment",
		"Managed runtime enrollment",
		RuntimeEnrollmentStalledErrorCode,
		ReasonEnrollmentStalled,
		providerID,
		true,
		map[string]any{
			"title": "Managed server is taking longer than expected",
			"body":  "This managed runtime has been pending enrollment longer than expected. Reconnecting re-runs the enrollment probe.",
			"next_steps": []string{
				"Use Reconnect to re-run enrollment.",
				"If reconnect does not recover it, decommission and recreate the server.",
			},
		},
		extra,
	)
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
	return managedRuntimeFailureDetails(
		"managed_runtime_decommission",
		"Managed runtime decommission",
		DecommissionBlockedProtectedErrorCode,
		ReasonLeaseProtected,
		providerID,
		false,
		map[string]any{
			"title": "This server is part of the kombify live demo",
			"body":  "The demo's anchor servers are protected and cannot be decommissioned. If this account currently has managed-server capacity, use the add-server flow instead.",
			"next_steps": []string{
				"Try the add-server flow if managed-server capacity is available for this account.",
			},
		},
		extra,
	)
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
	return managedRuntimeFailureDetails(
		"managed_runtime_decommission",
		"Managed runtime decommission",
		DecommissionBlockedUnreachableErrorCode,
		ReasonRuntimeUnreachable,
		providerID,
		true,
		map[string]any{
			"title":      "Cannot reach the server to decommission it",
			"body":       body,
			"next_steps": nextSteps,
		},
		extra,
	)
}
