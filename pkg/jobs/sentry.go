package jobs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/providererrors"
	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/getsentry/sentry-go"
)

const (
	maxSentryTextBytes = 2 * 1024
	sentryValueField   = "value"
	sentryStackField   = "stack"
	sentryMessageField = "message"
	sentryErrorField   = "error"
	nodeTargetType     = "node"
	serverIDField      = "server_id"
	serviceIDField     = "service_id"
	stackKitField      = "stack_kit"
)

var (
	sentryURLPattern   = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>]+`)
	sentryEmailPattern = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)
	sentryIPv4Pattern  = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
)

// withJobSentryScope clones a Sentry hub for the duration of one job execution,
// sets job-specific tags, captures panics, and returns a finisher that captures
// any returned error. With an empty SENTRY_DSN this is a cheap no-op.
//
// Usage:
//
//	ctx, finish := withJobSentryScope(ctx, job)
//	defer func() { finish(err) }()
func withJobSentryScope(ctx context.Context, job *Job) (context.Context, func(error)) {
	hub := sentry.CurrentHub().Clone()
	initial := job.Snapshot()
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag("component", "jobs")
		scope.SetTag("job_type", string(initial.Type))
		scope.SetTag("job_id", initial.ID)
		scope.SetTag("transaction", fmt.Sprintf("job.%s", initial.Type))
		scope.SetContext("job", jobSentryContextSnapshot(initial))
		applyJobCorrelationTags(scope, job)
	})
	ctx = sentry.SetHubOnContext(ctx, hub)
	hub.AddBreadcrumb(&sentry.Breadcrumb{
		Type:     "default",
		Category: "job.start",
		Message:  fmt.Sprintf("job %s started", initial.ID),
		Level:    sentry.LevelInfo,
		Data: map[string]interface{}{
			"job_id":   initial.ID,
			"job_type": string(initial.Type),
			"target":   initial.TargetID,
		},
	}, nil)
	finish := func(err error) {
		if rec := recover(); rec != nil {
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetContext("panic", sentry.Context{
					sentryValueField: safeSentryText(fmt.Sprintf("%v", rec)),
					sentryStackField: string(debug.Stack()),
				})
				scope.SetLevel(sentry.LevelFatal)
				hub.CaptureMessage("job_panic")
			})
			flushSentryHub(hub)
			panic(rec)
		}
		if err != nil {
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetContext("job", jobSentryContext(job))
				applyJobCorrelationTags(scope, job)
				if pe, ok := err.(*ProvisionError); ok {
					scope.SetTag("failed_step", pe.Step)
					applyProviderErrorScope(scope, providererrors.ClassifyMessage(pe.Message+"\n"+pe.Details))
					scope.SetContext("provision_error", sentry.Context{
						stepField:          pe.Step,
						sentryMessageField: safeSentryText(pe.Message),
						"details":          safeSentryText(pe.Details),
					})
				} else {
					applyProviderErrorScope(scope, providererrors.Classify(err))
				}
				hub.CaptureException(errors.New(safeSentryText(err.Error())))
			})
			flushSentryHub(hub)
		}
	}
	return ctx, finish
}

// breadcrumbStep records a job-step transition so the Sentry timeline shows
// exactly which step ran (and how long it took to start) before any failure.
// data is optional context shown on the breadcrumb (e.g. lease_id, provider).
func breadcrumbStep(ctx context.Context, stepID, message string, data map[string]interface{}) {
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		return
	}
	payload := safeJobCorrelationContext(data)
	payload[stepField] = safeSentryText(stepID)
	hub.AddBreadcrumb(&sentry.Breadcrumb{
		Type:     "default",
		Category: "job.step",
		Message:  safeSentryText(fmt.Sprintf("%s: %s", stepID, message)),
		Level:    sentry.LevelInfo,
		Data:     payload,
	}, nil)
}

// breadcrumbWait records progress while a step is blocked waiting on an
// external system (currently: the managed VM lease enrollment). Without this
// a 5-minute hang shows up in Sentry as a single transition with no detail.
func breadcrumbWait(ctx context.Context, stepID string, waited time.Duration, message string) {
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		return
	}
	hub.AddBreadcrumb(&sentry.Breadcrumb{
		Type:     "default",
		Category: "job.wait",
		Message:  safeSentryText(message),
		Level:    sentry.LevelInfo,
		Data: map[string]interface{}{
			stepField:       stepID,
			"waited_ms":     waited.Milliseconds(),
			"waited_pretty": waited.Truncate(time.Second).String(),
		},
	}, nil)
}

// captureJobError records an error with extra context but lets the caller keep
// returning the error to the queue. Use this for partial / mid-step failures
// (e.g. a lease resolver error during a wait loop) that should be visible in
// Sentry even when the step ultimately recovers and continues.
func captureJobError(ctx context.Context, err error, extra map[string]interface{}) {
	if err == nil {
		return
	}
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		return
	}
	hub.WithScope(func(scope *sentry.Scope) {
		if len(extra) > 0 {
			safeExtra := safeJobCorrelationContext(extra)
			scope.SetContext("job", sentry.Context(safeExtra))
			for _, key := range []string{stepField, leaseIDField, stackIDField, tenantIDField, providerField, reasonField} {
				if value, ok := safeExtra[key]; ok && value != nil {
					scope.SetTag(key, fmt.Sprintf("%v", value))
				}
			}
		}
		applyProviderErrorScope(scope, providererrors.Classify(err))
		hub.CaptureException(errors.New(safeSentryText(err.Error())))
	})
	flushSentryHub(hub)
}

func flushSentryHub(hub *sentry.Hub) {
	if hub == nil || hub.Client() == nil {
		return
	}
	hub.Flush(2 * time.Second)
}

func jobSentryContext(job *Job) sentry.Context {
	if job == nil {
		return sentry.Context{}
	}
	return jobSentryContextSnapshot(job.Snapshot())
}

func jobSentryContextSnapshot(job JobSnapshot) sentry.Context {
	ctx := sentry.Context{
		"job_id":           job.ID,
		"job_type":         string(job.Type),
		"target_type":      job.TargetType,
		"target_id":        job.TargetID,
		"state":            string(job.State),
		"step":             job.Step,
		sentryMessageField: safeSentryText(job.Message),
		"progress":         job.Progress,
		"attempts":         job.Attempts,
		"max_attempts":     job.MaxAttempts,
		sentryErrorField:   safeSentryText(job.Error),
		"error_details":    safeSentryText(job.ErrorDetails),
		"created_at":       job.CreatedAt,
		"started_at":       job.StartedAt,
		"completed_at":     job.CompletedAt,
	}
	for key, value := range jobCorrelationContextSnapshot(job) {
		ctx[key] = value
	}
	if len(job.Logs) > 0 {
		last := job.Logs[len(job.Logs)-1]
		ctx["last_log"] = sentry.Context{
			"timestamp":        last.Timestamp,
			"level":            last.Level,
			sentryMessageField: safeSentryText(last.Message),
		}
		ctx["log_count"] = len(job.Logs)
	}
	return ctx
}

func applyJobCorrelationTags(scope *sentry.Scope, job *Job) {
	if scope == nil || job == nil {
		return
	}
	for key, value := range jobCorrelationContext(job) {
		scope.SetTag(key, fmt.Sprintf("%v", value))
	}
}

func jobCorrelationContext(job *Job) map[string]interface{} {
	if job == nil {
		return nil
	}
	return jobCorrelationContextSnapshot(job.Snapshot())
}

func jobCorrelationContextSnapshot(job JobSnapshot) map[string]interface{} {
	values := map[string]interface{}{}
	targetType := strings.ToLower(strings.TrimSpace(job.TargetType))
	switch targetType {
	case "stack":
		values[stackIDField] = job.TargetID
	case nodeTargetType, "server":
		values[serverIDField] = job.TargetID
	case "service":
		values[serviceIDField] = job.TargetID
	}
	for _, key := range []string{
		stackIDField, serverIDField, serviceIDField, leaseIDField, tenantIDField,
		providerField, reasonField, "reason_code", "runtime_action_id", "request_id",
		"runtime_phase", "verification_status",
	} {
		if _, exists := values[key]; exists {
			continue
		}
		if value := firstNonEmpty(stringFromMap(job.Result, key), stringFromMap(job.Payload, key)); value != "" {
			values[key] = safeSentryText(value)
		}
	}
	for key, value := range values {
		if strings.TrimSpace(fmt.Sprintf("%v", value)) == "" {
			delete(values, key)
		}
	}
	return values
}

func safeJobCorrelationContext(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return map[string]interface{}{}
	}
	allowed := map[string]bool{
		stepField: true, stackIDField: true, serverIDField: true, serviceIDField: true,
		leaseIDField: true, tenantIDField: true, providerField: true, reasonField: true,
		"reason_code": true, "runtime_action_id": true, "request_id": true,
		"runtime_phase": true, "verification_status": true, stackKitField: true,
		"waited_ms": true, "waited_pretty": true,
	}
	result := map[string]interface{}{}
	for key, value := range input {
		if !allowed[key] || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			result[key] = safeSentryText(typed)
		case bool, int, int32, int64, uint, uint32, uint64, float32, float64:
			result[key] = typed
		default:
			result[key] = safeSentryText(fmt.Sprintf("%v", typed))
		}
	}
	return result
}

func safeSentryText(value string) string {
	value = secrets.Redact(strings.TrimSpace(value))
	value = sentryURLPattern.ReplaceAllString(value, "[url]")
	value = sentryEmailPattern.ReplaceAllString(value, "[email]")
	value = sentryIPv4Pattern.ReplaceAllString(value, "[ip]")
	if len(value) > maxSentryTextBytes {
		value = value[:maxSentryTextBytes] + "... [truncated]"
	}
	return value
}

func applyProviderErrorScope(scope *sentry.Scope, info providererrors.Info) {
	if scope == nil || !info.HasSignal() {
		return
	}
	if info.Provider != "" {
		scope.SetTag("provider_id", info.Provider)
	}
	if info.Code != "" {
		scope.SetTag("provider_error_code", info.Code)
	}
	if info.Category != "" {
		scope.SetTag("provider_error_category", info.Category)
	}
	if info.RetryHint != "" {
		scope.SetTag("provider_retry_hint", info.RetryHint)
	}
	scope.SetContext("provider_error", sentry.Context{
		"provider_id":          info.Provider,
		"provider_error_code":  info.Code,
		"category":             info.Category,
		"retry_hint":           info.RetryHint,
		"summary":              safeSentryText(info.Summary),
		"terminal_for_attempt": info.Terminal,
	})
}
