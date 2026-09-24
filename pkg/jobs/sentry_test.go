package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// All helpers must be safe no-ops when SENTRY_DSN is unset (self-hosted /
// dev). These tests run with a default no-op hub and confirm that the helpers
// neither panic nor mutate observable state when there is no DSN.

func TestWithJobSentryScope_NoopHub(t *testing.T) {
	t.Parallel()
	job := &Job{ID: "job-1", Type: JobTypeProvision, TargetID: "stack-1", TargetName: "techstack"}
	ctx, finish := withJobSentryScope(context.Background(), job)
	if sentry.GetHubFromContext(ctx) == nil {
		t.Fatalf("expected hub to be set on context")
	}
	// finish with nil err must not panic
	finish(nil)
	// finish with an error must not panic
	finish(errors.New("boom"))
	// finish with a ProvisionError must capture failed_step without panic
	finish(&ProvisionError{Step: StepCreateLease, Message: "lease boom", Details: "details"})
}

func TestWithJobSentryScope_CapturesAndFlushesJobContext(t *testing.T) {
	transport := &recordingJobSentryTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.com/1",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("new sentry client: %v", err)
	}
	previousClient := sentry.CurrentHub().Client()
	sentry.CurrentHub().BindClient(client)
	defer sentry.CurrentHub().BindClient(previousClient)

	job := &Job{
		ID:          "job-1",
		Type:        JobTypeDeploy,
		TargetType:  "stack",
		TargetID:    "stack-1",
		TargetName:  "techstack",
		State:       JobStateRunning,
		Step:        StepCreateLease,
		Message:     "Waiting for managed VM lease enrollment...",
		Progress:    10,
		Attempts:    1,
		MaxAttempts: 3,
		Result: map[string]interface{}{
			"lease_id":         "lease-stack-1",
			"lease_provider":   "ionos-managed",
			"runtime_ssh_user": "root",
			"target_bootstrap": map[string]interface{}{"reason": "target_bootstrap_ssh_auth_failed"},
		},
	}

	_, finish := withJobSentryScope(context.Background(), job)
	finish(&ProvisionError{
		Step:    StepCreateLease,
		Message: "managed runtime lease target was not available health:stackkits-cloud-host-security-local",
		Details: "monthlyruntime: monthly runtime feature disabled",
	})

	if len(transport.events) != 1 {
		t.Fatalf("expected one sentry event, got %d", len(transport.events))
	}
	if !transport.flushed {
		t.Fatalf("expected sentry flush")
	}
	event := transport.events[0]
	if got := event.Tags["job_id"]; got != "job-1" {
		t.Fatalf("job_id tag = %q", got)
	}
	if got := event.Tags["failed_step"]; got != StepCreateLease {
		t.Fatalf("failed_step tag = %q", got)
	}
	if got := event.Tags["provider"]; got != "ionos-managed" {
		t.Fatalf("provider tag = %q", got)
	}
	if got := event.Tags["ssh_user"]; got != "root" {
		t.Fatalf("ssh_user tag = %q", got)
	}
	if got := event.Tags["reason_code"]; got != "target_bootstrap_ssh_auth_failed" {
		t.Fatalf("reason_code tag = %q", got)
	}
	if got := event.Tags["health_target"]; got != "stackkits-cloud-host-security-local" {
		t.Fatalf("health_target tag = %q", got)
	}
	jobCtx := event.Contexts["job"]
	if got := jobCtx["job_id"]; got != "job-1" {
		t.Fatalf("job context job_id = %v", got)
	}
	if got := jobCtx["target_id"]; got != "stack-1" {
		t.Fatalf("job context target_id = %v", got)
	}
	provisionCtx := event.Contexts["provision_error"]
	if got := provisionCtx["details"]; got != "monthlyruntime: monthly runtime feature disabled" {
		t.Fatalf("provision details = %v", got)
	}
}

func TestWithJobSentryScope_RedactsPayloadResultLogsAndPrivateTargets(t *testing.T) {
	transport := &recordingJobSentryTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.com/1",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("new sentry client: %v", err)
	}
	previousClient := sentry.CurrentHub().Client()
	sentry.CurrentHub().BindClient(client)
	defer sentry.CurrentHub().BindClient(previousClient)

	payloadSecret := strings.Join([]string{"payload", "secret"}, "-")
	resultSecret := strings.Join([]string{"result", "secret"}, "-")
	sensitivePassword := strings.Join([]string{"super", "secret"}, "")
	privateIP := strings.Join([]string{"10", "0", "0", "4"}, ".")
	privateBaseURL := "https://" + privateIP
	bearerValue := strings.Join([]string{"abcdefghijkl", "mnopqrstuvwxyz"}, "")
	job := &Job{
		ID: "job-sensitive", Type: JobTypeDeploy, TargetType: "stack", TargetID: "stack-1",
		State: JobStateRunning, Step: StepCreateLease,
		Payload: map[string]interface{}{
			"password":  payloadSecret,
			"server_id": "server-1",
		},
		Result: map[string]interface{}{
			"api_token":   resultSecret,
			"service_id":  "service-1",
			"reason_code": "server_offline",
		},
		Logs: []LogEntry{{Level: "error", Message: "Bearer " + bearerValue + " at " + privateBaseURL + "/private"}},
	}

	_, finish := withJobSentryScope(context.Background(), job)
	finish(errors.New("password=" + sensitivePassword + " on " + privateBaseURL + "/admin"))

	if len(transport.events) != 1 {
		t.Fatalf("expected one sentry event, got %d", len(transport.events))
	}
	encoded, err := json.Marshal(transport.events[0])
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	text := string(encoded)
	for _, forbidden := range []string{payloadSecret, resultSecret, sensitivePassword, privateIP, bearerValue} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sentry event leaked %q: %s", forbidden, text)
		}
	}
	if got := transport.events[0].Tags["server_id"]; got != "server-1" {
		t.Fatalf("server_id tag = %q", got)
	}
	if got := transport.events[0].Tags["service_id"]; got != "service-1" {
		t.Fatalf("service_id tag = %q", got)
	}
	if got := transport.events[0].Tags["reason_code"]; got != "server_offline" {
		t.Fatalf("reason_code tag = %q", got)
	}
	jobContext := transport.events[0].Contexts["job"]
	if _, ok := jobContext["payload"]; ok {
		t.Fatalf("raw payload must not be sent to sentry")
	}
	if _, ok := jobContext["result"]; ok {
		t.Fatalf("raw result must not be sent to sentry")
	}
}

