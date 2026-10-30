package orchestrator

import (
	"github.com/kombifyio/techstack/pkg/failureknowledge"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// failureOccurrenceForJob normalizes the redacted receipt of a failed job into
// its class A failure occurrence (knowledge-search plan section 5.8). Only
// fields the job already records are used; unknown context stays empty.
func failureOccurrenceForJob(job jobs.JobSnapshot) *failureknowledge.Occurrence {
	if job.State != jobs.JobStateFailed {
		return nil
	}
	support, _ := job.Result["support_context"].(map[string]any)
	occurrence := failureknowledge.Normalize(failureknowledge.Input{
		Component: firstNonEmptyString(stringFromAnyMap(job.Result, "capability"), "techstack.job."+string(job.Type)),
		Phase:     firstNonEmptyString(job.Step, stringFromAnyMap(support, "failed_step")),
		ErrorCode: firstNonEmptyString(
			stringFromAnyMap(support, "stackkit_reason_code"),
			stringFromAnyMap(job.Result, "reason_code"),
			stringFromAnyMap(job.Result, "error_code"),
		),
		Text: firstNonEmptyString(job.Error, job.ErrorDetails),
		Context: failureknowledge.Context{
			StackKitModule:  firstNonEmptyString(firstNonEmptyJobString(job, "stackkit"), firstNonEmptyJobString(job, "stackkit_catalog_ref")),
			StackKitVersion: firstNonEmptyJobString(job, "stackkit_version"),
			Provider:        firstNonEmptyString(firstNonEmptyJobString(job, "lease_provider"), firstNonEmptyJobString(job, "provider_id")),
		},
	})
	return &occurrence
}
