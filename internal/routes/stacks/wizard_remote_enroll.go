package stacks

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/trust"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/remoteguard"
	"github.com/kombifyio/techstack/pkg/specv2"
)

const (
	// remoteEnrollmentTimeout bounds one enrollment attempt; the queue adds a
	// small number of retryable re-runs on top.
	remoteEnrollmentTimeout = 20 * time.Minute
)

// startWizardRemoteEnrollment admits the durable connect-remote enrollment job
// into the orchestrator queue. The pairing mint already created the durable
// row; the queue owns execution, retries and progress, so a control-plane
// restart resumes a pending row through the boot re-enqueue instead of losing
// a process-local goroutine.
func (h wizardRunHandlers) startWizardRemoteEnrollment(
	e *httpx.Event,
	run *wizardRunState,
	stackID string,
	minted trust.MintedStackPairing,
	plannedServerID string,
) {
	if wizardRunTransport(run.request) != specv2.TransportConnectRemote {
		return
	}
	if minted.JobID == "" || h.cfg.Trust.Jobs == nil {
		return
	}
	ctx := context.Background()
	if e != nil && e.Request != nil {
		ctx = e.Request.Context()
	}
	if h.crud.orch == nil {
		h.failRemoteEnrollmentJob(ctx, run.tenantID, minted.JobID,
			errors.New("the orchestrator is not configured; remote enrollment cannot be executed"))
		return
	}
	plannedServerID = strings.TrimSpace(plannedServerID)
	// The recovery payload is non-secret by contract: identity plus the resume
	// marker only. Pairing tokens and SSH passwords never reach payload_json;
	// the executor resolves them from the durable row and wallet custody.
	payload := map[string]any{
		"tenant_id":                  run.tenantID,
		"owner_id":                   run.ownerID,
		"planned_server_id":          plannedServerID,
		"remote_enrollment_recovery": true,
	}
	result := map[string]any{}
	if plannedServerID != "" {
		result["planned_server_id"] = plannedServerID
	}
	job := &jobs.Job{
		ID:          minted.JobID,
		Type:        jobs.JobTypeRemoteEnrollment,
		TargetType:  "stack",
		TargetID:    stackID,
		Payload:     payload,
		Result:      result,
		MaxAttempts: 1,
	}
	if enqueueErr := h.crud.orch.EnqueuePreparedJob(job, run.tenantID); enqueueErr != nil {
		h.failRemoteEnrollmentJob(ctx, run.tenantID, minted.JobID, enqueueErr)
	}
}

func (h wizardRunHandlers) resolveRemoteEnrollmentServerID(ctx context.Context, tenantID, plannedServerID string) string {
	plannedServerID = strings.TrimSpace(plannedServerID)
	if plannedServerID == "" || h.crud.serverStore == nil {
		return plannedServerID
	}
	server, err := h.crud.serverStore.GetServerRuntime(ctx, tenantID, plannedServerID)
	if err != nil || server == nil {
		return plannedServerID
	}
	if strings.TrimSpace(server.WorkerID) != "" {
		return server.ID
	}
	return plannedServerID
}

func (h wizardRunHandlers) resolveWizardRemoteCredentials(ctx context.Context, run *wizardRunState, stackID string) (remoteguard.Credentials, error) {
	if run != nil && run.request.Remote != nil {
		if creds, err := h.credentialsFromRemoteRequest(ctx, run, run.request.Remote); err == nil {
			return creds, nil
		}
	}
	if h.crud.stackStore == nil || strings.TrimSpace(stackID) == "" {
		return remoteguard.Credentials{}, errors.New("remote SSH configuration is required for connect-remote")
	}
	stack, err := h.crud.stackStore.GetStack(ctx, run.tenantID, stackID)
	if err != nil {
		return remoteguard.Credentials{}, errors.New("persisted remote SSH connection is unavailable")
	}
	return h.credentialsFromStackConfig(ctx, run.tenantID, run.ownerID, stack)
}