type recordingJobSentryTransport struct {
	events  []*sentry.Event
	flushed bool
}

func (t *recordingJobSentryTransport) Configure(sentry.ClientOptions) {}

func (t *recordingJobSentryTransport) SendEvent(event *sentry.Event) {
	t.events = append(t.events, event)
}

func (t *recordingJobSentryTransport) Flush(time.Duration) bool {
	t.flushed = true
	return true
}

func (t *recordingJobSentryTransport) FlushWithContext(context.Context) bool {
	t.flushed = true
	return true
}

func (t *recordingJobSentryTransport) Close() {}

func TestParseEnvDuration(t *testing.T) {
	t.Setenv("TECHSTACK_TEST_DURATION_VALID", "5m")
	if got := parseEnvDuration("TECHSTACK_TEST_DURATION_VALID"); got != 5*time.Minute {
		t.Fatalf("expected 5m, got %v", got)
	}
	t.Setenv("TECHSTACK_TEST_DURATION_BAD", "not-a-duration")
	if got := parseEnvDuration("TECHSTACK_TEST_DURATION_BAD"); got != 0 {
		t.Fatalf("expected 0 for bad value, got %v", got)
	}
	if got := parseEnvDuration("TECHSTACK_TEST_DURATION_UNSET"); got != 0 {
		t.Fatalf("expected 0 for unset, got %v", got)
	}
	t.Setenv("TECHSTACK_TEST_DURATION_NEG", "-1m")
	if got := parseEnvDuration("TECHSTACK_TEST_DURATION_NEG"); got != 0 {
		t.Fatalf("expected 0 for negative duration, got %v", got)
	}
}

func TestManagedRuntimeTargetWaitConfig(t *testing.T) {
	tests := []struct {
		name, envTimeout, envInterval string
		config                        *ProvisionConfig
		wantTimeout, wantInterval     time.Duration
	}{
		{"environment override", "2m", "1s", &ProvisionConfig{}, 2 * time.Minute, time.Second},
		{"defaults", "", "", nil, 20 * time.Minute, 5 * time.Second},
		{"oversized values clamp", "30m", "20m", &ProvisionConfig{
			ManagedRuntimeTargetWaitTimeout:  25 * time.Minute,
			ManagedRuntimeTargetPollInterval: 2 * time.Minute,
		}, 20 * time.Minute, 20 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TECHSTACK_MANAGED_RUNTIME_WAIT_TIMEOUT", tt.envTimeout)
			t.Setenv("TECHSTACK_MANAGED_RUNTIME_POLL_INTERVAL", tt.envInterval)
			timeout, interval := managedRuntimeTargetWaitConfig(tt.config)
			if timeout != tt.wantTimeout || interval != tt.wantInterval {
				t.Fatalf("wait config = (%v, %v), want (%v, %v)", timeout, interval, tt.wantTimeout, tt.wantInterval)
			}
		})
	}
}

func TestManagedRuntimeTargetResolveAttemptTimeout_EnvAndClamp(t *testing.T) {
	t.Setenv("TECHSTACK_MANAGED_RUNTIME_RESOLVE_TIMEOUT", "")
	if got := managedRuntimeTargetResolveAttemptTimeout(5*time.Minute, time.Second); got != 30*time.Second {
		t.Fatalf("expected default attempt timeout independent of poll interval, got %v", got)
	}

	t.Setenv("TECHSTACK_MANAGED_RUNTIME_RESOLVE_TIMEOUT", "750ms")
	if got := managedRuntimeTargetResolveAttemptTimeout(5*time.Second, time.Second); got != 750*time.Millisecond {
		t.Fatalf("expected env attempt timeout, got %v", got)
	}

	t.Setenv("TECHSTACK_MANAGED_RUNTIME_RESOLVE_TIMEOUT", "30s")
	if got := managedRuntimeTargetResolveAttemptTimeout(500*time.Millisecond, time.Second); got != 500*time.Millisecond {
		t.Fatalf("expected attempt timeout to clamp to total timeout, got %v", got)
	}
}
