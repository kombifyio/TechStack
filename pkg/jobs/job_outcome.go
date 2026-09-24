package jobs

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/outcome"
)

type jobOutcomeIdentity struct {
	ID, TargetType, TargetID, Step, ProviderID, RequestID string
	Type                                                  JobType
}

func (q *Queue) recordJobOutcome(job *Job, decision outcome.Decision) {
	if err := setJobOutcome(job, decision); err != nil {
		q.log.Error("job_outcome_rejected", "id", job.ID, "error", err)
	}
}

func setJobOutcome(job *Job, decision outcome.Decision) error {
	normalized, err := outcome.Normalize(decision, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("normalize job outcome: %w", err)
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.Result == nil {
		job.Result = make(map[string]interface{})
	}
	job.Result["last_outcome"] = normalized
	for _, key := range []string{
		"error_code", "reason_code", "capability", "provider_id", "required_features",
		"missing_features", "user_guidance", "remediation", "provider_diagnostics",
		"request_id", "support_context", "retry_after",
	} {
		delete(job.Result, key)
	}
	job.Result["retryable"] = normalized.Retryable
	if normalized.RetryAfter != nil {
		job.Result["retry_after"] = normalized.RetryAfter.UTC().Format(time.RFC3339)
	}
	if normalized.ErrorCode != "" {
		job.Result["error_code"] = normalized.ErrorCode
	}
	if normalized.ReasonCode != "" {
		job.Result["reason_code"] = normalized.ReasonCode
	}
	if normalized.Capability != "" {
		job.Result["capability"] = normalized.Capability
	}
	if normalized.ProviderID != "" {
		job.Result["provider_id"] = normalized.ProviderID
	}
	if len(normalized.RequiredFeatures) > 0 {
		job.Result["required_features"] = normalized.RequiredFeatures
	}
	if len(normalized.MissingFeatures) > 0 {
		job.Result["missing_features"] = normalized.MissingFeatures
	}
	if normalized.UserGuidance != nil {
		job.Result["user_guidance"] = normalized.UserGuidance
	}
	if normalized.Remediation != "" {
		job.Result["remediation"] = normalized.Remediation
	}
	if len(normalized.ProviderDiagnostics) > 0 {
		job.Result["provider_diagnostics"] = normalized.ProviderDiagnostics
	}
	if normalized.RequestID != "" {
		job.Result["request_id"] = normalized.RequestID
	}
	if len(normalized.SupportContext) > 0 {
		job.Result["support_context"] = normalized.SupportContext
	}
	return nil
}

func jobOutcomeIdentityOf(job *Job) jobOutcomeIdentity {
	job.mu.RLock()
	defer job.mu.RUnlock()
	return jobOutcomeIdentity{
		ID: job.ID, Type: job.Type, TargetType: job.TargetType, TargetID: job.TargetID, Step: job.Step,
		ProviderID: firstJobString(job.Payload, job.Result, "provider_id"),
		RequestID:  firstJobString(job.Payload, job.Result, "request_id"),
	}
}

func (identity jobOutcomeIdentity) supportContext(extra map[string]any) map[string]any {
	context := map[string]any{
		"job_id": identity.ID, "job_type": string(identity.Type),
		"target_type": identity.TargetType, "target_id": identity.TargetID,
	}
	if identity.Step != "" {
		context["step"] = identity.Step
	}
	for key, value := range extra {
		context[key] = value
	}
	return context
}

func jobPendingOutcome(job *Job, reasonCode string, extra map[string]any) outcome.Decision {
	identity := jobOutcomeIdentityOf(job)
	title, body, step := pendingJobGuidance(reasonCode)
	return outcome.Decision{
		Status: outcome.StatusPending, ReasonCode: reasonCode,
		Capability: jobOutcomeCapability(identity.Type), ProviderID: identity.ProviderID, Retryable: false,
		UserGuidance: &outcome.Guidance{
			Title: title, Body: body,
			NextSteps: []outcome.Step{step},
		},
		RequestID: identity.RequestID, SupportContext: identity.supportContext(extra),
	}
}

func jobFailedOutcome(job *Job, reasonCode string, retryable bool, extra map[string]any) outcome.Decision {
	identity := jobOutcomeIdentityOf(job)
	step := outcome.Step{ID: "open-support", Label: "Open support with this job context", Kind: "handoff"}
	body := "The operation stopped before completion. The job and resource identifiers below can be handed to support without exposing credentials."
	if retryable {
		step = outcome.Step{ID: "retry-operation", Label: "Retry the operation", Kind: "retry"}
		body = "The operation stopped after a temporary failure. Retrying is safe from this job state."
	}
	return outcome.Decision{
		Status: outcome.StatusFailed, ErrorCode: "JOB_EXECUTION_FAILED", ReasonCode: reasonCode,
		Capability: jobOutcomeCapability(identity.Type), ProviderID: identity.ProviderID, Retryable: retryable,
		UserGuidance: &outcome.Guidance{
			Title: "The operation needs attention", Body: body, NextSteps: []outcome.Step{step},
		},
		RequestID: identity.RequestID, SupportContext: identity.supportContext(extra),
	}
}

// provisionFailureOutcome keeps the generic provision reason unless the
// StackKits rollout reported a certificate authority rate limit. That refusal
// has one honest next step -- retry after the authority's named time -- and a
// new server for the same address would request the same certificate again.
func provisionFailureOutcome(job *Job, provisionErr *ProvisionError) outcome.Decision {
	extra := map[string]any{"failed_step": provisionErr.Step}
	var operationErr *typedStackKitOperationError
	if !errors.As(provisionErr, &operationErr) || operationErr.Failure.ReasonCode == "" {
		return jobFailedOutcome(job, "provision_failed", false, extra)
	}
	failure := operationErr.Failure
	extra["stackkit_reason_code"] = failure.ReasonCode
	if failure.ReasonCode != StackKitReasonACMERateLimited || !failure.Retryable || failure.RetryAfter.IsZero() {
		return jobFailedOutcome(job, "provision_failed", false, extra)
	}
	retryAfter := failure.RetryAfter.UTC()
	decision := jobFailedOutcome(job, "stackkit_"+StackKitReasonACMERateLimited, true, extra)
	decision.RetryAfter = &retryAfter
	when := retryAfter.Format(time.RFC3339)
	decision.UserGuidance = &outcome.Guidance{
		Title: "The certificate authority is rate-limiting this address",
		Body: "The public certificate authority refused new certificates for this address until " + when +
			". Retrying the rollout earlier, or creating a new server for the same address, is refused again.",
		NextSteps: []outcome.Step{{ID: "retry-after-certificate-authority", Label: "Retry the rollout after " + when, Kind: "retry"}},
	}
	return decision
}

func jobAvailableOutcome(job *Job) outcome.Decision {
	identity := jobOutcomeIdentityOf(job)
	return outcome.Decision{
		Status: outcome.StatusAvailable, Capability: jobOutcomeCapability(identity.Type),
		ProviderID: identity.ProviderID, Retryable: false, RequestID: identity.RequestID,
		SupportContext: identity.supportContext(nil),
	}
}

func pendingJobGuidance(reasonCode string) (string, string, outcome.Step) {
	switch strings.TrimSpace(reasonCode) {
	case WaitReasonManagedRuntimeProvider:
		return "The provider is still creating the server",
			"The durable provider operation is still in progress. Starting another request could create a duplicate resource.",
			outcome.Step{ID: "wait-provider", Label: "Keep this operation open; it resumes automatically", Kind: "note"}
	case WaitReasonManagedRuntimeEnrollment, WaitReasonCanonicalGuardEvidence:
		return "The server connection is still being verified",
			"Techstack is waiting for authenticated Guard evidence from the exact server before continuing.",
			outcome.Step{ID: "keep-server-online", Label: "Keep the server online; the operation resumes automatically", Kind: "connect"}
	case WaitReasonRetryBackoff:
		return "The operation will retry automatically",
			"A temporary dependency failed. Techstack retained the job and scheduled the next bounded attempt.",
			outcome.Step{ID: "wait-retry", Label: "No action is required while the retry is scheduled", Kind: "note"}
	default:
		return "The operation is still in progress", "The operation is waiting for a dependency and will continue automatically.",
			outcome.Step{ID: "wait-dependency", Label: "Keep this operation open for the next update", Kind: "note"}
	}
}

func jobOutcomeCapability(jobType JobType) string {
	switch jobType {
	case JobTypeProvision:
		return "techstack.managed.runtime.provision"
	case JobTypeDeploy:
		return "techstack.stackkit.deploy"
	case JobTypeDestroy, JobTypeReconcileLease:
		return "techstack.managed.runtime.decommission"
	case JobTypeDriftCheck, JobTypeDriftResolve:
		return "techstack.runtime.drift"
	case JobTypeCommand:
		return "techstack.runtime.command"
	case JobTypeStackKitLifecycle:
		return "techstack.stackkit.lifecycle"
	default:
		return "techstack.job.execute"
	}
}

func jobCompletionOutcomeIsActionable(job *Job) bool {
	job.mu.RLock()
	defer job.mu.RUnlock()
	value, ok := job.Result["last_outcome"]
	if !ok {
		return false
	}
	switch decision := value.(type) {
	case outcome.Decision:
		return decision.Status != outcome.StatusAvailable && decision.Status != outcome.StatusPending
	case *outcome.Decision:
		return decision != nil && decision.Status != outcome.StatusAvailable && decision.Status != outcome.StatusPending
	default:
		return false
	}
}