func (h wizardRunHandlers) credentialsFromRemoteRequest(ctx context.Context, run *wizardRunState, remote *wizardRunRemoteParams) (remoteguard.Credentials, error) {
	if remote == nil {
		return remoteguard.Credentials{}, errors.New("remote SSH configuration is required for connect-remote")
	}
	host := strings.TrimSpace(remote.Host)
	user := strings.TrimSpace(remote.User)
	if host == "" || user == "" {
		return remoteguard.Credentials{}, errors.New("remote host and SSH user are required")
	}
	port := 22
	if remote.Port != nil && *remote.Port > 0 {
		port = *remote.Port
	}
	creds := remoteguard.Credentials{
		Host:     host,
		Port:     port,
		User:     user,
		UseSudo:  remote.UseSudo,
		Password: strings.TrimSpace(remote.Password),
	}
	authMethod := strings.ToLower(strings.TrimSpace(remote.AuthMethod))
	if authMethod == "password" {
		if creds.Password == "" {
			return remoteguard.Credentials{}, errors.New("SSH password is required for password authentication")
		}
		return creds, nil
	}
	label := strings.TrimSpace(remote.SSHKeyLabel)
	if label == "" {
		return remoteguard.Credentials{}, errors.New("SSH key label is required for key authentication")
	}
	key, err := h.resolveWalletSSHKey(ctx, run.tenantID, run.ownerID, label)
	if err != nil {
		return remoteguard.Credentials{}, err
	}
	creds.PrivateKey = key
	return creds, nil
}

func (h wizardRunHandlers) patchRemoteEnrollmentJob(ctx context.Context, tenantID, jobID string, patch map[string]any) {
	if h.cfg.Trust.Jobs == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	current, err := h.cfg.Trust.Jobs.GetJob(ctx, tenantID, jobID)
	if err != nil || current == nil {
		return
	}
	state := strings.TrimSpace(stringFromAny(patch["state"]))
	if state == "" {
		state = current.State
	}
	step := strings.TrimSpace(stringFromAny(patch["step"]))
	if step == "" {
		step = current.Step
	}
	message := strings.TrimSpace(stringFromAny(patch["message"]))
	if message == "" {
		message = current.Message
	}
	progress := current.Progress
	if value, ok := patch["progress"].(int); ok {
		progress = value
	}
	result := map[string]any{}
	for key, value := range current.Result {
		result[key] = value
	}
	if serverID := strings.TrimSpace(stringFromAny(patch["server_id"])); serverID != "" {
		result["server_id"] = serverID
	}
	if plannedServerID := strings.TrimSpace(stringFromAny(patch["planned_server_id"])); plannedServerID != "" {
		result[wizardRunPlannedServerID] = plannedServerID
	}
	req := controlplane.UpsertJobRequest{
		ID:       current.ID,
		TenantID: tenantID,
		StackID:  current.StackID,
		Type:     current.Type,
		State:    state,
		Progress: progress,
		Step:     step,
		Message:  message,
		Result:   result,
	}
	if errText := strings.TrimSpace(stringFromAny(patch["error"])); errText != "" {
		req.Error = errText
	}
	if details := strings.TrimSpace(stringFromAny(patch["error_details"])); details != "" {
		req.ErrorDetails = details
	}
	if reason := strings.TrimSpace(stringFromAny(patch["reason_code"])); reason != "" {
		result["reason_code"] = reason
	}
	if retryable, ok := patch["retryable"].(bool); ok {
		result["retryable"] = retryable
	}
	req.Result = result
	if _, err := h.cfg.Trust.Jobs.UpsertJob(ctx, req); err != nil {
		logger.Default().Warn("wizard_remote_enrollment_job_patch_failed", "job_id", jobID, "error", err)
	}
}

func (h wizardRunHandlers) failRemoteEnrollmentJob(ctx context.Context, tenantID, jobID string, err error) {
	reason, message, retryable := remoteguard.ClassifyEnrollmentError(err)
	h.patchRemoteEnrollmentJob(ctx, tenantID, jobID, map[string]any{
		"state":         "failed",
		"step":          remoteguard.StepConnectSSH,
		"message":       message,
		"error":         message,
		"error_details": err.Error(),
		"reason_code":   reason,
		"retryable":     retryable,
		"progress":      100,
	})
}

func resolveWizardControlPlaneOrigin(e *httpx.Event) string {
	if origin := strings.TrimSpace(config.PublicOriginFromEnv()); origin != "" {
		return origin
	}
	if e == nil || e.Request == nil {
		return ""
	}
	scheme := "https"
	if e.Request.TLS == nil {
		host := strings.ToLower(e.Request.Host)
		if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") {
			scheme = "http"
		}
	}
	return scheme + "://" + strings.TrimSpace(e.Request.Host)
}
